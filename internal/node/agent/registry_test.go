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
	restartErr error
	restarted  int
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

func (a *recordingAdapter) Restart(context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.restarted++
	if a.restartErr != nil {
		return a.restartErr
	}
	return nil
}

func (a *recordingAdapter) restartCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.restarted
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

// Empty kinds means "every adapter this node runs" -- explicit rather than a
// sentinel, so a caller who meant one specific kind is never one typo away
// from restarting the whole node.
func TestRestartAll_EmptyKindsRestartsEverything(t *testing.T) {
	alpha := &recordingAdapter{kind: "alpha"}
	beta := &recordingAdapter{kind: "beta"}
	r := MustRegistry(alpha, beta)

	outcomes := r.RestartAll(context.Background(), nil)
	if len(outcomes) != 2 {
		t.Fatalf("got %d outcomes, want 2", len(outcomes))
	}
	if alpha.restartCount() != 1 || beta.restartCount() != 1 {
		t.Errorf("alpha restarted %d times, beta %d times; want 1 each",
			alpha.restartCount(), beta.restartCount())
	}
}

// Named kinds restart only those, and this is where a partial fleet restart
// (a UI that lets an operator restart "just xray") depends on the filter
// actually filtering.
func TestRestartAll_NamedKindsRestartsOnlyThose(t *testing.T) {
	alpha := &recordingAdapter{kind: "alpha"}
	beta := &recordingAdapter{kind: "beta"}
	r := MustRegistry(alpha, beta)

	outcomes := r.RestartAll(context.Background(), []string{"alpha"})
	if len(outcomes) != 1 {
		t.Fatalf("got %d outcomes, want 1", len(outcomes))
	}
	if alpha.restartCount() != 1 {
		t.Errorf("alpha restarted %d times, want 1", alpha.restartCount())
	}
	if beta.restartCount() != 0 {
		t.Errorf("beta restarted %d times, want 0 -- it was not named", beta.restartCount())
	}
}

// One adapter's restart failing must not stop the others from being
// attempted, and the failure must be reported against the RIGHT kind -- an
// operator who restarted a mixed fleet needs to know which protocol is
// still down, not merely that something is.
func TestRestartAll_OneFailureDoesNotStopTheOthers(t *testing.T) {
	failing := &recordingAdapter{kind: "wireguard", restartErr: adapter.ErrRestartUnsupported}
	working := &recordingAdapter{kind: "xray"}
	r := MustRegistry(failing, working)

	outcomes := r.RestartAll(context.Background(), nil)
	if len(outcomes) != 2 {
		t.Fatalf("got %d outcomes, want 2", len(outcomes))
	}
	if working.restartCount() != 1 {
		t.Errorf("the working adapter was not restarted after the other one failed")
	}

	byKind := map[adapter.Kind]AdapterRestartOutcome{}
	for _, o := range outcomes {
		byKind[o.Kind] = o
	}
	if byKind["wireguard"].OK {
		t.Error("wireguard outcome reported OK despite ErrRestartUnsupported")
	}
	if !errors.Is(byKind["wireguard"].Err, adapter.ErrRestartUnsupported) {
		t.Errorf("wireguard error = %v, want ErrRestartUnsupported", byKind["wireguard"].Err)
	}
	if !byKind["xray"].OK {
		t.Errorf("xray outcome reported not-OK: %v", byKind["xray"].Err)
	}
}

// geoAdapter wraps recordingAdapter and additionally implements
// adapter.GeoDataUpdater, so tests can construct a mixed registry where
// only some adapters have geo data at all -- recordingAdapter alone (no
// UpdateGeoData method) stands in for the majority of protocols that
// genuinely have none.
type geoAdapter struct {
	*recordingAdapter
	mu      sync.Mutex
	calls   int
	err     error
	geoip   string
	geosite string
}

func (g *geoAdapter) UpdateGeoData(_ context.Context, geoipURL, _, geositeURL, _ string) (adapter.GeoDataResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	if g.err != nil {
		return adapter.GeoDataResult{}, g.err
	}
	return adapter.GeoDataResult{GeoIPSHA256: g.geoip, GeoSiteSHA256: g.geosite}, nil
}

func (g *geoAdapter) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

var _ adapter.GeoDataUpdater = (*geoAdapter)(nil)

// TestUpdateGeoData_OnlyTouchesCapableAdapters is the design's central
// property: an adapter with no geo-data concept at all (wireguard, l2tp,
// hysteria2, ocserv, openvpn in production) must not appear in the result
// just because it happened to share a registry with xray.
func TestUpdateGeoData_OnlyTouchesCapableAdapters(t *testing.T) {
	xray := &geoAdapter{recordingAdapter: &recordingAdapter{kind: "xray"}, geoip: "aaa", geosite: "bbb"}
	wireguard := &recordingAdapter{kind: "wireguard"} // no UpdateGeoData method at all
	r := MustRegistry(xray, wireguard)

	outcomes := r.UpdateGeoData(context.Background(), "geoip-url", "geoip-sum-url", "geosite-url", "geosite-sum-url")
	if len(outcomes) != 1 {
		t.Fatalf("got %d outcomes, want exactly 1 (xray only): %+v", len(outcomes), outcomes)
	}
	if outcomes[0].Kind != "xray" {
		t.Errorf("outcome kind = %q, want xray", outcomes[0].Kind)
	}
	if !outcomes[0].OK {
		t.Errorf("outcome not OK: %v", outcomes[0].Err)
	}
	if outcomes[0].GeoIPSHA256 != "aaa" || outcomes[0].GeoSiteSHA256 != "bbb" {
		t.Errorf("checksums not carried through: %+v", outcomes[0])
	}
	if xray.callCount() != 1 {
		t.Errorf("xray.UpdateGeoData called %d times, want 1", xray.callCount())
	}
}

// TestUpdateGeoData_NoCapableAdaptersReturnsEmpty proves the "nothing to
// update" case is a plain empty slice rather than a synthesized error or a
// row for an adapter that was never asked -- httpapi is what turns an empty
// result into an operator-facing message, not this layer.
func TestUpdateGeoData_NoCapableAdaptersReturnsEmpty(t *testing.T) {
	r := MustRegistry(&recordingAdapter{kind: "wireguard"}, &recordingAdapter{kind: "openvpn"})

	outcomes := r.UpdateGeoData(context.Background(), "a", "b", "c", "d")
	if len(outcomes) != 0 {
		t.Errorf("got %d outcomes, want 0: %+v", len(outcomes), outcomes)
	}
}

func TestUpdateGeoData_FailurePropagatesWithoutStoppingOthers(t *testing.T) {
	failing := &geoAdapter{recordingAdapter: &recordingAdapter{kind: "xray"}, err: errors.New("checksum mismatch")}
	working := &geoAdapter{recordingAdapter: &recordingAdapter{kind: "singbox"}, geoip: "x", geosite: "y"}
	r := MustRegistry(failing, working)

	outcomes := r.UpdateGeoData(context.Background(), "a", "b", "c", "d")
	if len(outcomes) != 2 {
		t.Fatalf("got %d outcomes, want 2", len(outcomes))
	}
	byKind := map[adapter.Kind]AdapterGeoUpdateOutcome{}
	for _, o := range outcomes {
		byKind[o.Kind] = o
	}
	if byKind["xray"].OK {
		t.Error("xray outcome reported OK despite the update failing")
	}
	if !byKind["singbox"].OK {
		t.Errorf("singbox outcome reported not-OK: %v", byKind["singbox"].Err)
	}
}
