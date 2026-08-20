package subjects

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/store"
)

// Sweeper enforces expiry.
//
// Expiry is enforced by omission from the desired document (SP2 decision 2):
// the document builder already excludes an expired subject, so a node that
// reconciles after the expiry time stops serving them without anything else
// happening. The sweeper exists for the other half of the job — making the
// change VISIBLE and PROMPT:
//
//   - it stamps expired_at, so an operator can tell "expired last Tuesday"
//     from "never had an expiry";
//   - it flips enabled to 0, so the subject stops being served even if the
//     clock is later moved backwards;
//   - it bumps the revision of every affected node through CommitNodeChange,
//     which is what wakes the agent instead of leaving the removal to whenever
//     the next unrelated change happens to occur.
//
// Without it, an expired subject would linger on a node until something else
// bumped that node's revision, which could be days.
type Sweeper struct {
	db     *store.Store
	now    func() time.Time
	opts   []nodes.SnapshotOption
	notify func(nodeID, revision int64)
}

// NewSweeper returns a sweeper. opts must carry the unsealer, or rebuilding a
// document for a node that has subjects will fail — deliberately, since the
// alternative is publishing one that omits every subject.
func NewSweeper(
	db *store.Store, now func() time.Time, notify func(nodeID, revision int64),
	opts ...nodes.SnapshotOption,
) *Sweeper {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if notify == nil {
		notify = func(int64, int64) {}
	}
	return &Sweeper{db: db, now: now, opts: opts, notify: notify}
}

// Sweep expires everything due and returns how many subjects it retired.
func (s *Sweeper) Sweep(ctx context.Context) (int, error) {
	now := s.now().UTC()

	due, err := s.dueSubjects(ctx, now)
	if err != nil {
		return 0, err
	}
	if len(due) == 0 {
		return 0, nil
	}

	// The node set is captured BEFORE the subject is disabled, because the
	// query that finds it joins through grants that remain but would then
	// describe a subject nobody serves.
	affected := map[int64]struct{}{}
	for _, id := range due {
		ids, err := s.NodeIDsForRead(ctx, id)
		if err != nil {
			return 0, err
		}
		for _, nodeID := range ids {
			affected[nodeID] = struct{}{}
		}
	}

	// One transaction retires every due subject: a partial sweep that expired
	// half of them and then failed would leave the rest live until the next
	// tick, with no record of why.
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		for _, id := range due {
			if _, err := tx.ExecContext(ctx,
				`UPDATE subjects SET enabled = 0, expired_at = ? WHERE id = ?`,
				now.Unix(), id); err != nil {
				return fmt.Errorf("expire subject %d: %w", id, err)
			}
			// audit.InTx, never BestEffort: this call already holds the store's
			// single write connection, and BestEffort would block on it until
			// its own timeout and drop the record.
			if err := audit.InTx(ctx, tx, "", audit.SystemActor("expiry-sweeper"), audit.Record{
				Action:     "subject.expired",
				TargetType: "subject",
				TargetID:   sql.NullInt64{Int64: id, Valid: true},
				After:      map[string]any{"reason": "expires_at reached"},
				Result:     "ok",
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	// Republish AFTER the subjects are retired, so the rebuilt document
	// already excludes them.
	for nodeID := range affected {
		result, err := nodes.CommitNodeChange(ctx, s.db, nodeID,
			audit.SystemActor("expiry-sweeper"), "", "subject expired",
			func(*sql.Tx) error { return nil }, s.opts...)
		if err != nil {
			// One node failing must not strand the others; the subjects are
			// already disabled, so the panel is correct even if this node is
			// briefly stale.
			slog.ErrorContext(ctx, "expiry sweep could not republish a node",
				"node_id", nodeID, "error", err)
			continue
		}
		if result.Changed {
			s.notify(nodeID, result.Revision)
		}
	}

	return len(due), nil
}

// dueSubjects finds subjects past their expiry that are still enabled.
//
// The enabled check is what makes the sweep idempotent: once retired, a
// subject is no longer due, so a sweep that runs every 30 seconds does not
// re-expire and re-audit the same person forever.
func (s *Sweeper) dueSubjects(ctx context.Context, now time.Time) ([]int64, error) {
	rows, err := s.db.Read().QueryContext(ctx,
		`SELECT id FROM subjects
		  WHERE enabled = 1 AND expires_at IS NOT NULL AND expires_at <= ?
		  ORDER BY id`, now.Unix())
	if err != nil {
		return nil, fmt.Errorf("find expired subjects: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan expired subject: %w", err)
		}
		ids = append(ids, id)
	}
	// Without this a mid-iteration failure silently expires only some of them.
	return ids, rows.Err()
}

// NodeIDsForRead mirrors Store.NodeIDsForRead without requiring a Store, so
// the sweeper needs no secret box to find which nodes to republish.
func (s *Sweeper) NodeIDsForRead(ctx context.Context, subjectID int64) ([]int64, error) {
	rows, err := s.db.Read().QueryContext(ctx,
		`SELECT DISTINCT sv.node_id
		   FROM subject_services ss
		   JOIN services sv ON sv.id = ss.service_id
		  WHERE ss.subject_id = ?
		  ORDER BY sv.node_id`, subjectID)
	if err != nil {
		return nil, fmt.Errorf("find nodes for subject %d: %w", subjectID, err)
	}
	defer func() { _ = rows.Close() }()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Run sweeps on a ticker until ctx is cancelled.
func (s *Sweeper) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := s.Sweep(ctx)
			if err != nil {
				slog.ErrorContext(ctx, "expiry sweep failed", "error", err)
				continue
			}
			if n > 0 {
				slog.InfoContext(ctx, "expired subjects", "count", n)
			}
		}
	}
}
