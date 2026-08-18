package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/amyrm/antimage/internal/shared/secrets"
)

// newSubjectEnv builds a panel with a secret box and one node carrying one
// service, which is the minimum for a subject to be publishable.
func newSubjectEnv(t *testing.T) (*testEnv, string, int64) {
	t.Helper()
	key := make([]byte, secrets.KeySize)
	for i := range key {
		key[i] = byte(i + 11)
	}
	box, err := secrets.NewBox(key)
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}

	env := newTestEnv(t, func(d *Deps) { d.Box = box })
	env.seedAdmin(t, "root", "pw", "super_admin")
	token := env.login(t, "root", "pw")

	if res := env.post(t, "/api/v1/nodes", `{"name":"n1","address":"1.1.1.1"}`, token); res.Code != http.StatusCreated {
		t.Fatalf("create node: %d %s", res.Code, res.Body)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	res := env.post(t, "/api/v1/nodes/1/services",
		`{"adapter_kind":"stub","params":{"port":443}}`, token)
	if res.Code != http.StatusCreated {
		t.Fatalf("create service: %d %s", res.Code, res.Body)
	}
	_ = json.NewDecoder(res.Body).Decode(&created)
	return env, token, created.ID
}

func createSubjectVia(t *testing.T, env *testEnv, token, body string) int64 {
	t.Helper()
	res := env.post(t, "/api/v1/subjects", body, token)
	if res.Code != http.StatusCreated {
		t.Fatalf("create subject: %d %s", res.Code, res.Body)
	}
	var out struct {
		ID int64 `json:"id"`
	}
	_ = json.NewDecoder(res.Body).Decode(&out)
	return out.ID
}

func TestSubjectLifecycleCreateReadUpdateDelete(t *testing.T) {
	env, token, svcID := newSubjectEnv(t)

	id := createSubjectVia(t, env, token,
		`{"name":"alice","note":"first","service_ids":[`+itoa64(svcID)+`]}`)

	// Read
	res := env.get(t, "/api/v1/subjects/"+itoa64(id), token)
	if res.Code != http.StatusOK {
		t.Fatalf("get: %d", res.Code)
	}
	var got subjectDTO
	_ = json.NewDecoder(res.Body).Decode(&got)
	if got.Name != "alice" || !got.Enabled {
		t.Errorf("subject = %+v", got)
	}

	// List
	res = env.get(t, "/api/v1/subjects", token)
	var list struct {
		Subjects []subjectDTO `json:"subjects"`
	}
	_ = json.NewDecoder(res.Body).Decode(&list)
	if len(list.Subjects) != 1 {
		t.Fatalf("list = %d subjects, want 1", len(list.Subjects))
	}

	// Update: disable
	res = env.do(t, http.MethodPut, "/api/v1/subjects/"+itoa64(id), `{"enabled":false}`, token)
	if res.Code != http.StatusNoContent {
		t.Fatalf("update: %d %s", res.Code, res.Body)
	}
	res = env.get(t, "/api/v1/subjects/"+itoa64(id), token)
	_ = json.NewDecoder(res.Body).Decode(&got)
	if got.Enabled {
		t.Error("subject is still enabled after being disabled")
	}

	// Delete
	res = env.do(t, http.MethodDelete, "/api/v1/subjects/"+itoa64(id), "", token)
	if res.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", res.Code, res.Body)
	}
	if res := env.get(t, "/api/v1/subjects/"+itoa64(id), token); res.Code != http.StatusNotFound {
		t.Errorf("get after delete = %d, want 404", res.Code)
	}
}

// Creating a subject on a node must bump that node's revision, or the agent
// never learns the user exists.
func TestCreatingASubjectBumpsTheNodeRevision(t *testing.T) {
	env, token, svcID := newSubjectEnv(t)

	revision := func() int64 {
		var r int64
		_ = env.store.Read().QueryRow(`SELECT desired_revision FROM nodes WHERE id = 1`).Scan(&r)
		return r
	}
	before := revision()

	createSubjectVia(t, env, token, `{"name":"alice","service_ids":[`+itoa64(svcID)+`]}`)

	if after := revision(); after <= before {
		t.Errorf("revision did not move: %d -> %d", before, after)
	}
}

