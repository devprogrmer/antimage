package nodes

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

// NodeMetrics represents connection and reconciliation performance metrics.
type NodeMetrics struct {
	ReconnectCount          int
	LastReconcileDurationMs *int64
	FailedReconcileStreak   int
	AvgRTTMs                *int64 // Average of last 10 RTT samples
}

// RecordRTT stores an RTT measurement for a node.
func RecordRTT(ctx context.Context, s *store.Store, nodeID int64, rttMs int64, now time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO connection_metrics (node_id, measured_at, rtt_ms, reconnect_reason)
			VALUES (?, ?, ?, NULL)`,
			nodeID, now.Unix(), rttMs)
		return err
	})
}

// RecordReconnect increments the reconnect counter and logs the reason.
func RecordReconnect(ctx context.Context, s *store.Store, nodeID int64, reason string, now time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		// Increment reconnect counter
		_, err := tx.ExecContext(ctx, `
			UPDATE nodes SET reconnect_count = reconnect_count + 1 WHERE id = ?`,
			nodeID)
		if err != nil {
			return fmt.Errorf("increment reconnect_count: %w", err)
		}

		// Log reconnection event
		_, err = tx.ExecContext(ctx, `
			INSERT INTO connection_metrics (node_id, measured_at, rtt_ms, reconnect_reason)
			VALUES (?, ?, NULL, ?)`,
			nodeID, now.Unix(), reason)
		return err
	})
}

// UpdateReconcileMetrics updates reconciliation performance metrics.
func UpdateReconcileMetrics(ctx context.Context, s *store.Store, nodeID int64, durationMs int64, failed bool) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if failed {
			_, err := tx.ExecContext(ctx, `
				UPDATE nodes
				SET last_reconcile_duration_ms = ?,
				    failed_reconcile_streak = failed_reconcile_streak + 1
				WHERE id = ?`,
				durationMs, nodeID)
			return err
		}

		// Success: reset streak
		_, err := tx.ExecContext(ctx, `
			UPDATE nodes
			SET last_reconcile_duration_ms = ?,
			    failed_reconcile_streak = 0
			WHERE id = ?`,
			durationMs, nodeID)
		return err
	})
}

// GetMetrics retrieves connection and reconciliation metrics for a node.
func GetMetrics(ctx context.Context, s *store.Store, nodeID int64) (*NodeMetrics, error) {
	var m NodeMetrics
	var lastDuration sql.NullInt64

	// Fetch node-level metrics
	err := s.Read().QueryRowContext(ctx, `
		SELECT reconnect_count, last_reconcile_duration_ms, failed_reconcile_streak
		FROM nodes WHERE id = ?`, nodeID).Scan(
		&m.ReconnectCount, &lastDuration, &m.FailedReconcileStreak)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("node not found")
	}
	if err != nil {
		return nil, err
	}

	if lastDuration.Valid {
		m.LastReconcileDurationMs = &lastDuration.Int64
	}

	// Calculate average RTT from last 10 samples
	var avgRTT sql.NullFloat64
	err = s.Read().QueryRowContext(ctx, `
		SELECT AVG(rtt_ms) FROM (
			SELECT rtt_ms FROM connection_metrics
			WHERE node_id = ? AND rtt_ms IS NOT NULL
			ORDER BY measured_at DESC LIMIT 10
		)`, nodeID).Scan(&avgRTT)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("calculate avg RTT: %w", err)
	}

	if avgRTT.Valid {
		rounded := int64(avgRTT.Float64)
		m.AvgRTTMs = &rounded
	}

	return &m, nil
}
