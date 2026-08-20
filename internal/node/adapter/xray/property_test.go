package xray

import (
	"context"
	"math/rand"
	"testing"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// The convergence property test in internal/node/agent runs against the stub
// adapter. This is the same property against the real one, over randomised
// sequences of membership changes.
//
// Both defects this adapter has had were in TRANSITIONS rather than in any
// single state: revocation classified as a hot change, and a write whose
// restart failed reported as converged. Hand-written cases each pin one
// transition; a random walk covers the combinations nobody thought to write
// down.
//
// Two invariants are asserted after every step:
//
//   - applying a plan and re-planning yields nothing, or the node reconciles
//     forever;
//   - any step that drops a user is restart-class, or the revoked user stays
//     connected to a process that was never told about them.
func TestConvergenceAndRevocationHoldOverRandomSequences(t *testing.T) {
	rng := rand.New(rand.NewSource(20260819))
	ctx := context.Background()

	for trial := 0; trial < 200; trial++ {
		a, rt, _ := newAdapter(t, true) // hot add supported: the risky configuration

		prev := 0
		for step := 0; step < 6; step++ {
			// 1..4 users; the inbound itself never changes, so every difference
			// is a membership change and the hot path is always a candidate.
			n := 1 + rng.Intn(4)
			d := desiredWith(n, 10, tlsParams)

			obs, err := a.Observe(ctx)
			if err != nil {
				t.Fatalf("trial %d step %d: Observe: %v", trial, step, err)
			}
			plan, err := a.Plan(ctx, d, obs)
			if err != nil {
				t.Fatalf("trial %d step %d: Plan: %v", trial, step, err)
			}

			// A shrinking user set is a revocation and must not be cheap.
			if n < prev && plan.MaxDisruption() < adapter.DisruptRestart {
				t.Fatalf("trial %d step %d: %d -> %d users planned as %v; the revoked "+
					"user would stay connected", trial, step, prev, n, plan.MaxDisruption())
			}
			// A growing set on a hot-capable runtime should stay cheap, or the
			// capability is pointless.
			if n > prev && prev > 0 && plan.MaxDisruption() >= adapter.DisruptRestart {
				t.Fatalf("trial %d step %d: %d -> %d users planned as %v; a pure "+
					"addition should not drop sessions", trial, step, prev, n,
					plan.MaxDisruption())
			}

			restartsBefore, _, _, _ := rt.counts()
			for _, s := range plan.Steps {
				if _, err := a.Apply(ctx, s); err != nil {
					t.Fatalf("trial %d step %d: Apply %s: %v", trial, step, s.Kind, err)
				}
			}
			if n < prev {
				if restarts, _, _, _ := rt.counts(); restarts == restartsBefore {
					t.Fatalf("trial %d step %d: %d -> %d users applied without a restart",
						trial, step, prev, n)
				}
			}

			// THE property: re-planning an applied state yields nothing.
			obs, err = a.Observe(ctx)
			if err != nil {
				t.Fatalf("trial %d step %d: Observe: %v", trial, step, err)
			}
			again, err := a.Plan(ctx, d, obs)
			if err != nil {
				t.Fatalf("trial %d step %d: re-Plan: %v", trial, step, err)
			}
			if !again.IsEmpty() {
				t.Fatalf("trial %d step %d (%d -> %d users): did not converge; still "+
					"plans %+v", trial, step, prev, n, again.Steps)
			}
			prev = n
		}
	}
}