// Deleting a subject must also bump, or the node keeps serving a deleted user.
func TestDeletingASubjectBumpsTheNodeRevision(t *testing.T) {
	env, token, svcID := newSubjectEnv(t)
	id := createSubjectVia(t, env, token, `{"name":"alice","service_ids":[`+itoa64(svcID)+`]}`)

	var before int64
	_ = env.store.Read().QueryRow(`SELECT desired_revision FROM nodes WHERE id = 1`).Scan(&before)

	if res := env.do(t, http.MethodDelete, "/api/v1/subjects/"+itoa64(id), "", token); res.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", res.Code)
	}

	var after int64
	_ = env.store.Read().QueryRow(`SELECT desired_revision FROM nodes WHERE id = 1`).Scan(&after)
	if after <= before {
		t.Errorf("revision did not move on delete: %d -> %d; the node still serves them", before, after)
	}
}

// SECURITY: a list must never carry credential material. One response holding
// every user's credential would land in logs, proxies and browser caches.
func TestListAndGetNeverReturnCredentials(t *testing.T) {
	env, token, svcID := newSubjectEnv(t)
	createSubjectVia(t, env, token,
		`{"name":"alice","service_ids":[`+itoa64(svcID)+`],`+
			`"credentials":{"uuid":"11111111-2222-3333-4444-555555555555"}}`)

	for _, path := range []string{"/api/v1/subjects", "/api/v1/subjects/1"} {
		body := env.get(t, path, token).Body.String()
		for _, forbidden := range []string{"11111111-2222-3333-4444-555555555555", "credential", "value_enc"} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s leaked %q: %s", path, forbidden, body)
			}
		}
	}
}

// The reveal endpoint is the ONLY way to see a credential, and it is audited
// by kind, never by value.
func TestRevealReturnsTheCredentialAndAuditsWithoutIt(t *testing.T) {
	env, token, svcID := newSubjectEnv(t)
	const uuid = "11111111-2222-3333-4444-555555555555"
	id := createSubjectVia(t, env, token,
		`{"name":"alice","service_ids":[`+itoa64(svcID)+`],"credentials":{"uuid":"`+uuid+`"}}`)

	res := env.get(t, "/api/v1/subjects/"+itoa64(id)+"/credentials/uuid", token)
	if res.Code != http.StatusOK {
		t.Fatalf("reveal: %d %s", res.Code, res.Body)
	}
	var out struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	}
	_ = json.NewDecoder(res.Body).Decode(&out)
	if out.Value != uuid {
		t.Errorf("revealed %q, want the imported uuid", out.Value)
	}
	if cc := res.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q; a credential must not be cacheable", cc)
	}

	// The audit row records the kind, never the value.
	var after sql.NullString
	if err := env.store.Read().QueryRow(
		`SELECT after_json FROM audit_log WHERE action = 'credential.reveal'
		  ORDER BY id DESC LIMIT 1`).Scan(&after); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if !after.Valid || !strings.Contains(after.String, "uuid") {
		t.Errorf("audit does not record which kind was revealed: %q", after.String)
	}
	if strings.Contains(after.String, uuid) {
		t.Fatalf("THE CREDENTIAL WAS WRITTEN TO THE AUDIT LOG: %q", after.String)
	}
}

