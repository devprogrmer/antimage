package enforcement

import (
	"sync"
	"testing"
	"time"
)

// TestEnforcerStateRecovery tests that enforcer rebuilds state after restart.
func TestEnforcerStateRecovery(t *testing.T) {
	e := New()

	// Apply initial policy
	policy1 := Policy{
		SubjectID:      100,
		MaxConnections: int64Ptr(5),
		MaxDevices:     int64Ptr(3),
		MaxIPs:         int64Ptr(2),
	}
	e.UpdatePolicies([]Policy{policy1})

	// Register connections
	conn1 := "conn1"
	conn2 := "conn2"
	if err := e.CheckAndRegisterConnection(conn1, 100, "device1", "10.0.0.1", "vless"); err != nil {
		t.Fatalf("register conn1: %v", err)
	}
	if err := e.CheckAndRegisterConnection(conn2, 100, "device2", "10.0.0.2", "vless"); err != nil {
		t.Fatalf("register conn2: %v", err)
	}

	// Verify state before "restart"
	stats := e.Stats()
	if stats.TotalConnections != 2 {
		t.Errorf("before restart: expected 2 connections, got %d", stats.TotalConnections)
	}

	// Simulate restart: create new enforcer, apply same policies
	e2 := New()
	e2.UpdatePolicies([]Policy{policy1})

	// State should be empty after restart (connections not persisted)
	stats2 := e2.Stats()
	if stats2.TotalConnections != 0 {
		t.Errorf("after restart: expected 0 connections, got %d", stats2.TotalConnections)
	}

	// Re-registering connections should work
	if err := e2.CheckAndRegisterConnection(conn1, 100, "device1", "10.0.0.1", "vless"); err != nil {
		t.Errorf("re-register conn1 after restart: %v", err)
	}
	if err := e2.CheckAndRegisterConnection(conn2, 100, "device2", "10.0.0.2", "vless"); err != nil {
		t.Errorf("re-register conn2 after restart: %v", err)
	}

	stats3 := e2.Stats()
	if stats3.TotalConnections != 2 {
		t.Errorf("after re-registration: expected 2 connections, got %d", stats3.TotalConnections)
	}
}

// TestPolicyUpdateDuringTotalConnections tests policy changes while connections active.
func TestPolicyUpdateDuringTotalConnections(t *testing.T) {
	e := New()

	// Initial policy: allow 10 connections
	policy := Policy{
		SubjectID:      100,
		MaxConnections: int64Ptr(10),
	}
	e.UpdatePolicies([]Policy{policy})

	// Register 5 connections
	for i := 0; i < 5; i++ {
		connID := "conn" + string(rune('0'+i))
		if err := e.CheckAndRegisterConnection(connID, 100, "device1", "10.0.0.1", "vless"); err != nil {
			t.Fatalf("register %s: %v", connID, err)
		}
	}

	stats := e.Stats()
	if stats.TotalConnections != 5 {
		t.Fatalf("expected 5 connections, got %d", stats.TotalConnections)
	}

	// Update policy: reduce limit to 3 (below current 5)
	policyReduced := Policy{
		SubjectID:      100,
		MaxConnections: int64Ptr(3),
	}
	e.UpdatePolicies([]Policy{policyReduced})

	// Policy reduction terminates oldest connections to enforce new limit
	stats2 := e.Stats()
	if stats2.TotalConnections != 3 {
		t.Errorf("policy reduction should terminate oldest connections, got %d", stats2.TotalConnections)
	}

	// But new connections should be rejected until count drops below 3
	err := e.CheckAndRegisterConnection("conn_new", 100, "device1", "10.0.0.1", "vless")
	if err == nil {
		t.Error("expected new connection to be rejected (over limit)")
	}

	// Unregister 3 connections (bringing total to 2, below new limit of 3)
	e.UnregisterConnection("conn0")
	e.UnregisterConnection("conn1")
	e.UnregisterConnection("conn2")

	stats3 := e.Stats()
	if stats3.TotalConnections != 2 {
		t.Errorf("after unregister: expected 2 connections, got %d", stats3.TotalConnections)
	}

	// Now new connection should succeed
	if err := e.CheckAndRegisterConnection("conn_new", 100, "device1", "10.0.0.1", "vless"); err != nil {
		t.Errorf("new connection should succeed after dropping below limit: %v", err)
	}
}

