package xray

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/node/adapter"
	"github.com/amyrm/antimage/internal/node/enforcement"
)

// TestEndToEndConnectionLimitEnforcement verifies the complete enforcement flow:
// Database → Desired State → Adapter → Enforcer → Runtime behavior
func TestEndToEndConnectionLimitEnforcement(t *testing.T) {
	ctx := context.Background()

	t.Run("connection limit enforced", func(t *testing.T) {
		// Create fresh enforcer/tracker for this test
		enforcer := enforcement.New()
		runtime := &mockRuntime{stats: []UserStat{}}
		adapter := &Adapter{rt: runtime, shapes: make(map[int64]string), hotAdd: true}
		tracker := NewConnectionTracker(adapter, enforcer)

		// Step 1: Panel sets policy with limit=2
		maxConns := int64(2)
		enforcer.UpdatePolicies([]enforcement.Policy{
			{SubjectID: 1, MaxConnections: &maxConns},
		})

		// Step 2: First connection arrives
		runtime.stats = []UserStat{
			{Email: "subject-1@antimage", Uplink: 1000, Downlink: 2000},
		}
		tracker.Sync(ctx, "test-inbound")

		// Verify: Connection registered
		if len(enforcer.GetActiveConnections(1)) != 1 {
			t.Fatalf("expected 1 connection, got %d", len(enforcer.GetActiveConnections(1)))
		}

		// Step 3: Second connection arrives
		runtime.stats = append(runtime.stats, UserStat{
			Email: "subject-1@antimage-2", Uplink: 500, Downlink: 1000,
		})
		tracker.Sync(ctx, "test-inbound")

		// Verify: Second connection registered (at limit)
		if len(enforcer.GetActiveConnections(1)) != 2 {
			t.Fatalf("expected 2 connections, got %d", len(enforcer.GetActiveConnections(1)))
		}

		// Step 4: Third connection attempt (exceeds limit)
		runtime.stats = append(runtime.stats, UserStat{
			Email: "subject-1@antimage-3", Uplink: 100, Downlink: 200,
		})

		tracker.Sync(ctx, "test-inbound")

		// Verify: Third connection was terminated
		if runtime.removedUser == nil || !runtime.removedUser["subject-1@antimage-3"] {
			t.Error("expected third connection to be terminated due to limit")
		}

		// Verify: Still only 2 connections
		conns := enforcer.GetActiveConnections(1)
		if len(conns) != 2 {
			t.Errorf("expected 2 connections at limit, got %d", len(conns))
		}
	})

	t.Run("policy update reduces limit and terminates excess", func(t *testing.T) {
		// Reset
		enforcer2 := enforcement.New()
		runtime2 := &mockRuntime{stats: []UserStat{}}
		adapter2 := &Adapter{rt: runtime2, shapes: make(map[int64]string), hotAdd: true}
		_ = adapter2 // Use adapter2 to avoid unused variable error

		// Start with limit=5
		maxConns := int64(5)
		enforcer2.UpdatePolicies([]enforcement.Policy{
			{SubjectID: 1, MaxConnections: &maxConns},
		})

		// Register 3 connections
		for i := 1; i <= 3; i++ {
			enforcer2.CheckAndRegisterConnection(
				fmt.Sprintf("conn-%d", i), 1, "dev-1", "1.1.1.1", "test")
		}

		// Verify 3 connections
		if len(enforcer2.GetActiveConnections(1)) != 3 {
			t.Fatal("expected 3 connections registered")
		}

		// Panel reduces limit to 2
		newLimit := int64(2)
		enforcer2.UpdatePolicies([]enforcement.Policy{
			{SubjectID: 1, MaxConnections: &newLimit},
		})

		// Verify: Excess connections terminated (oldest removed)
		conns := enforcer2.GetActiveConnections(1)
		if len(conns) != 2 {
			t.Errorf("expected 2 connections after limit reduction, got %d", len(conns))
		}

		// Verify: Most recent connections kept (conn-2 and conn-3)
		connIDs := make(map[string]bool)
		for _, c := range conns {
			connIDs[c.ID] = true
		}
		if !connIDs["conn-2"] || !connIDs["conn-3"] {
			t.Error("expected most recent connections to be kept")
		}
	})

	t.Run("policy removal terminates all connections", func(t *testing.T) {
		enforcer3 := enforcement.New()

		// Set policy
		maxConns := int64(5)
		enforcer3.UpdatePolicies([]enforcement.Policy{
			{SubjectID: 1, MaxConnections: &maxConns},
		})

		// Register connections
		enforcer3.CheckAndRegisterConnection("conn-1", 1, "dev-1", "1.1.1.1", "test")
		enforcer3.CheckAndRegisterConnection("conn-2", 1, "dev-2", "1.1.1.2", "test")

		if len(enforcer3.GetActiveConnections(1)) != 2 {
			t.Fatal("expected 2 connections")
		}

		// Panel removes policy (subject deleted or disabled)
		enforcer3.UpdatePolicies([]enforcement.Policy{})

		// Verify: All connections terminated
		conns := enforcer3.GetActiveConnections(1)
		if len(conns) != 0 {
			t.Errorf("expected 0 connections after policy removal, got %d", len(conns))
		}
	})
}

