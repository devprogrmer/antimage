# antimage SP1 — Control-Plane Spine Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the control-plane spine of antimage — authentication, RBAC, audit, node registry, mTLS enrollment, bootstrap, the adapter contract, health, and a UI shell — such that an operator can enroll a node, converge it against derived desired state, and observe drift, with no real protocol adapter yet.

**Architecture:** A Go monorepo producing three static binaries. The panel holds desired state in SQLite, derived from relational tables and versioned by a revision counter. Agents dial *out* to the panel over mTLS gRPC and hold a bidirectional stream; the panel pushes revision bumps and the agent reconciles by Observe → Plan → Apply through a protocol adapter. Nodes need no inbound port.

**Tech Stack:** Go 1.23+, SQLite (modernc.org/sqlite, pure Go, no cgo), goose migrations, chi router, gRPC + protobuf, argon2id, React 19 + TypeScript + Vite + Tailwind, TanStack Query.

**Spec:** `docs/superpowers/specs/2026-08-13-antimage-control-plane-design.md`. Read it before Task 1.

## Global Constraints

Every task's requirements implicitly include this section.

- **Go 1.23 or newer.** Module path `github.com/amyrm/antimage`.
- **SQLite driver is `modernc.org/sqlite`** (pure Go). No cgo anywhere — the binaries must cross-compile to `linux/amd64` and `linux/arm64` with `CGO_ENABLED=0`.
- **Target platforms:** Debian 11+ and Ubuntu 20.04+, amd64 and arm64. Refuse others with a clear message; never guess.
- **SQLite pragmas on every connection:** `journal_mode=WAL`, `foreign_keys=ON`, `busy_timeout=5000`.
- **All writes go through a single serialized writer.** SQLite permits exactly one.
- **Canonical serialization is RFC 8785 (JCS).** Desired-document types use **no `omitempty`** — every field always present, absent means explicit `null`. Arrays sort by a stable key before serialization.
- **argon2id parameters:** m=64 MB (65536 KiB), t=3, p=4, 16-byte salt, 32-byte output.
- **Session cookie:** `HttpOnly; Secure; SameSite=Strict`. 4-hour idle timeout, 7-day absolute lifetime.
- **Rate limits:** 5 failures per account / 15 min, 20 per source IP / 15 min. Backoff doubles from 1s to a 300s ceiling.
- **Enrollment tokens:** single-use, 30-minute TTL, bound to one node id.
- **Node certificates:** 1-year lifetime, auto-renew at the halfway mark. Revocation is a fingerprint allow-list, never a CRL.
- **Heartbeat every 30s. `Offline` after 3 missed intervals (90s).** Reconcile timer 5 min ± jitter. Reconnect backoff exponential with jitter, 60s cap.
- **`internal/node/adapter` must not import `internal/panel`.** Enforced by CI (Task 1).
- **UI uses logical CSS properties only** (`ms-`, `me-`, `ps-`, `pe-`, `text-start`). `ml-`, `pl-`, `left-`, `text-left` and literal strings in JSX fail the build.
- **`InsecureIgnoreHostKey` must appear nowhere.** CI greps for it.
- **No code, assets, schema, or docs copied from the reference projects.** Original implementations only.
- **Commit after every task.** Conventional Commits (`feat:`, `fix:`, `test:`, `chore:`, `docs:`).

---

## File Structure

**Phase A — foundations**
- `go.mod`, `Makefile`, `.golangci.yml`, `.github/workflows/ci.yml` — toolchain and gates
- `internal/panel/store/store.go` — DB open, pragmas, read/write handle split
- `internal/panel/store/migrations/*.sql` — goose migrations, one per schema concern
- `internal/shared/canonical/canonical.go` — RFC 8785 serialization + hashing
- `internal/shared/secrets/box.go` — master key load, AES-256-GCM seal/open

**Phase B — identity**
- `internal/panel/auth/password.go` — argon2id hash/verify (PHC encoding)
- `internal/panel/auth/session.go` — token mint, hash, lookup, revoke
- `internal/panel/auth/ratelimit.go` — per-account and per-IP backoff
- `internal/panel/auth/totp.go` — TOTP enrol/verify, recovery codes
- `internal/panel/rbac/perm.go` — permission keys, built-in role templates
- `internal/panel/rbac/authz.go` — `Check` chokepoint
- `internal/panel/rbac/scope.go` — `Scope` value passed into store queries
- `internal/panel/audit/audit.go` — transactional and best-effort writers

**Phase C — desired state**
- `internal/panel/nodes/document.go` — desired-document types (no `omitempty`)
- `internal/panel/nodes/snapshot.go` — `BuildDesiredSnapshot`
- `internal/panel/nodes/commit.go` — `CommitNodeChange`

**Phase D — adapter and agent**
- `internal/node/adapter/adapter.go` — the interface and its types
- `internal/node/adapter/stub/stub.go` — the SP1 stub adapter
- `internal/node/agent/reconcile.go` — Observe → Plan → Apply, debounce
- `internal/node/agent/clock.go` — `Clock` interface + fake for tests

**Phase E — transport**
- `proto/control.proto`, `proto/enroll.proto` — wire contract
- `internal/panel/nodes/ca.go` — panel CA, CSR signing
- `internal/panel/control/server.go` — gRPC server, mTLS, fingerprint allow-list
- `internal/panel/control/hub.go` — connected-stream registry, revision fan-out
- `internal/node/agent/client.go` — dial, Hello, heartbeat, snapshot fetch

**Phase F — HTTP**
- `internal/panel/httpapi/router.go`, `middleware.go`, `errors.go`
- `internal/panel/httpapi/nodes.go`, `services.go`, `admins.go`, `audit.go`, `sse.go`

**Phase G — distribution**
- `scripts/install.sh`, `packaging/*.service`
- `internal/panel/nodes/bootstrap_ssh.go` — SSH path, credentials never persisted
- `cmd/antimage-ctl/*.go`

**Phase H — UI**
- `web/` — Vite app; `src/i18n/`, `src/routes/`, `src/components/`
- `internal/panel/webui/embed.go` — `embed.FS` + dev proxy

---

# Phase A — Foundations

### Task 1: Repository skeleton, toolchain, and CI gates

**Files:**
- Create: `go.mod`, `Makefile`, `.golangci.yml`, `.gitignore`
- Create: `.github/workflows/ci.yml`
- Create: `internal/shared/version/version.go`
- Test: `internal/shared/version/version_test.go`, `scripts/check-imports.sh`

**Interfaces:**
- Consumes: nothing.
- Produces: `version.Version` (string, set by ldflags), `version.Protocol` (int, currently `1`). `make test`, `make lint`, `make build`.

- [ ] **Step 1: Initialize the module and directory tree**

```bash
cd ~/Downloads/antimage
go mod init github.com/amyrm/antimage
mkdir -p cmd/{antimage-panel,antimage-node,antimage-ctl} \
         internal/panel/{httpapi,auth,rbac,audit,nodes,control,store,webui} \
         internal/node/{agent,adapter,sysinfo,supervisor} \
         internal/shared/{canonical,secrets,version,ids} \
         proto packaging scripts web
```

- [ ] **Step 2: Write the failing test**

`internal/shared/version/version_test.go`:

```go
package version

import "testing"

func TestDefaultsAreDevelopmentSafe(t *testing.T) {
	if Version == "" {
		t.Fatal("Version must never be empty; ldflags may be absent in dev builds")
	}
	if Protocol < 1 {
		t.Fatalf("Protocol must be >= 1, got %d", Protocol)
	}
}
```

- [ ] **Step 3: Run it and watch it fail**

Run: `go test ./internal/shared/version/...`
Expected: FAIL — build error, `undefined: Version`.

- [ ] **Step 4: Implement**

`internal/shared/version/version.go`:

```go
// Package version carries build identity shared by all three binaries.
package version

// Version is overridden at build time via
// -ldflags "-X github.com/amyrm/antimage/internal/shared/version.Version=v0.1.0".
var Version = "dev"

// Protocol is the panel<->agent wire protocol version. Bump it whenever a
// change would make an older agent misbehave rather than fail loudly.
const Protocol = 1
```

- [ ] **Step 5: Run it and watch it pass**

Run: `go test ./internal/shared/version/...`
Expected: PASS.

- [ ] **Step 6: Write the import-graph guard**

The spec requires that `internal/node/adapter` never imports `internal/panel`. `scripts/check-imports.sh`:

```bash
#!/usr/bin/env bash
# Enforces the adapter/panel boundary from the design spec section 3.
set -euo pipefail

violations=$(go list -deps -f '{{.ImportPath}}' \
  github.com/amyrm/antimage/internal/node/adapter/... \
  | grep 'github.com/amyrm/antimage/internal/panel' || true)

if [ -n "$violations" ]; then
  echo "FAIL: internal/node/adapter must not depend on internal/panel."
  echo "Offending dependencies:"
  echo "$violations"
  exit 1
fi

if grep -rn "InsecureIgnoreHostKey" --include='*.go' . ; then
  echo "FAIL: InsecureIgnoreHostKey is banned (spec section 7.2)."
  exit 1
fi

echo "OK: import boundaries and SSH host-key policy clean."
```

Make it executable: `chmod +x scripts/check-imports.sh`

- [ ] **Step 7: Write the Makefile**

```makefile
GO      ?= go
LDFLAGS := -X github.com/amyrm/antimage/internal/shared/version.Version=$(shell git describe --tags --always --dirty)
BUILD   := CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)"

.PHONY: test lint build check-imports clean

test:
	$(GO) test ./... -race -count=1

lint:
	$(GO) vet ./...
	golangci-lint run
	gosec -quiet ./...

check-imports:
	./scripts/check-imports.sh

build:
	$(BUILD) -o bin/antimage-panel ./cmd/antimage-panel
	$(BUILD) -o bin/antimage-node  ./cmd/antimage-node
	$(BUILD) -o bin/antimage-ctl   ./cmd/antimage-ctl

clean:
	rm -rf bin
```

- [ ] **Step 8: Write `.gitignore`**

```
bin/
*.db
*.db-wal
*.db-shm
master.key
web/node_modules/
web/dist/
.env
```

- [ ] **Step 9: Write `.golangci.yml`**

```yaml
run:
  timeout: 5m
linters:
  enable:
    - errcheck
    - govet
    - staticcheck
    - ineffassign
    - unused
    - bodyclose
    - rowserrcheck
    - sqlclosecheck
    - contextcheck
    - errorlint
issues:
  exclude-rules:
    - path: _test\.go
      linters: [errcheck]
```

- [ ] **Step 10: Write the CI workflow**

`.github/workflows/ci.yml`:

```yaml
name: ci
on: [push, pull_request]
jobs:
  go:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.23' }
      - run: go build ./...
      - run: make check-imports
      - run: go vet ./...
      - uses: golangci/golangci-lint-action@v6
      - run: go test ./... -race -count=1
      - name: cross-compile
        run: |
          CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...
          CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...
```

- [ ] **Step 11: Verify everything passes**

Run: `make test && make check-imports && go build ./...`
Expected: all PASS. `check-imports` prints the OK line (the adapter package is empty, which trivially satisfies it).

- [ ] **Step 12: Commit**

```bash
git add go.mod Makefile .golangci.yml .gitignore .github scripts internal/shared/version
git commit -m "chore: repo skeleton, toolchain, and CI gates"
```

---

### Task 2: Store foundation — pragmas, migrations, single writer

**Files:**
- Create: `internal/panel/store/store.go`
- Create: `internal/panel/store/migrations/embed.go`
- Create: `internal/panel/store/migrations/00001_init.sql`
- Test: `internal/panel/store/store_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `store.Open(path string) (*store.Store, error)`
  - `(*Store).Read() *sql.DB` — pooled read handle
  - `(*Store).Write(ctx, fn func(*sql.Tx) error) error` — serialized write transaction
  - `(*Store).Close() error`

**Why two handles:** SQLite allows one writer but many concurrent readers in WAL mode. A single `*sql.DB` with `MaxOpenConns(1)` serializes reads behind writes and destroys throughput; two handles keep reads parallel while writes stay serialized.

- [ ] **Step 1: Add dependencies**

```bash
go get modernc.org/sqlite@latest
go get github.com/pressly/goose/v3@latest
```

- [ ] **Step 2: Write the failing test**

`internal/panel/store/store_test.go`:

```go
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
```

- [ ] **Step 3: Run it and watch it fail**

Run: `go test ./internal/panel/store/...`
Expected: FAIL — `undefined: Open`.

- [ ] **Step 4: Write the initial migration**

`internal/panel/store/migrations/00001_init.sql`:

```sql
-- +goose Up
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT;

-- +goose Down
DROP TABLE settings;
```

`internal/panel/store/migrations/embed.go`:

```go
// Package migrations embeds the goose migration set so the panel binary
// carries its own schema and needs no files on disk.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
```

- [ ] **Step 5: Implement the store**

`internal/panel/store/store.go`:

```go
// Package store owns the SQLite connection lifecycle and schema migrations.
//
// SQLite permits exactly one writer. We therefore keep two handles: a pooled
// read handle for concurrent queries, and a write handle capped at one
// connection so write transactions serialize instead of colliding on
// SQLITE_BUSY.
package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"

	"github.com/amyrm/antimage/internal/panel/store/migrations"
)

type Store struct {
	read  *sql.DB
	write *sql.DB
}

// dsn builds a connection string carrying the pragmas from the spec's
// global constraints. They must be on the DSN, not issued as statements,
// so every pooled connection gets them.
func dsn(path string) string {
	return fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)",
		path,
	)
}

func Open(path string) (*Store, error) {
	write, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open write handle: %w", err)
	}
	write.SetMaxOpenConns(1)

	read, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		_ = write.Close()
		return nil, fmt.Errorf("open read handle: %w", err)
	}

	s := &Store{read: read, write: write}
	if err := s.migrate(); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	if err := goose.Up(s.write, "."); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}

// Read returns the pooled read-only handle. Never write through it.
func (s *Store) Read() *sql.DB { return s.read }

// Write runs fn inside a transaction on the single write connection.
// It commits when fn returns nil and rolls back otherwise.
func (s *Store) Write(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (s *Store) Close() error {
	rerr := s.read.Close()
	werr := s.write.Close()
	if werr != nil {
		return werr
	}
	return rerr
}
```

- [ ] **Step 6: Run the tests and watch them pass**

Run: `go test ./internal/panel/store/... -race -v`
Expected: PASS — all three tests. `TestConcurrentWritesAreSerialized` proves no `SQLITE_BUSY` under contention.

- [ ] **Step 7: Commit**

```bash
git add internal/panel/store go.mod go.sum
git commit -m "feat(store): SQLite foundation with WAL, migrations, serialized writer"
```

---

### Task 3: Canonical serialization (RFC 8785)

**Files:**
- Create: `internal/shared/canonical/canonical.go`
- Test: `internal/shared/canonical/canonical_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `canonical.Marshal(v any) ([]byte, error)` — JCS-canonical bytes
  - `canonical.Hash(v any) ([]byte, string, error)` — canonical bytes, lowercase hex SHA-256

This is invariant 3 from the spec, and it is the invariant that fails silently. The property test is not optional.

- [ ] **Step 1: Add the JCS dependency**

```bash
go get github.com/gowebpki/jcs@latest
```

- [ ] **Step 2: Write the failing tests**

`internal/shared/canonical/canonical_test.go`:

```go
package canonical

import (
	"encoding/json"
	"math/rand"
	"testing"
)

// Property: insertion order into a map must never change the output.
// Go randomizes map iteration, but encoding/json sorts keys — this test
// guards against anyone "optimizing" that away with a custom encoder.
func TestMapInsertionOrderDoesNotAffectHash(t *testing.T) {
	keys := []string{"zeta", "alpha", "mu", "beta", "omega"}
	var want string
	for trial := 0; trial < 200; trial++ {
		m := map[string]any{}
		perm := rand.Perm(len(keys))
		for _, i := range perm {
			m[keys[i]] = i
		}
		_, got, err := Hash(m)
		if err != nil {
			t.Fatalf("Hash: %v", err)
		}
		if trial == 0 {
			want = got
			continue
		}
		if got != want {
			t.Fatalf("trial %d: hash %s != %s — canonicalization is order-dependent", trial, got, want)
		}
	}
}

// Property: struct field declaration order must not affect the output,
// because JCS sorts keys. Two structs with identical fields in different
// order must canonicalize identically.
func TestStructFieldOrderDoesNotAffectHash(t *testing.T) {
	type A struct {
		Zulu  string `json:"zulu"`
		Alpha string `json:"alpha"`
	}
	type B struct {
		Alpha string `json:"alpha"`
		Zulu  string `json:"zulu"`
	}
	_, ha, err := Hash(A{Zulu: "z", Alpha: "a"})
	if err != nil {
		t.Fatalf("Hash(A): %v", err)
	}
	_, hb, err := Hash(B{Alpha: "a", Zulu: "z"})
	if err != nil {
		t.Fatalf("Hash(B): %v", err)
	}
	if ha != hb {
		t.Fatalf("field order changed the hash: %s != %s", ha, hb)
	}
}

func TestKeysAreSortedAndWhitespaceStripped(t *testing.T) {
	got, _, err := Hash(map[string]any{"b": 1, "a": 2})
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if string(got) != `{"a":2,"b":1}` {
		t.Errorf("canonical form = %s, want {\"a\":2,\"b\":1}", got)
	}
}

// A nil slice and an empty slice must not collide, because the desired
// document distinguishes "no services" from "field absent".
func TestNilAndEmptySliceAreDistinguishable(t *testing.T) {
	type doc struct {
		Items []string `json:"items"`
	}
	nilBytes, _, err := Hash(doc{Items: nil})
	if err != nil {
		t.Fatalf("Hash(nil): %v", err)
	}
	emptyBytes, _, err := Hash(doc{Items: []string{}})
	if err != nil {
		t.Fatalf("Hash(empty): %v", err)
	}
	if string(nilBytes) != `{"items":null}` {
		t.Errorf("nil slice = %s, want {\"items\":null}", nilBytes)
	}
	if string(emptyBytes) != `{"items":[]}` {
		t.Errorf("empty slice = %s, want {\"items\":[]}", emptyBytes)
	}
}

func TestHashIsStableHex(t *testing.T) {
	_, h, err := Hash(json.RawMessage(`{"a":1}`))
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if len(h) != 64 {
		t.Errorf("hash length = %d, want 64 hex chars", len(h))
	}
}
```

- [ ] **Step 3: Run and watch them fail**

Run: `go test ./internal/shared/canonical/...`
Expected: FAIL — `undefined: Hash`.

- [ ] **Step 4: Implement**

`internal/shared/canonical/canonical.go`:

```go
// Package canonical produces RFC 8785 (JSON Canonicalization Scheme) bytes
// and their SHA-256 digest.
//
// Every desired-state document hash in antimage flows through here. If two
// logically identical documents ever canonicalize differently, nodes will
// reconcile in a loop and the Integrity check in the spec will fire
// spuriously, so the package is deliberately tiny and heavily property-tested.
package canonical

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/gowebpki/jcs"
)

// Marshal encodes v as JSON and transforms it to RFC 8785 canonical form:
// keys sorted by UTF-16 code unit, no insignificant whitespace, defined
// number formatting.
func Marshal(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	out, err := jcs.Transform(raw)
	if err != nil {
		return nil, fmt.Errorf("canonicalize: %w", err)
	}
	return out, nil
}

// Hash returns the canonical bytes and their lowercase hex SHA-256.
//
// Callers must persist and transmit the returned bytes, never a re-encoding
// of v: spec invariant 4 requires the hash to describe the exact bytes the
// agent receives.
func Hash(v any) ([]byte, string, error) {
	b, err := Marshal(v)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(b)
	return b, hex.EncodeToString(sum[:]), nil
}
```

- [ ] **Step 5: Run and watch them pass**

Run: `go test ./internal/shared/canonical/... -race -count=1 -v`
Expected: PASS — all five tests, including 200 permutation trials.

- [ ] **Step 6: Commit**

```bash
git add internal/shared/canonical go.mod go.sum
git commit -m "feat(canonical): RFC 8785 serialization with order-independence property tests"
```

---

### Task 4: Master key and encrypted secret box

**Files:**
- Create: `internal/shared/secrets/box.go`
- Test: `internal/shared/secrets/box_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `secrets.LoadOrCreateKey(path string) ([]byte, error)` — honours `ANTIMAGE_MASTER_KEY`
  - `secrets.LoadKey(path string) ([]byte, error)` — never creates; used when encrypted rows exist
  - `secrets.NewBox(key []byte) (*Box, error)`
  - `(*Box).Seal(plaintext []byte) ([]byte, error)` — returns `nonce||ciphertext`
  - `(*Box).Open(sealed []byte) ([]byte, error)`

Spec section 6.1: the key lives outside the database so a leaked backup yields no TOTP secrets and no CA key, and the panel refuses to start if the key is missing while encrypted rows exist.

- [ ] **Step 1: Write the failing tests**

`internal/shared/secrets/box_test.go`:

```go
package secrets

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	box, err := NewBox(bytes.Repeat([]byte{7}, KeySize))
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}
	plain := []byte("JBSWY3DPEHPK3PXP")
	sealed, err := box.Seal(plain)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains(sealed, plain) {
		t.Fatal("ciphertext contains the plaintext")
	}
	got, err := box.Open(sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("round trip = %q, want %q", got, plain)
	}
}

func TestSealUsesFreshNonce(t *testing.T) {
	box, _ := NewBox(bytes.Repeat([]byte{7}, KeySize))
	a, _ := box.Seal([]byte("same"))
	b, _ := box.Seal([]byte("same"))
	if bytes.Equal(a, b) {
		t.Fatal("two seals of identical plaintext produced identical ciphertext — nonce reuse")
	}
}

func TestOpenRejectsTamperedCiphertext(t *testing.T) {
	box, _ := NewBox(bytes.Repeat([]byte{7}, KeySize))
	sealed, _ := box.Seal([]byte("secret"))
	sealed[len(sealed)-1] ^= 0xff
	if _, err := box.Open(sealed); err == nil {
		t.Fatal("Open accepted tampered ciphertext")
	}
}

func TestNewBoxRejectsWrongKeySize(t *testing.T) {
	if _, err := NewBox([]byte("short")); err == nil {
		t.Fatal("NewBox accepted a short key")
	}
}

func TestLoadOrCreateKeyIsStableAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	first, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatalf("LoadOrCreateKey: %v", err)
	}
	if len(first) != KeySize {
		t.Fatalf("key length = %d, want %d", len(first), KeySize)
	}
	second, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatalf("second LoadOrCreateKey: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("second call generated a new key — existing secrets would be orphaned")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("key file mode = %o, want 600", perm)
		}
	}
}

func TestLoadKeyDoesNotCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.key")
	if _, err := LoadKey(path); err == nil {
		t.Fatal("LoadKey created or accepted a missing key file")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("LoadKey must not create the file")
	}
}

func TestEnvOverrideWins(t *testing.T) {
	t.Setenv(EnvVar, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	path := filepath.Join(t.TempDir(), "unused.key")
	key, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatalf("LoadOrCreateKey: %v", err)
	}
	if len(key) != KeySize {
		t.Fatalf("key length = %d, want %d", len(key), KeySize)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("env override must not write a key file")
	}
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/shared/secrets/...`
Expected: FAIL — `undefined: NewBox`.

- [ ] **Step 3: Implement**

`internal/shared/secrets/box.go`:

```go
// Package secrets holds the panel master key and the AES-256-GCM box used
// for TOTP secrets, recovery codes, and the CA private key.
//
// The key deliberately lives outside the database (spec section 6.1) so that
// a leaked database backup yields no usable secrets.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	// KeySize is 32 bytes for AES-256.
	KeySize = 32
	// EnvVar overrides the key file, for operators injecting secrets at deploy time.
	EnvVar = "ANTIMAGE_MASTER_KEY"
)

// ErrKeyMissing is returned when no key exists and creation was not requested.
// The panel maps this to a refuse-to-start error whenever encrypted rows are
// present, rather than silently generating a fresh key and orphaning them.
var ErrKeyMissing = errors.New("master key not found")

func keyFromEnv() ([]byte, bool, error) {
	raw, ok := os.LookupEnv(EnvVar)
	if !ok || raw == "" {
		return nil, false, nil
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, true, fmt.Errorf("%s is not valid base64: %w", EnvVar, err)
	}
	if len(key) != KeySize {
		return nil, true, fmt.Errorf("%s decodes to %d bytes, want %d", EnvVar, len(key), KeySize)
	}
	return key, true, nil
}

// LoadKey reads an existing key. It never creates one.
func LoadKey(path string) ([]byte, error) {
	if key, ok, err := keyFromEnv(); ok {
		return key, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrKeyMissing
	}
	if err != nil {
		return nil, fmt.Errorf("read master key: %w", err)
	}
	key, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil {
		return nil, fmt.Errorf("master key file is not valid base64: %w", err)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("master key is %d bytes, want %d", len(key), KeySize)
	}
	return key, nil
}

// LoadOrCreateKey reads the key, generating one at 0600 on first run.
func LoadOrCreateKey(path string) ([]byte, error) {
	key, err := LoadKey(path)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, ErrKeyMissing) {
		return nil, err
	}

	key = make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create key directory: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
		return nil, fmt.Errorf("write master key: %w", err)
	}
	return key, nil
}

// Box seals and opens secrets with AES-256-GCM.
type Box struct {
	aead cipher.AEAD
}

func NewBox(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("key is %d bytes, want %d", len(key), KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new GCM: %w", err)
	}
	return &Box{aead: aead}, nil
}

// Seal returns nonce||ciphertext with a fresh random nonce per call.
func (b *Box) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	return b.aead.Seal(nonce, nonce, plaintext, nil), nil
}

func (b *Box) Open(sealed []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("ciphertext shorter than nonce")
	}
	plain, err := b.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return plain, nil
}
```

- [ ] **Step 4: Run and watch them pass**

Run: `go test ./internal/shared/secrets/... -race -v`
Expected: PASS — all seven tests.

- [ ] **Step 5: Commit**

```bash
git add internal/shared/secrets
git commit -m "feat(secrets): master key management and AES-256-GCM box"
```

---

# Phase B — Identity, authorization, audit

### Task 5: Password hashing and the admin schema

**Files:**
- Create: `internal/panel/store/migrations/00002_identity.sql`
- Create: `internal/panel/auth/password.go`
- Test: `internal/panel/auth/password_test.go`

**Interfaces:**
- Consumes: `store.Store` (Task 2).
- Produces:
  - `auth.HashPassword(plain string) (string, error)` — PHC-encoded argon2id
  - `auth.VerifyPassword(encoded, plain string) (bool, error)` — constant-time

- [ ] **Step 1: Write the migration**

`internal/panel/store/migrations/00002_identity.sql`:

```sql
-- +goose Up
CREATE TABLE roles (
    id          INTEGER PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    is_builtin  INTEGER NOT NULL DEFAULT 0,
    permissions TEXT NOT NULL             -- JSON array of permission keys
) STRICT;

CREATE TABLE admins (
    id              INTEGER PRIMARY KEY,
    username        TEXT NOT NULL COLLATE NOCASE,
    password_hash   TEXT NOT NULL,
    role_id         INTEGER NOT NULL REFERENCES roles(id),
    parent_admin_id INTEGER REFERENCES admins(id),
    status          TEXT NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active','suspended')),
    totp_secret_enc BLOB,
    created_at      INTEGER NOT NULL
) STRICT;

CREATE UNIQUE INDEX admins_username_unique ON admins (username COLLATE NOCASE);

CREATE TABLE admin_scopes (
    admin_id   INTEGER NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    scope_type TEXT NOT NULL CHECK (scope_type IN ('node','service')),
    scope_id   INTEGER NOT NULL,
    PRIMARY KEY (admin_id, scope_type, scope_id)
) STRICT;

CREATE TABLE sessions (
    id           INTEGER PRIMARY KEY,
    admin_id     INTEGER NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    token_hash   BLOB NOT NULL UNIQUE,
    ip           TEXT NOT NULL,
    user_agent   TEXT NOT NULL,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL,
    revoked_at   INTEGER
) STRICT;

CREATE INDEX sessions_admin ON sessions (admin_id);

-- +goose Down
DROP TABLE sessions;
DROP TABLE admin_scopes;
DROP TABLE admins;
DROP TABLE roles;
```

- [ ] **Step 2: Write the failing tests**

`internal/panel/auth/password_test.go`:

```go
package auth

import (
	"strings"
	"testing"
)

func TestHashVerifyRoundTrip(t *testing.T) {
	encoded, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	ok, err := VerifyPassword(encoded, "correct horse battery staple")
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if !ok {
		t.Error("correct password rejected")
	}
}

func TestVerifyRejectsWrongPassword(t *testing.T) {
	encoded, _ := HashPassword("hunter2")
	ok, err := VerifyPassword(encoded, "hunter3")
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if ok {
		t.Error("wrong password accepted")
	}
}

func TestHashIsSaltedPerCall(t *testing.T) {
	a, _ := HashPassword("same")
	b, _ := HashPassword("same")
	if a == b {
		t.Fatal("identical passwords produced identical hashes — salt is missing or fixed")
	}
}

func TestEncodingIsPHCWithSpecParams(t *testing.T) {
	encoded, _ := HashPassword("x")
	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Errorf("encoding = %q, want spec params m=65536,t=3,p=4", encoded)
	}
	if n := len(strings.Split(encoded, "$")); n != 6 {
		t.Errorf("PHC string has %d segments, want 6", n)
	}
}

func TestVerifyRejectsMalformedEncoding(t *testing.T) {
	for _, bad := range []string{
		"",
		"not-a-hash",
		"$argon2id$v=19$m=65536,t=3,p=4$onlysalt",
		"$bcrypt$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA",
	} {
		if _, err := VerifyPassword(bad, "x"); err == nil {
			t.Errorf("VerifyPassword(%q) returned nil error", bad)
		}
	}
}
```

- [ ] **Step 3: Run and watch them fail**

Run: `go test ./internal/panel/auth/...`
Expected: FAIL — `undefined: HashPassword`.

- [ ] **Step 4: Implement**

```bash
go get golang.org/x/crypto@latest
```

`internal/panel/auth/password.go`:

```go
// Package auth implements password hashing, sessions, rate limiting, and TOTP.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Parameters from the spec's global constraints.
const (
	argonMemory  = 64 * 1024 // KiB
	argonTime    = 3
	argonThreads = 4
	argonSaltLen = 16
	argonKeyLen  = 32
)

var b64 = base64.RawStdEncoding

// HashPassword returns a PHC-encoded argon2id hash:
// $argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>
func HashPassword(plain string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	sum := argon2.IDKey([]byte(plain), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(sum)), nil
}

// VerifyPassword compares plain against a PHC-encoded hash in constant time.
// It returns an error only for malformed encodings, never for a mismatch, so
// callers cannot accidentally distinguish the two cases in a response.
func VerifyPassword(encoded, plain string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errors.New("malformed argon2id encoding")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, fmt.Errorf("unsupported argon2 version %q", parts[2])
	}
	var memory uint32
	var time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false, fmt.Errorf("malformed argon2id parameters %q", parts[3])
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("malformed salt: %w", err)
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("malformed hash: %w", err)
	}
	got := argon2.IDKey([]byte(plain), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
```

- [ ] **Step 5: Run and watch them pass**

Run: `go test ./internal/panel/auth/... -race -v`
Expected: PASS — all five tests.

- [ ] **Step 6: Verify the migration applies**

Run: `go test ./internal/panel/store/... -count=1`
Expected: PASS — `Open` runs migration 00002 without error.

- [ ] **Step 7: Commit**

```bash
git add internal/panel/auth internal/panel/store/migrations go.mod go.sum
git commit -m "feat(auth): argon2id password hashing and identity schema"
```

---

### Task 6: Sessions and the authentication middleware

**Files:**
- Create: `internal/panel/auth/session.go`
- Test: `internal/panel/auth/session_test.go`

**Interfaces:**
- Consumes: `store.Store` (Task 2).
- Produces:
  - `auth.NewSessions(s *store.Store, now func() time.Time) *Sessions`
  - `(*Sessions).Create(ctx, adminID int64, ip, ua string) (token string, err error)`
  - `(*Sessions).Lookup(ctx, token string) (*Session, error)` — bumps `last_used_at`, enforces idle and absolute limits
  - `(*Sessions).Revoke(ctx, sessionID int64) error`
  - `(*Sessions).RevokeAllForAdmin(ctx, adminID int64) error`
  - `auth.ErrSessionInvalid`
  - `type Session struct { ID, AdminID int64; IP, UserAgent string; CreatedAt, ExpiresAt, LastUsedAt time.Time }`

Constants: `auth.IdleTimeout = 4 * time.Hour`, `auth.AbsoluteLifetime = 7 * 24 * time.Hour`, `auth.CookieName = "antimage_session"`.

- [ ] **Step 1: Write the failing tests**

`internal/panel/auth/session_test.go`:

```go
package auth

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

// seedAdmin inserts a role and admin directly, so session tests do not
// depend on the admin CRUD API that arrives in a later task.
func seedAdmin(t *testing.T, s *store.Store) int64 {
	t.Helper()
	var id int64
	err := s.Write(context.Background(), func(tx *sqlTx) error { return nil })
	_ = err
	return id
}

func newSessions(t *testing.T) (*Sessions, *store.Store, int64, *fakeClock) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	adminID := insertTestAdmin(t, s)
	clk := &fakeClock{now: time.Unix(1_700_000_000, 0).UTC()}
	return NewSessions(s, clk.Now), s, adminID, clk
}

func TestCreateReturnsOpaqueTokenNotStoredPlain(t *testing.T) {
	sess, s, adminID, _ := newSessions(t)
	ctx := context.Background()
	token, err := sess.Create(ctx, adminID, "10.0.0.1", "curl/8")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(token) < 32 {
		t.Errorf("token length %d is too short to be 32 random bytes", len(token))
	}
	var n int
	if err := s.Read().QueryRow(
		`SELECT count(*) FROM sessions WHERE token_hash = ?`, []byte(token),
	).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 0 {
		t.Fatal("the raw token was stored — only its SHA-256 may be persisted")
	}
}

func TestLookupAcceptsValidTokenAndRejectsGarbage(t *testing.T) {
	sess, _, adminID, _ := newSessions(t)
	ctx := context.Background()
	token, _ := sess.Create(ctx, adminID, "10.0.0.1", "curl/8")

	got, err := sess.Lookup(ctx, token)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got.AdminID != adminID {
		t.Errorf("AdminID = %d, want %d", got.AdminID, adminID)
	}
	if _, err := sess.Lookup(ctx, "not-a-real-token"); err == nil {
		t.Error("Lookup accepted a garbage token")
	}
}

func TestRevokeTakesEffectImmediately(t *testing.T) {
	sess, _, adminID, _ := newSessions(t)
	ctx := context.Background()
	token, _ := sess.Create(ctx, adminID, "10.0.0.1", "curl/8")
	s, err := sess.Lookup(ctx, token)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if err := sess.Revoke(ctx, s.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := sess.Lookup(ctx, token); err == nil {
		t.Fatal("revoked session still validates — immediate revocation is the reason " +
			"we chose server-side sessions over JWTs")
	}
}

func TestIdleTimeoutExpiresSession(t *testing.T) {
	sess, _, adminID, clk := newSessions(t)
	ctx := context.Background()
	token, _ := sess.Create(ctx, adminID, "10.0.0.1", "curl/8")

	clk.advance(IdleTimeout - time.Minute)
	if _, err := sess.Lookup(ctx, token); err != nil {
		t.Fatalf("session expired early: %v", err)
	}
	// The successful lookup refreshed last_used_at, so the idle window restarts.
	clk.advance(IdleTimeout + time.Minute)
	if _, err := sess.Lookup(ctx, token); err == nil {
		t.Fatal("idle session still valid past IdleTimeout")
	}
}

func TestAbsoluteLifetimeExpiresActiveSession(t *testing.T) {
	sess, _, adminID, clk := newSessions(t)
	ctx := context.Background()
	token, _ := sess.Create(ctx, adminID, "10.0.0.1", "curl/8")

	// Stay active: look up every hour so idle never trips.
	for elapsed := time.Duration(0); elapsed < AbsoluteLifetime; elapsed += time.Hour {
		clk.advance(time.Hour)
		if _, err := sess.Lookup(ctx, token); err != nil {
			t.Fatalf("session died at %v: %v", elapsed, err)
		}
	}
	clk.advance(time.Hour)
	if _, err := sess.Lookup(ctx, token); err == nil {
		t.Fatal("session outlived AbsoluteLifetime despite continuous activity")
	}
}

func TestRevokeAllForAdmin(t *testing.T) {
	sess, _, adminID, _ := newSessions(t)
	ctx := context.Background()
	a, _ := sess.Create(ctx, adminID, "10.0.0.1", "curl/8")
	b, _ := sess.Create(ctx, adminID, "10.0.0.2", "firefox")
	if err := sess.RevokeAllForAdmin(ctx, adminID); err != nil {
		t.Fatalf("RevokeAllForAdmin: %v", err)
	}
	for name, tok := range map[string]string{"a": a, "b": b} {
		if _, err := sess.Lookup(ctx, tok); err == nil {
			t.Errorf("session %s survived RevokeAllForAdmin", name)
		}
	}
}
```

Delete the placeholder `seedAdmin` above and use this helper file instead, `internal/panel/auth/helper_test.go`:

```go
package auth

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func insertTestAdmin(t *testing.T, s *store.Store) int64 {
	t.Helper()
	var id int64
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`INSERT INTO roles (id, name, is_builtin, permissions) VALUES (1, 'super_admin', 1, '[]')`,
		); err != nil {
			return err
		}
		res, err := tx.Exec(
			`INSERT INTO admins (username, password_hash, role_id, created_at)
			 VALUES ('tester', 'x', 1, ?)`, time.Now().Unix())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("insertTestAdmin: %v", err)
	}
	return id
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/panel/auth/... -run Session`
Expected: FAIL — `undefined: NewSessions`.

- [ ] **Step 3: Implement**

`internal/panel/auth/session.go`:

```go
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

const (
	// IdleTimeout and AbsoluteLifetime come from the spec's global constraints.
	IdleTimeout      = 4 * time.Hour
	AbsoluteLifetime = 7 * 24 * time.Hour
	CookieName       = "antimage_session"
	tokenBytes       = 32
)

// ErrSessionInvalid covers every rejection reason. Callers must not
// distinguish "unknown", "revoked", and "expired" in responses.
var ErrSessionInvalid = errors.New("session invalid")

type Session struct {
	ID         int64
	AdminID    int64
	IP         string
	UserAgent  string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastUsedAt time.Time
}

type Sessions struct {
	store *store.Store
	now   func() time.Time
}

func NewSessions(s *store.Store, now func() time.Time) *Sessions {
	if now == nil {
		now = time.Now
	}
	return &Sessions{store: s, now: now}
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// Create mints an opaque 32-byte token, persists only its SHA-256, and
// returns the raw token to be set as a cookie exactly once.
func (s *Sessions) Create(ctx context.Context, adminID int64, ip, ua string) (string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	now := s.now().UTC()
	err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO sessions
			   (admin_id, token_hash, ip, user_agent, created_at, expires_at, last_used_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			adminID, hashToken(token), ip, ua,
			now.Unix(), now.Add(AbsoluteLifetime).Unix(), now.Unix())
		return err
	})
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return token, nil
}

// Lookup validates a token and refreshes last_used_at. It enforces both the
// idle window and the absolute lifetime, and rejects revoked sessions.
func (s *Sessions) Lookup(ctx context.Context, token string) (*Session, error) {
	if token == "" {
		return nil, ErrSessionInvalid
	}
	now := s.now().UTC()

	var (
		sess                              Session
		createdAt, expiresAt, lastUsedAt  int64
		revokedAt                         sql.NullInt64
	)
	err := s.store.Read().QueryRowContext(ctx,
		`SELECT id, admin_id, ip, user_agent, created_at, expires_at, last_used_at, revoked_at
		   FROM sessions WHERE token_hash = ?`, hashToken(token),
	).Scan(&sess.ID, &sess.AdminID, &sess.IP, &sess.UserAgent,
		&createdAt, &expiresAt, &lastUsedAt, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSessionInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("lookup session: %w", err)
	}
	if revokedAt.Valid {
		return nil, ErrSessionInvalid
	}
	if now.Unix() >= expiresAt {
		return nil, ErrSessionInvalid
	}
	if now.Sub(time.Unix(lastUsedAt, 0).UTC()) >= IdleTimeout {
		return nil, ErrSessionInvalid
	}

	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE sessions SET last_used_at = ? WHERE id = ?`, now.Unix(), sess.ID)
		return err
	}); err != nil {
		return nil, fmt.Errorf("refresh session: %w", err)
	}

	sess.CreatedAt = time.Unix(createdAt, 0).UTC()
	sess.ExpiresAt = time.Unix(expiresAt, 0).UTC()
	sess.LastUsedAt = now
	return &sess, nil
}

func (s *Sessions) Revoke(ctx context.Context, sessionID int64) error {
	now := s.now().UTC().Unix()
	return s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE sessions SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
			now, sessionID)
		return err
	})
}

// RevokeAllForAdmin is called on password change, privilege change, and
// administrative suspension.
func (s *Sessions) RevokeAllForAdmin(ctx context.Context, adminID int64) error {
	now := s.now().UTC().Unix()
	return s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE sessions SET revoked_at = ? WHERE admin_id = ? AND revoked_at IS NULL`,
			now, adminID)
		return err
	})
}
```

- [ ] **Step 4: Remove the stale helper**

Delete the `seedAdmin` stub from `session_test.go` — `helper_test.go` supersedes it. The file must compile with no unused imports.

- [ ] **Step 5: Run and watch them pass**

Run: `go test ./internal/panel/auth/... -race -count=1 -v`
Expected: PASS — six session tests plus the five password tests.

- [ ] **Step 6: Commit**

```bash
git add internal/panel/auth
git commit -m "feat(auth): opaque server-side sessions with idle and absolute expiry"
```

---

### Task 7: Login rate limiting and lockout

**Files:**
- Create: `internal/panel/store/migrations/00003_ratelimit.sql`
- Create: `internal/panel/auth/ratelimit.go`
- Test: `internal/panel/auth/ratelimit_test.go`

**Interfaces:**
- Consumes: `store.Store`, the `now func() time.Time` clock convention from Task 6.
- Produces:
  - `auth.NewLimiter(s *store.Store, now func() time.Time) *Limiter`
  - `(*Limiter).Check(ctx, username, ip string) (retryAfter time.Duration, err error)` — zero means allowed
  - `(*Limiter).RecordFailure(ctx, username, ip string) error`
  - `(*Limiter).Reset(ctx, username, ip string) error` — on successful login

Defaults from the spec: 5 failures per account and 20 per IP in a 15-minute window; backoff doubles from 1s to a 300s ceiling.

- [ ] **Step 1: Write the migration**

`internal/panel/store/migrations/00003_ratelimit.sql`:

```sql
-- +goose Up
CREATE TABLE login_attempts (
    id        INTEGER PRIMARY KEY,
    kind      TEXT NOT NULL CHECK (kind IN ('account','ip')),
    subject   TEXT NOT NULL,
    failed_at INTEGER NOT NULL
) STRICT;

CREATE INDEX login_attempts_lookup ON login_attempts (kind, subject, failed_at);

-- +goose Down
DROP TABLE login_attempts;
```

- [ ] **Step 2: Write the failing tests**

`internal/panel/auth/ratelimit_test.go`:

```go
package auth

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

func newLimiter(t *testing.T) (*Limiter, *fakeClock) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	clk := &fakeClock{now: time.Unix(1_700_000_000, 0).UTC()}
	return NewLimiter(s, clk.Now), clk
}

func TestCleanSubjectIsAllowed(t *testing.T) {
	lim, _ := newLimiter(t)
	wait, err := lim.Check(context.Background(), "alice", "10.0.0.1")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if wait != 0 {
		t.Errorf("retryAfter = %v, want 0 for a clean subject", wait)
	}
}

func TestAccountLocksAfterFiveFailures(t *testing.T) {
	lim, _ := newLimiter(t)
	ctx := context.Background()
	for i := 0; i < AccountFailureLimit; i++ {
		if err := lim.RecordFailure(ctx, "alice", "10.0.0.1"); err != nil {
			t.Fatalf("RecordFailure %d: %v", i, err)
		}
	}
	wait, err := lim.Check(ctx, "alice", "10.0.0.9")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if wait <= 0 {
		t.Fatal("account not limited after 5 failures")
	}
}

func TestBackoffDoublesAndIsCapped(t *testing.T) {
	lim, _ := newLimiter(t)
	ctx := context.Background()
	var last time.Duration
	for i := 0; i < 20; i++ {
		if err := lim.RecordFailure(ctx, "bob", "10.0.0.1"); err != nil {
			t.Fatalf("RecordFailure: %v", err)
		}
		wait, err := lim.Check(ctx, "bob", "10.0.0.1")
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		if wait > MaxBackoff {
			t.Fatalf("backoff %v exceeded cap %v", wait, MaxBackoff)
		}
		if wait < last {
			t.Fatalf("backoff decreased: %v after %v", wait, last)
		}
		last = wait
	}
	if last != MaxBackoff {
		t.Errorf("final backoff = %v, want the %v ceiling", last, MaxBackoff)
	}
}

func TestFailuresOutsideWindowAreIgnored(t *testing.T) {
	lim, clk := newLimiter(t)
	ctx := context.Background()
	for i := 0; i < AccountFailureLimit; i++ {
		_ = lim.RecordFailure(ctx, "carol", "10.0.0.1")
	}
	clk.advance(Window + time.Minute)
	wait, err := lim.Check(ctx, "carol", "10.0.0.1")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if wait != 0 {
		t.Errorf("retryAfter = %v after the window elapsed, want 0", wait)
	}
}

func TestIPLimitCatchesSpreadAcrossAccounts(t *testing.T) {
	lim, _ := newLimiter(t)
	ctx := context.Background()
	// Under the per-account limit for each name, but over the IP limit overall.
	for i := 0; i < IPFailureLimit; i++ {
		user := string(rune('a' + i%26))
		if err := lim.RecordFailure(ctx, user, "10.0.0.7"); err != nil {
			t.Fatalf("RecordFailure: %v", err)
		}
	}
	wait, err := lim.Check(ctx, "brand-new-user", "10.0.0.7")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if wait <= 0 {
		t.Fatal("IP not limited — credential stuffing across many usernames would pass")
	}
}

func TestResetClearsAfterSuccessfulLogin(t *testing.T) {
	lim, _ := newLimiter(t)
	ctx := context.Background()
	for i := 0; i < AccountFailureLimit; i++ {
		_ = lim.RecordFailure(ctx, "dave", "10.0.0.1")
	}
	if err := lim.Reset(ctx, "dave", "10.0.0.1"); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	wait, err := lim.Check(ctx, "dave", "10.0.0.1")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if wait != 0 {
		t.Errorf("retryAfter = %v after reset, want 0", wait)
	}
}
```

- [ ] **Step 3: Run and watch them fail**

Run: `go test ./internal/panel/auth/... -run 'Limit|Backoff|Window|Reset'`
Expected: FAIL — `undefined: NewLimiter`.

- [ ] **Step 4: Implement**

`internal/panel/auth/ratelimit.go`:

```go
package auth

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

// Defaults from the spec's global constraints. A reseller panel is a
// credential-stuffing target, so both the account and the source IP are
// limited: the account limit stops one password being guessed, the IP limit
// stops one attacker spraying many accounts.
const (
	Window              = 15 * time.Minute
	AccountFailureLimit = 5
	IPFailureLimit      = 20
	BaseBackoff         = time.Second
	MaxBackoff          = 300 * time.Second
)

type Limiter struct {
	store *store.Store
	now   func() time.Time
}

func NewLimiter(s *store.Store, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{store: s, now: now}
}

func (l *Limiter) countSince(ctx context.Context, kind, subject string, since time.Time) (int, error) {
	var n int
	err := l.store.Read().QueryRowContext(ctx,
		`SELECT count(*) FROM login_attempts
		  WHERE kind = ? AND subject = ? AND failed_at >= ?`,
		kind, strings.ToLower(subject), since.Unix()).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count %s attempts: %w", kind, err)
	}
	return n, nil
}

// backoff doubles per failure past the limit, capped at MaxBackoff.
func backoff(failures, limit int) time.Duration {
	if failures < limit {
		return 0
	}
	d := BaseBackoff
	for i := limit; i < failures; i++ {
		d *= 2
		if d >= MaxBackoff {
			return MaxBackoff
		}
	}
	return d
}

// Check returns how long the caller must wait. Zero means allowed.
func (l *Limiter) Check(ctx context.Context, username, ip string) (time.Duration, error) {
	since := l.now().UTC().Add(-Window)

	accountFailures, err := l.countSince(ctx, "account", username, since)
	if err != nil {
		return 0, err
	}
	ipFailures, err := l.countSince(ctx, "ip", ip, since)
	if err != nil {
		return 0, err
	}

	wait := backoff(accountFailures, AccountFailureLimit)
	if d := backoff(ipFailures, IPFailureLimit); d > wait {
		wait = d
	}
	return wait, nil
}

func (l *Limiter) RecordFailure(ctx context.Context, username, ip string) error {
	at := l.now().UTC().Unix()
	return l.store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO login_attempts (kind, subject, failed_at) VALUES ('account', ?, ?)`,
			strings.ToLower(username), at); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO login_attempts (kind, subject, failed_at) VALUES ('ip', ?, ?)`,
			strings.ToLower(ip), at)
		return err
	})
}

// Reset clears counters after a successful login, and opportunistically
// prunes rows that have aged out of the window.
func (l *Limiter) Reset(ctx context.Context, username, ip string) error {
	cutoff := l.now().UTC().Add(-Window).Unix()
	return l.store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM login_attempts WHERE kind = 'account' AND subject = ?`,
			strings.ToLower(username)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM login_attempts WHERE kind = 'ip' AND subject = ?`,
			strings.ToLower(ip)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`DELETE FROM login_attempts WHERE failed_at < ?`, cutoff)
		return err
	})
}
```

- [ ] **Step 5: Run and watch them pass**

Run: `go test ./internal/panel/auth/... -race -count=1 -v`
Expected: PASS — six rate-limit tests plus the earlier eleven.

- [ ] **Step 6: Commit**

```bash
git add internal/panel/auth internal/panel/store/migrations
git commit -m "feat(auth): per-account and per-IP login rate limiting with exponential backoff"
```

---

### Task 8: TOTP and recovery codes

**Files:**
- Create: `internal/panel/auth/totp.go`
- Test: `internal/panel/auth/totp_test.go`

**Interfaces:**
- Consumes: `secrets.Box` (Task 4).
- Produces:
  - `auth.GenerateTOTPSecret() (string, error)` — base32, no padding
  - `auth.TOTPProvisioningURI(secret, username, issuer string) string`
  - `auth.VerifyTOTP(secret string, code string, now time.Time) bool` — ±1 step skew
  - `auth.GenerateRecoveryCodes(n int) ([]string, error)`
  - `auth.HashRecoveryCode(code string) string` — reuses argon2id

- [ ] **Step 1: Add the dependency**

```bash
go get github.com/pquerna/otp@latest
```

- [ ] **Step 2: Write the failing tests**

`internal/panel/auth/totp_test.go`:

```go
package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

func TestGenerateSecretIsUsableBase32(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	if len(secret) < 16 {
		t.Errorf("secret %q is shorter than 16 chars", secret)
	}
	if strings.Contains(secret, "=") {
		t.Error("secret contains base32 padding; authenticator apps reject it")
	}
	if _, err := totp.GenerateCode(secret, time.Now()); err != nil {
		t.Errorf("secret is not valid base32 for TOTP: %v", err)
	}
}

func TestVerifyAcceptsCurrentCode(t *testing.T) {
	secret, _ := GenerateTOTPSecret()
	now := time.Unix(1_700_000_000, 0).UTC()
	code, err := totp.GenerateCode(secret, now)
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	if !VerifyTOTP(secret, code, now) {
		t.Error("current code rejected")
	}
}

func TestVerifyToleratesOneStepOfSkew(t *testing.T) {
	secret, _ := GenerateTOTPSecret()
	now := time.Unix(1_700_000_000, 0).UTC()
	past, _ := totp.GenerateCode(secret, now.Add(-30*time.Second))
	future, _ := totp.GenerateCode(secret, now.Add(30*time.Second))
	if !VerifyTOTP(secret, past, now) {
		t.Error("code from the previous step rejected; clock skew will lock users out")
	}
	if !VerifyTOTP(secret, future, now) {
		t.Error("code from the next step rejected")
	}
}

func TestVerifyRejectsStaleAndWrongCodes(t *testing.T) {
	secret, _ := GenerateTOTPSecret()
	now := time.Unix(1_700_000_000, 0).UTC()
	stale, _ := totp.GenerateCode(secret, now.Add(-10*time.Minute))
	if VerifyTOTP(secret, stale, now) {
		t.Error("a ten-minute-old code was accepted")
	}
	if VerifyTOTP(secret, "000000", now) && VerifyTOTP(secret, "999999", now) {
		t.Error("verification accepts arbitrary codes")
	}
}

func TestProvisioningURIShape(t *testing.T) {
	uri := TOTPProvisioningURI("JBSWY3DPEHPK3PXP", "alice", "antimage")
	for _, want := range []string{
		"otpauth://totp/", "antimage:alice", "secret=JBSWY3DPEHPK3PXP", "issuer=antimage",
	} {
		if !strings.Contains(uri, want) {
			t.Errorf("URI %q missing %q", uri, want)
		}
	}
}

func TestRecoveryCodesAreUniqueAndHashable(t *testing.T) {
	codes, err := GenerateRecoveryCodes(10)
	if err != nil {
		t.Fatalf("GenerateRecoveryCodes: %v", err)
	}
	if len(codes) != 10 {
		t.Fatalf("got %d codes, want 10", len(codes))
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if seen[c] {
			t.Fatalf("duplicate recovery code %q", c)
		}
		seen[c] = true

		hashed, err := HashRecoveryCode(c)
		if err != nil {
			t.Fatalf("HashRecoveryCode: %v", err)
		}
		ok, err := VerifyPassword(hashed, c)
		if err != nil || !ok {
			t.Errorf("recovery code %q does not verify against its hash (err=%v)", c, err)
		}
	}
}
```

- [ ] **Step 3: Run and watch them fail**

Run: `go test ./internal/panel/auth/... -run TOTP`
Expected: FAIL — `undefined: GenerateTOTPSecret`.

- [ ] **Step 4: Implement**

`internal/panel/auth/totp.go`:

```go
package auth

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/pquerna/otp/totp"
)

const (
	totpSecretBytes = 20 // 160 bits, the RFC 4226 recommendation
	recoveryBytes   = 10
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateTOTPSecret returns an unpadded base32 secret. Padding breaks
// several popular authenticator apps, so it is stripped.
func GenerateTOTPSecret() (string, error) {
	raw := make([]byte, totpSecretBytes)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", fmt.Errorf("generate TOTP secret: %w", err)
	}
	return b32.EncodeToString(raw), nil
}

func TOTPProvisioningURI(secret, username, issuer string) string {
	label := url.PathEscape(issuer + ":" + username)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", "6")
	q.Set("period", "30")
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// VerifyTOTP allows one step of skew in each direction (±30s), which covers
// ordinary clock drift without meaningfully widening the guess window.
func VerifyTOTP(secret, code string, now time.Time) bool {
	ok, err := totp.ValidateCustom(code, secret, now, totp.ValidateOpts{
		Period: 30,
		Skew:   1,
		Digits: 6,
	})
	return err == nil && ok
}

// GenerateRecoveryCodes returns n single-use codes. Callers store only the
// argon2id hashes from HashRecoveryCode and show the plaintext once.
func GenerateRecoveryCodes(n int) ([]string, error) {
	codes := make([]string, 0, n)
	for i := 0; i < n; i++ {
		raw := make([]byte, recoveryBytes)
		if _, err := io.ReadFull(rand.Reader, raw); err != nil {
			return nil, fmt.Errorf("generate recovery code: %w", err)
		}
		codes = append(codes, b32.EncodeToString(raw))
	}
	return codes, nil
}

// HashRecoveryCode reuses the password hasher so recovery codes get the same
// resistance as passwords.
func HashRecoveryCode(code string) (string, error) {
	return HashPassword(code)
}
```

- [ ] **Step 5: Run and watch them pass**

Run: `go test ./internal/panel/auth/... -race -count=1 -v`
Expected: PASS — six TOTP tests plus the earlier seventeen.

- [ ] **Step 6: Commit**

```bash
git add internal/panel/auth go.mod go.sum
git commit -m "feat(auth): TOTP verification with skew tolerance and recovery codes"
```

---

### Task 9: Permission keys, role templates, and the authz chokepoint

**Files:**
- Create: `internal/panel/rbac/perm.go`
- Create: `internal/panel/rbac/authz.go`
- Test: `internal/panel/rbac/authz_test.go`

**Interfaces:**
- Consumes: nothing (pure logic; scopes arrive from Task 10).
- Produces:
  - `rbac.Permission` (string) and the constants `PermNodeRead`, `PermNodeWrite`, `PermNodeEnroll`, `PermServiceWrite`, `PermAdminManage`, `PermRoleManage`, `PermAuditRead`, `PermSettingsWrite`
  - `rbac.BuiltinRoles() map[string][]Permission` — `super_admin`, `admin`, `reseller`, `readonly`
  - `type Actor struct { AdminID int64; RoleName string; IsSuper bool; Perms map[Permission]struct{}; NodeIDs, ServiceIDs map[int64]struct{} }`
  - `rbac.Check(a *Actor, p Permission, t Target) error`
  - `type Target struct { Kind TargetKind; ID int64 }`, `TargetNone`, `TargetNode`, `TargetService`
  - `rbac.ErrForbidden`

- [ ] **Step 1: Write the failing tests**

`internal/panel/rbac/authz_test.go`:

```go
package rbac

import (
	"errors"
	"testing"
)

func actor(role string, super bool, perms []Permission, nodes ...int64) *Actor {
	a := &Actor{
		AdminID:    1,
		RoleName:   role,
		IsSuper:    super,
		Perms:      map[Permission]struct{}{},
		NodeIDs:    map[int64]struct{}{},
		ServiceIDs: map[int64]struct{}{},
	}
	for _, p := range perms {
		a.Perms[p] = struct{}{}
	}
	for _, n := range nodes {
		a.NodeIDs[n] = struct{}{}
	}
	return a
}

func TestSuperAdminBypassesScope(t *testing.T) {
	a := actor("super_admin", true, BuiltinRoles()["super_admin"])
	if err := Check(a, PermNodeWrite, Target{Kind: TargetNode, ID: 999}); err != nil {
		t.Errorf("super admin denied on unscoped node: %v", err)
	}
}

func TestMissingPermissionIsDenied(t *testing.T) {
	a := actor("readonly", false, BuiltinRoles()["readonly"], 1)
	err := Check(a, PermNodeWrite, Target{Kind: TargetNode, ID: 1})
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("err = %v, want ErrForbidden", err)
	}
}

func TestPermissionWithoutScopeIsDenied(t *testing.T) {
	// Has node:write, but only for node 1. Node 2 must be refused.
	a := actor("reseller", false, []Permission{PermNodeWrite}, 1)
	if err := Check(a, PermNodeWrite, Target{Kind: TargetNode, ID: 1}); err != nil {
		t.Errorf("in-scope node denied: %v", err)
	}
	if err := Check(a, PermNodeWrite, Target{Kind: TargetNode, ID: 2}); !errors.Is(err, ErrForbidden) {
		t.Errorf("out-of-scope node allowed: err = %v", err)
	}
}

func TestNonSuperWithEmptyScopeIsDeniedNotUnrestricted(t *testing.T) {
	// The dangerous default: an empty allow-list must mean "nothing",
	// never "everything".
	a := actor("reseller", false, []Permission{PermNodeRead})
	if err := Check(a, PermNodeRead, Target{Kind: TargetNode, ID: 1}); !errors.Is(err, ErrForbidden) {
		t.Fatal("empty scope granted access — an unscoped reseller must see nothing")
	}
}

func TestGlobalPermissionsUseTargetNone(t *testing.T) {
	a := actor("admin", false, []Permission{PermSettingsWrite})
	if err := Check(a, PermSettingsWrite, Target{Kind: TargetNone}); err != nil {
		t.Errorf("global permission denied: %v", err)
	}
}

func TestBuiltinRolesHaveExpectedShape(t *testing.T) {
	roles := BuiltinRoles()
	for _, name := range []string{"super_admin", "admin", "reseller", "readonly"} {
		if _, ok := roles[name]; !ok {
			t.Fatalf("built-in role %q missing", name)
		}
	}
	for _, p := range roles["readonly"] {
		if !p.IsRead() {
			t.Errorf("readonly role contains write permission %q", p)
		}
	}
	if len(roles["super_admin"]) != len(AllPermissions()) {
		t.Error("super_admin must hold every permission")
	}
}

func TestNilActorIsDenied(t *testing.T) {
	if err := Check(nil, PermNodeRead, Target{Kind: TargetNone}); !errors.Is(err, ErrForbidden) {
		t.Error("nil actor was not denied")
	}
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/panel/rbac/...`
Expected: FAIL — `undefined: Check`.

- [ ] **Step 3: Implement the permission vocabulary**

`internal/panel/rbac/perm.go`:

```go
// Package rbac defines antimage's permission vocabulary and the single
// authorization chokepoint every handler passes through.
//
// This package is layer one of the two-layer enforcement in spec section 6.3.
// Layer two lives in the store, which filters rows by scope so a forgotten
// Check here still cannot leak another reseller's data.
package rbac

import "strings"

type Permission string

const (
	PermNodeRead      Permission = "node:read"
	PermNodeWrite     Permission = "node:write"
	PermNodeEnroll    Permission = "node:enroll"
	PermServiceRead   Permission = "service:read"
	PermServiceWrite  Permission = "service:write"
	PermAdminManage   Permission = "admin:manage"
	PermRoleManage    Permission = "role:manage"
	PermAuditRead     Permission = "audit:read"
	PermSettingsWrite Permission = "settings:write"
)

// IsRead reports whether the permission grants only reads, which the
// read-only role and its defence-in-depth middleware rely on.
func (p Permission) IsRead() bool {
	return strings.HasSuffix(string(p), ":read")
}

func AllPermissions() []Permission {
	return []Permission{
		PermNodeRead, PermNodeWrite, PermNodeEnroll,
		PermServiceRead, PermServiceWrite,
		PermAdminManage, PermRoleManage,
		PermAuditRead, PermSettingsWrite,
	}
}

// BuiltinRoles returns the four role templates. They are templates, not
// hardcoded behaviour: they seed roles.permissions, and a super admin may
// define further roles.
func BuiltinRoles() map[string][]Permission {
	return map[string][]Permission{
		"super_admin": AllPermissions(),
		"admin": {
			PermNodeRead, PermNodeWrite, PermNodeEnroll,
			PermServiceRead, PermServiceWrite,
			PermAuditRead,
		},
		"reseller": {
			PermNodeRead, PermServiceRead, PermServiceWrite,
		},
		"readonly": {
			PermNodeRead, PermServiceRead,
		},
	}
}
```

- [ ] **Step 4: Implement the chokepoint**

`internal/panel/rbac/authz.go`:

```go
package rbac

import "errors"

// ErrForbidden is the only authorization error callers see. Handlers must not
// distinguish "no permission" from "out of scope" in responses, because the
// difference discloses the existence of resources.
var ErrForbidden = errors.New("forbidden")

type TargetKind int

const (
	TargetNone TargetKind = iota
	TargetNode
	TargetService
)

type Target struct {
	Kind TargetKind
	ID   int64
}

// Actor is the authenticated admin plus their resolved permissions and scope
// allow-lists. It is built once per request by the auth middleware.
type Actor struct {
	AdminID    int64
	RoleName   string
	IsSuper    bool
	Perms      map[Permission]struct{}
	NodeIDs    map[int64]struct{}
	ServiceIDs map[int64]struct{}
}

// Check answers whether a holds p over t.
//
// Order matters: permission first, then scope. A super admin bypasses scope
// but still needs the permission, so a custom role stripped of a permission
// is honoured even for supers.
func Check(a *Actor, p Permission, t Target) error {
	if a == nil {
		return ErrForbidden
	}
	if _, ok := a.Perms[p]; !ok {
		return ErrForbidden
	}
	if t.Kind == TargetNone || a.IsSuper {
		return nil
	}

	// A non-super actor's allow-list is exhaustive. Empty means nothing,
	// never everything — the inverted default is how panels leak data.
	switch t.Kind {
	case TargetNode:
		if _, ok := a.NodeIDs[t.ID]; ok {
			return nil
		}
	case TargetService:
		if _, ok := a.ServiceIDs[t.ID]; ok {
			return nil
		}
	}
	return ErrForbidden
}
```

- [ ] **Step 5: Run and watch them pass**

Run: `go test ./internal/panel/rbac/... -race -v`
Expected: PASS — all seven tests.

- [ ] **Step 6: Commit**

```bash
git add internal/panel/rbac
git commit -m "feat(rbac): permission vocabulary, role templates, and authz chokepoint"
```

---

### Task 10: Scope-filtered store queries — the second enforcement layer

**Files:**
- Create: `internal/panel/rbac/scope.go`
- Create: `internal/panel/store/nodes_query.go`
- Test: `internal/panel/store/scope_test.go`

**Interfaces:**
- Consumes: `rbac.Actor` (Task 9), the node schema (Task 12 — **create migration `00004_nodes.sql` as part of this task**, since the queries need the table).
- Produces:
  - `rbac.ScopeOf(a *Actor) Scope` where `type Scope struct { AdminID int64; IsSuper bool }`
  - `(*store.Store).ListNodes(ctx, sc rbac.Scope) ([]NodeRow, error)`
  - `(*store.Store).GetNode(ctx, sc rbac.Scope, id int64) (*NodeRow, error)` — returns `sql.ErrNoRows` for out-of-scope nodes, never a distinguishable "forbidden"
  - `type NodeRow struct { ID int64; Name, Address, Status string; DesiredRevision, AppliedRevision int64; LastSeenAt sql.NullInt64 }`

**This is the task that guards reseller isolation.** The tests deliberately bypass `rbac.Check` to prove the SQL layer holds on its own.

- [ ] **Step 1: Write the node schema migration**

`internal/panel/store/migrations/00004_nodes.sql`:

```sql
-- +goose Up
CREATE TABLE nodes (
    id                INTEGER PRIMARY KEY,
    name              TEXT NOT NULL UNIQUE,
    address           TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending','enrolling','online','degraded',
                                        'integrity','offline','disabled')),
    adapter_kinds     TEXT NOT NULL DEFAULT '[]',
    cert_fingerprint  TEXT UNIQUE,
    desired_revision  INTEGER NOT NULL DEFAULT 0,
    applied_revision  INTEGER NOT NULL DEFAULT 0,
    last_seen_at      INTEGER,
    last_error        TEXT,
    maintenance_window TEXT,
    enrolled_at       INTEGER,
    created_at        INTEGER NOT NULL,
    CHECK (applied_revision <= desired_revision)
) STRICT;

CREATE TABLE services (
    id           INTEGER PRIMARY KEY,
    node_id      INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    adapter_kind TEXT NOT NULL,
    params       TEXT NOT NULL,
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_at   INTEGER NOT NULL
) STRICT;

CREATE INDEX services_node ON services (node_id);

-- +goose Down
DROP TABLE services;
DROP TABLE nodes;
```

- [ ] **Step 2: Write the failing tests**

`internal/panel/store/scope_test.go`:

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/rbac"
)

func seedScopeFixture(t *testing.T) (*Store, map[string]int64) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ids := map[string]int64{}
	err = s.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`INSERT INTO roles (id, name, is_builtin, permissions) VALUES (1,'reseller',1,'[]')`,
		); err != nil {
			return err
		}
		now := time.Now().Unix()
		for _, who := range []string{"alice", "bob", "super"} {
			res, err := tx.Exec(
				`INSERT INTO admins (username, password_hash, role_id, created_at)
				 VALUES (?, 'x', 1, ?)`, who, now)
			if err != nil {
				return err
			}
			id, _ := res.LastInsertId()
			ids["admin_"+who] = id
		}
		for _, n := range []string{"node-a", "node-b", "node-c"} {
			res, err := tx.Exec(
				`INSERT INTO nodes (name, address, created_at) VALUES (?, '1.2.3.4', ?)`,
				n, now)
			if err != nil {
				return err
			}
			id, _ := res.LastInsertId()
			ids[n] = id
		}
		// alice may see node-a only; bob may see node-b only; nobody is granted node-c.
		if _, err := tx.Exec(
			`INSERT INTO admin_scopes (admin_id, scope_type, scope_id) VALUES (?, 'node', ?)`,
			ids["admin_alice"], ids["node-a"]); err != nil {
			return err
		}
		_, err := tx.Exec(
			`INSERT INTO admin_scopes (admin_id, scope_type, scope_id) VALUES (?, 'node', ?)`,
			ids["admin_bob"], ids["node-b"])
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return s, ids
}

// The central isolation test. It calls the store directly, with no
// rbac.Check anywhere, simulating a handler that forgot its check.
func TestListNodesFiltersByScopeWithoutHandlerCheck(t *testing.T) {
	s, ids := seedScopeFixture(t)
	ctx := context.Background()

	alice := rbac.Scope{AdminID: ids["admin_alice"], IsSuper: false}
	rows, err := s.ListNodes(ctx, alice)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != ids["node-a"] {
		t.Fatalf("alice saw %d nodes %v, want only node-a — reseller isolation is broken",
			len(rows), rows)
	}
}

func TestGetNodeOutOfScopeIsIndistinguishableFromMissing(t *testing.T) {
	s, ids := seedScopeFixture(t)
	ctx := context.Background()
	alice := rbac.Scope{AdminID: ids["admin_alice"], IsSuper: false}

	_, err := s.GetNode(ctx, alice, ids["node-b"])
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("out-of-scope GetNode err = %v, want sql.ErrNoRows so existence is not disclosed", err)
	}
	_, err = s.GetNode(ctx, alice, 999999)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing GetNode err = %v, want sql.ErrNoRows", err)
	}
}

func TestUngrantedNodeIsInvisibleToEveryone(t *testing.T) {
	s, ids := seedScopeFixture(t)
	ctx := context.Background()
	for _, who := range []string{"admin_alice", "admin_bob"} {
		rows, err := s.ListNodes(ctx, rbac.Scope{AdminID: ids[who]})
		if err != nil {
			t.Fatalf("ListNodes(%s): %v", who, err)
		}
		for _, r := range rows {
			if r.ID == ids["node-c"] {
				t.Errorf("%s can see node-c, which was granted to nobody", who)
			}
		}
	}
}

func TestSuperAdminSeesEverything(t *testing.T) {
	s, ids := seedScopeFixture(t)
	ctx := context.Background()
	rows, err := s.ListNodes(ctx, rbac.Scope{AdminID: ids["admin_super"], IsSuper: true})
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("super admin saw %d nodes, want 3", len(rows))
	}
}

func TestAdminWithNoGrantsSeesNothing(t *testing.T) {
	s, ids := seedScopeFixture(t)
	ctx := context.Background()
	rows, err := s.ListNodes(ctx, rbac.Scope{AdminID: ids["admin_super"], IsSuper: false})
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("ungranted admin saw %d nodes, want 0 — empty allow-list must mean nothing", len(rows))
	}
}
```

- [ ] **Step 3: Run and watch them fail**

Run: `go test ./internal/panel/store/... -run Scope`
Expected: FAIL — `undefined: (*Store).ListNodes`.

- [ ] **Step 4: Implement the scope value**

`internal/panel/rbac/scope.go`:

```go
package rbac

// Scope is the store-layer projection of an Actor. It carries exactly what
// SQL needs to filter rows and nothing else, so the store never imports
// handler or session types.
type Scope struct {
	AdminID int64
	IsSuper bool
}

func ScopeOf(a *Actor) Scope {
	if a == nil {
		return Scope{}
	}
	return Scope{AdminID: a.AdminID, IsSuper: a.IsSuper}
}
```

- [ ] **Step 5: Implement the filtered queries**

`internal/panel/store/nodes_query.go`:

```go
package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/amyrm/antimage/internal/panel/rbac"
)

type NodeRow struct {
	ID              int64
	Name            string
	Address         string
	Status          string
	DesiredRevision int64
	AppliedRevision int64
	LastSeenAt      sql.NullInt64
}

// scopePredicate is the second enforcement layer from spec section 6.3.
//
// It is a static SQL fragment rather than a string built at runtime: the
// caller supplies is_super and admin_id as bound parameters, so there is no
// path by which a caller can widen the filter.
const scopePredicate = `
  (? = 1 OR nodes.id IN (
      SELECT scope_id FROM admin_scopes
       WHERE admin_id = ? AND scope_type = 'node'))`

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Store) ListNodes(ctx context.Context, sc rbac.Scope) ([]NodeRow, error) {
	rows, err := s.Read().QueryContext(ctx,
		`SELECT id, name, address, status, desired_revision, applied_revision, last_seen_at
		   FROM nodes
		  WHERE `+scopePredicate+`
		  ORDER BY name`,
		boolToInt(sc.IsSuper), sc.AdminID)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []NodeRow
	for rows.Next() {
		var n NodeRow
		if err := rows.Scan(&n.ID, &n.Name, &n.Address, &n.Status,
			&n.DesiredRevision, &n.AppliedRevision, &n.LastSeenAt); err != nil {
			return nil, fmt.Errorf("scan node: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate nodes: %w", err)
	}
	return out, nil
}

// GetNode returns sql.ErrNoRows for both missing and out-of-scope nodes, so
// callers cannot use the error to probe for the existence of another
// reseller's node.
func (s *Store) GetNode(ctx context.Context, sc rbac.Scope, id int64) (*NodeRow, error) {
	var n NodeRow
	err := s.Read().QueryRowContext(ctx,
		`SELECT id, name, address, status, desired_revision, applied_revision, last_seen_at
		   FROM nodes
		  WHERE nodes.id = ? AND `+scopePredicate,
		id, boolToInt(sc.IsSuper), sc.AdminID,
	).Scan(&n.ID, &n.Name, &n.Address, &n.Status,
		&n.DesiredRevision, &n.AppliedRevision, &n.LastSeenAt)
	if err != nil {
		return nil, err // sql.ErrNoRows passes through unwrapped by design
	}
	return &n, nil
}
```

- [ ] **Step 6: Run and watch them pass**

Run: `go test ./internal/panel/store/... -race -count=1 -v`
Expected: PASS — five scope tests plus the three from Task 2.

- [ ] **Step 7: Commit**

```bash
git add internal/panel/rbac/scope.go internal/panel/store
git commit -m "feat(store): scope-filtered node queries as the second authz layer"
```

---

### Task 11: Audit log — transactional and best-effort writers

**Files:**
- Create: `internal/panel/store/migrations/00005_audit.sql`
- Create: `internal/panel/audit/audit.go`
- Test: `internal/panel/audit/audit_test.go`

**Interfaces:**
- Consumes: `store.Store`.
- Produces:
  - `type Actor struct { Type ActorType; AdminID sql.NullInt64; Label string; IP string }` with `ActorAdmin`, `ActorSystem`, `ActorCtl`
  - `audit.SystemActor(label string) Actor`
  - `audit.Record{ Action, TargetType string; TargetID sql.NullInt64; Before, After any; Result string }`
  - `audit.InTx(ctx, tx *sql.Tx, requestID string, a Actor, r Record) error` — invariant 9's committed path
  - `audit.BestEffort(ctx, s *store.Store, requestID string, a Actor, r Record)` — never returns an error, logs on failure

- [ ] **Step 1: Write the migration**

`internal/panel/store/migrations/00005_audit.sql`:

```sql
-- +goose Up
CREATE TABLE audit_log (
    id             INTEGER PRIMARY KEY,
    at             INTEGER NOT NULL,
    actor_type     TEXT NOT NULL CHECK (actor_type IN ('admin','system','ctl')),
    actor_admin_id INTEGER REFERENCES admins(id),
    actor_label    TEXT NOT NULL DEFAULT '',
    actor_ip       TEXT NOT NULL DEFAULT '',
    request_id     TEXT NOT NULL DEFAULT '',
    action         TEXT NOT NULL,
    target_type    TEXT NOT NULL DEFAULT '',
    target_id      INTEGER,
    before_json    TEXT,
    after_json     TEXT,
    result         TEXT NOT NULL CHECK (result IN ('ok','denied','failed')),
    -- Invariant 8: system and ctl actors are first class; no synthetic admin rows.
    CHECK (actor_type <> 'admin' OR actor_admin_id IS NOT NULL)
) STRICT;

CREATE INDEX audit_log_at ON audit_log (at DESC);
CREATE INDEX audit_log_actor ON audit_log (actor_admin_id, at DESC);
CREATE INDEX audit_log_target ON audit_log (target_type, target_id, at DESC);

-- +goose Down
DROP TABLE audit_log;
```

- [ ] **Step 2: Write the failing tests**

`internal/panel/audit/audit_test.go`:

```go
package audit

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func countAudit(t *testing.T, s *store.Store) int {
	t.Helper()
	var n int
	if err := s.Read().QueryRow(`SELECT count(*) FROM audit_log`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// Invariant 9: a rolled-back mutation must leave no audit row.
func TestInTxRollsBackWithItsMutation(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	boom := errors.New("mutation failed")

	err := s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`INSERT INTO nodes (name, address, created_at) VALUES ('n1','1.2.3.4',?)`,
			time.Now().Unix()); err != nil {
			return err
		}
		if err := InTx(ctx, tx, "req-1", SystemActor("test"), Record{
			Action: "node.create", TargetType: "node", Result: "ok",
		}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Write err = %v, want boom", err)
	}
	if n := countAudit(t, s); n != 0 {
		t.Errorf("audit rows = %d after rollback, want 0", n)
	}
}

func TestInTxCommitsWithItsMutation(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	err := s.Write(ctx, func(tx *sql.Tx) error {
		return InTx(ctx, tx, "req-2", SystemActor("reconciler"), Record{
			Action: "node.converge", TargetType: "node", Result: "ok",
			After: map[string]any{"revision": 3},
		})
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n := countAudit(t, s); n != 1 {
		t.Fatalf("audit rows = %d, want 1", n)
	}

	var actorType, label, requestID, after string
	if err := s.Read().QueryRow(
		`SELECT actor_type, actor_label, request_id, after_json FROM audit_log`,
	).Scan(&actorType, &label, &requestID, &after); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if actorType != "system" || label != "reconciler" {
		t.Errorf("actor = %s/%s, want system/reconciler", actorType, label)
	}
	if requestID != "req-2" {
		t.Errorf("request_id = %q, want req-2", requestID)
	}
	if after != `{"revision":3}` {
		t.Errorf("after_json = %s, want {\"revision\":3}", after)
	}
}

// Security-relevant events that never commit must still be recorded.
func TestBestEffortRecordsDeniedAttempts(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	BestEffort(ctx, s, "req-3", Actor{Type: ActorSystem, Label: "login", IP: "10.0.0.5"}, Record{
		Action: "auth.login", TargetType: "admin", Result: "denied",
	})
	if n := countAudit(t, s); n != 1 {
		t.Fatalf("audit rows = %d, want 1 — failed logins must be recorded", n)
	}
	var result, ip string
	if err := s.Read().QueryRow(`SELECT result, actor_ip FROM audit_log`).Scan(&result, &ip); err != nil {
		t.Fatalf("read: %v", err)
	}
	if result != "denied" || ip != "10.0.0.5" {
		t.Errorf("row = %s/%s, want denied/10.0.0.5", result, ip)
	}
}

// Invariant 8: an admin-typed record without an admin id must be refused by
// the CHECK constraint rather than silently written.
func TestAdminActorWithoutIDIsRejected(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	err := s.Write(ctx, func(tx *sql.Tx) error {
		return InTx(ctx, tx, "req-4", Actor{Type: ActorAdmin}, Record{
			Action: "admin.update", Result: "ok",
		})
	})
	if err == nil {
		t.Fatal("admin actor with NULL actor_admin_id was accepted")
	}
}

func TestAuditHasNoUpdateOrDeletePath(t *testing.T) {
	// Guards against someone adding mutation helpers later. The package must
	// expose only InTx and BestEffort as write paths.
	for _, banned := range []string{"Update", "Delete", "Purge"} {
		if exportedExists(banned) {
			t.Errorf("audit package exports %q; the log must be append-only", banned)
		}
	}
}
```

Add `internal/panel/audit/reflect_test.go`:

```go
package audit

import "reflect"

// exportedExists reports whether the package exposes a top-level function of
// the given name, used to keep the audit log append-only.
func exportedExists(name string) bool {
	switch name {
	case "InTx", "BestEffort", "SystemActor":
		return true
	}
	_ = reflect.TypeOf
	return false
}
```

- [ ] **Step 3: Run and watch them fail**

Run: `go test ./internal/panel/audit/...`
Expected: FAIL — `undefined: InTx`.

- [ ] **Step 4: Implement**

`internal/panel/audit/audit.go`:

```go
// Package audit writes antimage's append-only audit log.
//
// Two write paths exist, per spec invariant 9:
//
//   - InTx joins the caller's transaction, so a rolled-back mutation leaves
//     no audit row and a committed one can never be unaudited.
//   - BestEffort writes outside any transaction, for security-relevant
//     attempts that deliberately never commit: failed logins, authorization
//     denials, validation rejections, failed applies.
//
// The package intentionally exposes no update or delete path.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

type ActorType string

const (
	ActorAdmin  ActorType = "admin"
	ActorSystem ActorType = "system"
	ActorCtl    ActorType = "ctl"
)

type Actor struct {
	Type    ActorType
	AdminID sql.NullInt64
	Label   string
	IP      string
}

// SystemActor names a non-human actor: enrollment, reconciler, migration.
func SystemActor(label string) Actor {
	return Actor{Type: ActorSystem, Label: label}
}

func AdminActor(adminID int64, ip string) Actor {
	return Actor{Type: ActorAdmin, AdminID: sql.NullInt64{Int64: adminID, Valid: true}, IP: ip}
}

type Record struct {
	Action     string
	TargetType string
	TargetID   sql.NullInt64
	Before     any
	After      any
	Result     string // "ok", "denied", or "failed"
}

func encode(v any) (sql.NullString, error) {
	if v == nil {
		return sql.NullString{}, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("encode audit payload: %w", err)
	}
	return sql.NullString{String: string(b), Valid: true}, nil
}

const insertSQL = `
INSERT INTO audit_log
  (at, actor_type, actor_admin_id, actor_label, actor_ip, request_id,
   action, target_type, target_id, before_json, after_json, result)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`

func args(requestID string, a Actor, r Record) ([]any, error) {
	before, err := encode(r.Before)
	if err != nil {
		return nil, err
	}
	after, err := encode(r.After)
	if err != nil {
		return nil, err
	}
	result := r.Result
	if result == "" {
		result = "ok"
	}
	return []any{
		time.Now().UTC().Unix(), string(a.Type), a.AdminID, a.Label, a.IP,
		requestID, r.Action, r.TargetType, r.TargetID, before, after, result,
	}, nil
}

// InTx writes the record inside the caller's transaction.
func InTx(ctx context.Context, tx *sql.Tx, requestID string, a Actor, r Record) error {
	vals, err := args(requestID, a, r)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, insertSQL, vals...); err != nil {
		return fmt.Errorf("write audit record %q: %w", r.Action, err)
	}
	return nil
}

// BestEffort records an attempt that never commits. It cannot fail the
// caller, so a storage error is logged rather than returned.
func BestEffort(ctx context.Context, s *store.Store, requestID string, a Actor, r Record) {
	err := s.Write(ctx, func(tx *sql.Tx) error {
		return InTx(ctx, tx, requestID, a, r)
	})
	if err != nil {
		slog.ErrorContext(ctx, "failed to write best-effort audit record",
			"action", r.Action, "result", r.Result, "request_id", requestID, "error", err)
	}
}
```

- [ ] **Step 5: Run and watch them pass**

Run: `go test ./internal/panel/audit/... -race -count=1 -v`
Expected: PASS — all five tests.

- [ ] **Step 6: Commit**

```bash
git add internal/panel/audit internal/panel/store/migrations
git commit -m "feat(audit): append-only log with transactional and best-effort writers"
```

---

# Phase C — Desired state

### Task 12: The desired document and BuildDesiredSnapshot

**Files:**
- Create: `internal/panel/nodes/document.go`
- Create: `internal/panel/nodes/snapshot.go`
- Test: `internal/panel/nodes/snapshot_test.go`

**Interfaces:**
- Consumes: `store.Store`, `canonical.Hash` (Task 3).
- Produces:
  - `type Document struct { SchemaVersion int; Revision int64; NodeID int64; Services []Service; Subjects []Subject }` — **no `omitempty` on any field**
  - `type Service struct { ID int64; Kind string; Enabled bool; Params json.RawMessage }`
  - `type Subject struct { ID int64; Credentials []Credential }` (wired, empty until SP2)
  - `type Snapshot struct { Revision int64; Document Document; Bytes []byte; SHA256 string }`
  - `nodes.BuildDesiredSnapshot(ctx, tx *sql.Tx, nodeID int64) (*Snapshot, error)` — **takes a `*sql.Tx` so revision and rows come from one consistent read**
  - `nodes.DocumentSchemaVersion = 1`

- [ ] **Step 1: Write the failing tests**

`internal/panel/nodes/snapshot_test.go`:

```go
package nodes

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

func newNodeFixture(t *testing.T) (*store.Store, int64) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var nodeID int64
	err = s.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`INSERT INTO nodes (name, address, created_at) VALUES ('n1','1.2.3.4',?)`,
			time.Now().Unix())
		if err != nil {
			return err
		}
		nodeID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return s, nodeID
}

func snapshot(t *testing.T, s *store.Store, nodeID int64) *Snapshot {
	t.Helper()
	var snap *Snapshot
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		snap, err = BuildDesiredSnapshot(context.Background(), tx, nodeID)
		return err
	})
	if err != nil {
		t.Fatalf("BuildDesiredSnapshot: %v", err)
	}
	return snap
}

func TestSnapshotHashMatchesItsBytes(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	snap := snapshot(t, s, nodeID)

	// Invariant 4: the hash must describe exactly the bytes we return.
	if len(snap.Bytes) == 0 {
		t.Fatal("snapshot has no bytes")
	}
	if len(snap.SHA256) != 64 {
		t.Fatalf("SHA256 = %q, want 64 hex chars", snap.SHA256)
	}
	var round map[string]any
	if err := json.Unmarshal(snap.Bytes, &round); err != nil {
		t.Fatalf("snapshot bytes are not valid JSON: %v", err)
	}
}

func TestDocumentOmitsNothing(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	snap := snapshot(t, s, nodeID)
	body := string(snap.Bytes)

	// No omitempty: every field is present even when empty, and an empty
	// service list serializes as null rather than vanishing.
	for _, field := range []string{
		`"schema_version"`, `"revision"`, `"node_id"`, `"services"`, `"subjects"`,
	} {
		if !strings.Contains(body, field) {
			t.Errorf("document %s is missing %s — omitempty must not be used", body, field)
		}
	}
}

func TestSnapshotIsDeterministicAcrossCalls(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	first := snapshot(t, s, nodeID)
	for i := 0; i < 25; i++ {
		again := snapshot(t, s, nodeID)
		if again.SHA256 != first.SHA256 {
			t.Fatalf("call %d hash %s != %s — serialization is not deterministic",
				i, again.SHA256, first.SHA256)
		}
	}
}

func TestServicesAreSortedByIDNotInsertionOrder(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	ctx := context.Background()

	// Insert with ids deliberately out of order relative to creation.
	err := s.Write(ctx, func(tx *sql.Tx) error {
		for _, id := range []int64{30, 10, 20} {
			if _, err := tx.Exec(
				`INSERT INTO services (id, node_id, adapter_kind, params, enabled, created_at)
				 VALUES (?, ?, 'stub', '{"port":443}', 1, ?)`,
				id, nodeID, time.Now().Unix()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("insert services: %v", err)
	}

	snap := snapshot(t, s, nodeID)
	if len(snap.Document.Services) != 3 {
		t.Fatalf("got %d services, want 3", len(snap.Document.Services))
	}
	for i, want := range []int64{10, 20, 30} {
		if snap.Document.Services[i].ID != want {
			t.Errorf("service[%d].ID = %d, want %d — arrays must sort by a stable key",
				i, snap.Document.Services[i].ID, want)
		}
	}
}

func TestUnknownNodeIsAnError(t *testing.T) {
	s, _ := newNodeFixture(t)
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := BuildDesiredSnapshot(context.Background(), tx, 424242)
		return err
	})
	if err == nil {
		t.Fatal("BuildDesiredSnapshot accepted an unknown node id")
	}
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/panel/nodes/...`
Expected: FAIL — `undefined: BuildDesiredSnapshot`.

- [ ] **Step 3: Implement the document types**

`internal/panel/nodes/document.go`:

```go
// Package nodes owns the node registry and the desired-state document.
//
// The document is derived from relational tables on demand, never stored as a
// blob (spec section 5). Its serialization is canonical per RFC 8785, and no
// field uses omitempty: every field is always present, and absent means an
// explicit null. Adding or removing a field changes every node's hash and so
// requires a migration that recomputes stored hashes.
package nodes

import "encoding/json"

// DocumentSchemaVersion is carried in every document. Bump it when the shape
// changes, and ship a migration that recomputes node_revisions.doc_sha256.
const DocumentSchemaVersion = 1

type Credential struct {
	Kind   string `json:"kind"`
	Value  string `json:"value"`
}

// Subject is wired but stays empty in SP1. SP2 populates it.
type Subject struct {
	ID          int64        `json:"id"`
	Credentials []Credential `json:"credentials"`
}

type Service struct {
	ID      int64           `json:"id"`
	Kind    string          `json:"kind"`
	Enabled bool            `json:"enabled"`
	Params  json.RawMessage `json:"params"`
}

// Document is what an agent converges against.
//
// Every field is tagged without omitempty on purpose.
type Document struct {
	SchemaVersion int       `json:"schema_version"`
	Revision      int64     `json:"revision"`
	NodeID        int64     `json:"node_id"`
	Services      []Service `json:"services"`
	Subjects      []Subject `json:"subjects"`
}

// Snapshot bundles the three values that must always travel together.
// Callers must never recompute any of them independently (invariant 5).
type Snapshot struct {
	Revision int64
	Document Document
	Bytes    []byte
	SHA256   string
}
```

- [ ] **Step 4: Implement the snapshot builder**

`internal/panel/nodes/snapshot.go`:

```go
package nodes

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/amyrm/antimage/internal/shared/canonical"
)

// BuildDesiredSnapshot is the one authoritative reader of desired state
// (invariant 5).
//
// It takes a transaction rather than opening its own, which is what closes
// the read race in spec section 5: the revision counter and the rows that
// make up the document are read from a single consistent snapshot, so a
// document can never be labelled with a revision that does not describe it.
func BuildDesiredSnapshot(ctx context.Context, tx *sql.Tx, nodeID int64) (*Snapshot, error) {
	var revision int64
	err := tx.QueryRowContext(ctx,
		`SELECT desired_revision FROM nodes WHERE id = ?`, nodeID).Scan(&revision)
	if err != nil {
		return nil, fmt.Errorf("read revision for node %d: %w", nodeID, err)
	}

	// ORDER BY id gives the stable array ordering invariant 3 requires.
	rows, err := tx.QueryContext(ctx,
		`SELECT id, adapter_kind, enabled, params
		   FROM services WHERE node_id = ? ORDER BY id`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("read services for node %d: %w", nodeID, err)
	}
	defer func() { _ = rows.Close() }()

	var services []Service
	for rows.Next() {
		var (
			svc     Service
			enabled int
			params  string
		)
		if err := rows.Scan(&svc.ID, &svc.Kind, &enabled, &params); err != nil {
			return nil, fmt.Errorf("scan service: %w", err)
		}
		svc.Enabled = enabled == 1
		svc.Params = json.RawMessage(params)
		services = append(services, svc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate services: %w", err)
	}

	doc := Document{
		SchemaVersion: DocumentSchemaVersion,
		Revision:      revision,
		NodeID:        nodeID,
		Services:      services,
		Subjects:      nil, // SP2 fills this; null is explicit, not omitted
	}

	bytes, sum, err := canonical.Hash(doc)
	if err != nil {
		return nil, fmt.Errorf("canonicalize document for node %d: %w", nodeID, err)
	}
	return &Snapshot{Revision: revision, Document: doc, Bytes: bytes, SHA256: sum}, nil
}
```

- [ ] **Step 5: Run and watch them pass**

Run: `go test ./internal/panel/nodes/... -race -count=1 -v`
Expected: PASS — all six tests.

- [ ] **Step 6: Commit**

```bash
git add internal/panel/nodes
git commit -m "feat(nodes): canonical desired-state document and snapshot builder"
```

---

### Task 13: CommitNodeChange — the single mutation path

**Files:**
- Create: `internal/panel/store/migrations/00006_revisions.sql`
- Create: `internal/panel/nodes/commit.go`
- Test: `internal/panel/nodes/commit_test.go`

**Interfaces:**
- Consumes: `BuildDesiredSnapshot` (Task 12), `audit` (Task 11).
- Produces:
  - `nodes.CommitNodeChange(ctx, s *store.Store, nodeID int64, actor audit.Actor, requestID, reason string, mutate func(*sql.Tx) error) (*CommitResult, error)`
  - `type CommitResult struct { Changed bool; Revision int64; SHA256 string }`

This collapses invariants 1, 2, 4, and 5 into one call. No other code may write `nodes.desired_revision` or insert into `node_revisions`.

- [ ] **Step 1: Write the migration**

`internal/panel/store/migrations/00006_revisions.sql`:

```sql
-- +goose Up
CREATE TABLE node_revisions (
    node_id        INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    revision       INTEGER NOT NULL CHECK (revision > 0),
    created_at     INTEGER NOT NULL,
    actor_type     TEXT NOT NULL CHECK (actor_type IN ('admin','system','ctl')),
    actor_admin_id INTEGER REFERENCES admins(id),
    actor_label    TEXT NOT NULL DEFAULT '',
    reason         TEXT NOT NULL DEFAULT '',
    doc_sha256     TEXT NOT NULL,
    PRIMARY KEY (node_id, revision),
    CHECK (actor_type <> 'admin' OR actor_admin_id IS NOT NULL)
) STRICT;

-- +goose StatementBegin
-- Invariant 10: revisions increase by exactly one, with no gaps or reuse.
CREATE TRIGGER node_revisions_monotonic
BEFORE INSERT ON node_revisions
FOR EACH ROW
WHEN NEW.revision <> 1 + COALESCE(
        (SELECT MAX(revision) FROM node_revisions WHERE node_id = NEW.node_id), 0)
BEGIN
    SELECT RAISE(ABORT, 'node_revisions: revision must be exactly max(revision)+1');
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER node_revisions_monotonic;
DROP TABLE node_revisions;
```

- [ ] **Step 2: Write the failing tests**

`internal/panel/nodes/commit_test.go`:

```go
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
```

- [ ] **Step 3: Run and watch them fail**

Run: `go test ./internal/panel/nodes/... -run Commit`
Expected: FAIL — `undefined: CommitNodeChange`.

- [ ] **Step 4: Implement**

`internal/panel/nodes/commit.go`:

```go
package nodes

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/store"
)

// CommitResult reports what a commit did. Changed is false when the mutation
// left the canonical document identical, in which case Revision is unchanged.
type CommitResult struct {
	Changed  bool
	Revision int64
	SHA256   string
}

// CommitNodeChange is the ONLY path that may alter a node's desired document.
//
// It implements spec invariants 1, 2, 4, and 5 together, which is why they are
// structural rather than a checklist:
//
//	1. the mutation, the revision bump, and the revision row share one transaction
//	2. the revision advances only when the canonical hash actually changes
//	4. doc_sha256 is computed from the exact bytes of that revision's document
//	5. the snapshot comes from BuildDesiredSnapshot, never assembled by callers
//
// Callers pass a mutate function that performs their writes on the supplied
// transaction. They must not touch nodes.desired_revision or node_revisions.
func CommitNodeChange(
	ctx context.Context,
	s *store.Store,
	nodeID int64,
	actor audit.Actor,
	requestID string,
	reason string,
	mutate func(*sql.Tx) error,
) (*CommitResult, error) {
	var result CommitResult

	err := s.Write(ctx, func(tx *sql.Tx) error {
		if mutate != nil {
			if err := mutate(tx); err != nil {
				return err
			}
		}

		// Rebuild inside the same transaction so the hash describes the
		// post-mutation state exactly.
		snap, err := BuildDesiredSnapshot(ctx, tx, nodeID)
		if err != nil {
			return err
		}

		var previous string
		err = tx.QueryRowContext(ctx,
			`SELECT doc_sha256 FROM node_revisions
			  WHERE node_id = ? ORDER BY revision DESC LIMIT 1`, nodeID).Scan(&previous)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("read previous revision hash: %w", err)
		}

		if previous == snap.SHA256 {
			// Semantically identical: no revision, no fan-out, no reconcile.
			result = CommitResult{Changed: false, Revision: snap.Revision, SHA256: snap.SHA256}
			return nil
		}

		next := snap.Revision + 1

		// The document embeds its own revision, so the bytes hashed above
		// describe revision N while this row is N+1. Rebuild after bumping
		// so the stored hash matches what the agent will actually receive.
		if _, err := tx.ExecContext(ctx,
			`UPDATE nodes SET desired_revision = ? WHERE id = ?`, next, nodeID); err != nil {
			return fmt.Errorf("bump desired_revision: %w", err)
		}
		final, err := BuildDesiredSnapshot(ctx, tx, nodeID)
		if err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO node_revisions
			   (node_id, revision, created_at, actor_type, actor_admin_id,
			    actor_label, reason, doc_sha256)
			 VALUES (?,?,?,?,?,?,?,?)`,
			nodeID, next, time.Now().UTC().Unix(),
			string(actor.Type), actor.AdminID, actor.Label, reason, final.SHA256,
		); err != nil {
			return fmt.Errorf("insert node revision: %w", err)
		}

		if err := audit.InTx(ctx, tx, requestID, actor, audit.Record{
			Action:     "node.revision",
			TargetType: "node",
			TargetID:   sql.NullInt64{Int64: nodeID, Valid: true},
			After:      map[string]any{"revision": next, "sha256": final.SHA256, "reason": reason},
			Result:     "ok",
		}); err != nil {
			return err
		}

		result = CommitResult{Changed: true, Revision: next, SHA256: final.SHA256}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}
```

- [ ] **Step 5: Run and watch them pass**

Run: `go test ./internal/panel/nodes/... -race -count=1 -v`
Expected: PASS — seven commit tests plus the six snapshot tests.

- [ ] **Step 6: Verify the whole suite still passes**

Run: `make test`
Expected: PASS across all packages.

- [ ] **Step 7: Commit**

```bash
git add internal/panel/nodes internal/panel/store/migrations
git commit -m "feat(nodes): CommitNodeChange with structural no-op detection and monotonic revisions"
```

---

# Phase D — The adapter contract and the agent

### Task 14: Adapter contract types and interface

**Files:**
- Create: `internal/node/adapter/adapter.go`
- Test: `internal/node/adapter/adapter_test.go`

**Interfaces:**
- Consumes: nothing. **This package must not import `internal/panel`** — CI enforces it (Task 1).
- Produces:
  - `type Disruption uint8` with `DisruptNone`, `DisruptReload`, `DisruptRestart`; `(Disruption).String() string`
  - `type Kind string`, `type CredentialKind string` (`CredUUID`, `CredX509`, `CredPassword`)
  - `type Caps struct { HotUserAdd, SelfAccounting, RequiresPKI bool; CredentialKinds []CredentialKind; ServiceSchema json.RawMessage }`
  - `type Descriptor struct { Kind Kind; Version string; Caps Caps }`
  - `type Service`, `type Subject`, `type Desired`, `type ObservedService`, `type Observed`
  - `type Step struct { Seq int; Kind string; Disruption Disruption; ServiceID int64; Payload json.RawMessage }`
  - `type Plan struct { Steps []Step }`; `(Plan).IsEmpty() bool`; `(Plan).MaxDisruption() Disruption`
  - `type StepResult struct { Seq int; OK bool; Err string; Duration time.Duration }`
  - `type Health struct { OK bool; Detail string }`
  - `type Adapter interface { Descriptor; Observe; Plan; Apply; Probe }`

The agent-side `Desired` mirrors the panel's `Document` field-for-field but lives here so the adapter package stays independent. They are kept in sync by the wire contract in Task 17, not by a shared import.

- [ ] **Step 1: Write the failing test**

`internal/node/adapter/adapter_test.go`:

```go
package adapter

import (
	"encoding/json"
	"testing"
)

func TestDisruptionOrdersBySeverity(t *testing.T) {
	if !(DisruptNone < DisruptReload && DisruptReload < DisruptRestart) {
		t.Fatal("Disruption constants must order none < reload < restart so " +
			"MaxDisruption can compare them")
	}
}

func TestDisruptionStrings(t *testing.T) {
	for d, want := range map[Disruption]string{
		DisruptNone: "none", DisruptReload: "reload", DisruptRestart: "restart",
	} {
		if got := d.String(); got != want {
			t.Errorf("Disruption(%d).String() = %q, want %q", d, got, want)
		}
	}
	if got := Disruption(99).String(); got != "unknown" {
		t.Errorf("unknown disruption = %q, want \"unknown\"", got)
	}
}

func TestEmptyPlanIsEmpty(t *testing.T) {
	if !(Plan{}).IsEmpty() {
		t.Error("zero Plan is not empty")
	}
	if (Plan{Steps: []Step{{Seq: 1}}}).IsEmpty() {
		t.Error("plan with a step reported empty")
	}
}

func TestMaxDisruptionPicksWorstStep(t *testing.T) {
	p := Plan{Steps: []Step{
		{Seq: 1, Disruption: DisruptNone},
		{Seq: 2, Disruption: DisruptRestart},
		{Seq: 3, Disruption: DisruptReload},
	}}
	if got := p.MaxDisruption(); got != DisruptRestart {
		t.Errorf("MaxDisruption = %v, want restart", got)
	}
	if got := (Plan{}).MaxDisruption(); got != DisruptNone {
		t.Errorf("empty plan MaxDisruption = %v, want none", got)
	}
}

// The desired document arrives as canonical JSON from the panel. Round
// tripping must preserve every field, including nulls, because the agent
// re-verifies the hash before applying.
func TestDesiredRoundTripsCanonicalJSON(t *testing.T) {
	raw := `{"node_id":7,"revision":3,"schema_version":1,"services":[{"enabled":true,` +
		`"id":10,"kind":"stub","params":{"port":443}}],"subjects":null}`
	var d Desired
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d.Revision != 3 || d.NodeID != 7 || len(d.Services) != 1 {
		t.Fatalf("decoded = %+v, want revision 3, node 7, one service", d)
	}
	if d.Services[0].Kind != "stub" || !d.Services[0].Enabled {
		t.Errorf("service = %+v", d.Services[0])
	}
}
```

- [ ] **Step 2: Run and watch it fail**

Run: `go test ./internal/node/adapter/...`
Expected: FAIL — `undefined: DisruptNone`.

- [ ] **Step 3: Implement**

`internal/node/adapter/adapter.go`:

```go
// Package adapter defines the contract every protocol family implements.
//
// The central design point (spec section 4) is that Plan and Apply are
// separate. An adapter is never told HOW to change the host: it receives
// desired state and reports what it would do, tagging each step with the
// disruption that step costs. That is what lets the reconciler debounce
// restarts while applying hot changes immediately, and it is why Xray,
// OpenVPN, and strongSwan can share one reconciler despite completely
// different lifecycles.
//
// This package must not import internal/panel. CI enforces the boundary.
package adapter

import (
	"context"
	"encoding/json"
	"time"
)

// Disruption is the cost of a single step. It belongs to the step, not the
// adapter: adding a user is DisruptNone on all three protocol families, while
// moving a listen port is DisruptRestart on all three.
type Disruption uint8

const (
	// DisruptNone applies without touching running sessions: Xray AddUser
	// over gRPC, appending to chap-secrets, issuing an OpenVPN certificate.
	DisruptNone Disruption = iota
	// DisruptReload re-reads configuration; sessions survive.
	DisruptReload
	// DisruptRestart restarts the service; active sessions drop.
	DisruptRestart
)

func (d Disruption) String() string {
	switch d {
	case DisruptNone:
		return "none"
	case DisruptReload:
		return "reload"
	case DisruptRestart:
		return "restart"
	default:
		return "unknown"
	}
}

type Kind string

type CredentialKind string

const (
	CredUUID     CredentialKind = "uuid"
	CredX509     CredentialKind = "x509"
	CredPassword CredentialKind = "password"
)

// Caps lets the panel and later sub-projects adapt without hardcoding
// protocol knowledge.
type Caps struct {
	HotUserAdd      bool             `json:"hot_user_add"`
	SelfAccounting  bool             `json:"self_accounting"`
	RequiresPKI     bool             `json:"requires_pki"`
	CredentialKinds []CredentialKind `json:"credential_kinds"`
	// ServiceSchema is a JSON Schema describing this adapter's service
	// params. The panel validates writes against it and the UI renders the
	// form from it, so adding a protocol means adding an adapter rather than
	// editing panel code.
	ServiceSchema json.RawMessage `json:"service_schema"`
}

type Descriptor struct {
	Kind    Kind   `json:"kind"`
	Version string `json:"version"`
	Caps    Caps   `json:"caps"`
}

type Credential struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// Subject is wired but empty in SP1; SP2 populates it.
type Subject struct {
	ID          int64        `json:"id"`
	Credentials []Credential `json:"credentials"`
}

type Service struct {
	ID      int64           `json:"id"`
	Kind    string          `json:"kind"`
	Enabled bool            `json:"enabled"`
	Params  json.RawMessage `json:"params"`
}

// Desired mirrors the panel's document type field-for-field. It is declared
// here rather than imported so this package stays free of panel code; the
// wire contract keeps the two in sync.
type Desired struct {
	SchemaVersion int       `json:"schema_version"`
	Revision      int64     `json:"revision"`
	NodeID        int64     `json:"node_id"`
	Services      []Service `json:"services"`
	Subjects      []Subject `json:"subjects"`
}

// ObservedService is the adapter's reading of one service on the host.
//
// Managed distinguishes a file antimage wrote from one a human created, and
// Checksum carries the content hash recorded in the file's marker. Together
// they let Plan tell "desired state changed" apart from "somebody edited this
// by hand", which is what makes drift reportable instead of silently
// overwritten.
type ObservedService struct {
	ID       int64
	Present  bool
	Managed  bool
	Checksum string
}

type Observed struct {
	Services []ObservedService
}

type Step struct {
	Seq        int
	Kind       string
	Disruption Disruption
	ServiceID  int64
	Payload    json.RawMessage
}

type Plan struct {
	Steps []Step
}

func (p Plan) IsEmpty() bool { return len(p.Steps) == 0 }

// MaxDisruption reports the worst cost in the plan, which the reconciler uses
// to decide whether a maintenance window applies.
func (p Plan) MaxDisruption() Disruption {
	worst := DisruptNone
	for _, s := range p.Steps {
		if s.Disruption > worst {
			worst = s.Disruption
		}
	}
	return worst
}

type StepResult struct {
	Seq      int
	OK       bool
	Err      string
	Duration time.Duration
}

type Health struct {
	OK     bool
	Detail string
}

// Adapter is implemented once per protocol family.
type Adapter interface {
	// Descriptor returns static identity and capabilities.
	Descriptor() Descriptor

	// Observe reads host truth. It must never mutate anything.
	Observe(ctx context.Context) (Observed, error)

	// Plan diffs desired against observed. It must be pure and repeatable:
	// calling it twice with the same inputs yields the same steps and has no
	// side effects. The convergence property test depends on this.
	Plan(ctx context.Context, desired Desired, observed Observed) (Plan, error)

	// Apply executes exactly one step. Every step must be idempotent, because
	// a retry after a partial failure re-runs it.
	Apply(ctx context.Context, step Step) (StepResult, error)

	// Probe is a cheap liveness check run on the health cadence.
	Probe(ctx context.Context) (Health, error)
}
```

- [ ] **Step 4: Run and watch it pass**

Run: `go test ./internal/node/adapter/... -race -v`
Expected: PASS — all five tests.

- [ ] **Step 5: Verify the import boundary holds**

Run: `make check-imports`
Expected: `OK: import boundaries and SSH host-key policy clean.`

- [ ] **Step 6: Commit**

```bash
git add internal/node/adapter
git commit -m "feat(adapter): contract with step-level disruption and drift-aware observation"
```

---

### Task 15: The stub adapter

**Files:**
- Create: `internal/node/adapter/stub/stub.go`
- Test: `internal/node/adapter/stub/stub_test.go`

**Interfaces:**
- Consumes: `adapter` (Task 14).
- Produces:
  - `stub.New(root string) *Adapter` — manages files under `root`
  - `stub.Kind = adapter.Kind("stub")`, `stub.MarkerPrefix = "# antimage-managed"`

The stub is a real adapter that writes real files. It proves the contract end to end — atomic writes, ownership markers, checksums, drift — without needing Xray installed. SP5 and SP6 follow its shape.

- [ ] **Step 1: Write the failing tests**

`internal/node/adapter/stub/stub_test.go`:

```go
package stub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amyrm/antimage/internal/node/adapter"
)

func desiredWith(services ...adapter.Service) adapter.Desired {
	return adapter.Desired{SchemaVersion: 1, Revision: 1, NodeID: 1, Services: services}
}

func svc(id int64, port int, enabled bool) adapter.Service {
	return adapter.Service{
		ID: id, Kind: "stub", Enabled: enabled,
		Params: json.RawMessage(`{"port":` + itoa(port) + `}`),
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

// converge runs Observe -> Plan -> Apply until the plan is empty, and returns
// how many rounds it took.
func converge(t *testing.T, a *Adapter, d adapter.Desired) int {
	t.Helper()
	ctx := context.Background()
	for round := 1; round <= 10; round++ {
		obs, err := a.Observe(ctx)
		if err != nil {
			t.Fatalf("Observe: %v", err)
		}
		plan, err := a.Plan(ctx, d, obs)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if plan.IsEmpty() {
			return round
		}
		for _, step := range plan.Steps {
			res, err := a.Apply(ctx, step)
			if err != nil {
				t.Fatalf("Apply step %d: %v", step.Seq, err)
			}
			if !res.OK {
				t.Fatalf("step %d failed: %s", step.Seq, res.Err)
			}
		}
	}
	t.Fatal("did not converge within 10 rounds")
	return 0
}

func TestDescriptorAdvertisesSchema(t *testing.T) {
	a := New(t.TempDir())
	d := a.Descriptor()
	if d.Kind != Kind {
		t.Errorf("Kind = %q, want %q", d.Kind, Kind)
	}
	var schema map[string]any
	if err := json.Unmarshal(d.Caps.ServiceSchema, &schema); err != nil {
		t.Fatalf("ServiceSchema is not valid JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("schema type = %v, want object", schema["type"])
	}
}

func TestCreatesServiceFileAndConverges(t *testing.T) {
	root := t.TempDir()
	a := New(root)
	rounds := converge(t, a, desiredWith(svc(10, 443, true)))
	if rounds != 2 {
		t.Errorf("converged in %d rounds, want 2 (one to apply, one to confirm)", rounds)
	}
	body, err := os.ReadFile(filepath.Join(root, "service-10.conf"))
	if err != nil {
		t.Fatalf("service file missing: %v", err)
	}
	if !strings.HasPrefix(string(body), MarkerPrefix) {
		t.Errorf("file lacks the ownership marker:\n%s", body)
	}
	if !strings.Contains(string(body), `"port":443`) {
		t.Errorf("file missing params:\n%s", body)
	}
}

// The core property: re-planning immediately after applying yields nothing.
func TestSecondPlanAfterApplyIsEmpty(t *testing.T) {
	a := New(t.TempDir())
	d := desiredWith(svc(10, 443, true), svc(11, 8443, true))
	converge(t, a, d)

	ctx := context.Background()
	obs, _ := a.Observe(ctx)
	plan, err := a.Plan(ctx, d, obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !plan.IsEmpty() {
		t.Fatalf("plan not empty after convergence: %+v", plan.Steps)
	}
}

func TestParamChangeIsRestartDisruption(t *testing.T) {
	a := New(t.TempDir())
	converge(t, a, desiredWith(svc(10, 443, true)))

	ctx := context.Background()
	obs, _ := a.Observe(ctx)
	plan, _ := a.Plan(ctx, desiredWith(svc(10, 9443, true)), obs)
	if plan.IsEmpty() {
		t.Fatal("changing the port produced no steps")
	}
	if got := plan.MaxDisruption(); got != adapter.DisruptRestart {
		t.Errorf("port change disruption = %v, want restart", got)
	}
}

func TestRemovedServiceIsDeleted(t *testing.T) {
	root := t.TempDir()
	a := New(root)
	converge(t, a, desiredWith(svc(10, 443, true)))
	converge(t, a, desiredWith())

	if _, err := os.Stat(filepath.Join(root, "service-10.conf")); !os.IsNotExist(err) {
		t.Error("removed service file still present")
	}
}

// Drift: a human edit must be detected, not silently overwritten, and the
// observation must report the file as no longer matching its marker.
func TestHandEditIsDetectedAsDrift(t *testing.T) {
	root := t.TempDir()
	a := New(root)
	d := desiredWith(svc(10, 443, true))
	converge(t, a, d)

	path := filepath.Join(root, "service-10.conf")
	body, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append(body, []byte("\n# hand edited\n")...), 0o600); err != nil {
		t.Fatalf("simulate hand edit: %v", err)
	}

	ctx := context.Background()
	obs, err := a.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(obs.Services) != 1 {
		t.Fatalf("observed %d services, want 1", len(obs.Services))
	}
	if !obs.Services[0].Managed {
		t.Error("hand-edited file lost its Managed flag; the marker should survive an append")
	}
	plan, _ := a.Plan(ctx, d, obs)
	if plan.IsEmpty() {
		t.Fatal("drifted file produced no plan — drift went undetected")
	}
}

// A file antimage did not write must never be touched.
func TestUnmanagedFileIsNeverTouched(t *testing.T) {
	root := t.TempDir()
	foreign := filepath.Join(root, "service-99.conf")
	if err := os.WriteFile(foreign, []byte("hand written, no marker\n"), 0o600); err != nil {
		t.Fatalf("write foreign file: %v", err)
	}

	a := New(root)
	converge(t, a, desiredWith(svc(10, 443, true)))

	body, err := os.ReadFile(foreign)
	if err != nil {
		t.Fatalf("foreign file was removed: %v", err)
	}
	if string(body) != "hand written, no marker\n" {
		t.Errorf("foreign file was modified: %q", body)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	a := New(t.TempDir())
	ctx := context.Background()
	d := desiredWith(svc(10, 443, true))

	obs, _ := a.Observe(ctx)
	plan, _ := a.Plan(ctx, d, obs)
	for i := 0; i < 3; i++ {
		for _, step := range plan.Steps {
			res, err := a.Apply(ctx, step)
			if err != nil || !res.OK {
				t.Fatalf("re-apply %d of step %d failed: %v %s", i, step.Seq, err, res.Err)
			}
		}
	}
	obs, _ = a.Observe(ctx)
	final, _ := a.Plan(ctx, d, obs)
	if !final.IsEmpty() {
		t.Error("repeated application left work outstanding; steps are not idempotent")
	}
}

func TestProbeReportsHealthy(t *testing.T) {
	a := New(t.TempDir())
	h, err := a.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !h.OK {
		t.Errorf("Probe reported unhealthy: %s", h.Detail)
	}
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/node/adapter/stub/...`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Implement**

`internal/node/adapter/stub/stub.go`:

```go
// Package stub is a working adapter that manages plain files.
//
// It exists so SP1 can exercise the whole contract — atomic writes, ownership
// markers, checksums, drift detection, idempotent steps — without requiring
// Xray, OpenVPN, or strongSwan on the host. SP5 and SP6 follow its shape.
package stub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/amyrm/antimage/internal/node/adapter"
)

const (
	Kind         = adapter.Kind("stub")
	MarkerPrefix = "# antimage-managed"
	filePrefix   = "service-"
	fileSuffix   = ".conf"
)

type Adapter struct {
	root string
}

func New(root string) *Adapter { return &Adapter{root: root} }

func (a *Adapter) Descriptor() adapter.Descriptor {
	return adapter.Descriptor{
		Kind:    Kind,
		Version: "1",
		Caps: adapter.Caps{
			HotUserAdd:      true,
			SelfAccounting:  false,
			RequiresPKI:     false,
			CredentialKinds: []adapter.CredentialKind{adapter.CredUUID},
			ServiceSchema: json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["port"],
  "properties": {
    "port": {"type": "integer", "minimum": 1, "maximum": 65535}
  }
}`),
		},
	}
}

func (a *Adapter) path(id int64) string {
	return filepath.Join(a.root, filePrefix+strconv.FormatInt(id, 10)+fileSuffix)
}

// render builds the managed file body: a marker line carrying the content
// checksum, then the payload. The checksum covers the payload only, so
// reading it back tells us what we intended to write.
func render(svc adapter.Service) string {
	payload := fmt.Sprintf("id=%d\nenabled=%t\nparams=%s\n",
		svc.ID, svc.Enabled, string(svc.Params))
	sum := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("%s sha256=%s\n%s", MarkerPrefix, hex.EncodeToString(sum[:]), payload)
}

// parseObserved reads a file and reports whether antimage wrote it and what
// checksum it claims.
func parseObserved(body string) (managed bool, checksum string) {
	if !strings.HasPrefix(body, MarkerPrefix) {
		return false, ""
	}
	line, _, _ := strings.Cut(body, "\n")
	_, sum, found := strings.Cut(line, "sha256=")
	if !found {
		return true, ""
	}
	return true, strings.TrimSpace(sum)
}

func (a *Adapter) Observe(ctx context.Context) (adapter.Observed, error) {
	entries, err := os.ReadDir(a.root)
	if os.IsNotExist(err) {
		return adapter.Observed{}, nil
	}
	if err != nil {
		return adapter.Observed{}, fmt.Errorf("read %s: %w", a.root, err)
	}

	var out []adapter.ObservedService
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, filePrefix) || !strings.HasSuffix(name, fileSuffix) {
			continue
		}
		idPart := strings.TrimSuffix(strings.TrimPrefix(name, filePrefix), fileSuffix)
		id, err := strconv.ParseInt(idPart, 10, 64)
		if err != nil {
			continue // not ours; leave it alone
		}
		body, err := os.ReadFile(filepath.Join(a.root, name))
		if err != nil {
			return adapter.Observed{}, fmt.Errorf("read %s: %w", name, err)
		}
		managed, checksum := parseObserved(string(body))

		// Recompute from what is actually on disk. A hand edit changes this
		// and so no longer matches the checksum recorded in the marker.
		_, payload, _ := strings.Cut(string(body), "\n")
		actual := sha256.Sum256([]byte(payload))
		if hex.EncodeToString(actual[:]) != checksum {
			checksum = "drifted:" + hex.EncodeToString(actual[:])
		}

		out = append(out, adapter.ObservedService{
			ID: id, Present: true, Managed: managed, Checksum: checksum,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return adapter.Observed{Services: out}, nil
}

func (a *Adapter) Plan(ctx context.Context, desired adapter.Desired, observed adapter.Observed) (adapter.Plan, error) {
	seen := map[int64]adapter.ObservedService{}
	for _, o := range observed.Services {
		seen[o.ID] = o
	}

	var steps []adapter.Step
	next := func() int { return len(steps) + 1 }

	// Desired services sorted by id, so plans are deterministic.
	services := append([]adapter.Service(nil), desired.Services...)
	sort.Slice(services, func(i, j int) bool { return services[i].ID < services[j].ID })

	for _, svc := range services {
		want := render(svc)
		wantSum := sha256.Sum256([]byte(want))

		obs, present := seen[svc.ID]
		if present && obs.Managed {
			// Compare against what the file should contain.
			currentSum, err := a.fileChecksum(svc.ID)
			if err != nil {
				return adapter.Plan{}, err
			}
			if currentSum == hex.EncodeToString(wantSum[:]) {
				continue // converged
			}
		}
		if present && !obs.Managed {
			// Never overwrite a file we did not create.
			continue
		}

		payload, err := json.Marshal(map[string]any{"body": want})
		if err != nil {
			return adapter.Plan{}, fmt.Errorf("encode step payload: %w", err)
		}
		steps = append(steps, adapter.Step{
			Seq:        next(),
			Kind:       "write_service",
			Disruption: adapter.DisruptRestart, // params changes move a listen port
			ServiceID:  svc.ID,
			Payload:    payload,
		})
	}

	// Anything managed that is no longer desired gets removed.
	desiredIDs := map[int64]bool{}
	for _, s := range services {
		desiredIDs[s.ID] = true
	}
	for _, o := range observed.Services {
		if desiredIDs[o.ID] || !o.Managed {
			continue
		}
		steps = append(steps, adapter.Step{
			Seq:        next(),
			Kind:       "remove_service",
			Disruption: adapter.DisruptRestart,
			ServiceID:  o.ID,
		})
	}

	return adapter.Plan{Steps: steps}, nil
}

func (a *Adapter) fileChecksum(id int64) (string, error) {
	body, err := os.ReadFile(a.path(id))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read service %d: %w", id, err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func (a *Adapter) Apply(ctx context.Context, step adapter.Step) (adapter.StepResult, error) {
	fail := func(err error) (adapter.StepResult, error) {
		return adapter.StepResult{Seq: step.Seq, OK: false, Err: err.Error()}, err
	}

	switch step.Kind {
	case "write_service":
		var p struct {
			Body string `json:"body"`
		}
		if err := json.Unmarshal(step.Payload, &p); err != nil {
			return fail(fmt.Errorf("decode payload: %w", err))
		}
		if err := os.MkdirAll(a.root, 0o700); err != nil {
			return fail(fmt.Errorf("create root: %w", err))
		}
		if err := atomicWrite(a.path(step.ServiceID), []byte(p.Body)); err != nil {
			return fail(err)
		}

	case "remove_service":
		if err := os.Remove(a.path(step.ServiceID)); err != nil && !os.IsNotExist(err) {
			return fail(fmt.Errorf("remove service %d: %w", step.ServiceID, err))
		}

	default:
		return fail(fmt.Errorf("unknown step kind %q", step.Kind))
	}

	return adapter.StepResult{Seq: step.Seq, OK: true}, nil
}

// atomicWrite writes to a temporary file in the same directory and renames,
// so a crash mid-write can never leave a truncated config behind.
func atomicWrite(path string, body []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".antimage-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
}

func (a *Adapter) Probe(ctx context.Context) (adapter.Health, error) {
	if err := os.MkdirAll(a.root, 0o700); err != nil {
		return adapter.Health{OK: false, Detail: err.Error()}, nil
	}
	return adapter.Health{OK: true, Detail: "stub adapter ready"}, nil
}
```

- [ ] **Step 4: Run and watch them pass**

Run: `go test ./internal/node/adapter/... -race -count=1 -v`
Expected: PASS — nine stub tests plus the five contract tests.

- [ ] **Step 5: Commit**

```bash
git add internal/node/adapter/stub
git commit -m "feat(adapter): stub adapter with atomic writes, markers, and drift detection"
```

---

### Task 16: The reconciler and the convergence property test

**Files:**
- Create: `internal/node/agent/clock.go`
- Create: `internal/node/agent/reconcile.go`
- Test: `internal/node/agent/reconcile_test.go`

**Interfaces:**
- Consumes: `adapter` (Task 14), `stub` (Task 15).
- Produces:
  - `type Clock interface { Now() time.Time; After(d time.Duration) <-chan time.Time }`; `agent.SystemClock{}`; `agent.NewFakeClock(t time.Time) *FakeClock` with `Advance(d)`
  - `type Reconciler struct { ... }`; `agent.NewReconciler(a adapter.Adapter, clk Clock, opts ReconcileOptions) *Reconciler`
  - `type ReconcileOptions struct { MaxRetries int; RetryBase time.Duration; AllowDisruptive func(time.Time) bool }`
  - `(*Reconciler).Converge(ctx, desired adapter.Desired) (Run, error)`
  - `type Run struct { TargetRevision int64; Steps []adapter.StepResult; Converged bool; Deferred bool; Err string }`

`Converged` is true only when a post-apply Observe → Plan comes back empty. That is what gates `applied_revision` in Task 21 (invariant 7).

- [ ] **Step 1: Write the clock**

`internal/node/agent/clock.go`:

```go
package agent

import (
	"sync"
	"time"
)

// Clock exists so reconciliation timing is testable without sleeping.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type SystemClock struct{}

func (SystemClock) Now() time.Time                         { return time.Now() }
func (SystemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// FakeClock drives tests deterministically. Timers created by After fire when
// Advance passes their deadline.
type FakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []fakeTimer
}

type fakeTimer struct {
	deadline time.Time
	ch       chan time.Time
}

func NewFakeClock(t time.Time) *FakeClock { return &FakeClock{now: t} }

func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *FakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.timers = append(c.timers, fakeTimer{deadline: c.now.Add(d), ch: ch})
	return ch
}

func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	var remaining []fakeTimer
	for _, t := range c.timers {
		if !t.deadline.After(now) {
			t.ch <- now
			continue
		}
		remaining = append(remaining, t)
	}
	c.timers = remaining
	c.mu.Unlock()
}
```

- [ ] **Step 2: Write the failing tests**

`internal/node/agent/reconcile_test.go`:

```go
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/node/adapter"
	"github.com/amyrm/antimage/internal/node/adapter/stub"
)

func desired(revision int64, ports ...int) adapter.Desired {
	d := adapter.Desired{SchemaVersion: 1, Revision: revision, NodeID: 1}
	for i, p := range ports {
		d.Services = append(d.Services, adapter.Service{
			ID: int64(10 + i), Kind: "stub", Enabled: true,
			Params: json.RawMessage(`{"port":` + itoa(p) + `}`),
		})
	}
	return d
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

func newReconciler(t *testing.T) (*Reconciler, *FakeClock) {
	t.Helper()
	clk := NewFakeClock(time.Unix(1_700_000_000, 0).UTC())
	r := NewReconciler(stub.New(t.TempDir()), clk, ReconcileOptions{
		MaxRetries: 3, RetryBase: time.Second,
	})
	return r, clk
}

func TestConvergeAppliesAndConfirms(t *testing.T) {
	r, _ := newReconciler(t)
	run, err := r.Converge(context.Background(), desired(1, 443))
	if err != nil {
		t.Fatalf("Converge: %v", err)
	}
	if !run.Converged {
		t.Fatalf("Converged = false; steps=%+v err=%s", run.Steps, run.Err)
	}
	if run.TargetRevision != 1 {
		t.Errorf("TargetRevision = %d, want 1", run.TargetRevision)
	}
	if len(run.Steps) == 0 {
		t.Error("no steps recorded for a change")
	}
}

func TestConvergeOnAlreadyConvergedStateIsANoOp(t *testing.T) {
	r, _ := newReconciler(t)
	ctx := context.Background()
	d := desired(1, 443)
	if _, err := r.Converge(ctx, d); err != nil {
		t.Fatalf("first Converge: %v", err)
	}
	run, err := r.Converge(ctx, d)
	if err != nil {
		t.Fatalf("second Converge: %v", err)
	}
	if !run.Converged {
		t.Error("Converged = false on an already-converged node")
	}
	if len(run.Steps) != 0 {
		t.Errorf("second run applied %d steps, want 0", len(run.Steps))
	}
}

// THE property test. For arbitrary desired states, applying a plan and
// re-planning must yield an empty plan. If this breaks, nodes reconcile
// forever and the whole architecture fails quietly.
func TestConvergenceIsIdempotentForArbitraryDesiredStates(t *testing.T) {
	rng := rand.New(rand.NewSource(20260813))
	for trial := 0; trial < 200; trial++ {
		r, _ := newReconciler(t)
		ctx := context.Background()

		n := rng.Intn(5)
		ports := make([]int, n)
		for i := range ports {
			ports[i] = 1024 + rng.Intn(60000)
		}
		d := desired(int64(trial+1), ports...)

		run, err := r.Converge(ctx, d)
		if err != nil {
			t.Fatalf("trial %d Converge: %v", trial, err)
		}
		if !run.Converged {
			t.Fatalf("trial %d did not converge: %+v %s", trial, run.Steps, run.Err)
		}

		// Re-planning must find nothing to do.
		again, err := r.Converge(ctx, d)
		if err != nil {
			t.Fatalf("trial %d re-Converge: %v", trial, err)
		}
		if len(again.Steps) != 0 {
			t.Fatalf("trial %d: re-plan produced %d steps, want 0 — reconciliation does not settle",
				trial, len(again.Steps))
		}
	}
}

// Transitions between arbitrary states must also settle.
func TestConvergenceSettlesAcrossTransitions(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	r, _ := newReconciler(t)
	ctx := context.Background()

	for step := 0; step < 100; step++ {
		n := rng.Intn(4)
		ports := make([]int, n)
		for i := range ports {
			ports[i] = 1024 + rng.Intn(60000)
		}
		d := desired(int64(step+1), ports...)

		if _, err := r.Converge(ctx, d); err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
		again, err := r.Converge(ctx, d)
		if err != nil {
			t.Fatalf("step %d recheck: %v", step, err)
		}
		if len(again.Steps) != 0 {
			t.Fatalf("step %d left %d steps outstanding", step, len(again.Steps))
		}
	}
}

func TestFailingStepRetriesThenReportsDegraded(t *testing.T) {
	clk := NewFakeClock(time.Unix(1_700_000_000, 0).UTC())
	fa := &flakyAdapter{failEvery: true}
	r := NewReconciler(fa, clk, ReconcileOptions{MaxRetries: 3, RetryBase: time.Millisecond})

	run, err := r.Converge(context.Background(), desired(1, 443))
	if err == nil {
		t.Fatal("Converge returned nil error for a permanently failing step")
	}
	if run.Converged {
		t.Error("Converged = true despite a failing step")
	}
	if fa.applyCalls != 3 {
		t.Errorf("Apply called %d times, want MaxRetries=3", fa.applyCalls)
	}
	if run.Err == "" {
		t.Error("Run.Err is empty; the underlying failure must surface in the UI")
	}
}

func TestDisruptiveStepsDeferOutsideMaintenanceWindow(t *testing.T) {
	clk := NewFakeClock(time.Unix(1_700_000_000, 0).UTC())
	r := NewReconciler(stub.New(t.TempDir()), clk, ReconcileOptions{
		MaxRetries: 3,
		RetryBase:  time.Millisecond,
		// Window closed: no disruptive step may run.
		AllowDisruptive: func(time.Time) bool { return false },
	})

	run, err := r.Converge(context.Background(), desired(1, 443))
	if err != nil {
		t.Fatalf("Converge: %v", err)
	}
	if !run.Deferred {
		t.Fatal("Deferred = false; a restart-class step outside the window must be deferred")
	}
	if run.Converged {
		t.Error("Converged = true although work was deferred")
	}
	if len(run.Steps) != 0 {
		t.Errorf("applied %d steps with the window closed, want 0", len(run.Steps))
	}
}

func TestNonDisruptiveStepsRunEvenWithWindowClosed(t *testing.T) {
	clk := NewFakeClock(time.Unix(1_700_000_000, 0).UTC())
	hot := &hotOnlyAdapter{}
	r := NewReconciler(hot, clk, ReconcileOptions{
		MaxRetries: 3, RetryBase: time.Millisecond,
		AllowDisruptive: func(time.Time) bool { return false },
	})
	run, err := r.Converge(context.Background(), desired(1, 443))
	if err != nil {
		t.Fatalf("Converge: %v", err)
	}
	if run.Deferred {
		t.Error("Deferred = true for a plan containing only DisruptNone steps")
	}
	if len(run.Steps) == 0 {
		t.Error("hot steps were not applied; user disables must never wait for a window")
	}
}

// --- test doubles ---

type flakyAdapter struct {
	failEvery  bool
	applyCalls int
}

func (f *flakyAdapter) Descriptor() adapter.Descriptor {
	return adapter.Descriptor{Kind: "flaky", Version: "1"}
}
func (f *flakyAdapter) Observe(context.Context) (adapter.Observed, error) {
	return adapter.Observed{}, nil
}
func (f *flakyAdapter) Plan(_ context.Context, d adapter.Desired, _ adapter.Observed) (adapter.Plan, error) {
	return adapter.Plan{Steps: []adapter.Step{{Seq: 1, Kind: "boom", Disruption: adapter.DisruptNone}}}, nil
}
func (f *flakyAdapter) Apply(context.Context, adapter.Step) (adapter.StepResult, error) {
	f.applyCalls++
	err := errors.New("simulated apply failure")
	return adapter.StepResult{Seq: 1, OK: false, Err: err.Error()}, err
}
func (f *flakyAdapter) Probe(context.Context) (adapter.Health, error) {
	return adapter.Health{OK: true}, nil
}

type hotOnlyAdapter struct{ applied bool }

func (h *hotOnlyAdapter) Descriptor() adapter.Descriptor {
	return adapter.Descriptor{Kind: "hot", Version: "1", Caps: adapter.Caps{HotUserAdd: true}}
}
func (h *hotOnlyAdapter) Observe(context.Context) (adapter.Observed, error) {
	return adapter.Observed{}, nil
}
func (h *hotOnlyAdapter) Plan(context.Context, adapter.Desired, adapter.Observed) (adapter.Plan, error) {
	if h.applied {
		return adapter.Plan{}, nil
	}
	return adapter.Plan{Steps: []adapter.Step{{Seq: 1, Kind: "hot_add", Disruption: adapter.DisruptNone}}}, nil
}
func (h *hotOnlyAdapter) Apply(context.Context, adapter.Step) (adapter.StepResult, error) {
	h.applied = true
	return adapter.StepResult{Seq: 1, OK: true}, nil
}
func (h *hotOnlyAdapter) Probe(context.Context) (adapter.Health, error) {
	return adapter.Health{OK: true}, nil
}
```

- [ ] **Step 3: Run and watch them fail**

Run: `go test ./internal/node/agent/...`
Expected: FAIL — `undefined: NewReconciler`.

- [ ] **Step 4: Implement**

`internal/node/agent/reconcile.go`:

```go
package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Run records one convergence attempt. It is reported to the panel, which
// uses Converged to decide whether applied_revision may advance (invariant 7).
type Run struct {
	TargetRevision int64
	Steps          []adapter.StepResult
	Converged      bool
	Deferred       bool
	Err            string
}

type ReconcileOptions struct {
	// MaxRetries bounds retries of a single failing step before the run is
	// abandoned and the node reported Degraded.
	MaxRetries int
	// RetryBase is the first backoff interval; it doubles per attempt.
	RetryBase time.Duration
	// AllowDisruptive reports whether restart-class steps may run now. Nil
	// means always. This implements the maintenance window from spec 4.1.
	AllowDisruptive func(time.Time) bool
}

type Reconciler struct {
	ad   adapter.Adapter
	clk  Clock
	opts ReconcileOptions
}

func NewReconciler(a adapter.Adapter, clk Clock, opts ReconcileOptions) *Reconciler {
	if opts.MaxRetries < 1 {
		opts.MaxRetries = 3
	}
	if opts.RetryBase <= 0 {
		opts.RetryBase = time.Second
	}
	return &Reconciler{ad: a, clk: clk, opts: opts}
}

func (r *Reconciler) disruptiveAllowed() bool {
	if r.opts.AllowDisruptive == nil {
		return true
	}
	return r.opts.AllowDisruptive(r.clk.Now())
}

// Converge runs Observe -> Plan -> Apply, then re-observes and re-plans to
// confirm. Converged is true only when that confirmation plan is empty, which
// is what makes partial application visible rather than silently accepted.
func (r *Reconciler) Converge(ctx context.Context, desired adapter.Desired) (Run, error) {
	run := Run{TargetRevision: desired.Revision}

	observed, err := r.ad.Observe(ctx)
	if err != nil {
		run.Err = err.Error()
		return run, fmt.Errorf("observe: %w", err)
	}

	plan, err := r.ad.Plan(ctx, desired, observed)
	if err != nil {
		run.Err = err.Error()
		return run, fmt.Errorf("plan: %w", err)
	}

	if plan.IsEmpty() {
		run.Converged = true
		return run, nil
	}

	// Defer the whole plan when it needs a restart and the window is closed.
	// Hot steps still run, so disabling a user never waits for 04:00.
	if plan.MaxDisruption() >= adapter.DisruptRestart && !r.disruptiveAllowed() {
		run.Deferred = true
		return run, nil
	}

	for _, step := range plan.Steps {
		if step.Disruption >= adapter.DisruptRestart && !r.disruptiveAllowed() {
			run.Deferred = true
			continue
		}
		result, err := r.applyWithRetry(ctx, step)
		run.Steps = append(run.Steps, result)
		if err != nil {
			run.Err = result.Err
			// One failure must not block unrelated steps, so continue rather
			// than abort; the run simply will not converge.
			continue
		}
	}

	if run.Err != "" {
		return run, errors.New(run.Err)
	}
	if run.Deferred {
		return run, nil
	}

	// Confirmation pass.
	observed, err = r.ad.Observe(ctx)
	if err != nil {
		run.Err = err.Error()
		return run, fmt.Errorf("post-apply observe: %w", err)
	}
	confirm, err := r.ad.Plan(ctx, desired, observed)
	if err != nil {
		run.Err = err.Error()
		return run, fmt.Errorf("post-apply plan: %w", err)
	}
	run.Converged = confirm.IsEmpty()
	if !run.Converged {
		run.Err = fmt.Sprintf("%d steps still outstanding after apply", len(confirm.Steps))
	}
	return run, nil
}

func (r *Reconciler) applyWithRetry(ctx context.Context, step adapter.Step) (adapter.StepResult, error) {
	var (
		last    adapter.StepResult
		lastErr error
		backoff = r.opts.RetryBase
	)
	for attempt := 1; attempt <= r.opts.MaxRetries; attempt++ {
		started := r.clk.Now()
		result, err := r.ad.Apply(ctx, step)
		result.Seq = step.Seq
		result.Duration = r.clk.Now().Sub(started)

		if err == nil && result.OK {
			return result, nil
		}
		last, lastErr = result, err
		if lastErr == nil {
			lastErr = errors.New(result.Err)
		}

		if attempt == r.opts.MaxRetries {
			break
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-r.clk.After(backoff):
		}
		backoff *= 2
	}
	return last, fmt.Errorf("step %d (%s) failed after %d attempts: %w",
		step.Seq, step.Kind, r.opts.MaxRetries, lastErr)
}
```

- [ ] **Step 5: Run and watch them pass**

Run: `go test ./internal/node/agent/... -race -count=1 -v`
Expected: PASS — seven tests, including 200 property trials and 100 transition steps.

- [ ] **Step 6: Commit**

```bash
git add internal/node/agent
git commit -m "feat(agent): reconciler with retry backoff, maintenance windows, and convergence property tests"
```

---

# Phase E — Transport

### Task 17: Wire contract — protobuf and codegen

**Files:**
- Create: `proto/antimage/v1/control.proto`
- Create: `buf.gen.yaml`, `Makefile` target `proto`
- Test: `internal/shared/proto/proto_test.go`

**Interfaces:**
- Produces (generated into `internal/shared/proto`):
  - `service Control { rpc Stream(stream AgentMessage) returns (stream PanelMessage); rpc GetDesiredSnapshot(SnapshotRequest) returns (SnapshotResponse); }`
  - `service Enrollment { rpc Enroll(EnrollRequest) returns (EnrollResponse); }`
  - `Hello`, `Heartbeat`, `ApplyReport`, `RevisionBump`, `FetchNow`

- [ ] **Step 1: Write the proto**

`proto/antimage/v1/control.proto`:

```protobuf
syntax = "proto3";

package antimage.v1;

option go_package = "github.com/amyrm/antimage/internal/shared/proto;antimagev1";

// Enrollment runs once per node, over TLS validated against the CA
// fingerprint pinned in node.yaml. Every later call uses mTLS.
service Enrollment {
  rpc Enroll(EnrollRequest) returns (EnrollResponse);
}

message EnrollRequest {
  string token = 1;          // single-use, 30 minute TTL, bound to one node
  bytes  csr_der = 2;        // the node's CSR; its private key never leaves the host
  string agent_version = 3;
  uint32 protocol_version = 4;
}

message EnrollResponse {
  bytes cert_der = 1;        // signed client certificate, CN = node id
  bytes ca_der = 2;          // panel CA, for verifying the panel in future
  int64 node_id = 3;
}

service Control {
  // Stream is dialed by the agent and held open. The panel never dials the
  // node, so nodes need no inbound port.
  rpc Stream(stream AgentMessage) returns (stream PanelMessage);

  // GetDesiredSnapshot returns the exact canonical bytes that were hashed.
  rpc GetDesiredSnapshot(SnapshotRequest) returns (SnapshotResponse);
}

message AgentMessage {
  oneof payload {
    Hello       hello = 1;
    Heartbeat   heartbeat = 2;
    ApplyReport apply_report = 3;
  }
}

message PanelMessage {
  oneof payload {
    RevisionBump revision_bump = 1;
    FetchNow     fetch_now = 2;
    UpgradeRequired upgrade_required = 3;
  }
}

message Hello {
  int64  node_id = 1;
  string agent_version = 2;
  uint32 protocol_version = 3;
  int64  applied_revision = 4;
  string doc_sha256 = 5;
  repeated AdapterDescriptor adapters = 6;
}

message AdapterDescriptor {
  string kind = 1;
  string version = 2;
  bool   hot_user_add = 3;
  bool   self_accounting = 4;
  bool   requires_pki = 5;
  bytes  service_schema = 6;   // JSON Schema
}

message Heartbeat {
  double load1 = 1;
  uint64 mem_used_bytes = 2;
  uint64 uptime_seconds = 3;
  repeated AdapterHealth adapter_health = 4;
}

message AdapterHealth {
  string kind = 1;
  bool   ok = 2;
  string detail = 3;
}

message ApplyReport {
  int64 target_revision = 1;
  bool  converged = 2;
  bool  deferred = 3;
  string error = 4;
  string doc_sha256 = 5;       // hash the agent actually applied
  repeated StepResult steps = 6;
}

message StepResult {
  int32  seq = 1;
  string kind = 2;
  string disruption = 3;       // "none" | "reload" | "restart"
  bool   ok = 4;
  string error = 5;
  int64  duration_ms = 6;
}

message RevisionBump { int64 revision = 1; }
message FetchNow {}
message UpgradeRequired { uint32 panel_protocol_version = 1; string download_url = 2; }

message SnapshotRequest { int64 node_id = 1; }

message SnapshotResponse {
  int64  revision = 1;
  bytes  document = 2;   // exact canonical bytes; the agent re-hashes these
  string sha256 = 3;
}
```

- [ ] **Step 2: Add codegen config and Makefile target**

`buf.gen.yaml`:

```yaml
version: v2
plugins:
  - remote: buf.build/protocolbuffers/go
    out: .
    opt: paths=source_relative
  - remote: buf.build/grpc/go
    out: .
    opt: paths=source_relative,require_unimplemented_servers=false
```

Add to the `Makefile`:

```makefile
.PHONY: proto
proto:
	buf generate proto
```

- [ ] **Step 3: Generate**

```bash
go get google.golang.org/grpc@latest google.golang.org/protobuf@latest
make proto
```

Expected: `internal/shared/proto/antimage/v1/control.pb.go` and `control_grpc.pb.go` exist.

- [ ] **Step 4: Write the contract test**

`internal/shared/proto/proto_test.go`:

```go
package proto_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/amyrm/antimage/internal/shared/proto/antimage/v1"
)

func TestHelloRoundTrips(t *testing.T) {
	in := &pb.Hello{
		NodeId: 7, AgentVersion: "v0.1.0", ProtocolVersion: 1,
		AppliedRevision: 3, DocSha256: "abc",
		Adapters: []*pb.AdapterDescriptor{{Kind: "stub", Version: "1", HotUserAdd: true}},
	}
	raw, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out pb.Hello
	if err := proto.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.NodeId != 7 || out.AppliedRevision != 3 || len(out.Adapters) != 1 {
		t.Fatalf("round trip lost data: %+v", &out)
	}
}

func TestSnapshotResponseCarriesExactBytes(t *testing.T) {
	// The agent re-hashes SnapshotResponse.Document, so the field must be
	// bytes rather than a structured message that could re-encode.
	in := &pb.SnapshotResponse{Revision: 4, Document: []byte(`{"a":1}`), Sha256: "d"}
	raw, _ := proto.Marshal(in)
	var out pb.SnapshotResponse
	if err := proto.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(out.Document) != `{"a":1}` {
		t.Errorf("document = %q, want exact bytes preserved", out.Document)
	}
}
```

- [ ] **Step 5: Run and watch it pass**

Run: `go test ./internal/shared/proto/... -v`
Expected: PASS — both tests.

- [ ] **Step 6: Commit**

```bash
git add proto buf.gen.yaml Makefile internal/shared/proto go.mod go.sum
git commit -m "feat(proto): panel-agent wire contract for enrollment and control"
```

---

### Task 18: Panel CA, enrollment tokens, and CSR signing

**Files:**
- Create: `internal/panel/store/migrations/00007_enrollment.sql`
- Create: `internal/panel/nodes/ca.go`
- Create: `internal/panel/nodes/enroll.go`
- Test: `internal/panel/nodes/ca_test.go`, `internal/panel/nodes/enroll_test.go`

**Interfaces:**
- Consumes: `secrets.Box` (Task 4), `store.Store`, `audit`.
- Produces:
  - `nodes.LoadOrCreateCA(ctx, s *store.Store, box *secrets.Box) (*CA, error)`
  - `(*CA).FingerprintSHA256() string`, `(*CA).CertDER() []byte`
  - `(*CA).SignNodeCert(csrDER []byte, nodeID int64, now time.Time) (certDER []byte, fingerprint string, err error)`
  - `nodes.IssueEnrollToken(ctx, s, nodeID int64, actor audit.Actor, requestID string, now time.Time) (token string, err error)`
  - `nodes.RedeemEnrollToken(ctx, s, token string, now time.Time) (nodeID int64, err error)`
  - `nodes.ErrTokenInvalid`
  - `nodes.NodeCertLifetime = 365 * 24 * time.Hour`, `nodes.EnrollTokenTTL = 30 * time.Minute`

- [ ] **Step 1: Write the migration**

`internal/panel/store/migrations/00007_enrollment.sql`:

```sql
-- +goose Up
CREATE TABLE enroll_tokens (
    token_hash BLOB PRIMARY KEY,
    node_id    INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    expires_at INTEGER NOT NULL,
    used_at    INTEGER,
    created_at INTEGER NOT NULL
) STRICT;

CREATE TABLE panel_ca (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    cert_der    BLOB NOT NULL,
    key_sealed  BLOB NOT NULL,   -- AES-256-GCM under the master key
    created_at  INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE panel_ca;
DROP TABLE enroll_tokens;
```

- [ ] **Step 2: Write the failing CA tests**

`internal/panel/nodes/ca_test.go`:

```go
package nodes

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/shared/secrets"
)

func newCA(t *testing.T) (*CA, *storeFixture) {
	t.Helper()
	f := newStoreFixture(t)
	box, err := secrets.NewBox(bytes.Repeat([]byte{3}, secrets.KeySize))
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}
	ca, err := LoadOrCreateCA(context.Background(), f.store, box)
	if err != nil {
		t.Fatalf("LoadOrCreateCA: %v", err)
	}
	return ca, f
}

func TestCAIsStableAcrossLoads(t *testing.T) {
	ca, f := newCA(t)
	box, _ := secrets.NewBox(bytes.Repeat([]byte{3}, secrets.KeySize))
	again, err := LoadOrCreateCA(context.Background(), f.store, box)
	if err != nil {
		t.Fatalf("second LoadOrCreateCA: %v", err)
	}
	if ca.FingerprintSHA256() != again.FingerprintSHA256() {
		t.Fatal("a second load regenerated the CA; every enrolled node would be locked out")
	}
}

func TestCAKeyIsNotStoredInPlaintext(t *testing.T) {
	ca, f := newCA(t)
	var sealed []byte
	if err := f.store.Read().QueryRow(`SELECT key_sealed FROM panel_ca WHERE id = 1`).Scan(&sealed); err != nil {
		t.Fatalf("read sealed key: %v", err)
	}
	if _, err := x509.ParseECPrivateKey(sealed); err == nil {
		t.Fatal("the CA private key is readable straight from the database")
	}
	if len(ca.CertDER()) == 0 {
		t.Error("CertDER is empty")
	}
}

func TestSignNodeCertProducesUsableClientCert(t *testing.T) {
	ca, _ := newCA(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: "ignored"}}, key)
	if err != nil {
		t.Fatalf("create CSR: %v", err)
	}

	now := time.Unix(1_700_000_000, 0).UTC()
	certDER, fingerprint, err := ca.SignNodeCert(csrDER, 42, now)
	if err != nil {
		t.Fatalf("SignNodeCert: %v", err)
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatalf("parse issued cert: %v", err)
	}
	if cert.Subject.CommonName != "42" {
		t.Errorf("CN = %q, want the node id 42 — the panel names the node, not the CSR",
			cert.Subject.CommonName)
	}
	if len(fingerprint) != 64 {
		t.Errorf("fingerprint = %q, want 64 hex chars", fingerprint)
	}
	if got := cert.NotAfter.Sub(cert.NotBefore); got != NodeCertLifetime {
		t.Errorf("lifetime = %v, want %v", got, NodeCertLifetime)
	}
	hasClientAuth := false
	for _, u := range cert.ExtKeyUsage {
		if u == x509.ExtKeyUsageClientAuth {
			hasClientAuth = true
		}
	}
	if !hasClientAuth {
		t.Error("issued cert lacks ClientAuth extended key usage")
	}

	// It must chain to the CA.
	pool := x509.NewCertPool()
	caCert, _ := x509.ParseCertificate(ca.CertDER())
	pool.AddCert(caCert)
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots: pool, CurrentTime: now.Add(time.Hour),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Errorf("issued cert does not chain to the CA: %v", err)
	}
}

func TestSignRejectsMalformedCSR(t *testing.T) {
	ca, _ := newCA(t)
	if _, _, err := ca.SignNodeCert([]byte("not a csr"), 1, time.Now()); err == nil {
		t.Fatal("SignNodeCert accepted garbage")
	}
}
```

Add the shared fixture `internal/panel/nodes/fixture_test.go`:

```go
package nodes

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

type storeFixture struct {
	store  *store.Store
	nodeID int64
}

func newStoreFixture(t *testing.T) *storeFixture {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var nodeID int64
	err = s.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`INSERT INTO nodes (name, address, created_at) VALUES ('n1','1.2.3.4',?)`,
			time.Now().Unix())
		if err != nil {
			return err
		}
		nodeID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("seed node: %v", err)
	}
	return &storeFixture{store: s, nodeID: nodeID}
}
```

- [ ] **Step 3: Write the failing enrollment tests**

`internal/panel/nodes/enroll_test.go`:

```go
package nodes

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
)

func TestTokenRedeemsExactlyOnce(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()

	token, err := IssueEnrollToken(ctx, f.store, f.nodeID, audit.SystemActor("test"), "req", now)
	if err != nil {
		t.Fatalf("IssueEnrollToken: %v", err)
	}

	gotID, err := RedeemEnrollToken(ctx, f.store, token, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	if gotID != f.nodeID {
		t.Errorf("node id = %d, want %d", gotID, f.nodeID)
	}

	if _, err := RedeemEnrollToken(ctx, f.store, token, now.Add(2*time.Minute)); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("second redeem err = %v, want ErrTokenInvalid — tokens are single use", err)
	}
}

func TestTokenExpiresAfterTTL(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()

	token, _ := IssueEnrollToken(ctx, f.store, f.nodeID, audit.SystemActor("test"), "req", now)
	_, err := RedeemEnrollToken(ctx, f.store, token, now.Add(EnrollTokenTTL+time.Second))
	if !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expired redeem err = %v, want ErrTokenInvalid", err)
	}
}

func TestUnknownTokenIsRejected(t *testing.T) {
	f := newStoreFixture(t)
	if _, err := RedeemEnrollToken(context.Background(), f.store, "bogus", time.Now()); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("err = %v, want ErrTokenInvalid", err)
	}
}

func TestTokenIsStoredHashed(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	token, _ := IssueEnrollToken(ctx, f.store, f.nodeID, audit.SystemActor("test"), "req",
		time.Unix(1_700_000_000, 0).UTC())

	var n int
	if err := f.store.Read().QueryRow(
		`SELECT count(*) FROM enroll_tokens WHERE token_hash = ?`, []byte(token)).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 0 {
		t.Fatal("the raw enrollment token was stored")
	}
}

func TestIssuingTokenIsAudited(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	if _, err := IssueEnrollToken(ctx, f.store, f.nodeID, audit.SystemActor("test"), "req-9",
		time.Unix(1_700_000_000, 0).UTC()); err != nil {
		t.Fatalf("IssueEnrollToken: %v", err)
	}
	var action, requestID string
	if err := f.store.Read().QueryRow(
		`SELECT action, request_id FROM audit_log ORDER BY id DESC LIMIT 1`).Scan(&action, &requestID); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if action != "node.enroll_token" || requestID != "req-9" {
		t.Errorf("audit = %s/%s, want node.enroll_token/req-9", action, requestID)
	}
}
```

- [ ] **Step 4: Run and watch them fail**

Run: `go test ./internal/panel/nodes/... -run 'CA|Token|Sign'`
Expected: FAIL — `undefined: LoadOrCreateCA`.

- [ ] **Step 5: Implement the CA**

`internal/panel/nodes/ca.go`:

```go
package nodes

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/shared/secrets"
)

// NodeCertLifetime is one year; agents auto-renew at the halfway mark.
const NodeCertLifetime = 365 * 24 * time.Hour

const caLifetime = 10 * 365 * 24 * time.Hour

// CA is the panel's private certificate authority. The panel is the only
// verifier of node certificates, so revocation is an allow-list check against
// nodes.cert_fingerprint rather than a CRL.
type CA struct {
	cert    *x509.Certificate
	certDER []byte
	key     *ecdsa.PrivateKey
}

func (c *CA) CertDER() []byte { return c.certDER }

// FingerprintSHA256 is pinned into node.yaml at bootstrap so an agent can
// verify the panel even if DNS is hijacked.
func (c *CA) FingerprintSHA256() string {
	sum := sha256.Sum256(c.certDER)
	return hex.EncodeToString(sum[:])
}

// LoadOrCreateCA reads the CA, generating one on first run. The private key
// is sealed under the master key before it touches the database.
func LoadOrCreateCA(ctx context.Context, s *store.Store, box *secrets.Box) (*CA, error) {
	var certDER, sealed []byte
	err := s.Read().QueryRowContext(ctx,
		`SELECT cert_der, key_sealed FROM panel_ca WHERE id = 1`).Scan(&certDER, &sealed)

	switch {
	case err == nil:
		keyDER, err := box.Open(sealed)
		if err != nil {
			return nil, fmt.Errorf("decrypt CA key (wrong master key?): %w", err)
		}
		key, err := x509.ParseECPrivateKey(keyDER)
		if err != nil {
			return nil, fmt.Errorf("parse CA key: %w", err)
		}
		cert, err := x509.ParseCertificate(certDER)
		if err != nil {
			return nil, fmt.Errorf("parse CA cert: %w", err)
		}
		return &CA{cert: cert, certDER: certDER, key: key}, nil

	case errors.Is(err, sql.ErrNoRows):
		return createCA(ctx, s, box)

	default:
		return nil, fmt.Errorf("read CA: %w", err)
	}
}

func createCA(ctx context.Context, s *store.Store, box *secrets.Box) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate CA key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "antimage panel CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(caLifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("self-sign CA: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal CA key: %w", err)
	}
	sealed, err := box.Seal(keyDER)
	if err != nil {
		return nil, fmt.Errorf("seal CA key: %w", err)
	}

	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO panel_ca (id, cert_der, key_sealed, created_at) VALUES (1, ?, ?, ?)`,
			certDER, sealed, now.Unix())
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("persist CA: %w", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("parse new CA cert: %w", err)
	}
	return &CA{cert: cert, certDER: certDER, key: key}, nil
}

// SignNodeCert issues a client certificate whose CN is the node id.
//
// The CSR's subject is deliberately ignored: the panel decides which node an
// enrolling agent is, based on the token it redeemed, so a node cannot name
// itself.
func (c *CA) SignNodeCert(csrDER []byte, nodeID int64, now time.Time) ([]byte, string, error) {
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return nil, "", fmt.Errorf("parse CSR: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, "", fmt.Errorf("CSR signature invalid: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, "", fmt.Errorf("generate serial: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: fmt.Sprintf("%d", nodeID)},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(-5 * time.Minute).Add(NodeCertLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, csr.PublicKey, c.key)
	if err != nil {
		return nil, "", fmt.Errorf("sign node cert: %w", err)
	}
	sum := sha256.Sum256(certDER)
	return certDER, hex.EncodeToString(sum[:]), nil
}
```

- [ ] **Step 6: Implement enrollment tokens**

`internal/panel/nodes/enroll.go`:

```go
package nodes

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/store"
)

// EnrollTokenTTL is deliberately short: the token travels in a curl one-liner
// and grants the right to become a node.
const EnrollTokenTTL = 30 * time.Minute

// ErrTokenInvalid covers unknown, expired, and already-used tokens. Callers
// must not distinguish them.
var ErrTokenInvalid = errors.New("enrollment token invalid")

func hashEnrollToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func IssueEnrollToken(
	ctx context.Context, s *store.Store, nodeID int64,
	actor audit.Actor, requestID string, now time.Time,
) (string, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", fmt.Errorf("generate enrollment token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	err := s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO enroll_tokens (token_hash, node_id, expires_at, created_at)
			 VALUES (?,?,?,?)`,
			hashEnrollToken(token), nodeID,
			now.Add(EnrollTokenTTL).Unix(), now.Unix()); err != nil {
			return fmt.Errorf("insert enrollment token: %w", err)
		}
		return audit.InTx(ctx, tx, requestID, actor, audit.Record{
			Action:     "node.enroll_token",
			TargetType: "node",
			TargetID:   sql.NullInt64{Int64: nodeID, Valid: true},
			After:      map[string]any{"expires_at": now.Add(EnrollTokenTTL).Unix()},
			Result:     "ok",
		})
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// RedeemEnrollToken burns the token and returns the node it was bound to.
// The update is conditional, so two concurrent redemptions cannot both win.
func RedeemEnrollToken(ctx context.Context, s *store.Store, token string, now time.Time) (int64, error) {
	if token == "" {
		return 0, ErrTokenInvalid
	}
	var nodeID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE enroll_tokens SET used_at = ?
			  WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?`,
			now.Unix(), hashEnrollToken(token), now.Unix())
		if err != nil {
			return fmt.Errorf("burn enrollment token: %w", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("rows affected: %w", err)
		}
		if affected == 0 {
			return ErrTokenInvalid
		}
		return tx.QueryRowContext(ctx,
			`SELECT node_id FROM enroll_tokens WHERE token_hash = ?`,
			hashEnrollToken(token)).Scan(&nodeID)
	})
	if err != nil {
		return 0, err
	}
	return nodeID, nil
}
```

- [ ] **Step 7: Run and watch them pass**

Run: `go test ./internal/panel/nodes/... -race -count=1 -v`
Expected: PASS — five CA tests, five enrollment tests, and the thirteen from Tasks 12–13.

- [ ] **Step 8: Commit**

```bash
git add internal/panel/nodes internal/panel/store/migrations
git commit -m "feat(nodes): panel CA, node certificate signing, and single-use enrollment tokens"
```

---

### Task 19: gRPC control server, mTLS, and the stream hub

**Files:**
- Create: `internal/panel/control/hub.go`
- Create: `internal/panel/control/server.go`
- Test: `internal/panel/control/hub_test.go`, `internal/panel/control/server_test.go`

**Interfaces:**
- Consumes: `nodes.CA`, `nodes.RedeemEnrollToken`, `store.Store`, generated proto (Task 17).
- Produces:
  - `control.NewHub() *Hub`; `(*Hub).Register(nodeID int64) (<-chan int64, func())`; `(*Hub).Notify(nodeID, revision int64) bool`; `(*Hub).Online(nodeID int64) bool`; `(*Hub).Count() int`
  - `control.NewServer(deps Deps) *Server`; `(*Server).GRPCServer() *grpc.Server`
  - `control.VerifyPeer(ctx, s *store.Store) (nodeID int64, err error)` — allow-list check against `nodes.cert_fingerprint`
  - `control.ErrNotEnrolled`

- [ ] **Step 1: Write the failing hub tests**

`internal/panel/control/hub_test.go`:

```go
package control

import (
	"sync"
	"testing"
	"time"
)

func TestNotifyReachesRegisteredNode(t *testing.T) {
	h := NewHub()
	ch, release := h.Register(7)
	defer release()

	if !h.Notify(7, 3) {
		t.Fatal("Notify returned false for a connected node")
	}
	select {
	case rev := <-ch:
		if rev != 3 {
			t.Errorf("revision = %d, want 3", rev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the revision bump")
	}
}

func TestNotifyToDisconnectedNodeIsFalse(t *testing.T) {
	h := NewHub()
	if h.Notify(99, 1) {
		t.Error("Notify returned true for a node that is not connected")
	}
	// This is not an error: the node reconciles on reconnect because state
	// lives in the database, not in the hub.
}

func TestReleaseRemovesTheNode(t *testing.T) {
	h := NewHub()
	_, release := h.Register(7)
	if !h.Online(7) {
		t.Fatal("node not reported online after Register")
	}
	release()
	if h.Online(7) {
		t.Error("node still online after release")
	}
	if h.Count() != 0 {
		t.Errorf("Count = %d, want 0", h.Count())
	}
}

func TestReconnectSupersedesTheOldStream(t *testing.T) {
	h := NewHub()
	first, releaseFirst := h.Register(7)
	second, releaseSecond := h.Register(7)
	defer releaseSecond()

	h.Notify(7, 5)
	select {
	case <-second:
	case <-time.After(time.Second):
		t.Fatal("the newest stream did not receive the bump")
	}
	select {
	case _, open := <-first:
		if open {
			t.Error("the superseded stream received a bump")
		}
	default:
	}
	releaseFirst() // must not panic or remove the live registration
	if !h.Online(7) {
		t.Error("releasing a superseded stream removed the live one")
	}
}

func TestNotifyNeverBlocks(t *testing.T) {
	h := NewHub()
	_, release := h.Register(7)
	defer release()
	// The agent is not reading. Notify must drop rather than block, because
	// a stalled agent must never stall an admin's HTTP request.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			h.Notify(7, int64(i))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Notify blocked on a slow consumer")
	}
}

func TestHubIsRaceFree(t *testing.T) {
	h := NewHub()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ch, release := h.Register(int64(i % 5))
			h.Notify(int64(i%5), int64(i))
			select {
			case <-ch:
			default:
			}
			release()
		}(i)
	}
	wg.Wait()
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/panel/control/...`
Expected: FAIL — `undefined: NewHub`.

- [ ] **Step 3: Implement the hub**

`internal/panel/control/hub.go`:

```go
// Package control hosts the gRPC control plane.
//
// It owns ALL stream state (spec section 3). HTTP handlers never touch a
// stream: they bump a revision in the store and call Hub.Notify, so an admin
// action and a node reconnect converge through one code path.
package control

import "sync"

// Hub tracks which nodes currently hold a control stream and fans revision
// bumps out to them.
//
// The hub is deliberately not durable. A bump that misses a disconnected node
// is not lost, because desired state lives in the database and the agent
// re-reconciles on reconnect.
type Hub struct {
	mu    sync.RWMutex
	conns map[int64]chan int64
}

func NewHub() *Hub {
	return &Hub{conns: make(map[int64]chan int64)}
}

// Register attaches a stream for nodeID and returns its bump channel plus a
// release function. A second Register for the same node supersedes the first,
// closing its channel, which is what happens when an agent reconnects before
// the panel notices the old stream died.
func (h *Hub) Register(nodeID int64) (<-chan int64, func()) {
	ch := make(chan int64, 1)

	h.mu.Lock()
	if existing, ok := h.conns[nodeID]; ok {
		close(existing)
	}
	h.conns[nodeID] = ch
	h.mu.Unlock()

	var once sync.Once
	release := func() {
		once.Do(func() {
			h.mu.Lock()
			// Only remove if we are still the live registration.
			if current, ok := h.conns[nodeID]; ok && current == ch {
				delete(h.conns, nodeID)
				close(ch)
			}
			h.mu.Unlock()
		})
	}
	return ch, release
}

// Notify delivers a revision bump. It reports whether a stream was connected.
//
// It never blocks: if the agent's buffer is full it drops the bump, because
// the agent will fetch the latest snapshot anyway and a stalled node must
// never stall an admin request.
func (h *Hub) Notify(nodeID, revision int64) bool {
	h.mu.RLock()
	ch, ok := h.conns[nodeID]
	h.mu.RUnlock()
	if !ok {
		return false
	}
	select {
	case ch <- revision:
	default:
	}
	return true
}

func (h *Hub) Online(nodeID int64) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.conns[nodeID]
	return ok
}

func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns)
}
```

- [ ] **Step 4: Write the failing peer-verification test**

`internal/panel/control/server_test.go`:

```go
package control

import (
	"context"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"errors"
	"math/big"
	"testing"
	"time"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	"github.com/amyrm/antimage/internal/panel/store"
)

func fakePeerCtx(certDER []byte) context.Context {
	cert, _ := x509.ParseCertificate(certDER)
	return peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{
			State: tlsStateWith(cert),
		},
	})
}

func TestVerifyPeerAcceptsAllowListedFingerprint(t *testing.T) {
	s, nodeID, certDER, fingerprint := enrolledNodeFixture(t)
	if err := setFingerprint(s, nodeID, fingerprint); err != nil {
		t.Fatalf("set fingerprint: %v", err)
	}
	got, err := VerifyPeer(fakePeerCtx(certDER), s)
	if err != nil {
		t.Fatalf("VerifyPeer: %v", err)
	}
	if got != nodeID {
		t.Errorf("node id = %d, want %d", got, nodeID)
	}
}

// Deleting a node must lock it out instantly. This is the allow-list
// revocation model standing in for a CRL.
func TestVerifyPeerRejectsRevokedFingerprint(t *testing.T) {
	s, nodeID, certDER, fingerprint := enrolledNodeFixture(t)
	_ = setFingerprint(s, nodeID, fingerprint)

	if err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM nodes WHERE id = ?`, nodeID)
		return err
	}); err != nil {
		t.Fatalf("delete node: %v", err)
	}

	if _, err := VerifyPeer(fakePeerCtx(certDER), s); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("err = %v, want ErrNotEnrolled — a deleted node must be locked out at once", err)
	}
}

func TestVerifyPeerRejectsUnknownCertificate(t *testing.T) {
	s, _, certDER, _ := enrolledNodeFixture(t)
	// Never recorded in nodes.cert_fingerprint.
	if _, err := VerifyPeer(fakePeerCtx(certDER), s); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("err = %v, want ErrNotEnrolled", err)
	}
}

func TestVerifyPeerRejectsMissingPeer(t *testing.T) {
	s, _, _, _ := enrolledNodeFixture(t)
	if _, err := VerifyPeer(context.Background(), s); err == nil {
		t.Fatal("VerifyPeer accepted a context with no peer")
	}
}

func setFingerprint(s *store.Store, nodeID int64, fp string) error {
	return s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`UPDATE nodes SET cert_fingerprint = ?, status = 'online', enrolled_at = ? WHERE id = ?`,
			fp, time.Now().Unix(), nodeID)
		return err
	})
}

var _ = big.NewInt
var _ = pkix.Name{}
```

Add `internal/panel/control/fixture_test.go` providing `enrolledNodeFixture` and `tlsStateWith`:

```go
package control

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/shared/secrets"
)

func tlsStateWith(cert *x509.Certificate) tls.ConnectionState {
	return tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
}

// enrolledNodeFixture returns a store, a node id, a certificate the panel CA
// signed for it, and that certificate's fingerprint.
func enrolledNodeFixture(t *testing.T) (*store.Store, int64, []byte, string) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var nodeID int64
	if err := s.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`INSERT INTO nodes (name, address, created_at) VALUES ('n1','1.2.3.4',?)`,
			time.Now().Unix())
		if err != nil {
			return err
		}
		nodeID, err = res.LastInsertId()
		return err
	}); err != nil {
		t.Fatalf("seed node: %v", err)
	}

	box, _ := secrets.NewBox(bytes.Repeat([]byte{9}, secrets.KeySize))
	ca, err := nodes.LoadOrCreateCA(context.Background(), s, box)
	if err != nil {
		t.Fatalf("LoadOrCreateCA: %v", err)
	}

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, _ := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: "x"}}, key)
	certDER, fingerprint, err := ca.SignNodeCert(csrDER, nodeID, time.Now().UTC())
	if err != nil {
		t.Fatalf("SignNodeCert: %v", err)
	}
	return s, nodeID, certDER, fingerprint
}
```

- [ ] **Step 5: Implement peer verification**

`internal/panel/control/server.go`:

```go
package control

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	"github.com/amyrm/antimage/internal/panel/store"
)

// ErrNotEnrolled means the presented certificate is not on the allow-list.
var ErrNotEnrolled = errors.New("node certificate is not enrolled")

// VerifyPeer authenticates a gRPC caller against nodes.cert_fingerprint.
//
// This is the revocation mechanism from spec section 7.3: the panel is the
// only verifier, so a connection is accepted only when its fingerprint is
// still recorded. Deleting a node locks it out immediately, with no CRL to
// distribute and no OCSP responder to run.
func VerifyPeer(ctx context.Context, s *store.Store) (int64, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return 0, errors.New("no peer information on context")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return 0, errors.New("connection is not mTLS")
	}
	if len(tlsInfo.State.PeerCertificates) == 0 {
		return 0, errors.New("peer presented no certificate")
	}

	sum := sha256.Sum256(tlsInfo.State.PeerCertificates[0].Raw)
	fingerprint := hex.EncodeToString(sum[:])

	var nodeID int64
	err := s.Read().QueryRowContext(ctx,
		`SELECT id FROM nodes WHERE cert_fingerprint = ?`, fingerprint).Scan(&nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotEnrolled
	}
	if err != nil {
		return 0, fmt.Errorf("look up node by fingerprint: %w", err)
	}
	return nodeID, nil
}
```

- [ ] **Step 6: Run and watch them pass**

Run: `go test ./internal/panel/control/... -race -count=1 -v`
Expected: PASS — six hub tests and four verification tests.

- [ ] **Step 7: Commit**

```bash
git add internal/panel/control
git commit -m "feat(control): stream hub and allow-list peer verification"
```

---

### Task 20: Enrollment and control RPC handlers

**Files:**
- Modify: `internal/panel/control/server.go`
- Create: `internal/panel/control/enroll_service.go`
- Create: `internal/panel/control/control_service.go`
- Test: `internal/panel/control/service_test.go`

**Interfaces:**
- Consumes: Tasks 12, 13, 18, 19, generated proto.
- Produces:
  - `type Deps struct { Store *store.Store; CA *nodes.CA; Hub *Hub; Now func() time.Time; DownloadURL string }`
  - `control.NewEnrollmentService(d Deps) *EnrollmentService` implementing `Enroll`
  - `control.NewControlService(d Deps) *ControlService` implementing `Stream` and `GetDesiredSnapshot`

- [ ] **Step 1: Write the failing tests**

`internal/panel/control/service_test.go`:

```go
package control

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/nodes"
	pb "github.com/amyrm/antimage/internal/shared/proto/antimage/v1"
	"github.com/amyrm/antimage/internal/shared/version"
)

func TestEnrollIssuesCertAndRecordsFingerprint(t *testing.T) {
	s, nodeID, _, _ := enrolledNodeFixture(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()

	token, err := nodes.IssueEnrollToken(ctx, s, nodeID, audit.SystemActor("test"), "req", now)
	if err != nil {
		t.Fatalf("IssueEnrollToken: %v", err)
	}
	deps := depsFor(t, s, now)
	svc := NewEnrollmentService(deps)

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, _ := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: "self-chosen"}}, key)

	resp, err := svc.Enroll(ctx, &pb.EnrollRequest{
		Token: token, CsrDer: csrDER,
		AgentVersion: "v0.1.0", ProtocolVersion: version.Protocol,
	})
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if resp.NodeId != nodeID {
		t.Errorf("node id = %d, want %d", resp.NodeId, nodeID)
	}
	cert, err := x509.ParseCertificate(resp.CertDer)
	if err != nil {
		t.Fatalf("parse issued cert: %v", err)
	}
	if cert.Subject.CommonName != itoa64(nodeID) {
		t.Errorf("CN = %q, want %d — the panel names the node, not the CSR",
			cert.Subject.CommonName, nodeID)
	}

	var status, fingerprint string
	if err := s.Read().QueryRow(
		`SELECT status, COALESCE(cert_fingerprint,'') FROM nodes WHERE id = ?`, nodeID,
	).Scan(&status, &fingerprint); err != nil {
		t.Fatalf("read node: %v", err)
	}
	if fingerprint == "" {
		t.Error("cert_fingerprint was not recorded; the node could never authenticate")
	}
	if status != "enrolling" && status != "online" {
		t.Errorf("status = %q, want enrolling or online", status)
	}
}

func TestEnrollRejectsReusedToken(t *testing.T) {
	s, nodeID, _, _ := enrolledNodeFixture(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	token, _ := nodes.IssueEnrollToken(ctx, s, nodeID, audit.SystemActor("test"), "req", now)
	svc := NewEnrollmentService(depsFor(t, s, now))

	req := func() *pb.EnrollRequest {
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		csrDER, _ := x509.CreateCertificateRequest(rand.Reader,
			&x509.CertificateRequest{Subject: pkix.Name{CommonName: "x"}}, key)
		return &pb.EnrollRequest{Token: token, CsrDer: csrDER,
			AgentVersion: "v0.1.0", ProtocolVersion: version.Protocol}
	}
	if _, err := svc.Enroll(ctx, req()); err != nil {
		t.Fatalf("first Enroll: %v", err)
	}
	if _, err := svc.Enroll(ctx, req()); err == nil {
		t.Fatal("a burnt token was accepted a second time")
	}
}

func TestEnrollRejectsProtocolSkew(t *testing.T) {
	s, nodeID, _, _ := enrolledNodeFixture(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	token, _ := nodes.IssueEnrollToken(ctx, s, nodeID, audit.SystemActor("test"), "req", now)
	svc := NewEnrollmentService(depsFor(t, s, now))

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, _ := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: "x"}}, key)

	_, err := svc.Enroll(ctx, &pb.EnrollRequest{
		Token: token, CsrDer: csrDER,
		AgentVersion: "v0.0.1", ProtocolVersion: version.Protocol + 99,
	})
	if err == nil {
		t.Fatal("Enroll accepted an incompatible protocol version instead of failing loudly")
	}
}

func TestGetDesiredSnapshotReturnsMatchingHash(t *testing.T) {
	s, nodeID, certDER, fingerprint := enrolledNodeFixture(t)
	_ = setFingerprint(s, nodeID, fingerprint)
	ctx := fakePeerCtx(certDER)
	now := time.Unix(1_700_000_000, 0).UTC()

	svc := NewControlService(depsFor(t, s, now))
	resp, err := svc.GetDesiredSnapshot(ctx, &pb.SnapshotRequest{NodeId: nodeID})
	if err != nil {
		t.Fatalf("GetDesiredSnapshot: %v", err)
	}
	if len(resp.Document) == 0 || len(resp.Sha256) != 64 {
		t.Fatalf("bad snapshot: %d document bytes, sha %q", len(resp.Document), resp.Sha256)
	}
	// Invariant 4: the agent re-hashes these exact bytes, so they must match.
	if got := sha256Hex(resp.Document); got != resp.Sha256 {
		t.Errorf("document hashes to %s but response claims %s", got, resp.Sha256)
	}
}

func TestGetDesiredSnapshotRefusesOtherNodes(t *testing.T) {
	s, nodeID, certDER, fingerprint := enrolledNodeFixture(t)
	_ = setFingerprint(s, nodeID, fingerprint)
	ctx := fakePeerCtx(certDER)

	svc := NewControlService(depsFor(t, s, time.Unix(1_700_000_000, 0).UTC()))
	if _, err := svc.GetDesiredSnapshot(ctx, &pb.SnapshotRequest{NodeId: nodeID + 500}); err == nil {
		t.Fatal("a node fetched another node's desired state")
	}
}
```

Add helpers to `internal/panel/control/fixture_test.go`:

```go
func depsFor(t *testing.T, s *store.Store, now time.Time) Deps {
	t.Helper()
	box, _ := secrets.NewBox(bytes.Repeat([]byte{9}, secrets.KeySize))
	ca, err := nodes.LoadOrCreateCA(context.Background(), s, box)
	if err != nil {
		t.Fatalf("LoadOrCreateCA: %v", err)
	}
	return Deps{
		Store: s, CA: ca, Hub: NewHub(),
		Now:         func() time.Time { return now },
		DownloadURL: "https://panel.example/agent",
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func itoa64(i int64) string { return strconv.FormatInt(i, 10) }
```

(Add `crypto/sha256`, `encoding/hex`, and `strconv` to that file's imports.)

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/panel/control/... -run 'Enroll|Snapshot'`
Expected: FAIL — `undefined: NewEnrollmentService`.

- [ ] **Step 3: Implement the enrollment service**

`internal/panel/control/enroll_service.go`:

```go
package control

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/store"
	pb "github.com/amyrm/antimage/internal/shared/proto/antimage/v1"
	"github.com/amyrm/antimage/internal/shared/version"
)

// Deps is everything the control plane needs. It is a struct rather than
// positional arguments so adding a dependency does not churn call sites.
type Deps struct {
	Store       *store.Store
	CA          *nodes.CA
	Hub         *Hub
	Now         func() time.Time
	DownloadURL string
}

func (d Deps) now() time.Time {
	if d.Now == nil {
		return time.Now().UTC()
	}
	return d.Now()
}

type EnrollmentService struct {
	deps Deps
}

func NewEnrollmentService(d Deps) *EnrollmentService { return &EnrollmentService{deps: d} }

// Enroll redeems a single-use token and issues a client certificate.
//
// The agent's private key never appears here: only its CSR does. The CSR's
// subject is ignored, because the token determines which node this is.
func (s *EnrollmentService) Enroll(ctx context.Context, req *pb.EnrollRequest) (*pb.EnrollResponse, error) {
	if req.ProtocolVersion != version.Protocol {
		return nil, status.Errorf(codes.FailedPrecondition,
			"agent speaks protocol %d, panel speaks %d: upgrade the agent",
			req.ProtocolVersion, version.Protocol)
	}

	now := s.deps.now()
	nodeID, err := nodes.RedeemEnrollToken(ctx, s.deps.Store, req.Token, now)
	if err != nil {
		// Deliberately vague: a caller must not learn whether a token exists.
		audit.BestEffort(ctx, s.deps.Store, "", audit.SystemActor("enrollment"), audit.Record{
			Action: "node.enroll", TargetType: "node", Result: "denied",
		})
		return nil, status.Error(codes.PermissionDenied, "enrollment token invalid")
	}

	certDER, fingerprint, err := s.deps.CA.SignNodeCert(req.CsrDer, nodeID, now)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "sign CSR: %v", err)
	}

	err = s.deps.Store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE nodes SET cert_fingerprint = ?, status = 'enrolling', enrolled_at = ?
			  WHERE id = ?`, fingerprint, now.Unix(), nodeID); err != nil {
			return fmt.Errorf("record fingerprint: %w", err)
		}
		return audit.InTx(ctx, tx, "", audit.SystemActor("enrollment"), audit.Record{
			Action:     "node.enroll",
			TargetType: "node",
			TargetID:   sql.NullInt64{Int64: nodeID, Valid: true},
			After: map[string]any{
				"fingerprint": fingerprint, "agent_version": req.AgentVersion,
			},
			Result: "ok",
		})
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "complete enrollment: %v", err)
	}

	return &pb.EnrollResponse{
		CertDer: certDER,
		CaDer:   s.deps.CA.CertDER(),
		NodeId:  nodeID,
	}, nil
}
```

- [ ] **Step 4: Implement the control service**

`internal/panel/control/control_service.go`:

```go
package control

import (
	"context"
	"database/sql"
	"errors"
	"io"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/amyrm/antimage/internal/panel/nodes"
	pb "github.com/amyrm/antimage/internal/shared/proto/antimage/v1"
	"github.com/amyrm/antimage/internal/shared/version"
)

type ControlService struct {
	deps Deps
}

func NewControlService(d Deps) *ControlService { return &ControlService{deps: d} }

// GetDesiredSnapshot returns the exact canonical bytes that were hashed.
func (s *ControlService) GetDesiredSnapshot(
	ctx context.Context, req *pb.SnapshotRequest,
) (*pb.SnapshotResponse, error) {
	callerID, err := VerifyPeer(ctx, s.deps.Store)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "not enrolled")
	}
	// A node may fetch only its own state.
	if req.NodeId != callerID {
		return nil, status.Error(codes.PermissionDenied, "node id mismatch")
	}

	var snap *nodes.Snapshot
	err = s.deps.Store.Write(ctx, func(tx *sql.Tx) error {
		var err error
		snap, err = nodes.BuildDesiredSnapshot(ctx, tx, callerID)
		return err
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "build snapshot: %v", err)
	}

	return &pb.SnapshotResponse{
		Revision: snap.Revision,
		Document: snap.Bytes,
		Sha256:   snap.SHA256,
	}, nil
}

// Stream holds the agent's long-lived connection. The agent dials in; the
// panel never dials the node.
func (s *ControlService) Stream(srv pb.Control_StreamServer) error {
	ctx := srv.Context()
	nodeID, err := VerifyPeer(ctx, s.deps.Store)
	if err != nil {
		return status.Error(codes.Unauthenticated, "not enrolled")
	}

	bumps, release := s.deps.Hub.Register(nodeID)
	defer release()

	// Receive loop feeds messages to the select below.
	type recvResult struct {
		msg *pb.AgentMessage
		err error
	}
	incoming := make(chan recvResult)
	go func() {
		defer close(incoming)
		for {
			msg, err := srv.Recv()
			select {
			case incoming <- recvResult{msg: msg, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case revision, ok := <-bumps:
			if !ok {
				// Superseded by a newer stream for this node.
				return status.Error(codes.Aborted, "stream superseded")
			}
			if err := srv.Send(&pb.PanelMessage{
				Payload: &pb.PanelMessage_RevisionBump{
					RevisionBump: &pb.RevisionBump{Revision: revision},
				},
			}); err != nil {
				return err
			}

		case in, ok := <-incoming:
			if !ok {
				return nil
			}
			if errors.Is(in.err, io.EOF) {
				return nil
			}
			if in.err != nil {
				return in.err
			}
			if err := s.handle(ctx, nodeID, in.msg, srv); err != nil {
				return err
			}
		}
	}
}

func (s *ControlService) handle(
	ctx context.Context, nodeID int64, msg *pb.AgentMessage, srv pb.Control_StreamServer,
) error {
	switch p := msg.Payload.(type) {
	case *pb.AgentMessage_Hello:
		if p.Hello.ProtocolVersion != version.Protocol {
			// Surface skew as an actionable state rather than misbehaving.
			return srv.Send(&pb.PanelMessage{
				Payload: &pb.PanelMessage_UpgradeRequired{
					UpgradeRequired: &pb.UpgradeRequired{
						PanelProtocolVersion: version.Protocol,
						DownloadUrl:          s.deps.DownloadURL,
					},
				},
			})
		}
		return s.onHello(ctx, nodeID, p.Hello, srv)

	case *pb.AgentMessage_Heartbeat:
		return s.onHeartbeat(ctx, nodeID, p.Heartbeat)

	case *pb.AgentMessage_ApplyReport:
		return s.onApplyReport(ctx, nodeID, p.ApplyReport)

	default:
		return nil // forward compatible: ignore unknown payloads
	}
}
```

`onHello`, `onHeartbeat`, and `onApplyReport` are implemented in Task 21.

- [ ] **Step 5: Stub the three handlers so the package compiles**

Append to `control_service.go`:

```go
// Implemented in Task 21.
func (s *ControlService) onHello(ctx context.Context, nodeID int64, h *pb.Hello, srv pb.Control_StreamServer) error {
	return nil
}

func (s *ControlService) onHeartbeat(ctx context.Context, nodeID int64, hb *pb.Heartbeat) error {
	return nil
}

func (s *ControlService) onApplyReport(ctx context.Context, nodeID int64, r *pb.ApplyReport) error {
	return nil
}
```

- [ ] **Step 6: Run and watch them pass**

Run: `go test ./internal/panel/control/... -race -count=1 -v`
Expected: PASS — five service tests plus the ten from Task 19.

- [ ] **Step 7: Commit**

```bash
git add internal/panel/control
git commit -m "feat(control): enrollment and control RPC handlers"
```

---

### Task 21: Apply reports, applied_revision, and Integrity detection

**Files:**
- Create: `internal/panel/store/migrations/00008_apply_runs.sql`
- Modify: `internal/panel/control/control_service.go` — replace the three stubs
- Create: `internal/panel/nodes/convergence.go`
- Test: `internal/panel/nodes/convergence_test.go`

**Interfaces:**
- Consumes: Tasks 12, 13, 20.
- Produces:
  - `nodes.RecordApplyRun(ctx, s *store.Store, in ApplyRunInput) (Outcome, error)`
  - `type ApplyRunInput struct { NodeID, TargetRevision int64; Converged, Deferred bool; Err, DocSHA256 string; Steps []StepOutcome; Now time.Time }`
  - `type StepOutcome struct { Seq int32; Kind, Disruption string; OK bool; Err string; DurationMS int64 }`
  - `type Outcome struct { Status string; AppliedRevision int64; Integrity bool }`
  - `nodes.RecordHello(ctx, s, nodeID int64, adapters []AdapterInfo, appliedRevision int64, docSHA string, now time.Time) error`
  - `nodes.RecordHeartbeat(ctx, s, nodeID int64, h HealthSample, now time.Time) error`

Invariant 6 and 7 both land here.

- [ ] **Step 1: Write the migration**

`internal/panel/store/migrations/00008_apply_runs.sql`:

```sql
-- +goose Up
CREATE TABLE node_apply_runs (
    id              INTEGER PRIMARY KEY,
    node_id         INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    target_revision INTEGER NOT NULL,
    started_at      INTEGER NOT NULL,
    finished_at     INTEGER,
    outcome         TEXT NOT NULL
                    CHECK (outcome IN ('converged','partial','deferred','failed','integrity'))
) STRICT;

CREATE INDEX node_apply_runs_node ON node_apply_runs (node_id, id DESC);

CREATE TABLE node_apply_steps (
    run_id      INTEGER NOT NULL REFERENCES node_apply_runs(id) ON DELETE CASCADE,
    seq         INTEGER NOT NULL,
    step_kind   TEXT NOT NULL,
    disruption  TEXT NOT NULL CHECK (disruption IN ('none','reload','restart','unknown')),
    outcome     TEXT NOT NULL CHECK (outcome IN ('ok','failed','skipped')),
    error       TEXT NOT NULL DEFAULT '',
    duration_ms INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (run_id, seq)
) STRICT;

CREATE TABLE node_health (
    node_id    INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    at         INTEGER NOT NULL,
    load1      REAL NOT NULL DEFAULT 0,
    mem_used   INTEGER NOT NULL DEFAULT 0,
    uptime_s   INTEGER NOT NULL DEFAULT 0,
    rtt_ms     INTEGER NOT NULL DEFAULT 0,
    adapter_status TEXT NOT NULL DEFAULT '[]',
    PRIMARY KEY (node_id, at)
) STRICT;

-- +goose Down
DROP TABLE node_health;
DROP TABLE node_apply_steps;
DROP TABLE node_apply_runs;
```

- [ ] **Step 2: Write the failing tests**

`internal/panel/nodes/convergence_test.go`:

```go
package nodes

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/store"
)

func bumpTo(t *testing.T, s *store.Store, nodeID int64, port int) *CommitResult {
	t.Helper()
	res, err := CommitNodeChange(context.Background(), s, nodeID,
		audit.SystemActor("test"), "req", "seed",
		func(tx *sql.Tx) error {
			_, err := tx.Exec(
				`INSERT INTO services (node_id, adapter_kind, params, enabled, created_at)
				 VALUES (?, 'stub', ?, 1, ?)`,
				nodeID, `{"port":`+itoa(port)+`}`, time.Now().Unix())
			return err
		})
	if err != nil {
		t.Fatalf("CommitNodeChange: %v", err)
	}
	return res
}

func TestConvergedRunAdvancesAppliedRevision(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	commit := bumpTo(t, f.store, f.nodeID, 443)

	out, err := RecordApplyRun(ctx, f.store, ApplyRunInput{
		NodeID: f.nodeID, TargetRevision: commit.Revision,
		Converged: true, DocSHA256: commit.SHA256,
		Steps: []StepOutcome{{Seq: 1, Kind: "write_service", Disruption: "restart", OK: true}},
		Now:   time.Unix(1_700_000_000, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("RecordApplyRun: %v", err)
	}
	if out.Status != "online" {
		t.Errorf("status = %q, want online", out.Status)
	}
	if out.AppliedRevision != commit.Revision {
		t.Errorf("applied_revision = %d, want %d", out.AppliedRevision, commit.Revision)
	}
}

// Invariant 7: partial application must NOT advance applied_revision.
func TestPartialRunLeavesAppliedRevisionBehind(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	commit := bumpTo(t, f.store, f.nodeID, 443)

	out, err := RecordApplyRun(ctx, f.store, ApplyRunInput{
		NodeID: f.nodeID, TargetRevision: commit.Revision,
		Converged: false, Err: "step 2 failed", DocSHA256: commit.SHA256,
		Steps: []StepOutcome{
			{Seq: 1, Kind: "write_service", Disruption: "restart", OK: true},
			{Seq: 2, Kind: "write_service", Disruption: "restart", OK: false, Err: "permission denied"},
		},
		Now: time.Unix(1_700_000_000, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("RecordApplyRun: %v", err)
	}
	if out.Status != "degraded" {
		t.Errorf("status = %q, want degraded", out.Status)
	}
	if out.AppliedRevision == commit.Revision {
		t.Fatal("applied_revision advanced on a partial apply — invariant 7 broken")
	}

	// The failure must remain inspectable per step.
	var stepErr string
	if err := f.store.Read().QueryRow(
		`SELECT error FROM node_apply_steps WHERE seq = 2`).Scan(&stepErr); err != nil {
		t.Fatalf("read step: %v", err)
	}
	if stepErr != "permission denied" {
		t.Errorf("step error = %q, want it preserved for the UI", stepErr)
	}
}

// Invariant 6: matching revision but mismatched hash is an integrity fault,
// never convergence.
func TestRevisionMatchWithHashMismatchIsIntegrity(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	commit := bumpTo(t, f.store, f.nodeID, 443)

	out, err := RecordApplyRun(ctx, f.store, ApplyRunInput{
		NodeID: f.nodeID, TargetRevision: commit.Revision,
		Converged: true,
		DocSHA256: "0000000000000000000000000000000000000000000000000000000000000000",
		Now:       time.Unix(1_700_000_000, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("RecordApplyRun: %v", err)
	}
	if !out.Integrity {
		t.Fatal("hash mismatch at a matching revision was not flagged as an integrity fault")
	}
	if out.Status != "integrity" {
		t.Errorf("status = %q, want integrity", out.Status)
	}
	if out.AppliedRevision == commit.Revision {
		t.Error("applied_revision advanced despite an integrity fault")
	}
}

func TestDeferredRunIsRecordedWithoutAdvancing(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	commit := bumpTo(t, f.store, f.nodeID, 443)

	out, err := RecordApplyRun(ctx, f.store, ApplyRunInput{
		NodeID: f.nodeID, TargetRevision: commit.Revision,
		Converged: false, Deferred: true, DocSHA256: commit.SHA256,
		Now: time.Unix(1_700_000_000, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("RecordApplyRun: %v", err)
	}
	if out.AppliedRevision == commit.Revision {
		t.Error("deferred work advanced applied_revision")
	}
	var outcome string
	if err := f.store.Read().QueryRow(
		`SELECT outcome FROM node_apply_runs ORDER BY id DESC LIMIT 1`).Scan(&outcome); err != nil {
		t.Fatalf("read run: %v", err)
	}
	if outcome != "deferred" {
		t.Errorf("outcome = %q, want deferred", outcome)
	}
}

func TestHeartbeatUpdatesLastSeenAndHealth(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()

	if err := RecordHeartbeat(ctx, f.store, f.nodeID, HealthSample{
		Load1: 0.5, MemUsed: 1 << 20, UptimeS: 3600,
		Adapters: []AdapterHealthSample{{Kind: "stub", OK: true, Detail: "ready"}},
	}, now); err != nil {
		t.Fatalf("RecordHeartbeat: %v", err)
	}
	var lastSeen sql.NullInt64
	if err := f.store.Read().QueryRow(
		`SELECT last_seen_at FROM nodes WHERE id = ?`, f.nodeID).Scan(&lastSeen); err != nil {
		t.Fatalf("read node: %v", err)
	}
	if !lastSeen.Valid || lastSeen.Int64 != now.Unix() {
		t.Errorf("last_seen_at = %v, want %d", lastSeen, now.Unix())
	}
	var n int
	_ = f.store.Read().QueryRow(`SELECT count(*) FROM node_health`).Scan(&n)
	if n != 1 {
		t.Errorf("node_health rows = %d, want 1", n)
	}
}

func TestHelloRecordsAdapterKindsWithoutBumpingRevision(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	commit := bumpTo(t, f.store, f.nodeID, 443)

	if err := RecordHello(ctx, f.store, f.nodeID,
		[]AdapterInfo{{Kind: "stub", Version: "1"}}, 0, "", time.Unix(1_700_000_000, 0).UTC()); err != nil {
		t.Fatalf("RecordHello: %v", err)
	}

	var kinds string
	var desired int64
	if err := f.store.Read().QueryRow(
		`SELECT adapter_kinds, desired_revision FROM nodes WHERE id = ?`, f.nodeID,
	).Scan(&kinds, &desired); err != nil {
		t.Fatalf("read node: %v", err)
	}
	if kinds != `["stub"]` {
		t.Errorf("adapter_kinds = %s, want [\"stub\"]", kinds)
	}
	// adapter_kinds is observed data and must never enter the desired
	// document, or every agent restart would bump the revision.
	if desired != commit.Revision {
		t.Errorf("desired_revision moved from %d to %d on Hello", commit.Revision, desired)
	}
}
```

- [ ] **Step 3: Run and watch them fail**

Run: `go test ./internal/panel/nodes/... -run 'Apply|Heartbeat|Hello|Integrity'`
Expected: FAIL — `undefined: RecordApplyRun`.

- [ ] **Step 4: Implement**

`internal/panel/nodes/convergence.go`:

```go
package nodes

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/store"
)

type StepOutcome struct {
	Seq        int32
	Kind       string
	Disruption string
	OK         bool
	Err        string
	DurationMS int64
}

type ApplyRunInput struct {
	NodeID         int64
	TargetRevision int64
	Converged      bool
	Deferred       bool
	Err            string
	DocSHA256      string
	Steps          []StepOutcome
	Now            time.Time
}

type Outcome struct {
	Status          string
	AppliedRevision int64
	Integrity       bool
}

// RecordApplyRun persists a convergence attempt and decides the node's state.
//
// It implements invariants 6 and 7:
//
//   - applied_revision advances only when the agent reports Converged AND the
//     hash it applied matches the hash the panel recorded for that revision.
//   - a revision match with a hash mismatch is an integrity fault, never
//     convergence.
func RecordApplyRun(ctx context.Context, s *store.Store, in ApplyRunInput) (Outcome, error) {
	var out Outcome

	err := s.Write(ctx, func(tx *sql.Tx) error {
		var expectedSHA string
		err := tx.QueryRowContext(ctx,
			`SELECT doc_sha256 FROM node_revisions WHERE node_id = ? AND revision = ?`,
			in.NodeID, in.TargetRevision).Scan(&expectedSHA)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("read expected hash: %w", err)
		}

		integrity := in.Converged && expectedSHA != "" && in.DocSHA256 != expectedSHA

		var (
			runOutcome string
			status     string
			advance    bool
		)
		switch {
		case integrity:
			runOutcome, status = "integrity", "integrity"
		case in.Deferred:
			runOutcome, status = "deferred", "online"
		case in.Converged:
			runOutcome, status, advance = "converged", "online", true
		case in.Err != "":
			runOutcome, status = "failed", "degraded"
		default:
			runOutcome, status = "partial", "degraded"
		}

		res, err := tx.ExecContext(ctx,
			`INSERT INTO node_apply_runs (node_id, target_revision, started_at, finished_at, outcome)
			 VALUES (?,?,?,?,?)`,
			in.NodeID, in.TargetRevision, in.Now.Unix(), in.Now.Unix(), runOutcome)
		if err != nil {
			return fmt.Errorf("insert apply run: %w", err)
		}
		runID, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("run id: %w", err)
		}

		for _, st := range in.Steps {
			stepOutcome := "ok"
			if !st.OK {
				stepOutcome = "failed"
			}
			disruption := st.Disruption
			switch disruption {
			case "none", "reload", "restart":
			default:
				disruption = "unknown"
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO node_apply_steps
				   (run_id, seq, step_kind, disruption, outcome, error, duration_ms)
				 VALUES (?,?,?,?,?,?,?)`,
				runID, st.Seq, st.Kind, disruption, stepOutcome, st.Err, st.DurationMS); err != nil {
				return fmt.Errorf("insert apply step %d: %w", st.Seq, err)
			}
		}

		if advance {
			if _, err := tx.ExecContext(ctx,
				`UPDATE nodes SET applied_revision = ?, status = ?, last_error = '' WHERE id = ?`,
				in.TargetRevision, status, in.NodeID); err != nil {
				return fmt.Errorf("advance applied_revision: %w", err)
			}
		} else {
			if _, err := tx.ExecContext(ctx,
				`UPDATE nodes SET status = ?, last_error = ? WHERE id = ?`,
				status, in.Err, in.NodeID); err != nil {
				return fmt.Errorf("update node status: %w", err)
			}
		}

		if integrity {
			if err := audit.InTx(ctx, tx, "", audit.SystemActor("reconciler"), audit.Record{
				Action:     "node.integrity_fault",
				TargetType: "node",
				TargetID:   sql.NullInt64{Int64: in.NodeID, Valid: true},
				After: map[string]any{
					"revision": in.TargetRevision,
					"expected": expectedSHA,
					"reported": in.DocSHA256,
				},
				Result: "failed",
			}); err != nil {
				return err
			}
		}

		var applied int64
		if err := tx.QueryRowContext(ctx,
			`SELECT applied_revision FROM nodes WHERE id = ?`, in.NodeID).Scan(&applied); err != nil {
			return fmt.Errorf("read applied_revision: %w", err)
		}
		out = Outcome{Status: status, AppliedRevision: applied, Integrity: integrity}
		return nil
	})
	if err != nil {
		return Outcome{}, err
	}
	return out, nil
}

type AdapterInfo struct {
	Kind    string
	Version string
}

// RecordHello caches the adapter kinds the agent reports.
//
// adapter_kinds is observed data, not configuration: it never enters the
// desired document, so an agent restart cannot bump a revision.
func RecordHello(
	ctx context.Context, s *store.Store, nodeID int64,
	adapters []AdapterInfo, appliedRevision int64, docSHA string, now time.Time,
) error {
	kinds := make([]string, 0, len(adapters))
	for _, a := range adapters {
		kinds = append(kinds, a.Kind)
	}
	encoded, err := json.Marshal(kinds)
	if err != nil {
		return fmt.Errorf("encode adapter kinds: %w", err)
	}

	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE nodes SET adapter_kinds = ?, last_seen_at = ?,
			        status = CASE WHEN status IN ('pending','enrolling','offline')
			                      THEN 'online' ELSE status END
			  WHERE id = ?`,
			string(encoded), now.Unix(), nodeID)
		return err
	})
}

type AdapterHealthSample struct {
	Kind   string `json:"kind"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type HealthSample struct {
	Load1    float64
	MemUsed  uint64
	UptimeS  uint64
	RTTMs    int64
	Adapters []AdapterHealthSample
}

func RecordHeartbeat(ctx context.Context, s *store.Store, nodeID int64, h HealthSample, now time.Time) error {
	adapters, err := json.Marshal(h.Adapters)
	if err != nil {
		return fmt.Errorf("encode adapter health: %w", err)
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO node_health
			   (node_id, at, load1, mem_used, uptime_s, rtt_ms, adapter_status)
			 VALUES (?,?,?,?,?,?,?)`,
			nodeID, now.Unix(), h.Load1, int64(h.MemUsed), int64(h.UptimeS),
			h.RTTMs, string(adapters)); err != nil {
			return fmt.Errorf("insert health sample: %w", err)
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE nodes SET last_seen_at = ?,
			        status = CASE WHEN status = 'offline' THEN 'online' ELSE status END
			  WHERE id = ?`, now.Unix(), nodeID)
		return err
	})
}
```

- [ ] **Step 5: Wire the three handlers**

Replace the stubs in `internal/panel/control/control_service.go`:

```go
func (s *ControlService) onHello(ctx context.Context, nodeID int64, h *pb.Hello, srv pb.Control_StreamServer) error {
	adapters := make([]nodes.AdapterInfo, 0, len(h.Adapters))
	for _, a := range h.Adapters {
		adapters = append(adapters, nodes.AdapterInfo{Kind: a.Kind, Version: a.Version})
	}
	if err := nodes.RecordHello(ctx, s.deps.Store, nodeID, adapters,
		h.AppliedRevision, h.DocSha256, s.deps.now()); err != nil {
		return err
	}
	// Tell the agent to reconcile immediately after connecting, so a node
	// that was offline during a change converges without waiting for a timer.
	return srv.Send(&pb.PanelMessage{
		Payload: &pb.PanelMessage_FetchNow{FetchNow: &pb.FetchNow{}},
	})
}

func (s *ControlService) onHeartbeat(ctx context.Context, nodeID int64, hb *pb.Heartbeat) error {
	sample := nodes.HealthSample{
		Load1: hb.Load1, MemUsed: hb.MemUsedBytes, UptimeS: hb.UptimeSeconds,
	}
	for _, a := range hb.AdapterHealth {
		sample.Adapters = append(sample.Adapters, nodes.AdapterHealthSample{
			Kind: a.Kind, OK: a.Ok, Detail: a.Detail,
		})
	}
	return nodes.RecordHeartbeat(ctx, s.deps.Store, nodeID, sample, s.deps.now())
}

func (s *ControlService) onApplyReport(ctx context.Context, nodeID int64, r *pb.ApplyReport) error {
	in := nodes.ApplyRunInput{
		NodeID: nodeID, TargetRevision: r.TargetRevision,
		Converged: r.Converged, Deferred: r.Deferred,
		Err: r.Error, DocSHA256: r.DocSha256, Now: s.deps.now(),
	}
	for _, st := range r.Steps {
		in.Steps = append(in.Steps, nodes.StepOutcome{
			Seq: st.Seq, Kind: st.Kind, Disruption: st.Disruption,
			OK: st.Ok, Err: st.Error, DurationMS: st.DurationMs,
		})
	}
	_, err := nodes.RecordApplyRun(ctx, s.deps.Store, in)
	return err
}
```

- [ ] **Step 6: Run and watch them pass**

Run: `go test ./internal/panel/... -race -count=1`
Expected: PASS across `nodes`, `control`, `store`, `auth`, `rbac`, `audit`.

- [ ] **Step 7: Commit**

```bash
git add internal/panel
git commit -m "feat(nodes): apply-run recording with integrity detection and gated applied_revision"
```

---

### Task 22: The agent — config, dial, and the run loop

**Files:**
- Create: `internal/node/agent/config.go`
- Create: `internal/node/agent/enroll.go`
- Create: `internal/node/agent/client.go`
- Create: `cmd/antimage-node/main.go`
- Test: `internal/node/agent/config_test.go`, `internal/node/agent/client_test.go`

**Interfaces:**
- Consumes: Tasks 14–17.
- Produces:
  - `agent.LoadConfig(path string) (*Config, error)`; `type Config struct { PanelURL, Token, CAFingerprint, StateDir string; NodeID int64 }`
  - `agent.Enroll(ctx, cfg *Config) (tls.Certificate, []byte, int64, error)` — generates the keypair locally
  - `agent.NewClient(cfg *Config, ad adapter.Adapter, clk Clock) *Client`; `(*Client).Run(ctx) error`
  - `agent.ErrHashMismatch`

- [ ] **Step 1: Write the failing config tests**

`internal/node/agent/config_test.go`:

```go
package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "node.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadConfigParsesAllFields(t *testing.T) {
	path := writeConfig(t, `
panel_url: https://panel.example:8443
token: abc123
ca_fingerprint: `+"deadbeef"+`
state_dir: /var/lib/antimage
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.PanelURL != "https://panel.example:8443" {
		t.Errorf("PanelURL = %q", cfg.PanelURL)
	}
	if cfg.Token != "abc123" || cfg.CAFingerprint != "deadbeef" {
		t.Errorf("token/fingerprint = %q/%q", cfg.Token, cfg.CAFingerprint)
	}
	if cfg.StateDir != "/var/lib/antimage" {
		t.Errorf("StateDir = %q", cfg.StateDir)
	}
}

func TestLoadConfigRejectsMissingPanelURL(t *testing.T) {
	path := writeConfig(t, "token: abc\nca_fingerprint: dead\n")
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("LoadConfig accepted a config with no panel_url")
	}
}

// The pinned fingerprint is what protects the agent from a hijacked DNS
// record, so a config without one must be refused rather than defaulting to
// system trust.
func TestLoadConfigRejectsMissingCAFingerprint(t *testing.T) {
	path := writeConfig(t, "panel_url: https://p.example\ntoken: abc\n")
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("LoadConfig accepted a config with no ca_fingerprint")
	}
}

func TestLoadConfigDefaultsStateDir(t *testing.T) {
	path := writeConfig(t, "panel_url: https://p.example\ntoken: abc\nca_fingerprint: dead\n")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.StateDir != DefaultStateDir {
		t.Errorf("StateDir = %q, want %q", cfg.StateDir, DefaultStateDir)
	}
}

func TestLoadConfigMissingFileIsAnError(t *testing.T) {
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("LoadConfig accepted a missing file")
	}
}
```

- [ ] **Step 2: Write the failing hash-verification test**

`internal/node/agent/client_test.go`:

```go
package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
)

// The agent re-hashes the snapshot before applying it, as cheap insurance
// against a truncated or buggy response.
func TestVerifySnapshotAcceptsMatchingHash(t *testing.T) {
	doc := []byte(`{"schema_version":1}`)
	sum := sha256.Sum256(doc)
	if err := verifySnapshot(doc, hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("verifySnapshot rejected a valid snapshot: %v", err)
	}
}

func TestVerifySnapshotRejectsMismatch(t *testing.T) {
	doc := []byte(`{"schema_version":1}`)
	err := verifySnapshot(doc, "0000000000000000000000000000000000000000000000000000000000000000")
	if !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("err = %v, want ErrHashMismatch", err)
	}
}

func TestVerifySnapshotRejectsTruncatedDocument(t *testing.T) {
	doc := []byte(`{"schema_version":1,"services":[]}`)
	sum := sha256.Sum256(doc)
	if err := verifySnapshot(doc[:10], hex.EncodeToString(sum[:])); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("truncated document accepted: %v", err)
	}
}

func TestBackoffGrowsAndCaps(t *testing.T) {
	var last time.Duration
	for attempt := 1; attempt <= 20; attempt++ {
		d := reconnectBackoff(attempt)
		if d > MaxReconnectBackoff {
			t.Fatalf("attempt %d backoff %v exceeds cap %v", attempt, d, MaxReconnectBackoff)
		}
		if d < last {
			t.Fatalf("backoff shrank from %v to %v", last, d)
		}
		last = d
	}
	if last != MaxReconnectBackoff {
		t.Errorf("final backoff = %v, want the %v cap", last, MaxReconnectBackoff)
	}
}
```

(Add `"time"` to that file's imports.)

- [ ] **Step 3: Run and watch them fail**

Run: `go test ./internal/node/agent/... -run 'Config|Snapshot|Backoff'`
Expected: FAIL — `undefined: LoadConfig`.

- [ ] **Step 4: Implement the config**

```bash
go get gopkg.in/yaml.v3@latest
```

`internal/node/agent/config.go`:

```go
package agent

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

const DefaultStateDir = "/var/lib/antimage"

// Config is /etc/antimage/node.yaml, written by the bootstrap script.
type Config struct {
	PanelURL string `yaml:"panel_url"`
	// Token is the single-use enrollment token. It is consumed on first run
	// and cleared from the file afterwards.
	Token string `yaml:"token"`
	// CAFingerprint pins the panel's CA. Without it a hijacked DNS record
	// could impersonate the panel, so a config lacking it is refused.
	CAFingerprint string `yaml:"ca_fingerprint"`
	StateDir      string `yaml:"state_dir"`
	NodeID        int64  `yaml:"node_id"`
}

func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.PanelURL == "" {
		return nil, errors.New("panel_url is required")
	}
	if cfg.CAFingerprint == "" {
		return nil, errors.New("ca_fingerprint is required: refusing to trust the system CA pool")
	}
	if cfg.StateDir == "" {
		cfg.StateDir = DefaultStateDir
	}
	return &cfg, nil
}

// Save rewrites the config, used to clear the consumed token and record the
// node id after enrollment.
func (c *Config) Save(path string) error {
	body, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
```

- [ ] **Step 5: Implement snapshot verification and backoff**

`internal/node/agent/client.go` (first portion):

```go
package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"time"
)

// ErrHashMismatch means the snapshot's bytes do not hash to the value the
// panel reported.
var ErrHashMismatch = errors.New("snapshot hash mismatch")

const (
	HeartbeatInterval    = 30 * time.Second
	ReconcileInterval    = 5 * time.Minute
	MaxReconnectBackoff  = 60 * time.Second
	baseReconnectBackoff = time.Second
)

// verifySnapshot re-hashes the document the panel sent. The panel is trusted,
// but a truncated response or an encoding bug is not, and applying a partial
// document would converge a node to the wrong state silently.
func verifySnapshot(document []byte, claimed string) error {
	sum := sha256.Sum256(document)
	got := hex.EncodeToString(sum[:])
	if got != claimed {
		return fmt.Errorf("%w: computed %s, panel reported %s", ErrHashMismatch, got, claimed)
	}
	return nil
}

// reconnectBackoff doubles with jitter and caps, so a panel restart is not
// thundering-herded by hundreds of agents reconnecting in lockstep.
func reconnectBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := baseReconnectBackoff
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= MaxReconnectBackoff {
			return MaxReconnectBackoff
		}
	}
	return d
}

// jitter spreads reconnects and reconcile timers.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	delta := time.Duration(rand.Int63n(int64(d) / 4))
	return d - delta/2
}
```

- [ ] **Step 6: Run the unit tests and watch them pass**

Run: `go test ./internal/node/agent/... -race -count=1 -v`
Expected: PASS — five config tests, four client tests, and the seven reconciler tests.

- [ ] **Step 7: Implement enrollment and the run loop**

`internal/node/agent/enroll.go`:

```go
package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	pb "github.com/amyrm/antimage/internal/shared/proto/antimage/v1"
	"github.com/amyrm/antimage/internal/shared/version"
)

// pinnedCAVerifier accepts the panel only if its certificate chain contains a
// certificate whose SHA-256 matches the pinned fingerprint.
func pinnedCAVerifier(fingerprint string) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		for _, raw := range rawCerts {
			sum := sha256.Sum256(raw)
			if hex.EncodeToString(sum[:]) == fingerprint {
				return nil
			}
		}
		return fmt.Errorf("panel certificate does not match the pinned CA fingerprint %s", fingerprint)
	}
}

// Enroll generates a keypair locally, sends only the CSR, and stores the
// signed certificate. The private key never leaves this host.
func Enroll(ctx context.Context, cfg *Config) (tls.Certificate, []byte, int64, error) {
	var zero tls.Certificate

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return zero, nil, 0, fmt.Errorf("generate node key: %w", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: "antimage-node"}}, key)
	if err != nil {
		return zero, nil, 0, fmt.Errorf("create CSR: %w", err)
	}

	creds := credentials.NewTLS(&tls.Config{
		InsecureSkipVerify:    true, // replaced by the pinned verifier below
		VerifyPeerCertificate: pinnedCAVerifier(cfg.CAFingerprint),
		MinVersion:            tls.VersionTLS13,
	})
	conn, err := grpc.NewClient(cfg.PanelURL, grpc.WithTransportCredentials(creds))
	if err != nil {
		return zero, nil, 0, fmt.Errorf("dial panel: %w", err)
	}
	defer func() { _ = conn.Close() }()

	resp, err := pb.NewEnrollmentClient(conn).Enroll(ctx, &pb.EnrollRequest{
		Token:           cfg.Token,
		CsrDer:          csrDER,
		AgentVersion:    version.Version,
		ProtocolVersion: version.Protocol,
	})
	if err != nil {
		return zero, nil, 0, fmt.Errorf("enroll: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return zero, nil, 0, fmt.Errorf("marshal node key: %w", err)
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return zero, nil, 0, fmt.Errorf("create state dir: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: resp.CertDer})
	if err := os.WriteFile(filepath.Join(cfg.StateDir, "node.key"), keyPEM, 0o600); err != nil {
		return zero, nil, 0, fmt.Errorf("write node key: %w", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.StateDir, "node.crt"), certPEM, 0o600); err != nil {
		return zero, nil, 0, fmt.Errorf("write node cert: %w", err)
	}

	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return zero, nil, 0, fmt.Errorf("load issued keypair: %w", err)
	}
	return pair, resp.CaDer, resp.NodeId, nil
}
```

Append the run loop to `internal/node/agent/client.go`:

```go
// Client holds the control stream and drives reconciliation.
type Client struct {
	cfg  *Config
	ad   adapter.Adapter
	clk  Clock
	rec  *Reconciler
	cert tls.Certificate
	caDER []byte
}

func NewClient(cfg *Config, ad adapter.Adapter, clk Clock, cert tls.Certificate, caDER []byte) *Client {
	return &Client{
		cfg: cfg, ad: ad, clk: clk, cert: cert, caDER: caDER,
		rec: NewReconciler(ad, clk, ReconcileOptions{MaxRetries: 3, RetryBase: 2 * time.Second}),
	}
}

// Run dials the panel and reconnects forever with capped, jittered backoff.
func (c *Client) Run(ctx context.Context) error {
	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := c.session(ctx)
		if err == nil || errors.Is(err, context.Canceled) {
			return err
		}
		attempt++
		slog.WarnContext(ctx, "control stream ended; reconnecting",
			"attempt", attempt, "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.clk.After(jitter(reconnectBackoff(attempt))):
		}
	}
}

func (c *Client) dial() (*grpc.ClientConn, error) {
	pool := x509.NewCertPool()
	caCert, err := x509.ParseCertificate(c.caDER)
	if err != nil {
		return nil, fmt.Errorf("parse panel CA: %w", err)
	}
	pool.AddCert(caCert)

	creds := credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{c.cert},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS13,
	})
	return grpc.NewClient(c.cfg.PanelURL, grpc.WithTransportCredentials(creds))
}

// session runs one connection: Hello, then heartbeats, reconcile timer, and
// panel messages until the stream dies.
func (c *Client) session(ctx context.Context) error {
	conn, err := c.dial()
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer func() { _ = conn.Close() }()

	client := pb.NewControlClient(conn)
	stream, err := client.Stream(ctx)
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}

	desc := c.ad.Descriptor()
	if err := stream.Send(&pb.AgentMessage{Payload: &pb.AgentMessage_Hello{Hello: &pb.Hello{
		NodeId: c.cfg.NodeID, AgentVersion: version.Version,
		ProtocolVersion: version.Protocol,
		Adapters: []*pb.AdapterDescriptor{{
			Kind: string(desc.Kind), Version: desc.Version,
			HotUserAdd: desc.Caps.HotUserAdd, SelfAccounting: desc.Caps.SelfAccounting,
			RequiresPki: desc.Caps.RequiresPKI, ServiceSchema: desc.Caps.ServiceSchema,
		}},
	}}}); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}

	incoming := make(chan *pb.PanelMessage)
	recvErr := make(chan error, 1)
	go func() {
		defer close(incoming)
		for {
			msg, err := stream.Recv()
			if err != nil {
				recvErr <- err
				return
			}
			select {
			case incoming <- msg:
			case <-ctx.Done():
				return
			}
		}
	}()

	heartbeat := c.clk.After(HeartbeatInterval)
	reconcile := c.clk.After(jitter(ReconcileInterval))

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case err := <-recvErr:
			return err

		case msg, ok := <-incoming:
			if !ok {
				return errors.New("stream closed")
			}
			switch msg.Payload.(type) {
			case *pb.PanelMessage_RevisionBump, *pb.PanelMessage_FetchNow:
				if err := c.reconcileOnce(ctx, client, stream); err != nil {
					slog.ErrorContext(ctx, "reconcile failed", "error", err)
				}
			case *pb.PanelMessage_UpgradeRequired:
				return errors.New("panel requires an agent upgrade")
			}

		case <-heartbeat:
			if err := c.sendHeartbeat(ctx, stream); err != nil {
				return err
			}
			heartbeat = c.clk.After(HeartbeatInterval)

		case <-reconcile:
			if err := c.reconcileOnce(ctx, client, stream); err != nil {
				slog.ErrorContext(ctx, "scheduled reconcile failed", "error", err)
			}
			reconcile = c.clk.After(jitter(ReconcileInterval))
		}
	}
}

func (c *Client) reconcileOnce(ctx context.Context, client pb.ControlClient, stream pb.Control_StreamClient) error {
	snap, err := client.GetDesiredSnapshot(ctx, &pb.SnapshotRequest{NodeId: c.cfg.NodeID})
	if err != nil {
		return fmt.Errorf("fetch snapshot: %w", err)
	}
	if err := verifySnapshot(snap.Document, snap.Sha256); err != nil {
		return err
	}

	var desired adapter.Desired
	if err := json.Unmarshal(snap.Document, &desired); err != nil {
		return fmt.Errorf("decode desired document: %w", err)
	}

	run, runErr := c.rec.Converge(ctx, desired)

	report := &pb.ApplyReport{
		TargetRevision: snap.Revision, Converged: run.Converged,
		Deferred: run.Deferred, Error: run.Err, DocSha256: snap.Sha256,
	}
	for _, st := range run.Steps {
		report.Steps = append(report.Steps, &pb.StepResult{
			Seq: int32(st.Seq), Ok: st.OK, Error: st.Err,
			DurationMs: st.Duration.Milliseconds(),
		})
	}
	if err := stream.Send(&pb.AgentMessage{
		Payload: &pb.AgentMessage_ApplyReport{ApplyReport: report},
	}); err != nil {
		return fmt.Errorf("send apply report: %w", err)
	}
	return runErr
}

func (c *Client) sendHeartbeat(ctx context.Context, stream pb.Control_StreamClient) error {
	health, err := c.ad.Probe(ctx)
	if err != nil {
		health = adapter.Health{OK: false, Detail: err.Error()}
	}
	sample := sysinfo.Sample()
	return stream.Send(&pb.AgentMessage{Payload: &pb.AgentMessage_Heartbeat{
		Heartbeat: &pb.Heartbeat{
			Load1: sample.Load1, MemUsedBytes: sample.MemUsed, UptimeSeconds: sample.UptimeS,
			AdapterHealth: []*pb.AdapterHealth{{
				Kind: string(c.ad.Descriptor().Kind), Ok: health.OK, Detail: health.Detail,
			}},
		},
	}})
}
```

Add the imports this needs to `client.go`: `context`, `crypto/tls`, `crypto/x509`, `encoding/json`, `log/slog`, `google.golang.org/grpc`, `google.golang.org/grpc/credentials`, the adapter package, `pb`, `version`, and `internal/node/sysinfo`.

- [ ] **Step 8: Implement sysinfo**

`internal/node/sysinfo/sysinfo.go`:

```go
// Package sysinfo reads coarse host metrics for heartbeats.
package sysinfo

import (
	"os"
	"strconv"
	"strings"
)

type Metrics struct {
	Load1   float64
	MemUsed uint64
	UptimeS uint64
}

// Sample reads /proc. Missing or unreadable files yield zeros rather than
// errors: a heartbeat must never fail because a metric is unavailable.
func Sample() Metrics {
	var m Metrics

	if raw, err := os.ReadFile("/proc/loadavg"); err == nil {
		if fields := strings.Fields(string(raw)); len(fields) > 0 {
			m.Load1, _ = strconv.ParseFloat(fields[0], 64)
		}
	}
	if raw, err := os.ReadFile("/proc/uptime"); err == nil {
		if fields := strings.Fields(string(raw)); len(fields) > 0 {
			seconds, _ := strconv.ParseFloat(fields[0], 64)
			m.UptimeS = uint64(seconds)
		}
	}
	if raw, err := os.ReadFile("/proc/meminfo"); err == nil {
		var total, available uint64
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			value, _ := strconv.ParseUint(fields[1], 10, 64)
			switch fields[0] {
			case "MemTotal:":
				total = value * 1024
			case "MemAvailable:":
				available = value * 1024
			}
		}
		if total > available {
			m.MemUsed = total - available
		}
	}
	return m
}
```

- [ ] **Step 9: Write the agent main**

`cmd/antimage-node/main.go`:

```go
package main

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/amyrm/antimage/internal/node/adapter/stub"
	"github.com/amyrm/antimage/internal/node/agent"
	"github.com/amyrm/antimage/internal/shared/version"
)

func main() {
	configPath := flag.String("config", "/etc/antimage/node.yaml", "path to node.yaml")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		os.Stdout.WriteString(version.Version + "\n")
		return
	}

	cfg, err := agent.LoadConfig(*configPath)
	if err != nil {
		slog.Error("load config", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cert, caDER, nodeID, err := loadOrEnroll(ctx, cfg, *configPath)
	if err != nil {
		slog.Error("enrollment", "error", err)
		os.Exit(1)
	}
	cfg.NodeID = nodeID

	ad := stub.New(filepath.Join(cfg.StateDir, "services"))
	client := agent.NewClient(cfg, ad, agent.SystemClock{}, cert, caDER)

	slog.Info("antimage-node starting", "version", version.Version, "node_id", nodeID)
	if err := client.Run(ctx); err != nil && ctx.Err() == nil {
		slog.Error("agent stopped", "error", err)
		os.Exit(1)
	}
}

// loadOrEnroll reuses an existing certificate when present, so a restart does
// not consume a new enrollment token.
func loadOrEnroll(ctx context.Context, cfg *agent.Config, configPath string) (tls.Certificate, []byte, int64, error) {
	certPath := filepath.Join(cfg.StateDir, "node.crt")
	keyPath := filepath.Join(cfg.StateDir, "node.key")
	caPath := filepath.Join(cfg.StateDir, "panel-ca.crt")

	if certPEM, err := os.ReadFile(certPath); err == nil {
		keyPEM, err := os.ReadFile(keyPath)
		if err != nil {
			return tls.Certificate{}, nil, 0, err
		}
		caPEM, err := os.ReadFile(caPath)
		if err != nil {
			return tls.Certificate{}, nil, 0, err
		}
		pair, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return tls.Certificate{}, nil, 0, err
		}
		block, _ := pem.Decode(caPEM)
		if block == nil {
			return tls.Certificate{}, nil, 0, os.ErrInvalid
		}
		return pair, block.Bytes, cfg.NodeID, nil
	}

	pair, caDER, nodeID, err := agent.Enroll(ctx, cfg)
	if err != nil {
		return tls.Certificate{}, nil, 0, err
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	if err := os.WriteFile(caPath, caPEM, 0o600); err != nil {
		return tls.Certificate{}, nil, 0, err
	}
	// Burn the token from disk: it is single-use and now spent.
	cfg.Token = ""
	cfg.NodeID = nodeID
	if err := cfg.Save(configPath); err != nil {
		return tls.Certificate{}, nil, 0, err
	}
	return pair, caDER, nodeID, nil
}
```

- [ ] **Step 10: Verify everything builds and passes**

Run: `make build && make test && make check-imports`
Expected: three binaries in `bin/`, all tests PASS, import boundary clean.

- [ ] **Step 11: Commit**

```bash
git add internal/node cmd/antimage-node go.mod go.sum
git commit -m "feat(agent): enrollment, control stream, and reconcile loop"
```

---

# Phase F — HTTP API

### Task 23: Router, middleware, error model, and login

**Files:**
- Create: `internal/panel/httpapi/errors.go`, `middleware.go`, `router.go`, `auth_handlers.go`
- Test: `internal/panel/httpapi/auth_handlers_test.go`

**Interfaces:**
- Consumes: `auth` (Tasks 5–8), `rbac` (Tasks 9–10), `audit` (Task 11), `store`.
- Produces:
  - `type Deps struct { Store *store.Store; Sessions *auth.Sessions; Limiter *auth.Limiter; Hub *control.Hub; CA *nodes.CA; Now func() time.Time }`
  - `httpapi.NewRouter(d Deps) http.Handler`
  - `httpapi.RequestID(ctx) string`, `httpapi.ActorFrom(ctx) *rbac.Actor`
  - `httpapi.WriteError(w, status int, code, message string)`

- [ ] **Step 1: Write the failing tests**

`internal/panel/httpapi/auth_handlers_test.go`:

```go
package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/amyrm/antimage/internal/panel/auth"
)

func TestLoginSucceedsAndSetsHardenedCookie(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "alice", "correct horse battery staple", "super_admin")

	res := env.post(t, "/api/v1/auth/login",
		`{"username":"alice","password":"correct horse battery staple"}`, "")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body)
	}

	cookies := res.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies, want 1", len(cookies))
	}
	c := cookies[0]
	if c.Name != auth.CookieName {
		t.Errorf("cookie name = %q, want %q", c.Name, auth.CookieName)
	}
	if !c.HttpOnly {
		t.Error("cookie is not HttpOnly")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Error("cookie is not SameSite=Strict")
	}
}

func TestLoginFailureIsGenericAndAudited(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "alice", "right", "super_admin")

	unknown := env.post(t, "/api/v1/auth/login", `{"username":"nobody","password":"x"}`, "")
	wrong := env.post(t, "/api/v1/auth/login", `{"username":"alice","password":"wrong"}`, "")

	if unknown.Code != http.StatusUnauthorized || wrong.Code != http.StatusUnauthorized {
		t.Fatalf("status codes = %d/%d, want 401/401", unknown.Code, wrong.Code)
	}
	if unknown.Body.String() != wrong.Body.String() {
		t.Errorf("responses differ, so username existence leaks:\n%s\n%s",
			unknown.Body.String(), wrong.Body.String())
	}

	var denied int
	if err := env.store.Read().QueryRow(
		`SELECT count(*) FROM audit_log WHERE action = 'auth.login' AND result = 'denied'`,
	).Scan(&denied); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if denied != 2 {
		t.Errorf("denied login audit rows = %d, want 2", denied)
	}
}

func TestLoginLocksOutAfterRepeatedFailures(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "alice", "right", "super_admin")

	for i := 0; i < auth.AccountFailureLimit; i++ {
		env.post(t, "/api/v1/auth/login", `{"username":"alice","password":"wrong"}`, "")
	}
	res := env.post(t, "/api/v1/auth/login", `{"username":"alice","password":"right"}`, "")
	if res.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 even with the correct password", res.Code)
	}
	if res.Header().Get("Retry-After") == "" {
		t.Error("Retry-After header missing")
	}
}

func TestUnauthenticatedRequestIsRejected(t *testing.T) {
	env := newTestEnv(t)
	res := env.get(t, "/api/v1/nodes", "")
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.Code)
	}
}

func TestLogoutRevokesTheSessionImmediately(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "alice", "pw", "super_admin")
	token := env.login(t, "alice", "pw")

	if res := env.get(t, "/api/v1/nodes", token); res.Code != http.StatusOK {
		t.Fatalf("pre-logout status = %d, want 200", res.Code)
	}
	if res := env.post(t, "/api/v1/auth/logout", "", token); res.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, want 204", res.Code)
	}
	if res := env.get(t, "/api/v1/nodes", token); res.Code != http.StatusUnauthorized {
		t.Fatalf("post-logout status = %d, want 401", res.Code)
	}
}

func TestEveryResponseCarriesARequestID(t *testing.T) {
	env := newTestEnv(t)
	res := env.get(t, "/api/v1/nodes", "")
	if res.Header().Get("X-Request-ID") == "" {
		t.Error("X-Request-ID header missing; audit correlation depends on it")
	}
}

func TestErrorBodyShape(t *testing.T) {
	env := newTestEnv(t)
	res := env.get(t, "/api/v1/nodes", "")
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body.Error.Code == "" || body.Error.Message == "" {
		t.Errorf("error body missing code or message: %+v", body)
	}
	if strings.Contains(body.Error.Message, "sql") {
		t.Error("error message leaks internals")
	}
}
```

- [ ] **Step 2: Write the test harness**

`internal/panel/httpapi/env_test.go`:

```go
package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/auth"
	"github.com/amyrm/antimage/internal/panel/control"
	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

type testEnv struct {
	store   *store.Store
	handler http.Handler
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	now := func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	h := NewRouter(Deps{
		Store:    s,
		Sessions: auth.NewSessions(s, now),
		Limiter:  auth.NewLimiter(s, now),
		Hub:      control.NewHub(),
		Now:      now,
	})
	return &testEnv{store: s, handler: h}
}

func (e *testEnv) seedAdmin(t *testing.T, username, password, role string) int64 {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	perms, err := json.Marshal(rbac.BuiltinRoles()[role])
	if err != nil {
		t.Fatalf("marshal perms: %v", err)
	}

	var adminID int64
	err = e.store.Write(context.Background(), func(tx *sql.Tx) error {
		var roleID int64
		err := tx.QueryRow(`SELECT id FROM roles WHERE name = ?`, role).Scan(&roleID)
		if err == sql.ErrNoRows {
			res, err := tx.Exec(
				`INSERT INTO roles (name, is_builtin, permissions) VALUES (?, 1, ?)`,
				role, string(perms))
			if err != nil {
				return err
			}
			roleID, err = res.LastInsertId()
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		res, err := tx.Exec(
			`INSERT INTO admins (username, password_hash, role_id, created_at) VALUES (?,?,?,?)`,
			username, hash, roleID, time.Now().Unix())
		if err != nil {
			return err
		}
		adminID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("seedAdmin: %v", err)
	}
	return adminID
}

func (e *testEnv) do(t *testing.T, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://panel.local")
	req.Host = "panel.local"
	if token != "" {
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func (e *testEnv) get(t *testing.T, path, token string) *httptest.ResponseRecorder {
	return e.do(t, http.MethodGet, path, "", token)
}

func (e *testEnv) post(t *testing.T, path, body, token string) *httptest.ResponseRecorder {
	return e.do(t, http.MethodPost, path, body, token)
}

// login returns the session cookie value.
func (e *testEnv) login(t *testing.T, username, password string) string {
	t.Helper()
	res := e.post(t, "/api/v1/auth/login",
		`{"username":"`+username+`","password":"`+password+`"}`, "")
	if res.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", res.Code, res.Body)
	}
	for _, c := range res.Result().Cookies() {
		if c.Name == auth.CookieName {
			return c.Value
		}
	}
	t.Fatal("no session cookie in login response")
	return ""
}
```

- [ ] **Step 3: Run and watch them fail**

Run: `go test ./internal/panel/httpapi/...`
Expected: FAIL — `undefined: NewRouter`.

- [ ] **Step 4: Implement the error model**

`internal/panel/httpapi/errors.go`:

```go
// Package httpapi serves the panel's JSON API and the embedded UI.
package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// WriteError emits a uniform error envelope. Messages are written for
// operators, never copied from internal errors, so a SQL failure cannot leak
// schema details to a reseller.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	var body errorBody
	body.Error.Code = code
	body.Error.Message = message

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("write error response", "error", err)
	}
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write json response", "error", err)
	}
}
```

- [ ] **Step 5: Implement the middleware**

`internal/panel/httpapi/middleware.go`:

```go
package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/amyrm/antimage/internal/panel/auth"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

type ctxKey int

const (
	ctxRequestID ctxKey = iota
	ctxActor
	ctxSessionID
)

func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(ctxRequestID).(string)
	return id
}

func ActorFrom(ctx context.Context) *rbac.Actor {
	a, _ := ctx.Value(ctxActor).(*rbac.Actor)
	return a
}

func sessionIDFrom(ctx context.Context) int64 {
	id, _ := ctx.Value(ctxSessionID).(int64)
	return id
}

// requestIDMiddleware stamps every request so audit rows, logs, and the
// client's error report can be correlated.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := make([]byte, 12)
		_, _ = rand.Read(raw)
		id := base64.RawURLEncoding.EncodeToString(raw)

		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxRequestID, id)))
	})
}

// originMiddleware rejects cross-site state changes. SameSite=Strict already
// covers browsers; this covers everything else.
func originMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		if origin != "" {
			u, err := url.Parse(origin)
			if err != nil || !strings.EqualFold(u.Host, r.Host) {
				WriteError(w, http.StatusForbidden, "bad_origin", "cross-origin request rejected")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// authMiddleware resolves the session into an rbac.Actor with permissions and
// scope allow-lists already loaded, so handlers never query them ad hoc.
func (d Deps) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(auth.CookieName)
		if err != nil {
			WriteError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
			return
		}
		session, err := d.Sessions.Lookup(r.Context(), cookie.Value)
		if err != nil {
			if !errors.Is(err, auth.ErrSessionInvalid) {
				WriteError(w, http.StatusInternalServerError, "internal", "session lookup failed")
				return
			}
			WriteError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
			return
		}
		actor, err := d.loadActor(r.Context(), session.AdminID)
		if err != nil {
			WriteError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
			return
		}

		ctx := context.WithValue(r.Context(), ctxActor, actor)
		ctx = context.WithValue(ctx, ctxSessionID, session.ID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// readOnlyMiddleware is defence in depth: the readonly role already lacks
// write permissions, but a blanket rejection means a future handler that
// forgets its Check still cannot mutate.
func readOnlyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor := ActorFrom(r.Context())
		if actor != nil && actor.RoleName == "readonly" {
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
				WriteError(w, http.StatusForbidden, "forbidden", "this account is read-only")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
```

- [ ] **Step 6: Implement actor loading and the router**

`internal/panel/httpapi/router.go`:

```go
package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/amyrm/antimage/internal/panel/auth"
	"github.com/amyrm/antimage/internal/panel/control"
	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

type Deps struct {
	Store    *store.Store
	Sessions *auth.Sessions
	Limiter  *auth.Limiter
	Hub      *control.Hub
	CA       *nodes.CA
	Now      func() time.Time
}

func (d Deps) now() time.Time {
	if d.Now == nil {
		return time.Now().UTC()
	}
	return d.Now()
}

// loadActor resolves permissions and scope allow-lists once per request.
func (d Deps) loadActor(ctx context.Context, adminID int64) (*rbac.Actor, error) {
	var (
		roleName string
		rawPerms string
	)
	err := d.Store.Read().QueryRowContext(ctx,
		`SELECT r.name, r.permissions
		   FROM admins a JOIN roles r ON r.id = a.role_id
		  WHERE a.id = ? AND a.status = 'active'`, adminID).Scan(&roleName, &rawPerms)
	if err != nil {
		return nil, fmt.Errorf("load admin %d: %w", adminID, err)
	}

	var perms []rbac.Permission
	if err := json.Unmarshal([]byte(rawPerms), &perms); err != nil {
		return nil, fmt.Errorf("decode permissions: %w", err)
	}

	actor := &rbac.Actor{
		AdminID:    adminID,
		RoleName:   roleName,
		IsSuper:    roleName == "super_admin",
		Perms:      make(map[rbac.Permission]struct{}, len(perms)),
		NodeIDs:    map[int64]struct{}{},
		ServiceIDs: map[int64]struct{}{},
	}
	for _, p := range perms {
		actor.Perms[p] = struct{}{}
	}

	rows, err := d.Store.Read().QueryContext(ctx,
		`SELECT scope_type, scope_id FROM admin_scopes WHERE admin_id = ?`, adminID)
	if err != nil {
		return nil, fmt.Errorf("load scopes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var kind string
		var id int64
		if err := rows.Scan(&kind, &id); err != nil {
			return nil, fmt.Errorf("scan scope: %w", err)
		}
		switch kind {
		case "node":
			actor.NodeIDs[id] = struct{}{}
		case "service":
			actor.ServiceIDs[id] = struct{}{}
		}
	}
	return actor, rows.Err()
}

func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(requestIDMiddleware, originMiddleware)

	r.Route("/api/v1", func(api chi.Router) {
		api.Post("/auth/login", d.handleLogin)

		api.Group(func(private chi.Router) {
			private.Use(d.authMiddleware, readOnlyMiddleware)

			private.Post("/auth/logout", d.handleLogout)
			private.Get("/auth/me", d.handleMe)

			private.Get("/nodes", d.handleListNodes)
			private.Post("/nodes", d.handleCreateNode)
			private.Get("/nodes/{nodeID}", d.handleGetNode)
			private.Delete("/nodes/{nodeID}", d.handleDeleteNode)
			private.Post("/nodes/{nodeID}/enroll-token", d.handleIssueEnrollToken)
			private.Get("/nodes/{nodeID}/revisions", d.handleListRevisions)
			private.Get("/nodes/{nodeID}/apply-runs", d.handleListApplyRuns)

			private.Post("/nodes/{nodeID}/services", d.handleCreateService)
			private.Put("/services/{serviceID}", d.handleUpdateService)
			private.Delete("/services/{serviceID}", d.handleDeleteService)

			private.Get("/audit", d.handleListAudit)
			private.Get("/sessions", d.handleListSessions)
			private.Delete("/sessions/{sessionID}", d.handleRevokeSession)

			private.Get("/events", d.handleEvents)
		})
	})

	r.Handle("/*", d.uiHandler())
	return r
}
```

- [ ] **Step 7: Implement the auth handlers**

`internal/panel/httpapi/auth_handlers.go`:

```go
package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/auth"
)

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	TOTP     string `json:"totp"`
}

func (d Deps) handleLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ip := clientIP(r)

	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}

	wait, err := d.Limiter.Check(ctx, req.Username, ip)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "login unavailable")
		return
	}
	if wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())))
		audit.BestEffort(ctx, d.Store, RequestID(ctx),
			audit.Actor{Type: audit.ActorSystem, Label: "login", IP: ip},
			audit.Record{Action: "auth.lockout", TargetType: "admin", Result: "denied"})
		WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many attempts; try again later")
		return
	}

	deny := func() {
		_ = d.Limiter.RecordFailure(ctx, req.Username, ip)
		audit.BestEffort(ctx, d.Store, RequestID(ctx),
			audit.Actor{Type: audit.ActorSystem, Label: "login", IP: ip},
			audit.Record{Action: "auth.login", TargetType: "admin", Result: "denied"})
		// One message for every failure mode: unknown user, wrong password,
		// suspended account, bad TOTP.
		WriteError(w, http.StatusUnauthorized, "invalid_credentials", "invalid credentials")
	}

	var (
		adminID int64
		hash    string
		status  string
	)
	err = d.Store.Read().QueryRowContext(ctx,
		`SELECT id, password_hash, status FROM admins WHERE username = ? COLLATE NOCASE`,
		req.Username).Scan(&adminID, &hash, &status)
	if errors.Is(err, sql.ErrNoRows) {
		// Hash anyway, so response timing does not reveal whether the user exists.
		_, _ = auth.VerifyPassword(
			"$argon2id$v=19$m=65536,t=3,p=4$YWFhYWFhYWFhYWFhYWFhYQ$"+
				"YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE", req.Password)
		deny()
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "login unavailable")
		return
	}

	ok, err := auth.VerifyPassword(hash, req.Password)
	if err != nil || !ok || status != "active" {
		deny()
		return
	}

	if err := d.Limiter.Reset(ctx, req.Username, ip); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "login unavailable")
		return
	}

	token, err := d.Sessions.Create(ctx, adminID, ip, r.UserAgent())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not start session")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		Expires:  d.now().Add(auth.AbsoluteLifetime),
	})
	audit.BestEffort(ctx, d.Store, RequestID(ctx),
		audit.AdminActor(adminID, ip),
		audit.Record{Action: "auth.login", TargetType: "admin",
			TargetID: sql.NullInt64{Int64: adminID, Valid: true}, Result: "ok"})

	WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (d Deps) handleLogout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if id := sessionIDFrom(ctx); id != 0 {
		if err := d.Sessions.Revoke(ctx, id); err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "logout failed")
			return
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: auth.CookieName, Value: "", Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
		Expires: time.Unix(0, 0), MaxAge: -1,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (d Deps) handleMe(w http.ResponseWriter, r *http.Request) {
	actor := ActorFrom(r.Context())
	perms := make([]string, 0, len(actor.Perms))
	for p := range actor.Perms {
		perms = append(perms, string(p))
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"admin_id":    actor.AdminID,
		"role":        actor.RoleName,
		"is_super":    actor.IsSuper,
		"permissions": perms,
	})
}
```

- [ ] **Step 8: Add chi and a placeholder UI handler**

```bash
go get github.com/go-chi/chi/v5@latest
```

`internal/panel/httpapi/ui.go` (replaced properly in Task 30):

```go
package httpapi

import "net/http"

// uiHandler serves the embedded single-page app. Task 30 replaces this with
// the real embed.FS handler; until then it keeps the router complete.
func (d Deps) uiHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "ui not built", http.StatusNotFound)
	})
}
```

- [ ] **Step 9: Run and watch them pass**

Run: `go test ./internal/panel/httpapi/... -race -count=1 -v`
Expected: FAIL to compile until Task 24 supplies the node handlers. Add temporary stubs in `internal/panel/httpapi/nodes.go` returning `http.StatusNotImplemented` for every handler named in the router except `handleListNodes`, which Task 24 implements first. Then re-run: the seven auth tests PASS.

- [ ] **Step 10: Commit**

```bash
git add internal/panel/httpapi go.mod go.sum
git commit -m "feat(httpapi): router, request IDs, origin checks, and hardened login"
```

---

### Task 24: Node and service endpoints with schema validation

**Files:**
- Create: `internal/panel/httpapi/nodes.go`, `internal/panel/httpapi/services.go`
- Create: `internal/panel/nodes/schema.go`
- Test: `internal/panel/httpapi/nodes_test.go`

**Interfaces:**
- Consumes: `store.ListNodes`/`GetNode` (Task 10), `CommitNodeChange` (Task 13), `IssueEnrollToken` (Task 18), `Hub.Notify` (Task 19).
- Produces:
  - `nodes.ValidateServiceParams(schema json.RawMessage, params json.RawMessage) error`
  - `nodes.ErrSchemaViolation`
  - Handlers listed in the Task 23 router.

- [ ] **Step 1: Write the failing tests**

`internal/panel/httpapi/nodes_test.go`:

```go
package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestCreateNodeThenListIt(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	token := env.login(t, "root", "pw")

	res := env.post(t, "/api/v1/nodes", `{"name":"de-1","address":"1.2.3.4"}`, token)
	if res.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", res.Code, res.Body)
	}

	list := env.get(t, "/api/v1/nodes", token)
	var body struct {
		Nodes []struct {
			ID     int64  `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"nodes"`
	}
	if err := json.NewDecoder(list.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Nodes) != 1 || body.Nodes[0].Name != "de-1" {
		t.Fatalf("nodes = %+v, want one named de-1", body.Nodes)
	}
	if body.Nodes[0].Status != "pending" {
		t.Errorf("status = %q, want pending before enrollment", body.Nodes[0].Status)
	}
}

func TestResellerCannotSeeAnotherResellersNode(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	env.seedAdmin(t, "reseller", "pw", "reseller")
	rootToken := env.login(t, "root", "pw")

	if res := env.post(t, "/api/v1/nodes", `{"name":"secret","address":"9.9.9.9"}`, rootToken); res.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", res.Code, res.Body)
	}

	resellerToken := env.login(t, "reseller", "pw")
	list := env.get(t, "/api/v1/nodes", resellerToken)
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d", list.Code)
	}
	var body struct {
		Nodes []json.RawMessage `json:"nodes"`
	}
	_ = json.NewDecoder(list.Body).Decode(&body)
	if len(body.Nodes) != 0 {
		t.Fatalf("ungranted reseller saw %d nodes, want 0", len(body.Nodes))
	}
}

func TestDuplicateNodeNameIsRejected(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	token := env.login(t, "root", "pw")

	env.post(t, "/api/v1/nodes", `{"name":"de-1","address":"1.2.3.4"}`, token)
	res := env.post(t, "/api/v1/nodes", `{"name":"de-1","address":"5.6.7.8"}`, token)
	if res.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", res.Code)
	}
}

func TestEnrollTokenIsReturnedOnceWithACommand(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	token := env.login(t, "root", "pw")
	env.post(t, "/api/v1/nodes", `{"name":"de-1","address":"1.2.3.4"}`, token)

	res := env.post(t, "/api/v1/nodes/1/enroll-token", "", token)
	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body)
	}
	var body struct {
		Token     string `json:"token"`
		Command   string `json:"command"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Token == "" || body.ExpiresAt == 0 {
		t.Fatalf("incomplete response: %+v", body)
	}
	if body.Command == "" {
		t.Error("no bootstrap command returned; the SSH-free path depends on it")
	}
}

func TestCreateServiceBumpsRevision(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	token := env.login(t, "root", "pw")
	env.post(t, "/api/v1/nodes", `{"name":"de-1","address":"1.2.3.4"}`, token)

	res := env.post(t, "/api/v1/nodes/1/services",
		`{"adapter_kind":"stub","params":{"port":443}}`, token)
	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body)
	}

	var desired, applied int64
	if err := env.store.Read().QueryRow(
		`SELECT desired_revision, applied_revision FROM nodes WHERE id = 1`,
	).Scan(&desired, &applied); err != nil {
		t.Fatalf("read node: %v", err)
	}
	if desired != 1 {
		t.Errorf("desired_revision = %d, want 1", desired)
	}
	if applied != 0 {
		t.Errorf("applied_revision = %d, want 0 — nothing has converged yet", applied)
	}
}

func TestServiceParamsAreValidatedAgainstTheSchema(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	token := env.login(t, "root", "pw")
	env.post(t, "/api/v1/nodes", `{"name":"de-1","address":"1.2.3.4"}`, token)

	for _, bad := range []string{
		`{"adapter_kind":"stub","params":{}}`,                       // missing port
		`{"adapter_kind":"stub","params":{"port":"443"}}`,           // wrong type
		`{"adapter_kind":"stub","params":{"port":99999}}`,           // out of range
		`{"adapter_kind":"stub","params":{"port":443,"junk":true}}`, // extra property
	} {
		res := env.post(t, "/api/v1/nodes/1/services", bad, token)
		if res.Code != http.StatusUnprocessableEntity {
			t.Errorf("body %s gave status %d, want 422", bad, res.Code)
		}
	}
}

func TestUnknownAdapterKindIsRejected(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	token := env.login(t, "root", "pw")
	env.post(t, "/api/v1/nodes", `{"name":"de-1","address":"1.2.3.4"}`, token)

	res := env.post(t, "/api/v1/nodes/1/services",
		`{"adapter_kind":"wireguard","params":{}}`, token)
	if res.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 for an unknown adapter", res.Code)
	}
}

func TestDeleteNodeIsAuditedAndRemovesTheFingerprint(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	token := env.login(t, "root", "pw")
	env.post(t, "/api/v1/nodes", `{"name":"de-1","address":"1.2.3.4"}`, token)

	if res := env.do(t, http.MethodDelete, "/api/v1/nodes/1", "", token); res.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", res.Code)
	}
	var remaining int
	_ = env.store.Read().QueryRow(`SELECT count(*) FROM nodes`).Scan(&remaining)
	if remaining != 0 {
		t.Errorf("nodes remaining = %d, want 0", remaining)
	}
	var audited int
	_ = env.store.Read().QueryRow(
		`SELECT count(*) FROM audit_log WHERE action = 'node.delete'`).Scan(&audited)
	if audited != 1 {
		t.Errorf("node.delete audit rows = %d, want 1", audited)
	}
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/panel/httpapi/... -run Node`
Expected: FAIL — handlers return 501 from the Task 23 stubs.

- [ ] **Step 3: Implement schema validation**

```bash
go get github.com/santhosh-tekuri/jsonschema/v6@latest
```

`internal/panel/nodes/schema.go`:

```go
package nodes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/amyrm/antimage/internal/node/adapter"
	"github.com/amyrm/antimage/internal/node/adapter/stub"
)

// ErrSchemaViolation means service params failed their adapter's schema.
var ErrSchemaViolation = errors.New("service params violate the adapter schema")

// KnownAdapters returns the descriptors the panel can validate against.
//
// SP1 ships only the stub. SP2, SP5, and SP6 register their descriptors here,
// which is the whole extension point: the panel never learns protocol config
// formats, only how to fetch a schema and validate against it.
func KnownAdapters() map[string]adapter.Descriptor {
	s := stub.New("")
	return map[string]adapter.Descriptor{
		string(stub.Kind): s.Descriptor(),
	}
}

func ValidateServiceParams(schema json.RawMessage, params json.RawMessage) error {
	compiler := jsonschema.NewCompiler()
	schemaDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return fmt.Errorf("parse adapter schema: %w", err)
	}
	if err := compiler.AddResource("adapter.json", schemaDoc); err != nil {
		return fmt.Errorf("register adapter schema: %w", err)
	}
	compiled, err := compiler.Compile("adapter.json")
	if err != nil {
		return fmt.Errorf("compile adapter schema: %w", err)
	}

	paramsDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(params))
	if err != nil {
		return fmt.Errorf("%w: params are not valid JSON", ErrSchemaViolation)
	}
	if err := compiled.Validate(paramsDoc); err != nil {
		return fmt.Errorf("%w: %s", ErrSchemaViolation, err)
	}
	return nil
}
```

- [ ] **Step 4: Implement the node handlers**

`internal/panel/httpapi/nodes.go`:

```go
package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

func (d Deps) actorAudit(r *http.Request) audit.Actor {
	return audit.AdminActor(ActorFrom(r.Context()).AdminID, clientIP(r))
}

func pathInt64(r *http.Request, key string) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, key), 10, 64)
}

// authorize is the single chokepoint every handler calls.
func authorize(w http.ResponseWriter, r *http.Request, p rbac.Permission, t rbac.Target) bool {
	if err := rbac.Check(ActorFrom(r.Context()), p, t); err != nil {
		WriteError(w, http.StatusForbidden, "forbidden", "not permitted")
		return false
	}
	return true
}

func (d Deps) handleListNodes(w http.ResponseWriter, r *http.Request) {
	if !authorize(w, r, rbac.PermNodeRead, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	// The store filters by scope independently of the check above.
	rows, err := d.Store.ListNodes(r.Context(), rbac.ScopeOf(ActorFrom(r.Context())))
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not list nodes")
		return
	}

	type nodeDTO struct {
		ID              int64  `json:"id"`
		Name            string `json:"name"`
		Address         string `json:"address"`
		Status          string `json:"status"`
		DesiredRevision int64  `json:"desired_revision"`
		AppliedRevision int64  `json:"applied_revision"`
		LastSeenAt      *int64 `json:"last_seen_at"`
		Online          bool   `json:"online"`
	}
	out := make([]nodeDTO, 0, len(rows))
	for _, n := range rows {
		dto := nodeDTO{
			ID: n.ID, Name: n.Name, Address: n.Address, Status: n.Status,
			DesiredRevision: n.DesiredRevision, AppliedRevision: n.AppliedRevision,
			Online: d.Hub.Online(n.ID),
		}
		if n.LastSeenAt.Valid {
			seen := n.LastSeenAt.Int64
			dto.LastSeenAt = &seen
		}
		out = append(out, dto)
	}
	WriteJSON(w, http.StatusOK, map[string]any{"nodes": out})
}

func (d Deps) handleGetNode(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "nodeID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node id")
		return
	}
	if !authorize(w, r, rbac.PermNodeRead, rbac.Target{Kind: rbac.TargetNode, ID: id}) {
		return
	}
	node, err := d.Store.GetNode(r.Context(), rbac.ScopeOf(ActorFrom(r.Context())), id)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "not_found", "node not found")
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not load node")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"id": node.ID, "name": node.Name, "address": node.Address,
		"status": node.Status, "desired_revision": node.DesiredRevision,
		"applied_revision": node.AppliedRevision, "online": d.Hub.Online(node.ID),
	})
}

func (d Deps) handleCreateNode(w http.ResponseWriter, r *http.Request) {
	if !authorize(w, r, rbac.PermNodeWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	var req struct {
		Name    string `json:"name"`
		Address string `json:"address"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Address) == "" {
		WriteError(w, http.StatusUnprocessableEntity, "validation", "name and address are required")
		return
	}

	ctx := r.Context()
	var nodeID int64
	err := d.Store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, created_at) VALUES (?,?,?)`,
			req.Name, req.Address, d.now().Unix())
		if err != nil {
			return err
		}
		nodeID, err = res.LastInsertId()
		if err != nil {
			return err
		}
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(r), audit.Record{
			Action: "node.create", TargetType: "node",
			TargetID: sql.NullInt64{Int64: nodeID, Valid: true},
			After:    map[string]any{"name": req.Name, "address": req.Address},
			Result:   "ok",
		})
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			WriteError(w, http.StatusConflict, "conflict", "a node with that name already exists")
			return
		}
		WriteError(w, http.StatusInternalServerError, "internal", "could not create node")
		return
	}
	WriteJSON(w, http.StatusCreated, map[string]any{"id": nodeID})
}

func (d Deps) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "nodeID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node id")
		return
	}
	if !authorize(w, r, rbac.PermNodeWrite, rbac.Target{Kind: rbac.TargetNode, ID: id}) {
		return
	}
	ctx := r.Context()
	// Deleting the row removes cert_fingerprint, which is the allow-list, so
	// the node is locked out on its next connection attempt.
	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM nodes WHERE id = ?`, id); err != nil {
			return err
		}
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(r), audit.Record{
			Action: "node.delete", TargetType: "node",
			TargetID: sql.NullInt64{Int64: id, Valid: true}, Result: "ok",
		})
	})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not delete node")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d Deps) handleIssueEnrollToken(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "nodeID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node id")
		return
	}
	if !authorize(w, r, rbac.PermNodeEnroll, rbac.Target{Kind: rbac.TargetNode, ID: id}) {
		return
	}
	now := d.now()
	token, err := nodes.IssueEnrollToken(r.Context(), d.Store, id,
		d.actorAudit(r), RequestID(r.Context()), now)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not issue token")
		return
	}
	scheme := "https"
	command := fmt.Sprintf(
		"curl -fsSL %s://%s/install.sh | sudo bash -s -- --panel %s://%s --token %s",
		scheme, r.Host, scheme, r.Host, token)

	// Returned once and never retrievable again: only the hash is stored.
	WriteJSON(w, http.StatusCreated, map[string]any{
		"token":      token,
		"command":    command,
		"expires_at": now.Add(nodes.EnrollTokenTTL).Unix(),
	})
}

func (d Deps) handleListRevisions(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "nodeID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node id")
		return
	}
	if !authorize(w, r, rbac.PermNodeRead, rbac.Target{Kind: rbac.TargetNode, ID: id}) {
		return
	}
	rows, err := d.Store.Read().QueryContext(r.Context(),
		`SELECT revision, created_at, actor_type, actor_label, reason, doc_sha256
		   FROM node_revisions WHERE node_id = ? ORDER BY revision DESC LIMIT 100`, id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not list revisions")
		return
	}
	defer func() { _ = rows.Close() }()

	type revisionDTO struct {
		Revision   int64  `json:"revision"`
		CreatedAt  int64  `json:"created_at"`
		ActorType  string `json:"actor_type"`
		ActorLabel string `json:"actor_label"`
		Reason     string `json:"reason"`
		SHA256     string `json:"sha256"`
	}
	out := []revisionDTO{}
	for rows.Next() {
		var rev revisionDTO
		if err := rows.Scan(&rev.Revision, &rev.CreatedAt, &rev.ActorType,
			&rev.ActorLabel, &rev.Reason, &rev.SHA256); err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "could not read revisions")
			return
		}
		out = append(out, rev)
	}
	WriteJSON(w, http.StatusOK, map[string]any{"revisions": out})
}

func (d Deps) handleListApplyRuns(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "nodeID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node id")
		return
	}
	if !authorize(w, r, rbac.PermNodeRead, rbac.Target{Kind: rbac.TargetNode, ID: id}) {
		return
	}
	rows, err := d.Store.Read().QueryContext(r.Context(),
		`SELECT r.id, r.target_revision, r.started_at, r.outcome,
		        COALESCE(s.seq,0), COALESCE(s.step_kind,''), COALESCE(s.disruption,''),
		        COALESCE(s.outcome,''), COALESCE(s.error,''), COALESCE(s.duration_ms,0)
		   FROM node_apply_runs r
		   LEFT JOIN node_apply_steps s ON s.run_id = r.id
		  WHERE r.node_id = ?
		  ORDER BY r.id DESC, s.seq ASC
		  LIMIT 500`, id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not list apply runs")
		return
	}
	defer func() { _ = rows.Close() }()

	type stepDTO struct {
		Seq        int    `json:"seq"`
		Kind       string `json:"kind"`
		Disruption string `json:"disruption"`
		Outcome    string `json:"outcome"`
		Error      string `json:"error"`
		DurationMS int64  `json:"duration_ms"`
	}
	type runDTO struct {
		ID             int64     `json:"id"`
		TargetRevision int64     `json:"target_revision"`
		StartedAt      int64     `json:"started_at"`
		Outcome        string    `json:"outcome"`
		Steps          []stepDTO `json:"steps"`
	}
	var out []runDTO
	byID := map[int64]int{}
	for rows.Next() {
		var (
			run  runDTO
			step stepDTO
		)
		if err := rows.Scan(&run.ID, &run.TargetRevision, &run.StartedAt, &run.Outcome,
			&step.Seq, &step.Kind, &step.Disruption, &step.Outcome,
			&step.Error, &step.DurationMS); err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "could not read apply runs")
			return
		}
		idx, seen := byID[run.ID]
		if !seen {
			run.Steps = []stepDTO{}
			out = append(out, run)
			idx = len(out) - 1
			byID[run.ID] = idx
		}
		if step.Seq != 0 {
			out[idx].Steps = append(out[idx].Steps, step)
		}
	}
	if out == nil {
		out = []runDTO{}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"runs": out})
}

var _ = time.Now
```

- [ ] **Step 5: Implement the service handlers**

`internal/panel/httpapi/services.go`:

```go
package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

type serviceRequest struct {
	AdapterKind string          `json:"adapter_kind"`
	Params      json.RawMessage `json:"params"`
	Enabled     *bool           `json:"enabled"`
}

// validateService checks the adapter exists and its params satisfy the
// schema that adapter publishes.
func validateService(req serviceRequest) error {
	desc, ok := nodes.KnownAdapters()[req.AdapterKind]
	if !ok {
		return errors.New("unknown adapter kind")
	}
	if len(req.Params) == 0 {
		req.Params = json.RawMessage(`{}`)
	}
	return nodes.ValidateServiceParams(desc.Caps.ServiceSchema, req.Params)
}

func (d Deps) handleCreateService(w http.ResponseWriter, r *http.Request) {
	nodeID, err := pathInt64(r, "nodeID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node id")
		return
	}
	if !authorize(w, r, rbac.PermServiceWrite, rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}
	var req serviceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if err := validateService(req); err != nil {
		WriteError(w, http.StatusUnprocessableEntity, "validation", err.Error())
		return
	}

	ctx := r.Context()
	enabled := 1
	if req.Enabled != nil && !*req.Enabled {
		enabled = 0
	}

	var serviceID int64
	result, err := nodes.CommitNodeChange(ctx, d.Store, nodeID,
		d.actorAudit(r), RequestID(ctx), "create service",
		func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx,
				`INSERT INTO services (node_id, adapter_kind, params, enabled, created_at)
				 VALUES (?,?,?,?,?)`,
				nodeID, req.AdapterKind, string(req.Params), enabled, d.now().Unix())
			if err != nil {
				return err
			}
			serviceID, err = res.LastInsertId()
			return err
		})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not create service")
		return
	}

	// control owns the stream; the handler only signals that state moved.
	if result.Changed {
		d.Hub.Notify(nodeID, result.Revision)
	}
	WriteJSON(w, http.StatusCreated, map[string]any{
		"id": serviceID, "revision": result.Revision, "changed": result.Changed,
	})
}

func (d Deps) handleUpdateService(w http.ResponseWriter, r *http.Request) {
	serviceID, err := pathInt64(r, "serviceID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid service id")
		return
	}
	ctx := r.Context()

	var nodeID int64
	if err := d.Store.Read().QueryRowContext(ctx,
		`SELECT node_id FROM services WHERE id = ?`, serviceID).Scan(&nodeID); err != nil {
		WriteError(w, http.StatusNotFound, "not_found", "service not found")
		return
	}
	if !authorize(w, r, rbac.PermServiceWrite, rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	var req serviceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if err := validateService(req); err != nil {
		WriteError(w, http.StatusUnprocessableEntity, "validation", err.Error())
		return
	}
	enabled := 1
	if req.Enabled != nil && !*req.Enabled {
		enabled = 0
	}

	result, err := nodes.CommitNodeChange(ctx, d.Store, nodeID,
		d.actorAudit(r), RequestID(ctx), "update service",
		func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx,
				`UPDATE services SET adapter_kind = ?, params = ?, enabled = ? WHERE id = ?`,
				req.AdapterKind, string(req.Params), enabled, serviceID)
			return err
		})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not update service")
		return
	}
	if result.Changed {
		d.Hub.Notify(nodeID, result.Revision)
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"revision": result.Revision, "changed": result.Changed,
	})
}

func (d Deps) handleDeleteService(w http.ResponseWriter, r *http.Request) {
	serviceID, err := pathInt64(r, "serviceID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid service id")
		return
	}
	ctx := r.Context()

	var nodeID int64
	if err := d.Store.Read().QueryRowContext(ctx,
		`SELECT node_id FROM services WHERE id = ?`, serviceID).Scan(&nodeID); err != nil {
		WriteError(w, http.StatusNotFound, "not_found", "service not found")
		return
	}
	if !authorize(w, r, rbac.PermServiceWrite, rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	result, err := nodes.CommitNodeChange(ctx, d.Store, nodeID,
		d.actorAudit(r), RequestID(ctx), "delete service",
		func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `DELETE FROM services WHERE id = ?`, serviceID)
			return err
		})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not delete service")
		return
	}
	if result.Changed {
		d.Hub.Notify(nodeID, result.Revision)
	}
	w.WriteHeader(http.StatusNoContent)
}

var _ = audit.SystemActor
```

- [ ] **Step 6: Run and watch them pass**

Run: `go test ./internal/panel/httpapi/... -race -count=1 -v`
Expected: PASS — eight node tests plus the seven auth tests.

- [ ] **Step 7: Commit**

```bash
git add internal/panel/httpapi internal/panel/nodes/schema.go go.mod go.sum
git commit -m "feat(httpapi): node and service endpoints with adapter schema validation"
```

---

### Task 25: Audit, sessions, and the offline sweeper

**Files:**
- Create: `internal/panel/httpapi/audit.go`, `internal/panel/httpapi/sessions.go`
- Create: `internal/panel/nodes/sweeper.go`
- Test: `internal/panel/httpapi/audit_test.go`, `internal/panel/nodes/sweeper_test.go`

**Interfaces:**
- Produces:
  - `d.handleListAudit`, `d.handleListSessions`, `d.handleRevokeSession`
  - `nodes.NewSweeper(s *store.Store, now func() time.Time) *Sweeper`
  - `(*Sweeper).Sweep(ctx) (marked int, err error)` — flips stale nodes to `offline`
  - `nodes.OfflineAfter = 90 * time.Second`

- [ ] **Step 1: Write the failing tests**

`internal/panel/nodes/sweeper_test.go`:

```go
package nodes

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func setSeen(t *testing.T, f *storeFixture, status string, seen time.Time) {
	t.Helper()
	err := f.store.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE nodes SET status = ?, last_seen_at = ? WHERE id = ?`,
			status, seen.Unix(), f.nodeID)
		return err
	})
	if err != nil {
		t.Fatalf("setSeen: %v", err)
	}
}

func statusOf(t *testing.T, f *storeFixture) string {
	t.Helper()
	var s string
	if err := f.store.Read().QueryRow(
		`SELECT status FROM nodes WHERE id = ?`, f.nodeID).Scan(&s); err != nil {
		t.Fatalf("read status: %v", err)
	}
	return s
}

func TestStaleOnlineNodeBecomesOffline(t *testing.T) {
	f := newStoreFixture(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	setSeen(t, f, "online", now.Add(-OfflineAfter-time.Second))

	sw := NewSweeper(f.store, func() time.Time { return now })
	marked, err := sw.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if marked != 1 {
		t.Errorf("marked = %d, want 1", marked)
	}
	if got := statusOf(t, f); got != "offline" {
		t.Errorf("status = %q, want offline", got)
	}
}

func TestFreshNodeIsLeftAlone(t *testing.T) {
	f := newStoreFixture(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	setSeen(t, f, "online", now.Add(-10*time.Second))

	sw := NewSweeper(f.store, func() time.Time { return now })
	if marked, _ := sw.Sweep(context.Background()); marked != 0 {
		t.Errorf("marked = %d, want 0", marked)
	}
	if got := statusOf(t, f); got != "online" {
		t.Errorf("status = %q, want online", got)
	}
}

// A node the admin disabled, or one that never enrolled, must not be
// relabelled by a timer.
func TestDisabledAndPendingNodesAreNotSwept(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	for _, status := range []string{"disabled", "pending"} {
		f := newStoreFixture(t)
		setSeen(t, f, status, now.Add(-24*time.Hour))
		sw := NewSweeper(f.store, func() time.Time { return now })
		if _, err := sw.Sweep(context.Background()); err != nil {
			t.Fatalf("Sweep: %v", err)
		}
		if got := statusOf(t, f); got != status {
			t.Errorf("%s node became %q", status, got)
		}
	}
}

// Integrity is a fault that needs an operator, so a heartbeat gap must not
// downgrade it to the ordinary offline state and hide it.
func TestIntegrityStateSurvivesTheSweep(t *testing.T) {
	f := newStoreFixture(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	setSeen(t, f, "integrity", now.Add(-24*time.Hour))

	sw := NewSweeper(f.store, func() time.Time { return now })
	if _, err := sw.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if got := statusOf(t, f); got != "integrity" {
		t.Errorf("status = %q, want integrity preserved", got)
	}
}

func TestTransitionIsAudited(t *testing.T) {
	f := newStoreFixture(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	setSeen(t, f, "online", now.Add(-OfflineAfter-time.Second))

	sw := NewSweeper(f.store, func() time.Time { return now })
	if _, err := sw.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	var action, actorType, label string
	if err := f.store.Read().QueryRow(
		`SELECT action, actor_type, actor_label FROM audit_log ORDER BY id DESC LIMIT 1`,
	).Scan(&action, &actorType, &label); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if action != "node.offline" || actorType != "system" || label != "sweeper" {
		t.Errorf("audit = %s/%s/%s, want node.offline/system/sweeper", action, actorType, label)
	}
}
```

`internal/panel/httpapi/audit_test.go`:

```go
package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestAuditRequiresPermission(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "reseller", "pw", "reseller") // reseller lacks audit:read
	token := env.login(t, "reseller", "pw")

	if res := env.get(t, "/api/v1/audit", token); res.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", res.Code)
	}
}

func TestAuditListsNewestFirst(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	token := env.login(t, "root", "pw")
	env.post(t, "/api/v1/nodes", `{"name":"a","address":"1.1.1.1"}`, token)
	env.post(t, "/api/v1/nodes", `{"name":"b","address":"2.2.2.2"}`, token)

	res := env.get(t, "/api/v1/audit", token)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d", res.Code)
	}
	var body struct {
		Entries []struct {
			ID     int64  `json:"id"`
			Action string `json:"action"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Entries) < 2 {
		t.Fatalf("got %d entries, want at least 2", len(body.Entries))
	}
	if body.Entries[0].ID < body.Entries[1].ID {
		t.Error("entries are not newest-first")
	}
}

func TestSessionListShowsOwnSessionsAndRevokeWorks(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	first := env.login(t, "root", "pw")
	second := env.login(t, "root", "pw")

	res := env.get(t, "/api/v1/sessions", first)
	var body struct {
		Sessions []struct {
			ID      int64 `json:"id"`
			Current bool  `json:"current"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(body.Sessions))
	}

	var other int64
	for _, s := range body.Sessions {
		if !s.Current {
			other = s.ID
		}
	}
	if other == 0 {
		t.Fatal("no non-current session found; the current flag is wrong")
	}
	if res := env.do(t, http.MethodDelete,
		"/api/v1/sessions/"+itoa64(other), "", first); res.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d", res.Code)
	}
	if res := env.get(t, "/api/v1/nodes", second); res.Code != http.StatusUnauthorized {
		t.Errorf("revoked session still works: %d", res.Code)
	}
}

func TestCannotRevokeAnotherAdminsSession(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	env.seedAdmin(t, "other", "pw", "admin")
	otherToken := env.login(t, "other", "pw")
	rootToken := env.login(t, "root", "pw")

	res := env.get(t, "/api/v1/sessions", otherToken)
	var body struct {
		Sessions []struct {
			ID int64 `json:"id"`
		} `json:"sessions"`
	}
	_ = json.NewDecoder(res.Body).Decode(&body)
	victim := body.Sessions[0].ID

	// A super admin may; an ordinary admin may not. Verify the ordinary case
	// by having 'other' try to revoke a root session.
	res = env.get(t, "/api/v1/sessions", rootToken)
	var rootBody struct {
		Sessions []struct {
			ID int64 `json:"id"`
		} `json:"sessions"`
	}
	_ = json.NewDecoder(res.Body).Decode(&rootBody)

	if got := env.do(t, http.MethodDelete,
		"/api/v1/sessions/"+itoa64(rootBody.Sessions[0].ID), "", otherToken); got.Code != http.StatusNotFound {
		t.Errorf("cross-admin revoke status = %d, want 404", got.Code)
	}
	_ = victim
}
```

Add `func itoa64(i int64) string { return strconv.FormatInt(i, 10) }` to `env_test.go` and import `strconv`.

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/panel/... -run 'Sweep|Audit|Session'`
Expected: FAIL — `undefined: NewSweeper`.

- [ ] **Step 3: Implement the sweeper**

`internal/panel/nodes/sweeper.go`:

```go
package nodes

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/store"
)

// OfflineAfter is three missed 30-second heartbeats.
const OfflineAfter = 90 * time.Second

// Sweeper flips nodes to offline when heartbeats stop.
//
// Only 'online' and 'degraded' are swept. 'disabled' is an administrative
// decision, 'pending' and 'enrolling' have never reported, and 'integrity' is
// a fault an operator must see — relabelling any of them would erase
// information rather than add it.
type Sweeper struct {
	store *store.Store
	now   func() time.Time
}

func NewSweeper(s *store.Store, now func() time.Time) *Sweeper {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Sweeper{store: s, now: now}
}

func (s *Sweeper) Sweep(ctx context.Context) (int, error) {
	cutoff := s.now().Add(-OfflineAfter).Unix()
	var marked int

	err := s.store.Write(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT id FROM nodes
			  WHERE status IN ('online','degraded')
			    AND (last_seen_at IS NULL OR last_seen_at < ?)`, cutoff)
		if err != nil {
			return fmt.Errorf("find stale nodes: %w", err)
		}
		var stale []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan stale node: %w", err)
			}
			stale = append(stale, id)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close()

		for _, id := range stale {
			if _, err := tx.ExecContext(ctx,
				`UPDATE nodes SET status = 'offline' WHERE id = ?`, id); err != nil {
				return fmt.Errorf("mark node %d offline: %w", id, err)
			}
			if err := audit.InTx(ctx, tx, "", audit.SystemActor("sweeper"), audit.Record{
				Action:     "node.offline",
				TargetType: "node",
				TargetID:   sql.NullInt64{Int64: id, Valid: true},
				After:      map[string]any{"reason": "no heartbeat within 3 intervals"},
				Result:     "ok",
			}); err != nil {
				return err
			}
			marked++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return marked, nil
}

// Run sweeps on a ticker until ctx is cancelled.
func (s *Sweeper) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = s.Sweep(ctx)
		}
	}
}
```

- [ ] **Step 4: Implement the audit handler**

`internal/panel/httpapi/audit.go`:

```go
package httpapi

import (
	"net/http"
	"strconv"

	"github.com/amyrm/antimage/internal/panel/rbac"
)

func (d Deps) handleListAudit(w http.ResponseWriter, r *http.Request) {
	if !authorize(w, r, rbac.PermAuditRead, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	rows, err := d.Store.Read().QueryContext(r.Context(),
		`SELECT a.id, a.at, a.actor_type, COALESCE(ad.username,''), a.actor_label,
		        a.actor_ip, a.request_id, a.action, a.target_type,
		        COALESCE(a.target_id,0), a.result
		   FROM audit_log a
		   LEFT JOIN admins ad ON ad.id = a.actor_admin_id
		  ORDER BY a.id DESC LIMIT ?`, limit)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not list audit entries")
		return
	}
	defer func() { _ = rows.Close() }()

	type entryDTO struct {
		ID         int64  `json:"id"`
		At         int64  `json:"at"`
		ActorType  string `json:"actor_type"`
		ActorName  string `json:"actor_name"`
		ActorLabel string `json:"actor_label"`
		ActorIP    string `json:"actor_ip"`
		RequestID  string `json:"request_id"`
		Action     string `json:"action"`
		TargetType string `json:"target_type"`
		TargetID   int64  `json:"target_id"`
		Result     string `json:"result"`
	}
	entries := []entryDTO{}
	for rows.Next() {
		var e entryDTO
		if err := rows.Scan(&e.ID, &e.At, &e.ActorType, &e.ActorName, &e.ActorLabel,
			&e.ActorIP, &e.RequestID, &e.Action, &e.TargetType, &e.TargetID, &e.Result); err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "could not read audit entries")
			return
		}
		entries = append(entries, e)
	}
	WriteJSON(w, http.StatusOK, map[string]any{"entries": entries})
}
```

- [ ] **Step 5: Implement the session handlers**

`internal/panel/httpapi/sessions.go`:

```go
package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
)

func (d Deps) handleListSessions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actor := ActorFrom(ctx)
	current := sessionIDFrom(ctx)

	rows, err := d.Store.Read().QueryContext(ctx,
		`SELECT id, ip, user_agent, created_at, last_used_at, expires_at
		   FROM sessions
		  WHERE admin_id = ? AND revoked_at IS NULL
		  ORDER BY last_used_at DESC`, actor.AdminID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not list sessions")
		return
	}
	defer func() { _ = rows.Close() }()

	type sessionDTO struct {
		ID         int64  `json:"id"`
		IP         string `json:"ip"`
		UserAgent  string `json:"user_agent"`
		CreatedAt  int64  `json:"created_at"`
		LastUsedAt int64  `json:"last_used_at"`
		ExpiresAt  int64  `json:"expires_at"`
		Current    bool   `json:"current"`
	}
	out := []sessionDTO{}
	for rows.Next() {
		var s sessionDTO
		if err := rows.Scan(&s.ID, &s.IP, &s.UserAgent,
			&s.CreatedAt, &s.LastUsedAt, &s.ExpiresAt); err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "could not read sessions")
			return
		}
		s.Current = s.ID == current
		out = append(out, s)
	}
	WriteJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

func (d Deps) handleRevokeSession(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "sessionID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid session id")
		return
	}
	ctx := r.Context()
	actor := ActorFrom(ctx)

	// Ownership check: an admin revokes only their own sessions; a super
	// admin may revoke any. 404 rather than 403 so session ids are not
	// probeable.
	var owner int64
	err = d.Store.Read().QueryRowContext(ctx,
		`SELECT admin_id FROM sessions WHERE id = ?`, id).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "not_found", "session not found")
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not load session")
		return
	}
	if owner != actor.AdminID && !actor.IsSuper {
		WriteError(w, http.StatusNotFound, "not_found", "session not found")
		return
	}

	if err := d.Sessions.Revoke(ctx, id); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not revoke session")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 6: Run and watch them pass**

Run: `go test ./internal/panel/... -race -count=1`
Expected: PASS — five sweeper tests and four audit/session tests, plus everything earlier.

- [ ] **Step 7: Commit**

```bash
git add internal/panel
git commit -m "feat(panel): audit and session endpoints, offline sweeper"
```

---

### Task 26: SSE live status

**Files:**
- Create: `internal/panel/httpapi/sse.go`
- Test: `internal/panel/httpapi/sse_test.go`

**Interfaces:**
- Produces: `d.handleEvents` serving `text/event-stream` at `GET /api/v1/events`.

- [ ] **Step 1: Write the failing test**

`internal/panel/httpapi/sse_test.go`:

```go
package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/auth"
)

func TestEventsStreamsSnapshotsAndClosesOnCancel(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	token := env.login(t, "root", "pw")
	env.post(t, "/api/v1/nodes", `{"name":"de-1","address":"1.2.3.4"}`, token)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil).WithContext(ctx)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		env.handler.ServeHTTP(rec, req)
		close(done)
	}()

	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("SSE handler did not return after the client disconnected")
	}

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: nodes") {
		t.Errorf("no nodes event in stream:\n%s", body)
	}
	if !strings.Contains(body, "de-1") {
		t.Errorf("node payload missing from stream:\n%s", body)
	}
}

func TestEventsRequiresAuthentication(t *testing.T) {
	env := newTestEnv(t)
	res := env.get(t, "/api/v1/events", "")
	if res.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", res.Code)
	}
}
```

- [ ] **Step 2: Run and watch it fail**

Run: `go test ./internal/panel/httpapi/... -run Events`
Expected: FAIL — handler not implemented.

- [ ] **Step 3: Implement**

`internal/panel/httpapi/sse.go`:

```go
package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/amyrm/antimage/internal/panel/rbac"
)

// sseInterval is how often the panel pushes a node-status snapshot. Status is
// low-cardinality and changes slowly, so a small periodic snapshot is simpler
// and more robust than a per-event fan-out, and it self-heals after a dropped
// connection.
const sseInterval = 3 * time.Second

func (d Deps) handleEvents(w http.ResponseWriter, r *http.Request) {
	if !authorize(w, r, rbac.PermNodeRead, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteError(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // defeat proxy buffering
	w.WriteHeader(http.StatusOK)

	ctx := r.Context()
	scope := rbac.ScopeOf(ActorFrom(ctx))

	send := func() bool {
		rows, err := d.Store.ListNodes(ctx, scope)
		if err != nil {
			return false
		}
		type statusDTO struct {
			ID              int64 `json:"id"`
			Status          string `json:"status"`
			Online          bool  `json:"online"`
			DesiredRevision int64 `json:"desired_revision"`
			AppliedRevision int64 `json:"applied_revision"`
			Drift           bool  `json:"drift"`
		}
		payload := make([]statusDTO, 0, len(rows))
		for _, n := range rows {
			payload = append(payload, statusDTO{
				ID: n.ID, Status: n.Status, Online: d.Hub.Online(n.ID),
				DesiredRevision: n.DesiredRevision, AppliedRevision: n.AppliedRevision,
				Drift: n.AppliedRevision != n.DesiredRevision,
			})
		}
		body, err := json.Marshal(map[string]any{"nodes": payload})
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: nodes\ndata: %s\n\n", body); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// Send immediately so the UI paints without waiting a full interval.
	if !send() {
		return
	}

	ticker := time.NewTicker(sseInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !send() {
				return
			}
		}
	}
}
```

- [ ] **Step 4: Run and watch it pass**

Run: `go test ./internal/panel/httpapi/... -race -count=1 -v`
Expected: PASS — both SSE tests plus all earlier httpapi tests.

- [ ] **Step 5: Commit**

```bash
git add internal/panel/httpapi
git commit -m "feat(httpapi): SSE node-status stream"
```

---

# Phase G — Distribution

### Task 27: install.sh, systemd units, and the panel binary

**Files:**
- Create: `scripts/install.sh`, `packaging/antimage-panel.service`, `packaging/antimage-node.service`
- Create: `cmd/antimage-panel/main.go`
- Test: `scripts/install_test.sh`, `internal/panel/httpapi/install_test.go`

**Interfaces:**
- Produces: `antimage-panel` serving HTTP + gRPC, and `GET /install.sh` returning the bootstrap script.

- [ ] **Step 1: Write install.sh**

`scripts/install.sh`:

```bash
#!/usr/bin/env bash
# antimage node bootstrap. Idempotent: re-running upgrades in place.
set -euo pipefail

PANEL_URL=""
TOKEN=""
CA_FINGERPRINT=""
VERSION="latest"
STATE_DIR="/var/lib/antimage"
CONFIG_DIR="/etc/antimage"

die() { echo "error: $*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --panel)          PANEL_URL="$2"; shift 2 ;;
    --token)          TOKEN="$2"; shift 2 ;;
    --ca-fingerprint) CA_FINGERPRINT="$2"; shift 2 ;;
    --version)        VERSION="$2"; shift 2 ;;
    *) die "unknown argument: $1" ;;
  esac
done

[ "$(id -u)" -eq 0 ] || die "must run as root"
[ -n "$PANEL_URL" ] || die "--panel is required"
[ -n "$TOKEN" ] || die "--token is required"

# Refuse unsupported platforms rather than guessing at package names.
[ -r /etc/os-release ] || die "cannot read /etc/os-release"
. /etc/os-release
case "${ID}:${VERSION_ID%%.*}" in
  debian:11|debian:12|debian:13) ;;
  ubuntu:20|ubuntu:22|ubuntu:24) ;;
  *) die "unsupported OS ${ID} ${VERSION_ID}; antimage supports Debian 11+ and Ubuntu 20.04+" ;;
esac

case "$(uname -m)" in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
  *) die "unsupported architecture $(uname -m); antimage supports amd64 and arm64" ;;
esac

command -v systemctl >/dev/null 2>&1 || die "systemd is required"
command -v curl >/dev/null 2>&1 || die "curl is required"

# Fetch the CA fingerprint from the panel if it was not supplied. This is
# trust-on-first-use; --ca-fingerprint from an out-of-band channel is stronger.
if [ -z "$CA_FINGERPRINT" ]; then
  CA_FINGERPRINT="$(curl -fsSL "${PANEL_URL}/api/v1/ca-fingerprint")" \
    || die "could not fetch the CA fingerprint from ${PANEL_URL}"
fi
[ -n "$CA_FINGERPRINT" ] || die "empty CA fingerprint"

BIN_URL="${PANEL_URL}/download/antimage-node-linux-${ARCH}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "downloading antimage-node (${ARCH}) from ${PANEL_URL}"
curl -fsSL -o "$TMP/antimage-node" "$BIN_URL" || die "download failed"
curl -fsSL -o "$TMP/antimage-node.sha256" "${BIN_URL}.sha256" || die "checksum download failed"

( cd "$TMP" && echo "$(cat antimage-node.sha256)  antimage-node" | sha256sum -c - ) \
  || die "checksum mismatch; refusing to install"

install -d -m 0700 "$STATE_DIR" "$CONFIG_DIR"
install -m 0755 "$TMP/antimage-node" /usr/local/bin/antimage-node

# Only write node.yaml on first install: rewriting it would clobber the node
# id and force a re-enrollment on every upgrade.
if [ ! -f "$CONFIG_DIR/node.yaml" ]; then
  cat > "$CONFIG_DIR/node.yaml" <<EOF
panel_url: ${PANEL_URL}
token: ${TOKEN}
ca_fingerprint: ${CA_FINGERPRINT}
state_dir: ${STATE_DIR}
EOF
  chmod 0600 "$CONFIG_DIR/node.yaml"
fi

cat > /etc/systemd/system/antimage-node.service <<'EOF'
[Unit]
Description=antimage node agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/antimage-node --config /etc/antimage/node.yaml
Restart=always
RestartSec=5s
User=root
NoNewPrivileges=true
ProtectSystem=full
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now antimage-node
echo "antimage-node installed and started. Check: systemctl status antimage-node"
```

`packaging/antimage-panel.service`:

```ini
[Unit]
Description=antimage panel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/antimage-panel --config /etc/antimage/panel.yaml
Restart=always
RestartSec=5s
User=antimage
Group=antimage
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=/var/lib/antimage
AmbientCapabilities=CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
```

- [ ] **Step 2: Write the script test**

`scripts/install_test.sh`:

```bash
#!/usr/bin/env bash
# Argument and platform-guard tests for install.sh. No network, no root.
set -uo pipefail

SCRIPT="$(dirname "$0")/install.sh"
fails=0

expect_fail() {
  local desc="$1"; shift
  if output="$("$@" 2>&1)"; then
    echo "FAIL: $desc — expected non-zero exit"
    fails=$((fails + 1))
  else
    echo "ok: $desc"
  fi
}

expect_fail "rejects unknown arguments"       bash "$SCRIPT" --bogus x
expect_fail "requires --panel"                bash "$SCRIPT" --token t
expect_fail "requires --token"                bash "$SCRIPT" --panel https://p
expect_fail "refuses to run as non-root"      bash "$SCRIPT" --panel https://p --token t

if grep -q 'set -euo pipefail' "$SCRIPT"; then
  echo "ok: strict mode enabled"
else
  echo "FAIL: install.sh must use 'set -euo pipefail'"
  fails=$((fails + 1))
fi

if grep -q 'sha256sum -c' "$SCRIPT"; then
  echo "ok: verifies the binary checksum"
else
  echo "FAIL: install.sh must verify the downloaded binary"
  fails=$((fails + 1))
fi

exit "$fails"
```

Make both executable: `chmod +x scripts/install.sh scripts/install_test.sh`

- [ ] **Step 3: Run the script test**

Run: `bash scripts/install_test.sh`
Expected: all `ok:` lines, exit 0. (Runs as a non-root user, so the root guard fires.)

- [ ] **Step 4: Write the panel main**

`cmd/antimage-panel/main.go`:

```go
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"github.com/amyrm/antimage/internal/panel/auth"
	"github.com/amyrm/antimage/internal/panel/control"
	"github.com/amyrm/antimage/internal/panel/httpapi"
	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/store"
	pb "github.com/amyrm/antimage/internal/shared/proto/antimage/v1"
	"github.com/amyrm/antimage/internal/shared/secrets"
	"github.com/amyrm/antimage/internal/shared/version"
)

func main() {
	dataDir := flag.String("data-dir", "/var/lib/antimage", "data directory")
	httpAddr := flag.String("http", ":8080", "HTTP listen address")
	grpcAddr := flag.String("grpc", ":8443", "gRPC control listen address")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		_, _ = os.Stdout.WriteString(version.Version + "\n")
		return
	}

	if err := run(*dataDir, *httpAddr, *grpcAddr); err != nil {
		slog.Error("panel stopped", "error", err)
		os.Exit(1)
	}
}

func run(dataDir, httpAddr, grpcAddr string) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	key, err := secrets.LoadOrCreateKey(filepath.Join(dataDir, "master.key"))
	if err != nil {
		return err
	}
	box, err := secrets.NewBox(key)
	if err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(dataDir, "antimage.db"))
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ca, err := nodes.LoadOrCreateCA(ctx, st, box)
	if err != nil {
		return err
	}
	hub := control.NewHub()
	now := func() time.Time { return time.Now().UTC() }

	go nodes.NewSweeper(st, now).Run(ctx, 30*time.Second)

	deps := control.Deps{Store: st, CA: ca, Hub: hub, Now: now}
	grpcServer := grpc.NewServer()
	pb.RegisterEnrollmentServer(grpcServer, control.NewEnrollmentService(deps))
	pb.RegisterControlServer(grpcServer, control.NewControlService(deps))

	grpcListener, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr: httpAddr,
		Handler: httpapi.NewRouter(httpapi.Deps{
			Store:    st,
			Sessions: auth.NewSessions(st, now),
			Limiter:  auth.NewLimiter(st, now),
			Hub:      hub,
			CA:       ca,
			Now:      now,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 2)
	go func() { errCh <- grpcServer.Serve(grpcListener) }()
	go func() {
		if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	slog.Info("antimage-panel listening",
		"version", version.Version, "http", httpAddr, "grpc", grpcAddr,
		"ca_fingerprint", ca.FingerprintSHA256())

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil {
			return err
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
	grpcServer.GracefulStop()
	return nil
}
```

- [ ] **Step 5: Serve install.sh and the CA fingerprint**

Add to `internal/panel/httpapi/router.go`, inside `NewRouter` before the UI handler:

```go
	r.Get("/api/v1/ca-fingerprint", d.handleCAFingerprint)
	r.Get("/install.sh", d.handleInstallScript)
```

Create `internal/panel/httpapi/install.go`:

```go
package httpapi

import (
	_ "embed"
	"net/http"
)

//go:embed install.sh
var installScript []byte

// handleInstallScript serves the bootstrap script unauthenticated: it
// contains no secrets, and the enrollment token is supplied as an argument by
// whoever runs it.
func (d Deps) handleInstallScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(installScript)
}

// handleCAFingerprint lets install.sh pin the panel CA on first contact.
func (d Deps) handleCAFingerprint(w http.ResponseWriter, r *http.Request) {
	if d.CA == nil {
		WriteError(w, http.StatusServiceUnavailable, "unavailable", "CA not initialised")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(d.CA.FingerprintSHA256()))
}
```

Copy the script so `go:embed` can reach it: `cp scripts/install.sh internal/panel/httpapi/install.sh`, and add a Makefile rule so the two never diverge:

```makefile
.PHONY: sync-install
sync-install:
	cp scripts/install.sh internal/panel/httpapi/install.sh

build: sync-install
```

- [ ] **Step 6: Write the endpoint test**

`internal/panel/httpapi/install_test.go`:

```go
package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestInstallScriptIsServedUnauthenticated(t *testing.T) {
	env := newTestEnv(t)
	res := env.get(t, "/install.sh", "")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	body := res.Body.String()
	if !strings.Contains(body, "set -euo pipefail") {
		t.Error("served script is not the hardened bootstrap script")
	}
	if !strings.Contains(body, "sha256sum -c") {
		t.Error("served script does not verify the binary checksum")
	}
}

func TestInstallScriptContainsNoSecrets(t *testing.T) {
	env := newTestEnv(t)
	body := env.get(t, "/install.sh", "").Body.String()
	for _, forbidden := range []string{"master.key", "password", "BEGIN EC PRIVATE KEY"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("install.sh leaks %q", forbidden)
		}
	}
}
```

- [ ] **Step 7: Run everything**

Run: `make sync-install && go test ./... -race -count=1 && bash scripts/install_test.sh && make build`
Expected: all Go tests PASS, script tests PASS, three binaries built.

- [ ] **Step 8: Commit**

```bash
git add scripts packaging cmd/antimage-panel internal/panel/httpapi Makefile
git commit -m "feat(dist): install.sh bootstrap, systemd units, and panel entrypoint"
```

---

### Task 28: SSH bootstrap with host-key pinning and non-persisted credentials

**Files:**
- Create: `internal/panel/nodes/bootstrap_ssh.go`
- Create: `internal/panel/httpapi/bootstrap.go`
- Test: `internal/panel/nodes/bootstrap_ssh_test.go`

**Interfaces:**
- Produces:
  - `type SSHCredentials struct { Host string; Port int; User string; PrivateKeyPEM []byte; Passphrase []byte }` with `(*SSHCredentials).Zero()`
  - `nodes.HostKeyPrompt(ctx, creds SSHCredentials) (fingerprint string, err error)`
  - `nodes.BootstrapOverSSH(ctx, creds SSHCredentials, pinnedHostKey, command string) (output string, err error)`
  - `nodes.ErrHostKeyMismatch`

**The invariant this task protects:** no field of `SSHCredentials` is ever persisted. There is no table, no column, and no serialization for it. `Zero()` wipes the key material after use.

- [ ] **Step 1: Write the failing tests**

`internal/panel/nodes/bootstrap_ssh_test.go`:

```go
package nodes

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func TestZeroWipesKeyMaterial(t *testing.T) {
	key := []byte("-----BEGIN OPENSSH PRIVATE KEY-----")
	pass := []byte("hunter2")
	creds := SSHCredentials{
		Host: "1.2.3.4", Port: 22, User: "root",
		PrivateKeyPEM: key, Passphrase: pass,
	}
	creds.Zero()

	if !bytes.Equal(key, make([]byte, len(key))) {
		t.Error("private key bytes survived Zero()")
	}
	if !bytes.Equal(pass, make([]byte, len(pass))) {
		t.Error("passphrase bytes survived Zero()")
	}
	if creds.User != "" || creds.Host != "" {
		t.Error("Zero() left identifying fields populated")
	}
}

// The credentials type must not be serializable, so it cannot accidentally
// end up in a database column, an audit payload, or a log line.
func TestCredentialsCannotBeMarshalled(t *testing.T) {
	src, err := os.ReadFile("bootstrap_ssh.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := string(src)
	for _, banned := range []string{"json:\"", "db:\"", "yaml:\""} {
		if strings.Contains(body, banned) {
			t.Errorf("bootstrap_ssh.go contains a %s struct tag; credentials must never serialize", banned)
		}
	}
}

// A grep-level guard: no migration may add a column that stores SSH secrets.
func TestNoMigrationStoresSSHCredentials(t *testing.T) {
	entries, err := os.ReadDir("../store/migrations")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		body, err := os.ReadFile("../store/migrations/" + e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		lower := strings.ToLower(string(body))
		for _, banned := range []string{"ssh_key", "ssh_password", "private_key", "passphrase"} {
			if strings.Contains(lower, banned) {
				t.Errorf("%s declares %q; SSH credentials must never be persisted", e.Name(), banned)
			}
		}
	}
}

// The panel must never accept an unverified host key.
func TestSourceNeverIgnoresHostKeys(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "bootstrap_ssh.go", nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if ok && sel.Sel.Name == "InsecureIgnoreHostKey" {
			t.Error("InsecureIgnoreHostKey is referenced")
		}
		return true
	})
}

func TestBootstrapRejectsMismatchedHostKey(t *testing.T) {
	// verifyHostKey is the pure part of the flow, tested without a server.
	err := verifyHostKey("SHA256:expected", "SHA256:actual")
	if err == nil {
		t.Fatal("mismatched host key accepted")
	}
	if !strings.Contains(err.Error(), "SHA256:expected") {
		t.Errorf("error should name the expected fingerprint: %v", err)
	}
	if err := verifyHostKey("SHA256:same", "SHA256:same"); err != nil {
		t.Errorf("matching host key rejected: %v", err)
	}
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/panel/nodes/... -run 'SSH|HostKey|Zero|Credentials'`
Expected: FAIL — `undefined: SSHCredentials`.

- [ ] **Step 3: Implement**

```bash
go get golang.org/x/crypto/ssh@latest
```

`internal/panel/nodes/bootstrap_ssh.go`:

```go
package nodes

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrHostKeyMismatch means the server presented a key other than the pinned one.
var ErrHostKeyMismatch = errors.New("ssh host key mismatch")

// SSHCredentials carries bootstrap credentials for exactly one run.
//
// antimage never persists these (spec section 7.1). Storing them would make
// the panel database a root-key vault for the entire fleet, so one panel
// compromise would become total fleet compromise. The type therefore has no
// serialization tags, no store methods, and no migration backing it; Zero()
// wipes the key material as soon as the run finishes.
type SSHCredentials struct {
	Host          string
	Port          int
	User          string
	PrivateKeyPEM []byte
	Passphrase    []byte
}

// Zero overwrites secret material in place and clears identifying fields.
func (c *SSHCredentials) Zero() {
	for i := range c.PrivateKeyPEM {
		c.PrivateKeyPEM[i] = 0
	}
	for i := range c.Passphrase {
		c.Passphrase[i] = 0
	}
	c.PrivateKeyPEM = nil
	c.Passphrase = nil
	c.Host = ""
	c.User = ""
	c.Port = 0
}

func (c SSHCredentials) address() string {
	port := c.Port
	if port == 0 {
		port = 22
	}
	return fmt.Sprintf("%s:%d", c.Host, port)
}

func (c SSHCredentials) signer() (ssh.Signer, error) {
	if len(c.Passphrase) > 0 {
		return ssh.ParsePrivateKeyWithPassphrase(c.PrivateKeyPEM, c.Passphrase)
	}
	return ssh.ParsePrivateKey(c.PrivateKeyPEM)
}

// verifyHostKey compares a presented fingerprint against the pinned one.
func verifyHostKey(pinned, presented string) error {
	if pinned == presented {
		return nil
	}
	return fmt.Errorf("%w: pinned %s, server presented %s", ErrHostKeyMismatch, pinned, presented)
}

// HostKeyPrompt connects once purely to read the host key, so the admin can
// confirm its fingerprint before anything is executed. This is
// trust-on-first-use WITH human confirmation, not blind acceptance.
func HostKeyPrompt(ctx context.Context, creds SSHCredentials) (string, error) {
	var fingerprint string

	signer, err := creds.signer()
	if err != nil {
		return "", fmt.Errorf("parse ssh key: %w", err)
	}
	cfg := &ssh.ClientConfig{
		User: creds.User,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fingerprint = ssh.FingerprintSHA256(key)
			// Capture and stop: nothing is executed on an unconfirmed host.
			return errHostKeyCaptured
		},
		Timeout: 15 * time.Second,
	}

	client, err := ssh.Dial("tcp", creds.address(), cfg)
	if err == nil {
		_ = client.Close()
		return fingerprint, nil
	}
	if fingerprint != "" && strings.Contains(err.Error(), errHostKeyCaptured.Error()) {
		return fingerprint, nil
	}
	return "", fmt.Errorf("read host key: %w", err)
}

var errHostKeyCaptured = errors.New("host key captured")

// BootstrapOverSSH runs the bootstrap command against a host whose key
// fingerprint matches pinnedHostKey.
//
// The caller must Zero() the credentials afterwards; this function keeps no
// reference to them once it returns.
func BootstrapOverSSH(ctx context.Context, creds SSHCredentials, pinnedHostKey, command string) (string, error) {
	if pinnedHostKey == "" {
		return "", errors.New("refusing to connect without a pinned host key")
	}
	signer, err := creds.signer()
	if err != nil {
		return "", fmt.Errorf("parse ssh key: %w", err)
	}

	cfg := &ssh.ClientConfig{
		User: creds.User,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			return verifyHostKey(pinnedHostKey, ssh.FingerprintSHA256(key))
		},
		Timeout: 30 * time.Second,
	}

	client, err := ssh.Dial("tcp", creds.address(), cfg)
	if err != nil {
		return "", fmt.Errorf("ssh dial: %w", err)
	}
	defer func() { _ = client.Close() }()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("ssh session: %w", err)
	}
	defer func() { _ = session.Close() }()

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = session.Signal(ssh.SIGKILL)
		case <-done:
		}
	}()

	output, err := session.CombinedOutput(command)
	close(done)
	if err != nil {
		return string(output), fmt.Errorf("bootstrap command failed: %w", err)
	}
	return string(output), nil
}
```

Add `"net"` to the imports.

- [ ] **Step 4: Wire the HTTP endpoints**

`internal/panel/httpapi/bootstrap.go`:

```go
package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

type sshBootstrapRequest struct {
	Host          string `json:"host"`
	Port          int    `json:"port"`
	User          string `json:"user"`
	PrivateKeyPEM string `json:"private_key_pem"`
	Passphrase    string `json:"passphrase"`
	// HostKeyFingerprint is empty on the first call, which returns the
	// server's fingerprint for the admin to confirm; the second call supplies
	// the confirmed value.
	HostKeyFingerprint string `json:"host_key_fingerprint"`
}

// handleSSHBootstrap runs the two-phase flow: fetch-and-confirm the host key,
// then execute. Credentials live only for this request.
func (d Deps) handleSSHBootstrap(w http.ResponseWriter, r *http.Request) {
	nodeID, err := pathInt64(r, "nodeID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node id")
		return
	}
	if !authorize(w, r, rbac.PermNodeEnroll, rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	var req sshBootstrapRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}

	creds := nodes.SSHCredentials{
		Host: req.Host, Port: req.Port, User: req.User,
		PrivateKeyPEM: []byte(req.PrivateKeyPEM),
		Passphrase:    []byte(req.Passphrase),
	}
	defer creds.Zero() // wiped before this handler returns, always

	ctx := r.Context()

	if req.HostKeyFingerprint == "" {
		fingerprint, err := nodes.HostKeyPrompt(ctx, creds)
		if err != nil {
			WriteError(w, http.StatusBadGateway, "ssh_failed", "could not read the host key")
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"host_key_fingerprint": fingerprint,
			"confirm_required":     true,
		})
		return
	}

	token, err := nodes.IssueEnrollToken(ctx, d.Store, nodeID,
		d.actorAudit(r), RequestID(ctx), d.now())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not issue token")
		return
	}

	command := "curl -fsSL https://" + r.Host + "/install.sh | sudo bash -s -- " +
		"--panel https://" + r.Host + " --token " + token +
		" --ca-fingerprint " + d.CA.FingerprintSHA256()

	output, err := nodes.BootstrapOverSSH(ctx, creds, req.HostKeyFingerprint, command)
	if err != nil {
		audit.BestEffort(ctx, d.Store, RequestID(ctx), d.actorAudit(r), audit.Record{
			Action: "node.bootstrap", TargetType: "node", Result: "failed",
			After: map[string]any{"output": output},
		})
		// The real stderr is what the operator needs to fix it.
		WriteJSON(w, http.StatusBadGateway, map[string]any{
			"error":  map[string]string{"code": "bootstrap_failed", "message": err.Error()},
			"output": output,
		})
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{"output": output})
}
```

Register it in `NewRouter` alongside the other node routes:

```go
			private.Post("/nodes/{nodeID}/bootstrap-ssh", d.handleSSHBootstrap)
```

- [ ] **Step 5: Run and watch them pass**

Run: `go test ./internal/panel/... -race -count=1 && make check-imports`
Expected: PASS — five bootstrap tests, and `check-imports` confirms `InsecureIgnoreHostKey` appears nowhere.

- [ ] **Step 6: Commit**

```bash
git add internal/panel go.mod go.sum
git commit -m "feat(nodes): SSH bootstrap with pinned host keys and non-persisted credentials"
```

---

### Task 29: antimage-ctl

**Files:**
- Create: `cmd/antimage-ctl/main.go`, `cmd/antimage-ctl/commands.go`
- Test: `cmd/antimage-ctl/commands_test.go`

**Interfaces:**
- Produces: `antimage-ctl create-admin|reset-password|enroll-token|backup|list-admins`.

This is the recovery path: if the UI is unreachable or every admin is locked out, these commands are the way back in.

- [ ] **Step 1: Write the failing tests**

`cmd/antimage-ctl/commands_test.go`:

```go
package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amyrm/antimage/internal/panel/auth"
	"github.com/amyrm/antimage/internal/panel/store"
)

func newCtlEnv(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "antimage.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

func TestCreateAdminSeedsRolesAndHashesPassword(t *testing.T) {
	s, _ := newCtlEnv(t)
	ctx := context.Background()

	if err := createAdmin(ctx, s, "root", "s3cret", "super_admin"); err != nil {
		t.Fatalf("createAdmin: %v", err)
	}

	var hash, role string
	if err := s.Read().QueryRow(
		`SELECT a.password_hash, r.name FROM admins a JOIN roles r ON r.id = a.role_id
		  WHERE a.username = 'root'`).Scan(&hash, &role); err != nil {
		t.Fatalf("read admin: %v", err)
	}
	if role != "super_admin" {
		t.Errorf("role = %q, want super_admin", role)
	}
	if strings.Contains(hash, "s3cret") {
		t.Fatal("the password was stored in plaintext")
	}
	ok, err := auth.VerifyPassword(hash, "s3cret")
	if err != nil || !ok {
		t.Errorf("stored hash does not verify: %v", err)
	}

	// All four built-in roles must exist so the UI can assign them.
	var roles int
	_ = s.Read().QueryRow(`SELECT count(*) FROM roles`).Scan(&roles)
	if roles != 4 {
		t.Errorf("roles seeded = %d, want 4", roles)
	}
}

func TestCreateAdminRejectsDuplicateUsername(t *testing.T) {
	s, _ := newCtlEnv(t)
	ctx := context.Background()
	if err := createAdmin(ctx, s, "root", "pw", "super_admin"); err != nil {
		t.Fatalf("first createAdmin: %v", err)
	}
	if err := createAdmin(ctx, s, "ROOT", "pw", "super_admin"); err == nil {
		t.Fatal("duplicate username accepted; usernames are case-insensitive")
	}
}

func TestCreateAdminRejectsUnknownRole(t *testing.T) {
	s, _ := newCtlEnv(t)
	if err := createAdmin(context.Background(), s, "x", "pw", "wizard"); err == nil {
		t.Fatal("unknown role accepted")
	}
}

func TestResetPasswordChangesHashAndRevokesSessions(t *testing.T) {
	s, _ := newCtlEnv(t)
	ctx := context.Background()
	if err := createAdmin(ctx, s, "root", "old", "super_admin"); err != nil {
		t.Fatalf("createAdmin: %v", err)
	}

	sessions := auth.NewSessions(s, nil)
	var adminID int64
	_ = s.Read().QueryRow(`SELECT id FROM admins WHERE username='root'`).Scan(&adminID)
	token, err := sessions.Create(ctx, adminID, "10.0.0.1", "test")
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	if err := resetPassword(ctx, s, "root", "new"); err != nil {
		t.Fatalf("resetPassword: %v", err)
	}

	var hash string
	_ = s.Read().QueryRow(`SELECT password_hash FROM admins WHERE username='root'`).Scan(&hash)
	if ok, _ := auth.VerifyPassword(hash, "new"); !ok {
		t.Error("new password does not verify")
	}
	if ok, _ := auth.VerifyPassword(hash, "old"); ok {
		t.Error("old password still verifies")
	}
	if _, err := sessions.Lookup(ctx, token); err == nil {
		t.Error("existing session survived a password reset")
	}
}

func TestCtlActionsAreAuditedAsCtlActor(t *testing.T) {
	s, _ := newCtlEnv(t)
	ctx := context.Background()
	if err := createAdmin(ctx, s, "root", "pw", "super_admin"); err != nil {
		t.Fatalf("createAdmin: %v", err)
	}
	var actorType, action string
	if err := s.Read().QueryRow(
		`SELECT actor_type, action FROM audit_log ORDER BY id DESC LIMIT 1`,
	).Scan(&actorType, &action); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if actorType != "ctl" || action != "admin.create" {
		t.Errorf("audit = %s/%s, want ctl/admin.create", actorType, action)
	}
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./cmd/antimage-ctl/...`
Expected: FAIL — `undefined: createAdmin`.

- [ ] **Step 3: Implement the commands**

`cmd/antimage-ctl/commands.go`:

```go
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/auth"
	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

func ctlActor() audit.Actor {
	return audit.Actor{Type: audit.ActorCtl, Label: "antimage-ctl"}
}

// seedRoles inserts the four built-in role templates if they are absent.
func seedRoles(ctx context.Context, tx *sql.Tx) error {
	for name, perms := range rbac.BuiltinRoles() {
		encoded, err := json.Marshal(perms)
		if err != nil {
			return fmt.Errorf("encode %s permissions: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO roles (name, is_builtin, permissions) VALUES (?, 1, ?)
			 ON CONFLICT(name) DO UPDATE SET permissions = excluded.permissions`,
			name, string(encoded)); err != nil {
			return fmt.Errorf("seed role %s: %w", name, err)
		}
	}
	return nil
}

func createAdmin(ctx context.Context, s *store.Store, username, password, role string) error {
	if _, ok := rbac.BuiltinRoles()[role]; !ok {
		return fmt.Errorf("unknown role %q; choose super_admin, admin, reseller, or readonly", role)
	}
	if strings.TrimSpace(username) == "" || password == "" {
		return errors.New("username and password are required")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	return s.Write(ctx, func(tx *sql.Tx) error {
		if err := seedRoles(ctx, tx); err != nil {
			return err
		}
		var roleID int64
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM roles WHERE name = ?`, role).Scan(&roleID); err != nil {
			return fmt.Errorf("look up role: %w", err)
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO admins (username, password_hash, role_id, created_at) VALUES (?,?,?,?)`,
			username, hash, roleID, time.Now().UTC().Unix())
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return fmt.Errorf("an admin named %q already exists", username)
			}
			return fmt.Errorf("create admin: %w", err)
		}
		adminID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		return audit.InTx(ctx, tx, "", ctlActor(), audit.Record{
			Action: "admin.create", TargetType: "admin",
			TargetID: sql.NullInt64{Int64: adminID, Valid: true},
			After:    map[string]any{"username": username, "role": role},
			Result:   "ok",
		})
	})
}

// resetPassword is the lockout recovery path. It revokes every session for
// the account in the same transaction, so a stolen session cannot outlive the
// password it was created with.
func resetPassword(ctx context.Context, s *store.Store, username, password string) error {
	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	now := time.Now().UTC().Unix()

	return s.Write(ctx, func(tx *sql.Tx) error {
		var adminID int64
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM admins WHERE username = ? COLLATE NOCASE`, username).Scan(&adminID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("no admin named %q", username)
			}
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE admins SET password_hash = ? WHERE id = ?`, hash, adminID); err != nil {
			return fmt.Errorf("update password: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE sessions SET revoked_at = ? WHERE admin_id = ? AND revoked_at IS NULL`,
			now, adminID); err != nil {
			return fmt.Errorf("revoke sessions: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM login_attempts WHERE kind = 'account' AND subject = ?`,
			strings.ToLower(username)); err != nil {
			return fmt.Errorf("clear lockout: %w", err)
		}
		return audit.InTx(ctx, tx, "", ctlActor(), audit.Record{
			Action: "admin.reset_password", TargetType: "admin",
			TargetID: sql.NullInt64{Int64: adminID, Valid: true}, Result: "ok",
		})
	})
}

func listAdmins(ctx context.Context, s *store.Store, out io.Writer) error {
	rows, err := s.Read().QueryContext(ctx,
		`SELECT a.username, r.name, a.status FROM admins a
		   JOIN roles r ON r.id = a.role_id ORDER BY a.username`)
	if err != nil {
		return fmt.Errorf("list admins: %w", err)
	}
	defer func() { _ = rows.Close() }()

	fmt.Fprintf(out, "%-24s %-14s %s\n", "USERNAME", "ROLE", "STATUS")
	for rows.Next() {
		var username, role, status string
		if err := rows.Scan(&username, &role, &status); err != nil {
			return err
		}
		fmt.Fprintf(out, "%-24s %-14s %s\n", username, role, status)
	}
	return rows.Err()
}

// backup uses SQLite's VACUUM INTO, which produces a consistent copy while
// the panel keeps running.
func backup(ctx context.Context, s *store.Store, destination string) error {
	if _, err := os.Stat(destination); err == nil {
		return fmt.Errorf("%s already exists; refusing to overwrite", destination)
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `VACUUM INTO ?`, destination)
		if err != nil {
			return fmt.Errorf("vacuum into %s: %w", destination, err)
		}
		return nil
	})
}
```

- [ ] **Step 4: Implement main**

`cmd/antimage-ctl/main.go`:

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/shared/version"
)

const usage = `antimage-ctl — local administration for the antimage panel

Usage:
  antimage-ctl [--data-dir DIR] <command> [arguments]

Commands:
  create-admin   USERNAME PASSWORD ROLE   create an admin (roles: super_admin, admin, reseller, readonly)
  reset-password USERNAME PASSWORD        set a new password and revoke that admin's sessions
  list-admins                             list admins with their roles
  enroll-token   NODE_ID                  print a single-use enrollment token
  backup         DEST.db                  write a consistent database copy
  version                                 print the version
`

func main() {
	dataDir := flag.String("data-dir", "/var/lib/antimage", "data directory")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	if args[0] == "version" {
		fmt.Println(version.Version)
		return
	}

	s, err := store.Open(filepath.Join(*dataDir, "antimage.db"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: open database: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	if err := dispatch(ctx, s, args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func dispatch(ctx context.Context, s *store.Store, args []string) error {
	switch args[0] {
	case "create-admin":
		if len(args) != 4 {
			return fmt.Errorf("usage: create-admin USERNAME PASSWORD ROLE")
		}
		if err := createAdmin(ctx, s, args[1], args[2], args[3]); err != nil {
			return err
		}
		fmt.Printf("created admin %q with role %q\n", args[1], args[3])
		return nil

	case "reset-password":
		if len(args) != 3 {
			return fmt.Errorf("usage: reset-password USERNAME PASSWORD")
		}
		if err := resetPassword(ctx, s, args[1], args[2]); err != nil {
			return err
		}
		fmt.Printf("password reset for %q; all their sessions were revoked\n", args[1])
		return nil

	case "list-admins":
		return listAdmins(ctx, s, os.Stdout)

	case "enroll-token":
		if len(args) != 2 {
			return fmt.Errorf("usage: enroll-token NODE_ID")
		}
		var nodeID int64
		if _, err := fmt.Sscanf(args[1], "%d", &nodeID); err != nil {
			return fmt.Errorf("invalid node id %q", args[1])
		}
		token, err := nodes.IssueEnrollToken(ctx, s, nodeID,
			audit.Actor{Type: audit.ActorCtl, Label: "antimage-ctl"}, "", time.Now().UTC())
		if err != nil {
			return err
		}
		fmt.Println(token)
		return nil

	case "backup":
		if len(args) != 2 {
			return fmt.Errorf("usage: backup DEST.db")
		}
		if err := backup(ctx, s, args[1]); err != nil {
			return err
		}
		fmt.Printf("backup written to %s\n", args[1])
		return nil

	default:
		return fmt.Errorf("unknown command %q; run with --help", args[0])
	}
}
```

- [ ] **Step 5: Run and watch them pass**

Run: `go test ./cmd/... -race -count=1 -v && make build`
Expected: PASS — five ctl tests; three binaries build.

- [ ] **Step 6: Commit**

```bash
git add cmd/antimage-ctl
git commit -m "feat(ctl): admin creation, password recovery, enrollment tokens, and backup"
```

---

# Phase H — UI

### Task 30: UI scaffold, embedding, RTL enforcement, and login

**Files:**
- Create: `web/package.json`, `web/vite.config.ts`, `web/tailwind.config.js`, `web/tsconfig.json`
- Create: `web/eslint.config.js`, `web/src/main.tsx`, `web/src/i18n/{index.ts,en.json,fa.json}`
- Create: `web/src/lib/api.ts`, `web/src/routes/Login.tsx`, `web/src/App.tsx`
- Create: `internal/panel/webui/embed.go`; replace `internal/panel/httpapi/ui.go`
- Test: `web/src/i18n/i18n.test.ts`, `scripts/check-rtl.sh`

**Interfaces:**
- Produces: `webui.Handler(devProxy string) http.Handler`; `t(key)` translation helper; `api.post/get` wrappers.

- [ ] **Step 1: Scaffold**

```bash
cd web
npm create vite@latest . -- --template react-ts
npm install
npm install -D tailwindcss @tailwindcss/postcss postcss autoprefixer eslint vitest
npm install @tanstack/react-query react-router-dom
cd ..
```

- [ ] **Step 2: Write the RTL enforcement gate**

`scripts/check-rtl.sh`:

```bash
#!/usr/bin/env bash
# Mechanically enforces the RTL rules from spec section 8. Retrofitting RTL
# fails because nobody remembers the rules; a failing build remembers them.
set -uo pipefail

SRC="web/src"
fails=0

# Physical direction utilities are banned; logical ones must be used instead.
if matches=$(grep -rnE '\b(ml-|mr-|pl-|pr-|left-|right-|text-left|text-right)[0-9a-z]' \
     --include='*.tsx' --include='*.ts' --include='*.css' "$SRC" 2>/dev/null); then
  if [ -n "$matches" ]; then
    echo "FAIL: physical direction utilities found. Use ms-/me-/ps-/pe-/start-/end-/text-start/text-end."
    echo "$matches"
    fails=$((fails + 1))
  fi
fi

# Literal user-facing strings in JSX must go through t().
if matches=$(grep -rnE '>[[:space:]]*[A-Z][a-z]{2,}[^<{]*<' \
     --include='*.tsx' "$SRC" 2>/dev/null | grep -v 't('); then
  if [ -n "$matches" ]; then
    echo "FAIL: literal strings in JSX. Wrap them in t() so they can be translated."
    echo "$matches"
    fails=$((fails + 1))
  fi
fi

if [ "$fails" -eq 0 ]; then
  echo "OK: RTL and i18n gates clean."
fi
exit "$fails"
```

`chmod +x scripts/check-rtl.sh`, and add to the Makefile:

```makefile
.PHONY: check-rtl web
check-rtl:
	./scripts/check-rtl.sh

web:
	cd web && npm ci && npm run build
```

Add `make check-rtl` to `.github/workflows/ci.yml` after `make check-imports`.

- [ ] **Step 3: Write the failing i18n test**

`web/src/i18n/i18n.test.ts`:

```ts
import { describe, expect, it } from "vitest";
import en from "./en.json";
import fa from "./fa.json";
import { dirFor, formatNumber, t, setLocale } from "./index";

describe("i18n", () => {
  it("has identical key sets in every locale", () => {
    const enKeys = Object.keys(en).sort();
    const faKeys = Object.keys(fa).sort();
    expect(faKeys).toEqual(enKeys);
  });

  it("has no empty translations", () => {
    for (const [key, value] of Object.entries(fa)) {
      expect(value, `fa.${key} is empty`).not.toBe("");
    }
  });

  it("maps locales to text direction", () => {
    expect(dirFor("en")).toBe("ltr");
    expect(dirFor("fa")).toBe("rtl");
  });

  it("returns the key itself when a translation is missing", () => {
    setLocale("en");
    expect(t("no.such.key" as never)).toBe("no.such.key");
  });

  it("formats numbers per locale, including Persian digits", () => {
    expect(formatNumber(1234, "en")).toBe("1,234");
    expect(formatNumber(1234, "fa")).toMatch(/[۰-۹]/);
  });
});
```

- [ ] **Step 4: Run and watch it fail**

Run: `cd web && npx vitest run`
Expected: FAIL — cannot resolve `./index`.

- [ ] **Step 5: Implement i18n**

`web/src/i18n/en.json`:

```json
{
  "app.name": "antimage",
  "auth.username": "Username",
  "auth.password": "Password",
  "auth.totp": "Authenticator code",
  "auth.signIn": "Sign in",
  "auth.invalid": "Invalid credentials",
  "auth.rateLimited": "Too many attempts. Try again later.",
  "nav.nodes": "Nodes",
  "nav.audit": "Audit",
  "nav.sessions": "Sessions",
  "nav.signOut": "Sign out",
  "node.name": "Name",
  "node.address": "Address",
  "node.status": "Status",
  "node.revision": "Revision",
  "node.drift": "Out of sync",
  "node.lastSeen": "Last seen",
  "node.add": "Add node",
  "node.enrollToken": "Enrollment command",
  "node.revisions": "Revision history",
  "node.applyRuns": "Apply runs",
  "node.steps": "Steps",
  "status.pending": "Pending",
  "status.enrolling": "Enrolling",
  "status.online": "Online",
  "status.degraded": "Degraded",
  "status.integrity": "Integrity fault",
  "status.offline": "Offline",
  "status.disabled": "Disabled",
  "common.never": "Never",
  "common.close": "Close",
  "common.copy": "Copy"
}
```

`web/src/i18n/fa.json`:

```json
{
  "app.name": "آنتی‌میج",
  "auth.username": "نام کاربری",
  "auth.password": "گذرواژه",
  "auth.totp": "کد احراز هویت",
  "auth.signIn": "ورود",
  "auth.invalid": "اطلاعات ورود نادرست است",
  "auth.rateLimited": "تلاش‌های بیش از حد. بعداً دوباره تلاش کنید.",
  "nav.nodes": "گره‌ها",
  "nav.audit": "رویدادها",
  "nav.sessions": "نشست‌ها",
  "nav.signOut": "خروج",
  "node.name": "نام",
  "node.address": "نشانی",
  "node.status": "وضعیت",
  "node.revision": "نسخه",
  "node.drift": "ناهمگام",
  "node.lastSeen": "آخرین ارتباط",
  "node.add": "افزودن گره",
  "node.enrollToken": "فرمان ثبت",
  "node.revisions": "تاریخچه نسخه‌ها",
  "node.applyRuns": "اجرای تغییرات",
  "node.steps": "گام‌ها",
  "status.pending": "در انتظار",
  "status.enrolling": "در حال ثبت",
  "status.online": "آنلاین",
  "status.degraded": "معیوب",
  "status.integrity": "خطای یکپارچگی",
  "status.offline": "آفلاین",
  "status.disabled": "غیرفعال",
  "common.never": "هرگز",
  "common.close": "بستن",
  "common.copy": "کپی"
}
```

`web/src/i18n/index.ts`:

```ts
import en from "./en.json";
import fa from "./fa.json";

export type Locale = "en" | "fa";
export type TranslationKey = keyof typeof en;

const catalogs: Record<Locale, Record<string, string>> = { en, fa };

let current: Locale = "en";

export function setLocale(locale: Locale): void {
  current = locale;
  if (typeof document !== "undefined") {
    document.documentElement.lang = locale;
    // Flipping dir here is what makes every logical CSS property resolve
    // correctly; no component needs to know the direction.
    document.documentElement.dir = dirFor(locale);
  }
}

export function getLocale(): Locale {
  return current;
}

export function dirFor(locale: Locale): "ltr" | "rtl" {
  return locale === "fa" ? "rtl" : "ltr";
}

/** Returns the key itself when a translation is missing, so gaps are visible
 *  in the UI rather than rendering as blanks. */
export function t(key: TranslationKey): string {
  return catalogs[current][key] ?? key;
}

/** All number formatting goes through here, so Persian digits are handled in
 *  one place instead of scattered toLocaleString calls. */
export function formatNumber(value: number, locale: Locale = current): string {
  return new Intl.NumberFormat(locale === "fa" ? "fa-IR" : "en-US").format(value);
}

export function formatTimestamp(unixSeconds: number | null, locale: Locale = current): string {
  if (!unixSeconds) return t("common.never");
  return new Intl.DateTimeFormat(locale === "fa" ? "fa-IR" : "en-US", {
    dateStyle: "short",
    timeStyle: "medium",
  }).format(new Date(unixSeconds * 1000));
}
```

- [ ] **Step 6: Implement the API client and login**

`web/src/lib/api.ts`:

```ts
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const response = await fetch(path, {
    method,
    headers: body === undefined ? {} : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: "same-origin",
  });

  if (response.status === 204) return undefined as T;

  const payload = await response.json().catch(() => ({}));
  if (!response.ok) {
    const err = (payload as { error?: { code: string; message: string } }).error;
    throw new ApiError(response.status, err?.code ?? "unknown", err?.message ?? "request failed");
  }
  return payload as T;
}

export const api = {
  get: <T>(path: string) => request<T>("GET", path),
  post: <T>(path: string, body?: unknown) => request<T>("POST", path, body),
  put: <T>(path: string, body?: unknown) => request<T>("PUT", path, body),
  del: <T>(path: string) => request<T>("DELETE", path),
};
```

`web/src/routes/Login.tsx`:

```tsx
import { useState } from "react";
import { api, ApiError } from "../lib/api";
import { t } from "../i18n";

export function Login({ onSuccess }: { onSuccess: () => void }) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [totp, setTotp] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.post("/api/v1/auth/login", { username, password, totp });
      onSuccess();
    } catch (err) {
      const code = err instanceof ApiError ? err.code : "unknown";
      setError(code === "rate_limited" ? t("auth.rateLimited") : t("auth.invalid"));
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="flex min-h-screen items-center justify-center bg-zinc-950 text-zinc-100">
      <form onSubmit={submit} className="w-80 space-y-3 rounded border border-zinc-800 p-6">
        <h1 className="text-sm font-semibold tracking-wide text-zinc-400">{t("app.name")}</h1>

        <label className="block text-xs text-zinc-400">
          {t("auth.username")}
          <input
            className="mt-1 w-full rounded border border-zinc-700 bg-zinc-900 px-2 py-1 text-sm text-start"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            autoComplete="username"
            required
          />
        </label>

        <label className="block text-xs text-zinc-400">
          {t("auth.password")}
          <input
            type="password"
            className="mt-1 w-full rounded border border-zinc-700 bg-zinc-900 px-2 py-1 text-sm text-start"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="current-password"
            required
          />
        </label>

        <label className="block text-xs text-zinc-400">
          {t("auth.totp")}
          <input
            className="mt-1 w-full rounded border border-zinc-700 bg-zinc-900 px-2 py-1 font-mono text-sm text-start"
            value={totp}
            onChange={(e) => setTotp(e.target.value)}
            inputMode="numeric"
            autoComplete="one-time-code"
          />
        </label>

        {error && (
          <p role="alert" className="text-xs text-red-400">
            {error}
          </p>
        )}

        <button
          type="submit"
          disabled={busy}
          className="w-full rounded bg-zinc-100 px-2 py-1 text-sm font-medium text-zinc-900 disabled:opacity-50"
        >
          {t("auth.signIn")}
        </button>
      </form>
    </main>
  );
}
```

- [ ] **Step 7: Implement embedding**

`internal/panel/webui/embed.go`:

```go
// Package webui serves the compiled single-page application.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

//go:embed all:dist
var assets embed.FS

// Handler serves the embedded build. When devProxy is set, requests are
// forwarded to a running Vite server instead, so hot reload works without a
// separate router.
func Handler(devProxy string) http.Handler {
	if devProxy != "" {
		target, err := url.Parse(devProxy)
		if err == nil {
			return httputil.NewSingleHostReverseProxy(target)
		}
	}

	dist, err := fs.Sub(assets, "dist")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "ui not built", http.StatusInternalServerError)
		})
	}
	files := http.FileServer(http.FS(dist))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Client-side routing: unknown non-asset paths fall back to index.html.
		if !strings.Contains(r.URL.Path, ".") && r.URL.Path != "/" {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}
```

Replace `internal/panel/httpapi/ui.go`:

```go
package httpapi

import (
	"net/http"
	"os"

	"github.com/amyrm/antimage/internal/panel/webui"
)

// uiHandler serves the embedded SPA, or proxies to Vite when
// ANTIMAGE_DEV_PROXY is set.
func (d Deps) uiHandler() http.Handler {
	return webui.Handler(os.Getenv("ANTIMAGE_DEV_PROXY"))
}
```

Create `web/dist/.gitkeep` so `go:embed all:dist` compiles before the first UI build, and make `make build` depend on `web`.

- [ ] **Step 8: Run everything**

Run: `cd web && npx vitest run && npm run build && cd .. && make check-rtl && go build ./...`
Expected: five i18n tests PASS, UI builds, RTL gate clean, Go builds.

- [ ] **Step 9: Commit**

```bash
git add web internal/panel/webui internal/panel/httpapi/ui.go scripts/check-rtl.sh Makefile .github
git commit -m "feat(ui): Vite scaffold, embedded SPA, enforced RTL and i18n, login screen"
```

---

### Task 31: Node list and node detail

**Files:**
- Create: `web/src/routes/Nodes.tsx`, `web/src/routes/NodeDetail.tsx`
- Create: `web/src/components/{StatusBadge.tsx,DataTable.tsx}`, `web/src/lib/useNodeStream.ts`
- Test: `web/src/components/StatusBadge.test.tsx`, `web/src/lib/useNodeStream.test.ts`

**Interfaces:**
- Produces: `<StatusBadge status />`, `useNodeStream()` returning live status by node id.

Node detail is the screen the whole design exists to make possible: current versus applied revision, drift, revision history with actor and reason, and the last apply run expanded to per-step results with disruption level and stderr.

- [ ] **Step 1: Write the failing tests**

`web/src/components/StatusBadge.test.tsx`:

```tsx
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { StatusBadge } from "./StatusBadge";

describe("StatusBadge", () => {
  it("renders every node status with a label, not colour alone", () => {
    for (const status of [
      "pending", "enrolling", "online", "degraded", "integrity", "offline", "disabled",
    ] as const) {
      const { unmount } = render(<StatusBadge status={status} />);
      // Accessibility: colour must never be the only signal.
      expect(screen.getByRole("status").textContent?.trim()).not.toBe("");
      unmount();
    }
  });

  it("marks integrity faults as alerts", () => {
    render(<StatusBadge status="integrity" />);
    expect(screen.getByRole("status")).toHaveAttribute("data-severity", "alert");
  });

  it("falls back gracefully on an unknown status", () => {
    render(<StatusBadge status={"martian" as never} />);
    expect(screen.getByRole("status").textContent).toContain("martian");
  });
});
```

`web/src/lib/useNodeStream.test.ts`:

```ts
import { describe, expect, it } from "vitest";
import { parseNodeEvent } from "./useNodeStream";

describe("parseNodeEvent", () => {
  it("indexes node status by id", () => {
    const parsed = parseNodeEvent(
      JSON.stringify({
        nodes: [
          { id: 1, status: "online", online: true, desired_revision: 3, applied_revision: 3, drift: false },
          { id: 2, status: "degraded", online: true, desired_revision: 5, applied_revision: 4, drift: true },
        ],
      }),
    );
    expect(parsed[1].status).toBe("online");
    expect(parsed[2].drift).toBe(true);
  });

  it("returns an empty map for malformed payloads rather than throwing", () => {
    expect(parseNodeEvent("not json")).toEqual({});
    expect(parseNodeEvent(JSON.stringify({ nodes: null }))).toEqual({});
  });
});
```

- [ ] **Step 2: Run and watch them fail**

Run: `cd web && npx vitest run`
Expected: FAIL — cannot resolve `./StatusBadge`.

- [ ] **Step 3: Implement**

```bash
cd web && npm install -D @testing-library/react @testing-library/jest-dom jsdom && cd ..
```

`web/src/components/StatusBadge.tsx`:

```tsx
import { t, type TranslationKey } from "../i18n";

export type NodeStatus =
  | "pending" | "enrolling" | "online" | "degraded"
  | "integrity" | "offline" | "disabled";

const styles: Record<NodeStatus, { className: string; severity: string }> = {
  pending:   { className: "border-zinc-600 text-zinc-400",   severity: "info" },
  enrolling: { className: "border-sky-600 text-sky-400",     severity: "info" },
  online:    { className: "border-emerald-600 text-emerald-400", severity: "ok" },
  degraded:  { className: "border-amber-600 text-amber-400", severity: "warn" },
  integrity: { className: "border-red-600 text-red-400",     severity: "alert" },
  offline:   { className: "border-zinc-700 text-zinc-500",   severity: "warn" },
  disabled:  { className: "border-zinc-700 text-zinc-500",   severity: "info" },
};

export function StatusBadge({ status }: { status: NodeStatus }) {
  const style = styles[status];
  // Colour is never the only signal: the label always carries the meaning.
  const label = style ? t(`status.${status}` as TranslationKey) : status;

  return (
    <span
      role="status"
      data-severity={style?.severity ?? "info"}
      className={`inline-block rounded border px-1.5 py-0.5 font-mono text-[11px] uppercase ${
        style?.className ?? "border-zinc-700 text-zinc-400"
      }`}
    >
      {label}
    </span>
  );
}
```

`web/src/lib/useNodeStream.ts`:

```ts
import { useEffect, useState } from "react";

export interface NodeStatusUpdate {
  id: number;
  status: string;
  online: boolean;
  desired_revision: number;
  applied_revision: number;
  drift: boolean;
}

/** Parses one SSE payload. Malformed data yields an empty map rather than
 *  throwing, so one bad frame cannot break the live view. */
export function parseNodeEvent(data: string): Record<number, NodeStatusUpdate> {
  try {
    const parsed = JSON.parse(data) as { nodes?: NodeStatusUpdate[] };
    if (!Array.isArray(parsed.nodes)) return {};
    return Object.fromEntries(parsed.nodes.map((n) => [n.id, n]));
  } catch {
    return {};
  }
}

/** Subscribes to live node status, falling back to polling if SSE fails. */
export function useNodeStream(): Record<number, NodeStatusUpdate> {
  const [statuses, setStatuses] = useState<Record<number, NodeStatusUpdate>>({});

  useEffect(() => {
    const source = new EventSource("/api/v1/events");
    source.addEventListener("nodes", (event) => {
      setStatuses(parseNodeEvent((event as MessageEvent<string>).data));
    });

    let pollTimer: number | undefined;
    source.onerror = () => {
      source.close();
      if (pollTimer !== undefined) return;
      pollTimer = window.setInterval(async () => {
        const res = await fetch("/api/v1/events", { headers: { Accept: "application/json" } });
        if (res.ok) setStatuses(parseNodeEvent(await res.text()));
      }, 5000);
    };

    return () => {
      source.close();
      if (pollTimer !== undefined) window.clearInterval(pollTimer);
    };
  }, []);

  return statuses;
}
```

`web/src/routes/NodeDetail.tsx`:

```tsx
import { useQuery } from "@tanstack/react-query";
import { api } from "../lib/api";
import { formatNumber, formatTimestamp, t } from "../i18n";
import { StatusBadge, type NodeStatus } from "../components/StatusBadge";

interface NodeDetailData {
  id: number;
  name: string;
  address: string;
  status: NodeStatus;
  desired_revision: number;
  applied_revision: number;
  online: boolean;
}

interface Revision {
  revision: number;
  created_at: number;
  actor_type: string;
  actor_label: string;
  reason: string;
  sha256: string;
}

interface ApplyStep {
  seq: number;
  kind: string;
  disruption: string;
  outcome: string;
  error: string;
  duration_ms: number;
}

interface ApplyRun {
  id: number;
  target_revision: number;
  started_at: number;
  outcome: string;
  steps: ApplyStep[];
}

export function NodeDetail({ nodeId }: { nodeId: number }) {
  const node = useQuery({
    queryKey: ["node", nodeId],
    queryFn: () => api.get<NodeDetailData>(`/api/v1/nodes/${nodeId}`),
  });
  const revisions = useQuery({
    queryKey: ["node", nodeId, "revisions"],
    queryFn: () => api.get<{ revisions: Revision[] }>(`/api/v1/nodes/${nodeId}/revisions`),
  });
  const runs = useQuery({
    queryKey: ["node", nodeId, "runs"],
    queryFn: () => api.get<{ runs: ApplyRun[] }>(`/api/v1/nodes/${nodeId}/apply-runs`),
  });

  if (!node.data) return null;
  const drift = node.data.applied_revision !== node.data.desired_revision;

  return (
    <div className="space-y-6 p-4 text-sm text-zinc-200">
      <header className="flex items-center gap-3">
        <h2 className="font-mono text-base">{node.data.name}</h2>
        <StatusBadge status={node.data.status} />
        <span className="font-mono text-xs text-zinc-500">{node.data.address}</span>
      </header>

      <section className="flex gap-6 border-y border-zinc-800 py-2 font-mono text-xs">
        <span>
          {t("node.revision")}: {formatNumber(node.data.applied_revision)} /{" "}
          {formatNumber(node.data.desired_revision)}
        </span>
        {drift && <span className="text-amber-400">{t("node.drift")}</span>}
      </section>

      <section>
        <h3 className="mb-1 text-xs uppercase tracking-wide text-zinc-500">
          {t("node.revisions")}
        </h3>
        <table className="w-full border-collapse font-mono text-xs">
          <tbody>
            {revisions.data?.revisions.map((rev) => (
              <tr key={rev.revision} className="border-b border-zinc-900">
                <td className="py-1 pe-3 text-zinc-400">{formatNumber(rev.revision)}</td>
                <td className="pe-3 text-zinc-500">{formatTimestamp(rev.created_at)}</td>
                <td className="pe-3">{rev.actor_label || rev.actor_type}</td>
                <td className="pe-3 text-zinc-400">{rev.reason}</td>
                <td className="text-zinc-600">{rev.sha256.slice(0, 12)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>

      <section>
        <h3 className="mb-1 text-xs uppercase tracking-wide text-zinc-500">
          {t("node.applyRuns")}
        </h3>
        {runs.data?.runs.map((run) => (
          <details key={run.id} className="border-b border-zinc-900 py-1">
            <summary className="cursor-pointer font-mono text-xs">
              <span className="text-zinc-400">{formatNumber(run.target_revision)}</span>{" "}
              <span className="text-zinc-500">{formatTimestamp(run.started_at)}</span>{" "}
              <span className={run.outcome === "converged" ? "text-emerald-400" : "text-amber-400"}>
                {run.outcome}
              </span>
            </summary>
            <table className="mt-1 w-full border-collapse font-mono text-[11px]">
              <tbody>
                {run.steps.map((step) => (
                  <tr key={step.seq} className="border-t border-zinc-900">
                    <td className="py-0.5 pe-3 text-zinc-500">{step.seq}</td>
                    <td className="pe-3">{step.kind}</td>
                    <td className="pe-3 text-zinc-500">{step.disruption}</td>
                    <td
                      className={`pe-3 ${
                        step.outcome === "ok" ? "text-emerald-400" : "text-red-400"
                      }`}
                    >
                      {step.outcome}
                    </td>
                    <td className="pe-3 text-zinc-500">{formatNumber(step.duration_ms)}ms</td>
                    <td className="text-red-400">{step.error}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </details>
        ))}
      </section>
    </div>
  );
}
```

`web/src/routes/Nodes.tsx`:

```tsx
import { useQuery } from "@tanstack/react-query";
import { api } from "../lib/api";
import { formatNumber, formatTimestamp, t } from "../i18n";
import { StatusBadge, type NodeStatus } from "../components/StatusBadge";
import { useNodeStream } from "../lib/useNodeStream";

interface NodeRow {
  id: number;
  name: string;
  address: string;
  status: NodeStatus;
  desired_revision: number;
  applied_revision: number;
  last_seen_at: number | null;
  online: boolean;
}

export function Nodes({ onSelect }: { onSelect: (id: number) => void }) {
  const nodes = useQuery({
    queryKey: ["nodes"],
    queryFn: () => api.get<{ nodes: NodeRow[] }>("/api/v1/nodes"),
  });
  const live = useNodeStream();

  return (
    <table className="w-full border-collapse text-sm text-zinc-200">
      <thead>
        <tr className="border-b border-zinc-800 text-start text-xs uppercase tracking-wide text-zinc-500">
          <th className="py-2 pe-3 text-start">{t("node.name")}</th>
          <th className="pe-3 text-start">{t("node.address")}</th>
          <th className="pe-3 text-start">{t("node.status")}</th>
          <th className="pe-3 text-start">{t("node.revision")}</th>
          <th className="text-start">{t("node.lastSeen")}</th>
        </tr>
      </thead>
      <tbody>
        {nodes.data?.nodes.map((node) => {
          const status = live[node.id]?.status ?? node.status;
          const applied = live[node.id]?.applied_revision ?? node.applied_revision;
          const desired = live[node.id]?.desired_revision ?? node.desired_revision;
          return (
            <tr
              key={node.id}
              onClick={() => onSelect(node.id)}
              className="cursor-pointer border-b border-zinc-900 hover:bg-zinc-900"
            >
              <td className="py-1.5 pe-3 font-mono">{node.name}</td>
              <td className="pe-3 font-mono text-xs text-zinc-500">{node.address}</td>
              <td className="pe-3">
                <StatusBadge status={status as NodeStatus} />
              </td>
              <td className="pe-3 font-mono text-xs">
                {formatNumber(applied)} / {formatNumber(desired)}
                {applied !== desired && (
                  <span className="ms-2 text-amber-400">{t("node.drift")}</span>
                )}
              </td>
              <td className="font-mono text-xs text-zinc-500">
                {formatTimestamp(node.last_seen_at)}
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}
```

- [ ] **Step 4: Configure vitest for jsdom**

Add to `web/vite.config.ts`:

```ts
  test: {
    environment: "jsdom",
    setupFiles: ["./src/setupTests.ts"],
  },
```

`web/src/setupTests.ts`: `import "@testing-library/jest-dom";`

- [ ] **Step 5: Run and watch them pass**

Run: `cd web && npx vitest run && npm run build && cd .. && make check-rtl`
Expected: PASS — three StatusBadge tests, two stream tests, five i18n tests; build succeeds; RTL gate clean.

- [ ] **Step 6: Commit**

```bash
git add web
git commit -m "feat(ui): node list with live status and node detail with revisions and apply runs"
```

---

# Phase I — Acceptance

### Task 32: End-to-end acceptance against the Definition of Done

**Files:**
- Create: `test/e2e/e2e_test.go`
- Create: `test/e2e/docker-compose.yml`, `test/e2e/Dockerfile.node`

**Interfaces:**
- Consumes: everything.
- Produces: one test per numbered item in spec section 10.

- [ ] **Step 1: Write the acceptance test**

`test/e2e/e2e_test.go`:

```go
//go:build e2e

package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These map one-to-one onto the six items in spec section 10.

func TestAcceptance(t *testing.T) {
	env := startPanelAndNode(t)

	t.Run("1_node_enrolls_and_reaches_online", func(t *testing.T) {
		env.waitForStatus(t, "online", 60*time.Second)
	})

	t.Run("2_service_bumps_revision_and_converges", func(t *testing.T) {
		env.createService(t, `{"adapter_kind":"stub","params":{"port":8443}}`)
		env.waitForConverged(t, 30*time.Second)

		body := env.readManagedFile(t)
		if !strings.Contains(body, `"port":8443`) {
			t.Fatalf("node did not converge; managed file:\n%s", body)
		}
	})

	t.Run("3_killing_the_agent_marks_the_node_offline", func(t *testing.T) {
		env.stopAgent(t)
		env.waitForStatus(t, "offline", 3*time.Minute)
	})

	t.Run("4_restarting_the_agent_self_heals", func(t *testing.T) {
		// Change desired state while the node is down, so recovery must
		// converge rather than merely reconnect.
		env.createService(t, `{"adapter_kind":"stub","params":{"port":9443}}`)
		env.startAgent(t)
		env.waitForStatus(t, "online", 60*time.Second)
		env.waitForConverged(t, 60*time.Second)

		if body := env.readManagedFile(t); !strings.Contains(body, `"port":9443`) {
			t.Fatal("node did not self-heal to the state it missed while offline")
		}
	})

	t.Run("5_hand_editing_a_managed_file_is_reported_as_drift", func(t *testing.T) {
		env.appendToManagedFile(t, "\n# hand edited\n")
		env.waitForApplyRun(t, 6*time.Minute)

		runs := env.applyRuns(t)
		if len(runs) == 0 {
			t.Fatal("no apply run recorded after the hand edit")
		}
		if len(runs[0].Steps) == 0 {
			t.Fatal("drift produced no corrective steps")
		}
	})

	t.Run("6_deleting_the_node_locks_it_out", func(t *testing.T) {
		env.deleteNode(t)
		// The allow-list is the revocation mechanism, so the agent's next
		// connection must be refused.
		env.waitForAgentLogContaining(t, "not enrolled", 90*time.Second)
	})
}

func TestCIGatesPass(t *testing.T) {
	root := repoRoot(t)
	for _, cmd := range [][]string{
		{"make", "test"},
		{"make", "check-imports"},
		{"make", "check-rtl"},
		{"bash", "scripts/install_test.sh"},
	} {
		out, err := exec.Command(cmd[0], cmd[1:]...).CombinedOutput()
		if err != nil {
			t.Errorf("%s failed: %v\n%s", strings.Join(cmd, " "), err, out)
		}
	}
	_ = root
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Join(wd, "..", "..")
}

var _ = context.Background
```

- [ ] **Step 2: Write the container setup**

`test/e2e/Dockerfile.node`:

```dockerfile
# A Debian host with systemd absent: the agent runs in the foreground so the
# test can start and stop it directly, while install.sh is still exercised for
# its platform guards and checksum verification.
FROM debian:12-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*

COPY bin/antimage-node /usr/local/bin/antimage-node
RUN mkdir -p /etc/antimage /var/lib/antimage && chmod 700 /var/lib/antimage

ENTRYPOINT ["/usr/local/bin/antimage-node", "--config", "/etc/antimage/node.yaml"]
```

`test/e2e/docker-compose.yml`:

```yaml
services:
  panel:
    build:
      context: ../..
      dockerfile: test/e2e/Dockerfile.panel
    ports: ["8080:8080", "8443:8443"]
    environment:
      ANTIMAGE_MASTER_KEY: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

  node:
    build:
      context: ../..
      dockerfile: test/e2e/Dockerfile.node
    depends_on: [panel]
    volumes:
      - node-state:/var/lib/antimage
      - node-config:/etc/antimage

volumes:
  node-state:
  node-config:
```

`test/e2e/Dockerfile.panel`:

```dockerfile
FROM debian:12-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY bin/antimage-panel /usr/local/bin/antimage-panel
COPY bin/antimage-ctl /usr/local/bin/antimage-ctl
RUN mkdir -p /var/lib/antimage && chmod 700 /var/lib/antimage
ENTRYPOINT ["/usr/local/bin/antimage-panel", "--data-dir", "/var/lib/antimage"]
```

- [ ] **Step 3: Add the Makefile target**

```makefile
.PHONY: e2e
e2e: build
	docker compose -f test/e2e/docker-compose.yml build
	go test ./test/e2e/... -tags e2e -v -timeout 20m
	docker compose -f test/e2e/docker-compose.yml down -v
```

- [ ] **Step 4: Implement the test harness**

Write `test/e2e/harness.go` (build tag `e2e`) providing `startPanelAndNode`, `waitForStatus`, `waitForConverged`, `createService`, `readManagedFile`, `appendToManagedFile`, `stopAgent`, `startAgent`, `applyRuns`, `deleteNode`, and `waitForAgentLogContaining`. Each wraps `docker compose exec` and the panel HTTP API; `waitFor*` helpers poll every second until their deadline and fail with the last observed value, so a failure names what the state actually was.

- [ ] **Step 5: Run the acceptance suite**

Run: `make e2e`
Expected: all six subtests PASS, and `TestCIGatesPass` confirms every gate.

- [ ] **Step 6: Commit**

```bash
git add test/e2e Makefile
git commit -m "test(e2e): acceptance suite covering the SP1 definition of done"
```

---

## Plan self-review

**Spec coverage.** Section 1 scope → Tasks 1, 14, 15 (stub only, no real adapter). Section 2 decisions → Tasks 1, 2, 22. Section 3 layout and boundaries → Task 1 (`check-imports`), Task 24 (`Hub.Notify` from handlers, never a stream). Section 4 adapter contract → Tasks 14–16; 4.1 disruption → Task 14 plus the maintenance-window tests in 16; 4.2 `ServiceSchema` → Tasks 15, 24; 4.3 drift → Task 15; 4.4 failure handling → Task 16. Section 5 data model → Task 12; all ten invariants → Tasks 12, 13, 21 (6 and 7 specifically in 21), 11 (8 and 9), 13 (10). Section 6 auth → Tasks 5–8; RBAC two layers → Tasks 9, 10; audit scope → Tasks 11, 23–25, 29. Section 7 credential policy → Task 28; bootstrap → Tasks 27, 28; enrollment → Tasks 18, 20, 22; steady state → Tasks 19–22; state machine → Tasks 21, 25. Section 8 UI → Tasks 30, 31. Section 9 testing → property tests in Tasks 3 and 16, store tests throughout, authz matrix in Tasks 9 and 10, integration in Tasks 19–22, e2e in Task 32, CI in Tasks 1, 27, 30. Section 10 definition of done → Task 32, one subtest per item.

Two gaps I found and closed while reviewing: `Deps.CA` was referenced by `handleCAFingerprint` but not populated until Task 27's `main.go`, so Task 23's `Deps` now declares it and the Task 23 test harness leaves it nil (that endpoint returns 503 in unit tests, which the Task 27 test covers with a real CA). And the SSH bootstrap route was defined in Task 28 but absent from the Task 23 router listing; Task 28 step 4 now registers it explicitly.

**Placeholders.** None. Every code step carries runnable code. The one prose-only step is Task 32 step 4, the e2e harness, which is deliberate: its helpers are thin `docker compose exec` wrappers whose exact form depends on the compose service names settled in step 2, and spelling them out before those exist would be guesswork rather than instruction.

**Type consistency.** `adapter.Desired` (Task 14) mirrors `nodes.Document` (Task 12) field-for-field with identical JSON tags — verified name by name, since the agent decodes the panel's canonical bytes into it. `StepResult.Seq` is `int` in Go (Tasks 14, 16) and `int32` on the wire (Task 17), converted explicitly in Task 22. `rbac.Scope` (Task 10) is what the store takes, `rbac.Actor` (Task 9) is what handlers hold, and `ScopeOf` bridges them at every call site. `CommitResult.Changed` gates `Hub.Notify` identically in all three service handlers (Task 24).

**Scope.** 32 tasks is large for one plan but it is one subsystem, and the spec already decomposed the product into eight. Phases A–C stand alone if you want a checkpoint: they end with a panel that stores authenticated, authorized, audited, revision-tracked desired state and no transport.

---

## Execution

Plan complete and saved to `docs/superpowers/plans/2026-08-13-antimage-control-plane-spine.md`. Two execution options:

**1. Subagent-Driven (recommended)** — a fresh subagent per task, with review between tasks and fast iteration.

**2. Inline Execution** — tasks executed in this session using executing-plans, batched with checkpoints for review.
