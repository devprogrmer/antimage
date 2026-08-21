package enforcement

import (
	"fmt"
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
	})

	t.Run("register and unregister", func(t *testing.T) {
		e.RegisterConnection("conn-1", 1, "device-1", "192.168.1.1", "vless")

		conns := e.GetActiveConnections(1)
		if len(conns) != 1 {
			t.Fatalf("expected 1 connection, got %d", len(conns))
		}

		if conns[0].ID != "conn-1" {
			t.Errorf("expected conn-1, got %s", conns[0].ID)
		}

		e.UnregisterConnection("conn-1")

		conns = e.GetActiveConnections(1)
		if len(conns) != 0 {
			t.Errorf("expected 0 connections after unregister, got %d", len(conns))
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

	// Register 2 devices
	e.RegisterConnection("conn-1", 1, "device-1", "192.168.1.1", "vless")
	e.RegisterConnection("conn-2", 1, "device-2", "192.168.1.2", "vless")

	// Same device can reconnect
	err := e.CheckConnection(1, "device-1", "192.168.1.1")
	if err != nil {
		t.Errorf("same device should be allowed to reconnect: %v", err)
	}

	// Third device should be rejected
	err = e.CheckConnection(1, "device-3", "192.168.1.3")
	if err == nil {
		t.Error("expected device limit error")
	}
	if _, ok := err.(*ErrPolicyViolation); !ok {
		t.Errorf("expected ErrPolicyViolation, got %T", err)
	}
}

func TestIPLimit(t *testing.T) {
	e := New()

	maxIPs := int64(2)
	e.UpdatePolicies([]Policy{{
		SubjectID: 1,
		MaxIPs:    &maxIPs,
	}})

	// Connect from 2 IPs
	e.RegisterConnection("conn-1", 1, "device-1", "192.168.1.1", "vless")
	e.RegisterConnection("conn-2", 1, "device-1", "192.168.1.2", "vless")

	// Same IP can have multiple connections
	err := e.CheckConnection(1, "device-1", "192.168.1.1")
	if err != nil {
		t.Errorf("same IP should be allowed: %v", err)
	}

	// Third IP should be rejected
	err = e.CheckConnection(1, "device-1", "192.168.1.3")
	if err == nil {
		t.Error("expected IP limit error")
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
	for i := 1; i <= 3; i++ {
		e.RegisterConnection("conn-"+string(rune('0'+i)), 1, "device-1", "192.168.1.1", "vless")
	}

	// Fourth connection should be rejected
	err := e.CheckConnection(1, "device-1", "192.168.1.1")
	if err == nil {
		t.Error("expected connection limit error")
	}

	// After unregistering one, new connection should be allowed
	e.UnregisterConnection("conn-1")
	err = e.CheckConnection(1, "device-1", "192.168.1.1")
	if err != nil {
		t.Errorf("expected connection allowed after unregister: %v", err)
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

	up, down := e.GetSpeedLimits(1)
	if up == nil || *up != 1000 {
		t.Errorf("expected up=1000, got %v", up)
	}
	if down == nil || *down != 5000 {
		t.Errorf("expected down=5000, got %v", down)
	}

	// Subject without policy
	up, down = e.GetSpeedLimits(999)
	if up != nil || down != nil {
		t.Error("expected nil limits for unknown subject")
	}
}

func TestPolicyUpdate(t *testing.T) {
	e := New()

	maxConns := int64(5)
	e.UpdatePolicies([]Policy{{
		SubjectID:      1,
		MaxConnections: &maxConns,
	}})

	// Register connections
	e.RegisterConnection("conn-1", 1, "device-1", "192.168.1.1", "vless")
	e.RegisterConnection("conn-2", 1, "device-2", "192.168.1.2", "vless")

	// Update policy with stricter limit
	newMaxConns := int64(1)
	e.UpdatePolicies([]Policy{{
		SubjectID:      1,
		MaxConnections: &newMaxConns,
	}})

	// Existing connections remain, but new ones are rejected
	conns := e.GetActiveConnections(1)
	if len(conns) != 2 {
		t.Errorf("existing connections should remain, got %d", len(conns))
	}

	err := e.CheckConnection(1, "device-3", "192.168.1.3")
	if err == nil {
		t.Error("expected rejection with new stricter limit")
	}
}

func TestPolicyRemoval(t *testing.T) {
	e := New()

	maxConns := int64(2)
	e.UpdatePolicies([]Policy{{
		SubjectID:      1,
		MaxConnections: &maxConns,
	}})

	// Register connections
	e.RegisterConnection("conn-1", 1, "device-1", "192.168.1.1", "vless")
	e.RegisterConnection("conn-2", 1, "device-2", "192.168.1.2", "vless")

	// Remove policy (empty update)
	e.UpdatePolicies([]Policy{})

	// All connections should be terminated
	conns := e.GetActiveConnections(1)
	if len(conns) != 0 {
		t.Errorf("expected 0 connections after policy removal, got %d", len(conns))
	}
}

func TestCleanupStale(t *testing.T) {
	now := time.Now().UTC()
	e := New()
	e.now = func() time.Time { return now }

	// Register connections at different times
	e.RegisterConnection("conn-1", 1, "device-1", "192.168.1.1", "vless")

	// Move time forward
	e.now = func() time.Time { return now.Add(2 * time.Minute) }
	e.RegisterConnection("conn-2", 1, "device-2", "192.168.1.2", "vless")

	// Move time forward again
	e.now = func() time.Time { return now.Add(5 * time.Minute) }

	// Cleanup connections older than 3 minutes
	removed := e.CleanupStale(3 * time.Minute)

	if removed != 1 {
		t.Errorf("expected 1 stale connection removed, got %d", removed)
	}

	conns := e.GetActiveConnections(1)
	if len(conns) != 1 {
		t.Errorf("expected 1 remaining connection, got %d", len(conns))
	}
	if conns[0].ID != "conn-2" {
		t.Errorf("expected conn-2 to remain, got %s", conns[0].ID)
	}
}

func TestStats(t *testing.T) {
	e := New()

	maxDevices := int64(5)
	e.UpdatePolicies([]Policy{
		{SubjectID: 1, MaxDevices: &maxDevices},
		{SubjectID: 2, MaxDevices: &maxDevices},
	})

	e.RegisterConnection("conn-1", 1, "device-1", "192.168.1.1", "vless")
	e.RegisterConnection("conn-2", 1, "device-2", "192.168.1.2", "vless")
	e.RegisterConnection("conn-3", 2, "device-3", "192.168.1.3", "vmess")

	stats := e.Stats()

	if stats.TotalConnections != 3 {
		t.Errorf("expected 3 total connections, got %d", stats.TotalConnections)
	}
	if stats.TrackedSubjects != 2 {
		t.Errorf("expected 2 tracked subjects, got %d", stats.TrackedSubjects)
	}
	if stats.UniqueIPs != 3 {
		t.Errorf("expected 3 unique IPs, got %d", stats.UniqueIPs)
	}
	if stats.UniqueDevices != 3 {
		t.Errorf("expected 3 unique devices, got %d", stats.UniqueDevices)
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
