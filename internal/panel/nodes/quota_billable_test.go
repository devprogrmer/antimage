package nodes

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"

	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/testutil/storetest"
)

// discardLogger keeps sweeper output out of test logs.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// C4: quota is enforced on BILLABLE bytes for the current period.
//
// The distinguishing case in every test here is a coefficient that is not
// x1.0. At unity, billable enforcement and the raw-counter predicate it
// replaces agree exactly, so a test that leaves coefficients alone cannot tell
// the two apart -- and would pass against the very bug this change removes.

func setQuota(t *testing.T, st *store.Store, subjectID, quota, periodStart int64) {
	t.Helper()
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`UPDATE subjects SET quota_bytes = ?, quota_period_start = ? WHERE id = ?`,
			quota, periodStart, subjectID)
		return err
	})
	if err != nil {
		t.Fatalf("set quota: %v", err)
	}
}

func overQuotaIDs(t *testing.T, st *store.Store, now int64) []int64 {
	t.Helper()
	got, err := findSubjectsOverQuota(context.Background(), st, now)
	if err != nil {
		t.Fatalf("findSubjectsOverQuota: %v", err)
	}
	ids := make([]int64, 0, len(got))
	for _, s := range got {
		ids = append(ids, s.SubjectID)
	}
	return ids
}

// THE point of C4. A node priced at x2.0 must move when the customer is cut
// off, not only what they are charged.
func TestQuotaIsReachedOnBillableNotRawBytes(t *testing.T) {
	st := storetest.New(t)
	nodeID, alice, _ := twoSubjects(t, st)
	setCoefficient(t, st, "nodes", nodeID, 20000) // x2.0

	const at = 3600
	setQuota(t, st, alice, 150, 0)
	ingestAndFold(t, st, nodeID, 1, []UsageDelta{
		{SubjectID: alice, UplinkBytes: 100, DownlinkBytes: 0},
	}, at)

	// Raw is 100, under the quota of 150. Billable is 200, over it.
	ids := overQuotaIDs(t, st, at+3600)
	if len(ids) != 1 || ids[0] != alice {
		t.Errorf("over-quota subjects = %v, want [%d]; 100 raw bytes on a x2.0 "+
			"node bill as 200 and exceed a quota of 150, but enforcement is "+
			"still reading the raw counter", ids, alice)
	}
}

// And the converse: a discount must delay the cut-off, not just the invoice.
func TestADiscountDelaysTheFreeze(t *testing.T) {
	st := storetest.New(t)
	nodeID, alice, _ := twoSubjects(t, st)
	setCoefficient(t, st, "nodes", nodeID, 5000) // x0.5

	const at = 3600
	setQuota(t, st, alice, 100, 0)
	ingestAndFold(t, st, nodeID, 1, []UsageDelta{
		{SubjectID: alice, UplinkBytes: 150, DownlinkBytes: 0},
	}, at)

	// Raw is 150, over the quota of 100. Billable is 75, under it.
	if ids := overQuotaIDs(t, st, at+3600); len(ids) != 0 {
		t.Errorf("over-quota subjects = %v, want none; 150 raw bytes at x0.5 "+
			"bill as 75 and are under a quota of 100", ids)
	}
}

// Per-group, not per-subject-total. This is why AD-2's suggested adjusted
// threshold could not be used: there is no single coefficient to divide by.
func TestQuotaSumsBillablePerNode(t *testing.T) {
	st := storetest.New(t)
	nodeA, alice, _ := twoSubjects(t, st)
	nodeB := seedNodeOnly(t, st, "cheap")
	setCoefficient(t, st, "nodes", nodeA, 20000) // x2.0
	setCoefficient(t, st, "nodes", nodeB, 10000) // x1.0

	const at = 3600
	// 100 on each node: raw 200, billable 200 + 100 = 300.
	ingestAndFold(t, st, nodeA, 1, []UsageDelta{
		{SubjectID: alice, UplinkBytes: 100, DownlinkBytes: 0},
	}, at)
	ingestAndFold(t, st, nodeB, 1, []UsageDelta{
		{SubjectID: alice, UplinkBytes: 100, DownlinkBytes: 0},
	}, at)

	// Under 400 (what a single x2.0 threshold would compute) and over 300.
	setQuota(t, st, alice, 350, 0)
	if ids := overQuotaIDs(t, st, at+3600); len(ids) != 0 {
		t.Errorf("over-quota = %v, want none; billable is 300 against a quota "+
			"of 350, so applying ONE node coefficient to the combined total "+
			"has over-counted the cheap node's traffic", ids)
	}

	setQuota(t, st, alice, 250, 0)
	if ids := overQuotaIDs(t, st, at+3600); len(ids) != 1 {
		t.Errorf("over-quota = %v, want alice; billable is 300 against a quota "+
			"of 250", ids)
	}
}