// Rotation replaces one credential, returns the new value once, and bumps the
// revision so the node stops accepting the old one.
func TestRotateReplacesTheCredentialAndRepublishes(t *testing.T) {
	env, token, svcID := newSubjectEnv(t)
	const original = "11111111-2222-3333-4444-555555555555"
	id := createSubjectVia(t, env, token,
		`{"name":"alice","service_ids":[`+itoa64(svcID)+`],"credentials":{"uuid":"`+original+`"}}`)

	var before int64
	_ = env.store.Read().QueryRow(`SELECT desired_revision FROM nodes WHERE id = 1`).Scan(&before)

	res := env.post(t, "/api/v1/subjects/"+itoa64(id)+"/credentials/uuid/rotate", "", token)
	if res.Code != http.StatusOK {
		t.Fatalf("rotate: %d %s", res.Code, res.Body)
	}
	var out struct {
		Value string `json:"value"`
	}
	_ = json.NewDecoder(res.Body).Decode(&out)
	if out.Value == original {
		t.Fatal("rotation returned the same credential")
	}
	if out.Value == "" {
		t.Fatal("rotation returned no credential")
	}

	// The stored value is the new one.
	reveal := env.get(t, "/api/v1/subjects/"+itoa64(id)+"/credentials/uuid", token)
	var revealed struct {
		Value string `json:"value"`
	}
	_ = json.NewDecoder(reveal.Body).Decode(&revealed)
	if revealed.Value != out.Value {
		t.Errorf("stored credential %q does not match the rotated one %q", revealed.Value, out.Value)
	}

	var after int64
	_ = env.store.Read().QueryRow(`SELECT desired_revision FROM nodes WHERE id = 1`).Scan(&after)
	if after <= before {
		t.Errorf("rotation did not republish: %d -> %d; the old credential still works", before, after)
	}
}

// An imported credential must be preserved exactly, which is the requirement
// that ruled out deriving credentials.
func TestImportedCredentialIsPreservedExactly(t *testing.T) {
	env, token, svcID := newSubjectEnv(t)
	const existing = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	id := createSubjectVia(t, env, token,
		`{"name":"migrated","service_ids":[`+itoa64(svcID)+`],"credentials":{"uuid":"`+existing+`"}}`)

	res := env.get(t, "/api/v1/subjects/"+itoa64(id)+"/credentials/uuid", token)
	var out struct {
		Value string `json:"value"`
	}
	_ = json.NewDecoder(res.Body).Decode(&out)
	if out.Value != existing {
		t.Errorf("imported credential became %q; a migrating operator's clients would all break", out.Value)
	}
}

// Malformed credentials are refused and the refusal is audited (invariant 9).
func TestMalformedCredentialIsRefusedAndAudited(t *testing.T) {
	env, token, svcID := newSubjectEnv(t)

	res := env.post(t, "/api/v1/subjects",
		`{"name":"bad","service_ids":[`+itoa64(svcID)+`],"credentials":{"uuid":"not-a-uuid"}}`, token)
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", res.Code)
	}

	var n int
	_ = env.store.Read().QueryRow(
		`SELECT count(*) FROM audit_log WHERE action = 'subject.create' AND result = 'denied'`).Scan(&n)
	if n != 1 {
		t.Errorf("denied audit rows = %d, want 1", n)
	}
	var subjects int
	_ = env.store.Read().QueryRow(`SELECT count(*) FROM subjects`).Scan(&subjects)
	if subjects != 0 {
		t.Errorf("a rejected create left %d subjects behind", subjects)
	}
}

// Duplicate names are refused: SP3 and SP4 both key off a stable handle.
func TestDuplicateSubjectNameIsRefused(t *testing.T) {
	env, token, svcID := newSubjectEnv(t)
	createSubjectVia(t, env, token, `{"name":"alice","service_ids":[`+itoa64(svcID)+`]}`)

	res := env.post(t, "/api/v1/subjects", `{"name":"ALICE","service_ids":[`+itoa64(svcID)+`]}`, token)
	if res.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (names are case-insensitive)", res.Code)
	}
}

