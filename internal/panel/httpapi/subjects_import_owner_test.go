package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
)

// Gap 1 from docs/TENANT-ISOLATION.md: import created subjects without
// assigning ownership, so every imported row was platform-owned and invisible
// to every tenant.
//
// Ownership is now an explicit parameter, gated on reseller:write, because
// naming an owner decides who is billed.

func importCSV(t *testing.T, env *testEnv, body, token string) *struct {
	Imported int      `json:"imported"`
	Failed   int      `json:"failed"`
	Errors   []string `json:"errors"`
} {
	t.Helper()
	res := env.post(t, "/api/v1/subjects/import", body, token)
	if res.Code != http.StatusOK {
		t.Fatalf("import = %d: %s", res.Code, res.Body)
	}
	var out struct {
		Imported int      `json:"imported"`
		Failed   int      `json:"failed"`
		Errors   []string `json:"errors"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return &out
}

// resellerIDOf returns the reseller row seeded for an admin username.
func resellerIDOf(t *testing.T, env *testEnv, who string) int64 {
	t.Helper()
	var id int64
	if err := env.store.Read().QueryRow(
		`SELECT r.id FROM resellers r
		   JOIN admins a ON a.id = r.admin_id
		  WHERE a.username = ?`, who).Scan(&id); err != nil {
		t.Fatalf("reseller for %s: %v", who, err)
	}
	return id
}

func TestImportAssignsOwnership(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	aliceToken, _ := seedTenant(t, env, "alice", svcID, adminToken)
	aliceReseller := resellerIDOf(t, env, "alice")

	// Give the tenant enough credit that the provision is not refused for a
	// reason unrelated to what this test is about.
	if err := creditReseller(t, env, aliceReseller, 10_000); err != nil {
		t.Fatalf("credit: %v", err)
	}

	body, err := json.Marshal(map[string]any{
		"csv":         "Name,Note\nowned-one,\nowned-two,\n",
		"reseller_id": aliceReseller,
		"cost":        100,
		"service_ids": []int64{svcID},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := importCSV(t, env, string(body), adminToken)
	if out.Imported != 2 || out.Failed != 0 {
		t.Fatalf("imported=%d failed=%d errors=%v", out.Imported, out.Failed, out.Errors)
	}

	// Ownership recorded, which is the whole point of the gap.
	var owned int
	if err := env.store.Read().QueryRow(
		`SELECT COUNT(*) FROM reseller_subjects WHERE reseller_id = ?`,
		aliceReseller).Scan(&owned); err != nil {
		t.Fatalf("count ownership: %v", err)
	}
	// One from seedTenant plus the two imported.
	if owned != 3 {
		t.Errorf("reseller owns %d subjects, want 3", owned)
	}

	// And the tenant can now actually see them, which platform-owned rows
	// never allowed.
	ids := listedIDs(t, env, aliceToken)
	if len(ids) != 3 {
		t.Errorf("tenant sees %d subjects, want 3: imports were not visible to their owner", len(ids))
	}
}

// A tenant must not be able to name an owner, least of all themselves: they
// would be provisioning onto their own account at a cost they also chose.
func TestImportOwnerRequiresResellerWrite(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	aliceToken, _ := seedTenant(t, env, "alice", svcID, adminToken)
	aliceReseller := resellerIDOf(t, env, "alice")

	body, err := json.Marshal(map[string]any{
		"csv":         "Name\nfree-lunch\n",
		"reseller_id": aliceReseller,
		"cost":        0,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	res := env.post(t, "/api/v1/subjects/import", string(body), aliceToken)
	if res.Code != http.StatusForbidden {
		t.Errorf("tenant naming an owner = %d, want %d", res.Code, http.StatusForbidden)
	}
	var n int
	if err := env.store.Read().QueryRow(
		`SELECT COUNT(*) FROM subjects WHERE name = 'free-lunch'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Error("a tenant self-provisioned through import")
	}
}

