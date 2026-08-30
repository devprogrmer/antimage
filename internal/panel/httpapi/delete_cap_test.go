package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The delete cap stops a reseller deleting a customer they have been billed
// for.
//
// Usage rows cascade with the subject, so deleting a heavy user before
// settlement destroys the debt along with the evidence of it. Rebecca has this
// control; Antimage did not, and a reseller could clear their whole month.
//
// Enforced in service.Subjects.Delete rather than in a handler, which is what
// makes the bulk path safe too: bulk delete calls straight through to the same
// method. Reverting the checkDeleteCap call fails both tests below by name.

func setCap(t *testing.T, env *testEnv, username string, capBytes any) {
	t.Helper()
	err := env.store.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(),
			`UPDATE admins SET delete_cap_bytes = ? WHERE username = ?`, capBytes, username)
		return err
	})
	if err != nil {
		t.Fatalf("set delete cap: %v", err)
	}
}

// recordUsageFor gives the subject a lifetime billable figure by writing a
// folded rollup, which is what BillableForSubject reads.
func recordUsageFor(t *testing.T, env *testEnv, nodeID, subjectID, bytes int64) {
	t.Helper()
	err := env.store.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `
			INSERT INTO usage_rollups_hourly
			  (subject_id, node_id, service_id, hour_start, uplink_bytes, downlink_bytes)
			VALUES (?, ?, NULL, 0, ?, 0)`, subjectID, nodeID, bytes)
		return err
	})
	if err != nil {
		t.Fatalf("record usage: %v", err)
	}
}

func TestDeleteIsRefusedPastTheCap(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	sellerToken, subjectID := seedTenantWithRole(
		t, env, "seller", "reseller", svcID, adminToken)

	nodeID := nodeOfService(t, env, svcID)
	recordUsageFor(t, env, nodeID, subjectID, 5000)
	setCap(t, env, "seller", 1000)

	res := env.delete(t, "/api/v1/subjects/"+itoa64(subjectID), sellerToken)
	if res.Code != http.StatusForbidden {
		t.Fatalf("delete of a 5000-byte customer under a 1000-byte cap -> %d %s; "+
			"want %d", res.Code, strings.TrimSpace(res.Body.String()),
			http.StatusForbidden)
	}
	// The reason has to be the real one. "insufficient permissions" would send
	// the operator asking for a role change that would not help.
	if body := res.Body.String(); !strings.Contains(body, "delete_cap_exceeded") {
		t.Errorf("refusal body = %s, want it to name the cap", strings.TrimSpace(body))
	}

	// And the customer is still there, which is the point.
	if got := env.get(t, "/api/v1/subjects/"+itoa64(subjectID), sellerToken); got.Code != http.StatusOK {
		t.Errorf("subject went away despite the refusal: %d", got.Code)
	}
}

// The bulk path must not be a way around it. It calls the same service method,
// and this proves that rather than assuming it.
func TestBulkDeleteIsAlsoRefusedPastTheCap(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	sellerToken, subjectID := seedTenantWithRole(
		t, env, "seller2", "reseller", svcID, adminToken)

	nodeID := nodeOfService(t, env, svcID)
	recordUsageFor(t, env, nodeID, subjectID, 5000)
	setCap(t, env, "seller2", 1000)

	res := env.post(t, "/api/v1/subjects/bulk/delete",
		`{"subject_ids":[`+itoa64(subjectID)+`]}`, sellerToken)
	// Bulk reports per-row outcomes rather than failing the batch, so the
	// signal is that nothing was deleted.
	if body := res.Body.String(); !strings.Contains(body, `"deleted":0`) {
		t.Errorf("bulk delete reported %s, want deleted:0", strings.TrimSpace(body))
	}
	if got := env.get(t, "/api/v1/subjects/"+itoa64(subjectID), sellerToken); got.Code != http.StatusOK {
		t.Errorf("bulk delete removed a customer past the cap: %d", got.Code)
	}
}

// Under the cap it goes through, so the control cannot be mistaken for a
// blanket refusal that would pass the tests above while breaking deletion.
func TestDeleteIsAllowedUnderTheCap(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	sellerToken, subjectID := seedTenantWithRole(
		t, env, "seller3", "reseller", svcID, adminToken)

	nodeID := nodeOfService(t, env, svcID)
	recordUsageFor(t, env, nodeID, subjectID, 100)
	setCap(t, env, "seller3", 1000)

	if res := env.delete(t, "/api/v1/subjects/"+itoa64(subjectID), sellerToken); res.Code != http.StatusNoContent &&
		res.Code != http.StatusOK {
		t.Fatalf("delete under the cap -> %d %s", res.Code, strings.TrimSpace(res.Body.String()))
	}
}

// No cap is the default, and an upgrade must not start refusing deletions.
func TestNoCapMeansNoRefusal(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	sellerToken, subjectID := seedTenantWithRole(
		t, env, "seller4", "reseller", svcID, adminToken)

	nodeID := nodeOfService(t, env, svcID)
	recordUsageFor(t, env, nodeID, subjectID, 1<<40)
	// delete_cap_bytes left NULL, as every existing admin has it.

	if res := env.delete(t, "/api/v1/subjects/"+itoa64(subjectID), sellerToken); res.Code == http.StatusForbidden {
		t.Errorf("an admin with no cap was refused: %d %s",
			res.Code, strings.TrimSpace(res.Body.String()))
	}
}

