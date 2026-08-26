package nodes

import (
	"context"
	"database/sql"
	"testing"

	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/testutil/storetest"
)

// C2: usage carries the service it went through.
//
// The attribution existed at the edge -- Xray tags every user counter, and
// since C2 that tag carries the service id -- and was discarded on the way in.
// These cover the two halves of the exit criterion: attributed usage is stored
// with its service, and a tag that cannot be resolved writes NULL without
// taking the rest of the report down with it.

// seedNodeOnly adds a bare node, for the cross-node isolation case.
func seedNodeOnly(t *testing.T, st *store.Store, name string) int64 {
	t.Helper()
	var id int64
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		r, err := tx.ExecContext(context.Background(),
			`INSERT INTO nodes (name, address, created_at) VALUES (?, '127.0.0.1', 1000)`, name)
		if err != nil {
			return err
		}
		id, _ = r.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("seed node: %v", err)
	}
	return id
}

// attributions returns each delta's service_id, NULL included, for a subject.
func attributions(t *testing.T, st *store.Store, subjectID int64) []sql.NullInt64 {
	t.Helper()
	rows, err := st.Read().Query(
		`SELECT service_id FROM usage_deltas WHERE subject_id = ? ORDER BY id`, subjectID)
	if err != nil {
		t.Fatalf("read attributions: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []sql.NullInt64
	for rows.Next() {
		var v sql.NullInt64
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// The happy path, and the whole point of C2: the service reaches the row.
func TestIngestStoresTheReportedService(t *testing.T) {
	st := storetest.New(t)
	nodeID, alice, _ := twoSubjects(t, st)
	svcID := seedService(t, st, nodeID)

	err := IngestUsageReport(context.Background(), st, nodeID, 1, []UsageDelta{
		{SubjectID: alice, ServiceID: svcID, UplinkBytes: 100, DownlinkBytes: 200},
	}, 2000)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}

	got := attributions(t, st, alice)
	if len(got) != 1 {
		t.Fatalf("stored %d deltas, want 1", len(got))
	}
	if !got[0].Valid || got[0].Int64 != svcID {
		t.Errorf("service_id = %v, want %d; the attribution the node reported "+
			"was discarded on the way in, which is the defect C2 exists to fix",
			got[0], svcID)
	}
}

// THE grain change. A subject entitled to two inbounds on one node produces two
// samples in ONE report -- same node, same sequence, same subject, different
// service. Under 00026's key only one of them could be stored and the second
// INSERT failed the whole report.
//
// This is the same defect 00026 fixed one level up, and it would have come back
// the moment C2 started writing service ids.
func TestOneSubjectOnTwoServicesIngestsBothRows(t *testing.T) {
	st := storetest.New(t)
	nodeID, alice, _ := twoSubjects(t, st)
	svcA := seedService(t, st, nodeID)
	svcB := seedService(t, st, nodeID)

	err := IngestUsageReport(context.Background(), st, nodeID, 1, []UsageDelta{
		{SubjectID: alice, ServiceID: svcA, UplinkBytes: 100, DownlinkBytes: 0},
		{SubjectID: alice, ServiceID: svcB, UplinkBytes: 700, DownlinkBytes: 0},
	}, 2000)
	if err != nil {
		t.Fatalf("a report carrying one subject on two services was rejected "+
			"outright: %v", err)
	}

	got := attributions(t, st, alice)
	if len(got) != 2 {
		t.Fatalf("stored %d deltas, want 2; one inbound's traffic was silently "+
			"dropped by a uniqueness key coarser than the rows", len(got))
	}
	// And the subject's total counts both, which is what the bill is built from.
	if used := usageOf(t, st, alice); used != 800 {
		t.Errorf("quota_used_bytes = %d, want 800", used)
	}
}

// A tag can outlive the service it named: an inbound removed while a report was
// in flight is ordinary, not pathological. Losing an entire node's accounting
// because one id no longer resolves is the wrong trade.
func TestUnresolvableServiceWritesNullAndTheReportSurvives(t *testing.T) {
	st := storetest.New(t)
	nodeID, alice, bob := twoSubjects(t, st)
	svcID := seedService(t, st, nodeID)

	err := IngestUsageReport(context.Background(), st, nodeID, 1, []UsageDelta{
		// A service id that does not exist at all.
		{SubjectID: alice, ServiceID: 999999, UplinkBytes: 100, DownlinkBytes: 0},
		// A real one, in the same report.
		{SubjectID: bob, ServiceID: svcID, UplinkBytes: 50, DownlinkBytes: 0},
	}, 2000)
	if err != nil {
		t.Fatalf("one unresolvable service id failed the whole report: %v", err)
	}

	alices := attributions(t, st, alice)
	if len(alices) != 1 {
		t.Fatalf("alice has %d deltas, want 1", len(alices))
	}
	if alices[0].Valid {
		t.Errorf("service_id = %d for an id that does not exist; the panel "+
			"believed a node about a service it has no record of", alices[0].Int64)
	}
	// The unattributed traffic still counts. That is the point: the subject and
	// the bytes are what a bill is built from, and both survived.
	if used := usageOf(t, st, alice); used != 100 {
		t.Errorf("alice's usage = %d, want 100; unattributed traffic was dropped "+
			"rather than merely unattributed", used)
	}

	// The rest of the report landed intact.
	bobs := attributions(t, st, bob)
	if len(bobs) != 1 || !bobs[0].Valid || bobs[0].Int64 != svcID {
		t.Errorf("bob's delta = %v, want service %d; a neighbour's unresolvable "+
			"tag cost him his attribution", bobs, svcID)
	}
}

// The security half. Service ids are global, so a node reporting another node's
// id would otherwise have its traffic recorded against an inbound it does not
// own -- and since a service belongs to a node, that is another tenant's
// inbound, which C3 would then bill at that tenant's coefficient.
func TestServiceBelongingToAnotherNodeIsNotAccepted(t *testing.T) {
	st := storetest.New(t)
	nodeID, alice, _ := twoSubjects(t, st)
	otherNode := seedNodeOnly(t, st, "someone-elses-node")
	foreignSvc := seedService(t, st, otherNode)

	err := IngestUsageReport(context.Background(), st, nodeID, 1, []UsageDelta{
		{SubjectID: alice, ServiceID: foreignSvc, UplinkBytes: 100, DownlinkBytes: 0},
	}, 2000)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}

	got := attributions(t, st, alice)
	if len(got) != 1 {
		t.Fatalf("stored %d deltas, want 1", len(got))
	}
	if got[0].Valid {
		t.Errorf("service_id = %d: a node attributed its traffic to an inbound "+
			"on a DIFFERENT node, which C3 would bill at that node's coefficient",
			got[0].Int64)
	}
}

// An adapter that genuinely cannot attribute, and an agent older than the
// field, both send zero. Neither is an error and neither may lose traffic.
func TestUnattributedUsageStoresNull(t *testing.T) {
	st := storetest.New(t)
	nodeID, alice, _ := twoSubjects(t, st)

	err := IngestUsageReport(context.Background(), st, nodeID, 1, []UsageDelta{
		{SubjectID: alice, ServiceID: 0, UplinkBytes: 100, DownlinkBytes: 200},
	}, 2000)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}

	got := attributions(t, st, alice)
	if len(got) != 1 || got[0].Valid {
		t.Errorf("service_id = %v, want NULL", got)
	}
	if used := usageOf(t, st, alice); used != 300 {
		t.Errorf("usage = %d, want 300; an adapter that cannot attribute lost "+
			"its traffic entirely", used)
	}
}

// Idempotency is unchanged by the finer grain: a replayed report must not
// double-count, and the replay now carries several rows per subject.
func TestReplayedAttributedReportIsStillIgnored(t *testing.T) {
	st := storetest.New(t)
	nodeID, alice, _ := twoSubjects(t, st)
	svcA := seedService(t, st, nodeID)
	svcB := seedService(t, st, nodeID)

	report := []UsageDelta{
		{SubjectID: alice, ServiceID: svcA, UplinkBytes: 100, DownlinkBytes: 0},
		{SubjectID: alice, ServiceID: svcB, UplinkBytes: 700, DownlinkBytes: 0},
	}
	for i := 0; i < 3; i++ {
		if err := IngestUsageReport(context.Background(), st, nodeID, 1, report, 2000); err != nil {
			t.Fatalf("ingest %d: %v", i, err)
		}
	}

	if got := attributions(t, st, alice); len(got) != 2 {
		t.Errorf("stored %d deltas after three deliveries of one report, want 2", len(got))
	}
	if used := usageOf(t, st, alice); used != 800 {
		t.Errorf("usage = %d after three deliveries, want 800", used)
	}
}
