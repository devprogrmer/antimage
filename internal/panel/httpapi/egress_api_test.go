package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Egress is the first desired-state surface where a panel-side mistake sends a
// user's traffic somewhere they did not choose. These tests hold three lines:
// only an authorised actor may configure it, only a node whose adapters can
// apply it may be given it, and what the panel stores must actually reach the
// document.

// egressNode makes node 1 an xray node, which is what egressCapableAdapter
// looks for. The default fixture node runs the stub adapter, and stub declares
// SupportsOutbounds=false on purpose -- so a test that forgot this would be
// exercising the refusal path while appearing to exercise the happy one.
func egressNode(t *testing.T, env *testEnv, nodeID int64, kinds string) {
	t.Helper()
	if err := env.store.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE nodes SET adapter_kinds = ? WHERE id = ?`, kinds, nodeID)
		return err
	}); err != nil {
		t.Fatalf("set adapter kinds: %v", err)
	}
}

func nodeRevision(t *testing.T, env *testEnv, nodeID int64) int64 {
	t.Helper()
	var rev int64
	if err := env.store.Read().QueryRow(
		`SELECT desired_revision FROM nodes WHERE id = ?`, nodeID).Scan(&rev); err != nil {
		t.Fatalf("read revision: %v", err)
	}
	return rev
}

func createOutbound(t *testing.T, env *testEnv, token, body string) *struct {
	ID  int64  `json:"id"`
	Tag string `json:"tag"`
} {
	t.Helper()
	res := env.post(t, "/api/v1/nodes/1/outbounds", body, token)
	if res.Code != http.StatusCreated {
		t.Fatalf("create outbound = %d: %s", res.Code, res.Body)
	}
	var out struct {
		ID  int64  `json:"id"`
		Tag string `json:"tag"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return &out
}

func TestOutboundReachesTheDesiredDocument(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	egressNode(t, env, 1, `["xray"]`)

	before := nodeRevision(t, env, 1)
	created := createOutbound(t, env, adminToken,
		`{"tag":"warp","kind":"block"}`)
	if created.ID == 0 {
		t.Fatal("no outbound id returned")
	}

	// Configuring egress is a desired-state change like any other.
	if after := nodeRevision(t, env, 1); after <= before {
		t.Errorf("desired_revision %d did not advance past %d: the node was never "+
			"told about the outbound", after, before)
	}

	// And it must actually be IN the document, not merely stored. A row the
	// builder never reads is a policy the operator can see and the node cannot.
	var stored int
	if err := env.store.Read().QueryRow(
		`SELECT COUNT(*) FROM outbounds WHERE node_id = 1 AND tag = 'warp'`).Scan(&stored); err != nil {
		t.Fatalf("count: %v", err)
	}
	if stored != 1 {
		t.Fatalf("outbound not stored")
	}

	res := env.get(t, "/api/v1/nodes/1/outbounds", adminToken)
	if res.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", res.Code, res.Body)
	}
	if !strings.Contains(res.Body.String(), `"warp"`) {
		t.Errorf("created outbound missing from the list: %s", res.Body)
	}
}

// A node whose adapters have no routing engine must be refused. Accepting the
// outbound would show an operator an egress policy the node is not enforcing.
func TestOutboundRefusedOnIncapableNode(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	egressNode(t, env, 1, `["wireguard"]`)

	res := env.post(t, "/api/v1/nodes/1/outbounds", `{"tag":"warp","kind":"block"}`, adminToken)
	if res.Code != http.StatusUnprocessableEntity {
		t.Errorf("create on a wireguard-only node = %d, want 422: %s", res.Code, res.Body)
	}
	if !strings.Contains(res.Body.String(), "routing engine") {
		t.Errorf("refusal does not explain why: %s", res.Body)
	}
}

// The stub adapter declares SupportsOutbounds=false, so the default fixture
// node must be refused too. This is what stops the happy-path tests above from
// passing by accident if egressNode is ever dropped.
func TestOutboundRefusedOnStubNode(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)

	res := env.post(t, "/api/v1/nodes/1/outbounds", `{"tag":"warp","kind":"block"}`, adminToken)
	if res.Code != http.StatusUnprocessableEntity {
		t.Errorf("create on a stub node = %d, want 422: %s", res.Code, res.Body)
	}
}

// Duplicate tags are refused by both adapters at render time. The unique index
// means the operator is told when they submit, not when a rule quietly selects
// the wrong outbound.
func TestDuplicateOutboundTagIsAConflict(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	egressNode(t, env, 1, `["xray"]`)

	createOutbound(t, env, adminToken, `{"tag":"warp","kind":"block"}`)
	res := env.post(t, "/api/v1/nodes/1/outbounds", `{"tag":"warp","kind":"direct"}`, adminToken)
	if res.Code != http.StatusConflict {
		t.Errorf("duplicate tag = %d, want 409: %s", res.Code, res.Body)
	}
}

