package subjects

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/shared/secrets"
)

// The subject tables must be STRICT, and opening an existing database must not
// re-run migrations.
//
// STRICT is not cosmetic here: without it SQLite would accept a string into
// subjects.expires_at, and the expiry sweeper compares that column numerically.
// A silently coerced value would leave a subject that never expires.
func TestSubjectSchemaIsStrictAndMigrationsAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	var version int64
	if err := s.Read().QueryRow(
		`SELECT max(version_id) FROM goose_db_version`).Scan(&version); err != nil {
		t.Fatalf("read goose version: %v", err)
	}
	t.Logf("goose version = %d", version)

	for _, table := range []string{"subjects", "subject_services", "subject_credentials"} {
		var ddl string
		if err := s.Read().QueryRow(
			`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
			t.Fatalf("table %s missing: %v", table, err)
		}
		if !strings.Contains(strings.ToUpper(ddl), "STRICT") {
			t.Errorf("table %s is not STRICT", table)
		}
		t.Logf("%s: STRICT ok", table)
	}

	// Foreign keys must be enforced, or deleting a subject would orphan its
	// credentials instead of destroying them.
	var fk int
	if err := s.Read().QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("pragma: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}
	_ = s.Close()

	// Re-opening must be a no-op, not a re-application.
	s2, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = s2.Close() }()
	var version2 int64
	_ = s2.Read().QueryRow(`SELECT max(version_id) FROM goose_db_version`).Scan(&version2)
	if version2 != version {
		t.Errorf("reopen moved the schema version: %d -> %d", version, version2)
	}
	var rows int
	_ = s2.Read().QueryRow(`SELECT count(*) FROM goose_db_version`).Scan(&rows)
	t.Logf("reopen: version still %d, %d migration rows", version2, rows)
}

// SP2 decision 1 rests entirely on this: a credential must never be readable
// from the database file itself.
//
// The existing HTTP tests prove the API does not leak one in a response body.
// That is a different property from the one that matters when a backup, a
// stolen disk, or a support copy of the .db is what leaks -- which is the case
// sealing exists for. Nothing asserted this before.
func TestCredentialsAreSealedAtRest(t *testing.T) {
	s, box, _, svcID := newFixture(t)
	now := time.Now().UTC()
	id := createSubject(t, s, box, "alice", svcID, nil, now)

	st := NewStore(s, box, func() time.Time { return now })
	plain, err := st.Credential(context.Background(), id, "uuid")
	if err != nil {
		t.Fatalf("Credential: %v", err)
	}
	if plain == "" {
		t.Fatal("no credential was generated")
	}
	t.Logf("plaintext credential length = %d", len(plain))

	var stored []byte
	if err := s.Read().QueryRow(
		`SELECT value_enc FROM subject_credentials WHERE subject_id = ? AND kind = 'uuid'`,
		id).Scan(&stored); err != nil {
		t.Fatalf("read stored credential: %v", err)
	}
	if len(stored) == 0 {
		t.Fatal("nothing stored")
	}
	if strings.Contains(string(stored), plain) {
		t.Errorf("SECURITY: the %d-byte stored value contains the plaintext credential",
			len(stored))
	} else {
		t.Logf("stored blob is %d bytes and does not contain the plaintext", len(stored))
	}

	// A different box (wrong master key) must not be able to read it.
	var wrong [32]byte
	for i := range wrong {
		wrong[i] = 0xAB
	}
	other := NewStore(s, mustBox(t, wrong[:]), func() time.Time { return now })
	if got, err := other.Credential(context.Background(), id, "uuid"); err == nil {
		t.Errorf("SECURITY: a wrong master key decrypted the credential to %q", got)
	} else {
		t.Logf("wrong key correctly refused: %v", err)
	}
}

func mustBox(t *testing.T, key []byte) *secrets.Box {
	t.Helper()
	b, err := secrets.NewBox(key)
	if err != nil {
		t.Fatalf("box: %v", err)
	}
	return b
}
