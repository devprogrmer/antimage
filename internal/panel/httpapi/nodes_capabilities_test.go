package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/go-chi/chi/v5"
)

func TestHandleGetNodeCapabilities_Success(t *testing.T) {
	deps, s, actor := setupTestDeps(t)

	ctx := context.Background()
	nodeID := int64(1)
	now := time.Now()

	// Create node
	err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (id, name, address, status, created_at)
			VALUES (?, ?, ?, ?, ?)
		`, nodeID, "test-node", "10.0.0.1:8080", "online", now.Unix())
		return err
	})
	if err != nil {
		t.Fatalf("failed to create node: %v", err)
	}

	// Record capabilities
	xrayVersion := "1.8.4"
	capabilities := []nodes.NodeCapability{
		{NodeID: nodeID, Protocol: nodes.ProtocolXray, Available: true, Version: &xrayVersion, DetectedAt: now, LastCheckAt: now},
		{NodeID: nodeID, Protocol: nodes.ProtocolWireGuard, Available: true, DetectedAt: now, LastCheckAt: now},
		{NodeID: nodeID, Protocol: nodes.ProtocolHysteria2, Available: false, DetectedAt: now, LastCheckAt: now},
	}

	for _, cap := range capabilities {
		if err := nodes.RecordCapability(ctx, s, cap); err != nil {
			t.Fatalf("RecordCapability failed: %v", err)
		}
	}

	// Create request with authentication
	req := httptest.NewRequest("GET", "/api/v1/nodes/1/capabilities", nil)
	req = req.WithContext(withActor(req.Context(), actor))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	// Execute
	deps.handleGetNodeCapabilities(w, req)

	// Verify response
	if w.Code != http.StatusOK {
		t.Errorf("status code = %d, want %d", w.Code, http.StatusOK)
	}

	var response map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	caps, ok := response["capabilities"].([]interface{})
	if !ok {
		t.Fatal("capabilities field missing or wrong type")
	}

	if len(caps) != 3 {
		t.Errorf("got %d capabilities, want 3", len(caps))
	}

	// Verify Xray capability has version
	foundXray := false
	for _, c := range caps {
		capMap := c.(map[string]interface{})
		if capMap["protocol"] == "xray" {
			foundXray = true
			if capMap["available"] != true {
				t.Error("Xray should be available")
			}
			if capMap["version"] != xrayVersion {
				t.Errorf("Xray version = %v, want %s", capMap["version"], xrayVersion)
			}
		}
	}

	if !foundXray {
		t.Error("Xray capability not found in response")
	}
}

// A node that does not exist is 404 -- for a caller entitled to ask.
//
// This test used to construct Deps directly and call the handler with no actor
// in the context at all, which passed only because the handler had no
// authorization. It now supplies one, because "missing node" and "not allowed
// to look" are different answers and this test is about the first.
func TestHandleGetNodeCapabilities_NodeNotFound(t *testing.T) {
	deps, _, actor := setupTestDeps(t)

	req := httptest.NewRequest("GET", "/api/v1/nodes/999/capabilities", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "999")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	req = req.WithContext(withActor(req.Context(), actor))
	w := httptest.NewRecorder()

	deps.handleGetNodeCapabilities(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status code = %d, want %d", w.Code, http.StatusNotFound)
	}
}

// And an unauthenticated caller is refused rather than crashing the process.
//
// requirePermission reads the actor out of the request context, and a handler
// reached without one used to panic in the audit path -- recoverMiddleware
// would then report a refusal as a 500 and no denial record would be written.
func TestHandleGetNodeCapabilities_NoActorIsForbiddenNotAPanic(t *testing.T) {
	deps, _, _ := setupTestDeps(t)

	req := httptest.NewRequest("GET", "/api/v1/nodes/999/capabilities", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "999")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	deps.handleGetNodeCapabilities(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status code = %d, want %d for a request carrying no actor",
			w.Code, http.StatusForbidden)
	}
}

func TestHandleGetNodeCapabilities_EmptyCapabilities(t *testing.T) {
	deps, s, actor := setupTestDeps(t)

	ctx := context.Background()
	nodeID := int64(1)

	// Create node but no capabilities
	err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (id, name, address, status, created_at)
			VALUES (?, ?, ?, ?, ?)
		`, nodeID, "test-node", "10.0.0.1:8080", "pending", time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("failed to create node: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/nodes/1/capabilities", nil)
	req = req.WithContext(withActor(req.Context(), actor))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	deps.handleGetNodeCapabilities(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status code = %d, want %d", w.Code, http.StatusOK)
	}

	var response map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	caps, ok := response["capabilities"].([]interface{})
	if !ok {
		t.Fatal("capabilities field missing or wrong type")
	}

	if len(caps) != 0 {
		t.Errorf("got %d capabilities, want 0", len(caps))
	}
}
