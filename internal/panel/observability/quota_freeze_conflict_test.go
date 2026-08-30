package observability

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/store"
)

// Exactly one sweeper may freeze a subject for quota, and it is not this one.
//
// This package ran its own enforceQuotaFreeze, which stamped frozen_at while
// leaving enabled = 1 and committed no node change -- so the subject stayed in
// the desired document and kept being served. Worse, findSubjectsOverQuota
// selects `AND s.frozen_at IS NULL`, so that stamp excluded the subject from
// nodes.QuotaEnforcementSweeper permanently. The partial freeze was not a step
// towards the real one; it prevented it.
//
// The race was not close either: Run() sweeps immediately on start while the
// quota enforcer waits a full five minutes for its first tick, so every
// subject already over quota at panel start lost it.

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// testNow is past the seeded rollup hour, so the billable window covers it.
func testNow() time.Time {
	return time.Unix(7200, 0).UTC()
}

// overQuotaSubject creates a node and one subject already past a quota of 100
// bytes, with the usage recorded as a folded rollup so the billable enforcer
// can see it.
func overQuotaSubject(t *testing.T, st *store.Store) (nodeID, subjectID int64) {
	t.Helper()
	ctx := context.Background()
	err := st.Write(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, created_at) VALUES ('n1', '127.0.0.1', 1000)`)
		if err != nil {
			return err
		}
		nodeID, _ = r.LastInsertId()

		// The enforcer republishes to every node carrying an enabled service,
		// so without one there is nothing for it to commit to.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO services (node_id, adapter_kind, enabled, params, created_at)
			 VALUES (?, 'xray', 1, '{}', 1000)`, nodeID); err != nil {
			return err
		}

		r, err = tx.ExecContext(ctx,
			`INSERT INTO subjects (name, enabled, quota_bytes, quota_used_bytes,
			                       quota_period_start, created_at)
			 VALUES ('overspender', 1, 100, 500, 0, 1000)`)
		if err != nil {
			return err
		}
		subjectID, _ = r.LastInsertId()

		_, err = tx.ExecContext(ctx,
			`INSERT INTO usage_rollups_hourly
			   (subject_id, node_id, service_id, hour_start, uplink_bytes, downlink_bytes)
			 VALUES (?, ?, NULL, 0, 500, 0)`, subjectID, nodeID)
		return err
	})
	if err != nil {
		t.Fatalf("seed over-quota subject: %v", err)
	}
	return nodeID, subjectID
}

func subjectState(t *testing.T, st *store.Store, id int64) (enabled int, frozen sql.NullInt64) {
	t.Helper()
	err := st.Read().QueryRowContext(context.Background(),
		`SELECT enabled, frozen_at FROM subjects WHERE id = ?`, id).Scan(&enabled, &frozen)
	if err != nil {
		t.Fatalf("read subject state: %v", err)
	}
	return enabled, frozen
}

// The guard: an observability sweep must leave the freeze decision alone.
//
// Reintroducing an enforceQuotaFreeze call in sweep() makes this fail, which is
// the point -- a frozen_at stamp written here is invisible to the enforcer that
// actually cuts service.
func TestTheObservabilitySweepDoesNotFreezeForQuota(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	_, subjectID := overQuotaSubject(t, st)

	NewSweeper(st).sweep(ctx)

	_, frozen := subjectState(t, st, subjectID)
	if frozen.Valid {
		t.Errorf("the observability sweep froze subject %d (frozen_at=%d). Freezing "+
			"belongs to nodes.QuotaEnforcementSweeper, which also sets enabled = 0 "+
			"and commits a node change; a frozen_at written here excludes the "+
			"subject from findSubjectsOverQuota and strands it in service",
			subjectID, frozen.Int64)
	}
}

// And the enforcer that is allowed to freeze still reaches the subject and
// completes the job: disabled, and every serving node told about it.
func TestTheQuotaEnforcerStillCutsServiceAfterASweep(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	_, subjectID := overQuotaSubject(t, st)

	// Observability runs first, as it does on every panel start.
	NewSweeper(st).sweep(ctx)

	var committed []int64
	enforcer := &nodes.QuotaEnforcementSweeper{
		Store: st,
		Log:   discardLog(),
		CommitFunc: func(_ context.Context, nodeID int64, _, _ string) error {
			committed = append(committed, nodeID)
			return nil
		},
	}
	if err := enforcer.Run(ctx, testNow().Unix()); err != nil {
		t.Fatalf("quota enforcer Run: %v", err)
	}

	enabled, frozen := subjectState(t, st, subjectID)
	if enabled != 0 {
		t.Errorf("subject %d is over quota and still enabled after the enforcer ran",
			subjectID)
	}
	if !frozen.Valid {
		t.Errorf("subject %d was not marked frozen", subjectID)
	}
	if len(committed) == 0 {
		t.Error("no node change was committed, so no agent was told to stop " +
			"serving the subject")
	}
}
