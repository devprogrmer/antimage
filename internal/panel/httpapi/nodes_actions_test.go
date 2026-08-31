package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/amyrm/antimage/internal/panel/control"
	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
	pb "github.com/amyrm/antimage/internal/shared/proto/antimage/v1"
	"github.com/amyrm/antimage/internal/testutil/storetest"
)

func setupTestDeps(t *testing.T) (Deps, *store.Store, *rbac.Actor) {
	t.Helper()

	s, err := storetest.OpenCopy(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Create test admin and role
	ctx := context.Background()
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
		Now:   time.Now,
	}

	return deps, s, actor
}

func withActor(ctx context.Context, actor *rbac.Actor) context.Context {
	return context.WithValue(ctx, ctxActor, actor)
}

func createTestNode(t *testing.T, s *store.Store, nodeID int64, name, status string) {
	t.Helper()

	ctx := context.Background()
	err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (id, name, address, status, created_at)
			VALUES (?, ?, ?, ?, ?)
		`, nodeID, name, "10.0.0.1:8080", status, time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("failed to create test node: %v", err)
	}
}

func TestHandleRestartNode_Success(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	nodeID := int64(100)
	createTestNode(t, s, nodeID, "test-node", "online")

	req := httptest.NewRequest("POST", "/api/v1/nodes/100/restart", nil)
	req = req.WithContext(withActor(req.Context(), actor))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "100")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	deps.handleRestartNode(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
		t.Logf("body: %s", w.Body.String())
	}

	var response map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if response["action"] != "restart" {
		t.Errorf("action = %v, want restart", response["action"])
	}

	// Verify event was recorded
	ctx := context.Background()
	var count int
	err := s.Read().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM node_events
		WHERE node_id = ? AND event_type = 'restart_requested'
	`, nodeID).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query events: %v", err)
	}
	if count != 1 {
		t.Errorf("event count = %d, want 1", count)
	}

	// Verify audit log
	err = s.Read().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM audit_log
		WHERE action = 'node.restart' AND target_id = ?
	`, nodeID).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query audit log: %v", err)
	}
	if count != 1 {
		t.Errorf("audit count = %d, want 1", count)
	}

	// No agent registered on the hub for this node (deps.Hub is nil here,
	// matching every other test in this file that does not opt in), so
	// delivery must be reported honestly as false -- the previous
	// implementation had no way to be wrong about this because it never
	// checked; this is the regression guard for that gap re-opening.
	if response["delivered"] != false {
		t.Errorf("delivered = %v, want false (no agent connected)", response["delivered"])
	}
}

// TestHandleRestartNode_DeliversAndReportsRealOutcomes proves the fix: a
// registered agent receives an actual RestartAdapters command over the hub,
// and the HTTP response carries the per-adapter result the agent sent back
// -- not a canned "requested" string. This is the same shape as
// TestSendCommand_DeliversAndWaitsForResult in control/hub_test.go, but
// exercised through the real HTTP handler rather than the hub directly.
func TestHandleRestartNode_DeliversAndReportsRealOutcomes(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	deps.Hub = control.NewHub()
	nodeID := int64(105)
	createTestNode(t, s, nodeID, "connected-node", "online")

	_, cmds, release := deps.Hub.Register(nodeID)
	defer release()

	// Simulates the agent side: receive the command, reply as if xray
	// restarted cleanly and wireguard reported ErrRestartUnsupported.
	go func() {
		cmd := <-cmds
		deps.Hub.DeliverResult(&pb.AgentCommandResult{
			CommandId: cmd.CommandId,
			Body: &pb.AgentCommandResult_RestartAdapters{
				RestartAdapters: &pb.RestartAdaptersResult{
					Outcomes: []*pb.AdapterRestartOutcome{
						{Kind: "xray", Ok: true},
						{Kind: "wireguard", Ok: false, Error: "restart not supported by this adapter"},
					},
				},
			},
		})
	}()

	req := httptest.NewRequest("POST", "/api/v1/nodes/105/restart", nil)
	req = req.WithContext(withActor(req.Context(), actor))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "105")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	deps.handleRestartNode(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Delivered bool `json:"delivered"`
		Outcomes  []struct {
			Kind  string `json:"kind"`
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"outcomes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if !response.Delivered {
		t.Fatal("delivered = false, want true (agent registered and replied)")
	}
	if len(response.Outcomes) != 2 {
		t.Fatalf("got %d outcomes, want 2", len(response.Outcomes))
	}
	byKind := map[string]bool{}
	for _, o := range response.Outcomes {
		byKind[o.Kind] = o.OK
	}
	if !byKind["xray"] {
		t.Error("xray outcome not reported OK")
	}
	if byKind["wireguard"] {
		t.Error("wireguard outcome reported OK; it should have failed with 'not supported'")
	}
}

func TestHandleRestartNode_NotFound(t *testing.T) {
	deps, _, actor := setupTestDeps(t)

	req := httptest.NewRequest("POST", "/api/v1/nodes/999/restart", nil)
	req = req.WithContext(withActor(req.Context(), actor))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "999")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	deps.handleRestartNode(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleRestartNode_Unauthorized(t *testing.T) {
	deps, s, _ := setupTestDeps(t)
	nodeID := int64(100)
	createTestNode(t, s, nodeID, "test-node", "online")

	// Actor without node:write permission
	actor := &rbac.Actor{
		AdminID:  1,
		RoleName: "readonly",
		IsSuper:  false,
		Perms: map[rbac.Permission]struct{}{
			rbac.PermNodeRead: {},
		},
		NodeIDs:    map[int64]struct{}{},
		ServiceIDs: map[int64]struct{}{},
	}

	req := httptest.NewRequest("POST", "/api/v1/nodes/100/restart", nil)
	req = req.WithContext(withActor(req.Context(), actor))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "100")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	deps.handleRestartNode(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestHandleSyncNode_Success(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	nodeID := int64(101)
	createTestNode(t, s, nodeID, "sync-node", "online")

	req := httptest.NewRequest("POST", "/api/v1/nodes/101/sync", nil)
	req = req.WithContext(withActor(req.Context(), actor))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "101")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	deps.handleSyncNode(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
		t.Logf("body: %s", w.Body.String())
	}

	var response map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if response["action"] != "sync" {
		t.Errorf("action = %v, want sync", response["action"])
	}

	// Verify event was recorded
	ctx := context.Background()
	var count int
	err := s.Read().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM node_events
		WHERE node_id = ? AND event_type = 'sync_requested'
	`, nodeID).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query events: %v", err)
	}
	if count != 1 {
		t.Errorf("event count = %d, want 1", count)
	}

	// No agent is registered on the hub for this node, so delivery must be
	// reported honestly as false -- not as the "requested" success the
	// previous implementation always returned regardless of whether
	// anything was actually listening.
	if response["delivered"] != false {
		t.Errorf("delivered = %v, want false (no agent connected)", response["delivered"])
	}
}

