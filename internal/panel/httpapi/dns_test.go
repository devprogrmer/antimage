package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/amyrm/antimage/internal/panel/rbac"
)

// setupTestDeps's actor carries only node:read/write; DNS is gated on
// outbound:*, the same permission egress and routing already use, so these
// tests grant it explicitly rather than widening the shared fixture for
// every other test in this package.
func grantOutboundPerms(actor *rbac.Actor) {
	actor.Perms[rbac.PermOutboundRead] = struct{}{}
	actor.Perms[rbac.PermOutboundWrite] = struct{}{}
}

func newDNSRequest(method string, nodeID int64, body string) *http.Request {
	idStr := strconv.FormatInt(nodeID, 10)
	req := httptest.NewRequest(method, "/api/v1/nodes/"+idStr+"/dns", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", idStr)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	return req
}

func setAdapterKinds(t *testing.T, s interface {
	Write(context.Context, func(*sql.Tx) error) error
}, nodeID int64, kinds string) {
	t.Helper()
	if err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE nodes SET adapter_kinds = ? WHERE id = ?`, kinds, nodeID)
		return err
	}); err != nil {
		t.Fatalf("set adapter_kinds: %v", err)
	}
}

func TestHandleGetNodeDNS_UnsupportedNodeReportsNotHiddenAsError(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	grantOutboundPerms(actor)
	nodeID := int64(501)
	createTestNode(t, s, nodeID, "dns-node-1", "online")
	setAdapterKinds(t, s, nodeID, `["wireguard"]`)

	req := newDNSRequest("GET", nodeID, "")
	req = req.WithContext(withActor(req.Context(), actor))
	w := httptest.NewRecorder()
	deps.handleGetNodeDNS(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var resp dnsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if resp.Supported {
		t.Error("Supported = true for a node running only wireguard")
	}
	if resp.Reason == "" {
		t.Error("no reason given for an unsupported node")
	}
}

func TestHandleGetNodeDNS_NotFound(t *testing.T) {
	deps, _, actor := setupTestDeps(t)
	grantOutboundPerms(actor)

	req := newDNSRequest("GET", 999, "")
	req = req.WithContext(withActor(req.Context(), actor))
	w := httptest.NewRecorder()
	deps.handleGetNodeDNS(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleGetNodeDNS_DefaultsToEmptyOnASupportedNode(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	grantOutboundPerms(actor)
	nodeID := int64(502)
	createTestNode(t, s, nodeID, "dns-node-2", "online")
	setAdapterKinds(t, s, nodeID, `["xray"]`)

	req := newDNSRequest("GET", nodeID, "")
	req = req.WithContext(withActor(req.Context(), actor))
	w := httptest.NewRecorder()
	deps.handleGetNodeDNS(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var resp dnsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if !resp.Supported {
		t.Error("Supported = false for a node running xray")
	}
	if resp.AdapterKind != "xray" {
		t.Errorf("AdapterKind = %q, want xray", resp.AdapterKind)
	}
	if len(resp.Servers) != 0 {
		t.Errorf("Servers = %+v, want empty for a node that never saved a config", resp.Servers)
	}
}

func TestHandleSetNodeDNS_SavesAndBumpsRevision(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	grantOutboundPerms(actor)
	nodeID := int64(503)
	createTestNode(t, s, nodeID, "dns-node-3", "online")
	setAdapterKinds(t, s, nodeID, `["xray"]`)

	var before int64
	_ = s.Read().QueryRowContext(context.Background(),
		`SELECT desired_revision FROM nodes WHERE id = ?`, nodeID).Scan(&before)

	body := `{
		"servers": [{"address": "1.1.1.1"}, {"address": "10.0.0.1", "domains": ["corp.internal"], "skip_fallback": true}],
		"hosts": {"internal.corp": ["10.0.0.5"]},
		"fakedns": [{"ip_pool": "198.18.0.0/15", "pool_size": 65535}],
		"query_strategy": "UseIPv4",
		"disable_cache": true
	}`
	req := newDNSRequest("PUT", nodeID, body)
	req = req.WithContext(withActor(req.Context(), actor))
	w := httptest.NewRecorder()
	deps.handleSetNodeDNS(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var resp dnsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if len(resp.Servers) != 2 {
		t.Errorf("Servers = %+v, want 2", resp.Servers)
	}
	if resp.QueryStrategy != "UseIPv4" || !resp.DisableCache {
		t.Errorf("QueryStrategy/DisableCache not echoed back: %+v", resp)
	}

	var after int64
	_ = s.Read().QueryRowContext(context.Background(),
		`SELECT desired_revision FROM nodes WHERE id = ?`, nodeID).Scan(&after)
	if after <= before {
		t.Errorf("desired_revision = %d, want it to have moved past %d", after, before)
	}

	// A second GET must read back exactly what was saved -- proving the PUT
	// actually persisted rather than just echoing the request back.
	getReq := newDNSRequest("GET", nodeID, "")
	getReq = getReq.WithContext(withActor(getReq.Context(), actor))
	getW := httptest.NewRecorder()
	deps.handleGetNodeDNS(getW, getReq)
	var getResp dnsResponse
	if err := json.Unmarshal(getW.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("parse get response: %v", err)
	}
	if len(getResp.Servers) != 2 || getResp.Hosts["internal.corp"][0] != "10.0.0.5" {
		t.Errorf("stored config does not round-trip: %+v", getResp)
	}
}

func TestHandleSetNodeDNS_RefusesUnsupportedNode(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	grantOutboundPerms(actor)
	nodeID := int64(504)
	createTestNode(t, s, nodeID, "dns-node-4", "online")
	setAdapterKinds(t, s, nodeID, `["wireguard"]`)

	req := newDNSRequest("PUT", nodeID, `{"servers":[{"address":"1.1.1.1"}]}`)
	req = req.WithContext(withActor(req.Context(), actor))
	w := httptest.NewRecorder()
	deps.handleSetNodeDNS(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422: %s", w.Code, w.Body.String())
	}
}

func TestHandleSetNodeDNS_RefusesMissingServerAddress(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	grantOutboundPerms(actor)
	nodeID := int64(505)
	createTestNode(t, s, nodeID, "dns-node-5", "online")
	setAdapterKinds(t, s, nodeID, `["xray"]`)

	req := newDNSRequest("PUT", nodeID, `{"servers":[{"address":""}]}`)
	req = req.WithContext(withActor(req.Context(), actor))
	w := httptest.NewRecorder()
	deps.handleSetNodeDNS(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422: %s", w.Code, w.Body.String())
	}
}

func TestHandleSetNodeDNS_RefusesInvalidFakeDNSCIDR(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	grantOutboundPerms(actor)
	nodeID := int64(506)
	createTestNode(t, s, nodeID, "dns-node-6", "online")
	setAdapterKinds(t, s, nodeID, `["xray"]`)

	req := newDNSRequest("PUT", nodeID, `{"fakedns":[{"ip_pool":"not-a-cidr","pool_size":100}]}`)
	req = req.WithContext(withActor(req.Context(), actor))
	w := httptest.NewRecorder()
	deps.handleSetNodeDNS(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422: %s", w.Code, w.Body.String())
	}
}

func TestHandleSetNodeDNS_RefusesUnknownQueryStrategy(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	grantOutboundPerms(actor)
	nodeID := int64(507)
	createTestNode(t, s, nodeID, "dns-node-7", "online")
	setAdapterKinds(t, s, nodeID, `["xray"]`)

	req := newDNSRequest("PUT", nodeID, `{"query_strategy":"UseIPv5"}`)
	req = req.WithContext(withActor(req.Context(), actor))
	w := httptest.NewRecorder()
	deps.handleSetNodeDNS(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422: %s", w.Code, w.Body.String())
	}
}
