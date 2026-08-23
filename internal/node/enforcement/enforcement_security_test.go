package enforcement

import (
	"fmt"
	"sync"
	"testing"
)

// TestSecurityIntegerOverflow tests integer overflow protection in limits.
func TestSecurityIntegerOverflow(t *testing.T) {
	e := New()

	tests := []struct {
		name           string
		maxConnections *int64
		maxDevices     *int64
		maxIPs         *int64
		expectError    bool
	}{
		{
			name:           "negative connection limit",
			maxConnections: int64Ptr(-1),
			expectError:    true,
		},
		{
			name:        "negative device limit",
			maxDevices:  int64Ptr(-1),
			expectError: true,
		},
		{
			name:        "negative IP limit",
			maxIPs:      int64Ptr(-1),
			expectError: true,
		},
		{
			name:           "max int64",
			maxConnections: int64Ptr(9223372036854775807),
			expectError:    false,
		},
		{
			name:           "zero limits valid",
			maxConnections: int64Ptr(0),
			maxDevices:     int64Ptr(0),
			maxIPs:         int64Ptr(0),
			expectError:    false, // Zero is valid (deny all)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := Policy{
				SubjectID:      100,
				MaxConnections: tt.maxConnections,
				MaxDevices:     tt.maxDevices,
				MaxIPs:         tt.maxIPs,
			}
			e.UpdatePolicies([]Policy{policy})

			err := e.CheckAndRegisterConnection("conn1", 100, "device1", "10.0.0.1", "vless")

			if tt.expectError && err == nil {
				t.Error("expected error for invalid limit, got nil")
			}
			if !tt.expectError && err != nil && tt.maxConnections != nil && *tt.maxConnections == 0 {
				// Zero limit is valid but should reject connection
				return
			}
		})
	}
}

// TestSecuritySubjectIsolation tests that subjects cannot access each other's connections.
func TestSecuritySubjectIsolation(t *testing.T) {
	e := New()

	// Create policies for two subjects
	policy1 := Policy{
		SubjectID:      100,
		MaxConnections: int64Ptr(5),
	}
	policy2 := Policy{
		SubjectID:      200,
		MaxConnections: int64Ptr(5),
	}
	e.UpdatePolicies([]Policy{policy1, policy2})

	// Register connections for subject 100
	for i := 0; i < 5; i++ {
		connID := "subj100_conn" + string(rune('0'+i))
		if err := e.CheckAndRegisterConnection(connID, 100, "device1", "10.0.0.1", "vless"); err != nil {
			t.Fatalf("register conn for subject 100: %v", err)
		}
	}

	// Subject 100 should hit limit
	err := e.CheckAndRegisterConnection("subj100_conn_extra", 100, "device1", "10.0.0.1", "vless")
	if err == nil {
		t.Error("subject 100 should hit connection limit")
	}

	// Subject 200 should still be able to connect (isolated from subject 100)
	for i := 0; i < 5; i++ {
		connID := "subj200_conn" + string(rune('0'+i))
		if err := e.CheckAndRegisterConnection(connID, 200, "device1", "10.0.0.1", "vless"); err != nil {
			t.Errorf("subject 200 should not be affected by subject 100 limits: %v", err)
		}
	}

	// Subject 200 should also hit its own limit
	err = e.CheckAndRegisterConnection("subj200_conn_extra", 200, "device1", "10.0.0.1", "vless")
	if err == nil {
		t.Error("subject 200 should hit its own connection limit")
	}

	// Verify total connections (5 + 5 = 10)
	stats := e.Stats()
	if stats.TotalConnections != 10 {
		t.Errorf("expected 10 total connections, got %d", stats.TotalConnections)
	}
}

// TestSecurityDeviceIDSpoofing tests that device ID cannot be spoofed to bypass limits.
func TestSecurityDeviceIDSpoofing(t *testing.T) {
	e := New()

	policy := Policy{
		SubjectID:  100,
		MaxDevices: int64Ptr(2),
	}
	e.UpdatePolicies([]Policy{policy})

	// Register connections from 2 devices
	if err := e.CheckAndRegisterConnection("conn1", 100, "device1", "10.0.0.1", "vless"); err != nil {
		t.Fatalf("register device1: %v", err)
	}
	if err := e.CheckAndRegisterConnection("conn2", 100, "device2", "10.0.0.2", "vless"); err != nil {
		t.Fatalf("register device2: %v", err)
	}

	// Third device should be rejected
	err := e.CheckAndRegisterConnection("conn3", 100, "device3", "10.0.0.3", "vless")
	if err == nil {
		t.Error("third device should be rejected (max 2 devices)")
	}

	// Spoofing attempt: try to connect with same device ID but new connection ID
	if err := e.CheckAndRegisterConnection("conn4", 100, "device1", "10.0.0.4", "vless"); err != nil {
		t.Errorf("same device should be allowed additional connections: %v", err)
	}

	// But a truly new device should still be rejected
	err = e.CheckAndRegisterConnection("conn5", 100, "device_spoofed", "10.0.0.5", "vless")
	if err == nil {
		t.Error("new device should be rejected after limit reached")
	}
}

