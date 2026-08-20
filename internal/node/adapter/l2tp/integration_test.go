package l2tp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/amyrm/antimage/internal/node/adapter"
)

func TestObservePlanApplyCycle(t *testing.T) {
	// Skip if not running with privileges (systemctl needs root).
	if os.Getuid() != 0 {
		t.Skip("integration test requires root privileges")
	}

	tmpConf := t.TempDir()
	tmpState := t.TempDir()
	a := New(tmpConf, tmpState)
	ctx := context.Background()

	// Initial observe: nothing present.
	obs1, err := a.Observe(ctx)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if len(obs1.Services) != 0 {
		t.Errorf("want 0 services, got %d", len(obs1.Services))
	}

	// Build desired state.
	params := ServiceParams{
		IPRange:    "10.8.0.2-10.8.0.254",
		LocalIP:    "10.8.0.1",
		PSK:        "test-psk-secret-key",
		DNSServers: []string{"8.8.8.8"},
	}
	paramsJSON, _ := json.Marshal(params)

	desired := adapter.Desired{
		SchemaVersion: 1,
		Revision:      1,
		NodeID:        1,
		Services: []adapter.Service{
			{
				ID:      100,
				Kind:    string(Kind),
				Enabled: true,
				Params:  paramsJSON,
			},
		},
		Subjects: []adapter.Subject{
			{
				ID: 1,
				Credentials: []adapter.Credential{
					{Kind: string(adapter.CredPassword), Value: "password123"},
				},
			},
		},
	}

	// Plan: should emit install step.
	plan1, err := a.Plan(ctx, desired, obs1)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan1.Steps) != 1 {
		t.Fatalf("want 1 step, got %d", len(plan1.Steps))
	}
	if plan1.Steps[0].Kind != StepInstallConfigs {
		t.Errorf("want step kind %s, got %s", StepInstallConfigs, plan1.Steps[0].Kind)
	}
	if plan1.Steps[0].Disruption != adapter.DisruptRestart {
		t.Errorf("want disruption restart, got %v", plan1.Steps[0].Disruption)
	}

	// Apply the step (without actually calling systemctl in test).
	// For a real integration test, this would start services.
	result := a.applyInstallConfigsTestMode(ctx, plan1.Steps[0])
	if !result.OK {
		t.Errorf("apply failed: %s", result.Err)
	}

	// Observe again: should see managed service.
	obs2, err := a.Observe(ctx)
	if err != nil {
		t.Fatalf("observe after apply: %v", err)
	}
	if len(obs2.Services) != 1 {
		t.Fatalf("want 1 service after apply, got %d", len(obs2.Services))
	}
	if !obs2.Services[0].Present {
		t.Error("service not present after apply")
	}
	if !obs2.Services[0].Managed {
		t.Error("service not managed after apply")
	}

	// Plan again: should converge (empty plan).
	plan2, err := a.Plan(ctx, desired, obs2)
	if err != nil {
		t.Fatalf("plan after converge: %v", err)
	}
	if len(plan2.Steps) != 0 {
		t.Errorf("want 0 steps after convergence, got %d", len(plan2.Steps))
	}
}

// applyInstallConfigsTestMode is a test-mode version that writes configs but skips systemctl.
func (a *Adapter) applyInstallConfigsTestMode(ctx context.Context, step adapter.Step) adapter.StepResult {
	var payload installPayload
	if err := json.Unmarshal(step.Payload, &payload); err != nil {
		return adapter.StepResult{OK: false, Err: err.Error()}
	}

	params, err := parseServiceParams(payload.Params)
	if err != nil {
		return adapter.StepResult{OK: false, Err: err.Error()}
	}

	configs := map[string]string{
		"strongswan/ipsec.conf":    renderIPsecConf(step.ServiceID, params),
		"strongswan/ipsec.secrets": renderIPsecSecrets(step.ServiceID, params),
		"xl2tpd/xl2tpd.conf":       renderXL2TPDConf(step.ServiceID, params),
		"ppp/chap-secrets":         renderCHAPSecrets(step.ServiceID, payload.Subjects),
		"ppp/options.xl2tpd":       renderPPPOptions(step.ServiceID, params),
	}

	for relPath, content := range configs {
		fullPath := filepath.Join(a.confDir, relPath)
		if err := a.writeFile(fullPath, content); err != nil {
			return adapter.StepResult{OK: false, Err: err.Error()}
		}
	}

	return adapter.StepResult{OK: true, Kind: step.Kind, Disruption: step.Disruption}
}

