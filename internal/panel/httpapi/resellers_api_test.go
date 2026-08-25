package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Tenancy over HTTP. The engine was already tested; these hold the boundaries
// the routes introduce.
//
// Three lines matter here, and each has its own test below:
//   - credit:grant is not implied by reseller:write, because minting credit is
//     the only operation that creates value from nothing
//   - the reseller role holds NO reseller:* permission, so a tenant reaches
//     their own record through /me and nobody else's through anything
//   - an out-of-scope tenant is 404, never 403

func seedResellerAdmin(t *testing.T, env *testEnv, username, role string) int64 {
	t.Helper()
	return env.seedAdmin(t, username, "pw", role)
}

func createReseller(t *testing.T, env *testEnv, token string, adminID int64, name string) int64 {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"admin_id": adminID, "display_name": name, "credit_floor": 0,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	res := env.post(t, "/api/v1/resellers", string(body), token)
	if res.Code != http.StatusCreated {
		t.Fatalf("create reseller = %d: %s", res.Code, res.Body)
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out.ID
}

func TestResellerLifecycle(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	operator := seedResellerAdmin(t, env, "vendor", "reseller")
	id := createReseller(t, env, adminToken, operator, "vendor-vpn")

	got := env.get(t, "/api/v1/resellers/"+itoa64(id), adminToken)
	if got.Code != http.StatusOK {
		t.Fatalf("get = %d: %s", got.Code, got.Body)
	}
	if !strings.Contains(got.Body.String(), "vendor-vpn") {
		t.Errorf("record does not carry the display name: %s", got.Body)
	}

	list := env.get(t, "/api/v1/resellers", adminToken)
	if !strings.Contains(list.Body.String(), "vendor-vpn") {
		t.Errorf("created reseller missing from the list: %s", list.Body)
	}

	upd := env.put(t, "/api/v1/resellers/"+itoa64(id),
		`{"display_name":"vendor-vpn-renamed","enabled":false}`, adminToken)
	if upd.Code != http.StatusNoContent {
		t.Fatalf("update = %d: %s", upd.Code, upd.Body)
	}
	after := env.get(t, "/api/v1/resellers/"+itoa64(id), adminToken)
	if !strings.Contains(after.Body.String(), "vendor-vpn-renamed") {
		t.Errorf("rename did not stick: %s", after.Body)
	}
}

// One admin operates one tenant. The UNIQUE index is what makes "my reseller"
// resolvable from a session alone, so a second attempt is a conflict rather
// than a silent second row.
func TestOneAdminOperatesOneReseller(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	operator := seedResellerAdmin(t, env, "vendor", "reseller")
	createReseller(t, env, adminToken, operator, "first")

	body, _ := json.Marshal(map[string]any{
		"admin_id": operator, "display_name": "second",
	})
	res := env.post(t, "/api/v1/resellers", string(body), adminToken)
	if res.Code != http.StatusConflict {
		t.Errorf("second reseller for one admin = %d, want 409: %s", res.Code, res.Body)
	}
}

// THE separation that matters. reseller:write renames a tenant; credit:grant
// mints money. The admin role holds the first and not the second, so an admin
// must be able to edit a tenant and unable to fund one.
func TestCreditGrantIsSeparateFromResellerWrite(t *testing.T) {
	env, superToken, _ := newSubjectEnv(t)
	operator := seedResellerAdmin(t, env, "vendor", "reseller")
	id := createReseller(t, env, superToken, operator, "vendor-vpn")

	seedResellerAdmin(t, env, "manager", "admin")
	adminToken := env.login(t, "manager", "pw")

	// Holds reseller:write, so editing works.
	if res := env.put(t, "/api/v1/resellers/"+itoa64(id),
		`{"display_name":"renamed-by-admin"}`, adminToken); res.Code != http.StatusNoContent {
		t.Fatalf("admin could not edit a tenant (%d): %s — the grant test below "+
			"would then prove nothing", res.Code, res.Body)
	}

	// Does not hold credit:grant, so funding must not.
	body := `{"delta":100000,"reason":"topup","idempotency_key":"k1"}`
	res := env.post(t, "/api/v1/resellers/"+itoa64(id)+"/credit", body, adminToken)
	if res.Code != http.StatusForbidden {
		t.Errorf("admin granted credit = %d, want 403: anyone who can rename a "+
			"tenant could pay themselves", res.Code)
	}

	// The super admin holds it.
	res = env.post(t, "/api/v1/resellers/"+itoa64(id)+"/credit", body, superToken)
	if res.Code != http.StatusCreated {
		t.Fatalf("super admin could not grant credit = %d: %s", res.Code, res.Body)
	}
	if !strings.Contains(res.Body.String(), `"balance":100000`) {
		t.Errorf("grant did not report the new balance: %s", res.Body)
	}
}

// A credit movement with no idempotency key cannot be retried safely: the
// retry mints the credit a second time. Required rather than generated.
func TestCreditRequiresAnIdempotencyKey(t *testing.T) {
	env, superToken, _ := newSubjectEnv(t)
	operator := seedResellerAdmin(t, env, "vendor", "reseller")
	id := createReseller(t, env, superToken, operator, "vendor-vpn")

	res := env.post(t, "/api/v1/resellers/"+itoa64(id)+"/credit",
		`{"delta":1000,"reason":"topup"}`, superToken)
	if res.Code != http.StatusBadRequest {
		t.Errorf("credit without a key = %d, want 400: %s", res.Code, res.Body)
	}
}

// Replaying the same key must not mint twice. The ledger's UNIQUE constraint
// is the guarantee; this proves the route reaches it.
func TestCreditReplayDoesNotMintTwice(t *testing.T) {
	env, superToken, _ := newSubjectEnv(t)
	operator := seedResellerAdmin(t, env, "vendor", "reseller")
	id := createReseller(t, env, superToken, operator, "vendor-vpn")

	body := `{"delta":500,"reason":"topup","idempotency_key":"same-key"}`
	env.post(t, "/api/v1/resellers/"+itoa64(id)+"/credit", body, superToken)
	env.post(t, "/api/v1/resellers/"+itoa64(id)+"/credit", body, superToken)

	bal := env.get(t, "/api/v1/resellers/"+itoa64(id)+"/balance", superToken)
	if !strings.Contains(bal.Body.String(), `"balance":500`) {
		t.Errorf("replayed grant changed the balance: %s", bal.Body)
	}
}

// The reseller role deliberately holds no reseller:* permission. A tenant is
// not an administrator of tenancy, and granting reseller:read would let one
// tenant enumerate the others.
func TestTenantCannotReadTheTenancyAPI(t *testing.T) {
	env, superToken, _ := newSubjectEnv(t)
	operator := seedResellerAdmin(t, env, "vendor", "reseller")
	id := createReseller(t, env, superToken, operator, "vendor-vpn")
	tenantToken := env.login(t, "vendor", "pw")

	for _, path := range []string{
		"/api/v1/resellers",
		"/api/v1/resellers/" + itoa64(id),
		"/api/v1/resellers/" + itoa64(id) + "/balance",
		"/api/v1/resellers/" + itoa64(id) + "/ledger",
	} {
		if res := env.get(t, path, tenantToken); res.Code != http.StatusForbidden {
			t.Errorf("GET %s as a tenant = %d, want 403", path, res.Code)
		}
	}
}

// But a tenant reads their OWN record through /me, served by scope rather than
// by permission -- the same shape as /auth/me and the Telegram link routes.
func TestTenantReadsTheirOwnRecordThroughMe(t *testing.T) {
	env, superToken, _ := newSubjectEnv(t)
	operator := seedResellerAdmin(t, env, "vendor", "reseller")
	id := createReseller(t, env, superToken, operator, "vendor-vpn")
	env.post(t, "/api/v1/resellers/"+itoa64(id)+"/credit",
		`{"delta":750,"reason":"topup","idempotency_key":"seed"}`, superToken)

	tenantToken := env.login(t, "vendor", "pw")
	res := env.get(t, "/api/v1/me/reseller", tenantToken)
	if res.Code != http.StatusOK {
		t.Fatalf("tenant cannot read their own record = %d: %s", res.Code, res.Body)
	}
	body := res.Body.String()
	if !strings.Contains(body, "vendor-vpn") || !strings.Contains(body, `"balance":750`) {
		t.Errorf("own record is wrong: %s", body)
	}
}

// An admin who operates no tenant gets 404 rather than an empty record, so the
// UI can tell "no tenancy" from "a tenancy with nothing in it".
func TestMeResellerIsNotFoundForANonTenant(t *testing.T) {
	env, _, _ := newSubjectEnv(t)
	seedResellerAdmin(t, env, "plain", "admin")
	token := env.login(t, "plain", "pw")

	if res := env.get(t, "/api/v1/me/reseller", token); res.Code != http.StatusNotFound {
		t.Errorf("non-tenant /me/reseller = %d, want 404: %s", res.Code, res.Body)
	}
}

// Setting a limit to null means unlimited, which must survive the round trip.
// Collapsing null into "absent" would make an unlimited tenant unreachable
// through the API once any limit had been set.
func TestLimitsCanBeClearedBackToUnlimited(t *testing.T) {
	env, superToken, _ := newSubjectEnv(t)
	operator := seedResellerAdmin(t, env, "vendor", "reseller")
	id := createReseller(t, env, superToken, operator, "vendor-vpn")

	if res := env.put(t, "/api/v1/resellers/"+itoa64(id),
		`{"max_subjects":10}`, superToken); res.Code != http.StatusNoContent {
		t.Fatalf("set limit = %d: %s", res.Code, res.Body)
	}
	if body := env.get(t, "/api/v1/resellers/"+itoa64(id), superToken).Body.String(); !strings.Contains(body, `"max_subjects":10`) {
		t.Fatalf("limit not stored: %s", body)
	}

	if res := env.put(t, "/api/v1/resellers/"+itoa64(id),
		`{"max_subjects":null}`, superToken); res.Code != http.StatusNoContent {
		t.Fatalf("clear limit = %d: %s", res.Code, res.Body)
	}
	if body := env.get(t, "/api/v1/resellers/"+itoa64(id), superToken).Body.String(); !strings.Contains(body, `"max_subjects":null`) {
		t.Errorf("limit not cleared back to unlimited: %s", body)
	}
}

// A tenant that does not exist and one that is merely out of scope must be
// indistinguishable, or the id space becomes a way to count tenants.
func TestMissingResellerIsNotFound(t *testing.T) {
	env, superToken, _ := newSubjectEnv(t)
	if res := env.get(t, "/api/v1/resellers/99999", superToken); res.Code != http.StatusNotFound {
		t.Errorf("missing reseller = %d, want 404: %s", res.Code, res.Body)
	}
}
