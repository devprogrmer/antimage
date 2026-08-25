package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The rest of the tenancy lifecycle: deactivation, deletion, provisioning, the
// two hard ceilings, the credit floor, and audit.
//
// These exercise the engine THROUGH the API. The engine's own invariants were
// already tested when it landed; what was untested is that the routes reach
// them, because until now there were no routes.

func fund(t *testing.T, env *testEnv, token string, id, amount int64, key string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"delta": amount, "reason": "topup", "idempotency_key": key,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	res := env.post(t, "/api/v1/resellers/"+itoa64(id)+"/credit", string(body), token)
	if res.Code != http.StatusCreated {
		t.Fatalf("fund = %d: %s", res.Code, res.Body)
	}
}

func provision(
	t *testing.T, env *testEnv, token string, id int64,
	name string, svcID, cost, quota int64, key string,
) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"name": name, "service_ids": []int64{svcID},
		"cost": cost, "quota_bytes": quota, "idempotency_key": key,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return env.post(t, "/api/v1/resellers/"+itoa64(id)+"/subjects", string(body), token)
}

// Deactivating is reversible and stops provisioning without cutting anybody
// off. It is what an operator usually wants, so it must work independently of
// delete.
func TestDeactivateStopsProvisioningWithoutDeleting(t *testing.T) {
	env, superToken, svcID := newSubjectEnv(t)
	operator := seedResellerAdmin(t, env, "vendor", "reseller")
	id := createReseller(t, env, superToken, operator, "vendor-vpn")
	fund(t, env, superToken, id, 10000, "seed")

	if res := env.put(t, "/api/v1/resellers/"+itoa64(id),
		`{"enabled":false}`, superToken); res.Code != http.StatusNoContent {
		t.Fatalf("deactivate = %d: %s", res.Code, res.Body)
	}

	res := provision(t, env, superToken, id, "blocked", svcID, 100, 0, "k-blocked")
	if res.Code != http.StatusUnprocessableEntity {
		t.Errorf("provisioning through a disabled tenant = %d, want 422: %s", res.Code, res.Body)
	}

	// Still present and still readable: deactivation is not deletion.
	if got := env.get(t, "/api/v1/resellers/"+itoa64(id), superToken); got.Code != http.StatusOK {
		t.Errorf("deactivated tenant = %d, want 200", got.Code)
	}
}

// reseller_subjects.reseller_id is ON DELETE RESTRICT, deliberately: cascading
// would delete a tenant's live customers along with the tenant.
func TestDeleteRefusedWhileTenantOwnsCustomers(t *testing.T) {
	env, superToken, svcID := newSubjectEnv(t)
	operator := seedResellerAdmin(t, env, "vendor", "reseller")
	id := createReseller(t, env, superToken, operator, "vendor-vpn")
	fund(t, env, superToken, id, 10000, "seed")

	if res := provision(t, env, superToken, id, "customer", svcID, 100, 0, "k1"); res.Code != http.StatusCreated {
		t.Fatalf("provision = %d: %s", res.Code, res.Body)
	}

	res := env.delete(t, "/api/v1/resellers/"+itoa64(id), superToken)
	if res.Code != http.StatusConflict {
		t.Fatalf("delete with live customers = %d, want 409: %s", res.Code, res.Body)
	}
	if !strings.Contains(res.Body.String(), "deactivate") {
		t.Errorf("refusal does not offer the reversible option: %s", res.Body)
	}

	if got := env.get(t, "/api/v1/resellers/"+itoa64(id), superToken); got.Code != http.StatusOK {
		t.Error("refused delete removed the tenant anyway")
	}
}

func TestDeleteSucceedsWithoutCustomers(t *testing.T) {
	env, superToken, _ := newSubjectEnv(t)
	operator := seedResellerAdmin(t, env, "vendor", "reseller")
	id := createReseller(t, env, superToken, operator, "vendor-vpn")

	if res := env.delete(t, "/api/v1/resellers/"+itoa64(id), superToken); res.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", res.Code, res.Body)
	}
	if got := env.get(t, "/api/v1/resellers/"+itoa64(id), superToken); got.Code != http.StatusNotFound {
		t.Errorf("deleted tenant still readable = %d", got.Code)
	}
}

