package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// recordingAdapter is a minimal adapter that reports what it was shown.
//
// The point of most of these tests is what an adapter is NOT shown: isolation
// between adapters is structural in the Reconciler, so the way to prove it is
// to have each adapter record its inputs and then assert on them.
type recordingAdapter struct {
	kind string

	mu sync.Mutex
	// observedCalls counts Observe, so the confirmation pass is visible.
	observedCalls int
	// sawServices records the service ids handed to Plan, per call.
	sawServices [][]int64
	// sawObserved records the observed service ids handed to Plan, per call.
	sawObserved [][]int64
	// applied records the steps this adapter was asked to execute.
	applied []adapter.Step

	// own is what Observe reports as already present on the host.
	own []adapter.ObservedService
	// plan is returned by Plan until satisfied is set.
	plan []adapter.Step
	// satisfied makes Plan return empty, standing in for convergence.
	satisfied bool

	observeErr error
	planErr    error
	applyErr   string
	probe      adapter.Health
}

func (a *recordingAdapter) Descriptor() adapter.Descriptor {
	return adapter.Descriptor{Kind: adapter.Kind(a.kind), Version: "1"}
}

func (a *recordingAdapter) Observe(context.Context) (adapter.Observed, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.observedCalls++
	if a.observeErr != nil {
		return adapter.Observed{}, a.observeErr
	}
	return adapter.Observed{Services: a.own}, nil
}

func (a *recordingAdapter) Plan(
	_ context.Context, desired adapter.Desired, observed adapter.Observed,
) (adapter.Plan, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	desiredIDs := make([]int64, 0, len(desired.Services))
	for _, s := range desired.Services {
		desiredIDs = append(desiredIDs, s.ID)
	}
	observedIDs := make([]int64, 0, len(observed.Services))
	for _, s := range observed.Services {
		observedIDs = append(observedIDs, s.ID)
	}
	a.sawServices = append(a.sawServices, desiredIDs)
	a.sawObserved = append(a.sawObserved, observedIDs)

	if a.planErr != nil {
		return adapter.Plan{}, a.planErr
	}
	if a.satisfied {
		return adapter.Plan{}, nil
	}
	return adapter.Plan{Steps: a.plan}, nil
}

func (a *recordingAdapter) Apply(_ context.Context, step adapter.Step) (adapter.StepResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.applied = append(a.applied, step)
	if a.applyErr != "" {
		return adapter.StepResult{OK: false, Err: a.applyErr}, errors.New(a.applyErr)
	}
	// Applying satisfies this adapter, so the confirmation plan comes back
	// empty and the run converges.
	a.satisfied = true
	return adapter.StepResult{OK: true}, nil
}

func (a *recordingAdapter) Probe(context.Context) (adapter.Health, error) {
	return a.probe, nil
}

func (a *recordingAdapter) snapshot() ([][]int64, [][]int64, []adapter.Step, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sawServices, a.sawObserved, a.applied, a.observedCalls
}

func step(kind string, id int64, d adapter.Disruption) adapter.Step {
	return adapter.Step{Kind: kind, ServiceID: id, Disruption: d}
}

func multiDesired() adapter.Desired {
	return adapter.Desired{
		SchemaVersion: 1, Revision: 7, NodeID: 1,
		Services: []adapter.Service{
			{ID: 1, Kind: "alpha", Enabled: true},
			{ID: 2, Kind: "beta", Enabled: true},
		},
	}
}

// ---------------------------------------------------------------- Registry

func TestRegistryRefusesDuplicateKinds(t *testing.T) {
	_, err := NewRegistry(&recordingAdapter{kind: "alpha"}, &recordingAdapter{kind: "alpha"})
	if err == nil {
		t.Fatal("two adapters of the same kind were accepted; they would manage the " +
			"same files and overwrite each other on every pass")
	}
	if !strings.Contains(err.Error(), "alpha") {
		t.Errorf("error does not name the duplicated kind: %v", err)
	}
}

func TestRegistryRefusesNilAdapter(t *testing.T) {
	if _, err := NewRegistry(&recordingAdapter{kind: "alpha"}, nil); err == nil {
		t.Fatal("a nil adapter was accepted, and would panic on first use")
	}
}

// Order is preserved because it fixes step order in an apply run, and a plan
// that is not deterministic cannot be compared between passes.
func TestRegistryPreservesOrder(t *testing.T) {
	r := MustRegistry(
		&recordingAdapter{kind: "gamma"},
		&recordingAdapter{kind: "alpha"},
		&recordingAdapter{kind: "beta"},
	)
	var got []string
	for _, d := range r.Descriptors() {
		got = append(got, string(d.Kind))
	}
	want := []string{"gamma", "alpha", "beta"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("descriptors = %v, want %v (registration order)", got, want)
		}
	}
}

