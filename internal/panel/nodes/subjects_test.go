package nodes

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/shared/secrets"
)

// fixedNow freezes the expiry clock for the duration of a test.
func fixedNow(t *testing.T, at time.Time) {
	t.Helper()
	prev := nowUnix
	nowUnix = func() int64 { return at.Unix() }
	t.Cleanup(func() { nowUnix = prev })
}

func testBox(t *testing.T) *secrets.Box {
	t.Helper()
	key := make([]byte, secrets.KeySize)
	for i := range key {
		key[i] = byte(i + 7)
	}
	box, err := secrets.NewBox(key)
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}
	return box
}

// seedSubject inserts a subject, seals its credentials, and grants it a
// service. Returns the subject id.
func seedSubject(
	t *testing.T, s interface {
		Write(context.Context, func(*sql.Tx) error) error
	},
	box *secrets.Box, name string, serviceID int64, enabled bool, expiresAt *time.Time,
) int64 {
	t.Helper()
	var id int64
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		var exp any
		if expiresAt != nil {
			exp = expiresAt.Unix()
		}
		en := 0
		if enabled {
			en = 1
		}
		res, err := tx.Exec(
			`INSERT INTO subjects (name, enabled, expires_at, created_at, note)
			 VALUES (?,?,?,?,'')`, name, en, exp, 1_700_000_000)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return err
		}
		for kind, value := range map[string]string{
			"uuid":     "11111111-2222-3333-4444-555555555555",
			"password": "a-password-long-enough-to-pass",
		} {
			sealed, err := box.Seal([]byte(value))
			if err != nil {
				return err
			}
			if _, err := tx.Exec(
				`INSERT INTO subject_credentials (subject_id, kind, value_enc, rotation, created_at)
				 VALUES (?,?,?,0,?)`, id, kind, sealed, 1_700_000_000); err != nil {
				return err
			}
		}
		_, err = tx.Exec(
			`INSERT INTO subject_services (subject_id, service_id) VALUES (?,?)`, id, serviceID)
		return err
	})
	if err != nil {
		t.Fatalf("seed subject %s: %v", name, err)
	}
	return id
}

// seedService inserts a service on a node and returns its id.
func seedService(t *testing.T, s interface {
	Write(context.Context, func(*sql.Tx) error) error
}, nodeID int64) int64 {
	t.Helper()
	var id int64
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`INSERT INTO services (node_id, adapter_kind, params, enabled, created_at)
			 VALUES (?, 'stub', '{"port":443}', 1, ?)`, nodeID, 1_700_000_000)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("seed service: %v", err)
	}
	return id
}

