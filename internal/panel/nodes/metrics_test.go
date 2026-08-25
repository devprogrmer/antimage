package nodes

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/testutil/storetest"
)

func openTestStoreForMetrics(t *testing.T) *store.Store {
	t.Helper()
	s, err := storetest.OpenCopy(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRecordRTT(t *testing.T) {
	ctx := context.Background()
	s := openTestStoreForMetrics(t)

	// Seed a node
	var nodeID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, created_at) VALUES (?, ?, ?)`,
			"test-node", "test.example.com", time.Now().Unix())
		if err != nil {
			return err
		}
		nodeID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("seed node: %v", err)
	}

	// Record RTT
	now := time.Unix(1_700_000_000, 0).UTC()
	err = RecordRTT(ctx, s, nodeID, 42, now)
	if err != nil {
		t.Fatalf("RecordRTT: %v", err)
	}

	// Verify stored
	var rtt sql.NullInt64
	err = s.Read().QueryRowContext(ctx,
		`SELECT rtt_ms FROM connection_metrics WHERE node_id = ?`, nodeID).Scan(&rtt)
	if err != nil {
		t.Fatalf("query RTT: %v", err)
	}
	if !rtt.Valid || rtt.Int64 != 42 {
		t.Errorf("rtt = %v, want 42", rtt)
	}
}

func TestRecordReconnect(t *testing.T) {
	ctx := context.Background()
	s := openTestStoreForMetrics(t)

	// Seed a node
	var nodeID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, created_at) VALUES (?, ?, ?)`,
			"test-node", "test.example.com", time.Now().Unix())
		if err != nil {
			return err
		}
		nodeID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("seed node: %v", err)
	}

	// Record first reconnect
	now := time.Unix(1_700_000_000, 0).UTC()
	err = RecordReconnect(ctx, s, nodeID, "timeout", now)
	if err != nil {
		t.Fatalf("RecordReconnect (1): %v", err)
	}

	// Verify counter incremented
	var count int
	err = s.Read().QueryRowContext(ctx,
		`SELECT reconnect_count FROM nodes WHERE id = ?`, nodeID).Scan(&count)
	if err != nil {
		t.Fatalf("query reconnect_count: %v", err)
	}
	if count != 1 {
		t.Errorf("reconnect_count = %d, want 1", count)
	}

	// Record second reconnect
	err = RecordReconnect(ctx, s, nodeID, "network_error", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("RecordReconnect (2): %v", err)
	}

	// Verify counter incremented again
	err = s.Read().QueryRowContext(ctx,
		`SELECT reconnect_count FROM nodes WHERE id = ?`, nodeID).Scan(&count)
	if err != nil {
		t.Fatalf("query reconnect_count: %v", err)
	}
	if count != 2 {
		t.Errorf("reconnect_count = %d, want 2", count)
	}

	// Verify reconnect reason logged
	var reason sql.NullString
	err = s.Read().QueryRowContext(ctx,
		`SELECT reconnect_reason FROM connection_metrics WHERE node_id = ? ORDER BY measured_at DESC LIMIT 1`,
		nodeID).Scan(&reason)
	if err != nil {
		t.Fatalf("query reconnect_reason: %v", err)
	}
	if !reason.Valid || reason.String != "network_error" {
		t.Errorf("reconnect_reason = %v, want network_error", reason)
	}
}

func TestUpdateReconcileMetrics_Success(t *testing.T) {
	ctx := context.Background()
	s := openTestStoreForMetrics(t)

	// Seed a node with a failed streak
	var nodeID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, created_at, failed_reconcile_streak)
			 VALUES (?, ?, ?, ?)`,
			"test-node", "test.example.com", time.Now().Unix(), 3)
		if err != nil {
			return err
		}
		nodeID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("seed node: %v", err)
	}

	// Update with success
	err = UpdateReconcileMetrics(ctx, s, nodeID, 1500, false)
	if err != nil {
		t.Fatalf("UpdateReconcileMetrics: %v", err)
	}

	// Verify duration updated and streak reset
	var duration sql.NullInt64
	var streak int
	err = s.Read().QueryRowContext(ctx,
		`SELECT last_reconcile_duration_ms, failed_reconcile_streak FROM nodes WHERE id = ?`,
		nodeID).Scan(&duration, &streak)
	if err != nil {
		t.Fatalf("query metrics: %v", err)
	}

	if !duration.Valid || duration.Int64 != 1500 {
		t.Errorf("duration = %v, want 1500", duration)
	}
	if streak != 0 {
		t.Errorf("failed_reconcile_streak = %d, want 0 (reset on success)", streak)
	}
}

func TestUpdateReconcileMetrics_Failure(t *testing.T) {
	ctx := context.Background()
	s := openTestStoreForMetrics(t)

	// Seed a node
	var nodeID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, created_at) VALUES (?, ?, ?)`,
			"test-node", "test.example.com", time.Now().Unix())
		if err != nil {
			return err
		}
		nodeID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("seed node: %v", err)
	}

	// Update with failure
	err = UpdateReconcileMetrics(ctx, s, nodeID, 2000, true)
	if err != nil {
		t.Fatalf("UpdateReconcileMetrics (1): %v", err)
	}

	// Verify streak incremented
	var streak int
	err = s.Read().QueryRowContext(ctx,
		`SELECT failed_reconcile_streak FROM nodes WHERE id = ?`, nodeID).Scan(&streak)
	if err != nil {
		t.Fatalf("query streak: %v", err)
	}
	if streak != 1 {
		t.Errorf("failed_reconcile_streak = %d, want 1", streak)
	}

	// Another failure
	err = UpdateReconcileMetrics(ctx, s, nodeID, 2100, true)
	if err != nil {
		t.Fatalf("UpdateReconcileMetrics (2): %v", err)
	}

	// Verify streak incremented again
	err = s.Read().QueryRowContext(ctx,
		`SELECT failed_reconcile_streak FROM nodes WHERE id = ?`, nodeID).Scan(&streak)
	if err != nil {
		t.Fatalf("query streak: %v", err)
	}
	if streak != 2 {
		t.Errorf("failed_reconcile_streak = %d, want 2", streak)
	}
}

