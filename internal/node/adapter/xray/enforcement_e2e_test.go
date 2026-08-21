package xray

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/amyrm/antimage/internal/node/adapter"
)

func TestPolicyConfigWriting(t *testing.T) {
	dir := t.TempDir()

	t.Run("write and read policy config", func(t *testing.T) {
		upLimit := int64(1000)
		downLimit := int64(5000)

		subjects := []adapter.Subject{
			{
				ID:                 1,
				SpeedLimitUpKbps:   &upLimit,
				SpeedLimitDownKbps: &downLimit,
			},
		}

		// Generate policy config
		policyData, err := GeneratePolicyConfig(subjects)
		if err != nil {
			t.Fatalf("GeneratePolicyConfig failed: %v", err)
		}

		// Write policy file directly
		policyPath := filepath.Join(dir, policyConfigFile)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir failed: %v", err)
		}

		if err := os.WriteFile(policyPath, policyData, 0o600); err != nil {
			t.Fatalf("write policy failed: %v", err)
		}

		// Read and verify
		readData, err := os.ReadFile(policyPath)
		if err != nil {
			t.Fatalf("read policy failed: %v", err)
		}

		if string(readData) != string(policyData) {
			t.Error("policy file content mismatch")
		}

		// Verify JSON structure
		var doc map[string]any
		if err := json.Unmarshal(readData, &doc); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}

		policy := doc["policy"].(map[string]any)
		levels := policy["levels"].(map[string]any)

		// Should have default level + subject 1
		if len(levels) != 2 {
			t.Errorf("expected 2 levels, got %d", len(levels))
		}

		level1 := levels["1"].(map[string]any)
		if level1["upSpeed"].(float64) != 128000 {
			t.Errorf("wrong upSpeed: %v", level1["upSpeed"])
		}
	})
}

func TestEndToEndEnforcement(t *testing.T) {
	dir := t.TempDir()

	// Create a minimal adapter without runtime (for Plan/Observe only)
	a := &Adapter{
		dir:    dir,
		shapes: make(map[int64]string),
		hotAdd: true,
	}

	ctx := context.Background()

	t.Run("plan includes policy config", func(t *testing.T) {
		upLimit := int64(5000)
		downLimit := int64(10000)

		desired := adapter.Desired{
			SchemaVersion: 2,
			Revision:      100,
			NodeID:        1,
			Services: []adapter.Service{{
				ID:      1,
				Kind:    string(Kind),
				Enabled: true,
				Params:  []byte(`{"protocol":"vless","port":10086,"listen":"0.0.0.0"}`),
			}},
			Subjects: []adapter.Subject{
				{
					ID:                 10,
					SpeedLimitUpKbps:   &upLimit,
					SpeedLimitDownKbps: &downLimit,
					Credentials:        []adapter.Credential{{Kind: "uuid", Value: "test-uuid-1"}},
				},
				{
					ID:          20,
					Credentials: []adapter.Credential{{Kind: "uuid", Value: "test-uuid-2"}},
				},
			},
		}

		// Observe (nothing exists yet)
		observed, err := a.Observe(ctx)
		if err != nil {
			t.Fatalf("Observe failed: %v", err)
		}

		// Plan
		plan, err := a.Plan(ctx, desired, observed)
		if err != nil {
			t.Fatalf("Plan failed: %v", err)
		}

		if plan.IsEmpty() {
			t.Fatal("expected non-empty plan")
		}

		// Verify first step contains policy config
		var payload stepPayload
		if err := json.Unmarshal(plan.Steps[0].Payload, &payload); err != nil {
			t.Fatalf("unmarshal payload failed: %v", err)
		}

		if payload.PolicyConfig == "" {
			t.Error("expected policy config in step payload")
		}

		// Verify policy config is valid JSON
		var policyDoc map[string]any
		if err := json.Unmarshal([]byte(payload.PolicyConfig), &policyDoc); err != nil {
			t.Fatalf("invalid policy config JSON: %v", err)
		}

		policy := policyDoc["policy"].(map[string]any)
		levels := policy["levels"].(map[string]any)

		// Should have policies for subjects with speed limits
		if len(levels) < 2 {
			t.Errorf("expected at least 2 levels (default + subject 10), got %d", len(levels))
		}

		// Verify subject 10's policy
		level10, exists := levels["10"]
		if !exists {
			t.Error("expected level for subject 10")
		} else {
			l10 := level10.(map[string]any)
			// 5000 kbps = 640000 bytes/sec
			expectedUp := float64(640000)
			if l10["upSpeed"].(float64) != expectedUp {
				t.Errorf("expected upSpeed=%v, got %v", expectedUp, l10["upSpeed"])
			}
		}
	})
}
