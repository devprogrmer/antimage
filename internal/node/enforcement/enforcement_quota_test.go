package enforcement

import (
	"testing"
)

// TestImmediateQuotaEnforcement verifies that quota is checked at connection admission time.
func TestImmediateQuotaEnforcement(t *testing.T) {
	e := New()

	t.Run("connection rejected when quota exhausted", func(t *testing.T) {
		// Set quota: 1GB total, 1GB used (exhausted)
		quotaBytes := int64(1024 * 1024 * 1024)      // 1 GB
		usedBytes := int64(1024 * 1024 * 1024)       // 1 GB (100%)

		policy := Policy{
			SubjectID:      100,
			QuotaBytes:     &quotaBytes,
			QuotaUsedBytes: &usedBytes,
		}
		e.UpdatePolicies([]Policy{policy})

		// Try to establish connection
		err := e.CheckAndRegisterConnection("conn1", 100, "device1", "10.0.0.1", "vless")

		if err == nil {
			t.Error("expected connection to be rejected (quota exhausted)")
		}

		if _, ok := err.(*ErrPolicyViolation); !ok {
			t.Errorf("expected ErrPolicyViolation, got %T", err)
		}

		// Verify connection not registered
		conns := e.GetActiveConnections(100)
		if len(conns) != 0 {
			t.Errorf("expected 0 connections, got %d", len(conns))
		}
	})

	t.Run("connection allowed when quota available", func(t *testing.T) {
		e2 := New()

		// Set quota: 1GB total, 500MB used (50%)
		quotaBytes := int64(1024 * 1024 * 1024)      // 1 GB
		usedBytes := int64(512 * 1024 * 1024)        // 512 MB (50%)

		policy := Policy{
			SubjectID:      100,
			QuotaBytes:     &quotaBytes,
			QuotaUsedBytes: &usedBytes,
		}
		e2.UpdatePolicies([]Policy{policy})

		// Connection should succeed
		err := e2.CheckAndRegisterConnection("conn1", 100, "device1", "10.0.0.1", "vless")

		if err != nil {
			t.Errorf("expected connection to be allowed (quota available), got error: %v", err)
		}

		// Verify connection registered
		conns := e2.GetActiveConnections(100)
		if len(conns) != 1 {
			t.Errorf("expected 1 connection, got %d", len(conns))
		}
	})

	t.Run("connection allowed when at 99% quota", func(t *testing.T) {
		e3 := New()

		// Set quota: 1GB total, 990MB used (99%)
		quotaBytes := int64(1024 * 1024 * 1024)      // 1 GB
		usedBytes := int64(1013 * 1024 * 1024)       // ~99%

		policy := Policy{
			SubjectID:      100,
			QuotaBytes:     &quotaBytes,
			QuotaUsedBytes: &usedBytes,
		}
		e3.UpdatePolicies([]Policy{policy})

		// Connection should still succeed (not exhausted yet)
		err := e3.CheckAndRegisterConnection("conn1", 100, "device1", "10.0.0.1", "vless")

		if err != nil {
			t.Errorf("expected connection to be allowed (quota not exhausted), got error: %v", err)
		}
	})

	t.Run("connection rejected exactly at quota", func(t *testing.T) {
		e4 := New()

		// Set quota: exactly at limit
		quotaBytes := int64(1024 * 1024 * 1024)      // 1 GB
		usedBytes := int64(1024 * 1024 * 1024)       // 1 GB (exactly 100%)

		policy := Policy{
			SubjectID:      100,
			QuotaBytes:     &quotaBytes,
			QuotaUsedBytes: &usedBytes,
		}
		e4.UpdatePolicies([]Policy{policy})

		// Connection should be rejected (>= check, not > check)
		err := e4.CheckAndRegisterConnection("conn1", 100, "device1", "10.0.0.1", "vless")

		if err == nil {
			t.Error("expected connection to be rejected (quota exactly at limit)")
		}
	})

	t.Run("connection rejected when over quota", func(t *testing.T) {
		e5 := New()

		// Set quota: exceeded
		quotaBytes := int64(1024 * 1024 * 1024)      // 1 GB
		usedBytes := int64(1100 * 1024 * 1024)       // 1.1 GB (110%)

		policy := Policy{
			SubjectID:      100,
			QuotaBytes:     &quotaBytes,
			QuotaUsedBytes: &usedBytes,
		}
		e5.UpdatePolicies([]Policy{policy})

		// Connection should be rejected
		err := e5.CheckAndRegisterConnection("conn1", 100, "device1", "10.0.0.1", "vless")

		if err == nil {
			t.Error("expected connection to be rejected (quota exceeded)")
		}
	})

	t.Run("connection allowed when quota not set", func(t *testing.T) {
		e6 := New()

		// No quota set (nil)
		policy := Policy{
			SubjectID:      100,
			QuotaBytes:     nil,
			QuotaUsedBytes: nil,
		}
		e6.UpdatePolicies([]Policy{policy})

		// Connection should succeed (no quota limit)
		err := e6.CheckAndRegisterConnection("conn1", 100, "device1", "10.0.0.1", "vless")

		if err != nil {
			t.Errorf("expected connection to be allowed (no quota set), got error: %v", err)
		}
	})

	t.Run("connection allowed when quota bytes nil but used set", func(t *testing.T) {
		e7 := New()

		// Quota bytes not set, but used bytes set (edge case)
		usedBytes := int64(1024 * 1024 * 1024)
		policy := Policy{
			SubjectID:      100,
			QuotaBytes:     nil,
			QuotaUsedBytes: &usedBytes,
		}
		e7.UpdatePolicies([]Policy{policy})

		// Connection should succeed (no quota limit enforced)
		err := e7.CheckAndRegisterConnection("conn1", 100, "device1", "10.0.0.1", "vless")

		if err != nil {
			t.Errorf("expected connection to be allowed (quota bytes nil), got error: %v", err)
		}
	})

	t.Run("connection allowed when used bytes nil but quota set", func(t *testing.T) {
		e8 := New()

		// Quota set but used bytes not tracked (edge case)
		quotaBytes := int64(1024 * 1024 * 1024)
		policy := Policy{
			SubjectID:      100,
			QuotaBytes:     &quotaBytes,
			QuotaUsedBytes: nil,
		}
		e8.UpdatePolicies([]Policy{policy})

		// Connection should succeed (no used bytes to compare)
		err := e8.CheckAndRegisterConnection("conn1", 100, "device1", "10.0.0.1", "vless")

		if err != nil {
			t.Errorf("expected connection to be allowed (used bytes nil), got error: %v", err)
		}
	})
}