// TestHandleSyncNode_DeliversToConnectedAgent is the fix itself: the handler
// used to record an audit row and tell the operator "sync request recorded,
// node will apply latest configuration" without ever calling Hub.Notify, so
// a connected agent never actually received anything. This proves the
// revision now reaches the hub's channel for a node that IS connected.
func TestHandleSyncNode_DeliversToConnectedAgent(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	deps.Hub = control.NewHub()
	nodeID := int64(102)
	createTestNode(t, s, nodeID, "connected-node", "online")
	if err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE nodes SET desired_revision = 7 WHERE id = ?`, nodeID)
		return err
	}); err != nil {
		t.Fatalf("seed desired_revision: %v", err)
	}

	bumps, _, release := deps.Hub.Register(nodeID)
	defer release()

	req := httptest.NewRequest("POST", "/api/v1/nodes/102/sync", nil)
	req = req.WithContext(withActor(req.Context(), actor))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "102")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	deps.handleSyncNode(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var response map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if response["delivered"] != true {
		t.Errorf("delivered = %v, want true (agent registered on hub)", response["delivered"])
	}

	select {
	case rev := <-bumps:
		if rev != 7 {
			t.Errorf("bumped revision = %d, want 7 (the node's desired_revision)", rev)
		}
	default:
		t.Fatal("handler reported delivered=true but nothing arrived on the hub channel")
	}
}

func TestHandleSetNodeMaintenance_Enable(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	nodeID := int64(102)
	createTestNode(t, s, nodeID, "maint-node", "online")

	reqBody := map[string]interface{}{
		"enable": true,
		"reason": "scheduled maintenance",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/api/v1/nodes/102/maintenance", bytes.NewReader(body))
	req = req.WithContext(withActor(req.Context(), actor))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "102")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	deps.handleSetNodeMaintenance(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
		t.Logf("body: %s", w.Body.String())
	}

	// Verify maintenance mode was set
	ctx := context.Background()
	var maintenanceMode int
	var maintenanceReason string
	var status string
	err := s.Read().QueryRowContext(ctx, `
		SELECT maintenance_mode, maintenance_reason, status
		FROM nodes WHERE id = ?
	`, nodeID).Scan(&maintenanceMode, &maintenanceReason, &status)
	if err != nil {
		t.Fatalf("failed to query node: %v", err)
	}

	if maintenanceMode != 1 {
		t.Errorf("maintenance_mode = %d, want 1", maintenanceMode)
	}
	if maintenanceReason != "scheduled maintenance" {
		t.Errorf("maintenance_reason = %q, want %q", maintenanceReason, "scheduled maintenance")
	}
	if status != "maintenance" {
		t.Errorf("status = %s, want maintenance", status)
	}

	// Verify event was recorded
	var count int
	err = s.Read().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM node_events
		WHERE node_id = ? AND event_type = 'maintenance_enter'
	`, nodeID).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query events: %v", err)
	}
	if count != 1 {
		t.Errorf("event count = %d, want 1", count)
	}
}

