package xray

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// The egress document has to go round the whole Observe -> Plan -> Apply loop,
// not just render correctly. These exercise the loop against a real filesystem.

func egressAdapter(t *testing.T) (*Adapter, *fakeRuntime, string) {
	t.Helper()
	dir := t.TempDir()
	rt := &fakeRuntime{healthy: true}
	return New(dir, rt, true), rt, dir
}

func desiredWithEgress() adapter.Desired {
	return adapter.Desired{
		SchemaVersion: 3, Revision: 1, NodeID: 1,
		Outbounds: []adapter.Outbound{{ID: 1, Tag: "warp", Kind: "block"}},
		Routing: &adapter.Routing{Rules: []adapter.RoutingRule{
			{ID: 1, Priority: 10, Domains: []string{"example.com"}, OutboundTag: "warp"},
		}},
	}
}

func applyAll(t *testing.T, a *Adapter, plan adapter.Plan) {
	t.Helper()
	for _, step := range plan.Steps {
		res, err := a.Apply(context.Background(), step)
		if err != nil {
			t.Fatalf("apply %s: %v", step.Kind, err)
		}
		if !res.OK {
			t.Fatalf("apply %s not ok: %s", step.Kind, res.Err)
		}
	}
}

func TestEgressConvergesAndStaysConverged(t *testing.T) {
	a, rt, dir := egressAdapter(t)
	ctx := context.Background()
	desired := desiredWithEgress()

	obs, err := a.Observe(ctx)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Egress != nil {
		t.Fatalf("fresh confdir reported egress: %+v", obs.Egress)
	}

	plan, err := a.Plan(ctx, desired, obs)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Steps) != 1 || plan.Steps[0].Kind != StepWriteEgress {
		t.Fatalf("want one write_egress step, got %+v", plan.Steps)
	}
	// Xray reads outbounds and routing only at startup.
	if plan.Steps[0].Disruption != adapter.DisruptRestart {
		t.Errorf("egress write is %v, want restart: Xray would otherwise report "+
			"converged while still routing by the previous table",
			plan.Steps[0].Disruption)
	}

	applyAll(t, a, plan)

	if _, err := os.Stat(filepath.Join(dir, egressFile)); err != nil {
		t.Fatalf("egress file not written: %v", err)
	}
	if restarts, _, _, _ := rt.counts(); restarts == 0 {
		t.Error("egress applied without restarting the runtime")
	}

	// Second pass must be a no-op. A planner that rewrites every time keeps
	// restarting the proxy, dropping every session on the node each cycle.
	obs, err = a.Observe(ctx)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Egress == nil || !obs.Egress.Present || !obs.Egress.Managed {
		t.Fatalf("written egress not observed as managed: %+v", obs.Egress)
	}
	plan, err = a.Plan(ctx, desired, obs)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Steps) != 0 {
		t.Errorf("converged state still plans %d steps: %+v", len(plan.Steps), plan.Steps)
	}
}

// A hand edit to the routing table must be caught. This is the entire reason
// egress lives in the document rather than being rendered panel-side.
func TestEgressDriftIsDetected(t *testing.T) {
	a, _, dir := egressAdapter(t)
	ctx := context.Background()
	desired := desiredWithEgress()

	obs, _ := a.Observe(ctx)
	plan, err := a.Plan(ctx, desired, obs)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	applyAll(t, a, plan)

	// Somebody edits the routing table by hand, keeping the marker line so the
	// file still looks like ours.
	path := filepath.Join(dir, egressFile)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	tampered := string(body) + "\n// somebody was here\n"
	if err := os.WriteFile(path, []byte(tampered), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	obs, err = a.Observe(ctx)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	plan, err = a.Plan(ctx, desired, obs)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Steps) != 1 || plan.Steps[0].Kind != StepWriteEgress {
		t.Errorf("hand edit to the routing table produced no correction: %+v", plan.Steps)
	}
}

// An egress file this adapter did not write is never overwritten, matching how
// unmanaged service files are treated.
func TestUnmanagedEgressFileIsRefused(t *testing.T) {
	a, _, dir := egressAdapter(t)
	ctx := context.Background()

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, egressFile),
		[]byte(`{"outbounds":[{"tag":"hand-written","protocol":"freedom"}]}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	obs, err := a.Observe(ctx)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Egress == nil || obs.Egress.Managed {
		t.Fatalf("hand-written egress reported as managed: %+v", obs.Egress)
	}

	_, err = a.Plan(ctx, desiredWithEgress(), obs)
	if err == nil {
		t.Error("planned over a hand-written egress file instead of refusing")
	}
}

// Removing every outbound and rule must remove the file, not leave a stale one.
func TestEgressRemovedWhenNoLongerDesired(t *testing.T) {
	a, _, dir := egressAdapter(t)
	ctx := context.Background()

	obs, _ := a.Observe(ctx)
	plan, err := a.Plan(ctx, desiredWithEgress(), obs)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	applyAll(t, a, plan)

	obs, _ = a.Observe(ctx)
	bare := adapter.Desired{SchemaVersion: 2, Revision: 2, NodeID: 1}
	plan, err = a.Plan(ctx, bare, obs)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Steps) != 1 || plan.Steps[0].Kind != StepRemoveEgress {
		t.Fatalf("want one remove_egress step, got %+v", plan.Steps)
	}
	applyAll(t, a, plan)

	if _, err := os.Stat(filepath.Join(dir, egressFile)); !os.IsNotExist(err) {
		t.Errorf("egress file survived removal: %v", err)
	}
}

// A v2 document -- no egress at all -- must not touch the egress file, so a
// panel that has not adopted v3 never disturbs a node.
func TestV2DocumentPlansNoEgressWork(t *testing.T) {
	a, _, _ := egressAdapter(t)
	ctx := context.Background()

	obs, _ := a.Observe(ctx)
	plan, err := a.Plan(ctx, adapter.Desired{SchemaVersion: 2, Revision: 1, NodeID: 1}, obs)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Steps) != 0 {
		t.Errorf("v2 document produced egress work: %+v", plan.Steps)
	}
}
