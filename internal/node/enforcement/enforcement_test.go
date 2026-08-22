package enforcement

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestEnforcerBasics(t *testing.T) {
	e := New()

	t.Run("no policy allows everything", func(t *testing.T) {
		err := e.CheckConnection(1, "device-1", "192.168.1.1")
		if err != nil {
			t.Errorf("expected no error without policy, got %v", err)
		}

		e.RegisterConnection("conn-1", 1, "device-1", "192.168.1.1", "vless")
		if len(e.GetActiveConnections(1)) != 1 {
			t.Error("expected connection to be registered")
		}
	})

	t.Run("register and unregister", func(t *testing.T) {
		e2 := New()
		e2.RegisterConnection("conn-1", 1, "dev-1", "192.168.1.1", "vless")
		e2.UnregisterConnection("conn-1")

		if len(e2.GetActiveConnections(1)) != 0 {
			t.Error("expected connection to be unregistered")
		}
	})
}

func TestDeviceLimit(t *testing.T) {
	e := New()

	maxDevices := int64(2)
	e.UpdatePolicies([]Policy{{
		SubjectID:  1,
		MaxDevices: &maxDevices,
	}})

	// Register 2 devices (at limit)
	e.RegisterConnection("conn-1", 1, "device-1", "192.168.1.1", "vless")
	e.RegisterConnection("conn-2", 1, "device-2", "192.168.1.2", "vless")

	// 3rd device should be rejected
	err := e.CheckConnection(1, "device-3", "192.168.1.3")
	if err == nil {
		t.Error("expected device limit violation")
	}

	// Same device can reconnect
	err = e.CheckConnection(1, "device-1", "192.168.1.4")
	if err != nil {
		t.Errorf("expected existing device to be allowed, got %v", err)
	}
}

func TestIPLimit(t *testing.T) {
	e := New()

	maxIPs := int64(2)
	e.UpdatePolicies([]Policy{{
		SubjectID: 1,
		MaxIPs:    &maxIPs,
	}})

	// Register 2 IPs (at limit)
	e.RegisterConnection("conn-1", 1, "device-1", "192.168.1.1", "vless")
	e.RegisterConnection("conn-2", 1, "device-2", "192.168.1.2", "vless")

	// 3rd IP should be rejected
	err := e.CheckConnection(1, "device-3", "192.168.1.3")
	if err == nil {
		t.Error("expected IP limit violation")
	}

	// Same IP can reconnect
	err = e.CheckConnection(1, "device-3", "192.168.1.1")
	if err != nil {
		t.Errorf("expected existing IP to be allowed, got %v", err)
	}
}

func TestConnectionLimit(t *testing.T) {
	e := New()

	maxConns := int64(3)
	e.UpdatePolicies([]Policy{{
		SubjectID:      1,
		MaxConnections: &maxConns,
	}})

	// Register 3 connections (at limit)
	e.RegisterConnection("conn-1", 1, "device-1", "192.168.1.1", "vless")
	e.RegisterConnection("conn-2", 1, "device-1", "192.168.1.1", "vless")
	e.RegisterConnection("conn-3", 1, "device-1", "192.168.1.1", "vless")

	// 4th connection should be rejected
	err := e.CheckConnection(1, "device-1", "192.168.1.1")
	if err == nil {
		t.Error("expected connection limit violation")
	}
}

func TestSpeedLimits(t *testing.T) {
	e := New()

	upLimit := int64(1000)
	downLimit := int64(5000)
	e.UpdatePolicies([]Policy{{
		SubjectID:          1,
		SpeedLimitUpKbps:   &upLimit,
		SpeedLimitDownKbps: &downLimit,
	}})

	// Speed limits don't block connections
	err := e.CheckConnection(1, "device-1", "192.168.1.1")
	if err != nil {
		t.Errorf("speed limits should not block connections, got %v", err)
	}
}

func TestPolicyUpdate(t *testing.T) {
	e := New()

	// Initial policy
	maxConns := int64(5)
	e.UpdatePolicies([]Policy{{
		SubjectID:      1,
		MaxConnections: &maxConns,
	}})

	// Register 3 connections
	for i := 1; i <= 3; i++ {
		e.RegisterConnection(fmt.Sprintf("conn-%d", i), 1, "device-1", "192.168.1.1", "vless")
	}

	// Reduce limit to 2
	newLimit := int64(2)
	e.UpdatePolicies([]Policy{{
		SubjectID:      1,
		MaxConnections: &newLimit,
	}})

	// Existing connections above limit should be terminated
	conns := e.GetActiveConnections(1)
	if len(conns) > 2 {
		t.Errorf("expected connections to be terminated to meet new limit, got %d", len(conns))
	}
}

