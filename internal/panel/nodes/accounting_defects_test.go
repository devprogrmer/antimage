package nodes

import (
	"context"
	"database/sql"

	"github.com/amyrm/antimage/internal/panel/store"
	"testing"
)

// Regression cover for the two ingest defects found by probing accounting.go.
//
// Both survived because every pre-existing accounting test uses a report
// carrying exactly ONE subject and calls each rollup exactly ONCE. Neither
// shape is what production does: a node reports every user it serves in one
// message, and the sweeper in cmd/antimage-panel runs the hourly rollup every
// hour over a seven-day retention window.

// twoSubjects builds a node and two subjects, the minimum needed to exercise a
// report of the shape a real node sends.
func twoSubjects(t *testing.T, st *store.Store) (nodeID, alice, bob int64) {
	t.Helper()
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		r, err := tx.ExecContext(context.Background(),
			`INSERT INTO nodes (name, address, created_at) VALUES ('n', '127.0.0.1', 1000)`)
		if err != nil {
			return err
		}
		nodeID, _ = r.LastInsertId()
		for _, name := range []string{"alice", "bob"} {
			r, err = tx.ExecContext(context.Background(),
				`INSERT INTO subjects (name, enabled, quota_used_bytes, created_at)
				 VALUES (?, 1, 0, 1000)`, name)
			if err != nil {
				return err
			}
			id, _ := r.LastInsertId()
			if name == "alice" {
				alice = id
			} else {
				bob = id
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	return nodeID, alice, bob
}

func usageOf(t *testing.T, st *store.Store, id int64) int64 {
	t.Helper()
	var used int64
	if err := st.Read().QueryRow(
		`SELECT quota_used_bytes FROM subjects WHERE id = ?`, id).Scan(&used); err != nil {
		t.Fatalf("read usage: %v", err)
	}
	return used
}

func rollupTotal(t *testing.T, st *store.Store) int64 {
	t.Helper()
	var total int64
	if err := st.Read().QueryRow(
		`SELECT COALESCE(SUM(uplink_bytes + downlink_bytes), 0) FROM usage_rollups_hourly`,
	).Scan(&total); err != nil {
		t.Fatalf("read rollup total: %v", err)
	}
	return total
}

// DEFECT 1: a report carrying more than one subject was rejected in full.
//
// usage_deltas carried UNIQUE (node_id, sequence) while IngestUsageReport
// inserts one row per SAMPLE under a single sequence, so the second sample
// collided. The insert runs inside st.Write, so the whole transaction rolled
// back: not a partial write but total loss of the report, silent to the node,
// which has already advanced its counters.
//
// The idempotency key was right in intent -- (node_id, sequence) is what makes
// at-least-once delivery exact -- but enforced on the wrong grain.
func TestReportCarryingManySubjectsIsIngestedWhole(t *testing.T) {
	st := mustOpen(t)
	defer st.Close()
	ctx := context.Background()
	nodeID, alice, bob := twoSubjects(t, st)

	err := IngestUsageReport(ctx, st, nodeID, 1, []UsageDelta{
		{SubjectID: alice, UplinkBytes: 100, DownlinkBytes: 100},
		{SubjectID: bob, UplinkBytes: 200, DownlinkBytes: 200},
	}, 1000)
	if err != nil {
		t.Fatalf("multi-subject report rejected: %v\n"+
			"a node reports every user it serves in one message, so this is the "+
			"normal path on any node with two active users", err)
	}

	var rows int
	if err := st.Read().QueryRow(`SELECT COUNT(*) FROM usage_deltas`).Scan(&rows); err != nil {
		t.Fatalf("count deltas: %v", err)
	}
	if rows != 2 {
		t.Errorf("usage_deltas rows = %d, want 2", rows)
	}
	// Both subjects must be credited, not just whichever came first: a partial
	// apply would be worse than the outright failure it replaced.
	if got := usageOf(t, st, alice); got != 200 {
		t.Errorf("alice quota_used_bytes = %d, want 200", got)
	}
	if got := usageOf(t, st, bob); got != 400 {
		t.Errorf("bob quota_used_bytes = %d, want 400", got)
	}
}

// The report-level idempotency guard must survive the constraint change. A
// replayed sequence is still applied at most once (SP3 invariant 10).
func TestReplayedMultiSubjectReportIsAppliedOnce(t *testing.T) {
	st := mustOpen(t)
	defer st.Close()
	ctx := context.Background()
	nodeID, alice, bob := twoSubjects(t, st)

	samples := []UsageDelta{
		{SubjectID: alice, UplinkBytes: 100, DownlinkBytes: 100},
		{SubjectID: bob, UplinkBytes: 200, DownlinkBytes: 200},
	}
	for i := 0; i < 3; i++ {
		if err := IngestUsageReport(ctx, st, nodeID, 1, samples, 1000); err != nil {
			t.Fatalf("ingest %d: %v", i, err)
		}
	}

	var rows int
	if err := st.Read().QueryRow(`SELECT COUNT(*) FROM usage_deltas`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 2 {
		t.Errorf("rows = %d after three deliveries of one report, want 2", rows)
	}
	if got := usageOf(t, st, alice); got != 200 {
		t.Errorf("alice = %d, want 200: a replay was applied twice", got)
	}
}

// Distinct sequences from one node must all land -- the constraint change must
// not have loosened idempotency into "anything goes".
func TestDistinctSequencesAllLand(t *testing.T) {
	st := mustOpen(t)
	defer st.Close()
	ctx := context.Background()
	nodeID, alice, bob := twoSubjects(t, st)

	for seq := int64(1); seq <= 3; seq++ {
		if err := IngestUsageReport(ctx, st, nodeID, seq, []UsageDelta{
			{SubjectID: alice, UplinkBytes: 10, DownlinkBytes: 10},
			{SubjectID: bob, UplinkBytes: 10, DownlinkBytes: 10},
		}, 1000+seq); err != nil {
			t.Fatalf("sequence %d: %v", seq, err)
		}
	}
	if got := usageOf(t, st, alice); got != 60 {
		t.Errorf("alice = %d after three reports of 20, want 60", got)
	}
}

// DEFECT 2: RollupHourly inflated the rollup on every run.
//
// It selected WHERE created_at < ? with no lower bound and merged with
// x = x + excluded.x, so every run re-folded every unpruned delta. This is not
// a retry hazard: the sweeper in cmd/antimage-panel runs it EVERY HOUR while
// PruneUsageDeltas keeps deltas for SEVEN DAYS, so each delta was folded on the
// order of 168 times in normal operation.
//
// It matters more under Phase C than it does now, because AD-2 computes
// billable at read time from the rollup -- an inflated rollup is an inflated
// bill -- and pruning eventually makes the error unreconstructable.
func TestRepeatedHourlyRollupsDoNotInflate(t *testing.T) {
	st := mustOpen(t)
	defer st.Close()
	ctx := context.Background()
	nodeID, alice, bob := twoSubjects(t, st)

	if err := IngestUsageReport(ctx, st, nodeID, 1, []UsageDelta{
		{SubjectID: alice, UplinkBytes: 100, DownlinkBytes: 100},
		{SubjectID: bob, UplinkBytes: 50, DownlinkBytes: 50},
	}, 1000); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	if err := RollupHourly(ctx, st, 100000); err != nil {
		t.Fatalf("first rollup: %v", err)
	}
	first := rollupTotal(t, st)
	if first != 300 {
		t.Fatalf("rollup total = %d after one run, want 300", first)
	}

	// The sweeper's real cadence: many runs over a retention window in which
	// the deltas are still present.
	for i := 0; i < 24; i++ {
		if err := RollupHourly(ctx, st, 100000+int64(i)); err != nil {
			t.Fatalf("rollup %d: %v", i, err)
		}
	}
	if got := rollupTotal(t, st); got != 300 {
		t.Errorf("rollup total = %d after 25 runs, want 300: each run re-folded "+
			"every delta, so a customer's billed traffic grows every hour", got)
	}
}

// New deltas arriving between runs must still be folded. An idempotent rollup
// that folded nothing would pass the test above and be useless.
func TestHourlyRollupFoldsDeltasArrivingBetweenRuns(t *testing.T) {
	st := mustOpen(t)
	defer st.Close()
	ctx := context.Background()
	nodeID, alice, _ := twoSubjects(t, st)

	if err := IngestUsageReport(ctx, st, nodeID, 1,
		[]UsageDelta{{SubjectID: alice, UplinkBytes: 100, DownlinkBytes: 0}}, 1000); err != nil {
		t.Fatalf("ingest 1: %v", err)
	}
	if err := RollupHourly(ctx, st, 100000); err != nil {
		t.Fatalf("rollup 1: %v", err)
	}
	if got := rollupTotal(t, st); got != 100 {
		t.Fatalf("after first fold = %d, want 100", got)
	}

	// A second report in the SAME hour bucket, which must accumulate onto the
	// existing row rather than replace it.
	if err := IngestUsageReport(ctx, st, nodeID, 2,
		[]UsageDelta{{SubjectID: alice, UplinkBytes: 25, DownlinkBytes: 0}}, 1500); err != nil {
		t.Fatalf("ingest 2: %v", err)
	}
	if err := RollupHourly(ctx, st, 100000); err != nil {
		t.Fatalf("rollup 2: %v", err)
	}
	if got := rollupTotal(t, st); got != 125 {
		t.Errorf("after second fold = %d, want 125: a new delta was not folded", got)
	}

	// And re-running still changes nothing.
	if err := RollupHourly(ctx, st, 100000); err != nil {
		t.Fatalf("rollup 3: %v", err)
	}
	if got := rollupTotal(t, st); got != 125 {
		t.Errorf("after a redundant run = %d, want 125", got)
	}
}

// The daily rollup had the same accumulating merge, reading from a table that
// is a complete aggregate rather than a log. Recomputing it must be safe.
func TestRepeatedDailyRollupsDoNotInflate(t *testing.T) {
	st := mustOpen(t)
	defer st.Close()
	ctx := context.Background()
	nodeID, alice, _ := twoSubjects(t, st)

	if err := IngestUsageReport(ctx, st, nodeID, 1,
		[]UsageDelta{{SubjectID: alice, UplinkBytes: 400, DownlinkBytes: 100}}, 1000); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if err := RollupHourly(ctx, st, 100000); err != nil {
		t.Fatalf("hourly: %v", err)
	}

	dailyTotal := func() int64 {
		t.Helper()
		var total int64
		if err := st.Read().QueryRow(
			`SELECT COALESCE(SUM(uplink_bytes + downlink_bytes), 0) FROM usage_rollups_daily`,
		).Scan(&total); err != nil {
			t.Fatalf("read daily: %v", err)
		}
		return total
	}

	for i := 0; i < 5; i++ {
		if err := RollupDaily(ctx, st, 200000); err != nil {
			t.Fatalf("daily %d: %v", i, err)
		}
	}
	if got := dailyTotal(); got != 500 {
		t.Errorf("daily total = %d after five runs, want 500", got)
	}
}

// Determinism: the same deltas folded from a clean slate must produce the same
// rollup, whatever order and however many times the sweeper ran.
func TestRollupIsDeterministicAcrossRunPatterns(t *testing.T) {
	ctx := context.Background()

	build := func(t *testing.T, runs []int) int64 {
		t.Helper()
		st := mustOpen(t)
		defer st.Close()
		nodeID, alice, bob := twoSubjects(t, st)

		seq := int64(0)
		for _, batch := range runs {
			for i := 0; i < batch; i++ {
				seq++
				if err := IngestUsageReport(ctx, st, nodeID, seq, []UsageDelta{
					{SubjectID: alice, UplinkBytes: 7, DownlinkBytes: 3},
					{SubjectID: bob, UplinkBytes: 11, DownlinkBytes: 1},
				}, 1000+seq); err != nil {
					t.Fatalf("ingest: %v", err)
				}
			}
			if err := RollupHourly(ctx, st, 100000); err != nil {
				t.Fatalf("rollup: %v", err)
			}
		}
		return rollupTotal(t, st)
	}

	// Six reports, folded in different groupings. Same input, same output.
	all := build(t, []int{6})
	split := build(t, []int{1, 2, 3})
	trickle := build(t, []int{1, 1, 1, 1, 1, 1})

	if all != split || split != trickle {
		t.Errorf("rollup depends on when the sweeper happened to run: "+
			"one batch=%d, three batches=%d, six batches=%d", all, split, trickle)
	}
	if all != 132 {
		t.Errorf("total = %d, want 132 (6 reports x 22 bytes)", all)
	}
}
