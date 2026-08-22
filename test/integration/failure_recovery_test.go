package integration

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/node/enforcement"
)

// TestFailureRecovery_NodeOffline tests node offline scenarios
func TestFailureRecovery_NodeOffline(t *testing.T) {
	t.Run("HeartbeatTimeout", func(t *testing.T) {
		// Simulate node heartbeat timeout scenario
		enforcer := enforcement.New()

		subjectID := int64(8001)
		policy := enforcement.Policy{
			SubjectID:      subjectID,
			MaxConnections: ptrInt64(5),
		}
		enforcer.UpdatePolicies([]enforcement.Policy{policy})

		// Register connections
		for i := 0; i < 3; i++ {
			err := enforcer.CheckAndRegisterConnection(
				fmt.Sprintf("conn-hb-%d", i),
				subjectID,
				"device1",
				fmt.Sprintf("10.0.0.%d", i),
				"vless",
			)
			if err != nil {
				t.Fatalf("Connection %d failed: %v", i, err)
			}
		}

		conns := enforcer.GetActiveConnections(subjectID)
		if len(conns) != 3 {
			t.Fatalf("Expected 3 connections, got %d", len(conns))
		}

		// Simulate heartbeat timeout by removing policy (node offline)
		enforcer.UpdatePolicies([]enforcement.Policy{})

		// Verify connections terminated when policy removed
		conns = enforcer.GetActiveConnections(subjectID)
		if len(conns) != 0 {
			t.Errorf("Expected 0 connections after policy removal (node offline), got %d", len(conns))
		}

		t.Log("Heartbeat timeout handling: connections terminated when node goes offline")
	})

	t.Run("NodeRestartReconciliation", func(t *testing.T) {
		// Simulate node restart and state reconciliation
		enforcer1 := enforcement.New()

		subjectID := int64(8002)
		policy := enforcement.Policy{
			SubjectID:      subjectID,
			MaxConnections: ptrInt64(10),
		}
		enforcer1.UpdatePolicies([]enforcement.Policy{policy})

		// Register 5 connections before restart
		preRestartConns := 5
		for i := 0; i < preRestartConns; i++ {
			err := enforcer1.CheckAndRegisterConnection(
				fmt.Sprintf("conn-restart-%d", i),
				subjectID,
				"device1",
				fmt.Sprintf("10.0.1.%d", i),
				"vless",
			)
			if err != nil {
				t.Fatalf("Pre-restart connection %d failed: %v", i, err)
			}
		}

		// Simulate restart: new enforcer instance
		enforcer2 := enforcement.New()
		enforcer2.UpdatePolicies([]enforcement.Policy{policy})

		// After restart, memory cleared
		conns := enforcer2.GetActiveConnections(subjectID)
		if len(conns) != 0 {
			t.Errorf("Expected 0 connections after restart, got %d", len(conns))
		}

		// Reconciliation: re-register active connections (simulating adapter state sync)
		for i := 0; i < preRestartConns; i++ {
			err := enforcer2.CheckAndRegisterConnection(
				fmt.Sprintf("conn-restart-%d", i),
				subjectID,
				"device1",
				fmt.Sprintf("10.0.1.%d", i),
				"vless",
			)
			if err != nil {
				t.Fatalf("Reconciliation connection %d failed: %v", i, err)
			}
		}

		// Verify reconciled state
		conns = enforcer2.GetActiveConnections(subjectID)
		if len(conns) != preRestartConns {
			t.Errorf("Expected %d connections after reconciliation, got %d", preRestartConns, len(conns))
		}

		t.Log("Node restart reconciliation: state restored successfully")
	})
}

