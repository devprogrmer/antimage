package audit

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

// TestAuditLogIsAppendOnly scans audit.go's actual source for mutation
// paths, rather than relying on a hardcoded switch that can never fail.
// It must genuinely fail if someone adds an Update, Delete, or Purge
// function, or an UPDATE/DELETE statement against audit_log.
func TestAuditLogIsAppendOnly(t *testing.T) {
	src, err := os.ReadFile("audit.go")
	if err != nil {
		t.Fatalf("read audit.go: %v", err)
	}
	text := string(src)

	for _, banned := range []string{"Update", "Delete", "Purge"} {
		re := regexp.MustCompile(`func ` + banned + `\b`)
		if re.MatchString(text) {
			t.Errorf("audit.go declares func %s; the log must be append-only", banned)
		}
	}

	upper := strings.ToUpper(text)
	for _, stmt := range []string{"UPDATE ", "DELETE "} {
		if strings.Contains(upper, stmt) && strings.Contains(upper, "AUDIT_LOG") {
			// Only fail if the mutating keyword actually appears applied to
			// audit_log — search line by line so an unrelated DELETE/UPDATE
			// substring elsewhere (comments, other tables) doesn't false-positive.
			for _, line := range strings.Split(upper, "\n") {
				if strings.Contains(line, stmt) && strings.Contains(line, "AUDIT_LOG") {
					t.Errorf("audit.go contains a %q statement against audit_log: %q", stmt, line)
				}
			}
		}
	}
}
