package nodes

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/testutil/storetest"
)

func openTestStoreForRegistry(t *testing.T) *store.Store {
	t.Helper()
	s, err := storetest.OpenCopy(filepath.Join(t.TempDir(), "test.db"))
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
	err = UpsertAdapter(ctx, s, nodeID, AdapterInfo{Kind: "xray", Version: "1.8.0", Capabilities: caps}, now)
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
	err = UpsertAdapter(ctx, s, nodeID, AdapterInfo{Kind: "xray", Version: "1.8.0", Capabilities: []string{"tls"}}, now1)
	if err != nil {
		t.Fatalf("UpsertAdapter (insert): %v", err)
	}

	// Update version and capabilities
	now2 := time.Unix(1_700_000_100, 0).UTC()
	err = UpsertAdapter(ctx, s, nodeID, AdapterInfo{Kind: "xray", Version: "1.8.1", Capabilities: []string{"tls", "ws"}}, now2)
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
		err = UpsertAdapter(ctx, s, nodeID, AdapterInfo{Kind: a.kind, Version: a.ver, Capabilities: a.caps}, now)
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
	err = UpsertAdapter(ctx, s, nodeID, AdapterInfo{Kind: "test", Version: "1.0.0", Capabilities: []string{"valid"}}, now)
	if err != nil {
		t.Errorf("UpsertAdapter should handle any valid []string: %v", err)
	}
}

// seedNodeForRegistry creates a bare node row for tests that need a real
// node_id foreign key.
func seedNodeForRegistry(t *testing.T, s *store.Store) int64 {
	t.Helper()
	ctx := context.Background()
	var nodeID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, created_at) VALUES (?, ?, ?)`,
			"geo-test-node", "10.0.0.9", time.Now().Unix())
		if err != nil {
			return err
		}
		nodeID, _ = res.LastInsertId()
		return nil
	})
	if err != nil {
		t.Fatalf("seed node: %v", err)
	}
	return nodeID
}

// TestRecordGeoUpdate_StampsExistingRow proves the write path the HTTP
// handler depends on: an adapter that already reported in via Hello gets
// its geo columns filled in by a successful update.
func TestRecordGeoUpdate_StampsExistingRow(t *testing.T) {
	ctx := context.Background()
	s := openTestStoreForRegistry(t)
	nodeID := seedNodeForRegistry(t, s)

	if err := UpsertAdapter(ctx, s, nodeID, AdapterInfo{Kind: "xray", Version: "1.8.0"}, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatalf("seed adapter: %v", err)
	}

	at := time.Unix(1_700_001_000, 0).UTC()
	ok, err := RecordGeoUpdate(ctx, s, nodeID, "xray", "geoipsum", "geositesum", at)
	if err != nil {
		t.Fatalf("RecordGeoUpdate: %v", err)
	}
	if !ok {
		t.Fatal("RecordGeoUpdate reported no row updated, want true")
	}

	entries, err := ListAdapters(ctx, s, nodeID)
	if err != nil {
		t.Fatalf("ListAdapters: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d adapters, want 1", len(entries))
	}
	e := entries[0]
	if e.GeoUpdatedAt == nil || !e.GeoUpdatedAt.Equal(at) {
		t.Errorf("GeoUpdatedAt = %v, want %v", e.GeoUpdatedAt, at)
	}
	if e.GeoIPSHA256 != "geoipsum" || e.GeoSiteSHA256 != "geositesum" {
		t.Errorf("checksums = %q/%q, want geoipsum/geositesum", e.GeoIPSHA256, e.GeoSiteSHA256)
	}
}

// TestRecordGeoUpdate_NoMatchingRowReportsFalse proves the handler can
// distinguish "recorded" from "nothing to record it against" -- an adapter
// kind the node never reported via Hello has no row to stamp.
func TestRecordGeoUpdate_NoMatchingRowReportsFalse(t *testing.T) {
	ctx := context.Background()
	s := openTestStoreForRegistry(t)
	nodeID := seedNodeForRegistry(t, s)

	ok, err := RecordGeoUpdate(ctx, s, nodeID, "xray", "a", "b", time.Now())
	if err != nil {
		t.Fatalf("RecordGeoUpdate: %v", err)
	}
	if ok {
		t.Error("RecordGeoUpdate reported a row updated for a kind that was never registered")
	}
}

// TestUpsertAdapter_PreservesGeoColumnsAcrossHello is the invariant the
// migration's own comment promises: Hello re-upserting an adapter (every
// reconnect) must not wipe out geo tracking the panel recorded separately.
// UpsertAdapter's SET clause simply does not name the geo_* columns, so
// SQLite leaves them untouched -- this proves that holds, not just reads
// the source and assumes it.
func TestUpsertAdapter_PreservesGeoColumnsAcrossHello(t *testing.T) {
	ctx := context.Background()
	s := openTestStoreForRegistry(t)
	nodeID := seedNodeForRegistry(t, s)

	if err := UpsertAdapter(ctx, s, nodeID, AdapterInfo{Kind: "xray", Version: "1.8.0"}, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatalf("initial UpsertAdapter: %v", err)
	}
	at := time.Unix(1_700_001_000, 0).UTC()
	if _, err := RecordGeoUpdate(ctx, s, nodeID, "xray", "geoipsum", "geositesum", at); err != nil {
		t.Fatalf("RecordGeoUpdate: %v", err)
	}

	// Simulate the agent reconnecting and sending Hello again, with a new
	// version -- geo data on disk has not changed just because the process
	// restarted.
	if err := UpsertAdapter(ctx, s, nodeID, AdapterInfo{Kind: "xray", Version: "1.8.1"}, time.Unix(1_700_002_000, 0)); err != nil {
		t.Fatalf("second UpsertAdapter: %v", err)
	}

	entries, err := ListAdapters(ctx, s, nodeID)
	if err != nil {
		t.Fatalf("ListAdapters: %v", err)
	}
	e := entries[0]
	if e.Version != "1.8.1" {
		t.Errorf("version = %q, want 1.8.1 (Hello should still update this)", e.Version)
	}
	if e.GeoUpdatedAt == nil || !e.GeoUpdatedAt.Equal(at) {
		t.Errorf("GeoUpdatedAt = %v, want it preserved at %v across the Hello re-upsert", e.GeoUpdatedAt, at)
	}
	if e.GeoIPSHA256 != "geoipsum" {
		t.Errorf("GeoIPSHA256 = %q, want it preserved as geoipsum", e.GeoIPSHA256)
	}
}
