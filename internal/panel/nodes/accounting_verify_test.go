package nodes

import (
	"context"
	"database/sql"
	"testing"

	"github.com/amyrm/antimage/internal/panel/store"
)

// seedTraffic ingests `reports` reports of 10 up / 10 down per subject, all
// landing in the same hour bucket.
func seedTraffic(t *testing.T, st *store.Store, nodeID, alice, bob int64, reports int) {
	t.Helper()
	ctx := context.Background()
	for seq := 1; seq <= reports; seq++ {
		if err := IngestUsageReport(ctx, st, nodeID, int64(seq), []UsageDelta{
			{SubjectID: alice, UplinkBytes: 10, DownlinkBytes: 10},
			{SubjectID: bob, UplinkBytes: 10, DownlinkBytes: 10},
		}, 1000+int64(seq)); err != nil {
			t.Fatalf("ingest %d: %v", seq, err)
		}
	}
}

// The coefficient migration is supposed to change no figure anywhere: it adds
// columns whose defaults reproduce the previous behaviour exactly. Since the
// migrations run at Open, a database opened at the current schema version has
// already applied it -- so the property under test is that the accounting
// figures are a function of the traffic and not of the schema.
func TestCoefficientColumnsDoNotAlterAnyFigure(t *testing.T) {
	st := mustOpen(t)
	defer st.Close()
	ctx := context.Background()
	nodeID, alice, bob := twoSubjects(t, st)
	seedTraffic(t, st, nodeID, alice, bob, 3)
	if err := RollupHourly(ctx, st, 100000); err != nil {
		t.Fatalf("rollup: %v", err)
	}

	before, err := TakeChecksum(ctx, st)
	if err != nil {
		t.Fatalf("checksum: %v", err)
	}

	// Every coefficient must be at x1.0 after the migration. A non-unity
	// default would silently reprice every existing deployment.
	if before.NonUnityCoefficients != 0 {
		t.Errorf("coefficients off x1.0 = %d, want 0: the migration must be "+
			"behaviour-preserving", before.NonUnityCoefficients)
	}

	// And the columns exist to be set. Reading one proves the migration ran
	// rather than the count above passing because the column is missing.
	for _, table := range []string{"nodes", "services", "subjects", "resellers"} {
		var coef int64
		err := st.Read().QueryRow(
			`SELECT COALESCE(MIN(usage_coefficient), 10000) FROM ` + table).Scan(&coef)
		if err != nil {
			t.Errorf("%s.usage_coefficient missing: %v", table, err)
			continue
		}
		if coef != 10000 {
			t.Errorf("%s.usage_coefficient = %d, want 10000", table, coef)
		}
	}

	// usage_deltas.service_id exists and is NULL for rows recorded before
	// attribution. NULL is a true statement; a default would be a false one.
	var attributed, total int64
	if err := st.Read().QueryRow(
		`SELECT COUNT(service_id), COUNT(*) FROM usage_deltas`).Scan(&attributed, &total); err != nil {
		t.Fatalf("usage_deltas.service_id missing: %v", err)
	}
	if attributed != 0 {
		t.Errorf("%d of %d deltas carry an attribution, want 0 before C2", attributed, total)
	}

	after, err := TakeChecksum(ctx, st)
	if err != nil {
		t.Fatalf("second checksum: %v", err)
	}
	if diff := before.Divergence(after); len(diff) != 0 {
		t.Errorf("checksum moved with no traffic: %v", diff)
	}
	if before.Digest != after.Digest {
		t.Error("digest is not stable across repeated reads")
	}
}

// The digest must move when a billed figure moves, or it certifies nothing.
func TestChecksumDetectsDivergence(t *testing.T) {
	st := mustOpen(t)
	defer st.Close()
	ctx := context.Background()
	nodeID, alice, bob := twoSubjects(t, st)
	seedTraffic(t, st, nodeID, alice, bob, 2)

	before, err := TakeChecksum(ctx, st)
	if err != nil {
		t.Fatalf("checksum: %v", err)
	}

	seedTraffic(t, st, nodeID, alice, bob, 1) // one more report, seq 1 replayed
	if err := IngestUsageReport(ctx, st, nodeID, 99,
		[]UsageDelta{{SubjectID: alice, UplinkBytes: 5, DownlinkBytes: 5}}, 2000); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	after, err := TakeChecksum(ctx, st)
	if err != nil {
		t.Fatalf("checksum: %v", err)
	}
	if before.Digest == after.Digest {
		t.Error("digest unchanged after traffic was added: it certifies nothing")
	}
	if diff := before.Divergence(after); len(diff) == 0 {
		t.Error("Divergence reported no difference after traffic was added")
	}
}

