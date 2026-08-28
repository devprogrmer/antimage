package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/auth"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

type adminDTO struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"created_at"`
}

func (d Deps) handleListAdmins(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermAdminManage, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	rows, err := d.Store.Read().QueryContext(r.Context(),
		`SELECT a.id, a.username, r.name, a.status, a.created_at
		   FROM admins a JOIN roles r ON r.id = a.role_id
		  ORDER BY a.username`)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not list admins")
		return
	}
	defer func() { _ = rows.Close() }()
	out := make([]adminDTO, 0)
	for rows.Next() {
		var a adminDTO
		if err := rows.Scan(&a.ID, &a.Username, &a.Role, &a.Status, &a.CreatedAt); err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "could not read admins")
			return
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not read admins")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"admins": out})
}

func (d Deps) handleCreateAdmin(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermAdminManage, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" {
		WriteError(w, http.StatusUnprocessableEntity, "validation", "username and password are required")
		return
	}
	if len(req.Password) < 8 {
		WriteError(w, http.StatusUnprocessableEntity, "validation", "password must be at least 8 characters")
		return
	}
	if _, ok := rbac.BuiltinRoles()[req.Role]; !ok {
		WriteError(w, http.StatusUnprocessableEntity, "validation", "unknown role")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not hash password")
		return
	}
	ctx := r.Context()
	var id int64
	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		for name, perms := range rbac.BuiltinRoles() {
			encoded, err := json.Marshal(perms)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO roles (name, is_builtin, permissions) VALUES (?, 1, ?)
				 ON CONFLICT(name) DO UPDATE SET permissions = excluded.permissions`,
				name, string(encoded)); err != nil {
				return err
			}
		}
		var roleID int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM roles WHERE name = ?`, req.Role).Scan(&roleID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO admins (username, password_hash, role_id, created_at) VALUES (?,?,?,?)`,
			req.Username, hash, roleID, d.now().Unix())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return err
		}
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action: "admin.create", TargetType: "admin",
			TargetID: sql.NullInt64{Int64: id, Valid: true}, Result: "ok",
			After: map[string]any{"username": req.Username, "role": req.Role},
		})
	})
	if err != nil {
		if isUniqueViolation(err) {
			WriteError(w, http.StatusConflict, "conflict", "an admin with that username already exists")
			return
		}
		WriteError(w, http.StatusInternalServerError, "internal", "could not create admin")
		return
	}
	WriteJSON(w, http.StatusCreated, map[string]any{"id": id})
}

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
		WriteError(w, http.StatusUnprocessableEntity, "validation", "cannot delete your own account")
		return
	}
	ctx := r.Context()
	now := time.Now().UTC().Unix()
	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE sessions SET revoked_at = ? WHERE admin_id = ? AND revoked_at IS NULL`, now, id); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM admins WHERE id = ?`, id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return sql.ErrNoRows
		}
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action: "admin.delete", TargetType: "admin",
			TargetID: sql.NullInt64{Int64: id, Valid: true}, Result: "ok",
		})
	})
	if err != nil {
		if err == sql.ErrNoRows {
			WriteError(w, http.StatusNotFound, "not_found", "admin not found")
			return
		}
		WriteError(w, http.StatusInternalServerError, "internal", "could not delete admin")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
