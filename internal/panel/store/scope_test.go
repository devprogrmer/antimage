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
