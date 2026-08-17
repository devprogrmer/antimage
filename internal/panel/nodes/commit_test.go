package nodes

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/store"
)

func addService(port int) func(*sql.Tx) error {
	return func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO services (node_id, adapter_kind, params, enabled, created_at)
			 SELECT id, 'stub', ?, 1, ? FROM nodes LIMIT 1`,
			`{"port":`+itoa(port)+`}`, time.Now().Unix())
		return err
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func commit(t *testing.T, s *store.Store, nodeID int64, reason string, mutate func(*sql.Tx) error) *CommitResult {
	t.Helper()
	res, err := CommitNodeChange(context.Background(), s, nodeID,
		audit.SystemActor("test"), "req-1", reason, mutate)
	if err != nil {
		t.Fatalf("CommitNodeChange(%s): %v", reason, err)
	}
	return res
}

func TestFirstChangeCreatesRevisionOne(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	res := commit(t, s, nodeID, "add service", addService(443))
	if !res.Changed {
		t.Fatal("Changed = false for a real change")
	}
	if res.Revision != 1 {
		t.Errorf("Revision = %d, want 1", res.Revision)
	}

	var stored string
	if err := s.Read().QueryRow(
		`SELECT doc_sha256 FROM node_revisions WHERE node_id = ? AND revision = 1`, nodeID,
	).Scan(&stored); err != nil {
		t.Fatalf("read revision row: %v", err)
	}
	if stored != res.SHA256 {
		t.Errorf("stored hash %s != returned %s — invariant 4 broken", stored, res.SHA256)
	}
}

// Invariant 2: a mutation that changes nothing semantically must not bump.
func TestNoOpMutationCreatesNoRevision(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	first := commit(t, s, nodeID, "add service", addService(443))

	noop := commit(t, s, nodeID, "touch nothing", func(tx *sql.Tx) error { return nil })
	if noop.Changed {
		t.Error("Changed = true for a no-op mutation")
	}
	if noop.Revision != first.Revision {
		t.Errorf("revision moved from %d to %d on a no-op", first.Revision, noop.Revision)
	}

	var n int
	if err := s.Read().QueryRow(
		`SELECT count(*) FROM node_revisions WHERE node_id = ?`, nodeID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("node_revisions has %d rows, want 1", n)
	}
}

// A write that touches rows but leaves the document identical is still a no-op.
func TestSemanticallyIdenticalUpdateCreatesNoRevision(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	commit(t, s, nodeID, "add service", addService(443))

	res := commit(t, s, nodeID, "rewrite same params", func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`UPDATE services SET params = '{"port":443}' WHERE node_id = ?`, nodeID)
		return err
	})
	if res.Changed {
		t.Error("rewriting identical params bumped the revision")
	}
}

func TestRevisionsIncrementByOne(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	for i, port := range []int{443, 8443, 9443} {
		res := commit(t, s, nodeID, "add", addService(port))
		if want := int64(i + 1); res.Revision != want {
			t.Fatalf("commit %d gave revision %d, want %d", i, res.Revision, want)
		}
	}
	var desired int64
	if err := s.Read().QueryRow(
		`SELECT desired_revision FROM nodes WHERE id = ?`, nodeID).Scan(&desired); err != nil {
		t.Fatalf("read desired_revision: %v", err)
	}
	if desired != 3 {
		t.Errorf("desired_revision = %d, want 3", desired)
	}
}

func TestFailedMutationLeavesNoRevisionAndNoRows(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	commit(t, s, nodeID, "add", addService(443))
	boom := errors.New("mutation exploded")

	_, err := CommitNodeChange(context.Background(), s, nodeID,
		audit.SystemActor("test"), "req-x", "will fail",
		func(tx *sql.Tx) error {
			if _, err := tx.Exec(
				`INSERT INTO services (node_id, adapter_kind, params, enabled, created_at)
				 VALUES (?, 'stub', '{"port":1}', 1, ?)`, nodeID, time.Now().Unix()); err != nil {
				return err
			}
			return boom
		})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}

	var revisions, services int
	_ = s.Read().QueryRow(`SELECT count(*) FROM node_revisions WHERE node_id = ?`, nodeID).Scan(&revisions)
	_ = s.Read().QueryRow(`SELECT count(*) FROM services WHERE node_id = ?`, nodeID).Scan(&services)
	if revisions != 1 {
		t.Errorf("node_revisions = %d, want 1", revisions)
	}
	if services != 1 {
		t.Errorf("services = %d, want 1 — the failed insert was not rolled back", services)
	}
}

func TestCommitWritesAuditRowInSameTransaction(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	commit(t, s, nodeID, "add service", addService(443))

	var action, result string
	if err := s.Read().QueryRow(
		`SELECT action, result FROM audit_log ORDER BY id DESC LIMIT 1`,
	).Scan(&action, &result); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if action != "node.revision" || result != "ok" {
		t.Errorf("audit = %s/%s, want node.revision/ok", action, result)
	}
}

// TestStoredHashMatchesIndependentRecompute guards invariant 4 against a
// regression the returned CommitResult cannot see: a bug that stores the
// pre-bump snapshot's hash (describing revision N) under a row labelled N+1,
// while still returning that same pre-bump hash from CommitNodeChange. Such a
// bug is invisible to TestFirstChangeCreatesRevisionOne, because that test
// only checks stored == res.SHA256, and a consistent substitution satisfies
// that trivially. This test instead recomputes the hash independently, from
// a fresh transaction that reads the already-bumped desired_revision straight
// off the table, so it depends on neither the stored value nor res.SHA256.
func TestStoredHashMatchesIndependentRecompute(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	res := commit(t, s, nodeID, "add service", addService(443))

	var stored string
	if err := s.Read().QueryRow(
		`SELECT doc_sha256 FROM node_revisions WHERE node_id = ? AND revision = ?`,
		nodeID, res.Revision,
	).Scan(&stored); err != nil {
		t.Fatalf("read revision row: %v", err)
	}

	var recomputed string
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		snap, err := BuildDesiredSnapshot(context.Background(), tx, nodeID)
		if err != nil {
			return err
		}
		recomputed = snap.SHA256
		return nil
	})
	if err != nil {
		t.Fatalf("independent recompute: %v", err)
	}

	if stored != recomputed {
		t.Errorf("revision %d: stored hash %s != independently recomputed hash %s — "+
			"stored doc_sha256 does not describe the document at its own revision",
			res.Revision, stored, recomputed)
	}
}

func TestMonotonicTriggerRejectsManualGap(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	commit(t, s, nodeID, "add", addService(443))

	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO node_revisions
			   (node_id, revision, created_at, actor_type, actor_label, reason, doc_sha256)
			 VALUES (?, 99, ?, 'system', 'manual', 'gap', 'deadbeef')`,
			nodeID, time.Now().Unix())
		return err
	})
	if err == nil {
		t.Fatal("the monotonicity trigger allowed a revision gap")
	}
}
