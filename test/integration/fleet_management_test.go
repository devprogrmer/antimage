package integration

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/node/enforcement"
)

// TestFleetManagement_BulkOperations tests bulk node operations at scale
func TestFleetManagement_BulkOperations(t *testing.T) {
	t.Run("Bulk100NodeRegistration", func(t *testing.T) {
		enforcer := enforcement.New()

		// Create policies for 100 subjects
		nodeCount := 100
		policies := make([]enforcement.Policy, nodeCount)

		for i := 0; i < nodeCount; i++ {
			maxConn := int64(5)
			quotaBytes := int64(1024 * 1024 * 100) // 100 MB per subject
			quotaUsed := int64(0)

			policies[i] = enforcement.Policy{
				SubjectID:      int64(1000 + i),
				MaxConnections: &maxConn,
				QuotaBytes:     &quotaBytes,
				QuotaUsedBytes: &quotaUsed,
			}
		}

		// Bulk update all policies
		start := time.Now()
		enforcer.UpdatePolicies(policies)
		duration := time.Since(start)

		t.Logf("Bulk update of %d policies completed in %v", nodeCount, duration)

		// Verify a sample of policies are active
		for i := 0; i < 10; i++ {
			subjectID := int64(1000 + i*10)
			err := enforcer.CheckAndRegisterConnection(
				fmt.Sprintf("conn-%d", i),
				subjectID,
				"device1",
				fmt.Sprintf("10.0.%d.%d", i/256, i%256),
				"vless",
			)
			if err != nil {
				t.Errorf("Connection check failed for subject %d: %v", subjectID, err)
			}
		}
	})

	t.Run("ConcurrentNodeOperations", func(t *testing.T) {
		enforcer := enforcement.New()

		// Setup 50 subjects
		nodeCount := 50
		policies := make([]enforcement.Policy, nodeCount)

		for i := 0; i < nodeCount; i++ {
			maxConn := int64(10)
			quotaBytes := int64(1024 * 1024 * 50)
			quotaUsed := int64(0)

			policies[i] = enforcement.Policy{
				SubjectID:      int64(2000 + i),
				MaxConnections: &maxConn,
				QuotaBytes:     &quotaBytes,
				QuotaUsedBytes: &quotaUsed,
			}
		}

		enforcer.UpdatePolicies(policies)

		// Concurrent connection attempts from different goroutines
		var wg sync.WaitGroup
		errors := make(chan error, nodeCount*5)

		start := time.Now()
		for i := 0; i < nodeCount; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				subjectID := int64(2000 + idx)

				// Each goroutine attempts 5 connections
				for j := 0; j < 5; j++ {
					connID := fmt.Sprintf("conn-concurrent-%d-%d", idx, j)
					err := enforcer.CheckAndRegisterConnection(
						connID,
						subjectID,
						"device1",
						fmt.Sprintf("192.168.%d.%d", idx, j),
						"vless",
					)
					if err != nil {
						errors <- fmt.Errorf("subject %d conn %d: %w", subjectID, j, err)
					}
				}
			}(i)
		}

		wg.Wait()
		close(errors)
		duration := time.Since(start)

		// Check for errors
		errCount := 0
		for err := range errors {
			t.Logf("Concurrent error: %v", err)
			errCount++
		}

		t.Logf("Concurrent operations: %d subjects × 5 connections in %v, %d errors",
			nodeCount, duration, errCount)

		if errCount > 0 {
			t.Errorf("Expected no errors in concurrent operations, got %d", errCount)
		}
	})

	t.Run("PartialFailureHandling", func(t *testing.T) {
		enforcer := enforcement.New()

		// Create mix of valid and over-quota subjects
		policies := []enforcement.Policy{
			{
				SubjectID:      3001,
				MaxConnections: ptrInt64(5),
				QuotaBytes:     ptrInt64(1000),
				QuotaUsedBytes: ptrInt64(0), // Valid
			},
			{
				SubjectID:      3002,
				MaxConnections: ptrInt64(5),
				QuotaBytes:     ptrInt64(1000),
				QuotaUsedBytes: ptrInt64(1000), // Quota exhausted
			},
			{
				SubjectID:      3003,
				MaxConnections: ptrInt64(0), // Revoked
				QuotaBytes:     ptrInt64(1000),
				QuotaUsedBytes: ptrInt64(0),
			},
		}

		enforcer.UpdatePolicies(policies)

		// Attempt connections to all subjects
		results := make(map[int64]error)

		for _, policy := range policies {
			err := enforcer.CheckAndRegisterConnection(
				fmt.Sprintf("conn-%d", policy.SubjectID),
				policy.SubjectID,
				"device1",
				"1.2.3.4",
				"vless",
			)
			results[policy.SubjectID] = err
		}

		// Verify expected outcomes
		if results[3001] != nil {
			t.Errorf("Subject 3001 (valid) should succeed, got: %v", results[3001])
		}
		if results[3002] == nil {
			t.Error("Subject 3002 (quota exhausted) should fail")
		}
		if results[3003] == nil {
			t.Error("Subject 3003 (revoked) should fail")
		}

		t.Logf("Partial failure handling: 1 success, 2 expected failures")
	})
}

