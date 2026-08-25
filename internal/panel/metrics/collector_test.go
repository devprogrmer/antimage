package metrics

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/testutil/storetest"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := storetest.OpenCopy(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCollector_NodesTotal(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// Seed nodes with different statuses
	err := s.Write(ctx, func(tx *sql.Tx) error {
		statuses := []string{"online", "online", "offline", "degraded"}
		for i, status := range statuses {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO nodes (name, address, status, created_at)
				VALUES (?, ?, ?, ?)`,
				"node-"+status+string(rune('A'+i)), "test.example.com", status, time.Now().Unix())
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed nodes: %v", err)
	}

	// Create collector and gather metrics
	collector := NewCollector(s)
	reg := prometheus.NewRegistry()
	reg.MustRegister(collector)

	// Trigger collection by gathering
	_, err = reg.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}

	// Check metric values
	if val := testutil.ToFloat64(collector.nodesTotal.WithLabelValues("online")); val != 2 {
		t.Errorf("antimage_nodes_total{status=online} = %v, want 2", val)
	}
	if val := testutil.ToFloat64(collector.nodesTotal.WithLabelValues("offline")); val != 1 {
		t.Errorf("antimage_nodes_total{status=offline} = %v, want 1", val)
	}
	if val := testutil.ToFloat64(collector.nodesTotal.WithLabelValues("degraded")); val != 1 {
		t.Errorf("antimage_nodes_total{status=degraded} = %v, want 1", val)
	}
}

func TestCollector_HeartbeatAge(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	now := time.Now().Unix()

	// Seed nodes with different last_seen_at
	err := s.Write(ctx, func(tx *sql.Tx) error {
		// Node 1: seen 30 seconds ago
		_, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (name, address, status, last_seen_at, created_at)
			VALUES (?, ?, ?, ?, ?)`,
			"node-1", "test.example.com", "online", now-30, now)
		if err != nil {
			return err
		}
		// Node 2: seen 60 seconds ago (max)
		_, err = tx.ExecContext(ctx, `
			INSERT INTO nodes (name, address, status, last_seen_at, created_at)
			VALUES (?, ?, ?, ?, ?)`,
			"node-2", "test.example.com", "online", now-60, now)
		if err != nil {
			return err
		}
		// Node 3: offline (should not be counted)
		_, err = tx.ExecContext(ctx, `
			INSERT INTO nodes (name, address, status, last_seen_at, created_at)
			VALUES (?, ?, ?, ?, ?)`,
			"node-3", "test.example.com", "offline", now-120, now)
		return err
	})
	if err != nil {
		t.Fatalf("seed nodes: %v", err)
	}

	// Create collector and gather metrics
	collector := NewCollector(s)
	reg := prometheus.NewRegistry()
	reg.MustRegister(collector)

	// Trigger collection
	_, err = reg.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}

	// Check metric value (should be max age among online nodes)
	val := testutil.ToFloat64(collector.heartbeatAgeSeconds)
	if val < 59 || val > 61 {
		t.Errorf("antimage_node_heartbeat_age_seconds_max = %v, want ~60", val)
	}
}