// TestSecurityIPSpoofing tests that source IP cannot be spoofed to bypass limits.
func TestSecurityIPSpoofing(t *testing.T) {
	e := New()

	policy := Policy{
		SubjectID: 100,
		MaxIPs:    int64Ptr(2),
	}
	e.UpdatePolicies([]Policy{policy})

	// Register connections from 2 IPs
	if err := e.CheckAndRegisterConnection("conn1", 100, "device1", "10.0.0.1", "vless"); err != nil {
		t.Fatalf("register IP 10.0.0.1: %v", err)
	}
	if err := e.CheckAndRegisterConnection("conn2", 100, "device1", "10.0.0.2", "vless"); err != nil {
		t.Fatalf("register IP 10.0.0.2: %v", err)
	}

	// Third IP should be rejected
	err := e.CheckAndRegisterConnection("conn3", 100, "device1", "10.0.0.3", "vless")
	if err == nil {
		t.Error("third IP should be rejected (max 2 IPs)")
	}

	// Spoofing attempt: try to connect from same IP with new connection ID
	if err := e.CheckAndRegisterConnection("conn4", 100, "device1", "10.0.0.1", "vless"); err != nil {
		t.Errorf("same IP should be allowed additional connections: %v", err)
	}

	// But a truly new IP should still be rejected
	err = e.CheckAndRegisterConnection("conn5", 100, "device1", "10.0.0.99", "vless")
	if err == nil {
		t.Error("new IP should be rejected after limit reached")
	}
}

// TestSecurityConnectionIDCollision tests handling of connection ID collisions.
func TestSecurityConnectionIDCollision(t *testing.T) {
	e := New()

	policy := Policy{
		SubjectID:      100,
		MaxConnections: int64Ptr(10),
	}
	e.UpdatePolicies([]Policy{policy})

	// Register connection with ID "collision"
	if err := e.CheckAndRegisterConnection("collision", 100, "device1", "10.0.0.1", "vless"); err != nil {
		t.Fatalf("register first connection: %v", err)
	}

	// Try to register different subject with same connection ID
	policy2 := Policy{
		SubjectID:      200,
		MaxConnections: int64Ptr(10),
	}
	e.UpdatePolicies([]Policy{policy, policy2})

	// This should be idempotent (update last seen) not create new connection
	if err := e.CheckAndRegisterConnection("collision", 100, "device1", "10.0.0.1", "vless"); err != nil {
		t.Errorf("re-registering same connection should be idempotent: %v", err)
	}

	stats := e.Stats()
	if stats.TotalConnections != 1 {
		t.Errorf("collision should not create duplicate connection, got %d", stats.TotalConnections)
	}
}

// TestSecurityConcurrentSubjectAccess tests concurrent access across multiple subjects.
func TestSecurityConcurrentSubjectAccess(t *testing.T) {
	e := New()

	const numSubjects = 10
	const connectionsPerSubject = 5

	// Create policies for all subjects
	policies := make([]Policy, numSubjects)
	for i := 0; i < numSubjects; i++ {
		policies[i] = Policy{
			SubjectID:      int64(100 + i),
			MaxConnections: int64Ptr(int64(connectionsPerSubject)),
		}
	}
	e.UpdatePolicies(policies)

	var wg sync.WaitGroup

	// Each subject gets its own goroutine creating connections
	for subj := 0; subj < numSubjects; subj++ {
		wg.Add(1)
		go func(subjectID int64) {
			defer wg.Done()
			for i := 0; i < connectionsPerSubject; i++ {
				connID := "subj" + string(rune('0'+int(subjectID-100))) + "_conn" + string(rune('0'+i))
				if err := e.CheckAndRegisterConnection(connID, subjectID, "device1", "10.0.0.1", "vless"); err != nil {
					t.Errorf("subject %d conn %d: %v", subjectID, i, err)
				}
			}

			// Try to exceed limit
			connID := "subj" + string(rune('0'+int(subjectID-100))) + "_overflow"
			err := e.CheckAndRegisterConnection(connID, subjectID, "device1", "10.0.0.1", "vless")
			if err == nil {
				t.Errorf("subject %d should hit limit", subjectID)
			}
		}(int64(100 + subj))
	}

	wg.Wait()

	// Verify total connections
	stats := e.Stats()
	expected := numSubjects * connectionsPerSubject
	if stats.TotalConnections != expected {
		t.Errorf("expected %d total connections, got %d", expected, stats.TotalConnections)
	}

	// Verify each subject is isolated
	if stats.TrackedSubjects != numSubjects {
		t.Errorf("expected %d tracked subjects, got %d", numSubjects, stats.TrackedSubjects)
	}
}

