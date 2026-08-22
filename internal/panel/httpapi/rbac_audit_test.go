package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/amyrm/antimage/internal/panel/rbac"
)

func TestRBACAuditLogging(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "admin", "password", "super_admin")
	env.seedAdmin(t, "readonly", "password", "readonly")

	adminToken := env.login(t, "admin", "password")
	readonlyToken := env.login(t, "readonly", "password")

	t.Run("audit denied authorization on freeze", func(t *testing.T) {
		// Try to freeze subject with readonly account (should be denied)
		w := env.post(t, "/api/v1/subjects/1/freeze", `{"reason":"test"}`, readonlyToken)

		if w.Code != http.StatusForbidden {
			t.Errorf("expected 403 Forbidden, got %d: %s", w.Code, w.Body.String())
		}

		// Debug: list all audit logs
		rows, err := env.store.Read().Query(`SELECT id, action, result FROM audit_log ORDER BY id`)
		if err != nil {
			t.Fatalf("failed to query all audit logs: %v", err)
		}
		defer rows.Close()

		t.Log("All audit logs after freeze attempt:")
		for rows.Next() {
			var id int64
			var action, result string
			rows.Scan(&id, &action, &result)
			t.Logf("  id=%d action=%s result=%s", id, action, result)
		}

		// Check audit log for denial (new endpoints use 'rbac_check')
		var action, result, afterJSON string
		var actorAdminID int64
		err = env.store.Read().QueryRow(`
			SELECT action, result, actor_admin_id, after_json
			FROM audit_log
			WHERE action = 'rbac_check' AND result = 'denied'
			ORDER BY id DESC LIMIT 1`).Scan(&action, &result, &actorAdminID, &afterJSON)

		if err != nil {
			t.Fatalf("failed to query audit log: %v", err)
		}

		if action != "rbac_check" {
			t.Errorf("expected action 'rbac_check', got %s", action)
		}
		if result != "denied" {
			t.Errorf("expected result 'denied', got %s", result)
		}

		// Verify metadata
		var metadata map[string]interface{}
		if err := json.Unmarshal([]byte(afterJSON), &metadata); err != nil {
			t.Fatalf("failed to unmarshal metadata: %v", err)
		}

		if metadata["permission"] != "subject:write" {
			t.Errorf("expected permission 'subject:write', got %v", metadata["permission"])
		}
		if metadata["method"] != "POST" {
			t.Errorf("expected method 'POST', got %v", metadata["method"])
		}
	})

	t.Run("audit successful authorization with requirePermissionAuditGrant", func(t *testing.T) {
		// Get initial audit count
		var countBefore int
		err := env.store.Read().QueryRow(`
			SELECT COUNT(*) FROM audit_log
			WHERE action = 'rbac_check' AND result = 'ok'`).Scan(&countBefore)
		if err != nil {
			t.Fatalf("failed to count audit logs: %v", err)
		}

		// Make a successful request (admin has permission)
		w := env.get(t, "/api/v1/subjects", adminToken)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200 OK, got %d", w.Code)
		}

		// Normal operations don't audit grants (too verbose)
		// Only sensitive operations using requirePermissionAuditGrant do
		var countAfter int
		err = env.store.Read().QueryRow(`
			SELECT COUNT(*) FROM audit_log
			WHERE action = 'rbac_check' AND result = 'ok'`).Scan(&countAfter)
		if err != nil {
			t.Fatalf("failed to count audit logs: %v", err)
		}

		// List operation doesn't use requirePermissionAuditGrant
		if countAfter != countBefore {
			t.Errorf("expected no audit grant for list operation, count changed from %d to %d", countBefore, countAfter)
		}
	})

	t.Run("audit records actor information", func(t *testing.T) {
		// Try another denied operation using our new audit functions (freeze endpoint)
		w := env.do(t, "POST", "/api/v1/subjects/1/freeze", `{"reason":"test"}`, readonlyToken)

		if w.Code != http.StatusForbidden {
			t.Errorf("expected 403 Forbidden, got %d", w.Code)
		}

		// Verify actor information in audit log (new endpoints use 'rbac_check')
		var actorType, actorLabel, actorIP, afterJSON string
		var actorAdminID sql.NullInt64
		err := env.store.Read().QueryRow(`
			SELECT actor_type, actor_admin_id, actor_label, actor_ip, after_json
			FROM audit_log
			WHERE action = 'rbac_check' AND result = 'denied'
			ORDER BY id DESC LIMIT 1`).Scan(&actorType, &actorAdminID, &actorLabel, &actorIP, &afterJSON)

		if err != nil {
			t.Fatalf("failed to query audit log: %v", err)
		}

		if actorType != "admin" {
			t.Errorf("expected actor_type 'admin', got %s", actorType)
		}
		if !actorAdminID.Valid {
			t.Error("expected actor_admin_id to be set")
		}

		// Verify role in metadata
		var metadata map[string]interface{}
		if err := json.Unmarshal([]byte(afterJSON), &metadata); err != nil {
			t.Fatalf("failed to unmarshal metadata: %v", err)
		}

		if metadata["actor_role"] != "readonly" {
			t.Errorf("expected actor_role 'readonly', got %v", metadata["actor_role"])
		}
		if metadata["is_super"] != false {
			t.Errorf("expected is_super false, got %v", metadata["is_super"])
		}
	})
}

func TestRequirePermissionHelpers(t *testing.T) {
	env := newTestEnv(t)

	t.Run("requirePermission returns false on denial", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()

		// Create actor without permission
		actor := &rbac.Actor{
			AdminID:  1,
			RoleName: "custom",
			IsSuper:  false,
			Perms:    map[rbac.Permission]struct{}{},
		}
		req = req.WithContext(context.WithValue(req.Context(), ctxActor, actor))

		deps := Deps{Store: env.store}
		allowed := deps.requirePermission(w, req, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone})

		if allowed {
			t.Error("expected requirePermission to return false")
		}
		if w.Code != http.StatusForbidden {
			t.Errorf("expected 403 status code, got %d", w.Code)
		}
	})

	t.Run("requirePermission returns true on success", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()

		// Create super admin actor
		actor := &rbac.Actor{
			AdminID:  1,
			RoleName: "super_admin",
			IsSuper:  true,
			Perms: map[rbac.Permission]struct{}{
				rbac.PermSubjectWrite: {},
			},
		}
		req = req.WithContext(context.WithValue(req.Context(), ctxActor, actor))

		deps := Deps{Store: env.store}
		allowed := deps.requirePermission(w, req, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone})

		if !allowed {
			t.Error("expected requirePermission to return true")
		}
		if w.Code == http.StatusForbidden {
			t.Error("should not have set 403 status")
		}
	})
}