// TestFleetManagement_StateTransitions tests node state changes
func TestFleetManagement_StateTransitions(t *testing.T) {
	t.Run("PolicyUpdatesWithStateChanges", func(t *testing.T) {
		enforcer := enforcement.New()

		subjectID := int64(4001)

		// State 1: Active with quota
		policy1 := enforcement.Policy{
			SubjectID:      subjectID,
			MaxConnections: ptrInt64(5),
			QuotaBytes:     ptrInt64(10000),
			QuotaUsedBytes: ptrInt64(0),
		}
		enforcer.UpdatePolicies([]enforcement.Policy{policy1})

		// Register connection
		err := enforcer.CheckAndRegisterConnection("conn-1", subjectID, "dev1", "1.2.3.4", "vless")
		if err != nil {
			t.Fatalf("State 1 connection should succeed: %v", err)
		}

		// State 2: Reduce connection limit (force disconnect)
		policy2 := enforcement.Policy{
			SubjectID:      subjectID,
			MaxConnections: ptrInt64(0), // Revoked
			QuotaBytes:     ptrInt64(10000),
			QuotaUsedBytes: ptrInt64(0),
		}
		enforcer.UpdatePolicies([]enforcement.Policy{policy2})

		// Verify existing connections terminated
		conns := enforcer.GetActiveConnections(subjectID)
		if len(conns) != 0 {
			t.Errorf("State 2: Expected 0 connections after revocation, got %d", len(conns))
		}

		// State 3: Re-enable with new quota
		policy3 := enforcement.Policy{
			SubjectID:      subjectID,
			MaxConnections: ptrInt64(10),
			QuotaBytes:     ptrInt64(20000),
			QuotaUsedBytes: ptrInt64(0),
		}
		enforcer.UpdatePolicies([]enforcement.Policy{policy3})

		// New connection should work
		err = enforcer.CheckAndRegisterConnection("conn-2", subjectID, "dev1", "1.2.3.5", "vless")
		if err != nil {
			t.Fatalf("State 3 connection should succeed: %v", err)
		}

		t.Log("State transitions: active → revoked → re-enabled")
	})

	t.Run("TenantIsolation", func(t *testing.T) {
		enforcer := enforcement.New()

		// Create subjects in different "tenants" (using ID ranges)
		tenant1Subjects := []enforcement.Policy{
			{SubjectID: 5001, MaxConnections: ptrInt64(5)},
			{SubjectID: 5002, MaxConnections: ptrInt64(5)},
		}
		tenant2Subjects := []enforcement.Policy{
			{SubjectID: 6001, MaxConnections: ptrInt64(5)},
			{SubjectID: 6002, MaxConnections: ptrInt64(5)},
		}

		allPolicies := append(tenant1Subjects, tenant2Subjects...)
		enforcer.UpdatePolicies(allPolicies)

		// Register connections for tenant 1
		for _, policy := range tenant1Subjects {
			err := enforcer.CheckAndRegisterConnection(
				fmt.Sprintf("conn-t1-%d", policy.SubjectID),
				policy.SubjectID,
				"device1",
				"10.1.1.1",
				"vless",
			)
			if err != nil {
				t.Errorf("Tenant 1 connection failed: %v", err)
			}
		}

		// Register connections for tenant 2
		for _, policy := range tenant2Subjects {
			err := enforcer.CheckAndRegisterConnection(
				fmt.Sprintf("conn-t2-%d", policy.SubjectID),
				policy.SubjectID,
				"device1",
				"10.2.1.1",
				"vless",
			)
			if err != nil {
				t.Errorf("Tenant 2 connection failed: %v", err)
			}
		}

		// Verify tenant 1 connections
		t1Conns := 0
		for _, policy := range tenant1Subjects {
			conns := enforcer.GetActiveConnections(policy.SubjectID)
			t1Conns += len(conns)
		}

		// Verify tenant 2 connections
		t2Conns := 0
		for _, policy := range tenant2Subjects {
			conns := enforcer.GetActiveConnections(policy.SubjectID)
			t2Conns += len(conns)
		}

		if t1Conns != 2 {
			t.Errorf("Tenant 1 should have 2 connections, got %d", t1Conns)
		}
		if t2Conns != 2 {
			t.Errorf("Tenant 2 should have 2 connections, got %d", t2Conns)
		}

		t.Logf("Tenant isolation: tenant1=%d conns, tenant2=%d conns", t1Conns, t2Conns)
	})
}

// TestFleetManagement_Idempotency tests retry and idempotency
func TestFleetManagement_Idempotency(t *testing.T) {
	enforcer := enforcement.New()

	subjectID := int64(7001)
	policy := enforcement.Policy{
		SubjectID:      subjectID,
		MaxConnections: ptrInt64(5),
		QuotaBytes:     ptrInt64(10000),
		QuotaUsedBytes: ptrInt64(0),
	}

	// Apply policy multiple times (simulating retries)
	for i := 0; i < 5; i++ {
		enforcer.UpdatePolicies([]enforcement.Policy{policy})
	}

	// Register same connection multiple times (idempotent)
	connID := "conn-idempotent-1"
	for i := 0; i < 3; i++ {
		err := enforcer.CheckAndRegisterConnection(connID, subjectID, "dev1", "1.2.3.4", "vless")
		if err != nil {
			t.Errorf("Idempotent connection registration failed on attempt %d: %v", i+1, err)
		}
	}

	// Should only have 1 connection despite 3 registrations
	conns := enforcer.GetActiveConnections(subjectID)
	if len(conns) != 1 {
		t.Errorf("Expected 1 connection (idempotent), got %d", len(conns))
	}

	t.Log("Idempotency verified: multiple policy updates and connection registrations")
}
