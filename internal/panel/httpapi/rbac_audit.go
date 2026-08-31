package httpapi

import (
	"database/sql"
	"net/http"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

// targetKindString converts TargetKind to string for audit logging.
func targetKindString(kind rbac.TargetKind) string {
	switch kind {
	case rbac.TargetNone:
		return "none"
	case rbac.TargetNode:
		return "node"
	case rbac.TargetService:
		return "service"
	default:
		return "unknown"
	}
}

// auditRBAC logs authorization checks for audit trail.
// Records both successful and failed permission checks with context.
func (d Deps) auditRBAC(w http.ResponseWriter, r *http.Request, actor *rbac.Actor, perm rbac.Permission, target rbac.Target, result string) {
	ctx := r.Context()

	var targetID sql.NullInt64
	if target.ID != 0 {
		targetID = sql.NullInt64{Int64: target.ID, Valid: true}
	}

	// A nil actor is reachable and must not panic here.
	//
	// rbac.Check already treats nil as forbidden, so this runs on exactly the
	// path where a request reached a permission check without an actor. A panic
	// there is worse than the denial it replaces: recoverMiddleware turns it
	// into a 500, so a refusal is reported as a server fault and the record of
	// the denial -- the one event invariant 9 says must not be lost -- is never
	// written.
	//
	// The handling mirrors authorize(), which has resolved this the same way
	// since it was written: attribute the record to the system rather than to
	// an admin, because audit's actor_type 'admin' requires an admin id and
	// there is no admin here. actorAudit keeps its contract of taking a
	// non-nil actor, which is what its own comment promises callers.
	role, isSuper := "", false
	who := audit.Actor{Type: audit.ActorSystem, Label: "authz", IP: clientIP(r)}
	if actor != nil {
		role, isSuper = actor.RoleName, actor.IsSuper
		who = d.actorAudit(actor, r)
	}
	metadata := map[string]any{
		"permission":  string(perm),
		"target_kind": targetKindString(target.Kind),
		"actor_role":  role,
		"is_super":    isSuper,
		"method":      r.Method,
		"path":        r.URL.Path,
	}

	if target.ID != 0 {
		metadata["target_id"] = target.ID
	}

	record := audit.Record{
		Action:     "rbac_check",
		TargetType: "authorization",
		TargetID:   targetID,
		After:      metadata,
		Result:     result,
	}

	audit.BestEffort(ctx, d.Store, RequestID(ctx), who, record)
}

// auditRBACDenied logs a failed authorization check.
func (d Deps) auditRBACDenied(w http.ResponseWriter, r *http.Request, actor *rbac.Actor, perm rbac.Permission, target rbac.Target) {
	d.auditRBAC(w, r, actor, perm, target, "denied")
}

// auditRBACGranted logs a successful authorization check.
// Only called for sensitive operations that need full audit trail.

// requirePermission checks RBAC and audits denials.
// Returns true if authorized, false if denied (already logged and responded).
func (d Deps) requirePermission(w http.ResponseWriter, r *http.Request, perm rbac.Permission, target rbac.Target) bool {
	actor := ActorFrom(r.Context())

	if err := rbac.Check(actor, perm, target); err != nil {
		d.auditRBACDenied(w, r, actor, perm, target)
		WriteError(w, http.StatusForbidden, "forbidden", "insufficient permissions")
		return false
	}

	return true
}

// requirePermissionAuditGrant checks RBAC and audits both denials and grants.
// Use for sensitive operations that need full audit trail of all access.
