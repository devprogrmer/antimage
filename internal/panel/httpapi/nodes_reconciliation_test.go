package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/go-chi/chi/v5"
)

func TestHandleGetNodeReconciliation_Converged(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	ctx := context.Background()
	nodeID := int64(1)
	revision := int64(5)

	// Create node with matching desired/applied revisions
	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (id, name, address, status, created_at, desired_revision, applied_revision)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, nodeID, "test-node", "10.0.0.1:8080", "online", time.Now().Unix(), revision, revision)
		return err
	})
	if err != nil {
		t.Fatalf("failed to create node: %v", err)
	}

	// Create dispatcher
	d := Deps{Store: s}

	// Create request with chi context
	req := httptest.NewRequest("GET", "/api/v1/nodes/1/reconciliation", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	// Execute
	d.handleGetNodeReconciliation(w, req)

	// Verify response
	if w.Code != http.StatusOK {
		t.Errorf("status code = %d, want %d", w.Code, http.StatusOK)
	}

	var response map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if response["status"] != "converged" {
		t.Errorf("status = %v, want 'converged'", response["status"])
	}

	if response["drift_detected"] != false {
		t.Errorf("drift_detected = %v, want false", response["drift_detected"])
	}

	if response["needs_sync"] != false {
		t.Errorf("needs_sync = %v, want false", response["needs_sync"])
	}
}

func TestHandleGetNodeReconciliation_Pending(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	ctx := context.Background()
	nodeID := int64(1)

	// Create node with applied < desired
	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (id, name, address, status, created_at, desired_revision, applied_revision)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, nodeID, "test-node", "10.0.0.1:8080", "online", time.Now().Unix(), 10, 5)
		return err
	})
	if err != nil {
		t.Fatalf("failed to create node: %v", err)
	}

	d := Deps{Store: s}

	req := httptest.NewRequest("GET", "/api/v1/nodes/1/reconciliation", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	d.handleGetNodeReconciliation(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status code = %d, want %d", w.Code, http.StatusOK)
	}

	var response map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if response["status"] != "pending" {
		t.Errorf("status = %v, want 'pending'", response["status"])
	}

	if response["needs_sync"] != true {
		t.Errorf("needs_sync = %v, want true", response["needs_sync"])
	}
}

func TestHandleGetNodeReconciliation_WithApplyRuns(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	ctx := context.Background()
	nodeID := int64(1)

	// Create node
	err = s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (id, name, address, status, created_at, desired_revision, applied_revision)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, nodeID, "test-node", "10.0.0.1:8080", "online", time.Now().Unix(), 5, 5); err != nil {
			return err
		}

		// Create apply runs
		now := time.Now().Unix()
		_, err := tx.ExecContext(ctx, `
			INSERT INTO apply_runs (node_id, revision, outcome, started_at, finished_at)
			VALUES (?, ?, ?, ?, ?)
		`, nodeID, 5, "converged", now-300, now-290)
		return err
	})
	if err != nil {
		t.Fatalf("failed to create test data: %v", err)
	}

	d := Deps{Store: s}

	req := httptest.NewRequest("GET", "/api/v1/nodes/1/reconciliation", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()

	d.handleGetNodeReconciliation(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status code = %d, want %d", w.Code, http.StatusOK)
	}

	var response map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	runs, ok := response["recent_runs"].([]interface{})
	if !ok {
		t.Fatal("recent_runs field missing or wrong type")
	}

	if len(runs) == 0 {
		t.Error("expected at least one apply run")
	}
}