// TestPolicyRemovalTerminatesConnections tests removing policy disconnects all users.
func TestPolicyRemovalTerminatesConnections(t *testing.T) {
	e := New()

	// Apply policy
	policy := Policy{
		SubjectID:      100,
		MaxConnections: int64Ptr(5),
	}
	e.UpdatePolicies([]Policy{policy})

	// Register connections
	if err := e.CheckAndRegisterConnection("conn1", 100, "device1", "10.0.0.1", "vless"); err != nil {
		t.Fatalf("register conn1: %v", err)
	}
	if err := e.CheckAndRegisterConnection("conn2", 100, "device2", "10.0.0.2", "vless"); err != nil {
		t.Fatalf("register conn2: %v", err)
	}

	stats := e.Stats()
	if stats.TotalConnections != 2 {
		t.Fatalf("expected 2 connections, got %d", stats.TotalConnections)
	}

	// Remove policy (apply empty policy list)
	e.UpdatePolicies([]Policy{})

	// Connections should be terminated (policy removed)
	stats2 := e.Stats()
	if stats2.TotalConnections != 0 {
		t.Errorf("policy removal should terminate all connections, got %d", stats2.TotalConnections)
	}

	// New connections should be ALLOWED (no policy = allow all)
	err := e.CheckAndRegisterConnection("conn3", 100, "device1", "10.0.0.1", "vless")
	if err != nil {
		t.Errorf("expected connection to be allowed (no policy = allow all), got: %v", err)
	}
}

// TestStaleConnectionCleanupAfterPolicyUpdate tests cleanup of stale connections.
func TestStaleConnectionCleanupAfterPolicyUpdate(t *testing.T) {
	e := New()

	// Apply policy
	policy := Policy{
		SubjectID:      100,
		MaxConnections: int64Ptr(10),
	}
	e.UpdatePolicies([]Policy{policy})

	// Register connections
	for i := 0; i < 5; i++ {
		connID := "conn" + string(rune('0'+i))
		if err := e.CheckAndRegisterConnection(connID, 100, "device1", "10.0.0.1", "vless"); err != nil {
			t.Fatalf("register %s: %v", connID, err)
		}
	}

	// Manually mark some connections as stale (simulate protocol adapter not reporting them)
	e.mu.Lock()
	// Remove conn2 and conn4 from internal tracking (simulating they disappeared)
	delete(e.connections, "conn2")
	delete(e.connections, "conn4")
	e.mu.Unlock()

	// Apply policy update (triggers reconciliation)
	e.UpdatePolicies([]Policy{policy})

	// Active connections should reflect actual state
	stats := e.Stats()
	if stats.TotalConnections != 3 {
		t.Errorf("after cleanup: expected 3 connections, got %d", stats.TotalConnections)
	}
}