// The period is a window, and traffic before it must not count. Without this
// the sweeper compares a per-period quota against a subject's whole history and
// nobody ever gets a fresh month.
func TestUsageBeforeThePeriodDoesNotCount(t *testing.T) {
	st := storetest.New(t)
	nodeID, alice, _ := twoSubjects(t, st)

	// Last period's traffic.
	ingestAndFold(t, st, nodeID, 1, []UsageDelta{
		{SubjectID: alice, UplinkBytes: 1000, DownlinkBytes: 0},
	}, 3600)
	// This period's.
	ingestAndFold(t, st, nodeID, 2, []UsageDelta{
		{SubjectID: alice, UplinkBytes: 10, DownlinkBytes: 0},
	}, 7200)

	// The period starts at 7200, so only 10 bytes are in it.
	setQuota(t, st, alice, 100, 7200)
	if ids := overQuotaIDs(t, st, 10800); len(ids) != 0 {
		t.Errorf("over-quota = %v, want none; last period's 1000 bytes are "+
			"being counted against this period's quota, so the subject can "+
			"never come back from a reset", ids)
	}
}

// A quota that is set must be enforced. A subject with no recorded period is
// the ordinary state for anyone created before C4 or through a caller that does
// not set one, and skipping them would leave a quota that does nothing.
func TestASubjectWithNoRecordedPeriodIsStillEnforced(t *testing.T) {
	st := storetest.New(t)
	nodeID, alice, _ := twoSubjects(t, st)

	const at = 3600
	// quota_period_start left NULL on purpose.
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`UPDATE subjects SET quota_bytes = 50, quota_period_start = NULL WHERE id = ?`,
			alice)
		return err
	})
	if err != nil {
		t.Fatalf("set quota: %v", err)
	}

	ingestAndFold(t, st, nodeID, 1, []UsageDelta{
		{SubjectID: alice, UplinkBytes: 100, DownlinkBytes: 0},
	}, at)

	if ids := overQuotaIDs(t, st, at+3600); len(ids) != 1 {
		t.Errorf("over-quota = %v, want alice; a subject with a quota and no "+
			"recorded period was never enforced at all, which makes the quota "+
			"decorative", ids)
	}
}

// Enforcement must be current, not as-of-the-last-sweep: a subject who blows
// through their quota in one burst is frozen on the next sweep, not the next
// hour.
func TestUnrolledTrafficCountsTowardQuota(t *testing.T) {
	st := storetest.New(t)
	nodeID, alice, _ := twoSubjects(t, st)

	const at = 3600
	setQuota(t, st, alice, 50, 0)
	// Ingested, deliberately NOT folded.
	if err := IngestUsageReport(context.Background(), st, nodeID, 1, []UsageDelta{
		{SubjectID: alice, UplinkBytes: 100, DownlinkBytes: 0},
	}, at); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	if ids := overQuotaIDs(t, st, at+3600); len(ids) != 1 {
		t.Errorf("over-quota = %v, want alice; traffic that has not been rolled "+
			"up yet is invisible to enforcement, so a burst goes unmetered "+
			"until the next hourly fold", ids)
	}
}