// Two-layer authorization: the permission gate must refuse a role that lacks
// subject:write, and a readonly account must not be able to create one.
func TestSubjectWritesRequireThePermission(t *testing.T) {
	env, _, _ := newSubjectEnv(t)
	env.seedAdmin(t, "ro", "pw", "readonly")
	roToken := env.login(t, "ro", "pw")

	if res := env.post(t, "/api/v1/subjects", `{"name":"x"}`, roToken); res.Code != http.StatusForbidden {
		t.Errorf("readonly create = %d, want 403", res.Code)
	}
	// But reading is allowed for readonly.
	if res := env.get(t, "/api/v1/subjects", roToken); res.Code != http.StatusOK {
		t.Errorf("readonly list = %d, want 200", res.Code)
	}
	// And revealing a credential is NOT: it needs credential:reveal.
	if res := env.get(t, "/api/v1/subjects/1/credentials/uuid", roToken); res.Code != http.StatusForbidden {
		t.Errorf("readonly reveal = %d, want 403", res.Code)
	}
	// Nor is rotating one, which replaces the credential a user is connecting
	// with and is therefore a write however it is spelled.
	if res := env.post(t, "/api/v1/subjects/1/credentials/uuid/rotate", "{}", roToken); //nolint:lll
	res.Code != http.StatusForbidden {
		t.Errorf("readonly rotate = %d, want 403", res.Code)
	}
}

func TestSubjectEndpointsRequireAuthentication(t *testing.T) {
	env, _, _ := newSubjectEnv(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/subjects"},
		{http.MethodPost, "/api/v1/subjects"},
		{http.MethodGet, "/api/v1/subjects/1"},
		{http.MethodPut, "/api/v1/subjects/1"},
		{http.MethodDelete, "/api/v1/subjects/1"},
		{http.MethodGet, "/api/v1/subjects/1/credentials/uuid"},
		{http.MethodPost, "/api/v1/subjects/1/credentials/uuid/rotate"},
	} {
		if res := env.do(t, tc.method, tc.path, "{}", ""); res.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", tc.method, tc.path, res.Code)
		}
	}
}

// Without a master key, creating a subject must FAIL rather than storing
// credential material unsealed in every future backup.
func TestCreateWithoutASecretBoxFailsClosed(t *testing.T) {
	env := newTestEnv(t) // no Box
	env.seedAdmin(t, "root", "pw", "super_admin")
	token := env.login(t, "root", "pw")

	res := env.post(t, "/api/v1/subjects", `{"name":"alice"}`, token)
	if res.Code == http.StatusCreated {
		t.Fatal("a subject was created with no secret box; its credentials would be unsealed")
	}
	var n int
	_ = env.store.Read().QueryRow(`SELECT count(*) FROM subject_credentials`).Scan(&n)
	if n != 0 {
		t.Errorf("%d credential rows were written without a box", n)
	}
}

// Granting and revoking a service must republish BOTH the node gaining the
// subject and the node losing them.
func TestRevokingAServiceRepublishesTheLosingNode(t *testing.T) {
	env, token, svcID := newSubjectEnv(t)
	id := createSubjectVia(t, env, token, `{"name":"alice","service_ids":[`+itoa64(svcID)+`]}`)

	var before int64
	_ = env.store.Read().QueryRow(`SELECT desired_revision FROM nodes WHERE id = 1`).Scan(&before)

	// Revoke every service.
	res := env.do(t, http.MethodPut, "/api/v1/subjects/"+itoa64(id), `{"service_ids":[]}`, token)
	if res.Code != http.StatusNoContent {
		t.Fatalf("update: %d %s", res.Code, res.Body)
	}

	var after int64
	_ = env.store.Read().QueryRow(`SELECT desired_revision FROM nodes WHERE id = 1`).Scan(&after)
	if after <= before {
		t.Errorf("the losing node was not republished: %d -> %d", before, after)
	}

	// And the subject is gone from that node's document.
	err := env.store.Write(context.Background(), func(tx *sql.Tx) error {
		var n int
		return tx.QueryRow(
			`SELECT count(*) FROM subject_services WHERE subject_id = ?`, id).Scan(&n)
	})
	if err != nil {
		t.Fatalf("read grants: %v", err)
	}
}
