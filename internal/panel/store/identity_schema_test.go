package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// seedRole inserts the single role row admin inserts in this file need to
// satisfy admins.role_id's foreign key.
func seedRole(t *testing.T, tx *sql.Tx) {
	t.Helper()
	if _, err := tx.Exec(`INSERT INTO roles (id, name, is_builtin, permissions) VALUES (1, 'owner', 1, '[]')`); err != nil {
		t.Fatalf("seed role: %v", err)
	}
}

// TestUsernameUniquenessIsCaseInsensitive pins the admins_username_unique
// index's COLLATE NOCASE: 'alice' and 'Alice' must be treated as the same
// username for uniqueness purposes.
func TestUsernameUniquenessIsCaseInsensitive(t *testing.T) {
	s := openTemp(t)

	seedErr := s.Write(context.Background(), func(tx *sql.Tx) error {
		seedRole(t, tx)
		_, err := tx.Exec(`INSERT INTO admins (username, password_hash, role_id, created_at) VALUES ('alice', 'h', 1, 0)`)
		return err
	})
	if seedErr != nil {
		t.Fatalf("seed admin 'alice': %v", seedErr)
	}

	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO admins (username, password_hash, role_id, created_at) VALUES ('Alice', 'h', 1, 0)`)
		return err
	})
	if err == nil {
		t.Fatal("inserting 'Alice' after 'alice' succeeded, want a uniqueness violation")
	}
	if !strings.Contains(err.Error(), "UNIQUE") {
		t.Errorf("error = %q, want it to mention UNIQUE (unexpected failure mode)", err.Error())
	}

	var n int
	if err := s.Read().QueryRow(`SELECT count(*) FROM admins`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("admins has %d rows, want 1 (second insert should not have committed)", n)
	}
}

// TestUsernameLookupIsCaseInsensitive pins the admins.username column's
// COLLATE NOCASE: a lookup by a differently-cased username must still find
// the row. This is distinct from the uniqueness index above — a schema
// change that drops the column collation but keeps the index collation
// would fail this test while leaving the uniqueness test passing.
func TestUsernameLookupIsCaseInsensitive(t *testing.T) {
	s := openTemp(t)

	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		seedRole(t, tx)
		_, err := tx.Exec(`INSERT INTO admins (username, password_hash, role_id, created_at) VALUES ('alice', 'h', 1, 0)`)
		return err
	})
	if err != nil {
		t.Fatalf("seed admin: %v", err)
	}

	var id int64
	err = s.Read().QueryRow(`SELECT id FROM admins WHERE username = 'ALICE'`).Scan(&id)
	if err != nil {
		t.Fatalf("lookup by differently-cased username: %v", err)
	}
}
