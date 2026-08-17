package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestAuditRequiresPermission(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "reseller", "pw", "reseller") // reseller lacks audit:read
	token := env.login(t, "reseller", "pw")

	if res := env.get(t, "/api/v1/audit", token); res.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", res.Code)
	}
}

func TestAuditListsNewestFirst(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	token := env.login(t, "root", "pw")
	env.post(t, "/api/v1/nodes", `{"name":"a","address":"1.1.1.1"}`, token)
	env.post(t, "/api/v1/nodes", `{"name":"b","address":"2.2.2.2"}`, token)

	res := env.get(t, "/api/v1/audit", token)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d", res.Code)
	}
	var body struct {
		Entries []struct {
			ID     int64  `json:"id"`
			Action string `json:"action"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Entries) < 2 {
		t.Fatalf("got %d entries, want at least 2", len(body.Entries))
	}
	if body.Entries[0].ID < body.Entries[1].ID {
		t.Error("entries are not newest-first")
	}
}

// countAudit reports how many rows match an action and result.
func (e *testEnv) countAudit(t *testing.T, action, result string) int {
	t.Helper()
	var n int
	if err := e.store.Read().QueryRow(
		`SELECT count(*) FROM audit_log WHERE action = ? AND result = ?`,
		action, result).Scan(&n); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	return n
}

// Spec invariant 9: an attempt that never commits is still a security event.
// A reseller probing a node outside their scope must leave a trace, or the
// probe is indistinguishable from never having happened.
func TestAuthorizationDenialIsAudited(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	resellerID := env.seedAdmin(t, "reseller", "pw", "reseller")
	rootToken := env.login(t, "root", "pw")
	if res := env.post(t, "/api/v1/nodes",
		`{"name":"secret","address":"9.9.9.9"}`, rootToken); res.Code != http.StatusCreated {
		t.Fatalf("create node: %d %s", res.Code, res.Body)
	}

	resellerToken := env.login(t, "reseller", "pw")
	if res := env.get(t, "/api/v1/nodes/1", resellerToken); res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", res.Code)
	}

	var (
		actorType string
		adminID   int64
		perm      string
		targetTyp string
		targetID  int64
	)
	err := env.store.Read().QueryRow(
		`SELECT actor_type, actor_admin_id, json_extract(after_json,'$.permission'),
		        target_type, target_id
		   FROM audit_log
		  WHERE action = 'authz.deny' AND result = 'denied'
		  ORDER BY id DESC LIMIT 1`,
	).Scan(&actorType, &adminID, &perm, &targetTyp, &targetID)
	if err != nil {
		t.Fatalf("no denial audit row: %v", err)
	}
	if actorType != "admin" || adminID != resellerID {
		t.Errorf("actor = %s/%d, want admin/%d", actorType, adminID, resellerID)
	}
	if perm != "node:read" {
		t.Errorf("permission = %q, want node:read", perm)
	}
	if targetTyp != "node" || targetID != 1 {
		t.Errorf("target = %s/%d, want node/1", targetTyp, targetID)
	}
}

// A denial on a global-target permission is recorded too: it carries no
// target id, but "who tried to read the audit log" is exactly the event the
// log is for.
func TestGlobalPermissionDenialIsAudited(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "reseller", "pw", "reseller")
	token := env.login(t, "reseller", "pw")

	if res := env.get(t, "/api/v1/audit", token); res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", res.Code)
	}
	if got := env.countAudit(t, "authz.deny", "denied"); got != 1 {
		t.Errorf("authz.deny rows = %d, want 1", got)
	}
}

// The other half of invariant 9's "validation rejections": a 422 must not
// vanish. The rejected request is also the one that never opened a
// transaction, so this pins that BestEffort is reachable on that path.
func TestSchemaRejectionIsAudited(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	token := env.login(t, "root", "pw")
	env.post(t, "/api/v1/nodes", `{"name":"de-1","address":"1.2.3.4"}`, token)

	res := env.post(t, "/api/v1/nodes/1/services",
		`{"adapter_kind":"stub","params":{"port":"not-a-port"}}`, token)
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", res.Code)
	}
	if got := env.countAudit(t, "service.create", "denied"); got != 1 {
		t.Errorf("service.create/denied rows = %d, want 1", got)
	}

	// And the rejected update path, which is a separate call site.
	if res := env.post(t, "/api/v1/nodes/1/services",
		`{"adapter_kind":"stub","params":{"port":443}}`, token); res.Code != http.StatusCreated {
		t.Fatalf("create service: %d %s", res.Code, res.Body)
	}
	if res := env.do(t, http.MethodPut, "/api/v1/services/1",
		`{"adapter_kind":"stub","params":{}}`, token); res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("update status = %d, want 422", res.Code)
	}
	if got := env.countAudit(t, "service.update", "denied"); got != 1 {
		t.Errorf("service.update/denied rows = %d, want 1", got)
	}
}

// The audit row must describe the rejection without preserving the payload:
// an adapter schema may name a credential field, and this log is readable by
// every audit:read holder.
func TestSchemaRejectionDoesNotRecordSubmittedParams(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	token := env.login(t, "root", "pw")
	env.post(t, "/api/v1/nodes", `{"name":"de-1","address":"1.2.3.4"}`, token)

	env.post(t, "/api/v1/nodes/1/services",
		`{"adapter_kind":"stub","params":{"port":443,"password":"hunter2"}}`, token)

	var n int
	if err := env.store.Read().QueryRow(
		`SELECT count(*) FROM audit_log WHERE after_json LIKE '%hunter2%'`).Scan(&n); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if n != 0 {
		t.Errorf("%d audit rows echo the submitted params back", n)
	}
}

func TestSessionListShowsOwnSessionsAndRevokeWorks(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	first := env.login(t, "root", "pw")
	second := env.login(t, "root", "pw")

	res := env.get(t, "/api/v1/sessions", first)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body)
	}
	var body struct {
		Sessions []struct {
			ID      int64 `json:"id"`
			Current bool  `json:"current"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(body.Sessions))
	}

	var other int64
	for _, s := range body.Sessions {
		if !s.Current {
			other = s.ID
		}
	}
	if other == 0 {
		t.Fatal("no non-current session found; the current flag is wrong")
	}
	if res := env.do(t, http.MethodDelete,
		"/api/v1/sessions/"+itoa64(other), "", first); res.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d", res.Code)
	}
	if res := env.get(t, "/api/v1/nodes", second); res.Code != http.StatusUnauthorized {
		t.Errorf("revoked session still works: %d", res.Code)
	}
	// The revoking session must survive its own request.
	if res := env.get(t, "/api/v1/nodes", first); res.Code != http.StatusOK {
		t.Errorf("revoking session was collateral damage: %d", res.Code)
	}
}

