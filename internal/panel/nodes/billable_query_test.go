package nodes

import (
	"context"
	"database/sql"
	"testing"

	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/testutil/storetest"
)

// setCoefficient sets one row's usage_coefficient, in basis points.
func setCoefficient(t *testing.T, st *store.Store, table string, id, coef int64) {
	t.Helper()
	// Table name is a test-supplied constant, never user input.
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`UPDATE `+table+` SET usage_coefficient = ? WHERE id = ?`, coef, id)
		return err
	})
	if err != nil {
		t.Fatalf("set %s coefficient: %v", table, err)
	}
}

// ingestAndFold puts traffic through the real ingest path and rolls it up, so
// these tests read the same rows production would.
func ingestAndFold(t *testing.T, st *store.Store, nodeID, seq int64, deltas []UsageDelta, at int64) {
	t.Helper()
	if err := IngestUsageReport(context.Background(), st, nodeID, seq, deltas, at); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if err := RollupHourly(context.Background(), st, at); err != nil {
		t.Fatalf("rollup: %v", err)
	}
}

// THE case that decided the schema.
//
// A subject on a x2.0 node and a x1.0 node has no single node coefficient.
// Applying one to their combined raw bytes is wrong for half their traffic, so
// the bill has to be summed per group -- which is only possible because the
// rollups now carry the node.
func TestBillableSumsPerNodeRatherThanOverTheTotal(t *testing.T) {
	st := storetest.New(t)
	nodeA, alice, _ := twoSubjects(t, st)
	nodeB := seedNodeOnly(t, st, "node-b")
	setCoefficient(t, st, "nodes", nodeA, 20000) // x2.0
	setCoefficient(t, st, "nodes", nodeB, 10000) // x1.0

	const at = 3600
	ingestAndFold(t, st, nodeA, 1, []UsageDelta{
		{SubjectID: alice, UplinkBytes: 100, DownlinkBytes: 0},
	}, at)
	ingestAndFold(t, st, nodeB, 1, []UsageDelta{
		{SubjectID: alice, UplinkBytes: 100, DownlinkBytes: 0},
	}, at)

	got, err := BillableForSubject(context.Background(), st, alice, 0, at+3600)
	if err != nil {
		t.Fatalf("BillableForSubject: %v", err)
	}
	if got.RawBytes != 200 {
		t.Fatalf("raw = %d, want 200", got.RawBytes)
	}
	// 100 x2.0 + 100 x1.0 = 300. A single-coefficient implementation would give
	// 400 (both at x2.0) or 200 (both at x1.0), never 300.
	if got.Billable != 300 {
		t.Errorf("billable = %d, want 300; the bill was computed with ONE node "+
			"coefficient over the combined total, which is wrong for whichever "+
			"node did not supply it", got.Billable)
	}
	if len(got.Groups) != 2 {
		t.Errorf("returned %d groups, want 2; the UI cannot show a derivation "+
			"it was not given", len(got.Groups))
	}
}

// Every factor reaches the bill, and they compound.
func TestBillableAppliesAllFourLevels(t *testing.T) {
	st := storetest.New(t)
	nodeID, alice, _ := twoSubjects(t, st)
	svcID := seedService(t, st, nodeID)

	// Make alice a reseller's customer so the fourth factor has a row.
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		role, err := tx.Exec(
			`INSERT INTO roles (name, is_builtin, permissions) VALUES ('vendor', 0, '[]')`)
		if err != nil {
			return err
		}
		roleID, _ := role.LastInsertId()
		a, err := tx.Exec(
			`INSERT INTO admins (username, password_hash, role_id, created_at)
			 VALUES ('vendor', 'x', ?, 1000)`, roleID)
		if err != nil {
			return err
		}
		adminID, _ := a.LastInsertId()
		r, err := tx.Exec(
			`INSERT INTO resellers (admin_id, display_name, enabled, credit_floor,
			                        created_at, updated_at)
			 VALUES (?, 'vendor', 1, 0, 1000, 1000)`, adminID)
		if err != nil {
			return err
		}
		resellerID, _ := r.LastInsertId()
		_, err = tx.Exec(
			`UPDATE resellers SET usage_coefficient = 20000 WHERE id = ?`, resellerID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(
			`INSERT INTO reseller_subjects (subject_id, reseller_id, cost, created_at)
			 VALUES (?, ?, 0, 1000)`, alice, resellerID)
		return err
	})
	if err != nil {
		t.Fatalf("seed reseller: %v", err)
	}

	setCoefficient(t, st, "nodes", nodeID, 20000)
	setCoefficient(t, st, "services", svcID, 20000)
	setCoefficient(t, st, "subjects", alice, 20000)

	const at = 3600
	ingestAndFold(t, st, nodeID, 1, []UsageDelta{
		{SubjectID: alice, ServiceID: svcID, UplinkBytes: 100, DownlinkBytes: 0},
	}, at)

	got, err := BillableForSubject(context.Background(), st, alice, 0, at+3600)
	if err != nil {
		t.Fatalf("BillableForSubject: %v", err)
	}
	// 100 x 2 x 2 x 2 x 2 = 1600.
	if got.Billable != 1600 {
		t.Errorf("billable = %d, want 1600 (100 x2 x2 x2 x2); a level that "+
			"reports x1.0 here is a coefficient the operator can set and the "+
			"biller ignores", got.Billable)
	}
	if len(got.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(got.Groups))
	}
	f := got.Groups[0].Factors
	if f.Node != 20000 || f.Service != 20000 || f.Subject != 20000 || f.Reseller != 20000 {
		t.Errorf("reported factors = %+v, want every level at 20000; section 11 "+
			"requires the derivation be shown and this is what the UI renders", f)
	}
}

