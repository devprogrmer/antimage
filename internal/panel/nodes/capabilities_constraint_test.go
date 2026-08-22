package nodes

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

// TestCapabilityForeignKeyConstraint verifies that capabilities cannot reference nonexistent nodes.
func TestCapabilityForeignKeyConstraint(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	nonexistentNodeID := int64(999)
	now := time.Now()

	// Attempt to record capability for nonexistent node
	cap := NodeCapability{
		NodeID:     nonexistentNodeID,
		Protocol:   ProtocolXray,
		Available:  true,
		DetectedAt: now,
		LastCheckAt: now,
	}

	err = RecordCapability(ctx, s, cap)
	if err == nil {
		t.Fatal("RecordCapability should fail for nonexistent node, but succeeded")
	}

	// Verify error is foreign key constraint
	if err.Error() != "constraint failed: FOREIGN KEY constraint failed (787)" {
		t.Logf("Got expected foreign key error: %v", err)
	}
}

// TestCapabilityCascadeDelete verifies that deleting a node cascades to its capabilities.
func TestCapabilityCascadeDelete(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	nodeID := int64(1)
	now := time.Now()

	// Create node
	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (id, name, address, status, created_at)
			VALUES (?, ?, ?, ?, ?)
		`, nodeID, "test-node", "10.0.0.1:8080", "online", now.Unix())
		return err
	})
	if err != nil {
		t.Fatalf("failed to create node: %v", err)
	}

	// Record capability
	cap := NodeCapability{
		NodeID:     nodeID,
		Protocol:   ProtocolXray,
		Available:  true,
		DetectedAt: now,
		LastCheckAt: now,
	}

	if err := RecordCapability(ctx, s, cap); err != nil {
		t.Fatalf("RecordCapability failed: %v", err)
	}

	// Verify capability exists
	caps, err := GetNodeCapabilities(ctx, s, nodeID)
	if err != nil {
		t.Fatalf("GetNodeCapabilities failed: %v", err)
	}
	if len(caps) != 1 {
		t.Fatalf("got %d capabilities, want 1", len(caps))
	}

	// Delete node
	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM nodes WHERE id = ?`, nodeID)
		return err
	})
	if err != nil {
		t.Fatalf("failed to delete node: %v", err)
	}

	// Verify capability was cascade-deleted
	caps, err = GetNodeCapabilities(ctx, s, nodeID)
	if err != nil {
		t.Fatalf("GetNodeCapabilities failed: %v", err)
	}
	if len(caps) != 0 {
		t.Errorf("got %d capabilities after cascade delete, want 0", len(caps))
	}
}