func TestPolicyRemoval(t *testing.T) {
	e := New()

	maxConns := int64(2)
	e.UpdatePolicies([]Policy{{
		SubjectID:      1,
		MaxConnections: &maxConns,
	}})

	e.RegisterConnection("conn-1", 1, "device-1", "192.168.1.1", "vless")
	e.RegisterConnection("conn-2", 1, "device-1", "192.168.1.1", "vless")

	// Remove policy
	e.UpdatePolicies([]Policy{})

	// All connections should be terminated
	conns := e.GetActiveConnections(1)
	if len(conns) != 0 {
		t.Errorf("expected all connections terminated when policy removed, got %d", len(conns))
	}
}

func TestCleanupStale(t *testing.T) {
	e := New()

	e.RegisterConnection("conn-1", 1, "device-1", "192.168.1.1", "vless")

	// Cleanup connections older than 0 seconds (should remove all)
	time.Sleep(10 * time.Millisecond)
	removed := e.CleanupStale(0)
	if removed != 1 {
		t.Errorf("expected 1 stale connection removed, got %d", removed)
	}

	conns := e.GetActiveConnections(1)
	if len(conns) != 0 {
		t.Errorf("expected no connections after cleanup, got %d", len(conns))
	}
}

func TestStats(t *testing.T) {
	e := New()

	// Add policies first (TrackedSubjects counts policies, not connections)
	e.UpdatePolicies([]Policy{
		{SubjectID: 1},
		{SubjectID: 2},
	})

	e.RegisterConnection("conn-1", 1, "device-1", "192.168.1.1", "vless")
	e.RegisterConnection("conn-2", 1, "device-2", "192.168.1.2", "vless")
	e.RegisterConnection("conn-3", 2, "device-3", "192.168.1.3", "vless")

	globalStats := e.Stats()
	if globalStats.TotalConnections != 3 {
		t.Errorf("expected 3 total connections, got %d", globalStats.TotalConnections)
	}
	if globalStats.TrackedSubjects != 2 {
		t.Errorf("expected 2 tracked subjects, got %d", globalStats.TrackedSubjects)
	}
	if globalStats.UniqueIPs != 3 {
		t.Errorf("expected 3 unique IPs, got %d", globalStats.UniqueIPs)
	}
	if globalStats.UniqueDevices != 3 {
		t.Errorf("expected 3 unique devices, got %d", globalStats.UniqueDevices)
	}
}

func TestConcurrentAccess(t *testing.T) {
	e := New()

	maxConns := int64(100)
	e.UpdatePolicies([]Policy{{
		SubjectID:      1,
		MaxConnections: &maxConns,
	}})

	// Simulate concurrent connection attempts
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 10; j++ {
				connID := fmt.Sprintf("conn-%d-%d", id, j)
				if err := e.CheckConnection(1, "device-1", "192.168.1.1"); err == nil {
					e.RegisterConnection(connID, 1, "device-1", "192.168.1.1", "vless")
					e.UnregisterConnection(connID)
				}
			}
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	// Should have no connections at the end
	conns := e.GetActiveConnections(1)
	if len(conns) != 0 {
		t.Errorf("expected 0 connections, got %d", len(conns))
	}
}

func TestCheckAndRegisterAtomicity(t *testing.T) {
	e := New()

	limit := int64(2)
	e.UpdatePolicies([]Policy{{SubjectID: 1, MaxConnections: &limit}})

	// Register first connection
	err := e.CheckAndRegisterConnection("conn-1", 1, "dev-1", "192.168.1.1", "test")
	if err != nil {
		t.Fatalf("first connection failed: %v", err)
	}

	// Concurrent attempts to register 2nd and 3rd connections
	var wg sync.WaitGroup
	errors := make([]error, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			connID := fmt.Sprintf("conn-%d", idx+2)
			deviceID := fmt.Sprintf("dev-%d", idx+2)
			errors[idx] = e.CheckAndRegisterConnection(connID, 1, deviceID, "192.168.1.1", "test")
		}(i)
	}

	wg.Wait()

	// Exactly one should succeed, one should fail
	var successes, failures int
	for _, err := range errors {
		if err == nil {
			successes++
		} else {
			failures++
		}
	}

	if successes != 1 {
		t.Errorf("expected exactly 1 success with concurrent registration at limit, got %d", successes)
	}

	if failures != 1 {
		t.Errorf("expected exactly 1 failure with concurrent registration at limit, got %d", failures)
	}

	// Verify actual connection count
	conns := e.GetActiveConnections(1)
	if len(conns) != 2 {
		t.Errorf("expected exactly 2 active connections, got %d", len(conns))
	}
}

