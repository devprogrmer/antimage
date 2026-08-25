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

// RollupHourly folds raw deltas into hourly buckets. Safe to re-run.
//
// Each delta is folded EXACTLY ONCE, tracked by a watermark: the highest delta
// id already folded. Before the watermark existed this selected
// `WHERE created_at < now` with no lower bound and merged with `x = x + new`,
// so every run re-folded every unpruned delta. The sweeper runs this hourly and
// deltas are kept for seven days, so a delta was folded on the order of 168
// times and a customer's recorded traffic grew every hour for traffic that
// happened once.
//
// The watermark is on id rather than created_at because id is assigned by the
// database and is strictly monotonic, while created_at is supplied by the
// caller. A cutoff on created_at can step over a row permanently: if a delta
// carries a timestamp at or after the cutoff while a later-inserted row carries
// an earlier one, advancing past the second strands the first forever.
//
// `now` is retained for call compatibility and is deliberately unused. A
// deadline is no longer needed for correctness: folding a partially elapsed hour
// is harmless because each delta lands in the bucket its own timestamp names and
// buckets accumulate, so a later run adds the rest of that hour rather than
// recomputing it.
func RollupHourly(ctx context.Context, st *store.Store, _ int64) error {
	return st.Write(ctx, func(tx *sql.Tx) error {
		var watermark int64
		err := tx.QueryRowContext(ctx,
			`SELECT last_delta_id FROM usage_rollup_state WHERE name = 'hourly'`,
		).Scan(&watermark)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("read rollup watermark: %w", err)
		}

		// The ceiling is captured before the fold and written after it, inside
		// the same transaction, so a delta inserted mid-fold is picked up by the
		// next run rather than being skipped by a watermark that ran ahead of
		// the rows it claims to cover.
		var ceiling int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(id), ?) FROM usage_deltas WHERE id > ?`,
			watermark, watermark).Scan(&ceiling); err != nil {
			return fmt.Errorf("read delta ceiling: %w", err)
		}
		if ceiling <= watermark {
			return nil // nothing new to fold
		}

		// Hour boundaries are Unix epoch aligned: (created_at / 3600) * 3600.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO usage_rollups_hourly (subject_id, hour_start, uplink_bytes, downlink_bytes)
			SELECT
				subject_id,
				(created_at / 3600) * 3600 AS hour_start,
				SUM(uplink_bytes) AS uplink_bytes,
				SUM(downlink_bytes) AS downlink_bytes
			FROM usage_deltas
			WHERE id > ? AND id <= ?
			GROUP BY subject_id, hour_start
			ON CONFLICT (subject_id, hour_start) DO UPDATE SET
				uplink_bytes = usage_rollups_hourly.uplink_bytes + excluded.uplink_bytes,
				downlink_bytes = usage_rollups_hourly.downlink_bytes + excluded.downlink_bytes`,
			watermark, ceiling); err != nil {
			return fmt.Errorf("fold deltas into hourly rollups: %w", err)
		}

		if _, err := tx.ExecContext(ctx, `
			INSERT INTO usage_rollup_state (name, last_delta_id) VALUES ('hourly', ?)
			ON CONFLICT (name) DO UPDATE SET last_delta_id = excluded.last_delta_id`,
			ceiling); err != nil {
			return fmt.Errorf("advance rollup watermark: %w", err)
		}
		return nil
	})
}

// RollupDaily recomputes daily buckets from hourly ones. Safe to re-run.
//
// This RECOMPUTES rather than accumulates, which is the difference between the
// two rollups and follows from what they read. usage_deltas is a log, so folding
// it must add and must therefore track what has been folded. usage_rollups_hourly
// is a complete aggregate -- each row already holds the whole total for its
// (subject, hour) -- so the day's figure is a pure function of the hours in it,
// and the correct merge is to overwrite. Accumulating from a complete aggregate
// was the daily half of the same inflation bug.
//
// Hourly rows are never pruned (migration 00011: "kept indefinitely for billing
// history"), so the recomputation always sees every hour it needs. If that ever
// changes, this becomes lossy and must gain a watermark of its own.
func RollupDaily(ctx context.Context, st *store.Store, now int64) error {
	return st.Write(ctx, func(tx *sql.Tx) error {
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
				uplink_bytes = excluded.uplink_bytes,
				downlink_bytes = excluded.downlink_bytes`,
			now)
		return err
	})
}
