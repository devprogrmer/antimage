package httpapi

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"
)

// Regression cover for the quota-accounting bug.
//
// max_quota_bytes was decorative. checkLimits sums subjects.quota_bytes across
// a tenant's customers, but ProvisionSubject recorded the requested quota only
// in the audit entry and never on the subject row, so the sum stayed zero
// forever and every request was measured against an empty allocation.
//
// The shape of that bug is what these tests are built around: it is not enough
// to assert that an over-limit request is refused, because a ceiling that never
// trips also never refuses anything ONCE -- the first request under the limit
// passes either way. What distinguishes fixed from broken is whether the second
// request sees the first one's allocation, and whether the number reached the
// subject row at all.

// allocatedQuota reads what checkLimits will actually measure: the sum over the
// tenant's customers, from the subjects table.
func allocatedQuota(t *testing.T, env *testEnv, resellerID int64) int64 {
	t.Helper()
	var sum sql.NullInt64
	if err := env.store.Read().QueryRow(
		`SELECT SUM(s.quota_bytes)
		   FROM reseller_subjects rs
		   JOIN subjects s ON s.id = rs.subject_id
		  WHERE rs.reseller_id = ? AND s.quota_bytes IS NOT NULL`,
		resellerID).Scan(&sum); err != nil {
		t.Fatalf("sum allocated quota: %v", err)
	}
	return sum.Int64
}

func subjectQuota(t *testing.T, env *testEnv, name string) sql.NullInt64 {
	t.Helper()
	var q sql.NullInt64
	if err := env.store.Read().QueryRow(
		`SELECT quota_bytes FROM subjects WHERE name = ?`, name).Scan(&q); err != nil {
		t.Fatalf("read quota for %q: %v", name, err)
	}
	return q
}

// (1) Below the ceiling succeeds.
func TestProvisioningWithinTheQuotaCeilingSucceeds(t *testing.T) {
	env, superToken, svcID := newSubjectEnv(t)
	operator := seedResellerAdmin(t, env, "vendor", "reseller")
	id := createReseller(t, env, superToken, operator, "vendor-vpn")
	fund(t, env, superToken, id, 100000, "seed")
	if res := env.put(t, "/api/v1/resellers/"+itoa64(id),
		`{"max_quota_bytes":1000}`, superToken); res.Code != http.StatusNoContent {
		t.Fatalf("set ceiling = %d: %s", res.Code, res.Body)
	}

	if res := provision(t, env, superToken, id, "under", svcID, 10, 400, "q1"); res.Code != http.StatusCreated {
		t.Fatalf("provision under the ceiling = %d, want 201: %s", res.Code, res.Body)
	}
}

// (4) The stored subject quota matches what was provisioned.
//
// This is the assertion that would have caught the original bug on its own:
// the request succeeded, the audit entry carried the number, and the subject
// row held NULL.
func TestProvisionedQuotaReachesTheSubjectRow(t *testing.T) {
	env, superToken, svcID := newSubjectEnv(t)
	operator := seedResellerAdmin(t, env, "vendor", "reseller")
	id := createReseller(t, env, superToken, operator, "vendor-vpn")
	fund(t, env, superToken, id, 100000, "seed")

	if res := provision(t, env, superToken, id, "customer", svcID, 10, 4096, "q1"); res.Code != http.StatusCreated {
		t.Fatalf("provision = %d: %s", res.Code, res.Body)
	}

	got := subjectQuota(t, env, "customer")
	if !got.Valid {
		t.Fatal("subject.quota_bytes is NULL after provisioning with a quota; " +
			"the ceiling is measured from this column, so it would never trip")
	}
	if got.Int64 != 4096 {
		t.Errorf("subject.quota_bytes = %d, want 4096", got.Int64)
	}
}

// A provision with no quota must leave the column NULL rather than writing 0.
//
// NULL means "no quota", 0 would mean "a quota of nothing". checkLimits filters
// on IS NOT NULL, so the distinction decides whether an unmetered customer is
// counted as allocating zero or is excluded entirely -- the same answer here,
// but only NULL stays correct if the sum is ever used differently.
func TestProvisioningWithoutQuotaLeavesTheColumnNull(t *testing.T) {
	env, superToken, svcID := newSubjectEnv(t)
	operator := seedResellerAdmin(t, env, "vendor", "reseller")
	id := createReseller(t, env, superToken, operator, "vendor-vpn")
	fund(t, env, superToken, id, 100000, "seed")

	if res := provision(t, env, superToken, id, "unmetered", svcID, 10, 0, "q1"); res.Code != http.StatusCreated {
		t.Fatalf("provision = %d: %s", res.Code, res.Body)
	}
	if got := subjectQuota(t, env, "unmetered"); got.Valid {
		t.Errorf("quota_bytes = %d for an unmetered customer, want NULL", got.Int64)
	}
}

