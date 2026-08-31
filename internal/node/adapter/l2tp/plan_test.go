package l2tp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/amyrm/antimage/internal/node/adapter"
)

func TestPlanDriftDetection(t *testing.T) {
	a := New("/tmp/l2tp-test-conf", "/tmp/l2tp-test-state")

	params := ServiceParams{
		IPRange:    "10.0.0.2-10.0.0.254",
		LocalIP:    "10.0.0.1",
		PSK:        "test-psk-key",
		DNSServers: []string{"8.8.8.8", "8.8.4.4"},
	}
	paramsJSON, _ := json.Marshal(params)

	desired := adapter.Desired{
		Services: []adapter.Service{
			{
				ID:      1,
				Kind:    string(Kind),
				Enabled: true,
				Params:  paramsJSON,
			},
		},
		Subjects: []adapter.Subject{
			{
				ID: 100,
				Credentials: []adapter.Credential{
					{Kind: string(adapter.CredPassword), Value: "pass123"},
				},
			},
		},
	}

	// Simulate observed state: service present but not managed (drift).
	observed := adapter.Observed{
		Services: []adapter.ObservedService{
			{
				ID:       1,
				Present:  true,
				Managed:  false, // Files exist but lack our marker → drift.
				Checksum: "wrong-checksum",
			},
		},
	}

	plan, err := a.Plan(context.Background(), desired, observed)
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	// Drift detection should plan a restart to restore managed config.
	if len(plan.Steps) != 1 {
		t.Fatalf("expected 1 step for drift, got %d", len(plan.Steps))
	}

	step := plan.Steps[0]
	if step.Kind != StepUpdateConfigs {
		t.Errorf("expected StepUpdateConfigs for drift, got %s", step.Kind)
	}
	if step.Disruption != adapter.DisruptRestart {
		t.Errorf("expected DisruptRestart for drift, got %v", step.Disruption)
	}
	if step.ServiceID != 1 {
		t.Errorf("expected ServiceID=1, got %d", step.ServiceID)
	}
}

func TestPlanManagedServiceChecksum(t *testing.T) {
	a := New("/tmp/l2tp-test-conf", "/tmp/l2tp-test-state")

	params := ServiceParams{
		IPRange:    "10.0.0.2-10.0.0.254",
		LocalIP:    "10.0.0.1",
		PSK:        "test-psk-key",
		DNSServers: []string{"8.8.8.8"},
	}
	paramsJSON, _ := json.Marshal(params)

	desired := adapter.Desired{
		Services: []adapter.Service{
			{
				ID:      2,
				Kind:    string(Kind),
				Enabled: true,
				Params:  paramsJSON,
			},
		},
		Subjects: []adapter.Subject{
			{
				ID: 200,
				Credentials: []adapter.Credential{
					{Kind: string(adapter.CredPassword), Value: "password"},
				},
			},
		},
	}

	// Compute the expected checksum.
	desiredIPsec := renderIPsecConf(2, params)
	desiredSecrets := renderIPsecSecrets(2, params)
	desiredXL2TPD := renderXL2TPDConf(2, params)
	desiredCHAP := renderCHAPSecrets(2, desired.Subjects)
	desiredOpts := renderPPPOptions(2, params)

	combined := checksumOf(desiredIPsec) + ":" + checksumOf(desiredSecrets) + ":" +
		checksumOf(desiredXL2TPD) + ":" + checksumOf(desiredCHAP) + ":" + checksumOf(desiredOpts)
	correctChecksum := checksumOf(combined)

	// Case 1: Service managed, checksum matches → no steps.
	observed := adapter.Observed{
		Services: []adapter.ObservedService{
			{
				ID:       2,
				Present:  true,
				Managed:  true,
				Checksum: correctChecksum,
			},
		},
	}

	plan, err := a.Plan(context.Background(), desired, observed)
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}
	if len(plan.Steps) != 0 {
		t.Errorf("expected 0 steps when checksums match, got %d", len(plan.Steps))
	}

	// Case 2: Service managed, checksum differs → plan update.
	observed.Services[0].Checksum = "different-checksum"
	plan, err = a.Plan(context.Background(), desired, observed)
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("expected 1 step for checksum mismatch, got %d", len(plan.Steps))
	}

	step := plan.Steps[0]
	// Since detectParamsChange currently returns true, we expect a restart.
	if step.Kind != StepUpdateConfigs {
		t.Errorf("expected StepUpdateConfigs, got %s", step.Kind)
	}
	if step.Disruption != adapter.DisruptRestart {
		t.Errorf("expected DisruptRestart, got %v", step.Disruption)
	}
}

func TestPlanInstallRemove(t *testing.T) {
	a := New("/tmp/l2tp-test-conf", "/tmp/l2tp-test-state")

	params := ServiceParams{
		IPRange:    "10.0.0.2-10.0.0.254",
		LocalIP:    "10.0.0.1",
		PSK:        "test-psk",
		DNSServers: []string{"8.8.8.8"},
	}
	paramsJSON, _ := json.Marshal(params)

	// Case 1: Service desired but not present → install.
	desired := adapter.Desired{
		Services: []adapter.Service{
			{
				ID:      3,
				Kind:    string(Kind),
				Enabled: true,
				Params:  paramsJSON,
			},
		},
	}
	observed := adapter.Observed{}

	plan, err := a.Plan(context.Background(), desired, observed)
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("expected 1 install step, got %d", len(plan.Steps))
	}
	if plan.Steps[0].Kind != StepInstallConfigs {
		t.Errorf("expected StepInstallConfigs, got %s", plan.Steps[0].Kind)
	}

	// Case 2: Service not desired but present → remove.
	desired = adapter.Desired{}
	observed = adapter.Observed{
		Services: []adapter.ObservedService{
			{
				ID:      3,
				Present: true,
				Managed: true,
			},
		},
	}

	plan, err = a.Plan(context.Background(), desired, observed)
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("expected 1 remove step, got %d", len(plan.Steps))
	}
	if plan.Steps[0].Kind != StepRemoveConfigs {
		t.Errorf("expected StepRemoveConfigs, got %s", plan.Steps[0].Kind)
	}
}

func TestPlanMultipleServices(t *testing.T) {
	a := New("/tmp/l2tp-test-conf", "/tmp/l2tp-test-state")

	params := ServiceParams{
		IPRange:    "10.0.0.2-10.0.0.254",
		LocalIP:    "10.0.0.1",
		PSK:        "test-psk",
		DNSServers: []string{"8.8.8.8"},
	}
	paramsJSON, _ := json.Marshal(params)

	// SP6 design decision 7: only one L2TP service per node.
	desired := adapter.Desired{
		Services: []adapter.Service{
			{
				ID:      1,
				Kind:    string(Kind),
				Enabled: true,
				Params:  paramsJSON,
			},
			{
				ID:      2,
				Kind:    string(Kind),
				Enabled: true,
				Params:  paramsJSON,
			},
		},
	}
	observed := adapter.Observed{}

	_, err := a.Plan(context.Background(), desired, observed)
	if err == nil {
		t.Error("expected error for multiple L2TP services, got nil")
	}
	if err != nil && err.Error() != "multiple L2TP services not supported on one node" {
		t.Errorf("unexpected error: %v", err)
	}
}
