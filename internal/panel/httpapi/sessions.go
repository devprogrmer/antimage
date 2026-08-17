package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
)

type sessionDTO struct {
	ID         int64  `json:"id"`
	IP         string `json:"ip"`
	UserAgent  string `json:"user_agent"`
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt int64  `json:"last_used_at"`
	ExpiresAt  int64  `json:"expires_at"`
	Current    bool   `json:"current"`
}

// handleListSessions returns the caller's own live sessions.
//
// There is deliberately no authorize() call: the query is already scoped to
// the authenticated admin's id, so the permission that would gate it is
// "being logged in", which requireActor and authMiddleware already established.
// The token hash is never selected — the list identifies sessions, it does not
// hand out the means to use them.
func (d Deps) handleListSessions(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	current := sessionIDFrom(ctx)

	rows, err := d.Store.Read().QueryContext(ctx,
		`SELECT id, ip, user_agent, created_at, last_used_at, expires_at
		   FROM sessions
		  WHERE admin_id = ? AND revoked_at IS NULL
		  ORDER BY last_used_at DESC, id DESC`, actor.AdminID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not list sessions")
		return
	}
	defer func() { _ = rows.Close() }()

	out := []sessionDTO{}
	for rows.Next() {
		var s sessionDTO
		if err := rows.Scan(&s.ID, &s.IP, &s.UserAgent,
			&s.CreatedAt, &s.LastUsedAt, &s.ExpiresAt); err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "could not read sessions")
			return
		}
		s.Current = s.ID == current
		out = append(out, s)
	}
	// A truncated session list reads as "nothing else is signed in", which is
	// exactly the wrong answer to the question this endpoint exists to answer.
	if err := rows.Err(); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not read sessions")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

func (d Deps) handleRevokeSession(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "sessionID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid session id")
		return
	}
	ctx := r.Context()

	// Ownership check: an admin revokes only their own sessions; a super
	// admin may revoke any. 404 rather than 403 so session ids are not
	// probeable.
	var owner int64
	err = d.Store.Read().QueryRowContext(ctx,
		`SELECT admin_id FROM sessions WHERE id = ?`, id).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "not_found", "session not found")
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not load session")
		return
	}
	if owner != actor.AdminID && !actor.IsSuper {
		WriteError(w, http.StatusNotFound, "not_found", "session not found")
		return
	}

	if err := d.Sessions.Revoke(ctx, id); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not revoke session")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