// A super admin sets the caps, so a panel where they cannot remove a heavy
// user is a panel that fills up with them.
func TestSuperAdminIsExemptFromTheCap(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	_, subjectID := seedTenantWithRole(t, env, "seller5", "reseller", svcID, adminToken)

	nodeID := nodeOfService(t, env, svcID)
	recordUsageFor(t, env, nodeID, subjectID, 1<<40)
	setCap(t, env, "root", 1)

	if res := env.delete(t, "/api/v1/subjects/"+itoa64(subjectID), adminToken); res.Code == http.StatusForbidden {
		t.Errorf("super admin refused by a cap: %d %s",
			res.Code, strings.TrimSpace(res.Body.String()))
	}
}

// nodeOfService resolves the node a seeded service belongs to. usage_rollups
// references nodes, so the usage has to be attributed to a real one.
func nodeOfService(t *testing.T, env *testEnv, svcID int64) int64 {
	t.Helper()
	var nodeID int64
	if err := env.store.Read().QueryRowContext(context.Background(),
		`SELECT node_id FROM services WHERE id = ?`, svcID).Scan(&nodeID); err != nil {
		t.Fatalf("resolve node for service %d: %v", svcID, err)
	}
	return nodeID
}

// PLANS. user_presets carried quota, validity and auto-assigned services with
// full CRUD, routes and a management screen, and nothing had ever applied one
// to a subject -- a catalogue of products that could not be sold.

func createPlan(t *testing.T, env *testEnv, token, body string) int64 {
	t.Helper()
	res := env.post(t, "/api/v1/presets/users", body, token)
	if res.Code != http.StatusCreated && res.Code != http.StatusOK {
		t.Fatalf("create preset -> %d %s", res.Code, strings.TrimSpace(res.Body.String()))
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode preset: %v", err)
	}
	return out.ID
}

func plannedSubjectRow(t *testing.T, env *testEnv) (expiresAt, onHold sql.NullInt64) {
	t.Helper()
	if err := env.store.Read().QueryRowContext(context.Background(),
		`SELECT expires_at, on_hold_seconds FROM subjects WHERE name = ?`,
		"planned").Scan(&expiresAt, &onHold); err != nil {
		t.Fatalf("read subject: %v", err)
	}
	return
}

func TestAPlanSetsTheExpiryItSells(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	planID := createPlan(t, env, adminToken,
		`{"name":"monthly","validity_days":30,"is_public":true}`)

	res := env.post(t, "/api/v1/subjects",
		`{"name":"planned","preset_id":`+itoa64(planID)+`}`, adminToken)
	if res.Code != http.StatusCreated {
		t.Fatalf("create from plan -> %d %s", res.Code, strings.TrimSpace(res.Body.String()))
	}

	expires, onHold := plannedSubjectRow(t, env)
	if !expires.Valid {
		t.Error("a 30-day plan produced a subject with no expiry at all")
	}
	if onHold.Valid {
		t.Error("an ordinary plan put the subject on hold")
	}
}

// The difference between the two plan shapes is only WHEN the count starts.
func TestAnOnHoldPlanSellsWithoutStarting(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	planID := createPlan(t, env, adminToken,
		`{"name":"monthly-hold","validity_days":30,"on_hold":true,"is_public":true}`)

	res := env.post(t, "/api/v1/subjects",
		`{"name":"planned","preset_id":`+itoa64(planID)+`}`, adminToken)
	if res.Code != http.StatusCreated {
		t.Fatalf("create from on-hold plan -> %d %s", res.Code, strings.TrimSpace(res.Body.String()))
	}

	expires, onHold := plannedSubjectRow(t, env)
	if expires.Valid {
		t.Error("an on-hold plan set a fixed expiry; the clock must not have started")
	}
	if !onHold.Valid || onHold.Int64 != 30*24*60*60 {
		t.Errorf("on_hold_seconds = %v, want %d", onHold, 30*24*60*60)
	}
}

// A plan is a default, not a cage: what the operator typed wins.
func TestAnExplicitExpiryBeatsThePlan(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	planID := createPlan(t, env, adminToken,
		`{"name":"monthly2","validity_days":30,"is_public":true}`)

	const chosen = 4102444800 // far future, and not 30 days from now
	res := env.post(t, "/api/v1/subjects",
		`{"name":"planned","preset_id":`+itoa64(planID)+`,"expires_at":`+itoa64(chosen)+`}`,
		adminToken)
	if res.Code != http.StatusCreated {
		t.Fatalf("create -> %d %s", res.Code, strings.TrimSpace(res.Body.String()))
	}

	expires, _ := plannedSubjectRow(t, env)
	if expires.Int64 != chosen {
		t.Errorf("expires_at = %d, want %d: the plan overwrote what the operator "+
			"had already typed, which makes the plan dropdown a trap",
			expires.Int64, chosen)
	}
}

// An unknown or unreadable plan is refused, not ignored. A subject created on
// no plan when one was named is a billing discrepancy, not a default -- and
// GetPreset is what stops a reseller applying a competitor's private plan.
func TestAnUnknownPlanIsRefused(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)

	res := env.post(t, "/api/v1/subjects",
		`{"name":"planned","preset_id":424242}`, adminToken)
	if res.Code != http.StatusBadRequest {
		t.Errorf("create with an unknown preset -> %d %s; want %d",
			res.Code, strings.TrimSpace(res.Body.String()), http.StatusBadRequest)
	}
}
