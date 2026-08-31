//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/node/adapter"
	"github.com/amyrm/antimage/internal/node/agent"
)

// desiredNow decodes the panel's current desired document for the node.
func (s *sp2Env) desiredNow(t *testing.T) adapter.Desired {
	t.Helper()
	var d adapter.Desired
	if err := json.Unmarshal(s.snapshot(t).Bytes, &d); err != nil {
		t.Fatalf("decode desired document: %v", err)
	}
	return d
}

// The maintenance window is enforced against the REAL adapter, not just the
// stub, and revocation is subject to it.
//
// Two things are proven here that nothing else covers. First, the reconciler's
// AllowDisruptive gate is only ever exercised against the stub adapter; the SP2
// E2E path calls Observe/Plan/Apply directly and never goes through Converge at
// all, so no test has ever run a real Xray plan through the window logic.
//
// Second, and more importantly, making revocation restart-class has a
// consequence that deserves to be pinned down rather than discovered in
// production: a revocation raised while the window is shut is DEFERRED, so the
// revoked user keeps their session until the window opens. That is the correct
// trade -- the alternative is the silent hot path that left them connected
// forever -- but it is a real operational property and it is asserted here so
// it cannot change unnoticed.
func TestSP2MaintenanceWindowDefersRevocationUntilItOpens(t *testing.T) {
	e := startSP2(t, "xray")
	ctx := context.Background()

	windowOpen := true
	clk := agent.NewFakeClock(time.Unix(1_700_000_000, 0).UTC())
	rec := agent.NewReconciler(agent.MustRegistry(e.adapter), clk, agent.ReconcileOptions{
		MaxRetries:      3,
		RetryBase:       time.Millisecond,
		AllowDisruptive: func(time.Time) bool { return windowOpen },
	})

	// --- Two users, applied with the window open. ---
	keep := e.createSubject(t, "alice", nil)
	keepCred := e.credential(t, keep)
	revoke := e.createSubject(t, "mallory", nil)
	revokeCred := e.credential(t, revoke)

	run, err := rec.Converge(ctx, e.desiredNow(t))
	if err != nil {
		t.Fatalf("initial Converge: %v", err)
	}
	if run.Deferred {
		t.Fatal("initial convergence was deferred with the window open")
	}
	if !strings.Contains(e.generatedConfig(t), revokeCred) {
		t.Fatal("precondition: the user to revoke never reached the config")
	}
	restartsBefore := e.restartCount()

	// --- Shut the window, then revoke. ---
	windowOpen = false
	if code := e.apiJSON("DELETE", fmt.Sprintf("/api/v1/subjects/%d", revoke), "", nil); //nolint:lll
	code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}

	deferredRun, err := rec.Converge(ctx, e.desiredNow(t))
	if err != nil {
		t.Fatalf("Converge with the window shut: %v", err)
	}
	if !deferredRun.Deferred {
		t.Error("a restart-class revocation was NOT deferred with the window shut")
	}
	if deferredRun.Converged {
		t.Error("reported converged while the revocation was deferred")
	}
	if len(deferredRun.Steps) != 0 {
		t.Errorf("applied %d step(s) with the window shut, want 0", len(deferredRun.Steps))
	}
	// Nothing may have changed on the host yet.
	if e.restartCount() != restartsBefore {
		t.Errorf("the proxy was restarted with the window shut: %d -> %d",
			restartsBefore, e.restartCount())
	}
	if !strings.Contains(e.generatedConfig(t), revokeCred) {
		t.Error("the config was rewritten with the window shut; a deferred plan must " +
			"leave the host untouched")
	}
	t.Logf("window shut: revocation deferred, no restart, config untouched")

	// --- Open the window; the revocation lands. ---
	windowOpen = true
	applied, err := rec.Converge(ctx, e.desiredNow(t))
	if err != nil {
		t.Fatalf("Converge with the window open: %v", err)
	}
	if applied.Deferred {
		t.Fatal("still deferred with the window open")
	}
	if !applied.Converged {
		t.Errorf("did not converge once the window opened: %+v", applied.Steps)
	}
	config := e.generatedConfig(t)
	if strings.Contains(config, revokeCred) {
		t.Error("the revoked credential survived a convergence inside the window")
	}
	if !strings.Contains(config, keepCred) {
		t.Error("revoking one user removed another")
	}
	if e.restartCount() == restartsBefore {
		t.Error("the revocation never restarted the proxy, so the user keeps their session")
	}
	t.Logf("window open: revocation applied, %d restart(s) total", e.restartCount())

	// --- And it stays converged. ---
	for i := 0; i < 3; i++ {
		again, err := rec.Converge(ctx, e.desiredNow(t))
		if err != nil {
			t.Fatalf("Converge %d: %v", i, err)
		}
		if !again.Converged || len(again.Steps) != 0 {
			t.Fatalf("not idempotent on pass %d: converged=%v steps=%+v",
				i, again.Converged, again.Steps)
		}
	}
}
