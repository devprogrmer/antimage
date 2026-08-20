package nodes

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/amyrm/antimage/internal/panel/store"
)

// UsageDelta is one subject's traffic delta since the last report.
type UsageDelta struct {
	SubjectID     int64
	UplinkBytes   uint64
	DownlinkBytes uint64
}

// IngestUsageReport processes accounting deltas from a node.
// SP3 design decision 3: at-least-once delivery made exact by idempotency key
// (node_id, sequence). A repeated sequence is silently ignored.
// SP3 invariant 10: a usage delta is applied at most once.
// SP3 invariant 11: usage is never decreased; a backwards counter is a restart.
func IngestUsageReport(
	ctx context.Context, st *store.Store, nodeID, sequence int64, samples []UsageDelta, now int64,
) error {
	return st.Write(ctx, func(tx *sql.Tx) error {
		// Check idempotency: has this (node_id, sequence) been applied?
		var exists bool
		err := tx.QueryRowContext(ctx,
			`SELECT 1 FROM usage_deltas WHERE node_id = ? AND sequence = ? LIMIT 1`,
			nodeID, sequence).Scan(&exists)
		if err == nil {
			// Already applied. Ignore silently (at-least-once delivery).
			return nil
		}
		if err != sql.ErrNoRows {
			return fmt.Errorf("check idempotency: %w", err)
		}

		// Apply each delta.
		for _, sample := range samples {
			// Insert raw delta.
			_, err := tx.ExecContext(ctx, `
				INSERT INTO usage_deltas (node_id, subject_id, sequence, uplink_bytes, downlink_bytes, created_at)
				VALUES (?, ?, ?, ?, ?, ?)`,
				nodeID, sample.SubjectID, sequence, sample.UplinkBytes, sample.DownlinkBytes, now)
			if err != nil {
				return fmt.Errorf("insert usage delta: %w", err)
			}

			// Update subject's running total (invariant 11: never decrease).
			_, err = tx.ExecContext(ctx, `
				UPDATE subjects
				SET quota_used_bytes = quota_used_bytes + ?
				WHERE id = ?`,
				sample.UplinkBytes+sample.DownlinkBytes, sample.SubjectID)
			if err != nil {
				return fmt.Errorf("update subject quota usage: %w", err)
			}
		}

		return nil
	})
}

// PruneUsageDeltas removes raw deltas older than retentionSeconds.
// SP3 design decision 6: raw deltas are kept briefly for forensics; rollups
// answer operational questions. This should be called by a background sweeper.
func PruneUsageDeltas(ctx context.Context, st *store.Store, retentionSeconds int64, now int64) (int64, error) {
	cutoff := now - retentionSeconds
	var deleted int64
	err := st.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`DELETE FROM usage_deltas WHERE created_at < ?`, cutoff)
		if err != nil {
			return err
		}
		deleted, _ = result.RowsAffected()
		return nil
	})
	return deleted, err
}

// RollupHourly aggregates raw deltas into hourly rollups.
// Should be called periodically (e.g., at the top of each hour).
func RollupHourly(ctx context.Context, st *store.Store, now int64) error {
	return st.Write(ctx, func(tx *sql.Tx) error {
		// Aggregate deltas from the previous hour into rollups.
		// Hour boundaries are Unix epoch aligned: hour_start = (created_at / 3600) * 3600.
		_, err := tx.ExecContext(ctx, `
			INSERT INTO usage_rollups_hourly (subject_id, hour_start, uplink_bytes, downlink_bytes)
			SELECT
				subject_id,
				(created_at / 3600) * 3600 AS hour_start,
				SUM(uplink_bytes) AS uplink_bytes,
				SUM(downlink_bytes) AS downlink_bytes
			FROM usage_deltas
			WHERE created_at < ?
			GROUP BY subject_id, hour_start
			ON CONFLICT (subject_id, hour_start) DO UPDATE SET
				uplink_bytes = usage_rollups_hourly.uplink_bytes + excluded.uplink_bytes,
				downlink_bytes = usage_rollups_hourly.downlink_bytes + excluded.downlink_bytes`,
			now)
		return err
	})
}

// RollupDaily aggregates hourly rollups into daily rollups.
// Should be called periodically (e.g., at midnight).
func RollupDaily(ctx context.Context, st *store.Store, now int64) error {
	return st.Write(ctx, func(tx *sql.Tx) error {
		// Aggregate hourly rollups into daily rollups.
		// Day boundaries: day_start = (hour_start / 86400) * 86400.
		_, err := tx.ExecContext(ctx, `
			INSERT INTO usage_rollups_daily (subject_id, day_start, uplink_bytes, downlink_bytes)
			SELECT
				subject_id,
				(hour_start / 86400) * 86400 AS day_start,
				SUM(uplink_bytes) AS uplink_bytes,
				SUM(downlink_bytes) AS downlink_bytes
			FROM usage_rollups_hourly
			WHERE hour_start < ?
			GROUP BY subject_id, day_start
			ON CONFLICT (subject_id, day_start) DO UPDATE SET
				uplink_bytes = usage_rollups_daily.uplink_bytes + excluded.uplink_bytes,
				downlink_bytes = usage_rollups_daily.downlink_bytes + excluded.downlink_bytes`,
			now)
		return err
	})
}
