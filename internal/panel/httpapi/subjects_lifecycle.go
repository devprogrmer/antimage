package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/go-chi/chi/v5"
)

// handleFreezeSubject freezes a subject (quota enforcement, violations)
// POST /api/v1/subjects/:id/freeze
func (d Deps) handleFreezeSubject(w http.ResponseWriter, r *http.Request) {
	actor := ActorFrom(r.Context())

	subjectID, err := strconv.ParseInt(chi.URLParam(r, "subjectID"), 10, 64)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid subject ID")
		return
	}

	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid request body")
		return
	}

	if req.Reason == "" {
		req.Reason = "frozen by administrator"
	}

	// Check authorization BEFORE entering transaction to allow BestEffort audit logging
	if !d.requirePermission(w, r, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	// Second layer. PermSubjectWrite says this actor may change customers, not
	// that they may change ANY customer. Without this a reseller could freeze
	// or disable a competitor's users -- denial of service against another
	// tenant, from an endpoint their own role legitimately needs.
	if !d.requireSubjectInScope(w, r, ActorFrom(r.Context()), subjectID) {
		return
	}

	if err := d.subjectService().SetFrozen(
		r.Context(), d.svcActor(r, actor), subjectID, true, req.Reason); err != nil {
		d.writeServiceError(w, r, actor, "subject.freeze", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUnfreezeSubject unfreezes a subject
// POST /api/v1/subjects/:id/unfreeze
func (d Deps) handleUnfreezeSubject(w http.ResponseWriter, r *http.Request) {
	actor := ActorFrom(r.Context())

	subjectID, err := strconv.ParseInt(chi.URLParam(r, "subjectID"), 10, 64)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid subject ID")
		return
	}

	// Check authorization BEFORE entering transaction to allow BestEffort audit logging
	if !d.requirePermission(w, r, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	// Second layer. PermSubjectWrite says this actor may change customers, not
	// that they may change ANY customer. Without this a reseller could freeze
	// or disable a competitor's users -- denial of service against another
	// tenant, from an endpoint their own role legitimately needs.
	if !d.requireSubjectInScope(w, r, ActorFrom(r.Context()), subjectID) {
		return
	}

	if err := d.subjectService().SetFrozen(
		r.Context(), d.svcActor(r, actor), subjectID, false, ""); err != nil {
		d.writeServiceError(w, r, actor, "subject.unfreeze", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDisableSubject disables a subject (manual admin action)
// POST /api/v1/subjects/:id/disable
func (d Deps) handleDisableSubject(w http.ResponseWriter, r *http.Request) {
	actor := ActorFrom(r.Context())

	subjectID, err := strconv.ParseInt(chi.URLParam(r, "subjectID"), 10, 64)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid subject ID")
		return
	}

	// Check authorization BEFORE entering transaction to allow BestEffort audit logging
	if !d.requirePermission(w, r, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	// Second layer. PermSubjectWrite says this actor may change customers, not
	// that they may change ANY customer. Without this a reseller could freeze
	// or disable a competitor's users -- denial of service against another
	// tenant, from an endpoint their own role legitimately needs.
	if !d.requireSubjectInScope(w, r, ActorFrom(r.Context()), subjectID) {
		return
	}

	// Through the service: this path previously wrote no audit record at all,
	// so a disable left no trace of who did it.
	if err := d.subjectService().SetEnabled(
		r.Context(), d.svcActor(r, actor), subjectID, false); err != nil {
		d.writeServiceError(w, r, actor, "subject.disable", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleEnableSubject enables a subject
// POST /api/v1/subjects/:id/enable
func (d Deps) handleEnableSubject(w http.ResponseWriter, r *http.Request) {
	actor := ActorFrom(r.Context())

	subjectID, err := strconv.ParseInt(chi.URLParam(r, "subjectID"), 10, 64)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid subject ID")
		return
	}

	// Check authorization BEFORE entering transaction to allow BestEffort audit logging
	if !d.requirePermission(w, r, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	// Second layer. PermSubjectWrite says this actor may change customers, not
	// that they may change ANY customer. Without this a reseller could freeze
	// or disable a competitor's users -- denial of service against another
	// tenant, from an endpoint their own role legitimately needs.
	if !d.requireSubjectInScope(w, r, ActorFrom(r.Context()), subjectID) {
		return
	}

	if err := d.subjectService().SetEnabled(
		r.Context(), d.svcActor(r, actor), subjectID, true); err != nil {
		d.writeServiceError(w, r, actor, "subject.enable", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
