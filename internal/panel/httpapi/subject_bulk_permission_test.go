package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

// The bulk endpoints must check permission, not merely scope.
//
// This is gap 2 from docs/TENANT-ISOLATION.md. The two guards answer different
// questions: scope decides WHICH subjects a caller may touch, permission
// decides whether they may perform the operation at all. The bulk handlers had
// only the first, and that gap is invisible to the tenant-isolation suite --
// a reseller carrying subject:write passes both checks, so those tests stayed
// green either way.
//
// The actor that exposes it owns a subject but holds no subject:write. With the
// permission gate missing, the scope filter waves its own id straight through
// and the mutation runs. Reverting either authorize() call in
// subjects_bulk_operations.go or subjects_bulk_delete.go turns the 403s below
// back into 200s and fails this test by name.
func TestBulkEndpointsRequireSubjectWrite(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)

	// readonly holds subject:read but not subject:write, and owning a subject
	// gives it a non-empty scope -- so the scope filter alone cannot stop it.
	readerToken, ownSubject := seedTenantWithRole(
		t, env, "auditor", "readonly", svcID, adminToken)
	own := itoa64(ownSubject)

	for _, tc := range []struct{ name, path, body string }{
		{"bulk/enable", "/api/v1/subjects/bulk/enable", `{"subject_ids":[` + own + `]}`},
		{"bulk/disable", "/api/v1/subjects/bulk/disable", `{"subject_ids":[` + own + `]}`},
		{"bulk/delete", "/api/v1/subjects/bulk/delete", `{"subject_ids":[` + own + `]}`},
		{"bulk/extend", "/api/v1/subjects/bulk/extend", `{"subject_ids":[` + own + `],"days":30}`},
		{"bulk/reset-traffic", "/api/v1/subjects/bulk/reset-traffic", `{"subject_ids":[` + own + `]}`},
		{"bulk/set-quota", "/api/v1/subjects/bulk/set-quota", `{"subject_ids":[` + own + `],"quota_bytes":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := env.post(t, tc.path, tc.body, readerToken)
			t.Logf("%s -> %d %s", tc.name, res.Code, strings.TrimSpace(res.Body.String()))
			if res.Code != http.StatusForbidden {
				t.Errorf(
					"UNAUTHORIZED WRITE: %s returned %d for an actor without subject:write, want %d",
					tc.name, res.Code, http.StatusForbidden)
			}
		})
	}
}

// The gate must reject before reading the body, so a caller without permission
// cannot tell a malformed request from a well-formed one, and cannot use the
// difference between 400 and 200 to probe which subject ids exist.
func TestBulkPermissionCheckedBeforeRequestBody(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	readerToken, _ := seedTenantWithRole(
		t, env, "auditor", "readonly", svcID, adminToken)

	res := env.post(t, "/api/v1/subjects/bulk/delete", `{not json`, readerToken)
	t.Logf("malformed body as readonly -> %d %s", res.Code, strings.TrimSpace(res.Body.String()))
	if res.Code != http.StatusForbidden {
		t.Errorf("want %d before the body is parsed, got %d",
			http.StatusForbidden, res.Code)
	}
}

// An actor that does hold subject:write must still get through, so the new gate
// cannot be mistaken for a blanket denial that would pass the test above while
// breaking the feature.
func TestBulkEndpointsStillAllowSubjectWrite(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	writerToken, ownSubject := seedTenantWithRole(
		t, env, "vendor", "reseller", svcID, adminToken)

	res := env.post(t, "/api/v1/subjects/bulk/enable",
		`{"subject_ids":[`+itoa64(ownSubject)+`]}`, writerToken)
	t.Logf("bulk/enable as reseller -> %d %s", res.Code, strings.TrimSpace(res.Body.String()))
	if res.Code == http.StatusForbidden {
		t.Errorf("permission gate rejected an actor holding subject:write: %d %s",
			res.Code, res.Body.String())
	}
}

// bulk/disable is new, so it gets a functional test as well as a permission
// one: the menu that drives it used to offer an action against a route that did
// not exist, and a 403-only test would pass just as well against a handler that
// answered 200 and did nothing.
func TestBulkDisableActuallyDisables(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	writerToken, ownSubject := seedTenantWithRole(
		t, env, "vendor2", "reseller", svcID, adminToken)
	id := itoa64(ownSubject)

	res := env.post(t, "/api/v1/subjects/bulk/disable",
		`{"subject_ids":[`+id+`]}`, writerToken)
	if res.Code != http.StatusOK {
		t.Fatalf("bulk/disable -> %d %s", res.Code, strings.TrimSpace(res.Body.String()))
	}
	if body := res.Body.String(); !strings.Contains(body, `"disabled":1`) {
		t.Errorf("bulk/disable reported %s, want disabled:1", strings.TrimSpace(body))
	}

	// And it must be visible on the subject, not just in the response count.
	got := env.get(t, "/api/v1/subjects/"+id, writerToken)
	if got.Code != http.StatusOK {
		t.Fatalf("get subject -> %d", got.Code)
	}
	if body := got.Body.String(); !strings.Contains(body, `"enabled":false`) {
		t.Errorf("after bulk/disable the subject reads %s, want enabled:false",
			strings.TrimSpace(body))
	}

	// Symmetry: enable brings it back, so the pair is usable as a pair.
	if res := env.post(t, "/api/v1/subjects/bulk/enable",
		`{"subject_ids":[`+id+`]}`, writerToken); res.Code != http.StatusOK {
		t.Fatalf("bulk/enable -> %d %s", res.Code, strings.TrimSpace(res.Body.String()))
	}
	got = env.get(t, "/api/v1/subjects/"+id, writerToken)
	if body := got.Body.String(); !strings.Contains(body, `"enabled":true`) {
		t.Errorf("after bulk/enable the subject reads %s, want enabled:true",
			strings.TrimSpace(body))
	}
}
