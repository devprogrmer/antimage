package nodes

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/amyrm/antimage/internal/panel/store"
)

// DefaultQuotaPeriodSeconds is the period length assumed for a subject that
// has none recorded: thirty days, which is exactly what the reset sweeper
// hardcoded before C4 stored it per subject.
//
// Kept so that a subject created before the column existed, or by a caller that
// does not set it, behaves as it always did rather than as an undefined case.
const DefaultQuotaPeriodSeconds = 30 * 24 * 60 * 60

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
//
// Enforcement is on BILLABLE bytes for the current period (C4). The predicate
// used to be quota_used_bytes >= quota_bytes, which reads a stored counter of
// RAW bytes -- so an operator who priced a node at x2.0 changed what a customer
// was charged and not when they were cut off. A coefficient that moves the bill
// but not the enforcement is decorative, which is what AD-2's "quota should be
// what the operator sold" rules out.
//
// Note the behaviour this implies, and it belongs in the release notes:
// changing a coefficient retroactively changes when an existing subject hits
// quota, because the whole period is revalued at the coefficients in force.
// That is the correct reading -- the operator changed the price -- but it will
// surprise someone.
func (s *QuotaEnforcementSweeper) Run(ctx context.Context, now int64) error {
	toFreeze, err := findSubjectsOverQuota(ctx, s.Store, now)
	if err != nil {
		return fmt.Errorf("find subjects over quota: %w", err)
	}
	if len(toFreeze) == 0 {
		return nil
	}

	// Freeze each subject and commit the change.
	for _, sub := range toFreeze {
		if err := s.freezeSubject(ctx, sub.SubjectID, now); err != nil {
			s.Log.ErrorContext(ctx, "failed to freeze subject for quota",
				"subject_id", sub.SubjectID, "error", err)
			// Continue with remaining subjects rather than aborting.
			continue
		}
		s.Log.InfoContext(ctx, "subject frozen for quota",
			"subject_id", sub.SubjectID,
			"billable_bytes", sub.Billable, "quota_bytes", sub.Quota)
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
		ID           int64
		ResetAt      int64
		QuotaBytes   sql.NullInt64
		FrozenReason sql.NullString
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
			ID           int64
			ResetAt      int64
			QuotaBytes   sql.NullInt64
			FrozenReason sql.NullString
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
		// The period length is recorded per subject since C4: a weekly plan and
		// a monthly plan are different products, and the constant this replaces
		// could not tell them apart. DefaultQuotaPeriodSeconds is what that
		// constant was, kept as the fallback for a subject whose period was
		// never set so the behaviour is unchanged rather than undefined.
		periodSeconds := int64(DefaultQuotaPeriodSeconds)
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(quota_period_seconds, ?) FROM subjects WHERE id = ?`,
			DefaultQuotaPeriodSeconds, subjectID).Scan(&periodSeconds); err != nil {
			return fmt.Errorf("read quota period: %w", err)
		}
		nextReset := resetAt + periodSeconds

		// The period that is starting begins where the old one ended. Recording
		// it is what lets enforcement sum billable over "this period" at all;
		// without it the window has no lower bound and the sweeper would be
		// comparing a quota against a subject's entire history.
		if _, err := tx.ExecContext(ctx,
			`UPDATE subjects SET quota_period_start = ? WHERE id = ?`,
			resetAt, subjectID); err != nil {
			return fmt.Errorf("advance quota period start: %w", err)
		}

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