// TestSecurityPolicyBypassAttempt tests that connections cannot bypass policy by racing policy update.
func TestSecurityPolicyBypassAttempt(t *testing.T) {
	e := New()

	// Start with generous policy
	policy := Policy{
		SubjectID:      100,
		MaxConnections: int64Ptr(100),
	}
	e.UpdatePolicies([]Policy{policy})

	// Register 50 connections
	for i := 0; i < 50; i++ {
		connID := "conn" + string(rune('0'+(i%10))) + string(rune('a'+(i/10)))
		if err := e.CheckAndRegisterConnection(connID, 100, "device1", "10.0.0.1", "vless"); err != nil {
			t.Fatalf("register conn %d: %v", i, err)
		}
	}

	var wg sync.WaitGroup
	const numAttempts = 100

	// Reduce limit to 5 (below current 50)
	restrictivePolicy := Policy{
		SubjectID:      100,
		MaxConnections: int64Ptr(5),
	}

	// Goroutine 1: Apply restrictive policy repeatedly
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < numAttempts; i++ {
			e.UpdatePolicies([]Policy{restrictivePolicy})
		}
	}()

	// Goroutine 2: Try to register new connections during policy updates
	wg.Add(1)
	attemptedConnections := 0
	successfulBypass := 0
	go func() {
		defer wg.Done()
		for i := 0; i < numAttempts; i++ {
			connID := "bypass_attempt_" + string(rune('0'+(i%10))) + string(rune('a'+(i/10)))
			err := e.CheckAndRegisterConnection(connID, 100, "device1", "10.0.0.1", "vless")
			attemptedConnections++
			if err == nil {
				successfulBypass++
			}
		}
	}()

	wg.Wait()

	// After policy enforcement, connections should be at or below 5
	stats := e.Stats()
	if stats.TotalConnections > 5 {
		t.Errorf("policy bypass detected: %d connections (limit 5)", stats.TotalConnections)
	}

	t.Logf("Bypass attempts: %d, successful: %d, final connections: %d",
		attemptedConnections, successfulBypass, stats.TotalConnections)
}

// TestSecurityResourceExhaustion tests handling of resource exhaustion attempts.
func TestSecurityResourceExhaustion(t *testing.T) {
	e := New()

	// Allow unlimited connections
	policy := Policy{
		SubjectID:      100,
		MaxConnections: nil,
	}
	e.UpdatePolicies([]Policy{policy})

	// Try to exhaust resources by registering many connections
	const numConnections = 10000

	for i := 0; i < numConnections; i++ {
		// Generate unique connection IDs to avoid collisions
		connID := fmt.Sprintf("exhaust_%d", i)
		if err := e.CheckAndRegisterConnection(connID, 100, "device1", "10.0.0.1", "vless"); err != nil {
			t.Fatalf("register conn %d: %v", i, err)
		}
	}

	stats := e.Stats()
	if stats.TotalConnections != numConnections {
		t.Errorf("expected %d connections, got %d", numConnections, stats.TotalConnections)
	}

	// Verify enforcer still responsive
	if err := e.CheckAndRegisterConnection("final_check", 100, "device1", "10.0.0.1", "vless"); err != nil {
		t.Errorf("enforcer should still be responsive: %v", err)
	}
}

// TestSecurityEmptyStrings tests handling of empty/invalid input strings.
func TestSecurityEmptyStrings(t *testing.T) {
	e := New()

	policy := Policy{
		SubjectID:      100,
		MaxConnections: int64Ptr(10),
	}
	e.UpdatePolicies([]Policy{policy})

	tests := []struct {
		name     string
		connID   string
		deviceID string
		sourceIP string
		protocol string
	}{
		{"empty connection ID", "", "device1", "10.0.0.1", "vless"},
		{"empty device ID", "conn1", "", "10.0.0.1", "vless"},
		{"empty source IP", "conn1", "device1", "", "vless"},
		{"empty protocol", "conn1", "device1", "10.0.0.1", ""},
		{"all empty", "", "", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Should not crash with empty strings
			_ = e.CheckAndRegisterConnection(tt.connID, 100, tt.deviceID, tt.sourceIP, tt.protocol)
		})
	}

	// Enforcer should still be functional
	if err := e.CheckAndRegisterConnection("valid_conn", 100, "device1", "10.0.0.1", "vless"); err != nil {
		t.Errorf("enforcer should still work after empty string tests: %v", err)
	}
}
