package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/amyrm/antimage/internal/panel/control"
	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

func setupHealthTestDeps(t *testing.T) (Deps, *store.Store, *rbac.Actor) {
	t.Helper()

	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	ctx := context.Background()

	// Create test admin and role
	var adminID int64
	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO roles (id, name, permissions)
			VALUES (1, 'admin', '["node:read","node:write"]')
		`)
		if err != nil {
			return err
		}

		res, err := tx.ExecContext(ctx, `
			INSERT INTO admins (username, password_hash, role_id, created_at)
			VALUES ('testadmin', 'hash', 1, ?)
		`, time.Now().Unix())
		if err != nil {
			return err
		}

		adminID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("failed to create test admin: %v", err)
	}

	actor := &rbac.Actor{
		AdminID:  adminID,
		RoleName: "admin",
		IsSuper:  true, // Make super admin to have access to all nodes
		Perms: map[rbac.Permission]struct{}{
			rbac.PermNodeRead:  {},
			rbac.PermNodeWrite: {},
		},
		NodeIDs:    map[int64]struct{}{},
		ServiceIDs: map[int64]struct{}{},
	}

	deps := Deps{
		Store: s,
		Hub:   control.NewHub(),
		Now:   time.Now,
	}

	return deps, s, actor
}

func TestHandleGetNodeHealthLatest(t *testing.T) {
	deps, s, actor := setupHealthTestDeps(t)
	ctx := context.Background()

	// Create test node
	nodeID := int64(1)
	err := s.Write(ctx, func(tx *sql.Tx) error {
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

	// Create request
	req := httptest.NewRequest("GET", fmt.Sprintf("/api/v1/nodes/%d/health/latest", nodeID), nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxActor, actor))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", fmt.Sprintf("%d", nodeID))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()

	// Execute
	deps.handleGetNodeHealthLatest(w, req)

	// Verify response
	if w.Code != http.StatusOK {
		t.Errorf("status code = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
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
	deps, s, actor := setupHealthTestDeps(t)
	ctx := context.Background()

	// Create test node
	nodeID := int64(1)
	err := s.Write(ctx, func(tx *sql.Tx) error {
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
			NodeID:     nodeID,
			Timestamp:  now.Add(-time.Duration(i) * time.Minute),
			CPUPercent: &cpu,
		}
		if err := nodes.RecordMetrics(ctx, s, metrics); err != nil {
			t.Fatalf("failed to record metrics: %v", err)
		}
	}

	// Create request with query parameters
	from := now.Add(-1 * time.Hour).Unix()
	to := now.Unix()
	url := fmt.Sprintf("/api/v1/nodes/%d/health/history?from=%d&to=%d&limit=10", nodeID, from, to)
	req := httptest.NewRequest("GET", url, nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxActor, actor))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", fmt.Sprintf("%d", nodeID))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()

	// Execute
	deps.handleGetNodeHealthHistory(w, req)

	// Verify response
	if w.Code != http.StatusOK {
		t.Errorf("status code = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var results []map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&results); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(results) != 3 {
		t.Errorf("got %d metrics, want 3", len(results))
	}
}

func TestHandleGetNodeHealthLatest_Unauthorized(t *testing.T) {
	deps, s, _ := setupHealthTestDeps(t)
	ctx := context.Background()

	// Create test node
	nodeID := int64(1)
	err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (id, name, address, status, created_at)
			VALUES (?, ?, ?, ?, ?)
		`, nodeID, "test-node", "10.0.0.1:8080", "online", time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("failed to create node: %v", err)
	}

	// Actor without permissions
	actor := &rbac.Actor{
		AdminID:  2,
		RoleName: "readonly",
		IsSuper:  false,
		Perms:    map[rbac.Permission]struct{}{},
		NodeIDs:  map[int64]struct{}{},
	}

	req := httptest.NewRequest("GET", "/api/v1/nodes/1/health/latest", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxActor, actor))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()

	deps.handleGetNodeHealthLatest(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status code = %d, want %d", w.Code, http.StatusForbidden)
	}
}