// A node with no subjects must produce exactly the document SP1 produced.
// Populating a field that already serialised as null must not change any
// existing node's hash, or every node in a live fleet would report drift on
// upgrade.
func TestEmptySubjectListLeavesTheDocumentHashUnchanged(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	box := testBox(t)
	seedService(t, s, nodeID)

	var withoutOpt, withOpt string
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		a, err := BuildDesiredSnapshot(context.Background(), tx, nodeID)
		if err != nil {
			return err
		}
		b, err := BuildDesiredSnapshot(context.Background(), tx, nodeID, WithUnsealer(box))
		if err != nil {
			return err
		}
		withoutOpt, withOpt = a.SHA256, b.SHA256
		if !strings.Contains(string(a.Bytes), `"subjects":null`) {
			return errUnexpected(string(a.Bytes))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if withoutOpt != withOpt {
		t.Fatalf("hash changed merely by supplying an unsealer: %s vs %s", withoutOpt, withOpt)
	}
}

func errUnexpected(body string) error {
	return &unexpectedShape{body}
}

type unexpectedShape struct{ body string }

func (e *unexpectedShape) Error() string {
	return "document does not carry an explicit null subjects field: " + e.body
}

// An active subject appears with every credential it holds, unsealed.
func TestActiveSubjectAppearsWithUnsealedCredentials(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	box := testBox(t)
	svcID := seedService(t, s, nodeID)
	fixedNow(t, time.Unix(1_700_000_000, 0).UTC())
	id := seedSubject(t, s, box, "alice", svcID, true, nil)

	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		snap, err := BuildDesiredSnapshot(context.Background(), tx, nodeID, WithUnsealer(box))
		if err != nil {
			return err
		}
		if len(snap.Document.Subjects) != 1 {
			t.Fatalf("subjects = %d, want 1", len(snap.Document.Subjects))
		}
		sub := snap.Document.Subjects[0]
		if sub.ID != id {
			t.Errorf("subject id = %d, want %d", sub.ID, id)
		}
		if len(sub.Credentials) != 2 {
			t.Fatalf("credentials = %d, want 2", len(sub.Credentials))
		}
		// Ordered by kind so the canonical document is stable.
		if sub.Credentials[0].Kind != "password" || sub.Credentials[1].Kind != "uuid" {
			t.Errorf("credential order = %s,%s; want password,uuid",
				sub.Credentials[0].Kind, sub.Credentials[1].Kind)
		}
		if sub.Credentials[1].Value != "11111111-2222-3333-4444-555555555555" {
			t.Errorf("uuid did not round-trip through the seal: %q", sub.Credentials[1].Value)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
}

// Decision 2: expiry is enforced by omission from the desired document, so an
// expired subject simply stops being part of desired state and the ordinary
// convergence path removes them.
func TestExpiredSubjectIsOmittedFromTheDocument(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	box := testBox(t)
	svcID := seedService(t, s, nodeID)

	base := time.Unix(1_700_000_000, 0).UTC()
	expiry := base.Add(time.Hour)
	seedSubject(t, s, box, "temporary", svcID, true, &expiry)

	// Before expiry: present.
	fixedNow(t, base)
	assertSubjectCount(t, s, nodeID, box, 1, "before expiry")

	// After expiry: gone, without anyone having edited the subject row.
	fixedNow(t, expiry.Add(time.Second))
	assertSubjectCount(t, s, nodeID, box, 0, "after expiry")
}

func TestDisabledSubjectIsOmittedFromTheDocument(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	box := testBox(t)
	svcID := seedService(t, s, nodeID)
	fixedNow(t, time.Unix(1_700_000_000, 0).UTC())
	seedSubject(t, s, box, "suspended", svcID, false, nil)

	assertSubjectCount(t, s, nodeID, box, 0, "disabled subject")
}

func assertSubjectCount(t *testing.T, s interface {
	Write(context.Context, func(*sql.Tx) error) error
}, nodeID int64, box *secrets.Box, want int, what string) {
	t.Helper()
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		snap, err := BuildDesiredSnapshot(context.Background(), tx, nodeID, WithUnsealer(box))
		if err != nil {
			return err
		}
		if got := len(snap.Document.Subjects); got != want {
			t.Errorf("%s: subjects = %d, want %d", what, got, want)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("%s: build: %v", what, err)
	}
}

// Fail closed. If the master key is absent while subjects exist, building the
// document must FAIL rather than quietly producing one without them: a
// document that omits every subject would deprovision the entire node on the
// next convergence, and it would be hash-verified and audited as a success.
func TestMissingUnsealerFailsRatherThanDeprovisioning(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	box := testBox(t)
	svcID := seedService(t, s, nodeID)
	fixedNow(t, time.Unix(1_700_000_000, 0).UTC())
	seedSubject(t, s, box, "alice", svcID, true, nil)

	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := BuildDesiredSnapshot(context.Background(), tx, nodeID) // no unsealer
		return err
	})
	if err == nil {
		t.Fatal("built a document without the key, silently omitting every subject")
	}
	if !strings.Contains(err.Error(), "deprovision") {
		t.Errorf("err = %v, want it to name the consequence", err)
	}
}

