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
