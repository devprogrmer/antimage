package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/auth"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

// Extensions to the admin roster: change role/status, reset password, change
// my own password, list roles. Master's admins.go has List/Create/Delete;
// this file adds what a public panel still needs.

var (
	errAdminUnknownRole = errors.New("unknown role")
)

type updateAdminRequest struct {
	// Nullable pointers: only fields the caller sent are applied. A missing
	// role_id must not silently reset the admin to a default.
	RoleID *int64  `json:"role_id,omitempty"`
	Status *string `json:"status,omitempty"`
}

// handleUpdateAdmin changes role and/or status. Self-lockout guards keep the
// caller from removing their own access -- the panel's last super-admin has
// to always exist, and a self-suspend or self-role-downgrade could remove it.
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
				return errAdminUnknownRole
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
			// A suspended admin must not carry live sessions.
			if *req.Status == "suspended" {
				if _, err := tx.ExecContext(ctx,
					`DELETE FROM sessions WHERE admin_id = ?`, id); err != nil {
					return err
				}
			}
		}
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action: "admin.update", TargetType: "admin",
			TargetID: sql.NullInt64{Int64: id, Valid: true},
			After: map[string]any{
				"role_changed":   req.RoleID != nil,
				"status_changed": req.Status != nil,
			},
			Result: "ok",
		})
	})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		WriteError(w, http.StatusNotFound, "not_found", "admin not found")
		return
	case errors.Is(err, errAdminUnknownRole):
		WriteError(w, http.StatusUnprocessableEntity, "unknown_role", "role_id does not exist")
		return
	case err != nil:
		WriteError(w, http.StatusInternalServerError, "internal", "could not update admin")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type setPasswordRequest struct {
	NewPassword string `json:"new_password"`
}

// handleResetAdminPassword sets a new password for another admin without
// requiring the old one. Every session the target holds is revoked -- a
// rotation that leaves live sessions is theatre.
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

// handleChangeMyPassword requires the current password, so a session cookie
// alone cannot rotate. All other sessions for this admin are dropped after
// the write.
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
	if err := d.Store.Read().QueryRowContext(ctx,
		`SELECT password_hash FROM admins WHERE id = ?`, actor.AdminID).Scan(&currentHash); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not read profile")
		return
	}
	ok2, err := auth.VerifyPassword(currentHash, req.CurrentPassword)
	if err != nil || !ok2 {
		if d.Limiter != nil {
			_ = d.Limiter.RecordFailure(ctx, adminUsername(ctx, d, actor.AdminID), clientIP(r))
		}
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
	w.WriteHeader(http.StatusNoContent)
}

func adminUsername(ctx context.Context, d Deps, adminID int64) string {
	var u string
	_ = d.Store.Read().QueryRowContext(ctx,
		`SELECT username FROM admins WHERE id = ?`, adminID).Scan(&u)
	return u
}

// handleListRoles lists roles with the number of admins holding each one.
type roleRow struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	IsBuiltin   bool     `json:"is_builtin"`
	Permissions []string `json:"permissions"`
	Assigned    int      `json:"assigned"`
}

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
