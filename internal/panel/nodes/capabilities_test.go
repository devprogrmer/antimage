package nodes

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

func TestRecordAndGetCapabilities(t *testing.T) {
	ctx := context.Background()
	// Use temp file instead of :memory: to ensure migrations run properly
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer s.Close()

	nodeID := int64(1)
	now := time.Now()

	// Create parent node record (required by foreign key)
	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (id, name, address, status, created_at)
			VALUES (?, ?, ?, ?, ?)
		`, nodeID, "test-node", "10.0.0.1:8080", "online", now.Unix())
		return err
	})
	if err != nil {
		t.Fatalf("failed to create parent node: %v", err)
	}

	// Record capabilities for different protocols
	xrayVersion := "1.8.4"
	capabilities := []NodeCapability{
		{
			NodeID:     nodeID,
			Protocol:   ProtocolXray,
			Available:  true,
			Version:    &xrayVersion,
			DetectedAt: now,
			LastCheckAt: now,
		},
		{
			NodeID:     nodeID,
			Protocol:   ProtocolWireGuard,
			Available:  true,
			Version:    nil,
			DetectedAt: now,
			LastCheckAt: now,
		},
		{
			NodeID:     nodeID,
			Protocol:   ProtocolHysteria2,
			Available:  false,
			Version:    nil,
			DetectedAt: now,
			LastCheckAt: now,
		},
	}

	for _, cap := range capabilities {
		if err := RecordCapability(ctx, s, cap); err != nil {
			t.Fatalf("RecordCapability failed: %v", err)
		}
	}

	// Retrieve all capabilities
	retrieved, err := GetNodeCapabilities(ctx, s, nodeID)
	if err != nil {
		t.Fatalf("GetNodeCapabilities failed: %v", err)
	}

	if len(retrieved) != 3 {
		t.Errorf("got %d capabilities, want 3", len(retrieved))
	}

	// Verify Xray capability
	var xrayCap *NodeCapability
	for i := range retrieved {
		if retrieved[i].Protocol == ProtocolXray {
			xrayCap = &retrieved[i]
			break
		}
	}

	if xrayCap == nil {
		t.Fatal("Xray capability not found")
	}

	if !xrayCap.Available {
		t.Error("Xray should be available")
	}

	if xrayCap.Version == nil || *xrayCap.Version != xrayVersion {
		t.Errorf("Xray version = %v, want %s", xrayCap.Version, xrayVersion)
	}
}

func TestGetAvailableProtocols(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer s.Close()

	nodeID := int64(1)
	now := time.Now()

	// Create parent node record
	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (id, name, address, status, created_at)
			VALUES (?, ?, ?, ?, ?)
		`, nodeID, "test-node", "10.0.0.1:8080", "online", now.Unix())
		return err
	})
	if err != nil {
		t.Fatalf("failed to create parent node: %v", err)
	}

	// Record mixed available/unavailable protocols
	capabilities := []NodeCapability{
		{NodeID: nodeID, Protocol: ProtocolXray, Available: true, DetectedAt: now, LastCheckAt: now},
		{NodeID: nodeID, Protocol: ProtocolSingbox, Available: false, DetectedAt: now, LastCheckAt: now},
		{NodeID: nodeID, Protocol: ProtocolWireGuard, Available: true, DetectedAt: now, LastCheckAt: now},
		{NodeID: nodeID, Protocol: ProtocolHysteria2, Available: false, DetectedAt: now, LastCheckAt: now},
	}

	for _, cap := range capabilities {
		if err := RecordCapability(ctx, s, cap); err != nil {
			t.Fatalf("RecordCapability failed: %v", err)
		}
	}

	// Get only available protocols
	available, err := GetAvailableProtocols(ctx, s, nodeID)
	if err != nil {
		t.Fatalf("GetAvailableProtocols failed: %v", err)
	}

	if len(available) != 2 {
		t.Errorf("got %d available protocols, want 2", len(available))
	}

	// Verify correct protocols are returned
	expectedProtocols := map[Protocol]bool{
		ProtocolXray:      true,
		ProtocolWireGuard: true,
	}

	for _, p := range available {
		if !expectedProtocols[p] {
			t.Errorf("unexpected available protocol: %s", p)
		}
	}
}

func TestCapabilityUpdate(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer s.Close()

	nodeID := int64(1)
	now := time.Now()

	// Create parent node record
	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (id, name, address, status, created_at)
			VALUES (?, ?, ?, ?, ?)
		`, nodeID, "test-node", "10.0.0.1:8080", "online", now.Unix())
		return err
	})
	if err != nil {
		t.Fatalf("failed to create parent node: %v", err)
	}

	// Initial capability: Xray available
	v1 := "1.8.0"
	cap := NodeCapability{
		NodeID:     nodeID,
		Protocol:   ProtocolXray,
		Available:  true,
		Version:    &v1,
		DetectedAt: now,
		LastCheckAt: now,
	}

	if err := RecordCapability(ctx, s, cap); err != nil {
		t.Fatalf("initial RecordCapability failed: %v", err)
	}

	// Update: Xray unavailable (service stopped)
	later := now.Add(1 * time.Hour)
	cap.Available = false
	cap.LastCheckAt = later

	if err := RecordCapability(ctx, s, cap); err != nil {
		t.Fatalf("update RecordCapability failed: %v", err)
	}

	// Verify update
	retrieved, err := GetNodeCapabilities(ctx, s, nodeID)
	if err != nil {
		t.Fatalf("GetNodeCapabilities failed: %v", err)
	}

	if len(retrieved) != 1 {
		t.Fatalf("got %d capabilities, want 1", len(retrieved))
	}

	if retrieved[0].Available {
		t.Error("Xray should now be unavailable")
	}

	if !retrieved[0].LastCheckAt.Equal(later) {
		t.Errorf("LastCheckAt = %v, want %v", retrieved[0].LastCheckAt, later)
	}
}

// Compare timestamps within 1 second tolerance due to SQLite precision
	diff := retrieved[0].LastCheckAt.Sub(later)
	if diff < -time.Second || diff > time.Second {
		t.Errorf("LastCheckAt = %v, want %v (within 1s)", retrieved[0].LastCheckAt, later)
	}
}

func TestCapabilitiesForNonexistentNode(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer s.Close()

	capabilities, err := GetNodeCapabilities(ctx, s, 999)
	if err != nil {
		t.Fatalf("GetNodeCapabilities failed: %v", err)
	}

	if len(capabilities) != 0 {
		t.Errorf("got %d capabilities for nonexistent node, want 0", len(capabilities))
	}
}