// Every adapter is probed, and one failure does not suppress the others: an
// operator needs to know WHICH protocol is down.
func TestRegistryProbesEveryAdapter(t *testing.T) {
	up := &recordingAdapter{kind: "alpha", probe: adapter.Health{OK: true}}
	down := &recordingAdapter{kind: "beta", probe: adapter.Health{OK: false, Detail: "unit failed"}}
	r := MustRegistry(up, down)

	got := r.Probe(context.Background())
	if len(got) != 2 {
		t.Fatalf("probed %d adapters, want 2", len(got))
	}
	if got[0].Kind != "alpha" || !got[0].Health.OK {
		t.Errorf("alpha reported %+v", got[0])
	}
	if got[1].Kind != "beta" || got[1].Health.OK {
		t.Errorf("beta reported %+v, want unhealthy", got[1])
	}
	if got[1].Health.Detail != "unit failed" {
		t.Errorf("beta detail = %q, want the adapter's own reason", got[1].Health.Detail)
	}
}

// ------------------------------------------------------- Reconciler fan-out

// THE isolation guarantee.
//
// adapter.ObservedService carries no Kind, so an adapter shown another's
// observations cannot tell them apart from its own -- and every adapter's
// removal pass plans a removal for observed services it does not recognise.
// Merging observations would have one adapter plan the destruction of
// another's work and mean it.
func TestEachAdapterSeesOnlyItsOwnObservations(t *testing.T) {
	alpha := &recordingAdapter{
		kind: "alpha",
		own:  []adapter.ObservedService{{ID: 1, Present: true, Managed: true}},
	}
	beta := &recordingAdapter{
		kind: "beta",
		own:  []adapter.ObservedService{{ID: 2, Present: true, Managed: true}},
	}
	r := NewReconciler(MustRegistry(alpha, beta), NewFakeClock(time.Unix(1, 0)),
		ReconcileOptions{MaxRetries: 1, RetryBase: time.Millisecond})

	if _, err := r.Converge(context.Background(), multiDesired()); err != nil {
		t.Fatalf("Converge: %v", err)
	}

	_, alphaObserved, _, _ := alpha.snapshot()
	_, betaObserved, _, _ := beta.snapshot()

	for _, seen := range alphaObserved {
		for _, id := range seen {
			if id == 2 {
				t.Errorf("alpha was shown beta's observed service %d; its removal "+
					"pass would plan to destroy it", id)
			}
		}
	}
	for _, seen := range betaObserved {
		for _, id := range seen {
			if id == 1 {
				t.Errorf("beta was shown alpha's observed service %d", id)
			}
		}
	}
}

// The desired document, by contrast, IS shared: every adapter reads the whole
// thing and filters by Kind itself. That is the contract the adapters
// implement, and breaking it here would break every adapter's filter.
func TestEveryAdapterSeesTheWholeDesiredDocument(t *testing.T) {
	alpha := &recordingAdapter{kind: "alpha"}
	beta := &recordingAdapter{kind: "beta"}
	r := NewReconciler(MustRegistry(alpha, beta), NewFakeClock(time.Unix(1, 0)),
		ReconcileOptions{MaxRetries: 1, RetryBase: time.Millisecond})

	if _, err := r.Converge(context.Background(), multiDesired()); err != nil {
		t.Fatalf("Converge: %v", err)
	}

	for name, ad := range map[string]*recordingAdapter{"alpha": alpha, "beta": beta} {
		seen, _, _, _ := ad.snapshot()
		if len(seen) == 0 {
			t.Fatalf("%s was never asked to plan", name)
		}
		if len(seen[0]) != 2 {
			t.Errorf("%s saw %d desired services, want both: %v", name, len(seen[0]), seen[0])
		}
	}
}

// A step is executed by the adapter that planned it, never by another.
func TestStepsAreDispatchedToTheAdapterThatPlannedThem(t *testing.T) {
	alpha := &recordingAdapter{kind: "alpha", plan: []adapter.Step{step("install", 1, adapter.DisruptNone)}}
	beta := &recordingAdapter{kind: "beta", plan: []adapter.Step{step("reload", 2, adapter.DisruptNone)}}
	r := NewReconciler(MustRegistry(alpha, beta), NewFakeClock(time.Unix(1, 0)),
		ReconcileOptions{MaxRetries: 1, RetryBase: time.Millisecond})

	run, err := r.Converge(context.Background(), multiDesired())
	if err != nil {
		t.Fatalf("Converge: %v", err)
	}
	if !run.Converged {
		t.Fatalf("run did not converge: %s", run.Err)
	}

	_, _, alphaApplied, _ := alpha.snapshot()
	_, _, betaApplied, _ := beta.snapshot()
	if len(alphaApplied) != 1 || alphaApplied[0].ServiceID != 1 {
		t.Errorf("alpha applied %+v, want only its own service 1", alphaApplied)
	}
	if len(betaApplied) != 1 || betaApplied[0].ServiceID != 2 {
		t.Errorf("beta applied %+v, want only its own service 2", betaApplied)
	}
}

