package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/amyrm/antimage/internal/panel/auth"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

// TestListAdmins_And_Roles proves an operator can see who has access to the
// panel from the browser -- without opening the database. Full CRUD is not
// in this release, but a list is a hard requirement for a public panel.
func TestListAdmins_And_Roles(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	// The default fixture admin has node:read+write but not admin:manage.
	actor.Perms[rbac.PermAdminManage] = struct{}{}

	ctx := context.Background()
	// Seed a second admin with a role of its own and one scope so both
	// columns of the roster have something non-zero to prove.
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO roles (id, name, is_builtin, permissions)
			 VALUES (10, 'tenant-viewer', 1, '["subject:read","node:read"]')`); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO admins (username, password_hash, role_id, created_at)
			 VALUES ('viewer', 'x', 10, ?)`, time.Now().Unix())
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO admin_scopes (admin_id, scope_type, scope_id) VALUES (?, 'node', 1)`,
			id); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// /admins
	req := httptest.NewRequest("GET", "/api/v1/admins", nil)
	req = req.WithContext(withActor(req.Context(), actor))
	w := httptest.NewRecorder()
	deps.handleListAdmins(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("admins status %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Admins []adminRow `json:"admins"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("parse admins: %v", err)
	}
	// The default fixture creates 'testadmin' plus we added 'viewer'.
	if len(got.Admins) < 2 {
		t.Errorf("expected at least 2 admins, got %d", len(got.Admins))
	}
	// The scoped admin has to report scopes > 0 or the "unrestricted"
	// rendering in the UI would misrepresent them.
	var viewer *adminRow
	for i := range got.Admins {
		if got.Admins[i].Username == "viewer" {
			viewer = &got.Admins[i]
		}
	}
	if viewer == nil || viewer.Scopes == 0 {
		t.Errorf("viewer missing or scopes=0: %+v", viewer)
	}

	// /roles
	req = httptest.NewRequest("GET", "/api/v1/roles", nil)
	req = req.WithContext(withActor(req.Context(), actor))
	w = httptest.NewRecorder()
	deps.handleListRoles(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("roles status %d: %s", w.Code, w.Body.String())
	}
	var gotR struct {
		Roles []roleRow `json:"roles"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &gotR); err != nil {
		t.Fatalf("parse roles: %v", err)
	}
	// Find tenant-viewer and prove its permissions decoded.
	var tv *roleRow
	for i := range gotR.Roles {
		if gotR.Roles[i].Name == "tenant-viewer" {
			tv = &gotR.Roles[i]
		}
	}
	if tv == nil {
		t.Fatalf("tenant-viewer role missing: %+v", gotR.Roles)
	}
	if len(tv.Permissions) != 2 {
		t.Errorf("tenant-viewer perms decoded wrong: %+v", tv.Permissions)
	}
	if tv.Assigned != 1 {
		t.Errorf("assigned count for tenant-viewer = %d, want 1", tv.Assigned)
	}
}

// TestListAdmins_ForbiddenWithoutManage proves the reserved permission.
// A reseller with subject:read must not be able to enumerate panel admins.
func TestListAdmins_ForbiddenWithoutManage(t *testing.T) {
	deps, _, actor := setupTestDeps(t)
	// Explicitly strip: the fixture defaults to super, which bypasses scope
	// but never permission -- so downgrading it here proves the perm gate.
	actor.IsSuper = false
	actor.Perms = map[rbac.Permission]struct{}{rbac.PermSubjectRead: {}}

	req := httptest.NewRequest("GET", "/api/v1/admins", nil)
	req = req.WithContext(withActor(req.Context(), actor))
	w := httptest.NewRecorder()
	deps.handleListAdmins(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("status %d, want 403", w.Code)
	}
}

// TestCreateAdmin proves a working create path plus the two rejections that
// matter for a public panel: an unknown role must not silently create a
// role-less admin, and a duplicate username must return 409 rather than 500.
func TestCreateAdmin(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	actor.Perms[rbac.PermAdminManage] = struct{}{}

	body := createAdminRequest{Username: "op2", Password: "correct horse", RoleID: 1}
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/admins", bytes.NewReader(buf))
	req = req.WithContext(withActor(req.Context(), actor))
	w := httptest.NewRecorder()
	deps.handleCreateAdmin(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	// The password_hash column must NOT hold the plaintext.
	var hash string
	if err := s.Read().QueryRowContext(context.Background(),
		`SELECT password_hash FROM admins WHERE username = 'op2'`).Scan(&hash); err != nil {
		t.Fatalf("read admin: %v", err)
	}
	if strings.Contains(hash, "correct horse") {
		t.Errorf("password stored in plaintext: %q", hash)
	}
	ok, err := auth.VerifyPassword(hash, "correct horse")
	if err != nil || !ok {
		t.Errorf("hashed password does not verify: err=%v ok=%v", err, ok)
	}

	// Duplicate username → 409, not 500. A 500 would tell the caller
	// nothing beyond "server broke", and the panel already knows why.
	dup := createAdminRequest{Username: "op2", Password: "another one", RoleID: 1}
	buf, _ = json.Marshal(dup)
	req = httptest.NewRequest("POST", "/api/v1/admins", bytes.NewReader(buf))
	req = req.WithContext(withActor(req.Context(), actor))
	w = httptest.NewRecorder()
	deps.handleCreateAdmin(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("duplicate username status = %d, want 409", w.Code)
	}

	// Unknown role → 422.
	bad := createAdminRequest{Username: "op3", Password: "another one", RoleID: 9999}
	buf, _ = json.Marshal(bad)
	req = httptest.NewRequest("POST", "/api/v1/admins", bytes.NewReader(buf))
	req = req.WithContext(withActor(req.Context(), actor))
	w = httptest.NewRecorder()
	deps.handleCreateAdmin(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown role status = %d, want 422", w.Code)
	}
}

// TestAdminSelfLockoutGuards proves both self-lockout guards: an admin
// cannot suspend themselves and cannot delete themselves. Without these
// the last super-admin could lock the whole panel out with one wrong click.
func TestAdminSelfLockoutGuards(t *testing.T) {
	deps, _, actor := setupTestDeps(t)
	actor.Perms[rbac.PermAdminManage] = struct{}{}

	// Suspend self via PUT
	body, _ := json.Marshal(map[string]string{"status": "suspended"})
	req := httptest.NewRequest("PUT", "/api/v1/admins/"+itoaLocal(actor.AdminID), bytes.NewReader(body))
	req = req.WithContext(withActor(req.Context(), actor))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("adminID", itoaLocal(actor.AdminID))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	deps.handleUpdateAdmin(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("self-suspend status = %d, want 422", w.Code)
	}

	// Delete self
	req = httptest.NewRequest("DELETE", "/api/v1/admins/"+itoaLocal(actor.AdminID), nil)
	req = req.WithContext(withActor(req.Context(), actor))
	rctx = chi.NewRouteContext()
	rctx.URLParams.Add("adminID", itoaLocal(actor.AdminID))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w = httptest.NewRecorder()
	deps.handleDeleteAdmin(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("self-delete status = %d, want 422", w.Code)
	}
}

// TestResetPasswordRevokesSessions is the whole point of the reset route: a
// password change that leaves live sessions is theatre. The reset must
// remove every session the target holds.
func TestResetPasswordRevokesSessions(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	actor.Perms[rbac.PermAdminManage] = struct{}{}

	ctx := context.Background()
	// A second admin with a live session.
	var otherID int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO admins (username, password_hash, role_id, created_at)
			 VALUES ('victim', 'x', 1, ?)`, time.Now().Unix())
		if err != nil {
			return err
		}
		otherID, _ = res.LastInsertId()
		_, err = tx.ExecContext(ctx,
			`INSERT INTO sessions (id, admin_id, token_hash, ip, user_agent,
			                      created_at, expires_at, last_used_at)
			 VALUES (777, ?, ?, '127.0.0.1', 'test', ?, ?, ?)`,
			otherID, []byte("h"), time.Now().Unix(),
			time.Now().Add(time.Hour).Unix(), time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	body, _ := json.Marshal(setPasswordRequest{NewPassword: "brand new password"})
	req := httptest.NewRequest("POST",
		"/api/v1/admins/"+itoaLocal(otherID)+"/password", bytes.NewReader(body))
	req = req.WithContext(withActor(req.Context(), actor))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("adminID", itoaLocal(otherID))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	deps.handleResetAdminPassword(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	var live int
	if err := s.Read().QueryRowContext(ctx,
		`SELECT count(*) FROM sessions WHERE admin_id = ?`, otherID).Scan(&live); err != nil {
		t.Fatalf("session count: %v", err)
	}
	if live != 0 {
		t.Errorf("victim still holds %d sessions after reset", live)
	}
}

// TestChangeMyPassword_RequiresCurrent proves that a session cookie alone
// cannot rotate the password: the old password is required.
func TestChangeMyPassword_RequiresCurrent(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	// The rate limiter is required by the denial path; nil would panic.
	deps.Limiter = auth.NewLimiter(s, deps.Now)

	// Seed a known password on the fixture admin.
	hash, err := auth.HashPassword("old password 12")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(),
			`UPDATE admins SET password_hash = ? WHERE id = ?`, hash, actor.AdminID)
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Wrong current: 401.
	body, _ := json.Marshal(changePasswordRequest{
		CurrentPassword: "wrong old", NewPassword: "brand new password",
	})
	req := httptest.NewRequest("POST", "/api/v1/me/password", bytes.NewReader(body))
	req = req.WithContext(withActor(req.Context(), actor))
	w := httptest.NewRecorder()
	deps.handleChangeMyPassword(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong-current status = %d, want 401", w.Code)
	}

	// Correct current + valid new: 204, hash actually changes.
	body, _ = json.Marshal(changePasswordRequest{
		CurrentPassword: "old password 12", NewPassword: "brand new password",
	})
	req = httptest.NewRequest("POST", "/api/v1/me/password", bytes.NewReader(body))
	req = req.WithContext(withActor(req.Context(), actor))
	w = httptest.NewRecorder()
	deps.handleChangeMyPassword(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var newHash string
	_ = s.Read().QueryRowContext(context.Background(),
		`SELECT password_hash FROM admins WHERE id = ?`, actor.AdminID).Scan(&newHash)
	ok, _ := auth.VerifyPassword(newHash, "brand new password")
	if !ok {
		t.Errorf("password did not change to the new value")
	}
}

func itoaLocal(i int64) string { return strconv.FormatInt(i, 10) }
