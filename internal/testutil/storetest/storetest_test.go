package storetest

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

// The copy must carry the full schema, or tests fail in ways that look like
// application bugs.
//
// "Full" is defined against a database that store.Open just migrated, not
// against a hardcoded version number and a named column. The first version of
// this test asserted `version >= 27` and read `usage_coefficient`, which tied a
// property of the COPY MECHANISM to whichever migrations happened to exist the
// day it was written. When migrations 26 and 27 were later reverted, this test
// failed -- reporting a broken template when the template was fine and the
// migration set had simply changed underneath it.
//
// Comparing against a freshly migrated database says the thing actually meant:
// whatever the migrations produce, the copy has all of it.
func TestCopyHasTheMigratedSchema(t *testing.T) {
	copied := New(t)

	reference, err := store.Open(filepath.Join(t.TempDir(), "reference.db"))
	if err != nil {
		t.Fatalf("open reference store: %v", err)
	}
	defer func() { _ = reference.Close() }()

	if got, want := schemaVersion(t, copied), schemaVersion(t, reference); got != want {
		t.Errorf("copy is at schema version %d, a freshly migrated database is at %d",
			got, want)
	}

	gotTables := tableNames(t, copied)
	wantTables := tableNames(t, reference)
	if len(gotTables) != len(wantTables) {
		t.Fatalf("copy has %d tables, a freshly migrated database has %d:\n copy: %v\n want: %v",
			len(gotTables), len(wantTables), gotTables, wantTables)
	}
	for i := range wantTables {
		if gotTables[i] != wantTables[i] {
			t.Errorf("table %d: copy has %q, want %q", i, gotTables[i], wantTables[i])
		}
	}

	// Columns too, not just table names: a table that arrived complete and one
	// that arrived missing a column added by a later ALTER both pass a name
	// comparison, and only the second breaks the tests that use it.
	for _, table := range wantTables {
		got, want := columnNames(t, copied, table), columnNames(t, reference, table)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("table %s: copy has columns %v, want %v", table, got, want)
		}
	}
}

func schemaVersion(t *testing.T, s *store.Store) int64 {
	t.Helper()
	var v int64
	if err := s.Read().QueryRow(`SELECT MAX(version_id) FROM goose_db_version`).Scan(&v); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	return v
}

func tableNames(t *testing.T, s *store.Store) []string {
	t.Helper()
	rows, err := s.Read().Query(
		`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate tables: %v", err)
	}
	return out
}

func columnNames(t *testing.T, s *store.Store, table string) []string {
	t.Helper()
	rows, err := s.Read().Query(`SELECT name FROM pragma_table_info(?) ORDER BY name`, table)
	if err != nil {
		t.Fatalf("list columns of %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan column name: %v", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns of %s: %v", table, err)
	}
	return out
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