// The session list is scoped to the caller, so another admin's sessions must
// not appear in it even though they share the table.
func TestSessionListIsScopedToTheCaller(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	env.seedAdmin(t, "other", "pw", "admin")
	env.login(t, "other", "pw")
	env.login(t, "other", "pw")
	rootToken := env.login(t, "root", "pw")

	res := env.get(t, "/api/v1/sessions", rootToken)
	var body struct {
		Sessions []json.RawMessage `json:"sessions"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Sessions) != 1 {
		t.Errorf("root sees %d sessions, want only its own 1", len(body.Sessions))
	}
}

func TestCannotRevokeAnotherAdminsSession(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	env.seedAdmin(t, "other", "pw", "admin")
	otherToken := env.login(t, "other", "pw")
	rootToken := env.login(t, "root", "pw")

	// A super admin may revoke anyone; an ordinary admin may not. Verify the
	// ordinary case by having 'other' try to revoke a root session.
	res := env.get(t, "/api/v1/sessions", rootToken)
	var rootBody struct {
		Sessions []struct {
			ID int64 `json:"id"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(res.Body).Decode(&rootBody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rootBody.Sessions) != 1 {
		t.Fatalf("root has %d sessions, want 1", len(rootBody.Sessions))
	}
	rootSession := rootBody.Sessions[0].ID

	if got := env.do(t, http.MethodDelete,
		"/api/v1/sessions/"+itoa64(rootSession), "", otherToken); got.Code != http.StatusNotFound {
		t.Errorf("cross-admin revoke status = %d, want 404", got.Code)
	}
	// The refusal must actually have refused: root's session still works.
	if res := env.get(t, "/api/v1/nodes", rootToken); res.Code != http.StatusOK {
		t.Errorf("root session was revoked anyway: %d", res.Code)
	}
}

// A session id that belongs to nobody and one that belongs to someone else
// must be indistinguishable, or the endpoint enumerates sessions.
func TestUnknownSessionRevokeIsAlsoNotFound(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	token := env.login(t, "root", "pw")

	res := env.do(t, http.MethodDelete, "/api/v1/sessions/424242", "", token)
	if res.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.Code)
	}
}

// Invariant 9 names validation rejections alongside authz denials: an attempt
// that never commits must still leave a record. handleCreateNode's own
// required-fields check is such an attempt, and it is not a schema rejection,
// so services.go's path does not cover it.
func TestCreateNodeValidationRejectionIsAudited(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	token := env.login(t, "root", "pw")

	res := env.post(t, "/api/v1/nodes", `{"name":"","address":""}`, token)
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", res.Code)
	}

	var n int
	if err := env.store.Read().QueryRow(
		`SELECT count(*) FROM audit_log
		  WHERE action = 'node.create' AND result = 'denied'`).Scan(&n); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if n != 1 {
		t.Fatalf("node.create/denied audit rows = %d, want 1: a rejected create left no trace", n)
	}

	// The node must not exist: a denial record is not a partial commit.
	var nodes int
	_ = env.store.Read().QueryRow(`SELECT count(*) FROM nodes`).Scan(&nodes)
	if nodes != 0 {
		t.Errorf("nodes = %d, want 0", nodes)
	}
}