func TestHandleSetNodeMaintenance_Disable(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	nodeID := int64(103)

	// Create node in maintenance mode
	ctx := context.Background()
	err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO nodes (id, name, address, status, maintenance_mode, maintenance_reason, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, nodeID, "maint-node", "10.0.0.1:8080", "maintenance", 1, "test", time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("failed to create test node: %v", err)
	}

	reqBody := map[string]interface{}{
		"enable": false,
		"reason": "",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/api/v1/nodes/103/maintenance", bytes.NewReader(body))
	req = req.WithContext(withActor(req.Context(), actor))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "103")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	deps.handleSetNodeMaintenance(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
		t.Logf("body: %s", w.Body.String())
	}

	// Verify maintenance mode was disabled
	var maintenanceMode int
	var status string
	err = s.Read().QueryRowContext(ctx, `
		SELECT maintenance_mode, status FROM nodes WHERE id = ?
	`, nodeID).Scan(&maintenanceMode, &status)
	if err != nil {
		t.Fatalf("failed to query node: %v", err)
	}

	if maintenanceMode != 0 {
		t.Errorf("maintenance_mode = %d, want 0", maintenanceMode)
	}
	if status != "online" {
		t.Errorf("status = %s, want online", status)
	}
}

func TestHandleEnableNode_Success(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	nodeID := int64(104)
	createTestNode(t, s, nodeID, "disabled-node", "disabled")

	req := httptest.NewRequest("POST", "/api/v1/nodes/104/enable", nil)
	req = req.WithContext(withActor(req.Context(), actor))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "104")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	deps.handleEnableNode(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
		t.Logf("body: %s", w.Body.String())
	}

	// Verify status changed to pending
	ctx := context.Background()
	var status string
	err := s.Read().QueryRowContext(ctx, `
		SELECT status FROM nodes WHERE id = ?
	`, nodeID).Scan(&status)
	if err != nil {
		t.Fatalf("failed to query node: %v", err)
	}

	if status != "pending" {
		t.Errorf("status = %s, want pending", status)
	}

	// Verify event was recorded
	var count int
	err = s.Read().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM node_events
		WHERE node_id = ? AND event_type = 'node_enabled'
	`, nodeID).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query events: %v", err)
	}
	if count != 1 {
		t.Errorf("event count = %d, want 1", count)
	}
}

func TestHandleEnableNode_AlreadyEnabled(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	nodeID := int64(105)
	createTestNode(t, s, nodeID, "online-node", "online")

	req := httptest.NewRequest("POST", "/api/v1/nodes/105/enable", nil)
	req = req.WithContext(withActor(req.Context(), actor))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "105")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	deps.handleEnableNode(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
}

func TestHandleDisableNode_Success(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	nodeID := int64(106)
	createTestNode(t, s, nodeID, "active-node", "online")

	reqBody := map[string]interface{}{
		"reason": "security concern",
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/api/v1/nodes/106/disable", bytes.NewReader(body))
	req = req.WithContext(withActor(req.Context(), actor))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "106")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	deps.handleDisableNode(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
		t.Logf("body: %s", w.Body.String())
	}

	// Verify status changed to disabled
	ctx := context.Background()
	var status string
	err := s.Read().QueryRowContext(ctx, `
		SELECT status FROM nodes WHERE id = ?
	`, nodeID).Scan(&status)
	if err != nil {
		t.Fatalf("failed to query node: %v", err)
	}

	if status != "disabled" {
		t.Errorf("status = %s, want disabled", status)
	}

	// Verify event was recorded with reason
	var eventDetails string
	err = s.Read().QueryRowContext(ctx, `
		SELECT details FROM node_events
		WHERE node_id = ? AND event_type = 'node_disabled'
	`, nodeID).Scan(&eventDetails)
	if err != nil {
		t.Fatalf("failed to query events: %v", err)
	}

	var details map[string]interface{}
	if err := json.Unmarshal([]byte(eventDetails), &details); err != nil {
		t.Fatalf("failed to parse event details: %v", err)
	}

	if details["reason"] != "security concern" {
		t.Errorf("event reason = %v, want security concern", details["reason"])
	}
}

func TestNodeActions_CrossTenantAccessDenied(t *testing.T) {
	deps, s, _ := setupTestDeps(t)
	nodeID := int64(200)
	createTestNode(t, s, nodeID, "tenant-node", "online")

	// Actor with scoped access to different nodes
	actor := &rbac.Actor{
		AdminID:  2,
		RoleName: "reseller",
		IsSuper:  false,
		Perms: map[rbac.Permission]struct{}{
			rbac.PermNodeRead:  {},
			rbac.PermNodeWrite: {},
		},
		NodeIDs:    map[int64]struct{}{100: {}}, // Access to node 100 only
		ServiceIDs: map[int64]struct{}{},
	}

	req := httptest.NewRequest("POST", "/api/v1/nodes/200/restart", nil)
	req = req.WithContext(withActor(req.Context(), actor))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "200")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	deps.handleRestartNode(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (cross-tenant access should be denied)", w.Code)
	}

	// Verify no event was recorded
	ctx := context.Background()
	var count int
	err := s.Read().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM node_events WHERE node_id = ?
	`, nodeID).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query events: %v", err)
	}
	if count != 0 {
		t.Errorf("event count = %d, want 0 (no event should be recorded for denied access)", count)
	}
}

func TestNodeActions_AuditLogIntegrity(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	nodeID := int64(107)
	createTestNode(t, s, nodeID, "audit-node", "online")

	req := httptest.NewRequest("POST", "/api/v1/nodes/107/restart", nil)
	req = req.WithContext(withActor(req.Context(), actor))
	req = req.WithContext(context.WithValue(req.Context(), ctxRequestID, "test-request-id"))

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "107")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	deps.handleRestartNode(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	// Verify audit log has correct fields
	ctx := context.Background()
	var action, result string
	var targetID int64
	err := s.Read().QueryRowContext(ctx, `
		SELECT action, result, target_id
		FROM audit_log
		WHERE action = 'node.restart' AND target_id = ?
	`, nodeID).Scan(&action, &result, &targetID)
	if err != nil {
		t.Fatalf("failed to query audit log: %v", err)
	}

	if action != "node.restart" {
		t.Errorf("action = %s, want node.restart", action)
	}
	if result != "ok" {
		t.Errorf("result = %s, want ok", result)
	}
	if targetID != nodeID {
		t.Errorf("target_id = %d, want %d", targetID, nodeID)
	}
}
