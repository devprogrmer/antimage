package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/amyrm/antimage/internal/panel/control"
	pb "github.com/amyrm/antimage/internal/shared/proto/antimage/v1"
)

func TestHandleUpgradeNodeCore_DeliversAndRecordsSuccess(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	deps.Hub = control.NewHub()
	deps.Now = func() time.Time { return time.Unix(1700000000, 0) }
	nodeID := int64(301)
	createTestNode(t, s, nodeID, "core-node", "online")
	seedAdapterRow(t, s, nodeID, "xray")

	_, cmds, release := deps.Hub.Register(nodeID)
	defer release()

	var sent *pb.UpgradeCore
	go func() {
		cmd := <-cmds
		sent = cmd.GetUpgradeCore()
		deps.Hub.DeliverResult(&pb.AgentCommandResult{
			CommandId: cmd.CommandId,
			Body: &pb.AgentCommandResult_UpgradeCore{
				UpgradeCore: &pb.UpgradeCoreResult{
					Kind: "xray", Ok: true, InstalledVersion: "1.9.0",
				},
			},
		})
	}()

	body := `{"kind":"xray","binary_url":"https://example.com/Xray-linux-64.zip","binary_sha256":"aaaa","expected_version":"1.9.0"}`
	req := httptest.NewRequest("POST", "/api/v1/nodes/301/core-upgrade", strings.NewReader(body))
	req = req.WithContext(withActor(req.Context(), actor))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "301")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	deps.handleUpgradeNodeCore(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if sent == nil || sent.Kind != "xray" || sent.BinaryUrl != "https://example.com/Xray-linux-64.zip" {
		t.Fatalf("command not sent correctly: %+v", sent)
	}

	var response struct {
		Delivered        bool   `json:"delivered"`
		OK               bool   `json:"ok"`
		InstalledVersion string `json:"installed_version"`
		RolledBack       bool   `json:"rolled_back"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if !response.Delivered || !response.OK {
		t.Fatalf("response = %+v, want delivered+ok", response)
	}
	if response.InstalledVersion != "1.9.0" {
		t.Errorf("InstalledVersion = %q, want 1.9.0", response.InstalledVersion)
	}
	if response.RolledBack {
		t.Error("RolledBack = true on a success path")
	}

	// The database must reflect the new version -- this is the ONE path
	// allowed to overwrite adapter_registry.version outside of Hello, and
	// the test proves it actually does, not just that the handler claims to.
	var version string
	var coreUpgradedAt sql.NullInt64
	if err := s.Read().QueryRowContext(context.Background(),
		`SELECT version, core_upgraded_at FROM adapter_registry WHERE node_id = ? AND kind = 'xray'`, nodeID,
	).Scan(&version, &coreUpgradedAt); err != nil {
		t.Fatalf("read adapter_registry: %v", err)
	}
	if version != "1.9.0" {
		t.Errorf("stored version = %q, want 1.9.0", version)
	}
	if !coreUpgradedAt.Valid || coreUpgradedAt.Int64 != 1700000000 {
		t.Errorf("core_upgraded_at = %+v, want 1700000000", coreUpgradedAt)
	}
}

// TestHandleUpgradeNodeCore_RolledBackIsReportedNotHiddenAsFailure proves
// the response distinguishes "failed and rolled back" (node still working,
// just not upgraded) from a bare failure -- an operator reading "ok=false"
// alone cannot tell whether the node is now broken or merely unchanged.
func TestHandleUpgradeNodeCore_RolledBackIsReportedNotHiddenAsFailure(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	deps.Hub = control.NewHub()
	nodeID := int64(302)
	createTestNode(t, s, nodeID, "core-node-2", "online")

	_, cmds, release := deps.Hub.Register(nodeID)
	defer release()

	go func() {
		cmd := <-cmds
		deps.Hub.DeliverResult(&pb.AgentCommandResult{
			CommandId: cmd.CommandId,
			Body: &pb.AgentCommandResult_UpgradeCore{
				UpgradeCore: &pb.UpgradeCoreResult{
					Kind: "xray", Ok: false, RolledBack: true,
					InstalledVersion: "1.8.0", // what the rollback restored
					Error:            "the new binary did not become healthy",
				},
			},
		})
	}()

	body := `{"kind":"xray","binary_url":"https://example.com/x.zip","binary_sha256":"aaaa","expected_version":"1.9.0"}`
	req := httptest.NewRequest("POST", "/api/v1/nodes/302/core-upgrade", strings.NewReader(body))
	req = req.WithContext(withActor(req.Context(), actor))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "302")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	deps.handleUpgradeNodeCore(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Delivered  bool   `json:"delivered"`
		OK         bool   `json:"ok"`
		RolledBack bool   `json:"rolled_back"`
		Message    string `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if !response.RolledBack {
		t.Error("RolledBack = false, want true")
	}
	if response.OK {
		t.Error("OK = true despite the upgrade failing")
	}
	if !strings.Contains(response.Message, "rolled back") {
		t.Errorf("message = %q, want it to mention the rollback", response.Message)
	}

	// A failed-and-rolled-back upgrade must NOT stamp adapter_registry --
	// nothing new was actually installed to record.
	var coreUpgradedAt sql.NullInt64
	_ = s.Read().QueryRowContext(context.Background(),
		`SELECT core_upgraded_at FROM adapter_registry WHERE node_id = ? AND kind = 'xray'`, nodeID,
	).Scan(&coreUpgradedAt)
	if coreUpgradedAt.Valid {
		t.Error("core_upgraded_at was stamped despite the upgrade failing")
	}
}

func TestHandleUpgradeNodeCore_RequiresAllThreeFields(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	nodeID := int64(303)
	createTestNode(t, s, nodeID, "core-node-3", "online")

	cases := []string{
		`{"kind":"xray","binary_url":"https://x","binary_sha256":""}`,
		`{"kind":"xray","binary_url":"","binary_sha256":"aaaa"}`,
		`{"kind":"","binary_url":"https://x","binary_sha256":"aaaa"}`,
	}
	for _, body := range cases {
		req := httptest.NewRequest("POST", "/api/v1/nodes/303/core-upgrade", strings.NewReader(body))
		req = req.WithContext(withActor(req.Context(), actor))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("nodeID", "303")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

		w := httptest.NewRecorder()
		deps.handleUpgradeNodeCore(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400", body, w.Code)
		}
	}
}

func TestHandleUpgradeNodeCore_NotFound(t *testing.T) {
	deps, _, actor := setupTestDeps(t)

	body := `{"kind":"xray","binary_url":"https://x","binary_sha256":"aaaa"}`
	req := httptest.NewRequest("POST", "/api/v1/nodes/999/core-upgrade", strings.NewReader(body))
	req = req.WithContext(withActor(req.Context(), actor))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "999")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	deps.handleUpgradeNodeCore(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}
