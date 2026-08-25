package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/nodes"
)

func TestHandleListAdapters(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// Seed an admin and get session token.
	env.seedAdmin(t, "admin", "password", "super_admin")
	token := env.login(t, "admin", "password")

	// Seed a node.
	var nodeID int64
	err := env.store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (name, address, created_at)
			VALUES (?, ?, ?)`,
			"test-node", "test.example.com", time.Now().Unix())
		if err != nil {
			return err
		}
		nodeID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("seed node: %v", err)
	}

	// Seed adapters.
	now := time.Unix(1_700_000_000, 0).UTC()
	adapters := []struct {
		kind string
		ver  string
		caps []string
	}{
		{"xray", "1.8.0", []string{"tls", "ws"}},
		{"singbox", "1.5.0", []string{"tls", "grpc"}},
	}

	for _, a := range adapters {
		err = nodes.UpsertAdapter(ctx, env.store, nodeID,
			nodes.AdapterInfo{Kind: a.kind, Version: a.ver, Capabilities: a.caps}, now)
		if err != nil {
			t.Fatalf("upsert adapter %s: %v", a.kind, err)
		}
	}

	// List adapters.
	rec := env.get(t, "/api/v1/nodes/"+itoa64(nodeID)+"/adapters", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Adapters []AdapterJSON `json:"adapters"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if len(resp.Adapters) != 2 {
		t.Fatalf("expected 2 adapters, got %d", len(resp.Adapters))
	}

	// Verify sorted by kind (singbox, xray)
	if resp.Adapters[0].Kind != "singbox" {
		t.Errorf("adapters[0].kind = %q, want singbox", resp.Adapters[0].Kind)
	}
	if resp.Adapters[1].Kind != "xray" {
		t.Errorf("adapters[1].kind = %q, want xray", resp.Adapters[1].Kind)
	}

	// Verify xray details
	xray := resp.Adapters[1]
	if xray.Version != "1.8.0" {
		t.Errorf("xray.version = %q, want 1.8.0", xray.Version)
	}
	if len(xray.Capabilities) != 2 {
		t.Errorf("xray.capabilities = %v, want 2 items", xray.Capabilities)
	}
	if xray.ReportedAt != now.Unix() {
		t.Errorf("xray.reported_at = %d, want %d", xray.ReportedAt, now.Unix())
	}
}

func TestHandleListAdapters_EmptyList(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// Seed an admin and get session token.
	env.seedAdmin(t, "admin", "password", "super_admin")
	token := env.login(t, "admin", "password")

	// Seed a node with no adapters.
	var nodeID int64
	err := env.store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (name, address, created_at)
			VALUES (?, ?, ?)`,
			"test-node", "test.example.com", time.Now().Unix())
		if err != nil {
			return err
		}
		nodeID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("seed node: %v", err)
	}

	// List adapters should return empty array.
	rec := env.get(t, "/api/v1/nodes/"+itoa64(nodeID)+"/adapters", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Adapters []AdapterJSON `json:"adapters"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if len(resp.Adapters) != 0 {
		t.Errorf("expected 0 adapters, got %d", len(resp.Adapters))
	}
}

func TestHandleListAdapters_InvalidNodeID(t *testing.T) {
	env := newTestEnv(t)

	env.seedAdmin(t, "admin", "password", "super_admin")
	token := env.login(t, "admin", "password")

	rec := env.get(t, "/api/v1/nodes/invalid/adapters", token)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid node ID, got %d", rec.Code)
	}
}

func TestHandleListAdapters_RequiresAuth(t *testing.T) {
	env := newTestEnv(t)

	rec := env.get(t, "/api/v1/nodes/1/adapters", "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d", rec.Code)
	}
}
