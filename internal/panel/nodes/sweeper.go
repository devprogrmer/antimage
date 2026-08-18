package nodes

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/store"
)

// OfflineAfter is three missed 30-second heartbeats.
const OfflineAfter = 90 * time.Second

// Sweeper flips nodes to offline when heartbeats stop.
//
// Only 'online' and 'degraded' are swept. 'disabled' is an administrative
// decision, 'pending' and 'enrolling' have never reported, and 'integrity' is
// a fault an operator must see — relabelling any of them would erase
// information rather than add it.
type Sweeper struct {
	store *store.Store
	now   func() time.Time
	// after is how long a node may go unheard before it is swept. Zero means
	// OfflineAfter, which is what production runs; it is settable only so the
	// acceptance suite can exercise the real code path without a 90-second
	// wall-clock wait, the same reason httpapi.Deps.SSEInterval exists.
	after time.Duration
}

func NewSweeper(s *store.Store, now func() time.Time) *Sweeper {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Sweeper{store: s, now: now}
}

// WithThreshold returns a sweeper that marks nodes offline after d instead of
// the default OfflineAfter. A non-positive d selects the default.
func (s *Sweeper) WithThreshold(d time.Duration) *Sweeper {
	s.after = d
	return s
}

func (s *Sweeper) threshold() time.Duration {
	if s.after <= 0 {
		return OfflineAfter
	}
	return s.after
}

// Sweep marks every stale node offline and returns how many it moved.
//
// The audit row is written with audit.InTx inside the same transaction as the
// status update, so a rolled-back sweep leaves no claim that a node went
// offline. That is also why this function must never reach for
// audit.BestEffort: it already holds the store's single write connection.
func (s *Sweeper) Sweep(ctx context.Context) (int, error) {
	cutoff := s.now().Add(-s.threshold()).Unix()
	var marked int

	err := s.store.Write(ctx, func(tx *sql.Tx) error {
		stale, err := staleNodeIDs(ctx, tx, cutoff)
		if err != nil {
			return err
		}
		for _, id := range stale {
			if _, err := tx.ExecContext(ctx,
				`UPDATE nodes SET status = 'offline' WHERE id = ?`, id); err != nil {
				return fmt.Errorf("mark node %d offline: %w", id, err)
			}
			if err := audit.InTx(ctx, tx, "", audit.SystemActor("sweeper"), audit.Record{
				Action:     "node.offline",
				TargetType: "node",
				TargetID:   sql.NullInt64{Int64: id, Valid: true},
				After:      map[string]any{"reason": "no heartbeat within 3 intervals"},
				Result:     "ok",
			}); err != nil {
				return err
			}
			marked++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return marked, nil
}

// staleNodeIDs collects the ids first rather than updating mid-iteration:
// writing to the same table a cursor is still walking is how a sweep silently
// skips rows.
func staleNodeIDs(ctx context.Context, tx *sql.Tx, cutoff int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM nodes
		  WHERE status IN ('online','degraded')
		    AND (last_seen_at IS NULL OR last_seen_at < ?)`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("find stale nodes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var stale []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan stale node: %w", err)
		}
		stale = append(stale, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stale nodes: %w", err)
	}
	return stale, nil
}

// Run sweeps on a ticker until ctx is cancelled.
func (s *Sweeper) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = s.Sweep(ctx)
		}
	}
}
