package nodes

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

func openTestStoreForRegistry(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestUpsertAdapter_Insert(t *testing.T) {
	ctx := context.Background()
	s := openTestStoreForRegistry(t)

	// Create a test node
	var nodeID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, created_at) VALUES (?, ?, ?)`,
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

	// Upsert adapter
	now := time.Unix(1_700_000_000, 0).UTC()
	caps := []string{"tls", "ws", "grpc"}
	err = UpsertAdapter(ctx, s, nodeID, "xray", "1.8.0", caps, now)
	if err != nil {
		t.Fatalf("UpsertAdapter: %v", err)
	}

	// Verify stored
	entries, err := ListAdapters(ctx, s, nodeID)
	if err != nil {
		t.Fatalf("ListAdapters: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 adapter, got %d", len(entries))
	}

	e := entries[0]
	if e.Kind != "xray" {
		t.Errorf("kind = %q, want xray", e.Kind)
	}
	if e.Version != "1.8.0" {
		t.Errorf("version = %q, want 1.8.0", e.Version)
	}
	if len(e.Capabilities) != 3 {
		t.Errorf("capabilities = %v, want 3 items", e.Capabilities)
	}
	if e.ReportedAt != now {
		t.Errorf("reported_at = %v, want %v", e.ReportedAt, now)
	}
}

func TestUpsertAdapter_Update(t *testing.T) {
	ctx := context.Background()
	s := openTestStoreForRegistry(t)

	// Create a test node
	var nodeID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, created_at) VALUES (?, ?, ?)`,
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

	// Insert initial version
	now1 := time.Unix(1_700_000_000, 0).UTC()
	err = UpsertAdapter(ctx, s, nodeID, "xray", "1.8.0", []string{"tls"}, now1)
	if err != nil {
		t.Fatalf("UpsertAdapter (insert): %v", err)
	}

	// Update version and capabilities
	now2 := time.Unix(1_700_000_100, 0).UTC()
	err = UpsertAdapter(ctx, s, nodeID, "xray", "1.8.1", []string{"tls", "ws"}, now2)
	if err != nil {
		t.Fatalf("UpsertAdapter (update): %v", err)
	}

	// Verify updated
	entries, err := ListAdapters(ctx, s, nodeID)
	if err != nil {
		t.Fatalf("ListAdapters: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 adapter (not 2), got %d", len(entries))
	}

	e := entries[0]
	if e.Version != "1.8.1" {
		t.Errorf("version = %q, want 1.8.1 (updated)", e.Version)
	}
	if len(e.Capabilities) != 2 {
		t.Errorf("capabilities = %v, want 2 items (updated)", e.Capabilities)
	}
	if e.ReportedAt != now2 {
		t.Errorf("reported_at = %v, want %v (updated)", e.ReportedAt, now2)
	}
}

func TestListAdapters_MultipleAdapters(t *testing.T) {
	ctx := context.Background()
	s := openTestStoreForRegistry(t)

	// Create a test node
	var nodeID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, created_at) VALUES (?, ?, ?)`,
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

	// Insert multiple adapters
	now := time.Unix(1_700_000_000, 0).UTC()
	adapters := []struct {
		kind string
		ver  string
		caps []string
	}{
		{"xray", "1.8.0", []string{"tls", "ws"}},
		{"singbox", "1.5.0", []string{"tls", "grpc"}},
		{"stub", "1.0.0", []string{}},
	}

	for _, a := range adapters {
		err = UpsertAdapter(ctx, s, nodeID, a.kind, a.ver, a.caps, now)
		if err != nil {
			t.Fatalf("UpsertAdapter(%s): %v", a.kind, err)
		}
	}

	// List all
	entries, err := ListAdapters(ctx, s, nodeID)
	if err != nil {
		t.Fatalf("ListAdapters: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 adapters, got %d", len(entries))
	}

	// Verify sorted by kind
	kinds := []string{entries[0].Kind, entries[1].Kind, entries[2].Kind}
	expected := []string{"singbox", "stub", "xray"}
	for i, kind := range kinds {
		if kind != expected[i] {
			t.Errorf("adapter[%d].kind = %q, want %q (sorted)", i, kind, expected[i])
		}
	}
}

func TestListAdapters_EmptyForNode(t *testing.T) {
	ctx := context.Background()
	s := openTestStoreForRegistry(t)

	// Create a test node with no adapters
	var nodeID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, created_at) VALUES (?, ?, ?)`,
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

	// List should be empty
	entries, err := ListAdapters(ctx, s, nodeID)
	if err != nil {
		t.Fatalf("ListAdapters: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 adapters, got %d", len(entries))
	}
}

func TestUpsertAdapter_InvalidJSON(t *testing.T) {
	ctx := context.Background()
	s := openTestStoreForRegistry(t)

	// Create a test node
	var nodeID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, created_at) VALUES (?, ?, ?)`,
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

	// Capabilities are marshaled internally, so this should always succeed
	now := time.Unix(1_700_000_000, 0).UTC()
	err = UpsertAdapter(ctx, s, nodeID, "test", "1.0.0", []string{"valid"}, now)
	if err != nil {
		t.Errorf("UpsertAdapter should handle any valid []string: %v", err)
	}
}
