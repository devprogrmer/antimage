package nodes

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

func TestRecordAndGetLatestMetrics(t *testing.T) {
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

	cpu := 45.5
	memUsed := int64(8 * 1024 * 1024 * 1024)     // 8GB
	memTotal := int64(16 * 1024 * 1024 * 1024)   // 16GB
	diskUsed := int64(100 * 1024 * 1024 * 1024)  // 100GB
	diskTotal := int64(500 * 1024 * 1024 * 1024) // 500GB
	rxBytes := int64(1024 * 1024 * 1024)         // 1GB
	txBytes := int64(2 * 1024 * 1024 * 1024)     // 2GB
	conns := 42
	latency := 50

	metrics := HealthMetrics{
		NodeID:            nodeID,
		Timestamp:         now,
		CPUPercent:        &cpu,
		MemoryUsedBytes:   &memUsed,
		MemoryTotalBytes:  &memTotal,
		DiskUsedBytes:     &diskUsed,
		DiskTotalBytes:    &diskTotal,
		NetworkRxBytes:    &rxBytes,
		NetworkTxBytes:    &txBytes,
		ActiveConnections: &conns,
		LatencyMS:         &latency,
	}

	// Record metrics
	if err := RecordMetrics(ctx, s, metrics); err != nil {
		t.Fatalf("RecordMetrics failed: %v", err)
	}

	// Retrieve latest
	retrieved, err := GetLatestMetrics(ctx, s, nodeID)
	if err != nil {
		t.Fatalf("GetLatestMetrics failed: %v", err)
	}

	if retrieved == nil {
		t.Fatal("GetLatestMetrics returned nil")
	}

	if retrieved.NodeID != nodeID {
		t.Errorf("NodeID = %d, want %d", retrieved.NodeID, nodeID)
	}

	if retrieved.CPUPercent == nil || *retrieved.CPUPercent != cpu {
		t.Errorf("CPUPercent = %v, want %f", retrieved.CPUPercent, cpu)
	}

	if retrieved.ActiveConnections == nil || *retrieved.ActiveConnections != conns {
		t.Errorf("ActiveConnections = %v, want %d", retrieved.ActiveConnections, conns)
	}
}

func TestGetMetricsHistory(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer s.Close()

	nodeID := int64(1)
	now := time.Now()

	// Create parent node
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

	// Record 5 metrics over time
	for i := 0; i < 5; i++ {
		ts := now.Add(-time.Duration(i) * time.Minute)
		cpu := float64(50 + i*5)
		metrics := HealthMetrics{
			NodeID:     nodeID,
			Timestamp:  ts,
			CPUPercent: &cpu,
		}
		if err := RecordMetrics(ctx, s, metrics); err != nil {
			t.Fatalf("RecordMetrics failed: %v", err)
		}
	}

	// Retrieve history
	from := now.Add(-10 * time.Minute)
	to := now
	history, err := GetMetricsHistory(ctx, s, nodeID, from, to, 10)
	if err != nil {
		t.Fatalf("GetMetricsHistory failed: %v", err)
	}

	if len(history) != 5 {
		t.Errorf("got %d metrics, want 5", len(history))
	}

	// Should be in descending timestamp order
	for i := 0; i < len(history)-1; i++ {
		if history[i].Timestamp.Before(history[i+1].Timestamp) {
			t.Errorf("metrics not in descending order: %v before %v", history[i].Timestamp, history[i+1].Timestamp)
		}
	}
}

func TestCalculateHealthStatus_Online(t *testing.T) {
	now := time.Now()
	lastSeen := now.Add(-30 * time.Second)

	cpu := 50.0
	memUsed := int64(8 * 1024 * 1024 * 1024)
	memTotal := int64(16 * 1024 * 1024 * 1024)
	latency := 100

	metrics := &HealthMetrics{
		CPUPercent:       &cpu,
		MemoryUsedBytes:  &memUsed,
		MemoryTotalBytes: &memTotal,
		LatencyMS:        &latency,
	}

	status := CalculateHealthStatus(metrics, &lastSeen, DefaultHealthThresholds())

	if status.Status != StatusOnline {
		t.Errorf("Status = %s, want %s", status.Status, StatusOnline)
	}

	if !status.CPUHealthy {
		t.Error("CPUHealthy = false, want true")
	}

	if !status.MemoryHealthy {
		t.Error("MemoryHealthy = false, want true")
	}

	if !status.LatencyHealthy {
		t.Error("LatencyHealthy = false, want true")
	}
}

func TestCalculateHealthStatus_Degraded_CPU(t *testing.T) {
	now := time.Now()
	lastSeen := now.Add(-30 * time.Second)

	cpu := 92.0 // Above critical threshold (90%)
	metrics := &HealthMetrics{
		CPUPercent: &cpu,
	}

	status := CalculateHealthStatus(metrics, &lastSeen, DefaultHealthThresholds())

	if status.Status != StatusDegraded {
		t.Errorf("Status = %s, want %s", status.Status, StatusDegraded)
	}

	if status.CPUHealthy {
		t.Error("CPUHealthy = true, want false")
	}

	if status.Message == "" {
		t.Error("Message is empty, expected CPU warning")
	}
}

