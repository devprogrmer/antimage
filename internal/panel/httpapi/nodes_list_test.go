package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
)

func (e *testEnv) seedNode(t *testing.T, name string) int64 {
	t.Helper()
	var id int64
	err := e.store.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`INSERT INTO nodes (name, address, created_at) VALUES (?, ?, 0)`, name, "10.0.0.1")
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("seedNode: %v", err)
	}
	return id
}

func TestListNodesReturnsTheScopedRows(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "alice", "pw", "super_admin")
	env.seedNode(t, "edge-a")
	env.seedNode(t, "edge-b")
	token := env.login(t, "alice", "pw")

	res := env.get(t, "/api/v1/nodes", token)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body)
	}
	var body struct {
		Nodes []struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			LastSeenAt *int64 `json:"last_seen_at"`
		} `json:"nodes"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Nodes) != 2 {
		t.Fatalf("got %d nodes, want 2: %+v", len(body.Nodes), body.Nodes)
	}
	if body.Nodes[0].Name != "edge-a" || body.Nodes[1].Name != "edge-b" {
		t.Errorf("names = %q/%q, want edge-a/edge-b", body.Nodes[0].Name, body.Nodes[1].Name)
	}
	if body.Nodes[0].LastSeenAt != nil {
		t.Errorf("last_seen_at = %v, want null for a never-seen node", *body.Nodes[0].LastSeenAt)
	}
}

// TestListNodesScopesToTheCallersNodes pins the second enforcement layer: a
// non-super actor sees only the nodes in their allow-list, and an empty
// allow-list means none rather than all.
func TestListNodesScopesToTheCallersNodes(t *testing.T) {
	env := newTestEnv(t)
	mine := env.seedNode(t, "edge-mine")
	env.seedNode(t, "edge-theirs")

	adminID := env.seedAdmin(t, "rachel", "pw", "reseller")
	err := env.store.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO admin_scopes (admin_id, scope_type, scope_id) VALUES (?, 'node', ?)`,
			adminID, mine)
		return err
	})
	if err != nil {
		t.Fatalf("grant scope: %v", err)
	}

	token := env.login(t, "rachel", "pw")
	res := env.get(t, "/api/v1/nodes", token)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body)
	}
	var body struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Nodes) != 1 || body.Nodes[0].Name != "edge-mine" {
		t.Fatalf("got %+v, want only edge-mine", body.Nodes)
	}
}

// TestListNodesRequiresNodeRead pins the first enforcement layer. The role
// here is a custom one with no permissions at all, which is what a super admin
// stripping node:read from a role produces.
func TestListNodesRequiresNodeRead(t *testing.T) {
	env := newTestEnv(t)
	env.seedNode(t, "edge-a")
	env.seedAdmin(t, "nobody", "pw", "no_permissions")

	token := env.login(t, "nobody", "pw")
	res := env.get(t, "/api/v1/nodes", token)
	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", res.Code)
	}
	if got := errorCode(t, res); got != "forbidden" {
		t.Errorf("error code = %q, want %q", got, "forbidden")
	}
}
