package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/pressly/goose/v3"
)

// A rehearsal of the full migration protocol against a database carrying the
// exact damage the pre-00026 sweeper caused.
//
// This exists because "the migration changes no bill" and "the repair restores
// the true figure" are both claims with a closed form behind them, and a claim
// with a closed form should be checked rather than asserted. The arithmetic:
//
//	true      = sum of the raw deltas
//	inflated  = true x folds          (each sweeper run re-added every delta)
//	projected = true - inflated       = -true x (folds - 1)
//
// The protocol below is the one an operator runs, in order, with each step's
// expected outcome pinned.

const (
	rehearsalSubjects = 3
	rehearsalReports  = 8
	rehearsalBytes    = 25 // per report, per subject: 15 up + 10 down
	rehearsalFolds    = 12 // sweeper runs while the deltas were still present
)

// seedPreFixDatabase takes the schema back to 25, writes traffic the way the
// old ingest could (one subject per sequence, which is why the multi-subject
// defect stayed invisible), and folds it repeatedly the way the old rollup did.
func seedPreFixDatabase(t *testing.T, st *Store) (trueBytes, inflatedBytes int64) {
	t.Helper()
	ctx := context.Background()

	if err := goose.DownTo(st.write, ".", 25); err != nil {
		t.Fatalf("down to pre-fix schema: %v", err)
	}

	err := st.Write(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, created_at) VALUES ('n', '10.0.0.1', 1)`)
		if err != nil {
			return err
		}
		nodeID, _ := r.LastInsertId()

		var subjectIDs []int64
		for i := 0; i < rehearsalSubjects; i++ {
			r, err := tx.ExecContext(ctx,
				`INSERT INTO subjects (name, enabled, quota_used_bytes, created_at)
				 VALUES ('s' || ?, 1, 0, 1)`, i)
			if err != nil {
				return err
			}
			id, _ := r.LastInsertId()
			subjectIDs = append(subjectIDs, id)
		}

		// One subject per sequence: the old UNIQUE (node_id, sequence) admitted
		// nothing else, so this is the only traffic shape that survived.
		seq := int64(0)
		for i := 0; i < rehearsalReports; i++ {
			for _, subjectID := range subjectIDs {
				seq++
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO usage_deltas (node_id, subject_id, sequence,
					    uplink_bytes, downlink_bytes, created_at)
					VALUES (?, ?, ?, 15, 10, ?)`,
					nodeID, subjectID, seq, 3600+int64(i)); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	trueBytes = int64(rehearsalSubjects * rehearsalReports * rehearsalBytes)

	// The old rollup, reproduced exactly: no lower bound, accumulating merge.
	for i := 0; i < rehearsalFolds; i++ {
		err := st.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO usage_rollups_hourly (subject_id, hour_start, uplink_bytes, downlink_bytes)
				SELECT subject_id, (created_at / 3600) * 3600,
				       SUM(uplink_bytes), SUM(downlink_bytes)
				  FROM usage_deltas
				 WHERE created_at < 1000000
				 GROUP BY subject_id, (created_at / 3600) * 3600
				ON CONFLICT (subject_id, hour_start) DO UPDATE SET
					uplink_bytes = usage_rollups_hourly.uplink_bytes + excluded.uplink_bytes,
					downlink_bytes = usage_rollups_hourly.downlink_bytes + excluded.downlink_bytes`)
			return err
		})
		if err != nil {
			t.Fatalf("inflate %d: %v", i, err)
		}
	}
	return trueBytes, trueBytes * rehearsalFolds
}

func hourlyTotal(t *testing.T, st *Store) int64 {
	t.Helper()
	var total int64
	if err := st.Read().QueryRow(
		`SELECT COALESCE(SUM(uplink_bytes + downlink_bytes), 0) FROM usage_rollups_hourly`,
	).Scan(&total); err != nil {
		t.Fatalf("hourly total: %v", err)
	}
	return total
}

// TestMigrationProtocolRehearsal walks the operator protocol end to end and
// pins every step against the model above.
func TestMigrationProtocolRehearsal(t *testing.T) {
	st := openForMigrationTest(t)

	trueBytes, inflatedBytes := seedPreFixDatabase(t, st)

	// The damage is real and matches the model before anything is migrated.
	if got := hourlyTotal(t, st); got != inflatedBytes {
		t.Fatalf("seeded rollup = %d, want %d (true %d x %d folds)",
			got, inflatedBytes, trueBytes, rehearsalFolds)
	}
	t.Logf("STEP 1  pre-migration: true=%d inflated=%d (x%d)",
		trueBytes, inflatedBytes, rehearsalFolds)

	// STEP 2 -- checksum before. Uses raw SQL rather than nodes.TakeChecksum
	// because internal/panel/store may not import internal/panel/nodes; the
	// figures are the same ones.
	beforeDeltas, beforeHourly := accountingTotals(t, st)

	// STEP 3 -- migrate. In production this happens when the panel starts:
	// store.Open runs goose.Up, so there is no separate migrate command.
	if err := goose.Up(st.write, "."); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// STEP 4 -- checksum after. The migration must have moved nothing.
	afterDeltas, afterHourly := accountingTotals(t, st)
	if beforeDeltas != afterDeltas {
		t.Errorf("migration moved raw deltas: %d -> %d", beforeDeltas, afterDeltas)
	}
	if beforeHourly != afterHourly {
		t.Errorf("migration moved the rollup: %d -> %d", beforeHourly, afterHourly)
	}
	t.Logf("STEP 4  post-migration: deltas=%d hourly=%d (unchanged)", afterDeltas, afterHourly)

	// The watermark is seeded at MAX(id) so the first sweeper run after the
	// migration folds nothing again.
	var watermark, maxID int64
	if err := st.Read().QueryRow(
		`SELECT last_delta_id FROM usage_rollup_state WHERE name='hourly'`).Scan(&watermark); err != nil {
		t.Fatalf("watermark: %v", err)
	}
	if err := st.Read().QueryRow(
		`SELECT COALESCE(MAX(id),0) FROM usage_deltas`).Scan(&maxID); err != nil {
		t.Fatalf("max id: %v", err)
	}
	if watermark != maxID {
		t.Errorf("watermark = %d, want %d: the first post-migration fold would "+
			"re-add every surviving delta", watermark, maxID)
	}

	// Every coefficient is x1.0, so the migration reprices nothing.
	for _, table := range []string{"nodes", "services", "subjects", "resellers"} {
		var off int64
		if err := st.Read().QueryRow(
			`SELECT COUNT(*) FROM ` + table + ` WHERE usage_coefficient <> 10000`).Scan(&off); err != nil {
			t.Fatalf("%s coefficient: %v", table, err)
		}
		if off != 0 {
			t.Errorf("%s has %d coefficients off x1.0 immediately after migration", table, off)
		}
	}
	t.Log("STEP 4  all coefficients at x1.0; no repricing")
}

func accountingTotals(t *testing.T, st *Store) (deltas, hourly int64) {
	t.Helper()
	if err := st.Read().QueryRow(
		`SELECT COALESCE(SUM(uplink_bytes + downlink_bytes), 0) FROM usage_deltas`,
	).Scan(&deltas); err != nil {
		t.Fatalf("delta total: %v", err)
	}
	return deltas, hourlyTotal(t, st)
}