// The debit and the creation share one transaction, so a customer nobody paid
// for and a charge for a customer who does not exist are both impossible.
func TestProvisioningDebitsAndCreatesTogether(t *testing.T) {
	env, superToken, svcID := newSubjectEnv(t)
	operator := seedResellerAdmin(t, env, "vendor", "reseller")
	id := createReseller(t, env, superToken, operator, "vendor-vpn")
	fund(t, env, superToken, id, 1000, "seed")

	res := provision(t, env, superToken, id, "customer-1", svcID, 250, 0, "p1")
	if res.Code != http.StatusCreated {
		t.Fatalf("provision = %d: %s", res.Code, res.Body)
	}
	if !strings.Contains(res.Body.String(), `"balance":750`) {
		t.Errorf("provision did not debit: %s", res.Body)
	}

	var owned int
	if err := env.store.Read().QueryRow(
		`SELECT COUNT(*) FROM reseller_subjects WHERE reseller_id = ?`, id).Scan(&owned); err != nil {
		t.Fatalf("count: %v", err)
	}
	if owned != 1 {
		t.Errorf("provisioned customer is not owned by the tenant (%d rows)", owned)
	}
}

// The credit floor is where provisioning starts failing. Below it the engine
// refuses rather than letting a tenant spend money they do not have.
func TestProvisioningRefusedBelowTheCreditFloor(t *testing.T) {
	env, superToken, svcID := newSubjectEnv(t)
	operator := seedResellerAdmin(t, env, "vendor", "reseller")
	id := createReseller(t, env, superToken, operator, "vendor-vpn")
	fund(t, env, superToken, id, 100, "seed")

	res := provision(t, env, superToken, id, "too-expensive", svcID, 500, 0, "p1")
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("provision beyond the floor = %d, want 422: %s", res.Code, res.Body)
	}
	if !strings.Contains(res.Body.String(), "credit") {
		t.Errorf("refusal does not name the reason: %s", res.Body)
	}

	// Nothing created, nothing charged: the transaction is the guarantee.
	var created, balance int
	_ = env.store.Read().QueryRow(
		`SELECT COUNT(*) FROM subjects WHERE name = 'too-expensive'`).Scan(&created)
	_ = env.store.Read().QueryRow(
		`SELECT COALESCE(SUM(delta),0) FROM reseller_credit_ledger WHERE reseller_id = ?`, id).Scan(&balance)
	if created != 0 {
		t.Error("refused provision created the customer anyway")
	}
	if balance != 100 {
		t.Errorf("balance = %d, want 100: a refused provision moved the ledger", balance)
	}
}

// max_subjects is a hard ceiling. Unlike credit it cannot be fixed by topping
// up, which is why the engine checks it first.
func TestProvisioningRefusedBeyondTheSubjectCeiling(t *testing.T) {
	env, superToken, svcID := newSubjectEnv(t)
	operator := seedResellerAdmin(t, env, "vendor", "reseller")
	id := createReseller(t, env, superToken, operator, "vendor-vpn")
	fund(t, env, superToken, id, 100000, "seed")

	if res := env.put(t, "/api/v1/resellers/"+itoa64(id),
		`{"max_subjects":1}`, superToken); res.Code != http.StatusNoContent {
		t.Fatalf("set ceiling = %d: %s", res.Code, res.Body)
	}

	if res := provision(t, env, superToken, id, "first", svcID, 10, 0, "p1"); res.Code != http.StatusCreated {
		t.Fatalf("first provision = %d: %s", res.Code, res.Body)
	}
	res := provision(t, env, superToken, id, "second", svcID, 10, 0, "p2")
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("provision past the ceiling = %d, want 422: %s", res.Code, res.Body)
	}
	if !strings.Contains(res.Body.String(), "1 of 1") {
		t.Errorf("refusal does not say how much of the ceiling is used: %s", res.Body)
	}
}

