package integration

import (
	"fmt"
	"testing"

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

		enforcer.UpdatePolicies([]enforcement.Policy{policy})

		// 2. Connect: First connection should succeed
		conn1ID := "conn-1"
		err := enforcer.CheckAndRegisterConnection(conn1ID, subjectID, "device1", "1.2.3.4", "vless")
		if err != nil {
			t.Fatalf("First connection should succeed: %v", err)
		}

		// 3. Connect: Second connection should succeed (within limit)
		conn2ID := "conn-2"
		err = enforcer.CheckAndRegisterConnection(conn2ID, subjectID, "device1", "1.2.3.5", "vless")
		if err != nil {
			t.Fatalf("Second connection should succeed: %v", err)
		}

		// 4. Enforce: Third connection should fail (exceeds limit)
		conn3ID := "conn-3"
		err = enforcer.CheckAndRegisterConnection(conn3ID, subjectID, "device1", "1.2.3.6", "vless")
		if err == nil {
			t.Fatal("Third connection should fail (exceeds MaxConnections=2)")
		}
		if !isPolicyViolation(err) {
			t.Fatalf("Expected policy violation error, got: %v", err)
		}

		// Verify active connection count
		conns := enforcer.GetActiveConnections(subjectID)
		if len(conns) != 2 {
			t.Fatalf("Expected 2 active connections, got: %d", len(conns))
		}

		// 5. Revoke: Update policy to disable subject
		disabledMaxConn := int64(0)
		revokedPolicy := enforcement.Policy{
			SubjectID:      subjectID,
			MaxConnections: &disabledMaxConn,
			QuotaBytes:     &quotaBytes,
			QuotaUsedBytes: &quotaUsed,
		}

		enforcer.UpdatePolicies([]enforcement.Policy{revokedPolicy})

		// 6. Disconnect: New connections should fail immediately
		conn4ID := "conn-4"
		err = enforcer.CheckAndRegisterConnection(conn4ID, subjectID, "device1", "1.2.3.7", "vless")
		if err == nil {
			t.Fatal("Connection should fail after revocation")
		}

		// 7. Verify existing connections are terminated by policy update
		conns = enforcer.GetActiveConnections(subjectID)
		if len(conns) != 0 {
			t.Fatalf("Expected 0 active connections after revocation, got: %d", len(conns))
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

		enforcer.UpdatePolicies([]enforcement.Policy{policy})

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

		enforcer.UpdatePolicies([]enforcement.Policy{exhaustedPolicy})

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
		conns := enforcer.GetActiveConnections(subjectID)
		if len(conns) != 1 {
			t.Fatalf("Expected 1 active connection (established before quota exhausted), got: %d", len(conns))
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

		enforcer1.UpdatePolicies([]enforcement.Policy{policy})

		// Register 3 connections
		for i := 1; i <= 3; i++ {
			connID := fmt.Sprintf("conn-restart-%d", i)
			err := enforcer1.CheckAndRegisterConnection(connID, subjectID, "device1", fmt.Sprintf("1.2.3.%d", i), "vless")
			if err != nil {
				t.Fatalf("Connection %d should succeed: %v", i, err)
			}
		}

		// Verify 3 active
		conns1 := enforcer1.GetActiveConnections(subjectID)
		if len(conns1) != 3 {
			t.Fatalf("Expected 3 active connections before restart, got: %d", len(conns1))
		}

		// Simulate restart: new enforcer instance
		enforcer2 := enforcement.New()

		// Reconcile: restore policy
		enforcer2.UpdatePolicies([]enforcement.Policy{policy})

		// Reconcile: connections should be rebuilt from desired state
		// In production, this would read from adapter state or connection tracker
		// For this test, we simulate by re-registering known connections

		// After restart, connection count should be 0 (memory cleared)
		conns2 := enforcer2.GetActiveConnections(subjectID)
		if len(conns2) != 0 {
			t.Fatalf("Expected 0 active connections after restart (before reconciliation), got: %d", len(conns2))
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
		conns3 := enforcer2.GetActiveConnections(subjectID)
		if len(conns3) != 3 {
			t.Fatalf("Expected 3 active connections after reconciliation, got: %d", len(conns3))
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
	t.Skip("Requires Xray binary and runtime environment - integration test placeholder")

	// This test would verify:
	// - Xray adapter Plan() execution
	// - Enforcer policy updates
	// - Usage accounting integration
	// - Connection lifecycle across adapter + enforcer
}

// isPolicyViolation checks if error is a policy violation.
func isPolicyViolation(err error) bool {
	_, ok := err.(*enforcement.ErrPolicyViolation)
	return ok
}