// The wrong master key must fail the build too, rather than emitting garbage
// credentials that every client would then fail to authenticate with.
func TestWrongMasterKeyFailsTheBuild(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	box := testBox(t)
	svcID := seedService(t, s, nodeID)
	fixedNow(t, time.Unix(1_700_000_000, 0).UTC())
	seedSubject(t, s, box, "alice", svcID, true, nil)

	other := make([]byte, secrets.KeySize)
	for i := range other {
		other[i] = 0xAA
	}
	wrongBox, err := secrets.NewBox(other)
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}

	err = s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := BuildDesiredSnapshot(context.Background(), tx, nodeID, WithUnsealer(wrongBox))
		return err
	})
	if err == nil {
		t.Fatal("a document was built with the wrong master key")
	}
	if !strings.Contains(err.Error(), "master key") {
		t.Errorf("err = %v, want it to name the likely cause", err)
	}
}

// Invariant 3: the canonical document must be byte-identical across builds, so
// a node does not see a new hash and re-converge for no reason.
func TestSubjectDocumentIsDeterministic(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	box := testBox(t)
	svcID := seedService(t, s, nodeID)
	fixedNow(t, time.Unix(1_700_000_000, 0).UTC())
	for _, name := range []string{"carol", "alice", "bob"} {
		seedSubject(t, s, box, name, svcID, true, nil)
	}

	var first string
	for i := 0; i < 5; i++ {
		err := s.Write(context.Background(), func(tx *sql.Tx) error {
			snap, err := BuildDesiredSnapshot(context.Background(), tx, nodeID, WithUnsealer(box))
			if err != nil {
				return err
			}
			if first == "" {
				first = snap.SHA256
			} else if snap.SHA256 != first {
				t.Fatalf("build %d hashed differently: %s vs %s", i, snap.SHA256, first)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("build %d: %v", i, err)
		}
	}
}

// A subject granted a service on another node must not appear here.
func TestSubjectsAreScopedToTheirOwnNode(t *testing.T) {
	s, nodeA := newNodeFixture(t)
	box := testBox(t)
	svcA := seedService(t, s, nodeA)

	var nodeB int64
	if err := s.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`INSERT INTO nodes (name, address, created_at) VALUES ('other','2.2.2.2',?)`,
			1_700_000_000)
		if err != nil {
			return err
		}
		nodeB, err = res.LastInsertId()
		return err
	}); err != nil {
		t.Fatalf("seed node B: %v", err)
	}
	svcB := seedService(t, s, nodeB)

	fixedNow(t, time.Unix(1_700_000_000, 0).UTC())
	seedSubject(t, s, box, "on-a", svcA, true, nil)
	seedSubject(t, s, box, "on-b", svcB, true, nil)

	assertSubjectCount(t, s, nodeA, box, 1, "node A")
	assertSubjectCount(t, s, nodeB, box, 1, "node B")
}

// CommitNodeChange is what every service handler calls, and it rebuilds the
// desired document. If the unsealer does not reach it, the first subject
// created on a node makes every subsequent commit for that node fail forever:
// the builder correctly refuses to omit subjects it cannot unseal, but it must
// be given the means to unseal them. This pins the wiring, not the refusal.
func TestCommitStillWorksOnceASubjectExists(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	box := testBox(t)
	svcID := seedService(t, s, nodeID)
	fixedNow(t, time.Unix(1_700_000_000, 0).UTC())

	commit := func(reason string, opts ...SnapshotOption) error {
		_, err := CommitNodeChange(context.Background(), s, nodeID,
			audit.SystemActor("audit"), "req", reason,
			func(tx *sql.Tx) error { return nil }, opts...)
		return err
	}

	if err := commit("before any subject"); err != nil {
		t.Fatalf("commit with no subjects: %v", err)
	}

	seedSubject(t, s, box, "alice", svcID, true, nil)

	if err := commit("after a subject exists", WithUnsealer(box)); err != nil {
		t.Fatalf("commit with the unsealer supplied: %v", err)
	}
	// And the failure mode is still the safe one when it is NOT supplied.
	if err := commit("without the unsealer"); err == nil {
		t.Fatal("committed without an unsealer while subjects exist")
	}
}
