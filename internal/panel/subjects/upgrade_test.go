package subjects

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"

	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/panel/store/migrations"
	"github.com/amyrm/antimage/internal/shared/secrets"
)

// sp1SchemaVersion is the last migration that shipped in SP1. SP2 adds
// 00010_subjects.sql on top of it.
const sp1SchemaVersion = 9

// Upgrading a real SP1 panel must not lose anything.
//
// Every other migration test in this package starts from an empty database, so
// they prove the schema is reachable, not that an existing deployment survives
// reaching it. This builds a database at the SP1 schema, fills it with the rows
// an operator would actually have -- an account, a node, a service, audit
// history -- migrates it in place, and then exercises the SP2 feature on top of
// the upgraded database.
func TestUpgradingAnSP1DatabasePreservesItsState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sp1.db")

	// --- Build a database at the SP1 schema. ---
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("dialect: %v", err)
	}
	if err := goose.UpTo(raw, ".", sp1SchemaVersion); err != nil {
		t.Fatalf("migrate to SP1 schema: %v", err)
	}

	var got int64
	if err := raw.QueryRow(`SELECT max(version_id) FROM goose_db_version`).Scan(&got); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if got != sp1SchemaVersion {
		t.Fatalf("staged database is at version %d, want %d", got, sp1SchemaVersion)
	}
	t.Logf("staged an SP1 database at schema version %d", got)

	// The subjects tables must NOT exist yet, or this proves nothing.
	var n int
	_ = raw.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='subjects'`).Scan(&n)
	if n != 0 {
		t.Fatal("the SP1 schema already has a subjects table; the staging is wrong")
	}

	// --- Fill it with the state an operator would have. ---
	if _, err := raw.Exec(
		`INSERT INTO nodes (id, name, address, created_at) VALUES (1,'edge-1','203.0.113.7',?)`,
		1_700_000_000); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	if _, err := raw.Exec(
		`INSERT INTO services (id, node_id, adapter_kind, params, enabled, created_at)
		 VALUES (1, 1, 'xray', '{"protocol":"vless","port":443}', 1, ?)`,
		1_700_000_000); err != nil {
		t.Fatalf("seed service: %v", err)
	}
	if _, err := raw.Exec(
		`UPDATE nodes SET desired_revision = 7 WHERE id = 1`); err != nil {
		t.Fatalf("seed revision: %v", err)
	}
	if _, err := raw.Exec(
		`INSERT INTO audit_log (at, actor_type, actor_label, action, result)
		 VALUES (?, 'system', 'bootstrap', 'node.created', 'ok')`,
		1_700_000_000); err != nil {
		t.Fatalf("seed audit: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	// --- Upgrade in place, exactly as the panel does on boot. ---
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("upgrade to SP2: %v", err)
	}
	defer func() { _ = s.Close() }()

	var after int64
	if err := s.Read().QueryRow(
		`SELECT max(version_id) FROM goose_db_version`).Scan(&after); err != nil {
		t.Fatalf("read version after upgrade: %v", err)
	}
	if after <= sp1SchemaVersion {
		t.Fatalf("upgrade did not advance the schema: %d", after)
	}
	t.Logf("upgraded in place: schema %d -> %d", sp1SchemaVersion, after)

	// --- Nothing from SP1 may have been lost. ---
	var (
		nodeName string
		addr     string
		revision int64
	)
	if err := s.Read().QueryRow(
		`SELECT name, address, desired_revision FROM nodes WHERE id = 1`).
		Scan(&nodeName, &addr, &revision); err != nil {
		t.Fatalf("the SP1 node did not survive the upgrade: %v", err)
	}
	if nodeName != "edge-1" || addr != "203.0.113.7" {
		t.Errorf("node = %q/%q, want edge-1/203.0.113.7", nodeName, addr)
	}
	if revision != 7 {
		t.Errorf("desired_revision = %d, want 7; the agent would be told to re-apply", revision)
	}

	var kind string
	var enabled int
	if err := s.Read().QueryRow(
		`SELECT adapter_kind, enabled FROM services WHERE id = 1`).Scan(&kind, &enabled); err != nil {
		t.Fatalf("the SP1 service did not survive: %v", err)
	}
	if kind != "xray" || enabled != 1 {
		t.Errorf("service = %q enabled=%d, want xray enabled=1", kind, enabled)
	}

	var audits int
	if err := s.Read().QueryRow(
		`SELECT count(*) FROM audit_log WHERE action = 'node.created'`).Scan(&audits); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if audits != 1 {
		t.Errorf("audit rows = %d, want the SP1 history intact", audits)
	}
	t.Logf("SP1 state intact: node %q rev %d, service %q, %d audit row(s)",
		nodeName, revision, kind, audits)

	// --- The SP2 feature must work on the upgraded database. ---
	key := make([]byte, secrets.KeySize)
	for i := range key {
		key[i] = byte(i + 11)
	}
	box, err := secrets.NewBox(key)
	if err != nil {
		t.Fatalf("box: %v", err)
	}
	now := time.Now().UTC()
	st := NewStore(s, box, func() time.Time { return now })

	var subjectID int64
	err = s.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		subjectID, err = st.Create(context.Background(), tx, CreateInput{
			Name: "alice", ServiceIDs: []int64{1},
		})
		return err
	})
	if err != nil {
		t.Fatalf("create a subject on the upgraded database: %v", err)
	}

	cred, err := st.Credential(context.Background(), subjectID, "uuid")
	if err != nil {
		t.Fatalf("read the credential back: %v", err)
	}
	if cred == "" {
		t.Fatal("no credential was generated on the upgraded database")
	}

	// Sealed, not plaintext, on a database that started life as SP1.
	var stored []byte
	if err := s.Read().QueryRow(
		`SELECT value_enc FROM subject_credentials WHERE subject_id = ?`,
		subjectID).Scan(&stored); err != nil {
		t.Fatalf("read stored credential: %v", err)
	}
	if string(stored) == cred {
		t.Error("SECURITY: the credential is stored in plaintext after an upgrade")
	}
	t.Logf("SP2 works on the upgraded database: subject %d, %d-byte sealed credential",
		subjectID, len(stored))
}
