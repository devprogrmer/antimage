package subjects

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/shared/secrets"
)

func newFixture(t *testing.T) (*store.Store, *secrets.Box, int64, int64) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	key := make([]byte, secrets.KeySize)
	for i := range key {
		key[i] = byte(i + 3)
	}
	box, err := secrets.NewBox(key)
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}

	var nodeID, serviceID int64
	err = s.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`INSERT INTO nodes (name, address, created_at) VALUES ('n1','1.1.1.1',?)`, 1_700_000_000)
		if err != nil {
			return err
		}
		nodeID, _ = res.LastInsertId()
		res, err = tx.Exec(
			`INSERT INTO services (node_id, adapter_kind, params, enabled, created_at)
			 VALUES (?, 'xray', '{"protocol":"vless","port":443}', 1, ?)`, nodeID, 1_700_000_000)
		if err != nil {
			return err
		}
		serviceID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return s, box, nodeID, serviceID
}

func createSubject(
	t *testing.T, s *store.Store, box *secrets.Box, name string, svcID int64,
	expires *time.Time, now time.Time,
) int64 {
	t.Helper()
	st := NewStore(s, box, func() time.Time { return now })
	var id int64
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		id, err = st.Create(context.Background(), tx, CreateInput{
			Name: name, ServiceIDs: []int64{svcID}, ExpiresAt: expires,
		})
		return err
	})
	if err != nil {
		t.Fatalf("create subject %s: %v", name, err)
	}
	return id
}

func subjectRow(t *testing.T, s *store.Store, id int64) (enabled int, expiredAt sql.NullInt64) {
	t.Helper()
	if err := s.Read().QueryRow(
		`SELECT enabled, expired_at FROM subjects WHERE id = ?`, id).Scan(&enabled, &expiredAt); err != nil {
		t.Fatalf("read subject %d: %v", id, err)
	}
	return
}

func nodeRevision(t *testing.T, s *store.Store, nodeID int64) int64 {
	t.Helper()
	var rev int64
	if err := s.Read().QueryRow(
		`SELECT desired_revision FROM nodes WHERE id = ?`, nodeID).Scan(&rev); err != nil {
		t.Fatalf("read revision: %v", err)
	}
	return rev
}

// The core of decision 2: expiry retires the subject, stamps when, and bumps
// the node's revision so the agent is woken rather than waiting for some
// unrelated change.
func TestSweepExpiresStampsAndBumpsTheRevision(t *testing.T) {
	s, box, nodeID, svcID := newFixture(t)
	base := time.Unix(1_700_000_000, 0).UTC()
	expiry := base.Add(time.Hour)
	id := createSubject(t, s, box, "temp", svcID, &expiry, base)

	revBefore := nodeRevision(t, s, nodeID)

	var notified []int64
	sw := NewSweeper(s, func() time.Time { return expiry.Add(time.Second) },
		func(nodeID, _ int64) { notified = append(notified, nodeID) },
		nodes.WithUnsealer(box))

	n, err := sw.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("swept %d subjects, want 1", n)
	}

	enabled, expiredAt := subjectRow(t, s, id)
	if enabled != 0 {
		t.Error("an expired subject is still enabled")
	}
	if !expiredAt.Valid {
		t.Error("expired_at was not stamped, so an operator cannot tell when it happened")
	}
	if rev := nodeRevision(t, s, nodeID); rev <= revBefore {
		t.Errorf("revision did not move: %d -> %d; the node would keep serving them", revBefore, rev)
	}
	if len(notified) != 1 || notified[0] != nodeID {
		t.Errorf("notified = %v, want the affected node woken", notified)
	}
}

// A subject that has not reached its expiry must be left alone.
func TestSweepLeavesUnexpiredSubjectsAlone(t *testing.T) {
	s, box, nodeID, svcID := newFixture(t)
	base := time.Unix(1_700_000_000, 0).UTC()
	expiry := base.Add(24 * time.Hour)
	id := createSubject(t, s, box, "future", svcID, &expiry, base)
	revBefore := nodeRevision(t, s, nodeID)

	sw := NewSweeper(s, func() time.Time { return base.Add(time.Hour) }, nil, nodes.WithUnsealer(box))
	n, err := sw.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if n != 0 {
		t.Fatalf("swept %d subjects, want 0", n)
	}
	if enabled, _ := subjectRow(t, s, id); enabled != 1 {
		t.Error("an unexpired subject was disabled")
	}
	if rev := nodeRevision(t, s, nodeID); rev != revBefore {
		t.Errorf("revision moved for a no-op sweep: %d -> %d", revBefore, rev)
	}
}