// TestQuotaUpdateDuringActiveConnections verifies quota updates affect new connections.
func TestQuotaUpdateDuringActiveConnections(t *testing.T) {
	e := New()

	// Start with available quota
	quotaBytes := int64(1024 * 1024 * 1024)      // 1 GB
	usedBytes := int64(512 * 1024 * 1024)        // 512 MB (50%)

	policy := Policy{
		SubjectID:      100,
		QuotaBytes:     &quotaBytes,
		QuotaUsedBytes: &usedBytes,
	}
	e.UpdatePolicies([]Policy{policy})

	// Establish 2 connections
	if err := e.CheckAndRegisterConnection("conn1", 100, "device1", "10.0.0.1", "vless"); err != nil {
		t.Fatalf("conn1 failed: %v", err)
	}
	if err := e.CheckAndRegisterConnection("conn2", 100, "device2", "10.0.0.2", "vless"); err != nil {
		t.Fatalf("conn2 failed: %v", err)
	}

	if len(e.GetActiveConnections(100)) != 2 {
		t.Fatal("expected 2 active connections")
	}

	// Update policy: quota now exhausted
	exhaustedBytes := int64(1024 * 1024 * 1024)  // 1 GB used (100%)
	policyExhausted := Policy{
		SubjectID:      100,
		QuotaBytes:     &quotaBytes,
		QuotaUsedBytes: &exhaustedBytes,
	}
	e.UpdatePolicies([]Policy{policyExhausted})

	// Existing connections should remain active (not terminated)
	if len(e.GetActiveConnections(100)) != 2 {
		t.Error("existing connections should not be terminated by quota exhaustion")
	}

	// But new connection should be rejected
	err := e.CheckAndRegisterConnection("conn3", 100, "device3", "10.0.0.3", "vless")
	if err == nil {
		t.Error("expected new connection to be rejected (quota exhausted)")
	}

	// Still only 2 connections
	if len(e.GetActiveConnections(100)) != 2 {
		t.Errorf("expected 2 connections, got %d", len(e.GetActiveConnections(100)))
	}
}

