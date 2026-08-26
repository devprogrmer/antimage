package nodes

import (
	"context"
	"testing"

	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/testutil/storetest"
)

// The rollups carry (node, service) since C3, and NULL is a real value in both.
// SQLite treats NULLs as distinct in a unique index, so the row identity is an
// expression index over COALESCE. These are what hold that in place.

func hourlyRowCount(t *testing.T, st *store.Store, subjectID int64) int {
	t.Helper()
	var n int
	if err := st.Read().QueryRow(
		`SELECT COUNT(*) FROM usage_rollups_hourly WHERE subject_id = ?`,
		subjectID).Scan(&n); err != nil {
		t.Fatalf("count hourly rows: %v", err)
	}
	return n
}

func hourlyBytes(t *testing.T, st *store.Store, subjectID int64) int64 {
	t.Helper()
	var n int64
	if err := st.Read().QueryRow(
		`SELECT COALESCE(SUM(uplink_bytes + downlink_bytes), 0)
		   FROM usage_rollups_hourly WHERE subject_id = ?`,
		subjectID).Scan(&n); err != nil {
		t.Fatalf("sum hourly bytes: %v", err)
	}
	return n
}

// THE property the COALESCE index exists for.
//
// Unattributed traffic has a NULL service_id. Under a plain unique index on the
// bare columns, NULL never equals NULL, so ON CONFLICT would not fire and each
// fold would INSERT another row for the same bucket instead of merging into it.
// The daily fold then OVERWRITES with the group total rather than adding, so it
// would read those duplicates and multiply the day by however many had piled
// up -- the 168x inflation 00026 fixed, returning through a different door and
// only for traffic nobody could attribute.
func TestUnattributedTrafficMergesIntoOneRollupRow(t *testing.T) {
	st := storetest.New(t)
	nodeID, alice, _ := twoSubjects(t, st)
	ctx := context.Background()

	const at = 3600
	// Two separate reports in the same hour, each unattributed, each folded.
	for seq := int64(1); seq <= 3; seq++ {
		if err := IngestUsageReport(ctx, st, nodeID, seq, []UsageDelta{
			{SubjectID: alice, ServiceID: 0, UplinkBytes: 100, DownlinkBytes: 0},
		}, at); err != nil {
			t.Fatalf("ingest %d: %v", seq, err)
		}
		if err := RollupHourly(ctx, st, at); err != nil {
			t.Fatalf("rollup %d: %v", seq, err)
		}
	}

	if got := hourlyRowCount(t, st, alice); got != 1 {
		t.Errorf("the hour holds %d rollup rows for one unattributed bucket, want 1; "+
			"NULL service ids are not merging, so the row count grows with every "+
			"fold and the daily rollup will multiply the day by it", got)
	}
	if got := hourlyBytes(t, st, alice); got != 300 {
		t.Errorf("rolled up %d bytes, want 300", got)
	}
}

// Attributed and unattributed traffic in the same hour are different buckets,
// and must stay so: merging them would put bytes nobody could attribute under a
// service's coefficient.
func TestAttributedAndUnattributedStayInSeparateBuckets(t *testing.T) {
	st := storetest.New(t)
	nodeID, alice, _ := twoSubjects(t, st)
	svcID := seedService(t, st, nodeID)
	ctx := context.Background()

	const at = 3600
	if err := IngestUsageReport(ctx, st, nodeID, 1, []UsageDelta{
		{SubjectID: alice, ServiceID: svcID, UplinkBytes: 100, DownlinkBytes: 0},
		{SubjectID: alice, ServiceID: 0, UplinkBytes: 50, DownlinkBytes: 0},
	}, at); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if err := RollupHourly(ctx, st, at); err != nil {
		t.Fatalf("rollup: %v", err)
	}

	if got := hourlyRowCount(t, st, alice); got != 2 {
		t.Errorf("the hour holds %d rollup rows, want 2 (one attributed, one not); "+
			"collapsing them would bill unattributable bytes at a service's rate", got)
	}
	if got := hourlyBytes(t, st, alice); got != 150 {
		t.Errorf("rolled up %d bytes, want 150", got)
	}
}

// Two services in one hour are two buckets, which is the grain the billable
// formula needs.
func TestTrafficOnTwoServicesRollsUpSeparately(t *testing.T) {
	st := storetest.New(t)
	nodeID, alice, _ := twoSubjects(t, st)
	svcA := seedService(t, st, nodeID)
	svcB := seedService(t, st, nodeID)
	ctx := context.Background()

	const at = 3600
	if err := IngestUsageReport(ctx, st, nodeID, 1, []UsageDelta{
		{SubjectID: alice, ServiceID: svcA, UplinkBytes: 100, DownlinkBytes: 0},
		{SubjectID: alice, ServiceID: svcB, UplinkBytes: 700, DownlinkBytes: 0},
	}, at); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if err := RollupHourly(ctx, st, at); err != nil {
		t.Fatalf("rollup: %v", err)
	}

	if got := hourlyRowCount(t, st, alice); got != 2 {
		t.Fatalf("the hour holds %d rollup rows, want 2; the service dimension "+
			"was summed away and service_coef has nothing left to apply to", got)
	}
	if got := hourlyBytes(t, st, alice); got != 800 {
		t.Errorf("rolled up %d bytes, want 800", got)
	}
}

// The daily fold carries the same grain, and repeating it must not inflate --
// it overwrites with the group total, so a bucket that failed to match its own
// row would leave two rows each holding the full total.
func TestDailyRollupKeepsTheGrainAndDoesNotInflate(t *testing.T) {
	st := storetest.New(t)
	nodeID, alice, _ := twoSubjects(t, st)
	svcID := seedService(t, st, nodeID)
	ctx := context.Background()

	const at = 3600
	if err := IngestUsageReport(ctx, st, nodeID, 1, []UsageDelta{
		{SubjectID: alice, ServiceID: svcID, UplinkBytes: 100, DownlinkBytes: 0},
		{SubjectID: alice, ServiceID: 0, UplinkBytes: 50, DownlinkBytes: 0},
	}, at); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if err := RollupHourly(ctx, st, at); err != nil {
		t.Fatalf("rollup hourly: %v", err)
	}

	const dayEnd = 86400
	for i := 0; i < 3; i++ {
		if err := RollupDaily(ctx, st, dayEnd); err != nil {
			t.Fatalf("rollup daily %d: %v", i, err)
		}
	}

	var rows int
	var bytes int64
	if err := st.Read().QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(uplink_bytes + downlink_bytes), 0)
		   FROM usage_rollups_daily WHERE subject_id = ?`, alice,
	).Scan(&rows, &bytes); err != nil {
		t.Fatalf("read daily: %v", err)
	}
	if rows != 2 {
		t.Errorf("the day holds %d rows, want 2 (one per bucket); three identical "+
			"folds should be indistinguishable from one", rows)
	}
	if bytes != 150 {
		t.Errorf("the day totals %d bytes, want 150; repeating the daily fold "+
			"inflated it", bytes)
	}
}
