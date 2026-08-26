package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/pressly/goose/v3"
)

// 00029 rebuilds usage_deltas to widen the uniqueness grain, and a table
// rebuild is where an AUTOINCREMENT watermark goes missing.
//
// SQLite keeps the high-water mark in sqlite_sequence, keyed by table name. A
// rebuild sets it from the rows copied -- so when the source table is EMPTY it
// is not set at all and ids restart at 1. That is not an exotic case: the
// rollup watermark is an id, and PruneUsageDeltas empties this table for any
// node quiet longer than the seven-day retention window. Restarting beneath the
// watermark would strand every future delta from the rollups, permanently and
// silently, which is the failure 00026 added AUTOINCREMENT to prevent.
//
// The tests below are what stop 00029 from reintroducing it. They fail if the
// stash-and-restore around the rebuild is removed.

// seqOf reads the AUTOINCREMENT high-water mark for a table.
func seqOf(t *testing.T, st *Store, table string) int64 {
	t.Helper()
	var seq sql.NullInt64
	err := st.Read().QueryRow(
		`SELECT seq FROM sqlite_sequence WHERE name = ?`, table).Scan(&seq)
	if err == sql.ErrNoRows {
		return 0
	}
	if err != nil {
		t.Fatalf("read sqlite_sequence: %v", err)
	}
	return seq.Int64
}

// nextDeltaID inserts one delta and returns the id SQLite chose.
func nextDeltaID(t *testing.T, st *Store, nodeID, subjectID, sequence int64) int64 {
	t.Helper()
	var id int64
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		r, err := tx.Exec(
			`INSERT INTO usage_deltas (node_id, subject_id, sequence,
			                           uplink_bytes, downlink_bytes, created_at)
			 VALUES (?, ?, ?, 1, 1, 1000)`, nodeID, subjectID, sequence)
		if err != nil {
			return err
		}
		id, _ = r.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("insert delta: %v", err)
	}
	return id
}

// THE case: the table is emptied before the migration runs, exactly as
// PruneUsageDeltas leaves it.
func TestGrainMigrationKeepsTheWatermarkOverAnEmptyTable(t *testing.T) {
	st := openForMigrationTest(t)
	ctx := context.Background()

	// Go back to before 00029, then build up a watermark and prune it away.
	if err := goose.DownTo(st.write, ".", 28); err != nil {
		t.Fatalf("down to 28: %v", err)
	}
	seedAccountingRows(t, st)

	var nodeID, subjectID int64
	if err := st.Read().QueryRow(`SELECT id FROM nodes LIMIT 1`).Scan(&nodeID); err != nil {
		t.Fatalf("node: %v", err)
	}
	if err := st.Read().QueryRow(`SELECT id FROM subjects LIMIT 1`).Scan(&subjectID); err != nil {
		t.Fatalf("subject: %v", err)
	}
	for i := int64(100); i < 110; i++ {
		nextDeltaID(t, st, nodeID, subjectID, i)
	}
	before := seqOf(t, st, "usage_deltas")
	if before == 0 {
		t.Fatal("no watermark was established, so this test would prove nothing")
	}

	// Prune to empty, the way the sweeper does for a quiet node.
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM usage_deltas`)
		return err
	}); err != nil {
		t.Fatalf("prune: %v", err)
	}

	if err := goose.Up(st.write, "."); err != nil {
		t.Fatalf("up: %v", err)
	}

	after := seqOf(t, st, "usage_deltas")
	if after < before {
		t.Fatalf("the watermark fell from %d to %d across the rebuild; ids now "+
			"restart beneath the rollup watermark and every future delta would "+
			"be stranded", before, after)
	}
	// And the next id actually allocated is above it.
	if got := nextDeltaID(t, st, nodeID, subjectID, 200); got <= before {
		t.Errorf("the first id after migrating is %d, at or below the pre-migration "+
			"watermark %d", got, before)
	}
}

// The same property with rows present, where the copy sets the sequence itself.
// This one would pass without the stash, so it is here to pin the ordinary case
// rather than to catch the bug.
func TestGrainMigrationKeepsIDsWhenRowsArePresent(t *testing.T) {
	st := openForMigrationTest(t)

	if err := goose.DownTo(st.write, ".", 28); err != nil {
		t.Fatalf("down to 28: %v", err)
	}
	seedAccountingRows(t, st)

	var wantIDs []int64
	rows, err := st.Read().Query(`SELECT id FROM usage_deltas ORDER BY id`)
	if err != nil {
		t.Fatalf("read ids: %v", err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		wantIDs = append(wantIDs, id)
	}
	_ = rows.Close()
	if len(wantIDs) == 0 {
		t.Fatal("no deltas seeded, so this would pass vacuously")
	}

	if err := goose.Up(st.write, "."); err != nil {
		t.Fatalf("up: %v", err)
	}

	var gotIDs []int64
	rows, err = st.Read().Query(`SELECT id FROM usage_deltas ORDER BY id`)
	if err != nil {
		t.Fatalf("read ids after: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		gotIDs = append(gotIDs, id)
	}

	if len(gotIDs) != len(wantIDs) {
		t.Fatalf("kept %d deltas, want %d", len(gotIDs), len(wantIDs))
	}
	for i := range wantIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Errorf("delta %d was renumbered from %d to %d; the rollup watermark "+
				"is an id, so renumbering makes it meaningless", i, wantIDs[i], gotIDs[i])
		}
	}
}