// A subject with no expiry never expires.
func TestSweepIgnoresSubjectsWithNoExpiry(t *testing.T) {
	s, box, _, svcID := newFixture(t)
	base := time.Unix(1_700_000_000, 0).UTC()
	id := createSubject(t, s, box, "permanent", svcID, nil, base)

	sw := NewSweeper(s, func() time.Time { return base.Add(10 * 365 * 24 * time.Hour) },
		nil, nodes.WithUnsealer(box))
	if n, err := sw.Sweep(context.Background()); err != nil || n != 0 {
		t.Fatalf("swept %d (err %v), want 0", n, err)
	}
	if enabled, _ := subjectRow(t, s, id); enabled != 1 {
		t.Error("a subject with no expiry was retired")
	}
}

// Idempotency: the sweeper runs on a timer, so re-expiring the same person
// forever would flood the audit log and bump revisions endlessly.
func TestSweepIsIdempotent(t *testing.T) {
	s, box, nodeID, svcID := newFixture(t)
	base := time.Unix(1_700_000_000, 0).UTC()
	expiry := base.Add(time.Hour)
	createSubject(t, s, box, "temp", svcID, &expiry, base)

	after := expiry.Add(time.Second)
	sw := NewSweeper(s, func() time.Time { return after }, nil, nodes.WithUnsealer(box))

	if n, _ := sw.Sweep(context.Background()); n != 1 {
		t.Fatalf("first sweep took %d", n)
	}
	revAfterFirst := nodeRevision(t, s, nodeID)

	for i := 0; i < 5; i++ {
		n, err := sw.Sweep(context.Background())
		if err != nil {
			t.Fatalf("sweep %d: %v", i, err)
		}
		if n != 0 {
			t.Fatalf("sweep %d re-expired %d subjects", i, n)
		}
	}
	if rev := nodeRevision(t, s, nodeID); rev != revAfterFirst {
		t.Errorf("repeated sweeps moved the revision: %d -> %d", revAfterFirst, rev)
	}

	var audits int
	_ = s.Read().QueryRow(
		`SELECT count(*) FROM audit_log WHERE action = 'subject.expired'`).Scan(&audits)
	if audits != 1 {
		t.Errorf("audit rows = %d, want exactly 1", audits)
	}
}

// Expiry must be audited, or an operator cannot explain why a user stopped
// working.
func TestExpiryIsAudited(t *testing.T) {
	s, box, _, svcID := newFixture(t)
	base := time.Unix(1_700_000_000, 0).UTC()
	expiry := base.Add(time.Hour)
	id := createSubject(t, s, box, "temp", svcID, &expiry, base)

	sw := NewSweeper(s, func() time.Time { return expiry.Add(time.Second) },
		nil, nodes.WithUnsealer(box))
	if _, err := sw.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	var actorType, action string
	var target sql.NullInt64
	if err := s.Read().QueryRow(
		`SELECT actor_type, action, target_id FROM audit_log
		  WHERE action = 'subject.expired' ORDER BY id DESC LIMIT 1`).
		Scan(&actorType, &action, &target); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if actorType != "system" {
		t.Errorf("actor_type = %q, want system", actorType)
	}
	if !target.Valid || target.Int64 != id {
		t.Errorf("target_id = %v, want %d", target, id)
	}
}

// The expired subject must actually disappear from the document, which is the
// mechanism the whole decision rests on.
//
// The expiry is set relative to REAL time, not to the fixed timestamp the other
// tests use, because BuildDesiredSnapshot reads the wall clock for its own
// expiry predicate while the sweeper takes an injected one. That difference is
// deliberate and safe -- the builder excluding an already-expired subject is
// the primary enforcement, and the sweeper is the promptness half -- but a test
// that mixed the two clocks would prove nothing.
func TestExpiredSubjectLeavesTheDesiredDocument(t *testing.T) {
	s, box, nodeID, svcID := newFixture(t)
	realNow := time.Now().UTC()
	expiry := realNow.Add(24 * time.Hour)
	createSubject(t, s, box, "temp", svcID, &expiry, realNow)

	count := func() int {
		var n int
		err := s.Write(context.Background(), func(tx *sql.Tx) error {
			snap, err := nodes.BuildDesiredSnapshot(context.Background(), tx, nodeID,
				nodes.WithUnsealer(box))
			if err != nil {
				return err
			}
			n = len(snap.Document.Subjects)
			return nil
		})
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		return n
	}

	if got := count(); got != 1 {
		t.Fatalf("before expiry the document carries %d subjects, want 1", got)
	}

	// Sweep with a clock past the expiry. The sweeper disables the subject, so
	// the builder excludes it regardless of its own clock.
	sw := NewSweeper(s, func() time.Time { return expiry.Add(time.Second) },
		nil, nodes.WithUnsealer(box))
	n, err := sw.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("swept %d, want 1", n)
	}
	if got := count(); got != 0 {
		t.Errorf("after expiry the document still carries %d subjects", got)
	}
}
