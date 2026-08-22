package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/store"
)

func TestHandleGetNodeHealthLatest(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	ctx := context.Background()

	// Create test node
	nodeID := int64(1)
	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (id, name, address, status, created_at, last_seen_at)
			VALUES (?, ?, ?, ?, ?, ?)
		`, nodeID, "test-node", "10.0.0.1:8080", "online", time.Now().Unix(), time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("failed to create node: %v", err)
	}

	// Record health metrics
	cpu := 45.5
	memUsed := int64(8 * 1024 * 1024 * 1024)
	memTotal := int64(16 * 1024 * 1024 * 1024)
	latency := 50
	metrics := nodes.HealthMetrics{
		NodeID:           nodeID,
		Timestamp:        time.Now(),
		CPUPercent:       &cpu,
		MemoryUsedBytes:  &memUsed,
		MemoryTotalBytes: &memTotal,
		LatencyMS:        &latency,
	}
	if err := nodes.RecordMetrics(ctx, s, metrics); err != nil {
		t.Fatalf("failed to record metrics: %v", err)
	}

	// Create dispatcher
	d := &dispatcher{
		store: s,
	}
	d.authorize = func(ctx context.Context, permission string) error {
		return nil // Allow all for test
	}

	// Create request
	req := httptest.NewRequest("GET", fmt.Sprintf("/api/v1/nodes/%d/health/latest", nodeID), nil)
	req.SetPathValue("id", fmt.Sprintf("%d", nodeID))
	w := httptest.NewRecorder()

	// Execute
	d.handleGetNodeHealthLatest(w, req)

	// Verify response
	if w.Code != http.StatusOK {
		t.Errorf("status code = %d, want %d", w.Code, http.StatusOK)
	}

	var response map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if response["status"] != "online" {
		t.Errorf("status = %v, want 'online'", response["status"])
	}

	metricsMap, ok := response["metrics"].(map[string]interface{})
	if !ok {
		t.Fatal("metrics field missing or wrong type")
	}

	if metricsMap["cpu_percent"] == nil {
		t.Error("cpu_percent is nil")
	}
}

func TestHandleGetNodeHealthHistory(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	ctx := context.Background()

	// Create test node
	nodeID := int64(1)
	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (id, name, address, status, created_at)
			VALUES (?, ?, ?, ?, ?)
		`, nodeID, "test-node", "10.0.0.1:8080", "online", time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("failed to create node: %v", err)
	}

	// Record multiple metrics
	now := time.Now()
	for i := 0; i < 3; i++ {
		cpu := float64(50 + i*10)
		metrics := nodes.HealthMetrics{
			NodeID:    nodeID,
			Timestamp: now.Add(-time.Duration(i) * time.Minute),
			CPUPercent: &cpu,
		}
		if err := nodes.RecordMetrics(ctx, s, metrics); err != nil {
			t.Fatalf("failed to record metrics: %v", err)
		}
	}

	// Create dispatcher
	d := &Dispatcher{
		store: s,
		authorizeFunc: func(ctx context.Context, permission string) error {
			return nil
		},
	}

	// Create request with query parameters
	from := now.Add(-1 * time.Hour).Unix()
	to := now.Unix()
	url := fmt.Sprintf("/api/v1/nodes/%d/health/history?from=%d&to=%d&limit=10", nodeID, from, to)
	req := httptest.NewRequest("GET", url, nil)
	req.SetPathValue("id", fmt.Sprintf("%d", nodeID))
	w := httptest.NewRecorder()

	// Execute
	d.handleGetNodeHealthHistory(w, req)

	// Verify response
	if w.Code != http.StatusOK {
		t.Errorf("status code = %d, want %d", w.Code, http.StatusOK)
	}

	var response map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	metricsArray, ok := response["metrics"].([]interface{})
	if !ok {
		t.Fatal("metrics field missing or wrong type")
	}

	if len(metricsArray) != 3 {
		t.Errorf("got %d metrics, want 3", len(metricsArray))
	}
}

func TestHandleGetNodeHealthLatest_Unauthorized(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	d := &Dispatcher{
		store: s,
		authorizeFunc: func(ctx context.Context, permission string) error {
			return fmt.Errorf("unauthorized")
		},
	}

	req := httptest.NewRequest("GET", "/api/v1/nodes/1/health/latest", nil)
	req.SetPathValue("id", "1")
	w := httptest.NewRecorder()

	d.handleGetNodeHealthLatest(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status code = %d, want %d", w.Code, http.StatusForbidden)
	}
}