// Ownership costs credit, and the debit must land in the same ledger the
// ordinary provisioning path uses.
func TestImportDebitsResellerCredit(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	seedTenant(t, env, "alice", svcID, adminToken)
	aliceReseller := resellerIDOf(t, env, "alice")
	if err := creditReseller(t, env, aliceReseller, 500); err != nil {
		t.Fatalf("credit: %v", err)
	}

	body, err := json.Marshal(map[string]any{
		"csv":         "Name\npaid-one\npaid-two\n",
		"reseller_id": aliceReseller,
		"cost":        100,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if out := importCSV(t, env, string(body), adminToken); out.Imported != 2 {
		t.Fatalf("imported=%d failed=%d errors=%v", out.Imported, out.Failed, out.Errors)
	}

	var balance sql.NullInt64
	if err := env.store.Read().QueryRow(
		`SELECT SUM(delta) FROM reseller_credit_ledger WHERE reseller_id = ?`,
		aliceReseller).Scan(&balance); err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance.Int64 != 300 {
		t.Errorf("balance = %d, want 300 (500 granted minus 2 x 100)", balance.Int64)
	}
}

// Re-POSTing an identical CSV must not double-charge or duplicate: the
// idempotency key is derived from the CSV body and the row name.
func TestImportIsIdempotentOnReplay(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	seedTenant(t, env, "alice", svcID, adminToken)
	aliceReseller := resellerIDOf(t, env, "alice")
	if err := creditReseller(t, env, aliceReseller, 500); err != nil {
		t.Fatalf("credit: %v", err)
	}

	body, err := json.Marshal(map[string]any{
		"csv":         "Name\nreplayed\n",
		"reseller_id": aliceReseller,
		"cost":        100,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	first := importCSV(t, env, string(body), adminToken)
	if first.Imported != 1 {
		t.Fatalf("first import: imported=%d failed=%d errors=%v",
			first.Imported, first.Failed, first.Errors)
	}
	second := importCSV(t, env, string(body), adminToken)

	// The replay must be RECOGNISED, not merely harmless. Asserting on the
	// balance alone cannot tell the two apart: with a broken key the replay
	// still fails to charge, because subjects.Create rejects the duplicate
	// name before the ledger is reached. Only the reported outcome
	// distinguishes "already done" from "collided with itself".
	if second.Imported != 1 || second.Failed != 0 {
		t.Errorf("replay: imported=%d failed=%d errors=%v, want 1/0 — the idempotency key did not match, so the row was re-attempted and hit the unique name index",
			second.Imported, second.Failed, second.Errors)
	}

	var balance sql.NullInt64
	if err := env.store.Read().QueryRow(
		`SELECT SUM(delta) FROM reseller_credit_ledger WHERE reseller_id = ?`,
		aliceReseller).Scan(&balance); err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance.Int64 != 400 {
		t.Errorf("balance = %d, want 400: the replay charged again", balance.Int64)
	}

	var rows int
	if err := env.store.Read().QueryRow(
		`SELECT COUNT(*) FROM subjects WHERE name = 'replayed'`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("subject rows = %d, want 1", rows)
	}
}

// Without an owner the rows stay platform-owned, which is the documented
// behaviour for an administrative import -- but they must still be real
// subjects, with sealed credentials and service grants. The raw INSERT this
// replaced produced rows that could not authenticate and appeared on no node.
func TestImportWithoutOwnerStillCreatesUsableSubjects(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)

	body, err := json.Marshal(map[string]any{
		"csv":         "Name\nplatform-one\n",
		"service_ids": []int64{svcID},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if out := importCSV(t, env, string(body), adminToken); out.Imported != 1 {
		t.Fatalf("imported=%d failed=%d errors=%v", out.Imported, out.Failed, out.Errors)
	}

	var id int64
	if err := env.store.Read().QueryRow(
		`SELECT id FROM subjects WHERE name = 'platform-one'`).Scan(&id); err != nil {
		t.Fatalf("find subject: %v", err)
	}

	var creds int
	if err := env.store.Read().QueryRow(
		`SELECT COUNT(*) FROM subject_credentials WHERE subject_id = ?`, id).Scan(&creds); err != nil {
		t.Fatalf("count credentials: %v", err)
	}
	if creds != 2 {
		t.Errorf("subject has %d credentials, want 2 (uuid and password): it cannot authenticate", creds)
	}

	var grants int
	if err := env.store.Read().QueryRow(
		`SELECT COUNT(*) FROM subject_services WHERE subject_id = ?`, id).Scan(&grants); err != nil {
		t.Fatalf("count grants: %v", err)
	}
	if grants != 1 {
		t.Errorf("subject has %d service grants, want 1: it appears on no node", grants)
	}

	var owned int
	if err := env.store.Read().QueryRow(
		`SELECT COUNT(*) FROM reseller_subjects WHERE subject_id = ?`, id).Scan(&owned); err != nil {
		t.Fatalf("count ownership: %v", err)
	}
	if owned != 0 {
		t.Error("an ownerless import assigned ownership anyway")
	}
}

// creditReseller tops up a reseller directly, so a provisioning test fails on
// what it is testing rather than on an empty balance.
func creditReseller(t *testing.T, env *testEnv, resellerID, amount int64) error {
	t.Helper()
	return env.store.Write(context.Background(), func(tx *sql.Tx) error {
		// reason is CHECK-constrained to a fixed vocabulary; "topup" is the one
		// that means credit arriving from outside the system.
		_, err := tx.Exec(
			`INSERT INTO reseller_credit_ledger
			   (reseller_id, delta, reason, idempotency_key, at)
			 VALUES (?,?,?,?,?)`,
			resellerID, amount, "topup", "test-topup", int64(1_700_000_000))
		return err
	})
}