func TestGetMetrics(t *testing.T) {
	ctx := context.Background()
	s := openTestStoreForMetrics(t)

	// Seed a node with metrics
	var nodeID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (name, address, created_at, reconnect_count, last_reconcile_duration_ms, failed_reconcile_streak)
			VALUES (?, ?, ?, ?, ?, ?)`,
			"test-node", "test.example.com", time.Now().Unix(), 5, 1200, 2)
		if err != nil {
			return err
		}
		nodeID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("seed node: %v", err)
	}

	// Seed RTT samples
	now := time.Unix(1_700_000_000, 0).UTC()
	rtts := []int64{10, 15, 20, 25, 30, 35, 40, 45, 50, 55}
	for i, rtt := range rtts {
		err = RecordRTT(ctx, s, nodeID, rtt, now.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatalf("RecordRTT: %v", err)
		}
	}

	// Get metrics
	m, err := GetMetrics(ctx, s, nodeID)
	if err != nil {
		t.Fatalf("GetMetrics: %v", err)
	}

	if m.ReconnectCount != 5 {
		t.Errorf("ReconnectCount = %d, want 5", m.ReconnectCount)
	}
	if m.LastReconcileDurationMs == nil || *m.LastReconcileDurationMs != 1200 {
		t.Errorf("LastReconcileDurationMs = %v, want 1200", m.LastReconcileDurationMs)
	}
	if m.FailedReconcileStreak != 2 {
		t.Errorf("FailedReconcileStreak = %d, want 2", m.FailedReconcileStreak)
	}

	// Average of 10, 15, 20, 25, 30, 35, 40, 45, 50, 55 = 32.5 → 32
	if m.AvgRTTMs == nil || *m.AvgRTTMs != 32 {
		t.Errorf("AvgRTTMs = %v, want 32", m.AvgRTTMs)
	}
}

func TestGetMetrics_NoRTTSamples(t *testing.T) {
	ctx := context.Background()
	s := openTestStoreForMetrics(t)

	// Seed a node with no RTT samples
	var nodeID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, created_at) VALUES (?, ?, ?)`,
			"test-node", "test.example.com", time.Now().Unix())
		if err != nil {
			return err
		}
		nodeID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("seed node: %v", err)
	}

	// Get metrics
	m, err := GetMetrics(ctx, s, nodeID)
	if err != nil {
		t.Fatalf("GetMetrics: %v", err)
	}

	if m.AvgRTTMs != nil {
		t.Errorf("AvgRTTMs = %v, want nil (no samples)", m.AvgRTTMs)
	}
}

func TestGetMetrics_NodeNotFound(t *testing.T) {
	ctx := context.Background()
	s := openTestStoreForMetrics(t)

	// Query non-existent node
	_, err := GetMetrics(ctx, s, 999)
	if err == nil {
		t.Error("expected error for non-existent node, got nil")
	}
}

func TestConnectionMetricsRetention(t *testing.T) {
	ctx := context.Background()
	s := openTestStoreForMetrics(t)

	// Seed a node
	var nodeID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, created_at) VALUES (?, ?, ?)`,
			"test-node", "test.example.com", time.Now().Unix())
		if err != nil {
			return err
		}
		nodeID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("seed node: %v", err)
	}

	// Insert old metric (8 days ago)
	oldTime := time.Now().Add(-8 * 24 * time.Hour).Unix()
	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO connection_metrics (node_id, measured_at, rtt_ms, reconnect_reason)
			VALUES (?, ?, ?, NULL)`,
			nodeID, oldTime, 100)
		return err
	})
	if err != nil {
		t.Fatalf("insert old metric: %v", err)
	}

	// Insert new metric (triggers cleanup)
	now := time.Unix(1_700_000_000, 0).UTC()
	err = RecordRTT(ctx, s, nodeID, 50, now)
	if err != nil {
		t.Fatalf("RecordRTT: %v", err)
	}

	// Verify old metric was cleaned up
	var count int
	err = s.Read().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM connection_metrics WHERE node_id = ?`, nodeID).Scan(&count)
	if err != nil {
		t.Fatalf("count metrics: %v", err)
	}

	// Should only have the new metric (old one cleaned up by trigger)
	if count != 1 {
		t.Errorf("count = %d, want 1 (old metric cleaned up)", count)
	}
}
