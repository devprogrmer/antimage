package storetest

import (
	"testing"
	"time"
)

// The copy must carry the full schema, or tests would fail in ways that look
// like application bugs.
func TestCopyHasTheMigratedSchema(t *testing.T) {
	s := New(t)
	var version int64
	if err := s.Read().QueryRow(`SELECT MAX(version_id) FROM goose_db_version`).Scan(&version); err != nil {
		t.Fatalf("goose version: %v", err)
	}
	if version < 27 {
		t.Errorf("schema version = %d, want >= 27", version)
	}
	// A column from the newest migration, so a stale template is caught.
	var coef int64
	if err := s.Read().QueryRow(
		`SELECT COALESCE(MIN(usage_coefficient), 10000) FROM nodes`).Scan(&coef); err != nil {
		t.Fatalf("usage_coefficient missing from the copy: %v", err)
	}
}

// Copies must be INDEPENDENT. If they shared a file, one test's writes would
// appear in another's and the isolation the per-test database exists for would
// be gone.
func TestCopiesAreIndependent(t *testing.T) {
	a, b := New(t), New(t)
	if _, err := a.Read().Exec(
		`INSERT INTO nodes (name, address, created_at) VALUES ('only-in-a', '127.0.0.1', 1)`); err != nil {
		t.Fatalf("write to a: %v", err)
	}
	var n int
	if err := b.Read().QueryRow(
		`SELECT COUNT(*) FROM nodes WHERE name = 'only-in-a'`).Scan(&n); err != nil {
		t.Fatalf("read b: %v", err)
	}
	if n != 0 {
		t.Error("a write to one store was visible in another; the copies share a file")
	}
}

// And the copy must start empty -- the template carries schema, never rows.
func TestCopyStartsEmpty(t *testing.T) {
	s := New(t)
	for _, table := range []string{"nodes", "subjects", "usage_deltas", "resellers"} {
		var n int
		if err := s.Read().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s has %d rows in a fresh store", table, n)
		}
	}
}

// The point of the exercise.
func TestCopyIsFasterThanMigrating(t *testing.T) {
	New(t) // warm the template so it is not counted

	const n = 20
	start := time.Now()
	for i := 0; i < n; i++ {
		New(t)
	}
	mean := time.Since(start) / n
	t.Logf("storetest.New mean = %v (a cold store.Open is ~187ms)", mean)
	if mean > 60*time.Millisecond {
		t.Errorf("mean %v is not meaningfully cheaper than running the migrations", mean)
	}
}