// Steps are numbered across the whole run, not per adapter.
//
// Seq is the panel's only ordering for node_apply_steps, and no adapter can
// number steps it does not know about. Most never set it at all, so before
// this every step in a run reported as step 0.
func TestStepSequenceIsGlobalAcrossAdapters(t *testing.T) {
	alpha := &recordingAdapter{kind: "alpha", plan: []adapter.Step{
		step("install", 1, adapter.DisruptNone),
		step("reload", 1, adapter.DisruptNone),
	}}
	beta := &recordingAdapter{kind: "beta", plan: []adapter.Step{
		step("install", 2, adapter.DisruptNone),
	}}
	r := NewReconciler(MustRegistry(alpha, beta), NewFakeClock(time.Unix(1, 0)),
		ReconcileOptions{MaxRetries: 1, RetryBase: time.Millisecond})

	run, err := r.Converge(context.Background(), multiDesired())
	if err != nil {
		t.Fatalf("Converge: %v", err)
	}
	if len(run.Steps) != 3 {
		t.Fatalf("run has %d steps, want 3", len(run.Steps))
	}
	for i, s := range run.Steps {
		if s.Seq != i {
			t.Errorf("step %d reported Seq %d; sequence numbers must be unique "+
				"and ordered across the whole run", i, s.Seq)
		}
	}
}

// One adapter failing must not stop the others from reconciling. A node
// serving four protocols should not abandon three because the fourth's binary
// is missing.
func TestOneAdapterFailingDoesNotStopTheRest(t *testing.T) {
	broken := &recordingAdapter{kind: "alpha", observeErr: errors.New("binary not found")}
	healthy := &recordingAdapter{kind: "beta", plan: []adapter.Step{step("install", 2, adapter.DisruptNone)}}
	r := NewReconciler(MustRegistry(broken, healthy), NewFakeClock(time.Unix(1, 0)),
		ReconcileOptions{MaxRetries: 1, RetryBase: time.Millisecond})

	run, err := r.Converge(context.Background(), multiDesired())
	if err == nil {
		t.Fatal("a failed Observe must surface as an error")
	}
	if run.Converged {
		t.Error("Converged = true although one adapter could not be observed")
	}
	if _, _, applied, _ := healthy.snapshot(); len(applied) != 1 {
		t.Errorf("the healthy adapter applied %d steps, want 1; one adapter's "+
			"failure stopped another's work", len(applied))
	}
	if !strings.Contains(run.Err, "alpha") || !strings.Contains(run.Err, "binary not found") {
		t.Errorf("Run.Err does not name the failing adapter and reason: %q", run.Err)
	}
}

// Every failure is reported, not just the last. The one an operator needs is
// rarely the last.
func TestAllAdapterFailuresAreReported(t *testing.T) {
	a := &recordingAdapter{kind: "alpha", observeErr: errors.New("alpha is down")}
	b := &recordingAdapter{kind: "beta", planErr: errors.New("beta cannot plan")}
	r := NewReconciler(MustRegistry(a, b), NewFakeClock(time.Unix(1, 0)),
		ReconcileOptions{MaxRetries: 1, RetryBase: time.Millisecond})

	run, _ := r.Converge(context.Background(), multiDesired())
	for _, want := range []string{"alpha is down", "beta cannot plan"} {
		if !strings.Contains(run.Err, want) {
			t.Errorf("Run.Err = %q, missing %q", run.Err, want)
		}
	}
}

// Convergence is the node's, not one adapter's: the panel advances
// applied_revision on it, so one adapter still holding work must keep the
// whole run unconverged.
func TestRunConvergesOnlyWhenEveryAdapterDoes(t *testing.T) {
	settled := &recordingAdapter{kind: "alpha", satisfied: true}
	outstanding := &recordingAdapter{kind: "beta"}
	// Applying does not satisfy this one: its confirmation plan still has work.
	outstanding.plan = []adapter.Step{step("install", 2, adapter.DisruptNone)}
	outstanding.applyErr = ""

	r := NewReconciler(MustRegistry(settled, outstanding), NewFakeClock(time.Unix(1, 0)),
		ReconcileOptions{MaxRetries: 1, RetryBase: time.Millisecond})
	run, err := r.Converge(context.Background(), multiDesired())
	if err != nil {
		t.Fatalf("Converge: %v", err)
	}
	if !run.Converged {
		t.Fatalf("both adapters settled but the run did not converge: %s", run.Err)
	}

	// Now one that never settles.
	stuck := &recordingAdapter{kind: "beta", plan: []adapter.Step{step("install", 2, adapter.DisruptNone)}}
	stuck.applyErr = ""
	stuckR := NewReconciler(
		MustRegistry(&recordingAdapter{kind: "alpha", satisfied: true}, &neverSettles{recordingAdapter{
			kind: "beta", plan: []adapter.Step{step("install", 2, adapter.DisruptNone)},
		}}),
		NewFakeClock(time.Unix(1, 0)),
		ReconcileOptions{MaxRetries: 1, RetryBase: time.Millisecond})

	run2, err := stuckR.Converge(context.Background(), multiDesired())
	if err != nil {
		t.Fatalf("Converge: %v", err)
	}
	if run2.Converged {
		t.Error("Converged = true although one adapter's confirmation plan still " +
			"held work; the panel would advance applied_revision on a node that " +
			"has not finished")
	}
}