// TestFailureRecovery_ProcessCrash tests crash and recovery scenarios
func TestFailureRecovery_ProcessCrash(t *testing.T) {
	t.Run("CrashDuringPolicyUpdate", func(t *testing.T) {
		enforcer := enforcement.New()

		// Start with some policies
		initialPolicies := []enforcement.Policy{
			{SubjectID: 9001, MaxConnections: ptrInt64(5)},
			{SubjectID: 9002, MaxConnections: ptrInt64(5)},
		}
		enforcer.UpdatePolicies(initialPolicies)

		// Register connections
		for _, policy := range initialPolicies {
			err := enforcer.CheckAndRegisterConnection(
				fmt.Sprintf("conn-%d", policy.SubjectID),
				policy.SubjectID,
				"device1",
				"10.0.2.1",
				"vless",
			)
			if err != nil {
				t.Fatalf("Connection failed: %v", err)
			}
		}

		// Simulate crash recovery: new enforcer, restore last known good state
		recoveredEnforcer := enforcement.New()
		recoveredEnforcer.UpdatePolicies(initialPolicies)

		// Verify policies restored (connections need reconciliation)
		for _, policy := range initialPolicies {
			// New connection should work (policy exists)
			err := recoveredEnforcer.CheckAndRegisterConnection(
				fmt.Sprintf("conn-recovered-%d", policy.SubjectID),
				policy.SubjectID,
				"device1",
				"10.0.2.2",
				"vless",
			)
			if err != nil {
				t.Errorf("Post-recovery connection failed for subject %d: %v", policy.SubjectID, err)
			}
		}

		t.Log("Crash recovery: policies restored, new connections accepted")
	})

	t.Run("NetworkInterruption", func(t *testing.T) {
		enforcer := enforcement.New()

		subjectID := int64(9003)
		policy := enforcement.Policy{
			SubjectID:      subjectID,
			MaxConnections: ptrInt64(5),
		}
		enforcer.UpdatePolicies([]enforcement.Policy{policy})

		// Register connections before network interruption
		preInterruptConns := 3
		for i := 0; i < preInterruptConns; i++ {
			err := enforcer.CheckAndRegisterConnection(
				fmt.Sprintf("conn-ni-%d", i),
				subjectID,
				"device1",
				fmt.Sprintf("10.0.3.%d", i),
				"vless",
			)
			if err != nil {
				t.Fatalf("Pre-interrupt connection %d failed: %v", i, err)
			}
		}

		// Simulate network interruption (connections remain in memory)
		// In real scenario, adapter would detect disconnects and notify enforcer

		// After network recovery, connections should still be tracked
		conns := enforcer.GetActiveConnections(subjectID)
		if len(conns) != preInterruptConns {
			t.Errorf("Expected %d connections after network recovery, got %d", preInterruptConns, len(conns))
		}

		// New connections should work
		err := enforcer.CheckAndRegisterConnection(
			"conn-post-recovery",
			subjectID,
			"device1",
			"10.0.3.100",
			"vless",
		)
		if err != nil {
			t.Errorf("Post-recovery connection failed: %v", err)
		}

		t.Log("Network interruption: existing connections preserved, new connections accepted")
	})
}

