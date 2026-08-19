package nodes

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/amyrm/antimage/internal/panel/store"
)

// QuotaEnforcementSweeper finds subjects at or over quota and freezes them.
// SP3 design decision 4: quota is enforced panel-side by omission (freeze).
// SP3 invariant 12: only the quota sweeper may freeze for quota.
type QuotaEnforcementSweeper struct {
	Store *store.Store
	Log   *slog.Logger
	// CommitFunc is called for each node affected by a freeze to bump its
	// desired_revision and audit the change. It must be the same chokepoint
	// used for all document changes (CommitNodeChange).
	CommitFunc func(ctx context.Context, nodeID int64, actor, reason string) error
}

// Run finds subjects over quota and freezes them.
func (s *QuotaEnforcementSweeper) Run(ctx context.Context, now int64) error {
	var toFreeze []int64

	// Find subjects at or over quota, not yet frozen.
	rows, err := s.Store.Read().QueryContext(ctx, `
		SELECT id
		FROM subjects
		WHERE quota_bytes IS NOT NULL
		  AND quota_used_bytes >= quota_bytes
		  AND frozen_at IS NULL
		  AND enabled = 1`)
	if err != nil {
		return fmt.Errorf("query subjects over quota: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		toFreeze = append(toFreeze, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if len(toFreeze) == 0 {
		return nil
	}

	// Freeze each subject and commit the change.
	for _, subjectID := range toFreeze {
		if err := s.freezeSubject(ctx, subjectID, now); err != nil {
			s.Log.ErrorContext(ctx, "failed to freeze subject for quota",
				"subject_id", subjectID, "error", err)
			// Continue with remaining subjects rather than aborting.
			continue
		}
		s.Log.InfoContext(ctx, "subject frozen for quota", "subject_id", subjectID)
	}

	return nil
}

func (s *QuotaEnforcementSweeper) freezeSubject(ctx context.Context, subjectID, now int64) error {
	// Find all nodes serving this subject.
	var affectedNodes []int64
	err := s.Store.Write(ctx, func(tx *sql.Tx) error {
		// Freeze the subject (SP3 invariant 12: only the sweeper may freeze for quota).
		_, err := tx.ExecContext(ctx, `
			UPDATE subjects
			SET enabled = 0,
			    frozen_at = ?,
			    frozen_reason = 'quota_exceeded'
			WHERE id = ? AND enabled = 1`,
			now, subjectID)
		if err != nil {
			return fmt.Errorf("freeze subject: %w", err)
		}

		// Find nodes that have this subject in their desired document.
		// These nodes need their desired_revision bumped.
		rows, err := tx.QueryContext(ctx, `
			SELECT DISTINCT n.id
			FROM nodes n
			JOIN services s ON s.node_id = n.id
			WHERE s.enabled = 1`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()

		for rows.Next() {
			var nodeID int64
			if err := rows.Scan(&nodeID); err != nil {
				return err
			}
			affectedNodes = append(affectedNodes, nodeID)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}

	// Commit the change for each affected node through the chokepoint.
	// This bumps desired_revision and audits the freeze.
	for _, nodeID := range affectedNodes {
		reason := fmt.Sprintf("quota exceeded for subject %d", subjectID)
		if err := s.CommitFunc(ctx, nodeID, "system:quota-enforcer", reason); err != nil {
			return fmt.Errorf("commit node change for node %d: %w", nodeID, err)
		}
	}

	return nil
}

// QuotaResetSweeper finds subjects past their quota_reset_at and resets them.
// SP3 design decision 5: resets are an explicit timestamp, not a computed calendar.
type QuotaResetSweeper struct {
	Store *store.Store
	Log   *slog.Logger
	// CommitFunc is the chokepoint for document changes.
	CommitFunc func(ctx context.Context, nodeID int64, actor, reason string) error
}

// Run finds subjects past their reset time and resets them.
func (s *QuotaResetSweeper) Run(ctx context.Context, now int64) error {
	var toReset []struct {
		ID            int64
		ResetAt       int64
		QuotaBytes    sql.NullInt64
		FrozenReason  sql.NullString
	}

	// Find subjects past their reset time.
	rows, err := s.Store.Read().QueryContext(ctx, `
		SELECT id, quota_reset_at, quota_bytes, frozen_reason
		FROM subjects
		WHERE quota_reset_at IS NOT NULL
		  AND quota_reset_at <= ?`,
		now)
	if err != nil {
		return fmt.Errorf("query subjects for reset: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var r struct {
			ID            int64
			ResetAt       int64
			QuotaBytes    sql.NullInt64
			FrozenReason  sql.NullString
		}
		if err := rows.Scan(&r.ID, &r.ResetAt, &r.QuotaBytes, &r.FrozenReason); err != nil {
			return err
		}
		toReset = append(toReset, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if len(toReset) == 0 {
		return nil
	}

	// Reset each subject.
	for _, r := range toReset {
		if err := s.resetSubject(ctx, r.ID, r.ResetAt, r.QuotaBytes.Valid, r.FrozenReason, now); err != nil {
			s.Log.ErrorContext(ctx, "failed to reset subject quota",
				"subject_id", r.ID, "error", err)
			continue
		}
		s.Log.InfoContext(ctx, "subject quota reset", "subject_id", r.ID)
	}

	return nil
}

func (s *QuotaResetSweeper) resetSubject(
	ctx context.Context, subjectID, resetAt int64, hasQuota bool, frozenReason sql.NullString, now int64,
) error {
	var affectedNodes []int64
	err := s.Store.Write(ctx, func(tx *sql.Tx) error {
		// Determine the period: assume monthly (30 days) if quota is set.
		// This is a simple default; a production system might store the period.
		const defaultPeriodSeconds = 30 * 24 * 60 * 60
		nextReset := resetAt + defaultPeriodSeconds

		// Reset usage to zero and advance the reset timestamp.
		// If frozen for quota alone, unfreeze.
		shouldUnfreeze := frozenReason.Valid && frozenReason.String == "quota_exceeded"
		if shouldUnfreeze {
			_, err := tx.ExecContext(ctx, `
				UPDATE subjects
				SET quota_used_bytes = 0,
				    quota_reset_at = ?,
				    enabled = 1,
				    frozen_at = NULL,
				    frozen_reason = NULL
				WHERE id = ?`,
				nextReset, subjectID)
			if err != nil {
				return fmt.Errorf("reset and unfreeze subject: %w", err)
			}
		} else {
			_, err := tx.ExecContext(ctx, `
				UPDATE subjects
				SET quota_used_bytes = 0,
				    quota_reset_at = ?
				WHERE id = ?`,
				nextReset, subjectID)
			if err != nil {
				return fmt.Errorf("reset subject: %w", err)
			}
		}

		// Find affected nodes (only if we unfroze, otherwise no document change).
		if shouldUnfreeze {
			rows, err := tx.QueryContext(ctx, `
				SELECT DISTINCT n.id
				FROM nodes n
				JOIN services s ON s.node_id = n.id
				WHERE s.enabled = 1`)
			if err != nil {
				return err
			}
			defer func() { _ = rows.Close() }()

			for rows.Next() {
				var nodeID int64
				if err := rows.Scan(&nodeID); err != nil {
					return err
				}
				affectedNodes = append(affectedNodes, nodeID)
			}
			if err := rows.Err(); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return err
	}

	// Commit node changes if we unfroze the subject.
	for _, nodeID := range affectedNodes {
		reason := fmt.Sprintf("quota reset for subject %d", subjectID)
		if err := s.CommitFunc(ctx, nodeID, "system:quota-reset", reason); err != nil {
			return fmt.Errorf("commit node change for node %d: %w", nodeID, err)
		}
	}

	return nil
}