// Params are validated against the adapter's published OutboundSchema. The
// panel holds no protocol knowledge of its own.
func TestOutboundParamsAreValidatedAgainstTheAdapterSchema(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	egressNode(t, env, 1, `["xray"]`)

	res := env.post(t, "/api/v1/nodes/1/outbounds",
		`{"tag":"bad","kind":"socks","params":{"not_a_real_field":1}}`, adminToken)
	if res.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown param = %d, want 422: %s", res.Code, res.Body)
	}
}

// -- routing rules -----------------------------------------------------------

func TestRoutingRuleLifecycle(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	egressNode(t, env, 1, `["xray"]`)
	createOutbound(t, env, adminToken, `{"tag":"warp","kind":"block"}`)

	before := nodeRevision(t, env, 1)
	res := env.post(t, "/api/v1/nodes/1/routing",
		`{"priority":10,"domains":["example.com"],"outbound_tag":"warp"}`, adminToken)
	if res.Code != http.StatusCreated {
		t.Fatalf("create rule = %d: %s", res.Code, res.Body)
	}
	var rule struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(res.Body).Decode(&rule); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if after := nodeRevision(t, env, 1); after <= before {
		t.Errorf("routing change did not bump desired_revision")
	}

	list := env.get(t, "/api/v1/nodes/1/routing", adminToken)
	if !strings.Contains(list.Body.String(), "example.com") {
		t.Errorf("rule missing from the list: %s", list.Body)
	}

	del := env.delete(t, "/api/v1/nodes/1/routing/"+itoa64(rule.ID), adminToken)
	if del.Code != http.StatusNoContent {
		t.Errorf("delete = %d: %s", del.Code, del.Body)
	}
}

// A rule with no matchers is applied to ALL traffic by both proxies. An
// operator who left every field empty did not mean that.
func TestRoutingRuleWithoutMatchersIsRefused(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	egressNode(t, env, 1, `["xray"]`)
	createOutbound(t, env, adminToken, `{"tag":"warp","kind":"block"}`)

	res := env.post(t, "/api/v1/nodes/1/routing", `{"outbound_tag":"warp"}`, adminToken)
	if res.Code != http.StatusUnprocessableEntity {
		t.Errorf("matcherless rule = %d, want 422: %s", res.Code, res.Body)
	}
	if !strings.Contains(res.Body.String(), "ALL traffic") {
		t.Errorf("refusal does not explain the consequence: %s", res.Body)
	}
}

// A rule naming an outbound the node does not have is refused. The adapters
// make this check because they can see both panel-defined outbounds and the
// ones an adapter supplies itself; the panel surfaces their message.
func TestRuleNamingUnknownOutboundIsRefused(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	egressNode(t, env, 1, `["xray"]`)

	res := env.post(t, "/api/v1/nodes/1/routing",
		`{"domains":["example.com"],"outbound_tag":"does-not-exist"}`, adminToken)
	if res.Code != http.StatusUnprocessableEntity {
		t.Errorf("rule naming a missing outbound = %d, want 422: %s", res.Code, res.Body)
	}
}

// sing-box takes ports as numbers and has a separate field for ranges, so a
// range would be refused at render time by that adapter but not the other.
// Refusing at the API keeps the two from disagreeing about what is accepted.
func TestPortRangeIsRefused(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	egressNode(t, env, 1, `["xray"]`)
	createOutbound(t, env, adminToken, `{"tag":"warp","kind":"block"}`)

	res := env.post(t, "/api/v1/nodes/1/routing",
		`{"ports":["1000-2000"],"outbound_tag":"warp"}`, adminToken)
	if res.Code != http.StatusUnprocessableEntity {
		t.Errorf("port range = %d, want 422: %s", res.Code, res.Body)
	}
}

// PUT /routing/default and PUT /routing/{ruleID} share a path shape. chi must
// prefer the static segment, or setting the default would be parsed as a rule
// id and fail.
func TestDefaultOutboundRouteIsNotShadowedByTheRuleRoute(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	egressNode(t, env, 1, `["xray"]`)
	createOutbound(t, env, adminToken, `{"tag":"warp","kind":"block"}`)

	res := env.put(t, "/api/v1/nodes/1/routing/default", `{"outbound_tag":"warp"}`, adminToken)
	if res.Code != http.StatusOK {
		t.Fatalf("set default = %d, want 200 — the {ruleID} route may be shadowing it: %s",
			res.Code, res.Body)
	}

	var stored string
	if err := env.store.Read().QueryRow(
		`SELECT default_outbound_tag FROM nodes WHERE id = 1`).Scan(&stored); err != nil {
		t.Fatalf("read: %v", err)
	}
	if stored != "warp" {
		t.Errorf("default_outbound_tag = %q, want warp", stored)
	}
}

