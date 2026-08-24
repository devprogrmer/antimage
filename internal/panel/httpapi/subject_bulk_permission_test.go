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