// A redundant sweeper run must not read as divergence, or every deploy that
// happens to straddle a tick looks like data corruption to whoever is comparing
// checksums either side of it.
//
// This is the checksum's view of rollup idempotency, and it is worth pinning
// separately: the rollup tests assert the totals directly, while this asserts
// that the tool an operator actually runs during a migration agrees with them.
func TestRedundantRollupRunIsNotDivergence(t *testing.T) {
	st := mustOpen(t)
	defer st.Close()
	ctx := context.Background()
	nodeID, alice, bob := twoSubjects(t, st)
	seedTraffic(t, st, nodeID, alice, bob, 2)
	if err := RollupHourly(ctx, st, 100000); err != nil {
		t.Fatalf("rollup: %v", err)
	}

	before, err := TakeChecksum(ctx, st)
	if err != nil {
		t.Fatalf("checksum: %v", err)
	}
	// A redundant sweeper run: advances nothing billed, and must not be
	// reported as if it had.
	if err := RollupHourly(ctx, st, 200000); err != nil {
		t.Fatalf("second rollup: %v", err)
	}
	after, err := TakeChecksum(ctx, st)
	if err != nil {
		t.Fatalf("checksum: %v", err)
	}
	if diff := before.Divergence(after); len(diff) != 0 {
		t.Errorf("a redundant rollup run read as divergence: %v", diff)
	}
	if before.Digest != after.Digest {
		t.Error("digest moved on a redundant rollup run")
	}
}