// Clearing the default is a meaningful state, not an absent one: it returns the
// node to the proxy's own behaviour.
func TestDefaultOutboundCanBeCleared(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	egressNode(t, env, 1, `["xray"]`)
	createOutbound(t, env, adminToken, `{"tag":"warp","kind":"block"}`)
	env.put(t, "/api/v1/nodes/1/routing/default", `{"outbound_tag":"warp"}`, adminToken)

	res := env.put(t, "/api/v1/nodes/1/routing/default", `{"outbound_tag":""}`, adminToken)
	if res.Code != http.StatusOK {
		t.Fatalf("clear default = %d: %s", res.Code, res.Body)
	}
	var stored string
	if err := env.store.Read().QueryRow(
		`SELECT default_outbound_tag FROM nodes WHERE id = 1`).Scan(&stored); err != nil {
		t.Fatalf("read: %v", err)
	}
	if stored != "" {
		t.Errorf("default_outbound_tag = %q, want empty", stored)
	}
}

// -- permissions -------------------------------------------------------------

// scopeToNode gives an admin visibility of one node.
//
// Without it every non-super actor is refused by scope before the permission is
// ever consulted, which would make a permission test pass for the wrong reason.
func scopeToNode(t *testing.T, env *testEnv, adminID, nodeID int64) {
	t.Helper()
	if err := env.store.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO admin_scopes (admin_id, scope_type, scope_id) VALUES (?,'node',?)`,
			adminID, nodeID)
		return err
	}); err != nil {
		t.Fatalf("scope admin %d to node %d: %v", adminID, nodeID, err)
	}
}

// outbound:write is what separates seeing egress from redirecting traffic.
//
// The actor here is scoped to the node ON PURPOSE. An unscoped one is refused
// before the permission is consulted, so a test using a reseller would pass
// whatever the permission gate said -- it would be exercising node scope while
// claiming to exercise outbound:write. The readonly role carries outbound:read
// and not outbound:write, which is exactly the actor that isolates the gate.
func TestEgressMutationRequiresOutboundWrite(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	egressNode(t, env, 1, `["xray"]`)
	createOutbound(t, env, adminToken, `{"tag":"warp","kind":"block"}`)

	readerID := env.seedAdmin(t, "auditor", "pw", "readonly")
	scopeToNode(t, env, readerID, 1)
	readerToken := env.login(t, "auditor", "pw")

	// Reading works: the role has outbound:read and the scope covers the node.
	// This is what proves the refusals below are about the permission rather
	// than about the actor being invisible to the node.
	if res := env.get(t, "/api/v1/nodes/1/outbounds", readerToken); res.Code != http.StatusOK {
		t.Fatalf("scoped reader cannot list outbounds (%d) — the writes below would "+
			"then be refused by scope, not by permission: %s", res.Code, res.Body)
	}

	for _, tc := range []struct {
		name, method, path, body string
	}{
		{"create outbound", "POST", "/api/v1/nodes/1/outbounds", `{"tag":"x","kind":"block"}`},
		{"create rule", "POST", "/api/v1/nodes/1/routing", `{"domains":["a.com"],"outbound_tag":"warp"}`},
		{"set default", "PUT", "/api/v1/nodes/1/routing/default", `{"outbound_tag":"warp"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := env.post(t, tc.path, tc.body, readerToken)
			if tc.method == "PUT" {
				res = env.put(t, tc.path, tc.body, readerToken)
			}
			if res.Code != http.StatusForbidden {
				t.Errorf("%s without outbound:write = %d, want 403: an actor who can "+
					"only read egress could redirect traffic", tc.name, res.Code)
			}
		})
	}
}

// Holding outbound:read is not sufficient on its own.
//
// Egress is addressed by node, and rbac.Check treats a TargetNode as an
// exhaustive allow-list: a non-super actor sees a node only if it is in their
// NodeIDs. A reseller has no node scope, so they are refused even though the
// role carries the permission. That is the fail-closed direction and it is
// what should happen -- the permission says "may read egress", the scope says
// "of which nodes", and an empty scope means none rather than all.
//
// Pinned because the inverted default is exactly how panels leak: if node
// scoping were ever dropped, every tenant would silently gain visibility of
// every node's egress policy.
func TestEgressReadStillNeedsNodeScope(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	egressNode(t, env, 1, `["xray"]`)
	tenantToken, _ := seedTenant(t, env, "alice", svcID, adminToken)

	for _, path := range []string{
		"/api/v1/nodes/1/outbounds",
		"/api/v1/nodes/1/routing",
	} {
		if res := env.get(t, path, tenantToken); res.Code != http.StatusForbidden {
			t.Errorf("GET %s as an unscoped reseller = %d, want 403", path, res.Code)
		}
	}

	// The super admin, whose scope covers everything, still gets through.
	if res := env.get(t, "/api/v1/nodes/1/outbounds", adminToken); res.Code != http.StatusOK {
		t.Errorf("list outbounds as super admin = %d, want 200: %s", res.Code, res.Body)
	}
}