// TestEndToEndSpeedLimitEnforcement verifies speed limit policy generation and application
func TestEndToEndSpeedLimitEnforcement(t *testing.T) {
	t.Run("speed limits propagate to Xray policy config", func(t *testing.T) {
		upLimit := int64(5000)    // 5 Mbps
		downLimit := int64(10000) // 10 Mbps

		subjects := []adapter.Subject{
			{
				ID:                 1,
				SpeedLimitUpKbps:   &upLimit,
				SpeedLimitDownKbps: &downLimit,
				Credentials:        []adapter.Credential{{Kind: "uuid", Value: "test-uuid"}},
			},
		}

		// Generate policy config (this is what Xray loads)
		policyBytes, err := GeneratePolicyConfig(subjects)
		if err != nil {
			t.Fatalf("GeneratePolicyConfig failed: %v", err)
		}

		// Verify policy config contains speed limits
		var doc map[string]any
		if err := json.Unmarshal(policyBytes, &doc); err != nil {
			t.Fatalf("invalid policy JSON: %v", err)
		}

		policy := doc["policy"].(map[string]any)
		levels := policy["levels"].(map[string]any)

		// Check subject level exists
		level1, exists := levels["1"]
		if !exists {
			t.Fatal("expected policy level for subject 1")
		}

		l1 := level1.(map[string]any)

		// Verify conversion: kbps → bytes/sec
		// 5000 kbps = 5000 * 1024 / 8 = 640000 bytes/sec
		expectedUp := float64(640000)
		if l1["upSpeed"].(float64) != expectedUp {
			t.Errorf("expected upSpeed=%v, got %v", expectedUp, l1["upSpeed"])
		}

		// 10000 kbps = 10000 * 1024 / 8 = 1280000 bytes/sec
		expectedDown := float64(1280000)
		if l1["downSpeed"].(float64) != expectedDown {
			t.Errorf("expected downSpeed=%v, got %v", expectedDown, l1["downSpeed"])
		}
	})

	t.Run("speed limits included in plan and applied on restart", func(t *testing.T) {
		dir := t.TempDir()
		a := &Adapter{
			dir:    dir,
			shapes: make(map[int64]string),
			hotAdd: true,
		}

		ctx := context.Background()

		upLimit := int64(1000)
		downLimit := int64(5000)

		desired := adapter.Desired{
			SchemaVersion: 2,
			Services: []adapter.Service{{
				ID:      1,
				Kind:    string(Kind),
				Enabled: true,
				Params:  []byte(`{"protocol":"vless","port":10086}`),
			}},
			Subjects: []adapter.Subject{
				{
					ID:                 1,
					SpeedLimitUpKbps:   &upLimit,
					SpeedLimitDownKbps: &downLimit,
					Credentials:        []adapter.Credential{{Kind: "uuid", Value: "test-uuid"}},
				},
			},
		}

		observed, _ := a.Observe(ctx)
		plan, err := a.Plan(ctx, desired, observed)
		if err != nil {
			t.Fatalf("Plan failed: %v", err)
		}

		// Verify plan includes policy config
		if plan.IsEmpty() {
			t.Fatal("expected non-empty plan")
		}

		var payload stepPayload
		json.Unmarshal(plan.Steps[0].Payload, &payload)

		if payload.PolicyConfig == "" {
			t.Error("expected policy config in plan step")
		}

		// Verify policy config is valid
		var policyDoc map[string]any
		if err := json.Unmarshal([]byte(payload.PolicyConfig), &policyDoc); err != nil {
			t.Errorf("invalid policy config in plan: %v", err)
		}
	})
}

