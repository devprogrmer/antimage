package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// seedTenant creates a reseller admin, their reseller record, and one subject
// they own. Returns the admin's token and the owned subject id.
//
// Ownership is written directly rather than through the provisioning API,
// because the point of this test is the READ path: whatever created the row,
// one tenant must not be able to see another's.
func seedTenant(t *testing.T, env *testEnv, who string, svcID int64, adminToken string) (string, int64) {
	t.Helper()
	return seedTenantWithRole(t, env, who, "reseller", svcID, adminToken)
}

// seedTenantWithRole builds a tenant that owns one subject, under any role.
//
// The role is a parameter because scope and permission are independent axes and
// the interesting actor for permission tests sits where they disagree: someone
// who owns subjects (non-empty scope) but holds no subject:write. Hardcoding
// "reseller" cannot express that, since the reseller role has write.
func seedTenantWithRole(
	t *testing.T, env *testEnv, who, role string, svcID int64, adminToken string,
) (string, int64) {
	t.Helper()
	env.seedAdmin(t, who, "pw", role)
	token := env.login(t, who, "pw")

	// The subject is created by the super admin so that creation itself is not
	// what is under test.
	body := `{"name":"` + who + `-customer","service_ids":[` + itoa64(svcID) + `]}`
	res := env.post(t, "/api/v1/subjects", body, adminToken)
	if res.Code != http.StatusCreated {
		t.Fatalf("create %s customer: %d %s", who, res.Code, res.Body)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	_ = json.NewDecoder(res.Body).Decode(&created)
	if created.ID == 0 {
		t.Fatalf("no subject id returned for %s", who)
	}

	now := time.Now().Unix()
	err := env.store.Write(context.Background(), func(tx *sql.Tx) error {
		var adminID int64
		if err := tx.QueryRow(
			`SELECT id FROM admins WHERE username = ?`, who).Scan(&adminID); err != nil {
			return err
		}
		res, err := tx.Exec(
			`INSERT INTO resellers (admin_id, display_name, enabled, credit_floor,
			                        created_at, updated_at)
			 VALUES (?,?,1,0,?,?)`, adminID, who+"-vpn", now, now)
		if err != nil {
			return err
		}
		resellerID, _ := res.LastInsertId()
		_, err = tx.Exec(
			`INSERT INTO reseller_subjects (subject_id, reseller_id, cost, created_at)
			 VALUES (?,?,100,?)`, created.ID, resellerID, now)
		return err
	})
	if err != nil {
		t.Fatalf("seed tenant %s: %v", who, err)
	}
	return token, created.ID
}

func listedIDs(t *testing.T, env *testEnv, token string) []int64 {
	t.Helper()
	res := env.get(t, "/api/v1/subjects", token)
	if res.Code != http.StatusOK {
		t.Fatalf("list: %d %s", res.Code, res.Body)
	}
	var out struct {
		Subjects []struct {
			ID int64 `json:"id"`
		} `json:"subjects"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	ids := make([]int64, 0, len(out.Subjects))
	for _, s := range out.Subjects {
		ids = append(ids, s.ID)
	}
	return ids
}

// End to end: one reseller must never see another's customers through the API.
//
// The store-layer tests prove the predicate is correct. This proves the
// handlers actually apply it -- the gap that existed until List and Get took a
// scope as a required argument.
func TestAPIListNeverLeaksAcrossTenants(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)

	aliceToken, aliceSubject := seedTenant(t, env, "alice", svcID, adminToken)
	bobToken, bobSubject := seedTenant(t, env, "bob", svcID, adminToken)

	alice := listedIDs(t, env, aliceToken)
	if len(alice) != 1 || alice[0] != aliceSubject {
		t.Errorf("alice sees %v, want exactly [%d]", alice, aliceSubject)
	}
	for _, id := range alice {
		if id == bobSubject {
			t.Error("alice can see bob's customer in the subject list")
		}
	}

	bob := listedIDs(t, env, bobToken)
	if len(bob) != 1 || bob[0] != bobSubject {
		t.Errorf("bob sees %v, want exactly [%d]", bob, bobSubject)
	}

	// The super admin still sees both, so the filter is real rather than
	// everything being hidden from everyone.
	all := listedIDs(t, env, adminToken)
	if len(all) < 2 {
		t.Fatalf("super admin sees %v; the test would be vacuous", all)
	}
}

// Reading another tenant's customer must be indistinguishable from reading one
// that does not exist. Both are 404.
//
// A 403 here would confirm the id is real, letting a reseller count a
// competitor's customers by walking the id space.
func TestAPIForeignSubjectIsNotFoundNotForbidden(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	aliceToken, _ := seedTenant(t, env, "alice", svcID, adminToken)
	_, bobSubject := seedTenant(t, env, "bob", svcID, adminToken)

	foreign := env.get(t, "/api/v1/subjects/"+itoa64(bobSubject), aliceToken)
	missing := env.get(t, "/api/v1/subjects/9999999", aliceToken)

	if foreign.Code != http.StatusNotFound {
		t.Errorf("reading another tenant's subject returned %d, want 404", foreign.Code)
	}
	if missing.Code != http.StatusNotFound {
		t.Errorf("reading a missing subject returned %d, want 404", missing.Code)
	}
	if foreign.Code != missing.Code {
		t.Errorf("foreign=%d missing=%d are distinguishable; a tenant can probe "+
			"for the existence of another tenant's customers",
			foreign.Code, missing.Code)
	}
	if strings.TrimSpace(foreign.Body.String()) != strings.TrimSpace(missing.Body.String()) {
		t.Errorf("response bodies differ:\n foreign: %s\n missing: %s",
			foreign.Body.String(), missing.Body.String())
	}
}

// A platform-owned subject -- one with no reseller_subjects row -- must be
// invisible to every tenant, not visible to all of them.
func TestAPIPlatformOwnedSubjectsAreInvisibleToTenants(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	aliceToken, _ := seedTenant(t, env, "alice", svcID, adminToken)

	// Created by the super admin and never assigned an owner.
	res := env.post(t, "/api/v1/subjects",
		`{"name":"house-account","service_ids":[`+itoa64(svcID)+`]}`, adminToken)
	if res.Code != http.StatusCreated {
		t.Fatalf("create house account: %d %s", res.Code, res.Body)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	_ = json.NewDecoder(res.Body).Decode(&created)

	for _, id := range listedIDs(t, env, aliceToken) {
		if id == created.ID {
			t.Error("a tenant can see a platform-owned subject; an unowned subject " +
				"must not default to being everybody's")
		}
	}
	if got := env.get(t, "/api/v1/subjects/"+itoa64(created.ID), aliceToken); got.Code != http.StatusNotFound {
		t.Errorf("tenant read a platform-owned subject: %d", got.Code)
	}
}

// The credential reveal endpoint must not become a way around the list filter.
//
// It is the highest-value endpoint in the panel: it returns the secret a user
// connects with. A tenant who cannot see a subject must not be able to reveal
// its credential by id.
func TestAPICredentialRevealIsTenantScoped(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	aliceToken, _ := seedTenant(t, env, "alice", svcID, adminToken)
	_, bobSubject := seedTenant(t, env, "bob", svcID, adminToken)

	res := env.get(t, "/api/v1/subjects/"+itoa64(bobSubject)+"/credentials/uuid", aliceToken)
	if res.Code == http.StatusOK {
		t.Fatalf("SECURITY: alice revealed bob's customer credential: %s", res.Body)
	}
	if res.Code != http.StatusNotFound {
		t.Errorf("reveal of a foreign credential returned %d, want 404 "+
			"(indistinguishable from missing)", res.Code)
	}
}

// Mutations must be scoped too. A reseller who cannot see a subject must not
// be able to edit, delete, or rotate it either.
//
// Read isolation alone is not enough: an unscoped write path lets one tenant
// disable or delete a competitor's customers without ever reading them.
func TestAPIMutationsAreTenantScoped(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	aliceToken, aliceSubject := seedTenant(t, env, "alice", svcID, adminToken)
	_, bobSubject := seedTenant(t, env, "bob", svcID, adminToken)

	foreign := itoa64(bobSubject)
	cases := []struct{ name, method, path, body string }{
		{"update", http.MethodPut, "/api/v1/subjects/" + foreign, `{"enabled":false}`},
		{"delete", http.MethodDelete, "/api/v1/subjects/" + foreign, ""},
		{"rotate", http.MethodPost,
			"/api/v1/subjects/" + foreign + "/credentials/uuid/rotate", "{}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := env.do(t, tc.method, tc.path, tc.body, aliceToken)
			if res.Code != http.StatusNotFound {
				t.Errorf("%s on a foreign subject returned %d, want 404", tc.name, res.Code)
			}
		})
	}

	// Bob's customer is untouched.
	if res := env.get(t, "/api/v1/subjects/"+foreign, adminToken); res.Code != http.StatusOK {
		t.Fatalf("bob's subject no longer readable by admin: %d", res.Code)
	}
	var got struct {
		Enabled bool `json:"enabled"`
	}
	_ = json.NewDecoder(env.get(t, "/api/v1/subjects/"+foreign, adminToken).Body).Decode(&got)
	if !got.Enabled {
		t.Error("alice disabled bob's customer through an unscoped write path")
	}

	// And alice can still act on her OWN subject, so the guard is not simply
	// refusing everything.
	own := env.do(t, http.MethodPut, "/api/v1/subjects/"+itoa64(aliceSubject),
		`{"note":"mine"}`, aliceToken)
	if own.Code != http.StatusNoContent && own.Code != http.StatusOK {
		t.Errorf("alice cannot edit her own customer: %d %s", own.Code, own.Body)
	}
}
