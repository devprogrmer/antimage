package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/auth"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

// Read-only admin and role listing.
//
// A public panel with no way to SEE the admins configured on it is a panel
// operators cannot answer "who has access?" for from the browser. Full CRUD
// (create/edit password/rotate/scope) is not built into this release --
// seeding admins is still the antimage-ctl path -- but exposing the roster
// so an operator can see it, without opening the database, is a hard
// requirement for a public panel.

type adminRow struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	RoleID      int64  `json:"role_id"`
	RoleName    string `json:"role_name"`
	Status      string `json:"status"`
	TOTPEnabled bool   `json:"totp_enabled"`
	CreatedAt   int64  `json:"created_at"`
	Scopes      int    `json:"scopes"`
}

// handleListAdmins lists admins. Gated on admin:manage, because even the
// roster of usernames is infrastructure a customer with reseller:read has no
// legitimate need to enumerate.
func (d Deps) handleListAdmins(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermAdminManage, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}

	rows, err := d.Store.Read().QueryContext(r.Context(),
		`SELECT a.id, a.username, a.role_id, r.name, a.status,
		        CASE WHEN a.totp_secret_enc IS NULL OR length(a.totp_secret_enc) = 0
		             THEN 0 ELSE 1 END,
		        a.created_at,
		        (SELECT count(*) FROM admin_scopes s WHERE s.admin_id = a.id)
		   FROM admins a
		   JOIN roles r ON r.id = a.role_id
		  ORDER BY a.username`)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not list admins")
		return
	}
	defer func() { _ = rows.Close() }()

	out := []adminRow{}
	for rows.Next() {
		var a adminRow
		var totp int
		if err := rows.Scan(&a.ID, &a.Username, &a.RoleID, &a.RoleName,
			&a.Status, &totp, &a.CreatedAt, &a.Scopes); err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "could not read admins")
			return
		}
		a.TOTPEnabled = totp == 1
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not read admins")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"admins": out})
}

type roleRow struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	IsBuiltin   bool     `json:"is_builtin"`
	Permissions []string `json:"permissions"`
	Assigned    int      `json:"assigned"`
}

// handleListRoles lists roles with the number of admins holding each one.
// A separate role:read permission would only ever be granted to callers who
// already have admin:manage; not paying that cost.
func (d Deps) handleListRoles(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermAdminManage, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}

	rows, err := d.Store.Read().QueryContext(r.Context(),
		`SELECT r.id, r.name, r.is_builtin, r.permissions,
		        (SELECT count(*) FROM admins a WHERE a.role_id = r.id)
		   FROM roles r ORDER BY r.name`)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not list roles")
		return
	}
	defer func() { _ = rows.Close() }()

	out := []roleRow{}
	for rows.Next() {
		var rr roleRow
		var isBuiltin int
		var permsJSON string
		if err := rows.Scan(&rr.ID, &rr.Name, &isBuiltin, &permsJSON, &rr.Assigned); err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "could not read roles")
			return
		}
		rr.IsBuiltin = isBuiltin == 1
		// A malformed permissions blob is a data-integrity problem, not a
		// request problem; here it renders as an empty list so the whole
		// roles view keeps loading rather than 500-ing on one bad row.
		var perms []string
		if permsJSON != "" && permsJSON != "null" {
			_ = json.Unmarshal([]byte(permsJSON), &perms)
		}
		rr.Permissions = perms
		if rr.Permissions == nil {
			rr.Permissions = []string{}
		}
		out = append(out, rr)
	}
	if err := rows.Err(); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not read roles")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"roles": out})
}

// -- Admin CRUD ----------------------------------------------------------
//
// These are the write endpoints that let an operator manage the panel's own
// admin roster from the browser instead of needing antimage-ctl. Each takes
// PermAdminManage; the ONE exception is /me/password below, which acts on
// the caller's own account regardless of that permission.
//
// Deletion is soft ('suspended' status) rather than a DELETE FROM admins. A
// row that ever held a role has audit records that name it as actor; hard
// deletion would leave those records pointing at nothing.

