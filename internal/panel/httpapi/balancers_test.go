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
)

func newBalancerRequest(method string, nodeID int64, balancerID int64, body string) *http.Request {
	idStr := strconv.FormatInt(nodeID, 10)
	path := "/api/v1/nodes/" + idStr + "/balancers"
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", idStr)
	if balancerID != 0 {
		bidStr := strconv.FormatInt(balancerID, 10)
		path += "/" + bidStr
		rctx.URLParams.Add("balancerID", bidStr)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	return req
}

func TestHandleListBalancers_UnsupportedNodeReports404OnlyForMissingNode(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	grantOutboundPerms(actor)
	nodeID := int64(601)
	createTestNode(t, s, nodeID, "bal-node-1", "online")
	setAdapterKinds(t, s, nodeID, `["wireguard"]`)

	req := newBalancerRequest("GET", nodeID, 0, "")
	req = req.WithContext(withActor(req.Context(), actor))
	w := httptest.NewRecorder()
	deps.handleListBalancers(w, req)

	// Listing does not gate on capability the way create does -- an
	// unsupported node simply has none, the same as handleListRoutingRules.
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Balancers []balancerDTO `json:"balancers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if len(resp.Balancers) != 0 {
		t.Errorf("Balancers = %+v, want empty", resp.Balancers)
	}
}

func TestHandleCreateBalancer_SavesAndBumpsRevision(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	grantOutboundPerms(actor)
	nodeID := int64(602)
	createTestNode(t, s, nodeID, "bal-node-2", "online")
	setAdapterKinds(t, s, nodeID, `["xray"]`)

	var before int64
	_ = s.Read().QueryRowContext(context.Background(),
		`SELECT desired_revision FROM nodes WHERE id = ?`, nodeID).Scan(&before)

	req := newBalancerRequest("POST", nodeID, 0,
		`{"tag":"b1","selector":["warp-"],"strategy":"least_ping"}`)
	req = req.WithContext(withActor(req.Context(), actor))
	w := httptest.NewRecorder()
	deps.handleCreateBalancer(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var dto balancerDTO
	if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if dto.Tag != "b1" || dto.Strategy != "least_ping" || len(dto.Selector) != 1 {
		t.Errorf("dto = %+v", dto)
	}
	if !dto.Enabled {
		t.Error("Enabled = false, want true by default")
	}

	var after int64
	_ = s.Read().QueryRowContext(context.Background(),
		`SELECT desired_revision FROM nodes WHERE id = ?`, nodeID).Scan(&after)
	if after <= before {
		t.Errorf("desired_revision = %d, want it to have moved past %d", after, before)
	}
}

func TestHandleCreateBalancer_RefusesUnsupportedNode(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	grantOutboundPerms(actor)
	nodeID := int64(603)
	createTestNode(t, s, nodeID, "bal-node-3", "online")
	setAdapterKinds(t, s, nodeID, `["wireguard"]`)

	req := newBalancerRequest("POST", nodeID, 0, `{"tag":"b1","selector":["warp"]}`)
	req = req.WithContext(withActor(req.Context(), actor))
	w := httptest.NewRecorder()
	deps.handleCreateBalancer(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422: %s", w.Code, w.Body.String())
	}
}

func TestHandleCreateBalancer_RefusesEmptySelector(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	grantOutboundPerms(actor)
	nodeID := int64(604)
	createTestNode(t, s, nodeID, "bal-node-4", "online")
	setAdapterKinds(t, s, nodeID, `["xray"]`)

	req := newBalancerRequest("POST", nodeID, 0, `{"tag":"b1","selector":[]}`)
	req = req.WithContext(withActor(req.Context(), actor))
	w := httptest.NewRecorder()
	deps.handleCreateBalancer(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422: %s", w.Code, w.Body.String())
	}
}

func TestHandleCreateBalancer_RefusesDuplicateTag(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	grantOutboundPerms(actor)
	nodeID := int64(605)
	createTestNode(t, s, nodeID, "bal-node-5", "online")
	setAdapterKinds(t, s, nodeID, `["xray"]`)

	first := newBalancerRequest("POST", nodeID, 0, `{"tag":"same","selector":["warp"]}`)
	first = first.WithContext(withActor(first.Context(), actor))
	deps.handleCreateBalancer(httptest.NewRecorder(), first)

	second := newBalancerRequest("POST", nodeID, 0, `{"tag":"same","selector":["direct"]}`)
	second = second.WithContext(withActor(second.Context(), actor))
	w := httptest.NewRecorder()
	deps.handleCreateBalancer(w, second)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409: %s", w.Code, w.Body.String())
	}
}

func TestHandleUpdateBalancer_ChangesStoredFields(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	grantOutboundPerms(actor)
	nodeID := int64(606)
	createTestNode(t, s, nodeID, "bal-node-6", "online")
	setAdapterKinds(t, s, nodeID, `["xray"]`)

	createReq := newBalancerRequest("POST", nodeID, 0, `{"tag":"b1","selector":["warp"]}`)
	createReq = createReq.WithContext(withActor(createReq.Context(), actor))
	createW := httptest.NewRecorder()
	deps.handleCreateBalancer(createW, createReq)
	var created balancerDTO
	_ = json.Unmarshal(createW.Body.Bytes(), &created)

	updateReq := newBalancerRequest("PUT", nodeID, created.ID,
		`{"tag":"b1","selector":["warp","direct"],"strategy":"least_ping"}`)
	updateReq = updateReq.WithContext(withActor(updateReq.Context(), actor))
	updateW := httptest.NewRecorder()
	deps.handleUpdateBalancer(updateW, updateReq)

	if updateW.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", updateW.Code, updateW.Body.String())
	}
	var updated balancerDTO
	_ = json.Unmarshal(updateW.Body.Bytes(), &updated)
	if len(updated.Selector) != 2 || updated.Strategy != "least_ping" {
		t.Errorf("updated = %+v", updated)
	}
}

func TestHandleDeleteBalancer_RemovesTheRow(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	grantOutboundPerms(actor)
	nodeID := int64(607)
	createTestNode(t, s, nodeID, "bal-node-7", "online")
	setAdapterKinds(t, s, nodeID, `["xray"]`)

	createReq := newBalancerRequest("POST", nodeID, 0, `{"tag":"b1","selector":["warp"]}`)
	createReq = createReq.WithContext(withActor(createReq.Context(), actor))
	createW := httptest.NewRecorder()
	deps.handleCreateBalancer(createW, createReq)
	var created balancerDTO
	_ = json.Unmarshal(createW.Body.Bytes(), &created)

	deleteReq := newBalancerRequest("DELETE", nodeID, created.ID, "")
	deleteReq = deleteReq.WithContext(withActor(deleteReq.Context(), actor))
	deleteW := httptest.NewRecorder()
	deps.handleDeleteBalancer(deleteW, deleteReq)
	if deleteW.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d: %s", deleteW.Code, deleteW.Body.String())
	}

	listReq := newBalancerRequest("GET", nodeID, 0, "")
	listReq = listReq.WithContext(withActor(listReq.Context(), actor))
	listW := httptest.NewRecorder()
	deps.handleListBalancers(listW, listReq)
	var resp struct {
		Balancers []balancerDTO `json:"balancers"`
	}
	_ = json.Unmarshal(listW.Body.Bytes(), &resp)
	if len(resp.Balancers) != 0 {
		t.Errorf("balancer still listed after delete: %+v", resp.Balancers)
	}
}

func newRoutingRuleRequest(method string, nodeID int64, ruleID int64, body string) *http.Request {
	idStr := strconv.FormatInt(nodeID, 10)
	path := "/api/v1/nodes/" + idStr + "/routing"
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", idStr)
	if ruleID != 0 {
		ridStr := strconv.FormatInt(ruleID, 10)
		path += "/" + ridStr
		rctx.URLParams.Add("ruleID", ridStr)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	return req
}

func TestHandleCreateRoutingRule_TargetingABalancerSucceeds(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	grantOutboundPerms(actor)
	nodeID := int64(608)
	createTestNode(t, s, nodeID, "bal-node-8", "online")
	setAdapterKinds(t, s, nodeID, `["xray"]`)

	balReq := newBalancerRequest("POST", nodeID, 0, `{"tag":"b1","selector":["warp"]}`)
	balReq = balReq.WithContext(withActor(balReq.Context(), actor))
	deps.handleCreateBalancer(httptest.NewRecorder(), balReq)

	ruleReq := newRoutingRuleRequest("POST", nodeID, 0,
		`{"domains":["example.com"],"balancer_tag":"b1"}`)
	ruleReq = ruleReq.WithContext(withActor(ruleReq.Context(), actor))
	w := httptest.NewRecorder()
	deps.handleCreateRoutingRule(w, ruleReq)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var dto routingRuleDTO
	_ = json.Unmarshal(w.Body.Bytes(), &dto)
	if dto.BalancerTag != "b1" {
		t.Errorf("BalancerTag = %q, want b1", dto.BalancerTag)
	}
	if dto.OutboundTag != "" {
		t.Errorf("OutboundTag = %q, want empty for a balancer-targeting rule", dto.OutboundTag)
	}
}

func TestHandleCreateRoutingRule_RefusesBothOutboundAndBalancerTag(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	grantOutboundPerms(actor)
	nodeID := int64(609)
	createTestNode(t, s, nodeID, "bal-node-9", "online")
	setAdapterKinds(t, s, nodeID, `["xray"]`)

	req := newRoutingRuleRequest("POST", nodeID, 0,
		`{"domains":["example.com"],"outbound_tag":"direct","balancer_tag":"b1"}`)
	req = req.WithContext(withActor(req.Context(), actor))
	w := httptest.NewRecorder()
	deps.handleCreateRoutingRule(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422: %s", w.Code, w.Body.String())
	}
}

func TestHandleCreateRoutingRule_RefusesNeitherOutboundNorBalancerTag(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	grantOutboundPerms(actor)
	nodeID := int64(610)
	createTestNode(t, s, nodeID, "bal-node-10", "online")
	setAdapterKinds(t, s, nodeID, `["xray"]`)

	req := newRoutingRuleRequest("POST", nodeID, 0, `{"domains":["example.com"]}`)
	req = req.WithContext(withActor(req.Context(), actor))
	w := httptest.NewRecorder()
	deps.handleCreateRoutingRule(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422: %s", w.Code, w.Body.String())
	}
}

func TestHandleCreateRoutingRule_RefusesUnknownBalancerTag(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	grantOutboundPerms(actor)
	nodeID := int64(611)
	createTestNode(t, s, nodeID, "bal-node-11", "online")
	setAdapterKinds(t, s, nodeID, `["xray"]`)

	req := newRoutingRuleRequest("POST", nodeID, 0,
		`{"domains":["example.com"],"balancer_tag":"nope"}`)
	req = req.WithContext(withActor(req.Context(), actor))
	w := httptest.NewRecorder()
	deps.handleCreateRoutingRule(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422: %s", w.Code, w.Body.String())
	}
}
