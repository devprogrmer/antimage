package enforcement

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestAtomicAdmission_ConcurrentConnections verifies that CheckAndRegisterConnection
// prevents TOCTOU races when multiple goroutines try to connect simultaneously.
func TestAtomicAdmission_ConcurrentConnections(t *testing.T) {
	e := New()

	maxConns := int64(5)
	e.UpdatePolicies([]Policy{{
		SubjectID:      1,
		MaxConnections: &maxConns,
	}})

	// Try to register 100 concurrent connections, only 5 should succeed
	const numGoroutines = 100
	var accepted atomic.Int32
	var rejected atomic.Int32

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()

			connID := fmt.Sprintf("conn-%d", id)
			err := e.CheckAndRegisterConnection(connID, 1, "device-1", "192.168.1.1", "xray")

			if err == nil {
				accepted.Add(1)
			} else {
				rejected.Add(1)
			}
		}(i)
	}

	wg.Wait()

	if accepted.Load() != 5 {
		t.Errorf("expected exactly 5 accepted, got %d", accepted.Load())
	}

	if rejected.Load() != 95 {
		t.Errorf("expected exactly 95 rejected, got %d", rejected.Load())
	}

	// Verify internal state matches
	conns := e.GetActiveConnections(1)
	if len(conns) != 5 {
		t.Errorf("expected 5 active connections, got %d", len(conns))
	}
}

// TestAtomicAdmission_DeviceLimit verifies device limit enforcement under concurrent load.
func TestAtomicAdmission_DeviceLimit(t *testing.T) {
	e := New()

	maxDevices := int64(3)
	e.UpdatePolicies([]Policy{{
		SubjectID:  1,
		MaxDevices: &maxDevices,
	}})

	// Try to register 10 different devices concurrently
	const numDevices = 10
	var accepted atomic.Int32
	var rejected atomic.Int32

	var wg sync.WaitGroup
	wg.Add(numDevices)

	for i := 0; i < numDevices; i++ {
		go func(id int) {
			defer wg.Done()

			connID := fmt.Sprintf("conn-%d", id)
			deviceID := fmt.Sprintf("device-%d", id)
			err := e.CheckAndRegisterConnection(connID, 1, deviceID, "192.168.1.1", "xray")

			if err == nil {
				accepted.Add(1)
			} else {
				rejected.Add(1)
			}
		}(i)
	}

	wg.Wait()

	if accepted.Load() != 3 {
		t.Errorf("expected exactly 3 devices accepted, got %d", accepted.Load())
	}

	if rejected.Load() != 7 {
		t.Errorf("expected exactly 7 devices rejected, got %d", rejected.Load())
	}
}

// TestAtomicAdmission_IPLimit verifies IP limit enforcement under concurrent load.
func TestAtomicAdmission_IPLimit(t *testing.T) {
	e := New()

	maxIPs := int64(3)
	e.UpdatePolicies([]Policy{{
		SubjectID: 1,
		MaxIPs:    &maxIPs,
	}})

	// Try to register 10 different IPs concurrently
	const numIPs = 10
	var accepted atomic.Int32
	var rejected atomic.Int32

	var wg sync.WaitGroup
	wg.Add(numIPs)

	for i := 0; i < numIPs; i++ {
		go func(id int) {
			defer wg.Done()

			connID := fmt.Sprintf("conn-%d", id)
			sourceIP := fmt.Sprintf("192.168.1.%d", id)
			err := e.CheckAndRegisterConnection(connID, 1, "device-1", sourceIP, "xray")

			if err == nil {
				accepted.Add(1)
			} else {
				rejected.Add(1)
			}
		}(i)
	}

	wg.Wait()

	if accepted.Load() != 3 {
		t.Errorf("expected exactly 3 IPs accepted, got %d", accepted.Load())
	}

	if rejected.Load() != 7 {
		t.Errorf("expected exactly 7 IPs rejected, got %d", rejected.Load())
	}
}

// TestAtomicAdmission_PolicyUpdateDuringConnections verifies that policy updates
// are safe while connections are being registered concurrently.
func TestAtomicAdmission_PolicyUpdateDuringConnections(t *testing.T) {
	e := New()

	maxConns := int64(10)
	e.UpdatePolicies([]Policy{{
		SubjectID:      1,
		MaxConnections: &maxConns,
	}})

	var wg sync.WaitGroup
	stopChan := make(chan struct{})

	// Goroutine 1: Continuously register connections
	wg.Add(1)
	go func() {
		defer wg.Done()
		connID := 0
		for {
			select {
			case <-stopChan:
				return
			default:
				connID++
				_ = e.CheckAndRegisterConnection(fmt.Sprintf("conn-%d", connID), 1, "device-1", "192.168.1.1", "xray")
				time.Sleep(1 * time.Millisecond)
			}
		}
	}()

	// Goroutine 2: Continuously update policies
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			select {
			case <-stopChan:
				return
			default:
				limit := int64(5 + (i % 5)) // Vary limit between 5-9
				e.UpdatePolicies([]Policy{{
					SubjectID:      1,
					MaxConnections: &limit,
				}})
				time.Sleep(2 * time.Millisecond)
			}
		}
	}()

	// Let them run for a bit
	time.Sleep(200 * time.Millisecond)
	close(stopChan)
	wg.Wait()

	// Verify no crashes and state is consistent
	conns := e.GetActiveConnections(1)
	stats := e.Stats()

	if len(conns) != stats.TotalConnections {
		t.Errorf("inconsistent state: GetActiveConnections=%d, Stats.TotalConnections=%d",
			len(conns), stats.TotalConnections)
	}
}