// neverSettles keeps planning work no matter how often it is applied.
type neverSettles struct{ recordingAdapter }

func (n *neverSettles) Apply(_ context.Context, step adapter.Step) (adapter.StepResult, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.applied = append(n.applied, step)
	return adapter.StepResult{OK: true}, nil // never sets satisfied
}

// A confirmation plan that still holds work is NOT a Go error. The work was
// done and the node is simply not there yet; turning that into an error would
// make an in-progress node indistinguishable from a broken one.
func TestOutstandingWorkIsNotAReturnedError(t *testing.T) {
	r := NewReconciler(
		MustRegistry(&neverSettles{recordingAdapter{
			kind: "alpha", plan: []adapter.Step{step("install", 1, adapter.DisruptNone)},
		}}),
		NewFakeClock(time.Unix(1, 0)),
		ReconcileOptions{MaxRetries: 1, RetryBase: time.Millisecond})

	run, err := r.Converge(context.Background(), multiDesired())
	if err != nil {
		t.Fatalf("outstanding work was returned as an error: %v", err)
	}
	if run.Converged {
		t.Error("Converged = true although work remains")
	}
	if run.Err == "" {
		t.Error("Run.Err is empty although the confirmation plan found work")
	}
}

// Deferral is per adapter: a hot change on one protocol must not wait for a
// maintenance window because a different protocol wants to restart.
func TestDeferralIsPerAdapter(t *testing.T) {
	hot := &recordingAdapter{kind: "alpha", plan: []adapter.Step{step("add_user", 1, adapter.DisruptNone)}}
	disruptive := &recordingAdapter{kind: "beta", plan: []adapter.Step{step("restart", 2, adapter.DisruptRestart)}}

	r := NewReconciler(MustRegistry(hot, disruptive), NewFakeClock(time.Unix(1, 0)),
		ReconcileOptions{
			MaxRetries: 1, RetryBase: time.Millisecond,
			AllowDisruptive: func(time.Time) bool { return false },
		})

	run, err := r.Converge(context.Background(), multiDesired())
	if err != nil {
		t.Fatalf("Converge: %v", err)
	}
	if !run.Deferred {
		t.Error("Deferred = false although a restart-class plan was held back")
	}
	if _, _, hotApplied, _ := hot.snapshot(); len(hotApplied) != 1 {
		t.Errorf("the hot adapter applied %d steps; a user change must not wait "+
			"for a window because another protocol wants to restart", len(hotApplied))
	}
	if _, _, coldApplied, _ := disruptive.snapshot(); len(coldApplied) != 0 {
		t.Errorf("the restart-class adapter applied %d steps outside its window", len(coldApplied))
	}
}

// Usage is collected from EVERY self-accounting adapter. Taking only the first
// would silently drop the rest of the node's traffic, and the totals would
// still look plausible.
func TestUsageReportersFindsEveryAccountingAdapter(t *testing.T) {
	plain := &recordingAdapter{kind: "alpha"}
	accounting := &countingAdapter{recordingAdapter{kind: "beta"}}
	r := MustRegistry(plain, accounting)

	got := r.UsageReporters()
	if len(got) != 1 {
		t.Fatalf("found %d usage reporters, want 1", len(got))
	}

	both := MustRegistry(
		&countingAdapter{recordingAdapter{kind: "alpha"}},
		&countingAdapter{recordingAdapter{kind: "beta"}},
	)
	if n := len(both.UsageReporters()); n != 2 {
		t.Errorf("found %d usage reporters on a node where both account for "+
			"themselves, want 2; one protocol's traffic would go unreported", n)
	}
}

type countingAdapter struct{ recordingAdapter }

func (c *countingAdapter) Usage(context.Context) ([]adapter.UsageSample, error) {
	return []adapter.UsageSample{{SubjectID: 1, UplinkBytes: 10, DownlinkBytes: 20}}, nil
}
