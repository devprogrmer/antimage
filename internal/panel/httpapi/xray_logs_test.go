package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/amyrm/antimage/internal/panel/control"
	pb "github.com/amyrm/antimage/internal/shared/proto/antimage/v1"
)

func newXrayLogsRequest(nodeID int64, query string) *http.Request {
	idStr := strconv.FormatInt(nodeID, 10)
	path := "/api/v1/nodes/" + idStr + "/xray-logs"
	if query != "" {
		path += "?" + query
	}
	req := httptest.NewRequest("GET", path, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", idStr)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	return req
}

func TestHandleGetXrayLogs_DeliversAndReturnsLogText(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	deps.Hub = control.NewHub()
	nodeID := int64(401)
	createTestNode(t, s, nodeID, "logs-node", "online")
	seedAdapterRow(t, s, nodeID, "xray")

	_, cmds, release := deps.Hub.Register(nodeID)
	defer release()

	var sent *pb.FetchLogs
	go func() {
		cmd := <-cmds
		sent = cmd.GetFetchLogs()
		deps.Hub.DeliverResult(&pb.AgentCommandResult{
			CommandId: cmd.CommandId,
			Body: &pb.AgentCommandResult_FetchLogs{
				FetchLogs: &pb.FetchLogsResult{
					Kind: "xray", Ok: true, Logs: "Aug 31 12:00:00 xray started\n",
				},
			},
		})
	}()

	req := newXrayLogsRequest(nodeID, "lines=50")
	req = req.WithContext(withActor(req.Context(), actor))

	w := httptest.NewRecorder()
	deps.handleGetXrayLogs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if sent == nil || sent.Kind != "xray" || sent.Lines != 50 {
		t.Fatalf("command not sent correctly: %+v", sent)
	}

	var response struct {
		Delivered bool   `json:"delivered"`
		OK        bool   `json:"ok"`
		Logs      string `json:"logs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if !response.Delivered || !response.OK {
		t.Fatalf("response = %+v, want delivered+ok", response)
	}
	if !strings.Contains(response.Logs, "xray started") {
		t.Errorf("Logs = %q, want it to contain the agent's own log text", response.Logs)
	}
}

func TestHandleGetXrayLogs_DefaultsAndClampsLines(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	deps.Hub = control.NewHub()
	nodeID := int64(402)
	createTestNode(t, s, nodeID, "logs-node-2", "online")

	_, cmds, release := deps.Hub.Register(nodeID)
	defer release()

	var sent *pb.FetchLogs
	go func() {
		cmd := <-cmds
		sent = cmd.GetFetchLogs()
		deps.Hub.DeliverResult(&pb.AgentCommandResult{
			CommandId: cmd.CommandId,
			Body: &pb.AgentCommandResult_FetchLogs{
				FetchLogs: &pb.FetchLogsResult{Kind: "xray", Ok: true, Logs: "x"},
			},
		})
	}()

	req := newXrayLogsRequest(nodeID, "")
	req = req.WithContext(withActor(req.Context(), actor))
	w := httptest.NewRecorder()
	deps.handleGetXrayLogs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if sent == nil || sent.Lines != defaultXrayLogLines {
		t.Fatalf("Lines = %+v, want the default %d applied when the query omits it", sent, defaultXrayLogLines)
	}
}

// TestHandleGetXrayLogs_OfflineNodeReportsHonestly proves this route follows
// the same pattern as restart/geo-update/core-upgrade: no Hub delivery means
// a clear "offline" message, never a canned success.
func TestHandleGetXrayLogs_OfflineNodeReportsHonestly(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	deps.Hub = control.NewHub()
	nodeID := int64(403)
	createTestNode(t, s, nodeID, "logs-node-3", "offline")

	req := newXrayLogsRequest(nodeID, "")
	req = req.WithContext(withActor(req.Context(), actor))
	w := httptest.NewRecorder()
	deps.handleGetXrayLogs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Delivered bool   `json:"delivered"`
		OK        bool   `json:"ok"`
		Message   string `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if response.Delivered {
		t.Error("Delivered = true for a node with nothing registered on the Hub")
	}
	if response.OK {
		t.Error("OK = true for an undelivered command")
	}
	if !strings.Contains(response.Message, "offline") {
		t.Errorf("message = %q, want it to say the node is offline", response.Message)
	}
}

func TestHandleGetXrayLogs_NoAdapterOfThatKindIsReportedNotHiddenAsSuccess(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	deps.Hub = control.NewHub()
	nodeID := int64(404)
	createTestNode(t, s, nodeID, "logs-node-4", "online")

	_, cmds, release := deps.Hub.Register(nodeID)
	defer release()

	go func() {
		cmd := <-cmds
		deps.Hub.DeliverResult(&pb.AgentCommandResult{
			CommandId: cmd.CommandId,
			Body: &pb.AgentCommandResult_FetchLogs{
				FetchLogs: &pb.FetchLogsResult{
					Kind: "xray", Ok: false, Error: `this node runs no "xray" adapter`,
				},
			},
		})
	}()

	req := newXrayLogsRequest(nodeID, "")
	req = req.WithContext(withActor(req.Context(), actor))
	w := httptest.NewRecorder()
	deps.handleGetXrayLogs(w, req)

	var response struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if response.OK {
		t.Error("OK = true for a node with no xray adapter")
	}
	if !strings.Contains(response.Error, "no") {
		t.Errorf("error = %q, want it to explain no matching adapter exists", response.Error)
	}
}

func TestHandleGetXrayLogs_NotFound(t *testing.T) {
	deps, _, actor := setupTestDeps(t)

	req := newXrayLogsRequest(999, "")
	req = req.WithContext(withActor(req.Context(), actor))
	w := httptest.NewRecorder()
	deps.handleGetXrayLogs(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}