func TestCalculateHealthStatus_Degraded_Memory(t *testing.T) {
	now := time.Now()
	lastSeen := now.Add(-30 * time.Second)

	memUsed := int64(15 * 1024 * 1024 * 1024)  // 15GB
	memTotal := int64(16 * 1024 * 1024 * 1024) // 16GB (93.75% usage)
	metrics := &HealthMetrics{
		MemoryUsedBytes:  &memUsed,
		MemoryTotalBytes: &memTotal,
	}

	status := CalculateHealthStatus(metrics, &lastSeen, DefaultHealthThresholds())

	if status.Status != StatusDegraded {
		t.Errorf("Status = %s, want %s", status.Status, StatusDegraded)
	}

	if status.MemoryHealthy {
		t.Error("MemoryHealthy = true, want false")
	}
}

func TestCalculateHealthStatus_Degraded_Latency(t *testing.T) {
	now := time.Now()
	lastSeen := now.Add(-30 * time.Second)

	latency := 2500 // Above critical threshold (2000ms)
	metrics := &HealthMetrics{
		LatencyMS: &latency,
	}

	status := CalculateHealthStatus(metrics, &lastSeen, DefaultHealthThresholds())

	if status.Status != StatusDegraded {
		t.Errorf("Status = %s, want %s", status.Status, StatusDegraded)
	}

	if status.LatencyHealthy {
		t.Error("LatencyHealthy = true, want false")
	}
}

func TestCalculateHealthStatus_Degraded_Heartbeat(t *testing.T) {
	now := time.Now()
	lastSeen := now.Add(-3 * time.Minute) // Beyond degraded threshold (2 min)

	status := CalculateHealthStatus(nil, &lastSeen, DefaultHealthThresholds())

	if status.Status != StatusDegraded {
		t.Errorf("Status = %s, want %s", status.Status, StatusDegraded)
	}

	if status.Message == "" {
		t.Error("Message is empty, expected heartbeat delay")
	}
}

func TestCalculateHealthStatus_Offline_NoHeartbeat(t *testing.T) {
	status := CalculateHealthStatus(nil, nil, DefaultHealthThresholds())

	if status.Status != StatusOffline {
		t.Errorf("Status = %s, want %s", status.Status, StatusOffline)
	}

	if status.Message != "no heartbeat received" {
		t.Errorf("Message = %q, want 'no heartbeat received'", status.Message)
	}
}

func TestCalculateHealthStatus_Offline_TimeoutExceeded(t *testing.T) {
	now := time.Now()
	lastSeen := now.Add(-10 * time.Minute) // Beyond offline threshold (5 min)

	status := CalculateHealthStatus(nil, &lastSeen, DefaultHealthThresholds())

	if status.Status != StatusOffline {
		t.Errorf("Status = %s, want %s", status.Status, StatusOffline)
	}
}

func TestRecordNodeEvent(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer s.Close()

	nodeID := int64(1)
	adminID := int64(42)

	// Create parent node
	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (id, name, address, status, created_at)
			VALUES (?, ?, ?, ?, ?)
		`, nodeID, "test-node", "10.0.0.1:8080", "online", time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("failed to create parent node: %v", err)
	}

	// Create role record (required by FK on admin.role_id)
	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO roles (id, name, permissions)
			VALUES (?, ?, ?)
		`, 1, "test_role", "[]")
		return err
	})
	if err != nil {
		t.Fatalf("failed to create role: %v", err)
	}

	// Create admin record (required by FK on node_events.admin_id)
	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO admins (id, username, password_hash, role_id, status, created_at)
			VALUES (?, ?, ?, ?, ?, ?)
		`, adminID, "test-admin", "hash", 1, "active", time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("failed to create admin: %v", err)
	}

	details := map[string]interface{}{
		"reason":   "test maintenance",
		"duration": "2h",
	}

	err = RecordNodeEvent(ctx, s, nodeID, "maintenance_enter", "info", details, &adminID)
	if err != nil {
		t.Fatalf("RecordNodeEvent failed: %v", err)
	}

	// Verify event was recorded
	var count int
	err = s.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM node_events WHERE node_id = ?`, nodeID).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query events: %v", err)
	}

	if count != 1 {
		t.Errorf("event count = %d, want 1", count)
	}
}

func TestPruneOldMetrics(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer s.Close()

	nodeID := int64(1)
	now := time.Now()

	// Create parent node
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

	// Record old metrics (31 days ago)
	oldMetrics := HealthMetrics{
		NodeID:    nodeID,
		Timestamp: now.AddDate(0, 0, -31),
	}
	if err := RecordMetrics(ctx, s, oldMetrics); err != nil {
		t.Fatalf("RecordMetrics failed: %v", err)
	}

	// Record recent metrics (1 day ago)
	recentMetrics := HealthMetrics{
		NodeID:    nodeID,
		Timestamp: now.AddDate(0, 0, -1),
	}
	if err := RecordMetrics(ctx, s, recentMetrics); err != nil {
		t.Fatalf("RecordMetrics failed: %v", err)
	}

	// Prune metrics older than 30 days
	deleted, err := PruneOldMetrics(ctx, s, 30)
	if err != nil {
		t.Fatalf("PruneOldMetrics failed: %v", err)
	}

	if deleted != 1 {
		t.Errorf("deleted %d rows, want 1", deleted)
	}

	// Verify only recent metrics remain
	var count int
	err = s.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM node_metrics WHERE node_id = ?`, nodeID).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query metrics: %v", err)
	}

	if count != 1 {
		t.Errorf("remaining metrics = %d, want 1", count)
	}
}