// TestConcurrentPolicyUpdatesAndConnections tests policy updates racing with connection admission.
func TestConcurrentPolicyUpdatesAndConnections(t *testing.T) {
	e := New()

	policy := Policy{
		SubjectID:      100,
		MaxConnections: int64Ptr(10),
	}
	e.UpdatePolicies([]Policy{policy})

	var wg sync.WaitGroup
	const numGoroutines = 50
	const iterations = 10

	// Half goroutines do policy updates
	for i := 0; i < numGoroutines/2; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				// Alternate between different max connection values
				maxConn := int64(5 + (id+j)%5)
				p := Policy{
					SubjectID:      100,
					MaxConnections: &maxConn,
				}
				e.UpdatePolicies([]Policy{p})
				time.Sleep(time.Millisecond)
			}
		}(i)
	}

	// Other half do connection registration
	for i := 0; i < numGoroutines/2; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				connID := "conn-" + string(rune('a'+id)) + string(rune('0'+j))
				// Ignore errors (many will fail due to limit)
				_ = e.CheckAndRegisterConnection(connID, 100, "device1", "10.0.0.1", "vless")
				time.Sleep(time.Millisecond)
			}
		}(i)
	}

	wg.Wait()

	// Should not crash, state should be consistent
	stats := e.Stats()
	t.Logf("After concurrent updates: %d active connections", stats.TotalConnections)

	// Verify we can still operate normally
	e.UpdatePolicies([]Policy{policy})
	if err := e.CheckAndRegisterConnection("final_conn", 100, "device1", "10.0.0.1", "vless"); err != nil {
		// May fail if limit reached, but should not crash
		t.Logf("Final connection: %v", err)
	}
}

// TestEnforcerWithNilPolicy tests behavior when policy is nil (allow all).
func TestEnforcerWithNilPolicy(t *testing.T) {
	e := New()

	// Apply policy with nil limits (allow all)
	policy := Policy{
		SubjectID:      100,
		MaxConnections: nil,
		MaxDevices:     nil,
		MaxIPs:         nil,
	}
	e.UpdatePolicies([]Policy{policy})

	// Should allow unlimited connections
	for i := 0; i < 100; i++ {
		connID := "conn" + string(rune('0'+(i%10)))
		deviceID := "device" + string(rune('0'+(i%10)))
		ip := "10.0.0." + string(rune('1'+(i%10)))
		if err := e.CheckAndRegisterConnection(connID+string(rune('a'+(i/10))), 100, deviceID, ip, "vless"); err != nil {
			t.Errorf("nil policy should allow unlimited connections, got error at %d: %v", i, err)
		}
	}

	stats := e.Stats()
	if stats.TotalConnections != 100 {
		t.Errorf("expected 100 connections, got %d", stats.TotalConnections)
	}
}

// TestZeroLimitPolicy tests policy with zero limits (deny all).
func TestZeroLimitPolicy(t *testing.T) {
	e := New()

	// Policy with zero limits
	policy := Policy{
		SubjectID:      100,
		MaxConnections: int64Ptr(0),
		MaxDevices:     int64Ptr(0),
		MaxIPs:         int64Ptr(0),
	}
	e.UpdatePolicies([]Policy{policy})

	// All connections should be rejected
	err := e.CheckAndRegisterConnection("conn1", 100, "device1", "10.0.0.1", "vless")
	if err == nil {
		t.Error("expected connection to be rejected (zero limit)")
	}

	stats := e.Stats()
	if stats.TotalConnections != 0 {
		t.Errorf("expected 0 connections, got %d", stats.TotalConnections)
	}
}

// TestDuplicateConnectionRegistration tests registering same connection ID twice.
func TestDuplicateConnectionRegistration(t *testing.T) {
	e := New()

	policy := Policy{
		SubjectID:      100,
		MaxConnections: int64Ptr(10),
	}
	e.UpdatePolicies([]Policy{policy})

	// First registration
	connID := "conn1"
	if err := e.CheckAndRegisterConnection(connID, 100, "device1", "10.0.0.1", "vless"); err != nil {
		t.Fatalf("first registration: %v", err)
	}

	// Duplicate registration - should be idempotent (no error)
	if err := e.CheckAndRegisterConnection(connID, 100, "device1", "10.0.0.1", "vless"); err != nil {
		t.Errorf("duplicate registration should be idempotent: %v", err)
	}

	stats := e.Stats()
	if stats.TotalConnections != 1 {
		t.Errorf("duplicate should not increase count, got %d", stats.TotalConnections)
	}
}

// Helper function to create int64 pointer
func int64Ptr(v int64) *int64 {
	return &v
}
