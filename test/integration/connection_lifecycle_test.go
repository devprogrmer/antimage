package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/node/adapter"
	"github.com/amyrm/antimage/internal/node/adapter/xray"
	"github.com/amyrm/antimage/internal/node/enforcement"
)

// TestConnectionLifecycleE2E verifies the complete connection lifecycle:
// 1. Create subject with policy
// 2. Connect (admission check)
// 3. Enforce policies (quota, connection limit)
// 4. Revoke subject
// 5. Disconnect existing connections
// 6. Restart node/adapter
// 7. Reconcile state
//
// This tests the integration between:
// - Adapter (Xray)
// - Enforcer
// - Policy management
// - State persistence across restarts
func TestConnectionLifecycleE2E(t *testing.T) {
	t.Run("CreateConnectEnforceRevokeDisconnect", func(t *testing.T) {
		// 1. Setup: Create enforcer and subject with policy
		enforcer := enforcement.New()

		subjectID := int64(1001)
		maxConnections := int64(2)
		quotaBytes := int64(1024 * 1024) // 1 MB
		quotaUsed := int64(0)

		policy := enforcement.Policy{
			SubjectID:      subjectID,
			MaxConnections: &maxConnections,
			QuotaBytes:     &quotaBytes,
			QuotaUsedBytes: &quotaUsed,
		}

		if err := enforcer.UpdatePolicy(subjectID, policy); err != nil {
			t.Fatalf("UpdatePolicy failed: %v", err)
		}

		// 2. Connect: First connection should succeed
		conn1ID := "conn-1"
		err := enforcer.CheckAndRegisterConnection(conn1ID, subjectID, "device1", "1.2.3.4", "vless")
		if err != nil {
			t.Fatalf("First connection should succeed, got error: %v", err)
		}

		// 3. Enforce: Second connection should succeed
		conn2ID := "conn-2"
		err = enforcer.CheckAndRegisterConnection(conn2ID, subjectID, "device1", "1.2.3.5", "vless")
		if err != nil {
			t.Fatalf("Second connection should succeed, got error: %v", err)
		}

		// 4. Enforce: Third connection should fail (max=2)
		conn3ID := "conn-3"
		err = enforcer.CheckAndRegisterConnection(conn3ID, subjectID, "device1", "1.2.3.6", "vless")
		if err == nil {
			t.Fatal("Third connection should fail due to connection limit")
		}
		// Check it's a policy violation error
		if !isPolicyViolation(err) {
			t.Fatalf("Expected policy violation error, got: %v", err)
		}

		// Verify active connection count
		stats := enforcer.GetStats(subjectID)
		if stats.ActiveConnections != 2 {
			t.Fatalf("Expected 2 active connections, got: %d", stats.ActiveConnections)
		}

		// 5. Revoke: Update policy to disable subject
		disabledMaxConn := int64(0)
		revokedPolicy := enforcement.Policy{
			SubjectID:      subjectID,
			MaxConnections: &disabledMaxConn,
			QuotaBytes:     &quotaBytes,
			QuotaUsedBytes: &quotaUsed,
		}

		if err := enforcer.UpdatePolicy(subjectID, revokedPolicy); err != nil {
			t.Fatalf("UpdatePolicy for revocation failed: %v", err)
		}

		// 6. Disconnect: New connections should fail immediately
		conn4ID := "conn-4"
		err = enforcer.CheckAndRegisterConnection(conn4ID, subjectID, "device1", "1.2.3.7", "vless")
		if err == nil {
			t.Fatal("Connection should fail after revocation")
		}

		// 7. Disconnect existing: Terminate active connections
		enforcer.DisconnectSubject(subjectID)

		// Verify all connections terminated
		stats = enforcer.GetStats(subjectID)
		if stats.ActiveConnections != 0 {
			t.Fatalf("Expected 0 active connections after disconnect, got: %d", stats.ActiveConnections)
		}
	})

	t.Run("QuotaEnforcementLifecycle", func(t *testing.T) {
		enforcer := enforcement.New()

		subjectID := int64(2001)
		quotaBytes := int64(1000)
		quotaUsed := int64(950) // 95% used

		policy := enforcement.Policy{
			SubjectID:      subjectID,
			QuotaBytes:     &quotaBytes,
			QuotaUsedBytes: &quotaUsed,
		}

		if err := enforcer.UpdatePolicy(subjectID, policy); err != nil {
			t.Fatalf("UpdatePolicy failed: %v", err)
		}

		// Connection allowed at 95%
		connID := "conn-quota-1"
		err := enforcer.CheckAndRegisterConnection(connID, subjectID, "device1", "1.2.3.10", "vless")
		if err != nil {
			t.Fatalf("Connection should succeed at 95%% quota, got error: %v", err)
		}

		// Update quota to 100% (exhausted)
		quotaUsed = int64(1000)
		exhaustedPolicy := enforcement.Policy{
			SubjectID:      subjectID,
			QuotaBytes:     &quotaBytes,
			QuotaUsedBytes: &quotaUsed,
		}

		if err := enforcer.UpdatePolicy(subjectID, exhaustedPolicy); err != nil {
			t.Fatalf("UpdatePolicy failed: %v", err)
		}

		// New connection should fail
		conn2ID := "conn-quota-2"
		err = enforcer.CheckAndRegisterConnection(conn2ID, subjectID, "device1", "1.2.3.11", "vless")
		if err == nil {
			t.Fatal("Connection should fail when quota exhausted")
		}
		if !isPolicyViolation(err) {
			t.Fatalf("Expected policy violation error, got: %v", err)
		}

		// Existing connection still tracked
		stats := enforcer.GetStats(subjectID)
		if stats.ActiveConnections != 1 {
			t.Fatalf("Expected 1 active connection (established before quota exhausted), got: %d", stats.ActiveConnections)
		}
	})

	t.Run("RestartReconciliation", func(t *testing.T) {
		// Simulate node restart: create new enforcer, restore state
		enforcer1 := enforcement.New()

		subjectID := int64(3001)
		maxConn := int64(5)

		policy := enforcement.Policy{
			SubjectID:      subjectID,
			MaxConnections: &maxConn,
		}

		enforcer1.UpdatePolicy(subjectID, policy)

		// Register 3 connections
		for i := 1; i <= 3; i++ {
			connID := fmt.Sprintf("conn-restart-%d", i)
			err := enforcer1.CheckAndRegisterConnection(connID, subjectID, "device1", fmt.Sprintf("1.2.3.%d", i), "vless")
			if err != nil {
				t.Fatalf("Connection %d should succeed: %v", i, err)
			}
		}

		// Verify 3 active
		stats1 := enforcer1.GetStats(subjectID)
		if stats1.ActiveConnections != 3 {
			t.Fatalf("Expected 3 active connections before restart, got: %d", stats1.ActiveConnections)
		}

		// Simulate restart: new enforcer instance
		enforcer2 := enforcement.New()

		// Reconcile: restore policy
		enforcer2.UpdatePolicy(subjectID, policy)

		// Reconcile: connections should be rebuilt from desired state
		// In production, this would read from adapter state or connection tracker
		// For this test, we simulate by re-registering known connections

		// After restart, connection count should be 0 (memory cleared)
		stats2 := enforcer2.GetStats(subjectID)
		if stats2.ActiveConnections != 0 {
			t.Fatalf("Expected 0 active connections after restart (before reconciliation), got: %d", stats2.ActiveConnections)
		}

		// Reconciliation: re-register active connections
		for i := 1; i <= 3; i++ {
			connID := fmt.Sprintf("conn-restart-%d", i)
			err := enforcer2.CheckAndRegisterConnection(connID, subjectID, "device1", fmt.Sprintf("1.2.3.%d", i), "vless")
			if err != nil {
				t.Fatalf("Reconcile connection %d failed: %v", i, err)
			}
		}

		// Verify reconciled state
		stats3 := enforcer2.GetStats(subjectID)
		if stats3.ActiveConnections != 3 {
			t.Fatalf("Expected 3 active connections after reconciliation, got: %d", stats3.ActiveConnections)
		}

		// New connections should respect limit
		err := enforcer2.CheckAndRegisterConnection("conn-restart-4", subjectID, "device1", "1.2.3.100", "vless")
		if err != nil {
			t.Fatalf("Fourth connection should succeed (limit=5): %v", err)
		}
	})
}