// TestAtomicAdmission_DuplicateRegistration verifies that registering the same
// connection ID twice is idempotent.
func TestAtomicAdmission_DuplicateRegistration(t *testing.T) {
	e := New()

	maxConns := int64(5)
	e.UpdatePolicies([]Policy{{
		SubjectID:      1,
		MaxConnections: &maxConns,
	}})

	// First registration
	err := e.CheckAndRegisterConnection("conn-1", 1, "device-1", "192.168.1.1", "xray")
	if err != nil {
		t.Fatalf("first registration failed: %v", err)
	}

	// Duplicate registration should succeed (update last seen)
	err = e.CheckAndRegisterConnection("conn-1", 1, "device-1", "192.168.1.1", "xray")
	if err != nil {
		t.Errorf("duplicate registration should succeed, got error: %v", err)
	}

	// Verify only one connection exists
	conns := e.GetActiveConnections(1)
	if len(conns) != 1 {
		t.Errorf("expected 1 connection, got %d", len(conns))
	}
}

// TestAtomicAdmission_ZeroLimits verifies behavior with zero limits.
func TestAtomicAdmission_ZeroLimits(t *testing.T) {
	e := New()

	zeroConns := int64(0)
	e.UpdatePolicies([]Policy{{
		SubjectID:      1,
		MaxConnections: &zeroConns,
	}})

	// With zero limit, no connections should be allowed
	err := e.CheckAndRegisterConnection("conn-1", 1, "device-1", "192.168.1.1", "xray")
	if err == nil {
		t.Error("expected connection to be rejected with zero limit")
	}
}

// TestAtomicAdmission_NegativeLimits verifies behavior with invalid negative limits.
func TestAtomicAdmission_NegativeLimits(t *testing.T) {
	e := New()

	negativeConns := int64(-1)
	e.UpdatePolicies([]Policy{{
		SubjectID:      1,
		MaxConnections: &negativeConns,
	}})

	// Negative limits should be treated as invalid and reject connections
	err := e.CheckAndRegisterConnection("conn-1", 1, "device-1", "192.168.1.1", "xray")
	if err == nil {
		t.Error("expected connection to be rejected with negative limit")
	}

	if _, ok := err.(*ErrPolicyViolation); !ok {
		t.Errorf("expected ErrPolicyViolation, got %T", err)
	}
}

// TestAtomicAdmission_NilPolicy verifies behavior when no policy exists.
func TestAtomicAdmission_NilPolicy(t *testing.T) {
	e := New()

	// No policy set - should allow everything
	err := e.CheckAndRegisterConnection("conn-1", 1, "device-1", "192.168.1.1", "xray")
	if err != nil {
		t.Errorf("expected connection to be allowed without policy, got %v", err)
	}

	// Verify connection was registered
	conns := e.GetActiveConnections(1)
	if len(conns) != 1 {
		t.Errorf("expected 1 connection, got %d", len(conns))
	}
}

// TestAtomicAdmission_ConcurrentUnregister verifies that concurrent unregistration
// is safe and doesn't corrupt internal state.
func TestAtomicAdmission_ConcurrentUnregister(t *testing.T) {
	e := New()

	// Register 100 connections
	for i := 0; i < 100; i++ {
		connID := fmt.Sprintf("conn-%d", i)
		err := e.CheckAndRegisterConnection(connID, 1, "device-1", "192.168.1.1", "xray")
		if err != nil {
			t.Fatalf("failed to register connection %s: %v", connID, err)
		}
	}

	// Unregister all concurrently
	var wg sync.WaitGroup
	wg.Add(100)

	for i := 0; i < 100; i++ {
		go func(id int) {
			defer wg.Done()
			connID := fmt.Sprintf("conn-%d", id)
			e.UnregisterConnection(connID)
		}(i)
	}

	wg.Wait()

	// Verify all connections removed
	conns := e.GetActiveConnections(1)
	if len(conns) != 0 {
		t.Errorf("expected 0 connections, got %d", len(conns))
	}

	stats := e.Stats()
	if stats.TotalConnections != 0 {
		t.Errorf("expected 0 total connections, got %d", stats.TotalConnections)
	}
}

