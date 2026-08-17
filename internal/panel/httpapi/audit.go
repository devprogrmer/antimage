package httpapi

import (
	"net/http"
	"strconv"

	"github.com/amyrm/antimage/internal/panel/rbac"
)

// auditPageSize bounds the response. The cap exists so a single request
// cannot pull the whole log into memory.
const (
	auditDefaultLimit = 100
	auditMaxLimit     = 500
)

type auditEntryDTO struct {
	ID         int64  `json:"id"`
	At         int64  `json:"at"`
	ActorType  string `json:"actor_type"`
	ActorName  string `json:"actor_name"`
	ActorLabel string `json:"actor_label"`
	ActorIP    string `json:"actor_ip"`
	RequestID  string `json:"request_id"`
	Action     string `json:"action"`
	TargetType string `json:"target_type"`
	TargetID   int64  `json:"target_id"`
	Result     string `json:"result"`
}

// handleListAudit returns the audit log newest-first.
//
// Exposure, stated plainly: the only gate is the audit:read permission. There
// is no scope filter, so a node-scoped admin — the builtin admin role holds
// audit:read and may be scoped to a subset of nodes — sees rows for nodes
// outside their scope, including other admins' login IP addresses. That is
// deliberate for SP1: the spec does not require scope-filtering the audit log,
// and the role that would make the leak dangerous (reseller) does not hold
// audit:read at all. TestAuditRequiresPermission pins that last fact, which is
// the only thing standing between this handler and a tenant-visible log.
// Narrowing this is a design decision, not a bug fix; do not add a filter here
// without one.
func (d Deps) handleListAudit(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermAuditRead, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	limit := auditDefaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= auditMaxLimit {
			limit = n
		}
	}

	rows, err := d.Store.Read().QueryContext(r.Context(),
		`SELECT a.id, a.at, a.actor_type, COALESCE(ad.username,''), a.actor_label,
		        a.actor_ip, a.request_id, a.action, a.target_type,
		        COALESCE(a.target_id,0), a.result
		   FROM audit_log a
		   LEFT JOIN admins ad ON ad.id = a.actor_admin_id
		  ORDER BY a.id DESC LIMIT ?`, limit)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not list audit entries")
		return
	}
	defer func() { _ = rows.Close() }()

	entries := []auditEntryDTO{}
	for rows.Next() {
		var e auditEntryDTO
		if err := rows.Scan(&e.ID, &e.At, &e.ActorType, &e.ActorName, &e.ActorLabel,
			&e.ActorIP, &e.RequestID, &e.Action, &e.TargetType, &e.TargetID, &e.Result); err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "could not read audit entries")
			return
		}
		entries = append(entries, e)
	}
	// Without this, a failure part-way through iteration is served as a
	// complete page: the operator reads a truncated audit log as the whole
	// truth, which is the one thing an audit log must never do.
	if err := rows.Err(); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not read audit entries")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"entries": entries})
}