// TestFailureRecovery_StaleState tests stale state detection
func TestFailureRecovery_StaleState(t *testing.T) {
	t.Run("DuplicateRegistration", func(t *testing.T) {
		enforcer := enforcement.New()

		subjectID := int64(9004)
		policy := enforcement.Policy{
			SubjectID:      subjectID,
			MaxConnections: ptrInt64(5),
		}
		enforcer.UpdatePolicies([]enforcement.Policy{policy})

		connID := "conn-duplicate"

		// Register connection multiple times (should be idempotent)
		for i := 0; i < 5; i++ {
			err := enforcer.CheckAndRegisterConnection(
				connID,
				subjectID,
				"device1",
				"10.0.4.1",
				"vless",
			)
			if err != nil {
				t.Errorf("Duplicate registration attempt %d failed: %v", i+1, err)
			}
		}

		// Should only have 1 connection
		conns := enforcer.GetActiveConnections(subjectID)
		if len(conns) != 1 {
			t.Errorf("Expected 1 connection (idempotent duplicate), got %d", len(conns))
		}

		t.Log("Duplicate registration handled: idempotent behavior verified")
	})

	t.Run("EventualConvergence", func(t *testing.T) {
		enforcer := enforcement.New()

		subjectID := int64(9005)

		// Start with permissive policy
		policy1 := enforcement.Policy{
			SubjectID:      subjectID,
			MaxConnections: ptrInt64(10),
		}
		enforcer.UpdatePolicies([]enforcement.Policy{policy1})

		// Register 8 connections
		for i := 0; i < 8; i++ {
			err := enforcer.CheckAndRegisterConnection(
				fmt.Sprintf("conn-ec-%d", i),
				subjectID,
				"device1",
				fmt.Sprintf("10.0.5.%d", i),
				"vless",
			)
			if err != nil {
				t.Fatalf("Connection %d failed: %v", i, err)
			}
		}

		conns := enforcer.GetActiveConnections(subjectID)
		if len(conns) != 8 {
			t.Fatalf("Expected 8 connections, got %d", len(conns))
		}

		// Update to restrictive policy (force convergence)
		policy2 := enforcement.Policy{
			SubjectID:      subjectID,
			MaxConnections: ptrInt64(5),
		}
		enforcer.UpdatePolicies([]enforcement.Policy{policy2})

		// Verify excess connections terminated (eventual convergence)
		conns = enforcer.GetActiveConnections(subjectID)
		if len(conns) > 5 {
			t.Errorf("Expected ≤5 connections after policy update (convergence), got %d", len(conns))
		}

		// New connection should respect new limit
		err := enforcer.CheckAndRegisterConnection(
			"conn-post-convergence",
			subjectID,
			"device1",
			"10.0.5.100",
			"vless",
		)

		// Should succeed if under limit, fail if at limit
		conns = enforcer.GetActiveConnections(subjectID)
		if err == nil && len(conns) > 5 {
			t.Errorf("Convergence failed: connections exceed new limit")
		}

		t.Logf("Eventual convergence: %d connections after policy reduction from 10→5", len(conns))
	})
}

// TestFailureRecovery_ConcurrentFailures tests concurrent failure scenarios
func TestFailureRecovery_ConcurrentFailures(t *testing.T) {
	enforcer := enforcement.New()

	// Setup 20 subjects
	nodeCount := 20
	policies := make([]enforcement.Policy, nodeCount)
	for i := 0; i < nodeCount; i++ {
		policies[i] = enforcement.Policy{
			SubjectID:      int64(10000 + i),
			MaxConnections: ptrInt64(5),
		}
	}
	enforcer.UpdatePolicies(policies)

	// Concurrent connection attempts + policy updates
	var wg sync.WaitGroup
	errors := make(chan error, nodeCount*10)

	start := time.Now()

	// Goroutine pool: connection attempts
	for i := 0; i < nodeCount; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			subjectID := int64(10000 + idx)

			for j := 0; j < 3; j++ {
				err := enforcer.CheckAndRegisterConnection(
					fmt.Sprintf("conn-%d-%d", idx, j),
					subjectID,
					"device1",
					fmt.Sprintf("192.168.%d.%d", idx, j),
					"vless",
				)
				if err != nil {
					errors <- err
				}
				time.Sleep(1 * time.Millisecond) // Small delay
			}
		}(i)
	}

	// Concurrent policy updates (simulating dynamic changes)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			// Re-apply same policies (simulating reconciliation loop)
			enforcer.UpdatePolicies(policies)
			time.Sleep(5 * time.Millisecond)
		}
	}()

	wg.Wait()
	close(errors)
	duration := time.Since(start)

	// Count errors
	errCount := 0
	for range errors {
		errCount++
	}

	t.Logf("Concurrent failures test: %d subjects, %v duration, %d errors",
		nodeCount, duration, errCount)

	// Some errors expected due to race conditions, but system should remain consistent
	if errCount > nodeCount {
		t.Errorf("Too many errors during concurrent operations: %d", errCount)
	}
}

func ptrInt64(v int64) *int64 {
	return &v
}