// TestAtomicAdmission_StaleConnectionCleanup verifies concurrent cleanup of stale connections.
func TestAtomicAdmission_StaleConnectionCleanup(t *testing.T) {
	e := New()

	// Override time function to control time
	baseTime := time.Now()
	e.now = func() time.Time { return baseTime }

	// Register 10 connections
	for i := 0; i < 10; i++ {
		connID := fmt.Sprintf("conn-%d", i)
		_ = e.CheckAndRegisterConnection(connID, 1, "device-1", "192.168.1.1", "xray")
	}

	// Advance time by 1 hour
	e.now = func() time.Time { return baseTime.Add(1 * time.Hour) }

	// Cleanup connections older than 30 minutes
	removed := e.CleanupStale(30 * time.Minute)

	if removed != 10 {
		t.Errorf("expected 10 removed, got %d", removed)
	}

	conns := e.GetActiveConnections(1)
	if len(conns) != 0 {
		t.Errorf("expected 0 connections after cleanup, got %d", len(conns))
	}
}

// TestAtomicAdmission_MultipleSubjects verifies isolation between subjects.
func TestAtomicAdmission_MultipleSubjects(t *testing.T) {
	e := New()

	maxConns := int64(2)
	e.UpdatePolicies([]Policy{
		{SubjectID: 1, MaxConnections: &maxConns},
		{SubjectID: 2, MaxConnections: &maxConns},
	})

	// Register 2 connections for subject 1
	_ = e.CheckAndRegisterConnection("conn-1-1", 1, "device-1", "192.168.1.1", "xray")
	_ = e.CheckAndRegisterConnection("conn-1-2", 1, "device-2", "192.168.1.2", "xray")

	// Third connection for subject 1 should fail
	err := e.CheckAndRegisterConnection("conn-1-3", 1, "device-3", "192.168.1.3", "xray")
	if err == nil {
		t.Error("expected subject 1 third connection to be rejected")
	}

	// Subject 2 should still have quota
	err = e.CheckAndRegisterConnection("conn-2-1", 2, "device-1", "192.168.1.1", "xray")
	if err != nil {
		t.Errorf("expected subject 2 first connection to succeed, got %v", err)
	}

	// Verify isolation
	conns1 := e.GetActiveConnections(1)
	conns2 := e.GetActiveConnections(2)

	if len(conns1) != 2 {
		t.Errorf("expected 2 connections for subject 1, got %d", len(conns1))
	}

	if len(conns2) != 1 {
		t.Errorf("expected 1 connection for subject 2, got %d", len(conns2))
	}
}

// TestAtomicAdmission_PolicyRemoval verifies that removing a policy terminates all connections.
func TestAtomicAdmission_PolicyRemoval(t *testing.T) {
	e := New()

	maxConns := int64(10)
	e.UpdatePolicies([]Policy{{
		SubjectID:      1,
		MaxConnections: &maxConns,
	}})

	// Register 5 connections
	for i := 0; i < 5; i++ {
		connID := fmt.Sprintf("conn-%d", i)
		_ = e.CheckAndRegisterConnection(connID, 1, "device-1", "192.168.1.1", "xray")
	}

	// Verify connections exist
	conns := e.GetActiveConnections(1)
	if len(conns) != 5 {
		t.Fatalf("expected 5 connections, got %d", len(conns))
	}

	// Remove policy (pass empty list)
	e.UpdatePolicies([]Policy{})

	// All connections should be terminated
	conns = e.GetActiveConnections(1)
	if len(conns) != 0 {
		t.Errorf("expected 0 connections after policy removal, got %d", len(conns))
	}
}

// TestAtomicAdmission_LimitReduction verifies that reducing limits terminates excess connections.
func TestAtomicAdmission_LimitReduction(t *testing.T) {
	e := New()

	maxConns := int64(10)
	e.UpdatePolicies([]Policy{{
		SubjectID:      1,
		MaxConnections: &maxConns,
	}})

	// Register 10 connections
	for i := 0; i < 10; i++ {
		connID := fmt.Sprintf("conn-%d", i)
		_ = e.CheckAndRegisterConnection(connID, 1, "device-1", "192.168.1.1", "xray")
	}

	// Reduce limit to 5
	reducedLimit := int64(5)
	e.UpdatePolicies([]Policy{{
		SubjectID:      1,
		MaxConnections: &reducedLimit,
	}})

	// Should have exactly 5 connections (oldest terminated)
	conns := e.GetActiveConnections(1)
	if len(conns) != 5 {
		t.Errorf("expected 5 connections after limit reduction, got %d", len(conns))
	}
}
