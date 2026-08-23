package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

// Every remaining subject-bearing endpoint must be tenant-scoped.
//
// Scoping list, get, update, delete, reveal and rotate was not enough: this
// suite found five more live leaks (devices, connections, enforcement read
// 200; disable and freeze acted with 204) plus bulk operations that reached
// SQL against foreign ids. It exists so the next endpoint added to this
// surface is checked rather than assumed.
func TestEverySubjectEndpointIsTenantScoped(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	aliceToken, _ := seedTenant(t, env, "alice", svcID, adminToken)
	_, bobSubject := seedTenant(t, env, "bob", svcID, adminToken)
	foreign := itoa64(bobSubject)

	t.Run("export", func(t *testing.T) {
		res := env.get(t, "/api/v1/subjects/export", aliceToken)
		body := res.Body.String()
		t.Logf("export -> %d (%d bytes)", res.Code, len(body))
		if res.Code == http.StatusOK && strings.Contains(body, "bob-customer") {
			t.Error("LEAK: export returned another tenant's customer")
		}
	})

	for _, tc := range []struct{ name, path, body string }{
		{"bulk/enable", "/api/v1/subjects/bulk/enable", `{"subject_ids":[` + foreign + `],"enabled":false}`},
		{"bulk/delete", "/api/v1/subjects/bulk/delete", `{"subject_ids":[` + foreign + `]}`},
		{"bulk/set-quota", "/api/v1/subjects/bulk/set-quota", `{"subject_ids":[` + foreign + `],"quota_bytes":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := env.post(t, tc.path, tc.body, aliceToken)
			t.Logf("%s -> %d %s", tc.name, res.Code, strings.TrimSpace(res.Body.String()))
			body := res.Body.String()
			if !strings.Contains(body, "\"failed\":0") || strings.Contains(body, "\"enabled\":1") || strings.Contains(body, "\"deleted\":1") || strings.Contains(body, "\"updated\":1") {
				t.Errorf("LEAK: %s affected another tenant: %s", tc.name, body)
			}
		})
	}

	for _, tc := range []struct{ name, path string }{
		{"devices", "/api/v1/subjects/" + foreign + "/devices"},
		{"connections", "/api/v1/subjects/" + foreign + "/connections"},
		{"enforcement", "/api/v1/subjects/" + foreign + "/enforcement"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := env.get(t, tc.path, aliceToken)
			t.Logf("%s -> %d", tc.name, res.Code)
			if res.Code == http.StatusOK {
				t.Errorf("LEAK: %s exposed another tenant's customer data", tc.name)
			}
		})
	}

	for _, tc := range []struct{ name, path string }{
		{"disable", "/api/v1/subjects/" + foreign + "/disable"},
		{"freeze", "/api/v1/subjects/" + foreign + "/freeze"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := env.post(t, tc.path, "{}", aliceToken)
			t.Logf("%s -> %d", tc.name, res.Code)
			if res.Code == http.StatusOK || res.Code == http.StatusNoContent {
				t.Errorf("LEAK: %s acted on another tenant's customer", tc.name)
			}
		})
	}
}