// Unattributed traffic bills at x1.0 for the dimensions nobody recorded, and it
// still bills. Dropping it would be worse than not attributing it.
func TestUnattributedTrafficStillBills(t *testing.T) {
	st := storetest.New(t)
	nodeID, alice, _ := twoSubjects(t, st)
	setCoefficient(t, st, "nodes", nodeID, 20000)
	setCoefficient(t, st, "subjects", alice, 10000)

	const at = 3600
	// No ServiceID: the adapter could not attribute.
	ingestAndFold(t, st, nodeID, 1, []UsageDelta{
		{SubjectID: alice, UplinkBytes: 100, DownlinkBytes: 0},
	}, at)

	got, err := BillableForSubject(context.Background(), st, alice, 0, at+3600)
	if err != nil {
		t.Fatalf("BillableForSubject: %v", err)
	}
	// Node is known (x2.0), service is not (x1.0).
	if got.Billable != 200 {
		t.Errorf("billable = %d, want 200; an unknown service must bill at x1.0 "+
			"rather than dropping the traffic or guessing a coefficient", got.Billable)
	}
	if len(got.Groups) != 1 || got.Groups[0].ServiceID != nil {
		t.Errorf("groups = %+v, want one with no service", got.Groups)
	}
}

// Deltas not yet folded into a rollup must still count, or every bill would lag
// the sweeper by up to an hour.
func TestBillableIncludesUnrolledDeltas(t *testing.T) {
	st := storetest.New(t)
	nodeID, alice, _ := twoSubjects(t, st)
	setCoefficient(t, st, "nodes", nodeID, 20000)

	const at = 3600
	// Ingest WITHOUT rolling up.
	if err := IngestUsageReport(context.Background(), st, nodeID, 1, []UsageDelta{
		{SubjectID: alice, UplinkBytes: 100, DownlinkBytes: 0},
	}, at); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	got, err := BillableForSubject(context.Background(), st, alice, 0, at+3600)
	if err != nil {
		t.Fatalf("BillableForSubject: %v", err)
	}
	if got.Billable != 200 {
		t.Errorf("billable = %d, want 200; traffic that has not been folded yet "+
			"is missing from the bill", got.Billable)
	}
}

// And the two sources must not double count. The fold advances its watermark in
// the same transaction that writes the rows, so a delta belongs to exactly one.
func TestBillableDoesNotDoubleCountAcrossTheFold(t *testing.T) {
	st := storetest.New(t)
	nodeID, alice, _ := twoSubjects(t, st)

	const at = 3600
	if err := IngestUsageReport(context.Background(), st, nodeID, 1, []UsageDelta{
		{SubjectID: alice, UplinkBytes: 100, DownlinkBytes: 0},
	}, at); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	before, err := BillableForSubject(context.Background(), st, alice, 0, at+3600)
	if err != nil {
		t.Fatalf("before: %v", err)
	}

	// Fold, several times: the rollup is idempotent and so must the bill be.
	for i := 0; i < 3; i++ {
		if err := RollupHourly(context.Background(), st, at); err != nil {
			t.Fatalf("rollup: %v", err)
		}
	}

	after, err := BillableForSubject(context.Background(), st, alice, 0, at+3600)
	if err != nil {
		t.Fatalf("after: %v", err)
	}
	if before.RawBytes != after.RawBytes {
		t.Errorf("raw went from %d to %d across the fold; the delta is being "+
			"counted both as un-rolled and as rolled up",
			before.RawBytes, after.RawBytes)
	}
	if after.RawBytes != 100 {
		t.Errorf("raw = %d, want 100", after.RawBytes)
	}
}

// A period with no traffic is a real answer, and it marshals to an empty array
// rather than null so the UI does not branch on it.
func TestBillableOverAnEmptyPeriod(t *testing.T) {
	st := storetest.New(t)
	_, alice, _ := twoSubjects(t, st)

	got, err := BillableForSubject(context.Background(), st, alice, 0, 100)
	if err != nil {
		t.Fatalf("BillableForSubject: %v", err)
	}
	if got.Billable != 0 || got.RawBytes != 0 {
		t.Errorf("empty period billed %d from %d raw", got.Billable, got.RawBytes)
	}
	if got.Groups == nil {
		t.Error("Groups is nil, which marshals to null")
	}
}

// The period bounds are honoured, so a monthly invoice does not quietly include
// last month.
func TestBillableRespectsThePeriod(t *testing.T) {
	st := storetest.New(t)
	nodeID, alice, _ := twoSubjects(t, st)

	ingestAndFold(t, st, nodeID, 1, []UsageDelta{
		{SubjectID: alice, UplinkBytes: 100, DownlinkBytes: 0},
	}, 3600)
	ingestAndFold(t, st, nodeID, 2, []UsageDelta{
		{SubjectID: alice, UplinkBytes: 700, DownlinkBytes: 0},
	}, 7200)

	// Only the first hour.
	got, err := BillableForSubject(context.Background(), st, alice, 3600, 7200)
	if err != nil {
		t.Fatalf("BillableForSubject: %v", err)
	}
	if got.RawBytes != 100 {
		t.Errorf("raw = %d over [3600, 7200), want 100; the window is not being "+
			"applied", got.RawBytes)
	}
}
