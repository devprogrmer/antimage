package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Self-service ledger.
//
// A note on what "another tenant's ledger" means for this route. GET
// /me/reseller/ledger takes no reseller id, so a tenant asking for somebody
// else's history is not refused -- the request has nowhere to put the question.
// Testing for a 403 there would be testing a code path that cannot be reached.
//
// The two boundaries that CAN be crossed are tested instead:
//
//   - the route resolves the tenancy from the session, so a tenant funded
//     alongside another must see their own movements and none of the other's
//   - the platform route, which does take an id, must stay 403 for a tenant;
//     adding self-service must not have opened a way around it

func movements(t *testing.T, body string) []struct {
	ID     int64  `json:"id"`
	Delta  int64  `json:"delta"`
	Reason string `json:"reason"`
	Note   string `json:"note"`
} {
	t.Helper()
	var out struct {
		Movements []struct {
			ID     int64  `json:"id"`
			Delta  int64  `json:"delta"`
			Reason string `json:"reason"`
			Note   string `json:"note"`
		} `json:"movements"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode movements: %v (body %s)", err, body)
	}
	return out.Movements
}

func TestTenantReadsTheirOwnLedger(t *testing.T) {
	env, superToken, _ := newSubjectEnv(t)
	operator := seedResellerAdmin(t, env, "vendor", "reseller")
	id := createReseller(t, env, superToken, operator, "vendor-vpn")
	fund(t, env, superToken, id, 750, "seed")

	tenantToken := env.login(t, "vendor", "pw")
	res := env.get(t, "/api/v1/me/reseller/ledger", tenantToken)
	if res.Code != http.StatusOK {
		t.Fatalf("tenant cannot read their own ledger = %d: %s", res.Code, res.Body)
	}

	got := movements(t, res.Body.String())
	if len(got) != 1 {
		t.Fatalf("movements = %d, want 1: %s", len(got), res.Body)
	}
	if got[0].Delta != 750 {
		t.Errorf("delta = %d, want 750", got[0].Delta)
	}
}

// THE isolation test. Two funded tenants, and each must see only their own
// movements -- not merely a correct count, but no trace of the other's amounts,
// which is what a predicate applied to the wrong table would leak.
func TestOwnLedgerExcludesOtherTenants(t *testing.T) {
	env, superToken, _ := newSubjectEnv(t)
	aliceAdmin := seedResellerAdmin(t, env, "alice", "reseller")
	bobAdmin := seedResellerAdmin(t, env, "bob", "reseller")
	alice := createReseller(t, env, superToken, aliceAdmin, "alice-vpn")
	bob := createReseller(t, env, superToken, bobAdmin, "bob-vpn")

	fund(t, env, superToken, alice, 111, "alice-seed")
	fund(t, env, superToken, bob, 222, "bob-seed")
	fund(t, env, superToken, bob, 333, "bob-second")

	aliceToken := env.login(t, "alice", "pw")
	res := env.get(t, "/api/v1/me/reseller/ledger", aliceToken)
	if res.Code != http.StatusOK {
		t.Fatalf("alice ledger = %d: %s", res.Code, res.Body)
	}

	got := movements(t, res.Body.String())
	if len(got) != 1 {
		t.Fatalf("alice sees %d movements, want 1: %s", len(got), res.Body)
	}
	if got[0].Delta != 111 {
		t.Errorf("alice delta = %d, want 111", got[0].Delta)
	}
	// Bob's amounts must not appear anywhere in the payload, including in a
	// field this test does not otherwise read.
	for _, leaked := range []string{"222", "333"} {
		if strings.Contains(res.Body.String(), leaked) {
			t.Errorf("alice's ledger contains bob's movement %s: %s", leaked, res.Body)
		}
	}

	// And the relationship holds in the other direction, so a test that passed
	// merely because alice happened to be first is ruled out.
	bobToken := env.login(t, "bob", "pw")
	bobRes := env.get(t, "/api/v1/me/reseller/ledger", bobToken)
	bobGot := movements(t, bobRes.Body.String())
	if len(bobGot) != 2 {
		t.Fatalf("bob sees %d movements, want 2: %s", len(bobGot), bobRes.Body)
	}
	if strings.Contains(bobRes.Body.String(), "111") {
		t.Errorf("bob's ledger contains alice's movement: %s", bobRes.Body)
	}
}

// Self-service must not have opened a route to the platform ledger. The
// reseller role still holds no reseller:* permission, so the id-taking route
// stays 403 for a tenant -- including for their OWN id, which is the case a
// well-meaning "but it's their data" exception would have let through.
func TestSelfServiceDoesNotOpenThePlatformLedger(t *testing.T) {
	env, superToken, _ := newSubjectEnv(t)
	operator := seedResellerAdmin(t, env, "vendor", "reseller")
	id := createReseller(t, env, superToken, operator, "vendor-vpn")
	fund(t, env, superToken, id, 750, "seed")
	tenantToken := env.login(t, "vendor", "pw")

	if res := env.get(t, "/api/v1/resellers/"+itoa64(id)+"/ledger", tenantToken); res.Code != http.StatusForbidden {
		t.Errorf("tenant reading the platform ledger for their own id = %d, want 403: %s",
			res.Code, res.Body)
	}
}

// An admin who operates no tenancy has no ledger, and says so with the same
// 404 as /me/reseller. Returning an empty movements list would read as "your
// billing history is empty" rather than "this question does not apply".
func TestMyLedgerIsNotFoundForANonTenant(t *testing.T) {
	env, _, _ := newSubjectEnv(t)
	seedResellerAdmin(t, env, "plain", "admin")
	token := env.login(t, "plain", "pw")

	if res := env.get(t, "/api/v1/me/reseller/ledger", token); res.Code != http.StatusNotFound {
		t.Errorf("non-tenant /me/reseller/ledger = %d, want 404: %s", res.Code, res.Body)
	}
}

// A super admin's scope matches every reseller, so a resolution step that did
// not special-case them would return an arbitrary tenant's billing history.
// They mint credit rather than hold it, so they have no ledger of their own.
func TestMyLedgerIsNotFoundForASuperAdmin(t *testing.T) {
	env, superToken, _ := newSubjectEnv(t)
	operator := seedResellerAdmin(t, env, "vendor", "reseller")
	id := createReseller(t, env, superToken, operator, "vendor-vpn")
	fund(t, env, superToken, id, 750, "seed")

	res := env.get(t, "/api/v1/me/reseller/ledger", superToken)
	if res.Code != http.StatusNotFound {
		t.Fatalf("super admin /me/reseller/ledger = %d, want 404: %s", res.Code, res.Body)
	}
	if strings.Contains(res.Body.String(), "750") {
		t.Errorf("super admin was handed a tenant's movements: %s", res.Body)
	}
}

// limit is the caller's, and it is honoured. Without it a tenant with a long
// history pulls the whole thing on every page load.
func TestOwnLedgerRespectsTheLimit(t *testing.T) {
	env, superToken, _ := newSubjectEnv(t)
	operator := seedResellerAdmin(t, env, "vendor", "reseller")
	id := createReseller(t, env, superToken, operator, "vendor-vpn")
	for _, key := range []string{"a", "b", "c", "d", "e"} {
		fund(t, env, superToken, id, 100, key)
	}
	tenantToken := env.login(t, "vendor", "pw")

	res := env.get(t, "/api/v1/me/reseller/ledger?limit=2", tenantToken)
	if res.Code != http.StatusOK {
		t.Fatalf("limited ledger = %d: %s", res.Code, res.Body)
	}
	if got := movements(t, res.Body.String()); len(got) != 2 {
		t.Errorf("movements = %d with limit=2, want 2: %s", len(got), res.Body)
	}

	// An unparseable limit must fall back to the store's default rather than
	// becoming zero and returning nothing, which would render an empty ledger
	// for a tenant who has one.
	res = env.get(t, "/api/v1/me/reseller/ledger?limit=nonsense", tenantToken)
	if res.Code != http.StatusOK {
		t.Fatalf("garbage limit = %d, want 200: %s", res.Code, res.Body)
	}
	if got := movements(t, res.Body.String()); len(got) != 5 {
		t.Errorf("movements = %d with a garbage limit, want all 5: %s", len(got), res.Body)
	}

	// A limit above the store's ceiling must not disable it.
	res = env.get(t, "/api/v1/me/reseller/ledger?limit=100000", tenantToken)
	if got := movements(t, res.Body.String()); len(got) != 5 {
		t.Errorf("movements = %d with an absurd limit, want 5: %s", len(got), res.Body)
	}
}

// The ledger is what makes a balance checkable by the person being billed, so
// the debits a tenant is charged must appear in it -- not just the credits an
// operator grants. Most recent first.
func TestOwnLedgerShowsProvisioningDebits(t *testing.T) {
	env, superToken, svcID := newSubjectEnv(t)
	operator := seedResellerAdmin(t, env, "vendor", "reseller")
	id := createReseller(t, env, superToken, operator, "vendor-vpn")
	fund(t, env, superToken, id, 1000, "seed")
	if res := provision(t, env, superToken, id, "customer", svcID, 250, 0, "p1"); res.Code != http.StatusCreated {
		t.Fatalf("provision = %d: %s", res.Code, res.Body)
	}

	tenantToken := env.login(t, "vendor", "pw")
	got := movements(t, env.get(t, "/api/v1/me/reseller/ledger", tenantToken).Body.String())
	if len(got) != 2 {
		t.Fatalf("movements = %d, want 2 (a grant and a charge)", len(got))
	}
	// Most recent first, so the charge leads.
	if got[0].Delta != -250 {
		t.Errorf("newest delta = %d, want -250: the charge is missing or not first", got[0].Delta)
	}
	if got[1].Delta != 1000 {
		t.Errorf("oldest delta = %d, want 1000", got[1].Delta)
	}
}