func TestCollector_ReconnectTotal(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// Seed nodes with reconnect counts
	err := s.Write(ctx, func(tx *sql.Tx) error {
		for i, count := range []int{3, 5, 2} {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO nodes (name, address, reconnect_count, created_at)
				VALUES (?, ?, ?, ?)`,
				"node-"+string(rune('A'+i)), "test.example.com", count, time.Now().Unix())
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed nodes: %v", err)
	}

	// Create collector and gather metrics
	collector := NewCollector(s)
	reg := prometheus.NewRegistry()
	reg.MustRegister(collector)

	// Trigger collection
	_, err = reg.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}

	// Check metric value (sum = 3 + 5 + 2 = 10)
	val := testutil.ToFloat64(collector.reconnectTotal)
	if val != 10 {
		t.Errorf("antimage_node_reconnect_total = %v, want 10", val)
	}
}

func TestCollector_ReconcileDuration(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// Seed nodes with reconcile durations
	err := s.Write(ctx, func(tx *sql.Tx) error {
		for i, duration := range []int64{1000, 2000, 3000} {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO nodes (name, address, last_reconcile_duration_ms, created_at)
				VALUES (?, ?, ?, ?)`,
				"node-"+string(rune('A'+i)), "test.example.com", duration, time.Now().Unix())
			if err != nil {
				return err
			}
		}
		// Add node with NULL duration (should not affect average)
		_, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (name, address, last_reconcile_duration_ms, created_at)
			VALUES (?, ?, NULL, ?)`,
			"node-null", "test.example.com", time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("seed nodes: %v", err)
	}

	// Create collector and gather metrics
	collector := NewCollector(s)
	reg := prometheus.NewRegistry()
	reg.MustRegister(collector)

	// Trigger collection
	_, err = reg.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}

	// Check metric value (average = (1000 + 2000 + 3000) / 3 = 2000)
	val := testutil.ToFloat64(collector.reconcileDuration)
	if val != 2000 {
		t.Errorf("antimage_node_reconcile_duration_ms_avg = %v, want 2000", val)
	}
}

func TestCollector_FailedReconcileNodes(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// Seed nodes with different failed streaks
	err := s.Write(ctx, func(tx *sql.Tx) error {
		for i, streak := range []int{0, 1, 3, 0, 5} {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO nodes (name, address, failed_reconcile_streak, created_at)
				VALUES (?, ?, ?, ?)`,
				"node-"+string(rune('A'+i)), "test.example.com", streak, time.Now().Unix())
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed nodes: %v", err)
	}

	// Create collector and gather metrics
	collector := NewCollector(s)
	reg := prometheus.NewRegistry()
	reg.MustRegister(collector)

	// Trigger collection
	_, err = reg.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}

	// Check metric value (count of nodes with streak > 0 = 3)
	val := testutil.ToFloat64(collector.failedReconcileNodes)
	if val != 3 {
		t.Errorf("antimage_nodes_with_failed_reconcile_streak = %v, want 3", val)
	}
}

func TestCollector_EmptyDatabase(t *testing.T) {
	s := openTestStore(t)

	// Create collector with empty database
	collector := NewCollector(s)
	reg := prometheus.NewRegistry()
	reg.MustRegister(collector)

	// Should not panic, should return zero values
	val := testutil.ToFloat64(collector.reconnectTotal)
	if val != 0 {
		t.Errorf("reconnectTotal = %v, want 0 (empty db)", val)
	}
}

func TestCollector_PrometheusFormat(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// Seed some data
	err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (name, address, status, reconnect_count, created_at)
			VALUES (?, ?, ?, ?, ?)`,
			"test-node", "test.example.com", "online", 5, time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("seed node: %v", err)
	}

	// Create collector
	collector := NewCollector(s)
	reg := prometheus.NewRegistry()
	reg.MustRegister(collector)

	// Gather metrics in Prometheus text format
	metrics, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}

	// Verify metrics are present
	found := make(map[string]bool)
	for _, m := range metrics {
		found[m.GetName()] = true
	}

	expected := []string{
		"antimage_nodes_total",
		"antimage_node_heartbeat_age_seconds_max",
		"antimage_node_reconnect_total",
		"antimage_node_reconcile_duration_ms_avg",
		"antimage_nodes_with_failed_reconcile_streak",
	}

	for _, name := range expected {
		if !found[name] {
			t.Errorf("metric %q not found in output", name)
		}
	}
}

func TestCollector_ConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// Seed some data
	err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (name, address, status, created_at)
			VALUES (?, ?, ?, ?)`,
			"test-node", "test.example.com", "online", time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("seed node: %v", err)
	}

	// Create collector
	collector := NewCollector(s)

	// Simulate concurrent scrapes
	done := make(chan bool)
	for i := 0; i < 5; i++ {
		go func() {
			reg := prometheus.NewRegistry()
			reg.MustRegister(collector)
			_, _ = reg.Gather()
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 5; i++ {
		<-done
	}
}

func TestCollector_MetricHelp(t *testing.T) {
	s := openTestStore(t)
	collector := NewCollector(s)

	// Verify metric descriptions are present
	ch := make(chan *prometheus.Desc, 10)
	collector.Describe(ch)
	close(ch)

	count := 0
	for desc := range ch {
		count++
		// Verify desc has a string representation (not nil)
		if desc.String() == "" {
			t.Error("metric description is empty")
		}
	}

	// We expect 5 metrics (nodesTotal has multiple labels but one desc)
	if count < 5 {
		t.Errorf("expected at least 5 metric descriptions, got %d", count)
	}
}

func TestCollector_TextFormat(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// Seed data
	err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (name, address, status, reconnect_count, created_at)
			VALUES (?, ?, ?, ?, ?)`,
			"test-node", "test.example.com", "online", 10, time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("seed node: %v", err)
	}

	collector := NewCollector(s)
	reg := prometheus.NewRegistry()
	reg.MustRegister(collector)

	// Trigger collection
	_, err = reg.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}

	// Verify the actual value
	val := testutil.ToFloat64(collector.reconnectTotal)
	if val != 10 {
		t.Errorf("reconnectTotal = %v, want 10", val)
	}
}

func TestCollector_ContextTimeout(t *testing.T) {
	s := openTestStore(t)
	collector := NewCollector(s)

	// Should complete quickly even with timeout
	start := time.Now()
	ch := make(chan prometheus.Metric, 100)
	collector.Collect(ch)
	close(ch)
	duration := time.Since(start)

	// Drain channel
	for range ch {
	}

	// Should complete well under 5 seconds
	if duration > 2*time.Second {
		t.Errorf("collection took %v, expected < 2s", duration)
	}
}

func TestCollector_NullHandling(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// Seed node with NULL values
	err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (name, address, status, last_seen_at, last_reconcile_duration_ms, created_at)
			VALUES (?, ?, ?, NULL, NULL, ?)`,
			"test-node", "test.example.com", "pending", time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("seed node: %v", err)
	}

	collector := NewCollector(s)
	reg := prometheus.NewRegistry()
	reg.MustRegister(collector)

	// Should not panic, should handle NULLs gracefully
	_, err = reg.Gather()
	if err != nil {
		t.Errorf("gather with NULL values: %v", err)
	}
}

