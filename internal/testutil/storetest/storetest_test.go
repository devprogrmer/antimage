package storetest

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
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

// The point of the exercise: copying must beat migrating.
//
// Both sides are measured HERE, in this process, on this machine. An earlier
// version asserted an absolute threshold -- "mean under 60ms" -- and that is a
// measurement of the hardware, not of the optimisation. It passed locally at
// 3.5ms and failed CI at 73.7ms under -race on a shared runner, where a cold
// open is correspondingly slower too. The ratio is the claim; the milliseconds
// are not.
//
// The margin is deliberately loose. This exists to catch the template being
// removed or silently stopping working, which would put the cost back to a full
// migration run -- a change of more than an order of magnitude. It is not a
// benchmark, and tightening it would only buy flakes.
func TestCopyIsCheaperThanMigrating(t *testing.T) {
	New(t) // build the template first, so its cost lands on neither side

	const n = 5

	// Cold: what every test used to pay. store.Open runs goose.Up.
	start := time.Now()
	for i := 0; i < n; i++ {
		s, err := store.Open(filepath.Join(t.TempDir(), "cold.db"))
		if err != nil {
			t.Fatalf("cold open: %v", err)
		}
		_ = s.Close()
	}
	cold := time.Since(start) / n

	// Warm: the copy path.
	start = time.Now()
	for i := 0; i < n; i++ {
		New(t)
	}
	warm := time.Since(start) / n

	t.Logf("cold store.Open = %v, storetest.New = %v (%.1fx cheaper)",
		cold, warm, float64(cold)/float64(warm))

	if warm*3 > cold {
		t.Errorf("storetest.New (%v) is not meaningfully cheaper than a cold "+
			"store.Open (%v); the schema template is not doing its job", warm, cold)
	}
}
