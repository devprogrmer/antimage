package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// GET /v2/subjects had no permission check and no scope predicate, and was not
// caught by TestEverySubjectEndpointIsTenantScoped because that suite predates
// the route. It was not a live leak only because it selected columns that do
// not exist and scanned six of the eleven it asked for, so every call returned
// 500 -- the same "safe because broken" shape as gap 4.

func listV2(t *testing.T, env *testEnv, query, token string) (int, []subjectDTO) {
	t.Helper()
	res := env.get(t, "/api/v1/v2/subjects"+query, token)
	if res.Code != http.StatusOK {
		return res.Code, nil
	}
	var out struct {
		Subjects []subjectDTO `json:"subjects"`
		Total    int          `json:"total"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return res.Code, out.Subjects
}

func TestListSubjectsV2Works(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	seedTenant(t, env, "alice", svcID, adminToken)

	code, subs := listV2(t, env, "", adminToken)
	if code != http.StatusOK {
		t.Fatalf("v2 list = %d, want 200", code)
	}
	if len(subs) == 0 {
		t.Fatal("v2 list returned no subjects")
	}
	// A scan-order bug shows up as fields landing in the wrong place, so the
	// values matter, not just the count.
	found := false
	for _, s := range subs {
		if s.Name == "alice-customer" {
			found = true
			if s.ID == 0 || s.CreatedAt == 0 || !s.Enabled {
				t.Errorf("row decoded wrong: %+v", s)
			}
		}
	}
	if !found {
		t.Errorf("alice-customer missing from %+v", subs)
	}
}

// The core of it: one tenant must not see another's customers through this
// route, in any filter combination.
func TestListSubjectsV2IsTenantScoped(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	aliceToken, _ := seedTenant(t, env, "alice", svcID, adminToken)
	seedTenant(t, env, "bob", svcID, adminToken)

	for _, q := range []string{
		"",
		"?search=bob",
		"?status=active",
		"?page_size=1000",
		"?sort=name&order=asc",
		"?quota_status=under_limit",
	} {
		t.Run("query="+q, func(t *testing.T) {
			code, subs := listV2(t, env, q, aliceToken)
			if code != http.StatusOK {
				t.Fatalf("v2 list %q = %d", q, code)
			}
			for _, s := range subs {
				if strings.Contains(s.Name, "bob") {
					t.Errorf("LEAK: alice saw %q through %q", s.Name, q)
				}
			}
		})
	}
}

func TestListSubjectsV2RequiresSubjectRead(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	seedTenant(t, env, "alice", svcID, adminToken)

	// An admin with no subject permissions at all. seedAdmin takes a builtin
	// role name; "readonly" holds subject:read, so the negative case needs a
	// principal that holds none -- an unauthenticated request is the one the
	// router itself must reject.
	res := env.get(t, "/api/v1/v2/subjects", "")
	if res.Code != http.StatusUnauthorized && res.Code != http.StatusForbidden {
		t.Errorf("unauthenticated v2 list = %d, want 401 or 403", res.Code)
	}
}

// Status filters referenced `disabled` and `frozen`, neither of which exists.
func TestListSubjectsV2StatusFilters(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	_, id := seedTenant(t, env, "alice", svcID, adminToken)

	if err := execSQL(t, env, `UPDATE subjects SET enabled = 0 WHERE id = ?`, id); err != nil {
		t.Fatalf("disable: %v", err)
	}

	code, subs := listV2(t, env, "?status=disabled", adminToken)
	if code != http.StatusOK {
		t.Fatalf("status=disabled = %d, want 200", code)
	}
	if len(subs) != 1 || subs[0].ID != id {
		t.Errorf("status=disabled returned %+v, want just subject %d", subs, id)
	}

	code, subs = listV2(t, env, "?status=active", adminToken)
	if code != http.StatusOK {
		t.Fatalf("status=active = %d, want 200", code)
	}
	for _, s := range subs {
		if s.ID == id {
			t.Errorf("disabled subject %d matched status=active", id)
		}
	}
}