// (3) Multiple subjects are summed, and the sum is what the ceiling is measured
// against.
//
// Three provisions of 300 against a ceiling of 1000: the first two fit, the
// third must not. A ceiling that ignored prior allocations would accept all
// three, which is exactly what the bug did.
func TestQuotaAllocationsAccumulateAcrossSubjects(t *testing.T) {
	env, superToken, svcID := newSubjectEnv(t)
	operator := seedResellerAdmin(t, env, "vendor", "reseller")
	id := createReseller(t, env, superToken, operator, "vendor-vpn")
	fund(t, env, superToken, id, 100000, "seed")
	if res := env.put(t, "/api/v1/resellers/"+itoa64(id),
		`{"max_quota_bytes":1000}`, superToken); res.Code != http.StatusNoContent {
		t.Fatalf("set ceiling = %d: %s", res.Code, res.Body)
	}

	for i, name := range []string{"first", "second"} {
		res := provision(t, env, superToken, id, name, svcID, 10, 300, "q"+name)
		if res.Code != http.StatusCreated {
			t.Fatalf("provision %d (%s) = %d, want 201: %s", i, name, res.Code, res.Body)
		}
	}
	if got := allocatedQuota(t, env, id); got != 600 {
		t.Fatalf("allocated = %d after two provisions of 300, want 600 — "+
			"the ceiling is measured from this sum", got)
	}

	// 600 + 300 = 900, still under 1000.
	if res := provision(t, env, superToken, id, "third", svcID, 10, 300, "qthird"); res.Code != http.StatusCreated {
		t.Fatalf("third provision = %d, want 201 (900 <= 1000): %s", res.Code, res.Body)
	}
	if got := allocatedQuota(t, env, id); got != 900 {
		t.Fatalf("allocated = %d, want 900", got)
	}

	// (2) 900 + 300 = 1200 exceeds the ceiling.
	res := provision(t, env, superToken, id, "fourth", svcID, 10, 300, "qfourth")
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("fourth provision = %d, want 422: prior allocations were ignored: %s",
			res.Code, res.Body)
	}
	if !strings.Contains(res.Body.String(), "900 of 1000") {
		t.Errorf("refusal does not report the real allocation: %s", res.Body)
	}

	// The refused request must not have allocated anything.
	if got := allocatedQuota(t, env, id); got != 900 {
		t.Errorf("allocated = %d after a refused provision, want 900", got)
	}
	var created int
	if err := env.store.Read().QueryRow(
		`SELECT COUNT(*) FROM subjects WHERE name = 'fourth'`).Scan(&created); err != nil {
		t.Fatalf("count: %v", err)
	}
	if created != 0 {
		t.Error("refused provision created the customer anyway")
	}
}

// The ceiling is per tenant. One tenant's allocation must not count against
// another's, which a sum missing its WHERE clause would get wrong.
func TestQuotaCeilingsAreIsolatedBetweenTenants(t *testing.T) {
	env, superToken, svcID := newSubjectEnv(t)
	aliceAdmin := seedResellerAdmin(t, env, "alice", "reseller")
	bobAdmin := seedResellerAdmin(t, env, "bob", "reseller")
	alice := createReseller(t, env, superToken, aliceAdmin, "alice-vpn")
	bob := createReseller(t, env, superToken, bobAdmin, "bob-vpn")
	fund(t, env, superToken, alice, 100000, "seed-a")
	fund(t, env, superToken, bob, 100000, "seed-b")

	for _, id := range []int64{alice, bob} {
		if res := env.put(t, "/api/v1/resellers/"+itoa64(id),
			`{"max_quota_bytes":1000}`, superToken); res.Code != http.StatusNoContent {
			t.Fatalf("set ceiling = %d: %s", res.Code, res.Body)
		}
	}

	// Alice fills her ceiling.
	if res := provision(t, env, superToken, alice, "alice-cust", svcID, 10, 900, "qa"); res.Code != http.StatusCreated {
		t.Fatalf("alice provision = %d: %s", res.Code, res.Body)
	}

	// Bob's ceiling is untouched by it.
	if res := provision(t, env, superToken, bob, "bob-cust", svcID, 10, 900, "qb"); res.Code != http.StatusCreated {
		t.Fatalf("bob provision = %d, want 201: alice's allocation counted against "+
			"bob's ceiling: %s", res.Code, res.Body)
	}

	if got := allocatedQuota(t, env, alice); got != 900 {
		t.Errorf("alice allocated = %d, want 900", got)
	}
	if got := allocatedQuota(t, env, bob); got != 900 {
		t.Errorf("bob allocated = %d, want 900", got)
	}
}

// A replayed provision must not allocate twice. The idempotency check returns
// the original outcome before reaching checkLimits, so the allocation stays put.
func TestReplayedProvisionDoesNotAllocateTwice(t *testing.T) {
	env, superToken, svcID := newSubjectEnv(t)
	operator := seedResellerAdmin(t, env, "vendor", "reseller")
	id := createReseller(t, env, superToken, operator, "vendor-vpn")
	fund(t, env, superToken, id, 100000, "seed")

	if res := provision(t, env, superToken, id, "customer", svcID, 10, 500, "same"); res.Code != http.StatusCreated {
		t.Fatalf("first provision = %d: %s", res.Code, res.Body)
	}
	if res := provision(t, env, superToken, id, "customer", svcID, 10, 500, "same"); res.Code != http.StatusCreated {
		t.Fatalf("replay = %d: %s", res.Code, res.Body)
	}
	if got := allocatedQuota(t, env, id); got != 500 {
		t.Errorf("allocated = %d after a replay, want 500", got)
	}
}