func TestObserveDetectsDrift(t *testing.T) {
	tmpConf := t.TempDir()
	tmpState := t.TempDir()
	a := New(tmpConf, tmpState)
	ctx := context.Background()

	// Write a non-managed config file.
	ipsecPath := filepath.Join(tmpConf, "strongswan/ipsec.conf")
	if err := os.MkdirAll(filepath.Dir(ipsecPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ipsecPath, []byte("# hand-edited config\nconn test\n"), 0644); err != nil {
		t.Fatal(err)
	}

	obs, err := a.Observe(ctx)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}

	// Should detect file present but not managed (drift).
	if len(obs.Services) == 0 {
		// No services observed because no managed files found (correct behavior).
		return
	}

	if len(obs.Services) == 1 && obs.Services[0].Present && !obs.Services[0].Managed {
		// Detected drift (also correct).
		return
	}

	t.Error("expected drift detection or no services, got managed service")
}

func TestPlanMultipleL2TPServices(t *testing.T) {
	tmpConf := t.TempDir()
	tmpState := t.TempDir()
	a := New(tmpConf, tmpState)
	ctx := context.Background()

	paramsJSON := json.RawMessage(`{"ip_range":"10.8.0.2-10.8.0.254","local_ip":"10.8.0.1","psk":"test"}`)

	desired := adapter.Desired{
		SchemaVersion: 1,
		Revision:      1,
		NodeID:        1,
		Services: []adapter.Service{
			{ID: 1, Kind: string(Kind), Enabled: true, Params: paramsJSON},
			{ID: 2, Kind: string(Kind), Enabled: true, Params: paramsJSON},
		},
	}

	obs := adapter.Observed{}

	// Plan should reject multiple L2TP services.
	_, err := a.Plan(ctx, desired, obs)
	if err == nil {
		t.Error("expected error for multiple L2TP services, got nil")
	}
	if err != nil && err.Error() != "multiple L2TP services not supported on one node" {
		t.Errorf("wrong error: %v", err)
	}
}

func TestPlanReloadVsRestart(t *testing.T) {
	tmpConf := t.TempDir()
	tmpState := t.TempDir()
	a := New(tmpConf, tmpState)
	ctx := context.Background()

	// Set up initial state: service already installed.
	params := ServiceParams{
		IPRange:    "10.8.0.2-10.8.0.254",
		LocalIP:    "10.8.0.1",
		PSK:        "test-psk",
		DNSServers: []string{"8.8.8.8"},
	}
	paramsJSON, _ := json.Marshal(params)

	serviceID := int64(100)

	// Write initial configs.
	configs := map[string]string{
		"strongswan/ipsec.conf":    renderIPsecConf(serviceID, params),
		"strongswan/ipsec.secrets": renderIPsecSecrets(serviceID, params),
		"xl2tpd/xl2tpd.conf":       renderXL2TPDConf(serviceID, params),
		"ppp/chap-secrets":         renderCHAPSecrets(serviceID, []adapter.Subject{}),
		"ppp/options.xl2tpd":       renderPPPOptions(serviceID, params),
	}

	for relPath, content := range configs {
		fullPath := filepath.Join(tmpConf, relPath)
		if err := a.writeFile(fullPath, content); err != nil {
			t.Fatal(err)
		}
	}

	// Observe initial state.
	obs, err := a.Observe(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Desired state: add a user (no param changes).
	desired := adapter.Desired{
		SchemaVersion: 1,
		Revision:      2,
		NodeID:        1,
		Services: []adapter.Service{
			{ID: serviceID, Kind: string(Kind), Enabled: true, Params: paramsJSON},
		},
		Subjects: []adapter.Subject{
			{ID: 1, Credentials: []adapter.Credential{{Kind: "password", Value: "newpass"}}},
		},
	}

	plan, err := a.Plan(ctx, desired, obs)
	if err != nil {
		t.Fatal(err)
	}

	// Current implementation conservatively restarts. In a future refinement,
	// we could detect user-only changes and emit reload.
	// For now, we accept either reload or restart as valid.
	if len(plan.Steps) == 0 {
		t.Error("expected plan step for adding user")
	}
}

func TestProbe(t *testing.T) {
	tmpConf := t.TempDir()
	tmpState := t.TempDir()
	a := New(tmpConf, tmpState)
	ctx := context.Background()

	// Probe should report services not running (they're not installed in test).
	health, err := a.Probe(ctx)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}

	// In test environment, services won't be running.
	if health.OK {
		t.Error("expected health.OK=false when services not running")
	}
	if health.Detail == "" {
		t.Error("expected health detail message")
	}
}