func TestCheckAndRegisterIdempotent(t *testing.T) {
	e := New()

	limit := int64(1)
	e.UpdatePolicies([]Policy{{SubjectID: 1, MaxConnections: &limit}})

	// Register same connection ID twice
	err1 := e.CheckAndRegisterConnection("conn-1", 1, "dev-1", "192.168.1.1", "test")
	if err1 != nil {
		t.Fatalf("first registration failed: %v", err1)
	}

	// Second registration with same ID should succeed (idempotent)
	err2 := e.CheckAndRegisterConnection("conn-1", 1, "dev-1", "192.168.1.1", "test")
	if err2 != nil {
		t.Errorf("idempotent registration failed: %v", err2)
	}

	// Should still have only 1 connection
	conns := e.GetActiveConnections(1)
	if len(conns) != 1 {
		t.Errorf("expected 1 connection after idempotent register, got %d", len(conns))
	}

	// Different connection should fail (at limit)
	err3 := e.CheckAndRegisterConnection("conn-2", 1, "dev-2", "192.168.1.2", "test")
	if err3 == nil {
		t.Error("expected failure when registering different connection at limit")
	}
}

func TestNegativeLimitValidation(t *testing.T) {
	e := New()

	negativeDev := int64(-1)
	e.UpdatePolicies([]Policy{{SubjectID: 1, MaxDevices: &negativeDev}})

	err := e.CheckAndRegisterConnection("conn-1", 1, "dev-1", "192.168.1.1", "test")
	if err == nil {
		t.Error("expected error with negative device limit")
	}

	negativeIP := int64(-1)
	e.UpdatePolicies([]Policy{{SubjectID: 2, MaxIPs: &negativeIP}})

	err = e.CheckAndRegisterConnection("conn-2", 2, "dev-2", "192.168.1.1", "test")
	if err == nil {
		t.Error("expected error with negative IP limit")
	}

	negativeConn := int64(-1)
	e.UpdatePolicies([]Policy{{SubjectID: 3, MaxConnections: &negativeConn}})

	err = e.CheckAndRegisterConnection("conn-3", 3, "dev-3", "192.168.1.1", "test")
	if err == nil {
		t.Error("expected error with negative connection limit")
	}
}

func TestUpdateLastSeen(t *testing.T) {
	e := New()

	err := e.CheckAndRegisterConnection("conn-1", 1, "dev-1", "192.168.1.1", "test")
	if err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	// Get initial count
	conns1 := e.GetActiveConnections(1)
	initial := len(conns1)

	time.Sleep(10 * time.Millisecond)

	// Update last seen
	e.UpdateLastSeen("conn-1")

	// Should still have 1 connection
	conns2 := e.GetActiveConnections(1)
	if len(conns2) != initial {
		t.Error("connection count changed after UpdateLastSeen")
	}
}

func TestConcurrentLimitBypass(t *testing.T) {
	// This test specifically targets the TOCTOU race that CheckAndRegisterConnection should prevent
	e := New()

	limit := int64(10)
	e.UpdatePolicies([]Policy{{SubjectID: 1, MaxConnections: &limit}})

	// Launch many concurrent attempts to bypass the limit
	const goroutines = 20
	const iterations = 10

	var wg sync.WaitGroup
	errors := make([]error, goroutines*iterations)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				idx := gid*iterations + j
				connID := fmt.Sprintf("conn-%d-%d", gid, j)
				err := e.CheckAndRegisterConnection(connID, 1, fmt.Sprintf("dev-%d", gid), "192.168.1.1", "test")
				errors[idx] = err
			}
		}(i)
	}

	wg.Wait()

	// Count successful registrations
	var success, rejected int
	for _, err := range errors {
		if err == nil {
			success++
		} else {
			rejected++
		}
	}

	// Should have exactly 10 successful (the limit), rest rejected
	conns := e.GetActiveConnections(1)
	actualConns := len(conns)
	if actualConns != int(limit) {
		t.Errorf("TOCTOU race detected: expected exactly %d connections, got %d (success=%d, rejected=%d)",
			limit, actualConns, success, rejected)
	}

	if success != int(limit) {
		t.Errorf("expected %d successful registrations, got %d (rejected: %d)", limit, success, rejected)
	}
}