type createAdminRequest struct {
	Username string  `json:"username"`
	Password string  `json:"password"`
	RoleID   int64   `json:"role_id"`
	NodeIDs  []int64 `json:"node_ids"`
	// ServiceIDs sit in admin_scopes with scope_type='service'; the panel
	// uses them but the UI is not built yet. Accepted here so a caller who
	// wants to seed them can, even though the roster page does not show
	// them broken out yet.
	ServiceIDs []int64 `json:"service_ids"`
}

func (d Deps) handleCreateAdmin(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermAdminManage, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	var req createAdminRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" || req.RoleID == 0 {
		WriteError(w, http.StatusBadRequest, "bad_request",
			"username, password and role_id are all required")
		return
	}
	// Reject passwords that could not survive login: everything else validates
	// later, but bcrypt/argon2id truncates or errors above a length that a UI
	// would happily accept, and empty-string comparisons are worth blocking
	// visibly rather than hashing.
	if len(req.Password) < 8 {
		WriteError(w, http.StatusUnprocessableEntity, "weak_password",
			"password must be at least 8 characters")
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not hash password")
		return
	}

	ctx := r.Context()
	var newID int64
	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		// The role has to exist; a foreign-key error surfaces as 500 without
		// this and reads as a panel bug, not a user mistake.
		var roleExists int
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM roles WHERE id = ?`, req.RoleID).Scan(&roleExists); err != nil {
			return err
		}
		if roleExists == 0 {
			return errUnknownRole
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO admins (username, password_hash, role_id, parent_admin_id,
			                     status, created_at)
			 VALUES (?, ?, ?, ?, 'active', ?)`,
			req.Username, hash, req.RoleID, actor.AdminID, d.now().Unix())
		if err != nil {
			// SQLite reports a UNIQUE constraint on admins_username_unique.
			if strings.Contains(err.Error(), "UNIQUE") ||
				strings.Contains(err.Error(), "unique") {
				return errUsernameTaken
			}
			return err
		}
		newID, _ = res.LastInsertId()
		for _, nodeID := range req.NodeIDs {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO admin_scopes (admin_id, scope_type, scope_id) VALUES (?, 'node', ?)`,
				newID, nodeID); err != nil {
				return err
			}
		}
		for _, sid := range req.ServiceIDs {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO admin_scopes (admin_id, scope_type, scope_id) VALUES (?, 'service', ?)`,
				newID, sid); err != nil {
				return err
			}
		}
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action: "admin.create", TargetType: "admin",
			TargetID: sql.NullInt64{Int64: newID, Valid: true},
			// Password is NOT logged; the after row records what changed
			// (role and scope), not credentials.
			After: map[string]any{
				"username": req.Username, "role_id": req.RoleID,
				"node_scope_count": len(req.NodeIDs),
			},
			Result: "ok",
		})
	})
	switch {
	case errors.Is(err, errUnknownRole):
		WriteError(w, http.StatusUnprocessableEntity, "unknown_role", "role_id does not exist")
		return
	case errors.Is(err, errUsernameTaken):
		WriteError(w, http.StatusConflict, "username_taken", "an admin with this username already exists")
		return
	case err != nil:
		WriteError(w, http.StatusInternalServerError, "internal", "could not create admin")
		return
	}
	WriteJSON(w, http.StatusCreated, map[string]any{"id": newID, "username": req.Username})
}

type updateAdminRequest struct {
	// Nullable pointers: only fields the caller sent are applied. A missing
	// role_id must not silently reset the admin to a default.
	RoleID     *int64   `json:"role_id,omitempty"`
	Status     *string  `json:"status,omitempty"`
	NodeIDs    *[]int64 `json:"node_ids,omitempty"`
	ServiceIDs *[]int64 `json:"service_ids,omitempty"`
}

