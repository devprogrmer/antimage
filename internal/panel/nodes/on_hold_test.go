package nodes

import (
	"context"
	"database/sql"
	"testing"

	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/testutil/storetest"
)

// on_hold activation: a subject sold without a start date begins its plan the
// first time it actually carries traffic.
//
// The trigger lives on the usage-ingest path because usage is the only signal
// every adapter reports. active_connections would work for the protocols whose
// device tracking is wired and silently never fire for the others, which is a
// worse failure than none: the customer would be served indefinitely on a plan
// that never started and never expired.

const thirtyDays = int64(30 * 24 * 60 * 60)

func onHoldSubject(t *testing.T, st *store.Store, seconds int64) (nodeID, subjectID int64) {
	t.Helper()
	ctx := context.Background()
	err := st.Write(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, created_at) VALUES ('n1', '127.0.0.1', 1000)`)
		if err != nil {
			return err
		}
		nodeID, _ = r.LastInsertId()

		r, err = tx.ExecContext(ctx,
			`INSERT INTO subjects (name, enabled, quota_used_bytes, on_hold_seconds,
			                       status_changed_at, created_at)
			 VALUES ('newcomer', 1, 0, ?, 1000, 1000)`, seconds)
		if err != nil {
			return err
		}
		subjectID, _ = r.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("seed on-hold subject: %v", err)
	}
	return nodeID, subjectID
}

func holdState(t *testing.T, st *store.Store, id int64) (onHold, expiresAt, statusChanged sql.NullInt64) {
	t.Helper()
	err := st.Read().QueryRowContext(context.Background(),
		`SELECT on_hold_seconds, expires_at, status_changed_at FROM subjects WHERE id = ?`,
		id).Scan(&onHold, &expiresAt, &statusChanged)
	if err != nil {
		t.Fatalf("read hold state: %v", err)
	}
	return
}

func TestFirstUseStartsTheClock(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	nodeID, subjectID := onHoldSubject(t, st, thirtyDays)

	const now = int64(5000)
	err := IngestUsageReport(ctx, st, nodeID, 1, []UsageDelta{
		{SubjectID: subjectID, UplinkBytes: 100, DownlinkBytes: 200},
	}, now)
	if err != nil {
		t.Fatalf("IngestUsageReport: %v", err)
	}

	onHold, expires, changed := holdState(t, st, subjectID)
	if onHold.Valid {
		t.Error("on_hold_seconds survived activation, so the subject could start twice")
	}
	if !expires.Valid || expires.Int64 != now+thirtyDays {
		t.Errorf("expires_at = %v, want %d: the plan must run its full length "+
			"from first use, not from when it was sold", expires, now+thirtyDays)
	}
	if !changed.Valid || changed.Int64 != now {
		t.Errorf("status_changed_at = %v, want %d", changed, now)
	}
}

// A node reports a subject as soon as it is configured, before anyone connects.
// Treating that as first use would start every customer's clock on delivery --
// exactly the loss on-hold exists to prevent.
func TestAZeroByteReportDoesNotStartTheClock(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	nodeID, subjectID := onHoldSubject(t, st, thirtyDays)

	err := IngestUsageReport(ctx, st, nodeID, 1, []UsageDelta{
		{SubjectID: subjectID, UplinkBytes: 0, DownlinkBytes: 0},
	}, 5000)
	if err != nil {
		t.Fatalf("IngestUsageReport: %v", err)
	}

	onHold, expires, _ := holdState(t, st, subjectID)
	if !onHold.Valid {
		t.Error("a zero-byte report started the plan; the subject was configured " +
			"on the node but nobody had used it yet")
	}
	if expires.Valid {
		t.Errorf("expires_at was set to %v by a report carrying no traffic", expires)
	}
}

// Activation is one-way. A later report must not push the expiry further out,
// or a customer who keeps using the service never expires at all.
func TestActivationHappensOnceOnly(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	nodeID, subjectID := onHoldSubject(t, st, thirtyDays)

	if err := IngestUsageReport(ctx, st, nodeID, 1, []UsageDelta{
		{SubjectID: subjectID, UplinkBytes: 10, DownlinkBytes: 0},
	}, 5000); err != nil {
		t.Fatalf("first report: %v", err)
	}
	_, firstExpiry, _ := holdState(t, st, subjectID)

	// A week later, still using it.
	if err := IngestUsageReport(ctx, st, nodeID, 2, []UsageDelta{
		{SubjectID: subjectID, UplinkBytes: 10, DownlinkBytes: 0},
	}, 5000+7*24*60*60); err != nil {
		t.Fatalf("second report: %v", err)
	}

	_, secondExpiry, _ := holdState(t, st, subjectID)
	if secondExpiry.Int64 != firstExpiry.Int64 {
		t.Errorf("expires_at moved from %d to %d on a later report: a subject "+
			"who keeps using the service would never expire",
			firstExpiry.Int64, secondExpiry.Int64)
	}
}

// An ordinary subject must be untouched by any of this.
func TestUsageDoesNotDisturbASubjectThatIsNotOnHold(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	nodeID, alice, _ := twoSubjects(t, st)

	// alice has a fixed expiry, as an ordinary sale does.
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE subjects SET expires_at = 9999 WHERE id = ?`, alice)
		return err
	}); err != nil {
		t.Fatalf("set expiry: %v", err)
	}

	if err := IngestUsageReport(ctx, st, nodeID, 1, []UsageDelta{
		{SubjectID: alice, UplinkBytes: 500, DownlinkBytes: 500},
	}, 5000); err != nil {
		t.Fatalf("IngestUsageReport: %v", err)
	}

	onHold, expires, _ := holdState(t, st, alice)
	if onHold.Valid {
		t.Error("usage put an ordinary subject on hold")
	}
	if expires.Int64 != 9999 {
		t.Errorf("expires_at = %d, want 9999: usage moved the expiry of a "+
			"subject that was never on hold", expires.Int64)
	}
}
