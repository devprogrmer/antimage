package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"

	"github.com/amyrm/antimage/internal/panel/store/migrations"
)

// A Down that has never been executed is not a rollback path, it is a comment.
// These run the accounting migrations backwards against a database holding real
// rows, which is the only way to find out whether they work.

func openForMigrationTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("dialect: %v", err)
	}
	return st
}

// seedAccountingRows puts one node, two subjects and a multi-subject report in
// place, so Down has something to collapse rather than running over an empty
// table and passing vacuously.
func seedAccountingRows(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	err := st.Write(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, created_at) VALUES ('n', '127.0.0.1', 1)`)
		if err != nil {
			return err
		}
		nodeID, _ := r.LastInsertId()
		for i, name := range []string{"alice", "bob"} {
			r, err := tx.ExecContext(ctx,
				`INSERT INTO subjects (name, enabled, quota_used_bytes, created_at)
				 VALUES (?, 1, 0, 1)`, name)
			if err != nil {
				return err
			}
			subjectID, _ := r.LastInsertId()
			// Both under sequence 1: the shape the old constraint rejected and
			// the new one allows, so Down has to collapse them.
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO usage_deltas (node_id, subject_id, sequence,
				     uplink_bytes, downlink_bytes, created_at)
				 VALUES (?, ?, 1, ?, ?, 100)`,
				nodeID, subjectID, 10*(i+1), 20*(i+1)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// 00027 is the coefficient migration. Its Down must run cleanly and leave the
// accounting rows untouched -- it only ever added columns.
func TestCoefficientMigrationDownIsClean(t *testing.T) {
	st := openForMigrationTest(t)
	seedAccountingRows(t, st)

	var before int64
	if err := st.Read().QueryRow(
		`SELECT COALESCE(SUM(uplink_bytes + downlink_bytes), 0) FROM usage_deltas`,
	).Scan(&before); err != nil {
		t.Fatalf("read before: %v", err)
	}

	if err := goose.DownTo(st.write, ".", 26); err != nil {
		t.Fatalf("down to 26: %v", err)
	}

	// The columns are gone.
	var coef int64
	if err := st.Read().QueryRow(`SELECT usage_coefficient FROM nodes LIMIT 1`).Scan(&coef); err == nil {
		t.Error("nodes.usage_coefficient survived the Down")
	}

	// The bytes are not.
	var after int64
	if err := st.Read().QueryRow(
		`SELECT COALESCE(SUM(uplink_bytes + downlink_bytes), 0) FROM usage_deltas`,
	).Scan(&after); err != nil {
		t.Fatalf("read after: %v", err)
	}
	if after != before {
		t.Errorf("usage bytes = %d after Down, want %d: a column-only migration "+
			"moved a billed figure", after, before)
	}

	// And forward again, so the pair is a round trip rather than a one-way door.
	if err := goose.Up(st.write, "."); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if err := st.Read().QueryRow(`SELECT usage_coefficient FROM nodes LIMIT 1`).Scan(&coef); err != nil {
		t.Fatalf("coefficient column did not come back: %v", err)
	}
	if coef != 10000 {
		t.Errorf("usage_coefficient = %d after re-migrating, want 10000", coef)
	}
}

// 00026 changes the uniqueness grain, so its Down cannot be lossless: the old
// constraint admits one row per (node_id, sequence) and the fix writes several.
// What it MUST do is say so by collapsing rather than failing, and preserve the
// byte totals while it does -- losing the attribution is unavoidable, losing
// the traffic is not.
func TestIngestFixDownCollapsesRatherThanFailing(t *testing.T) {
	st := openForMigrationTest(t)
	seedAccountingRows(t, st)

	var beforeRows int64
	var beforeBytes int64
	if err := st.Read().QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(uplink_bytes + downlink_bytes), 0) FROM usage_deltas`,
	).Scan(&beforeRows, &beforeBytes); err != nil {
		t.Fatalf("read before: %v", err)
	}
	if beforeRows != 2 {
		t.Fatalf("seeded %d rows, want 2 under one sequence", beforeRows)
	}

	if err := goose.DownTo(st.write, ".", 25); err != nil {
		t.Fatalf("down to 25: %v", err)
	}

	var afterRows, afterBytes int64
	if err := st.Read().QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(uplink_bytes + downlink_bytes), 0) FROM usage_deltas`,
	).Scan(&afterRows, &afterBytes); err != nil {
		t.Fatalf("read after: %v", err)
	}

	// Attribution is lost: two subjects collapse to one row.
	if afterRows != 1 {
		t.Errorf("rows = %d after Down, want 1: the old constraint admits one "+
			"row per (node_id, sequence)", afterRows)
	}
	// Traffic is not.
	if afterBytes != beforeBytes {
		t.Errorf("bytes = %d after Down, want %d: collapsing rows must sum them, "+
			"not discard them", afterBytes, beforeBytes)
	}

	// The watermark table is gone with it.
	var n int64
	if err := st.Read().QueryRow(
		`SELECT COUNT(*) FROM usage_rollup_state`).Scan(&n); err == nil {
		t.Error("usage_rollup_state survived the Down")
	}

	// Forward again must restore the fixed constraint, so a multi-subject
	// report is accepted after a round trip.
	if err := goose.Up(st.write, "."); err != nil {
		t.Fatalf("up again: %v", err)
	}
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		for _, subject := range []int64{1, 2} {
			if _, err := tx.ExecContext(context.Background(),
				`INSERT INTO usage_deltas (node_id, subject_id, sequence,
				     uplink_bytes, downlink_bytes, created_at)
				 VALUES (1, ?, 77, 1, 1, 200)`, subject); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Errorf("multi-subject insert rejected after a Down/Up round trip: %v", err)
	}
}
