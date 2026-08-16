package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestPragmasApplied(t *testing.T) {
	s := openTemp(t)
	var journal string
	if err := s.Read().QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if journal != "wal" {
		t.Errorf("journal_mode = %q, want wal", journal)
	}
	var fk int
	if err := s.Read().QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("query foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}
}

func TestWriteRollsBackOnError(t *testing.T) {
	s := openTemp(t)
	wantErr := sql.ErrNoRows
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES ('k','v')`); err != nil {
			return err
		}
		return wantErr
	})
	if err == nil {
		t.Fatal("Write returned nil, want the callback error")
	}
	var n int
	if err := s.Read().QueryRow(`SELECT count(*) FROM settings`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("settings has %d rows after rollback, want 0", n)
	}
}

func TestConcurrentWritesAreSerialized(t *testing.T) {
	s := openTemp(t)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := s.Write(context.Background(), func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, 'v')`, i)
				return err
			})
			if err != nil {
				t.Errorf("concurrent write %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	var n int
	if err := s.Read().QueryRow(`SELECT count(*) FROM settings`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 50 {
		t.Errorf("got %d rows, want 50 — writes were lost or contended", n)
	}
}