// TestQuotaIsolationBetweenSubjects verifies quota is per-subject.
func TestQuotaIsolationBetweenSubjects(t *testing.T) {
	e := New()

	// Subject 100: quota exhausted
	quota100 := int64(1024 * 1024 * 1024)
	used100 := int64(1024 * 1024 * 1024)

	// Subject 200: quota available
	quota200 := int64(1024 * 1024 * 1024)
	used200 := int64(512 * 1024 * 1024)

	e.UpdatePolicies([]Policy{
		{SubjectID: 100, QuotaBytes: &quota100, QuotaUsedBytes: &used100},
		{SubjectID: 200, QuotaBytes: &quota200, QuotaUsedBytes: &used200},
	})

	// Subject 100 should be rejected
	err100 := e.CheckAndRegisterConnection("conn100", 100, "device1", "10.0.0.1", "vless")
	if err100 == nil {
		t.Error("subject 100 should be rejected (quota exhausted)")
	}

	// Subject 200 should succeed
	err200 := e.CheckAndRegisterConnection("conn200", 200, "device1", "10.0.0.1", "vless")
	if err200 != nil {
		t.Errorf("subject 200 should succeed (quota available): %v", err200)
	}

	// Verify subject 200 connection registered, subject 100 not
	if len(e.GetActiveConnections(100)) != 0 {
		t.Error("subject 100 should have 0 connections")
	}
	if len(e.GetActiveConnections(200)) != 1 {
		t.Error("subject 200 should have 1 connection")
	}
}

// TestQuotaWithZeroLimit verifies zero quota blocks connections.
func TestQuotaWithZeroLimit(t *testing.T) {
	e := New()

	// Zero quota (deny all)
	quotaBytes := int64(0)
	usedBytes := int64(0)

	policy := Policy{
		SubjectID:      100,
		QuotaBytes:     &quotaBytes,
		QuotaUsedBytes: &usedBytes,
	}
	e.UpdatePolicies([]Policy{policy})

	// Connection should be rejected (0 >= 0)
	err := e.CheckAndRegisterConnection("conn1", 100, "device1", "10.0.0.1", "vless")
	if err == nil {
		t.Error("expected connection to be rejected (zero quota)")
	}
}

// TestQuotaCombinedWithOtherLimits verifies quota works alongside other limits.
func TestQuotaCombinedWithOtherLimits(t *testing.T) {
	e := New()

	// Set both quota and connection limit
	quotaBytes := int64(1024 * 1024 * 1024)
	usedBytes := int64(512 * 1024 * 1024)
	maxConns := int64(2)

	policy := Policy{
		SubjectID:      100,
		QuotaBytes:     &quotaBytes,
		QuotaUsedBytes: &usedBytes,
		MaxConnections: &maxConns,
	}
	e.UpdatePolicies([]Policy{policy})

	// First connection: succeeds (quota OK, under connection limit)
	if err := e.CheckAndRegisterConnection("conn1", 100, "device1", "10.0.0.1", "vless"); err != nil {
		t.Fatalf("conn1 failed: %v", err)
	}

	// Second connection: succeeds (quota OK, at connection limit)
	if err := e.CheckAndRegisterConnection("conn2", 100, "device2", "10.0.0.2", "vless"); err != nil {
		t.Fatalf("conn2 failed: %v", err)
	}

	// Third connection: rejected by connection limit (before quota check)
	err := e.CheckAndRegisterConnection("conn3", 100, "device3", "10.0.0.3", "vless")
	if err == nil {
		t.Error("expected conn3 to be rejected (connection limit)")
	}

	// Update to exhaust quota but increase connection limit
	exhaustedBytes := int64(1024 * 1024 * 1024)
	maxConns10 := int64(10)
	policyExhausted := Policy{
		SubjectID:      100,
		QuotaBytes:     &quotaBytes,
		QuotaUsedBytes: &exhaustedBytes,
		MaxConnections: &maxConns10,
	}
	e.UpdatePolicies([]Policy{policyExhausted})

	// Fourth connection: rejected by quota (even though connection limit allows)
	err2 := e.CheckAndRegisterConnection("conn4", 100, "device4", "10.0.0.4", "vless")
	if err2 == nil {
		t.Error("expected conn4 to be rejected (quota exhausted)")
	}
}
