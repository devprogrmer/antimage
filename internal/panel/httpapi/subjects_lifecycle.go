package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/subjects"
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

	store := subjects.NewStore(d.Store, d.Box, d.Now)
	ctx := r.Context()

	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		return store.Freeze(ctx, tx, subjectID, req.Reason)
	})

	if err != nil {
		if err == sql.ErrNoRows {
			WriteError(w, http.StatusNotFound, "not_found", "subject not found")
			return
		}
		WriteError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}

	// Republish affected nodes
	if err := d.republishSubject(ctx, r, actor, subjectID, "subject frozen"); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "failed to republish nodes")
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

	store := subjects.NewStore(d.Store, d.Box, d.Now)
	ctx := r.Context()

	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		return store.Unfreeze(ctx, tx, subjectID)
	})

	if err != nil {
		if err == sql.ErrNoRows {
			WriteError(w, http.StatusNotFound, "not_found", "subject not found")
			return
		}
		WriteError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}

	// Republish affected nodes
	if err := d.republishSubject(ctx, r, actor, subjectID, "subject unfrozen"); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "failed to republish nodes")
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

	store := subjects.NewStore(d.Store, d.Box, d.Now)
	ctx := r.Context()

	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		return store.Disable(ctx, tx, subjectID)
	})

	if err != nil {
		if err == sql.ErrNoRows {
			WriteError(w, http.StatusNotFound, "not_found", "subject not found")
			return
		}
		WriteError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}

	// Republish affected nodes
	if err := d.republishSubject(ctx, r, actor, subjectID, "subject disabled"); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "failed to republish nodes")
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

	store := subjects.NewStore(d.Store, d.Box, d.Now)
	ctx := r.Context()

	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		return store.Enable(ctx, tx, subjectID)
	})

	if err != nil {
		if err == sql.ErrNoRows {
			WriteError(w, http.StatusNotFound, "not_found", "subject not found")
			return
		}
		WriteError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}

	// Republish affected nodes
	if err := d.republishSubject(ctx, r, actor, subjectID, "subject enabled"); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "failed to republish nodes")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