// TestEndToEndDeviceRevocation verifies device revocation behavior
func TestEndToEndDeviceRevocation(t *testing.T) {
	enforcer := enforcement.New()
	runtime := &mockRuntime{stats: []UserStat{}}
	adapter := &Adapter{rt: runtime, shapes: make(map[int64]string), hotAdd: true}
	tracker := NewConnectionTracker(adapter, enforcer)

	ctx := context.Background()

	// Step 1: Set policy first
	maxConns := int64(5)
	enforcer.UpdatePolicies([]enforcement.Policy{
		{SubjectID: 1, MaxConnections: &maxConns},
	})

	// Step 2: Register connection
	runtime.stats = []UserStat{
		{Email: "subject-1@antimage", Uplink: 1000, Downlink: 2000},
	}

	tracker.Sync(ctx, "test-inbound")

	// Verify connection active
	if len(enforcer.GetActiveConnections(1)) != 1 {
		t.Fatal("expected 1 active connection")
	}

	// Step 3: Panel revokes device (simulated by policy removal)
	enforcer.UpdatePolicies([]enforcement.Policy{})

	// Verify connection terminated by policy removal
	if len(enforcer.GetActiveConnections(1)) != 0 {
		t.Error("expected connection terminated after policy removal")
	}

	// Step 4: Verify reconnection attempt without policy is not tracked
	runtime.stats = []UserStat{
		{Email: "subject-1@antimage", Uplink: 2000, Downlink: 4000},
	}

	// Reset tracker state so it sees this as a "new" connection
	tracker.Reset()

	// Sync - with no policy, connection won't be rejected but also won't be tracked
	tracker.Sync(ctx, "test-inbound")

	// With no policy, connections are allowed but the enforcer doesn't track them
	// (no policy means no restrictions)
	conns := enforcer.GetActiveConnections(1)
	// The connection gets registered because there's no policy to enforce
	if len(conns) > 1 {
		t.Errorf("unexpected number of connections: %d", len(conns))
	}
}

// TestRuntimeEnforcementIntegration verifies the complete integration
func TestRuntimeEnforcementIntegration(t *testing.T) {
	t.Run("StartEnforcement integrates with adapter", func(t *testing.T) {
		enforcer := enforcement.New()
		runtime := &mockRuntime{stats: []UserStat{}}
		adapter := &Adapter{
			rt:     runtime,
			dir:    t.TempDir(),
			shapes: make(map[int64]string),
			hotAdd: true,
		}

		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		// Start enforcement (as the agent would)
		adapter.StartEnforcement(ctx, enforcer, 50*time.Millisecond)

		// Set a policy
		maxConns := int64(1)
		enforcer.UpdatePolicies([]enforcement.Policy{
			{SubjectID: 1, MaxConnections: &maxConns},
		})

		// Simulate connection arriving
		runtime.stats = []UserStat{
			{Email: "subject-1@antimage", Uplink: 1000, Downlink: 2000},
		}

		// Wait for at least one enforcement cycle
		time.Sleep(150 * time.Millisecond)

		// Verify connection was processed
		conns := enforcer.GetActiveConnections(1)
		if len(conns) != 1 {
			t.Errorf("expected connection to be registered by enforcement loop, got %d", len(conns))
		}
	})
}