// max_quota_bytes is the other ceiling, counted as the sum already allocated.
func TestProvisioningRefusedBeyondTheQuotaCeiling(t *testing.T) {
	env, superToken, svcID := newSubjectEnv(t)
	operator := seedResellerAdmin(t, env, "vendor", "reseller")
	id := createReseller(t, env, superToken, operator, "vendor-vpn")
	fund(t, env, superToken, id, 100000, "seed")

	if res := env.put(t, "/api/v1/resellers/"+itoa64(id),
		`{"max_quota_bytes":1000}`, superToken); res.Code != http.StatusNoContent {
		t.Fatalf("set quota ceiling = %d: %s", res.Code, res.Body)
	}

	if res := provision(t, env, superToken, id, "within", svcID, 10, 800, "p1"); res.Code != http.StatusCreated {
		t.Fatalf("provision within the ceiling = %d: %s", res.Code, res.Body)
	}
	res := provision(t, env, superToken, id, "over", svcID, 10, 500, "p2")
	if res.Code != http.StatusUnprocessableEntity {
		t.Errorf("provision past the quota ceiling = %d, want 422: %s", res.Code, res.Body)
	}
}

// A retried provision must return the original outcome, not charge again.
func TestProvisioningReplayDoesNotDoubleCharge(t *testing.T) {
	env, superToken, svcID := newSubjectEnv(t)
	operator := seedResellerAdmin(t, env, "vendor", "reseller")
	id := createReseller(t, env, superToken, operator, "vendor-vpn")
	fund(t, env, superToken, id, 1000, "seed")

	if first := provision(t, env, superToken, id, "customer", svcID, 250, 0, "same"); first.Code != http.StatusCreated {
		t.Fatalf("first provision = %d: %s", first.Code, first.Body)
	}
	second := provision(t, env, superToken, id, "customer", svcID, 250, 0, "same")
	if second.Code != http.StatusCreated {
		t.Fatalf("replay = %d, want 201 reporting the original outcome: %s", second.Code, second.Body)
	}
	if !strings.Contains(second.Body.String(), `"balance":750`) {
		t.Errorf("replay charged again: %s", second.Body)
	}
}

func TestProvisioningRequiresAnIdempotencyKey(t *testing.T) {
	env, superToken, svcID := newSubjectEnv(t)
	operator := seedResellerAdmin(t, env, "vendor", "reseller")
	id := createReseller(t, env, superToken, operator, "vendor-vpn")

	body, err := json.Marshal(map[string]any{
		"name": "x", "service_ids": []int64{svcID}, "cost": 10,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	res := env.post(t, "/api/v1/resellers/"+itoa64(id)+"/subjects", string(body), superToken)
	if res.Code != http.StatusBadRequest {
		t.Errorf("provision without a key = %d, want 400: %s", res.Code, res.Body)
	}
}

// A tenant must not provision onto their own account: they would be choosing
// both the owner and the price.
func TestTenantCannotProvisionForThemselves(t *testing.T) {
	env, superToken, svcID := newSubjectEnv(t)
	operator := seedResellerAdmin(t, env, "vendor", "reseller")
	id := createReseller(t, env, superToken, operator, "vendor-vpn")
	fund(t, env, superToken, id, 10000, "seed")
	tenantToken := env.login(t, "vendor", "pw")

	res := provision(t, env, tenantToken, id, "self-served", svcID, 0, 0, "p1")
	if res.Code != http.StatusForbidden {
		t.Errorf("tenant provisioning for themselves = %d, want 403: %s", res.Code, res.Body)
	}
}

// Every tenancy mutation is auditable. The ledger cascades away with a deleted
// tenant, so for deletion the audit row is the only surviving account of it.
func TestTenancyMutationsAreAudited(t *testing.T) {
	env, superToken, _ := newSubjectEnv(t)
	operator := seedResellerAdmin(t, env, "vendor", "reseller")
	id := createReseller(t, env, superToken, operator, "vendor-vpn")
	fund(t, env, superToken, id, 500, "seed")
	env.put(t, "/api/v1/resellers/"+itoa64(id), `{"display_name":"renamed"}`, superToken)
	env.delete(t, "/api/v1/resellers/"+itoa64(id), superToken)

	for _, action := range []string{
		"reseller.create", "reseller.credit", "reseller.update", "reseller.delete",
	} {
		var n int
		if err := env.store.Read().QueryRow(
			`SELECT COUNT(*) FROM audit_log WHERE action = ?`, action).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", action, err)
		}
		if n == 0 {
			t.Errorf("no audit record for %s", action)
		}
	}
}