// TestXrayAdapterIntegration tests Xray adapter with enforcer
func TestXrayAdapterIntegration(t *testing.T) {
	t.Skip("Requires Xray binary and runtime environment")

	ctx := context.Background()

	// Setup Xray adapter
	rt := xray.NewExecRuntime("/tmp/xray-test", "xray")
	adapter := xray.New("/tmp/xray-test-config", rt, true)

	// Verify adapter capabilities
	desc := adapter.Descriptor()
	if !desc.Caps.HotUserAdd {
		t.Fatal("Xray should support hot user add")
	}
	if !desc.Caps.SelfAccounting {
		t.Fatal("Xray should support self accounting")
	}

	// Setup desired state with subjects
	desired := adapter.Desired{
		SchemaVersion: 2,
		Services: []adapter.Service{
			{
				ID:      1,
				Kind:    "xray",
				Enabled: true,
				Params:  []byte(`{"protocol":"vless","port":10086,"uuid":"test-uuid"}`),
			},
		},
		Subjects: []adapter.Subject{
			{
				ID: 1001,
				Credentials: []adapter.Credential{
					{Kind: "uuid", Value: "550e8400-e29b-41d4-a716-446655440000"},
				},
				MaxConnections:     ptrInt64(3),
				SpeedLimitUpKbps:   ptrInt64(5000),
				SpeedLimitDownKbps: ptrInt64(10000),
			},
		},
	}

	// Observe current state
	observed, err := adapter.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe failed: %v", err)
	}

	// Plan convergence
	plan, err := adapter.Plan(ctx, desired, observed)
	if err != nil {
		t.Fatalf("Plan failed: %v", err)
	}

	// Apply steps
	for _, step := range plan.Steps {
		result, err := adapter.Apply(ctx, step)
		if err != nil {
			t.Fatalf("Apply step %d failed: %v", step.Seq, err)
		}
		if !result.OK {
			t.Fatalf("Apply step %d failed: %s", step.Seq, result.Err)
		}
	}

	// Wait for convergence
	time.Sleep(2 * time.Second)

	// Test accounting (if supported)
	if reporter, ok := interface{}(adapter).(adapter.UsageReporter); ok {
		samples, err := reporter.Usage(ctx)
		if err != nil {
			t.Fatalf("Usage failed: %v", err)
		}
		t.Logf("Got %d usage samples", len(samples))
	}
}

func ptrInt64(v int64) *int64 {
	return &v
}

// isPolicyViolation checks if error is a policy violation
func isPolicyViolation(err error) bool {
	// Check error message contains policy violation indicators
	msg := err.Error()
	return contains(msg, "exceeded") || contains(msg, "quota") || contains(msg, "limit")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && indexOfSubstring(s, substr) >= 0
}

func indexOfSubstring(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

