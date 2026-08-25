package agent

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/node/adapter"
	"github.com/amyrm/antimage/internal/node/adapter/stub"
)

func desired(revision int64, ports ...int) adapter.Desired {
	d := adapter.Desired{SchemaVersion: 1, Revision: revision, NodeID: 1}
	for i, p := range ports {
		d.Services = append(d.Services, adapter.Service{
			ID: int64(10 + i), Kind: "stub", Enabled: true,
			Params: json.RawMessage(`{"port":` + itoa(p) + `}`),
		})
	}
	return d
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func newReconciler(t *testing.T) (*Reconciler, *FakeClock) {
	t.Helper()
	clk := NewFakeClock(time.Unix(1_700_000_000, 0).UTC())
	r := NewReconciler(MustRegistry(stub.New(t.TempDir())), clk, ReconcileOptions{
		MaxRetries: 3, RetryBase: time.Second,
	})
	return r, clk
}

func TestConvergeAppliesAndConfirms(t *testing.T) {
	r, _ := newReconciler(t)
	run, err := r.Converge(context.Background(), desired(1, 443))
	if err != nil {
		t.Fatalf("Converge: %v", err)
	}
	if !run.Converged {
		t.Fatalf("Converged = false; steps=%+v err=%s", run.Steps, run.Err)
	}
	if run.TargetRevision != 1 {
		t.Errorf("TargetRevision = %d, want 1", run.TargetRevision)
	}
	if len(run.Steps) == 0 {
		t.Error("no steps recorded for a change")
	}
}

func TestConvergeOnAlreadyConvergedStateIsANoOp(t *testing.T) {
	r, _ := newReconciler(t)
	ctx := context.Background()
	d := desired(1, 443)
	if _, err := r.Converge(ctx, d); err != nil {
		t.Fatalf("first Converge: %v", err)
	}
	run, err := r.Converge(ctx, d)
	if err != nil {
		t.Fatalf("second Converge: %v", err)
	}
	if !run.Converged {
		t.Error("Converged = false on an already-converged node")
	}
	if len(run.Steps) != 0 {
		t.Errorf("second run applied %d steps, want 0", len(run.Steps))
	}
}

// THE property test. For arbitrary desired states, applying a plan and
// re-planning must yield an empty plan. If this breaks, nodes reconcile
// forever and the whole architecture fails quietly.
func TestConvergenceIsIdempotentForArbitraryDesiredStates(t *testing.T) {
	rng := rand.New(rand.NewSource(20260813))
	for trial := 0; trial < 200; trial++ {
		r, _ := newReconciler(t)
		ctx := context.Background()

		n := rng.Intn(5)
		ports := make([]int, n)
		for i := range ports {
			ports[i] = 1024 + rng.Intn(60000)
		}
		d := desired(int64(trial+1), ports...)

		run, err := r.Converge(ctx, d)
		if err != nil {
			t.Fatalf("trial %d Converge: %v", trial, err)
		}
		if !run.Converged {
			t.Fatalf("trial %d did not converge: %+v %s", trial, run.Steps, run.Err)
		}

		// Re-planning must find nothing to do.
		again, err := r.Converge(ctx, d)
		if err != nil {
			t.Fatalf("trial %d re-Converge: %v", trial, err)
		}
		if len(again.Steps) != 0 {
			t.Fatalf("trial %d: re-plan produced %d steps, want 0 — reconciliation does not settle",
				trial, len(again.Steps))
		}
	}
}

// Transitions between arbitrary states must also settle.
func TestConvergenceSettlesAcrossTransitions(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	r, _ := newReconciler(t)
	ctx := context.Background()

	for step := 0; step < 100; step++ {
		n := rng.Intn(4)
		ports := make([]int, n)
		for i := range ports {
			ports[i] = 1024 + rng.Intn(60000)
		}
		d := desired(int64(step+1), ports...)

		if _, err := r.Converge(ctx, d); err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
		again, err := r.Converge(ctx, d)
		if err != nil {
			t.Fatalf("step %d recheck: %v", step, err)
		}
		if len(again.Steps) != 0 {
			t.Fatalf("step %d left %d steps outstanding", step, len(again.Steps))
		}
	}
}

// Uses SystemClock rather than FakeClock: the reconciler blocks on
// Clock.After between retries, and nothing in this test ever calls
// FakeClock.Advance. With FakeClock that wait never fires and the test hangs
// instead of failing. RetryBase is 1ms, so real backoff (1ms + 2ms) is
// negligible with SystemClock.
func TestFailingStepRetriesThenReportsDegraded(t *testing.T) {
	fa := &flakyAdapter{failEvery: true}
	r := NewReconciler(MustRegistry(fa), SystemClock{}, ReconcileOptions{MaxRetries: 3, RetryBase: time.Millisecond})

	run, err := r.Converge(context.Background(), desired(1, 443))
	if err == nil {
		t.Fatal("Converge returned nil error for a permanently failing step")
	}
	if run.Converged {
		t.Error("Converged = true despite a failing step")
	}
	if fa.applyCalls != 3 {
		t.Errorf("Apply called %d times, want MaxRetries=3", fa.applyCalls)
	}
	if run.Err == "" {
		t.Error("Run.Err is empty; the underlying failure must surface in the UI")
	}
}

func TestDisruptiveStepsDeferOutsideMaintenanceWindow(t *testing.T) {
	clk := NewFakeClock(time.Unix(1_700_000_000, 0).UTC())
	r := NewReconciler(MustRegistry(stub.New(t.TempDir())), clk, ReconcileOptions{
		MaxRetries: 3,
		RetryBase:  time.Millisecond,
		// Window closed: no disruptive step may run.
		AllowDisruptive: func(time.Time) bool { return false },
	})

	run, err := r.Converge(context.Background(), desired(1, 443))
	if err != nil {
		t.Fatalf("Converge: %v", err)
	}
	if !run.Deferred {
		t.Fatal("Deferred = false; a restart-class step outside the window must be deferred")
	}
	if run.Converged {
		t.Error("Converged = true although work was deferred")
	}
	if len(run.Steps) != 0 {
		t.Errorf("applied %d steps with the window closed, want 0", len(run.Steps))
	}
}

func TestNonDisruptiveStepsRunEvenWithWindowClosed(t *testing.T) {
	clk := NewFakeClock(time.Unix(1_700_000_000, 0).UTC())
	hot := &hotOnlyAdapter{}
	r := NewReconciler(MustRegistry(hot), clk, ReconcileOptions{
		MaxRetries: 3, RetryBase: time.Millisecond,
		AllowDisruptive: func(time.Time) bool { return false },
	})
	run, err := r.Converge(context.Background(), desired(1, 443))
	if err != nil {
		t.Fatalf("Converge: %v", err)
	}
	if run.Deferred {
		t.Error("Deferred = true for a plan containing only DisruptNone steps")
	}
	if len(run.Steps) == 0 {
		t.Error("hot steps were not applied; user disables must never wait for a window")
	}
}

// Discriminates a Converge that trusts Apply's success instead of
// re-observing and re-planning to confirm. staggeredAdapter needs two
// separate Apply rounds to fully converge (its second required step is only
// revealed by a fresh Plan call after the first is applied), so a correct
// Converge must report Converged = false after applying just the first step,
// with the outstanding step surfaced via Run.Err. Task 21 gates
// applied_revision on this flag, so a reconciler that lies here would let
// the panel believe a partially-applied revision fully landed.
func TestConvergedRequiresConfirmationPlanToBeEmpty(t *testing.T) {
	clk := NewFakeClock(time.Unix(1_700_000_000, 0).UTC())
	sa := &staggeredAdapter{}
	r := NewReconciler(MustRegistry(sa), clk, ReconcileOptions{MaxRetries: 3, RetryBase: time.Millisecond})

	run, err := r.Converge(context.Background(), desired(1, 443))
	if err != nil {
		t.Fatalf("Converge: %v", err)
	}
	if run.Converged {
		t.Fatal("Converged = true after one round, but a fresh Plan still finds outstanding work — " +
			"Converge must re-observe and re-plan to confirm rather than trust that Apply succeeded")
	}
	if len(run.Steps) != 1 {
		t.Fatalf("applied %d steps, want 1", len(run.Steps))
	}
	if run.Err == "" {
		t.Error("Run.Err is empty although the confirmation plan found outstanding work")
	}
	if sa.rounds != 1 {
		t.Fatalf("adapter rounds = %d, want 1 — the confirmation pass must Plan, not Apply", sa.rounds)
	}
}

// --- test doubles ---

type flakyAdapter struct {
	failEvery  bool
	applyCalls int
}

func (f *flakyAdapter) Descriptor() adapter.Descriptor {
	return adapter.Descriptor{Kind: "flaky", Version: "1"}
}
func (f *flakyAdapter) Observe(context.Context) (adapter.Observed, error) {
	return adapter.Observed{}, nil
}
func (f *flakyAdapter) Plan(_ context.Context, d adapter.Desired, _ adapter.Observed) (adapter.Plan, error) {
	return adapter.Plan{Steps: []adapter.Step{{Seq: 1, Kind: "boom", Disruption: adapter.DisruptNone}}}, nil
}
func (f *flakyAdapter) Apply(context.Context, adapter.Step) (adapter.StepResult, error) {
	f.applyCalls++
	err := errors.New("simulated apply failure")
	return adapter.StepResult{Seq: 1, OK: false, Err: err.Error()}, err
}
func (f *flakyAdapter) Probe(context.Context) (adapter.Health, error) {
	return adapter.Health{OK: true}, nil
}

type hotOnlyAdapter struct{ applied bool }

func (h *hotOnlyAdapter) Descriptor() adapter.Descriptor {
	return adapter.Descriptor{Kind: "hot", Version: "1", Caps: adapter.Caps{HotUserAdd: true}}
}
func (h *hotOnlyAdapter) Observe(context.Context) (adapter.Observed, error) {
	return adapter.Observed{}, nil
}
func (h *hotOnlyAdapter) Plan(context.Context, adapter.Desired, adapter.Observed) (adapter.Plan, error) {
	if h.applied {
		return adapter.Plan{}, nil
	}
	return adapter.Plan{Steps: []adapter.Step{{Seq: 1, Kind: "hot_add", Disruption: adapter.DisruptNone}}}, nil
}
func (h *hotOnlyAdapter) Apply(context.Context, adapter.Step) (adapter.StepResult, error) {
	h.applied = true
	return adapter.StepResult{Seq: 1, OK: true}, nil
}
func (h *hotOnlyAdapter) Probe(context.Context) (adapter.Health, error) {
	return adapter.Health{OK: true}, nil
}

// staggeredAdapter needs two Apply rounds to converge: Plan keeps reporting
// one outstanding step until two rounds have been applied. This models a
// dependency a single Plan call cannot see all at once, and exists purely to
// prove that Converge's confirmation pass is load-bearing.
type staggeredAdapter struct{ rounds int }

func (s *staggeredAdapter) Descriptor() adapter.Descriptor {
	return adapter.Descriptor{Kind: "staggered", Version: "1"}
}
func (s *staggeredAdapter) Observe(context.Context) (adapter.Observed, error) {
	return adapter.Observed{}, nil
}
func (s *staggeredAdapter) Plan(context.Context, adapter.Desired, adapter.Observed) (adapter.Plan, error) {
	if s.rounds >= 2 {
		return adapter.Plan{}, nil
	}
	return adapter.Plan{Steps: []adapter.Step{{Seq: 1, Kind: "step", Disruption: adapter.DisruptNone}}}, nil
}
func (s *staggeredAdapter) Apply(context.Context, adapter.Step) (adapter.StepResult, error) {
	s.rounds++
	return adapter.StepResult{Seq: 1, OK: true}, nil
}
func (s *staggeredAdapter) Probe(context.Context) (adapter.Health, error) {
	return adapter.Health{OK: true}, nil
}