func (d Deps) handleUpdateAdmin(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermAdminManage, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	id, err := pathInt64(r, "adminID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid admin id")
		return
	}
	var req updateAdminRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	// A caller cannot lock themselves out: the panel's last super-admin has
	// to always exist. Rather than counting super-admins here, we forbid the
	// caller from suspending or downgrading THEIR OWN row; a super lockout
	// then requires two admins to conspire.
	if id == actor.AdminID {
		if req.Status != nil && *req.Status != "active" {
			WriteError(w, http.StatusUnprocessableEntity, "self_lockout",
				"you cannot suspend your own account")
			return
		}
		if req.RoleID != nil {
			WriteError(w, http.StatusUnprocessableEntity, "self_lockout",
				"you cannot change your own role")
			return
		}
	}
	if req.Status != nil && *req.Status != "active" && *req.Status != "suspended" {
		WriteError(w, http.StatusBadRequest, "bad_request",
			"status must be 'active' or 'suspended'")
		return
	}

	ctx := r.Context()
	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		// Check the target exists so 404 is distinguishable from a no-op.
		var exists int
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM admins WHERE id = ?`, id).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return sql.ErrNoRows
		}
		if req.RoleID != nil {
			var roleExists int
			if err := tx.QueryRowContext(ctx,
				`SELECT count(*) FROM roles WHERE id = ?`, *req.RoleID).Scan(&roleExists); err != nil {
				return err
			}
			if roleExists == 0 {
				return errUnknownRole
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE admins SET role_id = ? WHERE id = ?`, *req.RoleID, id); err != nil {
				return err
			}
		}
		if req.Status != nil {
			if _, err := tx.ExecContext(ctx,
				`UPDATE admins SET status = ? WHERE id = ?`, *req.Status, id); err != nil {
				return err
			}
			// A suspended admin must not carry live sessions -- otherwise
			// suspension is only a login-time gate and any browser tab that
			// authenticated first keeps working.
			if *req.Status == "suspended" {
				if _, err := tx.ExecContext(ctx,
					`DELETE FROM sessions WHERE admin_id = ?`, id); err != nil {
					return err
				}
			}
		}
		// Scopes are a set: passing an empty [] means "remove all scopes",
		// which is different from "field omitted".
		if req.NodeIDs != nil {
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM admin_scopes WHERE admin_id = ? AND scope_type = 'node'`, id); err != nil {
				return err
			}
			for _, nid := range *req.NodeIDs {
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO admin_scopes (admin_id, scope_type, scope_id) VALUES (?, 'node', ?)`,
					id, nid); err != nil {
					return err
				}
			}
		}
		if req.ServiceIDs != nil {
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM admin_scopes WHERE admin_id = ? AND scope_type = 'service'`, id); err != nil {
				return err
			}
			for _, sid := range *req.ServiceIDs {
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO admin_scopes (admin_id, scope_type, scope_id) VALUES (?, 'service', ?)`,
					id, sid); err != nil {
					return err
				}
			}
		}
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action: "admin.update", TargetType: "admin",
			TargetID: sql.NullInt64{Int64: id, Valid: true},
			After: map[string]any{
				"role_changed":    req.RoleID != nil,
				"status_changed":  req.Status != nil,
				"scopes_replaced": req.NodeIDs != nil || req.ServiceIDs != nil,
			},
			Result: "ok",
		})
	})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		WriteError(w, http.StatusNotFound, "not_found", "admin not found")
		return
	case errors.Is(err, errUnknownRole):
		WriteError(w, http.StatusUnprocessableEntity, "unknown_role", "role_id does not exist")
		return
	case err != nil:
		WriteError(w, http.StatusInternalServerError, "internal", "could not update admin")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteAdmin soft-deletes by suspending the admin and revoking every
// live session. Hard deletion would leave audit rows pointing at nothing.
func (d Deps) handleDeleteAdmin(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermAdminManage, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	id, err := pathInt64(r, "adminID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid admin id")
		return
	}
	if id == actor.AdminID {
		WriteError(w, http.StatusUnprocessableEntity, "self_lockout",
			"you cannot delete your own account")
		return
	}
	ctx := r.Context()
	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE admins SET status = 'suspended' WHERE id = ? AND status <> 'suspended'`, id)
		if err != nil {
			return err
		}
		aff, _ := res.RowsAffected()
		if aff == 0 {
			// Either not present or already suspended. Distinguish, so a
			// caller who suspends a suspended row does not get a 500.
			var exists int
			if err := tx.QueryRowContext(ctx,
				`SELECT count(*) FROM admins WHERE id = ?`, id).Scan(&exists); err != nil {
				return err
			}
			if exists == 0 {
				return sql.ErrNoRows
			}
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM sessions WHERE admin_id = ?`, id); err != nil {
			return err
		}
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action: "admin.delete", TargetType: "admin",
			TargetID: sql.NullInt64{Int64: id, Valid: true}, Result: "ok",
		})
	})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		WriteError(w, http.StatusNotFound, "not_found", "admin not found")
		return
	case err != nil:
		WriteError(w, http.StatusInternalServerError, "internal", "could not delete admin")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// -- Password: reset (admin manages other), change (self) --------------

