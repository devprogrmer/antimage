package stub

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amyrm/antimage/internal/node/adapter"
)

func desiredWith(services ...adapter.Service) adapter.Desired {
	return adapter.Desired{SchemaVersion: 1, Revision: 1, NodeID: 1, Services: services}
}

func svc(id int64, port int, enabled bool) adapter.Service {
	return adapter.Service{
		ID: id, Kind: "stub", Enabled: enabled,
		Params: json.RawMessage(`{"port":` + itoa(port) + `}`),
	}
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

// converge runs Observe -> Plan -> Apply until the plan is empty, and returns
// how many rounds it took.
func converge(t *testing.T, a *Adapter, d adapter.Desired) int {
	t.Helper()
	ctx := context.Background()
	for round := 1; round <= 10; round++ {
		obs, err := a.Observe(ctx)
		if err != nil {
			t.Fatalf("Observe: %v", err)
		}
		plan, err := a.Plan(ctx, d, obs)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if plan.IsEmpty() {
			return round
		}
		for _, step := range plan.Steps {
			res, err := a.Apply(ctx, step)
			if err != nil {
				t.Fatalf("Apply step %d: %v", step.Seq, err)
			}
			if !res.OK {
				t.Fatalf("step %d failed: %s", step.Seq, res.Err)
			}
		}
	}
	t.Fatal("did not converge within 10 rounds")
	return 0
}

func TestDescriptorAdvertisesSchema(t *testing.T) {
	a := New(t.TempDir())
	d := a.Descriptor()
	if d.Kind != Kind {
		t.Errorf("Kind = %q, want %q", d.Kind, Kind)
	}
	var schema map[string]any
	if err := json.Unmarshal(d.Caps.ServiceSchema, &schema); err != nil {
		t.Fatalf("ServiceSchema is not valid JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("schema type = %v, want object", schema["type"])
	}
}

func TestCreatesServiceFileAndConverges(t *testing.T) {
	root := t.TempDir()
	a := New(root)
	rounds := converge(t, a, desiredWith(svc(10, 443, true)))
	if rounds != 2 {
		t.Errorf("converged in %d rounds, want 2 (one to apply, one to confirm)", rounds)
	}
	body, err := os.ReadFile(filepath.Join(root, "service-10.conf"))
	if err != nil {
		t.Fatalf("service file missing: %v", err)
	}
	if !strings.HasPrefix(string(body), MarkerPrefix) {
		t.Errorf("file lacks the ownership marker:\n%s", body)
	}
	if !strings.Contains(string(body), `"port":443`) {
		t.Errorf("file missing params:\n%s", body)
	}
}

// The core property: re-planning immediately after applying yields nothing.
func TestSecondPlanAfterApplyIsEmpty(t *testing.T) {
	a := New(t.TempDir())
	d := desiredWith(svc(10, 443, true), svc(11, 8443, true))
	converge(t, a, d)

	ctx := context.Background()
	obs, _ := a.Observe(ctx)
	plan, err := a.Plan(ctx, d, obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !plan.IsEmpty() {
		t.Fatalf("plan not empty after convergence: %+v", plan.Steps)
	}
}

func TestParamChangeIsRestartDisruption(t *testing.T) {
	a := New(t.TempDir())
	converge(t, a, desiredWith(svc(10, 443, true)))

	ctx := context.Background()
	obs, _ := a.Observe(ctx)
	plan, _ := a.Plan(ctx, desiredWith(svc(10, 9443, true)), obs)
	if plan.IsEmpty() {
		t.Fatal("changing the port produced no steps")
	}
	if got := plan.MaxDisruption(); got != adapter.DisruptRestart {
		t.Errorf("port change disruption = %v, want restart", got)
	}
}

func TestRemovedServiceIsDeleted(t *testing.T) {
	root := t.TempDir()
	a := New(root)
	converge(t, a, desiredWith(svc(10, 443, true)))
	converge(t, a, desiredWith())

	if _, err := os.Stat(filepath.Join(root, "service-10.conf")); !os.IsNotExist(err) {
		t.Error("removed service file still present")
	}
}

// Drift: a human edit must be detected, not silently overwritten, and the
// observation must report the file as no longer matching its marker.
func TestHandEditIsDetectedAsDrift(t *testing.T) {
	root := t.TempDir()
	a := New(root)
	d := desiredWith(svc(10, 443, true))
	converge(t, a, d)

	path := filepath.Join(root, "service-10.conf")
	body, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append(body, []byte("\n# hand edited\n")...), 0o600); err != nil {
		t.Fatalf("simulate hand edit: %v", err)
	}

	ctx := context.Background()
	obs, err := a.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(obs.Services) != 1 {
		t.Fatalf("observed %d services, want 1", len(obs.Services))
	}
	if !obs.Services[0].Managed {
		t.Error("hand-edited file lost its Managed flag; the marker should survive an append")
	}
	plan, _ := a.Plan(ctx, d, obs)
	if plan.IsEmpty() {
		t.Fatal("drifted file produced no plan — drift went undetected")
	}
}

// A desired service whose id collides with a pre-existing, unmanaged file
// must never have that file overwritten — this is the guard in the write
// loop (as opposed to TestUnmanagedFileIsNeverTouched, which only exercises
// the removal loop's guard, since its foreign file has no matching desired
// service).
//
// The refusal must also be visible: Plan emits a "blocked_unmanaged" step
// for the colliding service instead of silently skipping it, and Apply
// fails that step with a diagnosable error. A plan that silently converged
// to empty here would let the reconciler report the node as in sync while
// this service's desired state was never actually applied.
func TestUnmanagedFileWithCollidingIDIsNeverOverwritten(t *testing.T) {
	root := t.TempDir()
	foreign := filepath.Join(root, "service-10.conf")
	original := "hand written, colliding id, no marker\n"
	if err := os.WriteFile(foreign, []byte(original), 0o600); err != nil {
		t.Fatalf("write foreign file: %v", err)
	}

	a := New(root)
	ctx := context.Background()
	d := desiredWith(svc(10, 443, true))

	obs, err := a.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	plan, err := a.Plan(ctx, d, obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("plan has %d steps, want exactly 1 (the blocked service): %+v", len(plan.Steps), plan.Steps)
	}
	blocked := plan.Steps[0]
	if blocked.Kind != "blocked_unmanaged" {
		t.Errorf("step kind = %q, want %q", blocked.Kind, "blocked_unmanaged")
	}
	if blocked.ServiceID != 10 {
		t.Errorf("step ServiceID = %d, want 10", blocked.ServiceID)
	}

	res, err := a.Apply(ctx, blocked)
	if err == nil {
		t.Error("Apply on a blocked_unmanaged step returned nil error, want a diagnosable failure")
	}
	if res.OK {
		t.Error("StepResult.OK = true for a blocked step, want false")
	}
	if res.Err == "" {
		t.Error("StepResult.Err is empty for a blocked step, want a diagnosable message")
	}

	body, err := os.ReadFile(foreign)
	if err != nil {
		t.Fatalf("foreign file was removed: %v", err)
	}
	if string(body) != original {
		t.Errorf("foreign file was modified: %q", body)
	}
	if strings.HasPrefix(string(body), MarkerPrefix) {
		t.Error("foreign file gained a marker; the adapter overwrote a file it did not create")
	}

	// A converge cycle must not reach an empty plan: the blocked service's
	// desired state genuinely was not applied, so reporting convergence
	// would be dishonest.
	obs, err = a.Observe(ctx)
	if err != nil {
		t.Fatalf("second Observe: %v", err)
	}
	final, err := a.Plan(ctx, d, obs)
	if err != nil {
		t.Fatalf("second Plan: %v", err)
	}
	if final.IsEmpty() {
		t.Error("plan converged to empty despite the collision; the blockage must stay visible as outstanding work")
	}
}

// The blocked service must not hold up any other service: Plan/Apply's
// per-step design means one refused step should not prevent an unrelated
// service from converging normally. This is the property that justifies
// signalling the collision via a failed step rather than an error from
// Plan itself, which would have blocked the whole node.
func TestBlockedServiceDoesNotPreventOthersConverging(t *testing.T) {
	root := t.TempDir()
	foreign := filepath.Join(root, "service-10.conf")
	original := "hand written, colliding id, no marker\n"
	if err := os.WriteFile(foreign, []byte(original), 0o600); err != nil {
		t.Fatalf("write foreign file: %v", err)
	}

	a := New(root)
	ctx := context.Background()
	d := desiredWith(svc(10, 443, true), svc(11, 8443, true))

	obs, err := a.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	plan, err := a.Plan(ctx, d, obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Steps) != 2 {
		t.Fatalf("plan has %d steps, want 2 (one blocked, one write): %+v", len(plan.Steps), plan.Steps)
	}
	for _, step := range plan.Steps {
		res, err := a.Apply(ctx, step)
		switch step.ServiceID {
		case 10:
			if err == nil || res.OK {
				t.Errorf("service 10 (blocked) step: err=%v res=%+v, want a failure", err, res)
			}
		case 11:
			if err != nil || !res.OK {
				t.Fatalf("service 11 (clean) step failed: err=%v res=%+v", err, res)
			}
		default:
			t.Fatalf("unexpected step for service %d", step.ServiceID)
		}
	}

	// The clean service converged: its file exists, is marked, and carries
	// the right params.
	cleanBody, err := os.ReadFile(filepath.Join(root, "service-11.conf"))
	if err != nil {
		t.Fatalf("service 11 file missing: %v", err)
	}
	if !strings.HasPrefix(string(cleanBody), MarkerPrefix) {
		t.Errorf("service 11 file lacks the ownership marker:\n%s", cleanBody)
	}
	if !strings.Contains(string(cleanBody), `"port":8443`) {
		t.Errorf("service 11 file missing params:\n%s", cleanBody)
	}

	// The blocked service's foreign file is still exactly as a human left
	// it, untouched and unmarked.
	blockedBody, err := os.ReadFile(foreign)
	if err != nil {
		t.Fatalf("foreign file was removed: %v", err)
	}
	if string(blockedBody) != original {
		t.Errorf("foreign file was modified: %q", blockedBody)
	}
	if strings.HasPrefix(string(blockedBody), MarkerPrefix) {
		t.Error("foreign file gained a marker; the adapter overwrote a file it did not create")
	}

	// Re-planning still reports service 10 as blocked, not silently done.
	obs, err = a.Observe(ctx)
	if err != nil {
		t.Fatalf("second Observe: %v", err)
	}
	final, err := a.Plan(ctx, d, obs)
	if err != nil {
		t.Fatalf("second Plan: %v", err)
	}
	if len(final.Steps) != 1 || final.Steps[0].Kind != "blocked_unmanaged" || final.Steps[0].ServiceID != 10 {
		t.Errorf("second plan = %+v, want exactly one blocked_unmanaged step for service 10", final.Steps)
	}
}

// A file antimage did not write must never be touched.
func TestUnmanagedFileIsNeverTouched(t *testing.T) {
	root := t.TempDir()
	foreign := filepath.Join(root, "service-99.conf")
	if err := os.WriteFile(foreign, []byte("hand written, no marker\n"), 0o600); err != nil {
		t.Fatalf("write foreign file: %v", err)
	}

	a := New(root)
	converge(t, a, desiredWith(svc(10, 443, true)))

	body, err := os.ReadFile(foreign)
	if err != nil {
		t.Fatalf("foreign file was removed: %v", err)
	}
	if string(body) != "hand written, no marker\n" {
		t.Errorf("foreign file was modified: %q", body)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	a := New(t.TempDir())
	ctx := context.Background()
	d := desiredWith(svc(10, 443, true))

	obs, _ := a.Observe(ctx)
	plan, _ := a.Plan(ctx, d, obs)
	for i := 0; i < 3; i++ {
		for _, step := range plan.Steps {
			res, err := a.Apply(ctx, step)
			if err != nil || !res.OK {
				t.Fatalf("re-apply %d of step %d failed: %v %s", i, step.Seq, err, res.Err)
			}
		}
	}
	obs, _ = a.Observe(ctx)
	final, _ := a.Plan(ctx, d, obs)
	if !final.IsEmpty() {
		t.Error("repeated application left work outstanding; steps are not idempotent")
	}
}

func TestProbeReportsHealthy(t *testing.T) {
	a := New(t.TempDir())
	h, err := a.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !h.OK {
		t.Errorf("Probe reported unhealthy: %s", h.Detail)
	}
}

// This adapter manages plain files directly, with no daemon behind them.
// Restart must say so, not silently succeed at bouncing nothing.
func TestRestartIsUnsupported(t *testing.T) {
	a := New(t.TempDir())
	if err := a.Restart(context.Background()); !errors.Is(err, adapter.ErrRestartUnsupported) {
		t.Errorf("Restart = %v, want ErrRestartUnsupported", err)
	}
}
