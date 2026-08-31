package enforcement

import (
	"context"
	"testing"
)

// TestTrafficShaper tests the tc-based traffic shaping implementation.
// This test requires:
// - Linux OS
// - tc (iproute2) installed
// - CAP_NET_ADMIN capability or root access
// - Network interface available
func TestTrafficShaper(t *testing.T) {
	if !IsSupported() {
		t.Skip("Traffic shaping not supported on this platform (requires Linux + tc)")
	}

	// Use loopback interface for testing
	iface := "lo"

	ts, err := NewTrafficShaper(iface)
	if err != nil {
		t.Skipf("Cannot initialize traffic shaper: %v (may need CAP_NET_ADMIN)", err)
	}
	defer ts.Cleanup()

	ctx := context.Background()

	t.Run("apply_and_remove_limit", func(t *testing.T) {
		subjectID := int64(100)
		sourceIP := "127.0.0.1"
		uploadKbps := int64(5000) // 5 Mbps

		// Apply limit
		if err := ts.ApplyLimit(ctx, subjectID, sourceIP, uploadKbps, 0); err != nil {
			t.Fatalf("ApplyLimit failed: %v", err)
		}

		// Verify shape exists
		if _, exists := ts.shapes[subjectID]; !exists {
			t.Error("Shape not registered")
		}

		// Remove limit
		if err := ts.RemoveLimit(ctx, subjectID); err != nil {
			t.Fatalf("RemoveLimit failed: %v", err)
		}

		// Verify shape removed
		if _, exists := ts.shapes[subjectID]; exists {
			t.Error("Shape not removed")
		}
	})

	t.Run("multiple_subjects", func(t *testing.T) {
		subjects := []struct {
			id         int64
			ip         string
			uploadKbps int64
		}{
			{101, "192.168.1.2", 1000},  // 1 Mbps
			{102, "192.168.1.3", 5000},  // 5 Mbps
			{103, "192.168.1.4", 10000}, // 10 Mbps
		}

		// Apply limits for all subjects
		for _, s := range subjects {
			if err := ts.ApplyLimit(ctx, s.id, s.ip, s.uploadKbps, 0); err != nil {
				t.Fatalf("ApplyLimit failed for subject %d: %v", s.id, err)
			}
		}

		// Verify all registered
		if len(ts.shapes) != len(subjects) {
			t.Errorf("Expected %d shapes, got %d", len(subjects), len(ts.shapes))
		}

		// Remove all
		for _, s := range subjects {
			if err := ts.RemoveLimit(ctx, s.id); err != nil {
				t.Errorf("RemoveLimit failed for subject %d: %v", s.id, err)
			}
		}

		// Verify all removed
		if len(ts.shapes) != 0 {
			t.Errorf("Expected 0 shapes after removal, got %d", len(ts.shapes))
		}
	})

	t.Run("idempotent_removal", func(t *testing.T) {
		subjectID := int64(200)

		// Remove non-existent limit (should not error)
		if err := ts.RemoveLimit(ctx, subjectID); err != nil {
			t.Errorf("RemoveLimit should be idempotent, got error: %v", err)
		}
	})
}

// TestTrafficShaperIntegration tests actual bandwidth enforcement with real traffic.
// This requires a more complex setup and is marked as integration test.
func TestTrafficShaperIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	if !IsSupported() {
		t.Skip("Traffic shaping not supported on this platform")
	}

	t.Skip("Integration test requires network traffic generation - implement separately")

	// TODO: Implement full integration test with:
	// 1. Create test server
	// 2. Apply bandwidth limit via tc
	// 3. Generate sustained traffic
	// 4. Measure actual throughput
	// 5. Verify enforcement
}

// BenchmarkTrafficShaper measures performance of tc operations
func BenchmarkTrafficShaper(b *testing.B) {
	if !IsSupported() {
		b.Skip("Traffic shaping not supported")
	}

	ts, err := NewTrafficShaper("lo")
	if err != nil {
		b.Skipf("Cannot initialize: %v", err)
	}
	defer ts.Cleanup()

	ctx := context.Background()

	b.Run("ApplyLimit", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			subjectID := int64(1000 + i)
			sourceIP := "192.168.1.1"
			ts.ApplyLimit(ctx, subjectID, sourceIP, 5000, 0)
		}
	})

	b.Run("RemoveLimit", func(b *testing.B) {
		// Pre-create limits
		for i := 0; i < b.N; i++ {
			subjectID := int64(2000 + i)
			sourceIP := "192.168.1.1"
			ts.ApplyLimit(ctx, subjectID, sourceIP, 5000, 0)
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			subjectID := int64(2000 + i)
			ts.RemoveLimit(ctx, subjectID)
		}
	})
}
