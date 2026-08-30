package httpapi

import (
	"database/sql"
	"net/http"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/subjects"
)

// handleIssueSubscription creates or rotates a subject's subscription token.
//
// POST /api/v1/subjects/{subjectID}/subscription
//
// One route for issue and regenerate because they are the same operation from
// the operator's side: "give me a link that works, and stop the previous one
// if there was one". Rotating is the only way to withdraw a link that has been
// copied somewhere it should not be, so the request body says which was meant
// and the audit records it.
func (d Deps) handleIssueSubscription(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	// Issuing a subscription link hands out access to the subject's traffic,
	// so it is gated as a write on the subject rather than as a read.
	if !d.authorize(w, r, actor, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	subjectID, err := pathInt64(r, "subjectID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid subject id")
		return
	}
	if !d.requireSubjectInScope(w, r, actor, subjectID) {
		return
	}

	ctx := r.Context()
	existing, err := subjects.PeekToken(ctx, d.Store, subjectID)
	if err != nil {
		WriteError(w, http.StatusNotFound, "not_found", "subject not found")
		return
	}

	var token string
	action := "subscription.issue"
	if existing == "" {
		token, err = subjects.EnsureToken(ctx, d.Store, subjectID)
	} else {
		// Already had one: this is a regenerate, and the old link stops
		// working the moment this returns.
		action = "subscription.regenerate"
		token, err = subjects.RevokeToken(ctx, d.Store, subjectID)
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not issue subscription")
		return
	}

	// Audited by ACTION, never by value. The token is the credential; putting
	// it in the audit log would defeat rotating it.
	audit.BestEffort(ctx, d.Store, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
		Action: action, TargetType: "subject",
		TargetID: sql.NullInt64{Int64: subjectID, Valid: true},
		After:    map[string]any{"regenerated": existing != ""},
		Result:   "ok",
	})

	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, http.StatusOK, map[string]any{
		"subscription_url": "/subscribe/" + token,
		"regenerated":      existing != "",
	})
}

// handleRevokeSubscription withdraws a subject's subscription link entirely.
//
// DELETE /api/v1/subjects/{subjectID}/subscription
//
// Distinct from regenerating. RevokeToken rotates -- it always leaves a
// working link -- which covers "this leaked, give me a new one" and does NOT
// cover "this customer should have no link at all". Clearing the column is the
// only way to express the second, and without it an operator who wanted to cut
// off subscription access could only keep issuing new links forever.
func (d Deps) handleRevokeSubscription(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	subjectID, err := pathInt64(r, "subjectID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid subject id")
		return
	}
	if !d.requireSubjectInScope(w, r, actor, subjectID) {
		return
	}

	ctx := r.Context()
	if err := subjects.ClearToken(ctx, d.Store, subjectID); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not revoke subscription")
		return
	}

	audit.BestEffort(ctx, d.Store, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
		Action: "subscription.revoke", TargetType: "subject",
		TargetID: sql.NullInt64{Int64: subjectID, Valid: true},
		Result:   "ok",
	})

	w.WriteHeader(http.StatusNoContent)
}
