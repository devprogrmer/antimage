package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestCreateNodeThenListIt(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	token := env.login(t, "root", "pw")

	res := env.post(t, "/api/v1/nodes", `{"name":"de-1","address":"1.2.3.4"}`, token)
	if res.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", res.Code, res.Body)
	}

	list := env.get(t, "/api/v1/nodes", token)
	var body struct {
		Nodes []struct {
			ID     int64  `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"nodes"`
	}
	if err := json.NewDecoder(list.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Nodes) != 1 || body.Nodes[0].Name != "de-1" {
		t.Fatalf("nodes = %+v, want one named de-1", body.Nodes)
	}
	if body.Nodes[0].Status != "pending" {
		t.Errorf("status = %q, want pending before enrollment", body.Nodes[0].Status)
	}
}

func TestResellerCannotSeeAnotherResellersNode(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	env.seedAdmin(t, "reseller", "pw", "reseller")
	rootToken := env.login(t, "root", "pw")

	if res := env.post(t, "/api/v1/nodes", `{"name":"secret","address":"9.9.9.9"}`, rootToken); res.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", res.Code, res.Body)
	}

	resellerToken := env.login(t, "reseller", "pw")
	list := env.get(t, "/api/v1/nodes", resellerToken)
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d", list.Code)
	}
	var body struct {
		Nodes []json.RawMessage `json:"nodes"`
	}
	_ = json.NewDecoder(list.Body).Decode(&body)
	if len(body.Nodes) != 0 {
		t.Fatalf("ungranted reseller saw %d nodes, want 0", len(body.Nodes))
	}
}

// TestCreateNodeRequiresNodeWrite pins the first enforcement layer on the
// write path: node:read is enough to list nodes and never enough to add one.
func TestCreateNodeRequiresNodeWrite(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "reseller", "pw", "reseller")
	token := env.login(t, "reseller", "pw")

	res := env.post(t, "/api/v1/nodes", `{"name":"de-1","address":"1.2.3.4"}`, token)
	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", res.Code)
	}
	if got := errorCode(t, res); got != "forbidden" {
		t.Errorf("error code = %q, want %q", got, "forbidden")
	}
	var created int
	_ = env.store.Read().QueryRow(`SELECT count(*) FROM nodes`).Scan(&created)
	if created != 0 {
		t.Errorf("nodes created = %d, want 0 after a denied request", created)
	}
}

func TestDuplicateNodeNameIsRejected(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	token := env.login(t, "root", "pw")

	env.post(t, "/api/v1/nodes", `{"name":"de-1","address":"1.2.3.4"}`, token)
	res := env.post(t, "/api/v1/nodes", `{"name":"de-1","address":"5.6.7.8"}`, token)
	if res.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", res.Code)
	}
}

func TestEnrollTokenIsReturnedOnceWithACommand(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	token := env.login(t, "root", "pw")
	env.post(t, "/api/v1/nodes", `{"name":"de-1","address":"1.2.3.4"}`, token)

	res := env.post(t, "/api/v1/nodes/1/enroll-token", "", token)
	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body)
	}
	var body struct {
		Token     string `json:"token"`
		Command   string `json:"command"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Token == "" || body.ExpiresAt == 0 {
		t.Fatalf("incomplete response: %+v", body)
	}
	if body.Command == "" {
		t.Error("no bootstrap command returned; the SSH-free path depends on it")
	}

	// Issuing the right to become a node is exactly the kind of act the log
	// exists for, and the token itself must never reach it.
	var audited int
	_ = env.store.Read().QueryRow(
		`SELECT count(*) FROM audit_log WHERE action = 'node.enroll_token'`).Scan(&audited)
	if audited != 1 {
		t.Errorf("node.enroll_token audit rows = %d, want 1", audited)
	}
	var leaked int
	_ = env.store.Read().QueryRow(
		`SELECT count(*) FROM audit_log
		  WHERE COALESCE(after_json,'') LIKE ? OR COALESCE(before_json,'') LIKE ?`,
		"%"+body.Token+"%", "%"+body.Token+"%").Scan(&leaked)
	if leaked != 0 {
		t.Errorf("the enrollment token was written into %d audit rows, want 0", leaked)
	}
}

func TestCreateServiceBumpsRevision(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	token := env.login(t, "root", "pw")
	env.post(t, "/api/v1/nodes", `{"name":"de-1","address":"1.2.3.4"}`, token)

	res := env.post(t, "/api/v1/nodes/1/services",
		`{"adapter_kind":"stub","params":{"port":443}}`, token)
	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body)
	}

	var desired, applied int64
	if err := env.store.Read().QueryRow(
		`SELECT desired_revision, applied_revision FROM nodes WHERE id = 1`,
	).Scan(&desired, &applied); err != nil {
		t.Fatalf("read node: %v", err)
	}
	if desired != 1 {
		t.Errorf("desired_revision = %d, want 1", desired)
	}
	if applied != 0 {
		t.Errorf("applied_revision = %d, want 0 — nothing has converged yet", applied)
	}
}

func TestServiceParamsAreValidatedAgainstTheSchema(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	token := env.login(t, "root", "pw")
	env.post(t, "/api/v1/nodes", `{"name":"de-1","address":"1.2.3.4"}`, token)

	for _, bad := range []string{
		`{"adapter_kind":"stub","params":{}}`,                       // missing port
		`{"adapter_kind":"stub","params":{"port":"443"}}`,           // wrong type
		`{"adapter_kind":"stub","params":{"port":99999}}`,           // out of range
		`{"adapter_kind":"stub","params":{"port":443,"junk":true}}`, // extra property
	} {
		res := env.post(t, "/api/v1/nodes/1/services", bad, token)
		if res.Code != http.StatusUnprocessableEntity {
			t.Errorf("body %s gave status %d, want 422", bad, res.Code)
		}
	}

	// A rejected write must not have moved the node's desired state.
	var desired int64
	_ = env.store.Read().QueryRow(`SELECT desired_revision FROM nodes WHERE id = 1`).Scan(&desired)
	if desired != 0 {
		t.Errorf("desired_revision = %d after four rejections, want 0", desired)
	}
}

func TestUnknownAdapterKindIsRejected(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	token := env.login(t, "root", "pw")
	env.post(t, "/api/v1/nodes", `{"name":"de-1","address":"1.2.3.4"}`, token)

	res := env.post(t, "/api/v1/nodes/1/services",
		`{"adapter_kind":"wireguard","params":{}}`, token)
	if res.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 for an unknown adapter", res.Code)
	}
}

func TestDeleteNodeIsAuditedAndRemovesTheFingerprint(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "root", "pw", "super_admin")
	token := env.login(t, "root", "pw")
	env.post(t, "/api/v1/nodes", `{"name":"de-1","address":"1.2.3.4"}`, token)

	if res := env.do(t, http.MethodDelete, "/api/v1/nodes/1", "", token); res.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", res.Code)
	}
	var remaining int
	_ = env.store.Read().QueryRow(`SELECT count(*) FROM nodes`).Scan(&remaining)
	if remaining != 0 {
		t.Errorf("nodes remaining = %d, want 0", remaining)
	}
	var audited int
	_ = env.store.Read().QueryRow(
		`SELECT count(*) FROM audit_log WHERE action = 'node.delete'`).Scan(&audited)
	if audited != 1 {
		t.Errorf("node.delete audit rows = %d, want 1", audited)
	}
}
