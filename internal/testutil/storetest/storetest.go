// Package storetest hands tests a migrated store without paying for the
// migrations every time.
//
// store.Open runs goose.Up, and that costs 185ms of the 187ms a cold open
// takes -- a warm open, against a database whose migrations have already been
// applied, is 2.3ms. Every test that wanted its own database was therefore
// re-executing all 27 migrations, and internal/panel/httpapi alone opens 212 of
// them: 39 seconds of one package's 125, spent building the same schema over
// and over.
//
// That is what pushed the package past `go test`'s 10-minute per-package
// default under -race on CI. Raising the timeout stopped the bleeding; this
// removes the cause.
//
// The schema is built ONCE per test binary and copied per test. Copying is not
// sharing: each test still gets its own file, its own connections and its own
// isolation, so nothing about test independence changes. Only the schema
// construction is amortised.
package storetest

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/amyrm/antimage/internal/panel/store"
)

var (
	templateOnce sync.Once
	templateDB   *store.Store
	templatePath string
	templateErr  error
)

// template opens the one migrated database this process copies from.
//
// It is deliberately never closed. Its lifetime is the test binary's, and a
// t.Cleanup on whichever test happened to be first would close it out from
// under every later one.
func template() (*store.Store, error) {
	templateOnce.Do(func() {
		// Not t.TempDir(): that is removed when the owning test ends, and this
		// outlives every individual test.
		dir, err := mkTempDir()
		if err != nil {
			templateErr = err
			return
		}
		templatePath = filepath.Join(dir, "template.db")
		templateDB, templateErr = store.Open(templatePath)
		if templateErr != nil {
			return
		}

		// Fold the write-ahead log into the database file.
		//
		// This is what makes copying the single file correct. The store runs in
		// WAL mode, so immediately after migrating, the schema lives almost
		// entirely in template.db-wal -- the main file is 4KB and the sidecar
		// is 1.2MB. Copying the main file alone at that point yields an EMPTY
		// database, and the failure would surface later as "no such table" in
		// whichever test drew the short straw.
		//
		// TRUNCATE checkpoints every frame back into the database and empties
		// the log, so the file becomes self-contained (4KB -> 636KB, log -> 0).
		if _, err := templateDB.Read().Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			templateErr = err
		}
	})
	return templateDB, templateErr
}

// New returns a migrated store backed by a fresh database file.
//
// The file is a byte copy of the template, which is sound only because of two
// properties the template is built to have: it is checkpointed, so the whole
// database really is in the one file; and it is written exactly once, during
// init, so nothing can be mid-write while a copy is taken. SQLite's own
// VACUUM INTO would be correct without relying on either, but it costs 31ms
// against 0.5ms for the copy, and the whole point here is the per-test cost.
func New(t *testing.T) *store.Store {
	t.Helper()
	s, err := OpenCopy(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// OpenCopy is a drop-in replacement for store.Open in tests.
//
// It keeps store.Open's exact signature so a test can swap one identifier and
// change nothing else -- which matters, because the call site usually declares
// the `err` that the following lines go on to reuse. Rewriting those call sites
// into a form that returns only a store would break that, and mechanically
// repairing it across thirty files is how a "speed up the tests" change turns
// into a change nobody can review.
func OpenCopy(path string) (*store.Store, error) {
	if _, err := template(); err != nil {
		return nil, fmt.Errorf("build schema template: %w", err)
	}

	body, err := os.ReadFile(templatePath)
	if err != nil {
		return nil, fmt.Errorf("read schema template: %w", err)
	}
	// A template this small means the checkpoint did not happen and the schema
	// is still in the log. Failing here names the cause; letting it through
	// surfaces later as "no such table" in an unrelated test.
	if len(body) < 64*1024 {
		return nil, fmt.Errorf("schema template is only %d bytes, so it holds "+
			"no schema; the WAL checkpoint did not take effect", len(body))
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return nil, fmt.Errorf("write store copy: %w", err)
	}

	// A warm open: goose reads goose_db_version, finds every migration already
	// applied, and does nothing.
	return store.Open(path)
}

// mkTempDir creates a directory that outlives any single test.
func mkTempDir() (string, error) { return os.MkdirTemp("", "antimage-schema-*") }