// Already-frozen and disabled subjects are not candidates, so a sweep does not
// re-freeze or fight another control.
func TestFrozenAndDisabledSubjectsAreNotCandidates(t *testing.T) {
	st := storetest.New(t)
	nodeID, alice, bob := twoSubjects(t, st)

	const at = 3600
	setQuota(t, st, alice, 1, 0)
	setQuota(t, st, bob, 1, 0)
	ingestAndFold(t, st, nodeID, 1, []UsageDelta{
		{SubjectID: alice, UplinkBytes: 100, DownlinkBytes: 0},
		{SubjectID: bob, UplinkBytes: 100, DownlinkBytes: 0},
	}, at)

	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`UPDATE subjects SET frozen_at = 1 WHERE id = ?`, alice); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE subjects SET enabled = 0 WHERE id = ?`, bob)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	if ids := overQuotaIDs(t, st, at+3600); len(ids) != 0 {
		t.Errorf("over-quota = %v, want none; a frozen or disabled subject is "+
			"not a freeze candidate", ids)
	}
}

// ------------------------------------------------------- the reset sweeper

// A reset must move the WINDOW, not just zero a counter.
//
// Enforcement sums billable from quota_period_start, and the usage history it
// reads is permanent -- rollups are never pruned. So a reset that left the
// start where it was would have the subject re-freeze on the very next sweep,
// still carrying last period's traffic. Zeroing quota_used_bytes does nothing
// about that, because the counter is no longer what enforcement reads.
func TestResetAdvancesThePeriodWindow(t *testing.T) {
	st := storetest.New(t)
	nodeID, alice, _ := twoSubjects(t, st)
	ctx := context.Background()

	const periodStart = 0
	const resetAt = 7200
	setQuota(t, st, alice, 50, periodStart)
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`UPDATE subjects SET quota_reset_at = ?, quota_period_seconds = 7200
			  WHERE id = ?`, resetAt, alice)
		return err
	}); err != nil {
		t.Fatalf("schedule reset: %v", err)
	}

	// Traffic in the first period puts them over.
	ingestAndFold(t, st, nodeID, 1, []UsageDelta{
		{SubjectID: alice, UplinkBytes: 100, DownlinkBytes: 0},
	}, 3600)
	if ids := overQuotaIDs(t, st, resetAt); len(ids) != 1 {
		t.Fatalf("over-quota before reset = %v, want alice; the rest of this "+
			"test would prove nothing", ids)
	}

	resetter := &QuotaResetSweeper{Store: st, Log: discardLogger()}
	if err := resetter.Run(ctx, resetAt); err != nil {
		t.Fatalf("reset sweep: %v", err)
	}

	var gotStart, gotReset sql.NullInt64
	if err := st.Read().QueryRow(
		`SELECT quota_period_start, quota_reset_at FROM subjects WHERE id = ?`, alice,
	).Scan(&gotStart, &gotReset); err != nil {
		t.Fatalf("read period: %v", err)
	}
	if !gotStart.Valid || gotStart.Int64 != resetAt {
		t.Errorf("quota_period_start = %v after reset, want %d; the window did "+
			"not advance, so last period's traffic still counts and the subject "+
			"is re-frozen on the next sweep", gotStart, resetAt)
	}

	// And they are genuinely under quota again in the new period.
	if ids := overQuotaIDs(t, st, resetAt+3600); len(ids) != 0 {
		t.Errorf("over-quota after reset = %v, want none; the reset did not "+
			"actually give the subject a fresh period", ids)
	}

	// The stored period length is what advances the next reset, not the old
	// hardcoded thirty days.
	if !gotReset.Valid || gotReset.Int64 != resetAt+7200 {
		t.Errorf("quota_reset_at = %v after reset, want %d; the per-subject "+
			"period length was ignored in favour of a constant, so a weekly "+
			"plan silently becomes a monthly one", gotReset, resetAt+7200)
	}
}

// A subject with no recorded period length keeps the thirty days the old
// constant meant, so nothing changes for anyone who never set one.
func TestResetWithoutARecordedPeriodUsesTheOldDefault(t *testing.T) {
	st := storetest.New(t)
	_, alice, _ := twoSubjects(t, st)
	ctx := context.Background()

	const resetAt = 7200
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`UPDATE subjects
			    SET quota_bytes = 100, quota_reset_at = ?, quota_period_seconds = NULL
			  WHERE id = ?`, resetAt, alice)
		return err
	}); err != nil {
		t.Fatalf("schedule reset: %v", err)
	}

	resetter := &QuotaResetSweeper{Store: st, Log: discardLogger()}
	if err := resetter.Run(ctx, resetAt); err != nil {
		t.Fatalf("reset sweep: %v", err)
	}

	var gotReset int64
	if err := st.Read().QueryRow(
		`SELECT quota_reset_at FROM subjects WHERE id = ?`, alice).Scan(&gotReset); err != nil {
		t.Fatalf("read reset: %v", err)
	}
	if want := int64(resetAt + DefaultQuotaPeriodSeconds); gotReset != want {
		t.Errorf("quota_reset_at = %d, want %d (the thirty days the old "+
			"constant meant)", gotReset, want)
	}
}