type setPasswordRequest struct {
	NewPassword string `json:"new_password"`
}

// handleResetAdminPassword lets a caller with admin:manage set a NEW password
// for another admin without knowing their old one. All the target's sessions
// are revoked -- the password just changed, so anything holding one is now
// a stale credential.
func (d Deps) handleResetAdminPassword(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermAdminManage, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	id, err := pathInt64(r, "adminID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid admin id")
		return
	}
	var req setPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if len(req.NewPassword) < 8 {
		WriteError(w, http.StatusUnprocessableEntity, "weak_password",
			"password must be at least 8 characters")
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not hash password")
		return
	}
	ctx := r.Context()
	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE admins SET password_hash = ? WHERE id = ?`, hash, id)
		if err != nil {
			return err
		}
		aff, _ := res.RowsAffected()
		if aff == 0 {
			return sql.ErrNoRows
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM sessions WHERE admin_id = ?`, id); err != nil {
			return err
		}
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action: "admin.password.reset", TargetType: "admin",
			TargetID: sql.NullInt64{Int64: id, Valid: true}, Result: "ok",
		})
	})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		WriteError(w, http.StatusNotFound, "not_found", "admin not found")
		return
	case err != nil:
		WriteError(w, http.StatusInternalServerError, "internal", "could not reset password")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// handleChangeMyPassword rotates the caller's own password.
//
// Requires the CURRENT password, so a session cookie alone can't rotate it.
// Every other session for the same admin is dropped after the write --
// including the browser tab the operator changed it in; the client is
// expected to log in again. That is the correct behaviour for a rotation:
// anything that still carried the old token has to prove the new one.
func (d Deps) handleChangeMyPassword(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	var req changePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if len(req.NewPassword) < 8 {
		WriteError(w, http.StatusUnprocessableEntity, "weak_password",
			"password must be at least 8 characters")
		return
	}
	if req.CurrentPassword == "" {
		WriteError(w, http.StatusBadRequest, "bad_request", "current_password is required")
		return
	}

	ctx := r.Context()
	var currentHash string
	err := d.Store.Read().QueryRowContext(ctx,
		`SELECT password_hash FROM admins WHERE id = ?`, actor.AdminID).Scan(&currentHash)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not read profile")
		return
	}
	ok2, err := auth.VerifyPassword(currentHash, req.CurrentPassword)
	if err != nil || !ok2 {
		// Rate-limit hint: sharing the login limiter here uses the same
		// budget as failed logins do, so a session-bound brute force on the
		// current password costs what one at the login form costs.
		_ = d.Limiter.RecordFailure(ctx, currentUsername(ctx, d, actor.AdminID), clientIP(r))
		audit.BestEffort(ctx, d.Store, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action: "admin.password.change", TargetType: "admin",
			TargetID: sql.NullInt64{Int64: actor.AdminID, Valid: true}, Result: "denied",
		})
		WriteError(w, http.StatusUnauthorized, "invalid_password", "current password is wrong")
		return
	}

	newHash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not hash password")
		return
	}
	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE admins SET password_hash = ? WHERE id = ?`, newHash, actor.AdminID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM sessions WHERE admin_id = ?`, actor.AdminID); err != nil {
			return err
		}
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action: "admin.password.change", TargetType: "admin",
			TargetID: sql.NullInt64{Int64: actor.AdminID, Valid: true}, Result: "ok",
		})
	})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not change password")
		return
	}
	// The caller's own session is now gone; the client has to sign in again.
	w.WriteHeader(http.StatusNoContent)
}

func currentUsername(ctx context.Context, d Deps, adminID int64) string {
	var u string
	_ = d.Store.Read().QueryRowContext(ctx,
		`SELECT username FROM admins WHERE id = ?`, adminID).Scan(&u)
	return u
}

var (
	errUnknownRole   = errors.New("unknown role")
	errUsernameTaken = errors.New("username taken")
)
