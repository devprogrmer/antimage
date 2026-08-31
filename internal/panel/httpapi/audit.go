package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

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
	// The state either side of the change, as recorded. audit_log has carried
	// before_json and after_json since SP1 and the query never selected them,
	// so "what actually changed" was written down and unreadable.
	//
	// No payload written anywhere in the panel carries a credential -- the
	// keys are ids, names, counts and reasons -- so these are returned to a
	// holder of audit:read rather than redacted. A payload that ever does
	// carry one must be redacted at the point it is WRITTEN, not here.
	Before json.RawMessage `json:"before,omitempty"`
	After  json.RawMessage `json:"after,omitempty"`
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

	// Every filter is a bound parameter. An audit search assembled by string
	// concatenation is a search whose results can be rewritten by whoever
	// types into it, which is the one thing an audit log must not allow.
	where := []string{"1=1"}
	args := []any{}
	if v := strings.TrimSpace(r.URL.Query().Get("action")); v != "" {
		where = append(where, "a.action = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(r.URL.Query().Get("result")); v != "" {
		where = append(where, "a.result = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(r.URL.Query().Get("target_type")); v != "" {
		where = append(where, "a.target_type = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(r.URL.Query().Get("request_id")); v != "" {
		// The id an operator quotes from a failure screen, which is the whole
		// reason WriteError returns it.
		where = append(where, "a.request_id = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(r.URL.Query().Get("actor")); v != "" {
		where = append(where, "(ad.username = ? OR a.actor_label = ?)")
		args = append(args, v, v)
	}
	args = append(args, limit)

	rows, err := d.Store.Read().QueryContext(r.Context(),
		`SELECT a.id, a.at, a.actor_type, COALESCE(ad.username,''), a.actor_label,
		        a.actor_ip, a.request_id, a.action, a.target_type,
		        COALESCE(a.target_id,0), a.result, a.before_json, a.after_json
		   FROM audit_log a
		   LEFT JOIN admins ad ON ad.id = a.actor_admin_id
		  WHERE `+strings.Join(where, " AND ")+`
		  ORDER BY a.id DESC LIMIT ?`, args...)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not list audit entries")
		return
	}
	defer func() { _ = rows.Close() }()

	entries := []auditEntryDTO{}
	for rows.Next() {
		var e auditEntryDTO
		var before, after sql.NullString
		if err := rows.Scan(&e.ID, &e.At, &e.ActorType, &e.ActorName, &e.ActorLabel,
			&e.ActorIP, &e.RequestID, &e.Action, &e.TargetType, &e.TargetID, &e.Result,
			&before, &after); err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "could not read audit entries")
			return
		}
		// NULL stays absent rather than becoming "null": an action with no
		// recorded before-state is different from one whose before-state was
		// the JSON literal null.
		if before.Valid {
			e.Before = json.RawMessage(before.String)
		}
		if after.Valid {
			e.After = json.RawMessage(after.String)
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