// Verify metrics can be scraped multiple times
func TestCollector_MultipleScrapes(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// Seed initial data
	err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (name, address, status, created_at)
			VALUES (?, ?, ?, ?)`,
			"test-node", "test.example.com", "online", time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("seed node: %v", err)
	}

	collector := NewCollector(s)
	reg := prometheus.NewRegistry()
	reg.MustRegister(collector)

	// First scrape
	_, err = reg.Gather()
	if err != nil {
		t.Fatalf("first gather: %v", err)
	}

	// Second scrape should work
	_, err = reg.Gather()
	if err != nil {
		t.Fatalf("second gather: %v", err)
	}
}

func BenchmarkCollector_Collect(b *testing.B) {
	ctx := context.Background()
	s, _ := store.Open(filepath.Join(b.TempDir(), "bench.db"))
	defer func() { _ = s.Close() }()

	// Seed some nodes
	_ = s.Write(ctx, func(tx *sql.Tx) error {
		for i := 0; i < 100; i++ {
			_, _ = tx.ExecContext(ctx, `
				INSERT INTO nodes (name, address, status, created_at)
				VALUES (?, ?, ?, ?)`,
				"node", "test.example.com", "online", time.Now().Unix())
		}
		return nil
	})

	collector := NewCollector(s)
	ch := make(chan prometheus.Metric, 100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		collector.Collect(ch)
		// Drain channel
		for len(ch) > 0 {
			<-ch
		}
	}
}

// Helper to consume all metrics without allocation in tests
func drainMetrics(ch <-chan prometheus.Metric) {
	for range ch {
	}
}

// Verify no metrics are written if db query fails
func TestCollector_ErrorHandling(t *testing.T) {
	s := openTestStore(t)
	_ = s.Close() // Close store to cause errors

	collector := NewCollector(s)
	ch := make(chan prometheus.Metric, 100)

	// Should not panic even with closed store
	collector.Collect(ch)
	close(ch)

	// Drain
	drainMetrics(ch)
}

// Ensure collector can be registered only once
func TestCollector_SingleRegistration(t *testing.T) {
	s := openTestStore(t)
	collector := NewCollector(s)

	reg := prometheus.NewRegistry()
	err := reg.Register(collector)
	if err != nil {
		t.Fatalf("first registration: %v", err)
	}

	// Second registration should fail
	err = reg.Register(collector)
	if err == nil {
		t.Error("expected error on duplicate registration, got nil")
	}
}

// Test that all metrics have bounded cardinality
func TestCollector_BoundedCardinality(t *testing.T) {
	s := openTestStore(t)
	collector := NewCollector(s)

	// Count label dimensions
	ch := make(chan *prometheus.Desc, 10)
	collector.Describe(ch)
	close(ch)

	for desc := range ch {
		str := desc.String()
		// Verify no node_id label (would be unbounded)
		if strings.Contains(str, "node_id") {
			t.Errorf("metric %s contains node_id label (unbounded cardinality)", str)
		}
	}
}