// inflate reproduces the pre-fix damage directly, since the fixed code can no
// longer produce it: fold the same deltas repeatedly the way the old rollup did.
func inflate(t *testing.T, st *store.Store, times int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < times; i++ {
		err := st.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO usage_rollups_hourly (subject_id, hour_start, uplink_bytes, downlink_bytes)
				SELECT subject_id, (created_at / 3600) * 3600, SUM(uplink_bytes), SUM(downlink_bytes)
				  FROM usage_deltas
				 GROUP BY subject_id, (created_at / 3600) * 3600
				ON CONFLICT (subject_id, hour_start) DO UPDATE SET
					uplink_bytes = usage_rollups_hourly.uplink_bytes + excluded.uplink_bytes,
					downlink_bytes = usage_rollups_hourly.downlink_bytes + excluded.downlink_bytes`)
			return err
		})
		if err != nil {
			t.Fatalf("inflate: %v", err)
		}
	}
}

// A dry run must report the projection and write absolutely nothing.
func TestRepairDryRunWritesNothing(t *testing.T) {
	st := mustOpen(t)
	defer st.Close()
	ctx := context.Background()
	nodeID, alice, bob := twoSubjects(t, st)
	seedTraffic(t, st, nodeID, alice, bob, 3)
	inflate(t, st, 5) // as the broken sweeper would have

	before, err := TakeChecksum(ctx, st)
	if err != nil {
		t.Fatalf("checksum: %v", err)
	}

	report, err := RepairHourlyRollups(ctx, st, true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !report.DryRun {
		t.Error("report does not record that it was a dry run")
	}
	if report.RecoverableHours == 0 {
		t.Error("dry run found nothing to repair, but the rollup was inflated 5x")
	}
	if report.ProjectedDelta() >= 0 {
		t.Errorf("projected delta = %+d, want negative: the bug inflated",
			report.ProjectedDelta())
	}

	after, err := TakeChecksum(ctx, st)
	if err != nil {
		t.Fatalf("checksum: %v", err)
	}
	if diff := before.Divergence(after); len(diff) != 0 {
		t.Errorf("dry run wrote to the database: %v", diff)
	}
}

// The applied repair must land exactly the projection the dry run reported.
// An operator approves a number; that number is what must be applied.
func TestRepairAppliesTheProjectedChange(t *testing.T) {
	st := mustOpen(t)
	defer st.Close()
	ctx := context.Background()
	nodeID, alice, bob := twoSubjects(t, st)
	seedTraffic(t, st, nodeID, alice, bob, 3) // 3 reports x 2 subjects x 20 = 120
	inflate(t, st, 4)

	projected, err := RepairHourlyRollups(ctx, st, true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}

	applied, err := RepairHourlyRollups(ctx, st, false)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if applied.DryRun {
		t.Error("applied run reports itself as a dry run")
	}
	if applied.ProjectedDelta() != projected.ProjectedDelta() {
		t.Errorf("applied %+d but the dry run promised %+d",
			applied.ProjectedDelta(), projected.ProjectedDelta())
	}

	// And the repaired figure is the true one: what the deltas actually say.
	if got := rollupTotal(t, st); got != 120 {
		t.Errorf("repaired rollup total = %d, want 120 (the real traffic)", got)
	}
}

// The projection must match the closed form, not merely "look negative".
//
//	true      = sum of the raw deltas
//	inflated  = true x folds
//	projected = true - inflated = -true x (folds - 1)
//
// Checking against the model rather than against a recorded number is what
// makes the dry run something an operator can reason about before approving it:
// if the reported delta is not -true x (folds-1), either the repair is wrong or
// the damage is not what we think it is, and both are worth stopping for.
func TestRepairProjectionMatchesTheModel(t *testing.T) {
	for _, folds := range []int{2, 7, 12, 168} {
		st := mustOpen(t)
		ctx := context.Background()
		nodeID, alice, bob := twoSubjects(t, st)

		const reports = 4
		seedTraffic(t, st, nodeID, alice, bob, reports) // 2 subjects x 20 bytes
		trueBytes := int64(reports * 2 * 20)

		// The fixed rollup folds once; the old one folded `folds` times in
		// total, so `folds-1` further folds reproduce the historical damage.
		if err := RollupHourly(ctx, st, 100000); err != nil {
			t.Fatalf("fold: %v", err)
		}
		inflate(t, st, folds-1)

		inflated := trueBytes * int64(folds)
		if got := rollupTotal(t, st); got != inflated {
			t.Fatalf("folds=%d: rollup = %d, want %d", folds, got, inflated)
		}

		report, err := RepairHourlyRollups(ctx, st, true)
		if err != nil {
			t.Fatalf("folds=%d: dry run: %v", folds, err)
		}

		wantDelta := -trueBytes * int64(folds-1)
		if report.ProjectedDelta() != wantDelta {
			t.Errorf("folds=%d: projected %+d, model says %+d",
				folds, report.ProjectedDelta(), wantDelta)
		}
		if report.BytesAfter != trueBytes {
			t.Errorf("folds=%d: repair would leave %d, want the true %d",
				folds, report.BytesAfter, trueBytes)
		}

		// And applying it lands exactly there.
		if _, err := RepairHourlyRollups(ctx, st, false); err != nil {
			t.Fatalf("folds=%d: apply: %v", folds, err)
		}
		if got := rollupTotal(t, st); got != trueBytes {
			t.Errorf("folds=%d: after repair = %d, want %d", folds, got, trueBytes)
		}
		_ = st.Close()
	}
}

// Idempotent: a second repair over the same data changes nothing.
func TestRepairIsIdempotent(t *testing.T) {
	st := mustOpen(t)
	defer st.Close()
	ctx := context.Background()
	nodeID, alice, bob := twoSubjects(t, st)
	seedTraffic(t, st, nodeID, alice, bob, 3)
	inflate(t, st, 3)

	if _, err := RepairHourlyRollups(ctx, st, false); err != nil {
		t.Fatalf("first repair: %v", err)
	}
	first, err := TakeChecksum(ctx, st)
	if err != nil {
		t.Fatalf("checksum: %v", err)
	}

	second, err := RepairHourlyRollups(ctx, st, false)
	if err != nil {
		t.Fatalf("second repair: %v", err)
	}
	if second.ProjectedDelta() != 0 {
		t.Errorf("second repair moved %+d bytes, want 0", second.ProjectedDelta())
	}
	afterSecond, err := TakeChecksum(ctx, st)
	if err != nil {
		t.Fatalf("checksum: %v", err)
	}
	if diff := first.Divergence(afterSecond); len(diff) != 0 {
		t.Errorf("repair is not idempotent: %v", diff)
	}
}

// Deterministic: same input, same output, independent of how it got there.
func TestRepairIsDeterministic(t *testing.T) {
	ctx := context.Background()

	run := func(t *testing.T, inflateBy int) string {
		t.Helper()
		st := mustOpen(t)
		defer st.Close()
		nodeID, alice, bob := twoSubjects(t, st)
		seedTraffic(t, st, nodeID, alice, bob, 4)
		inflate(t, st, inflateBy)
		if _, err := RepairHourlyRollups(ctx, st, false); err != nil {
			t.Fatalf("repair: %v", err)
		}
		c, err := TakeChecksum(ctx, st)
		if err != nil {
			t.Fatalf("checksum: %v", err)
		}
		return c.Digest
	}

	// However badly it was inflated, repair lands on the same true figure.
	if a, b := run(t, 2), run(t, 9); a != b {
		t.Errorf("repair depends on how inflated the data was: %s vs %s", a, b)
	}
}

// A bucket whose deltas have been pruned cannot be recomputed. Its row is left
// exactly as it is: deleting it destroys the only record the traffic happened,
// and writing a zero is equally wrong but looks deliberate.
func TestRepairLeavesUnrecoverableBucketsAlone(t *testing.T) {
	st := mustOpen(t)
	defer st.Close()
	ctx := context.Background()
	nodeID, alice, bob := twoSubjects(t, st)
	seedTraffic(t, st, nodeID, alice, bob, 3)
	inflate(t, st, 4)

	// Prune every delta, as retention eventually does.
	if _, err := PruneUsageDeltas(ctx, st, 0, 1_000_000); err != nil {
		t.Fatalf("prune: %v", err)
	}
	inflatedTotal := rollupTotal(t, st)

	report, err := RepairHourlyRollups(ctx, st, false)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if report.RecoverableHours != 0 {
		t.Errorf("recoverable = %d with no surviving deltas, want 0", report.RecoverableHours)
	}
	if report.UnrecoverableHours == 0 {
		t.Error("unrecoverable buckets were not reported")
	}
	if got := rollupTotal(t, st); got != inflatedTotal {
		t.Errorf("rollup total = %d, want %d untouched: an unrecoverable bucket "+
			"was rewritten with a figure nothing supports", got, inflatedTotal)
	}
}
