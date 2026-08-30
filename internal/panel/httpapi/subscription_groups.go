package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/subscriptions"
)

// Subscription groups: a named selection of protocols that decides what a
// customer's subscription actually contains.
//
// Gated on subject permissions rather than a new one of their own. A group is
// a property of what a customer is sold; anyone who may change that already
// holds subject:write, and adding a parallel permission would mean a role that
// can edit customers but not the tiers they sit on -- a distinction nobody
// asked for and one more thing to get wrong.

type groupRequest struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Protocols   []string `json:"protocols"`
	IsPublic    bool     `json:"is_public"`
}

func (d Deps) handleListSubscriptionGroups(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSubjectRead, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	groups, err := subscriptions.ListGroups(r.Context(), d.Store, *actor)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not list groups")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"groups": groups,
		// The protocols a group may select, so the form offers exactly what
		// the panel can produce rather than a list the UI keeps in step by
		// hand.
		"available_protocols": subscriptions.KnownProtocols(),
	})
}

func (d Deps) handleCreateSubscriptionGroup(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	var req groupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	group, err := subscriptions.CreateGroup(r.Context(), d.Store, *actor,
		subscriptions.GroupInput{
			Name: req.Name, Description: req.Description,
			Protocols: req.Protocols, IsPublic: req.IsPublic,
		}, d.now())
	if err != nil {
		d.writeGroupError(w, err)
		return
	}
	WriteJSON(w, http.StatusCreated, group)
}

func (d Deps) handleUpdateSubscriptionGroup(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	id, err := pathInt64(r, "groupID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid group id")
		return
	}
	var req groupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	group, err := subscriptions.UpdateGroup(r.Context(), d.Store, *actor, id,
		subscriptions.GroupInput{
			Name: req.Name, Description: req.Description,
			Protocols: req.Protocols, IsPublic: req.IsPublic,
		}, d.now())
	if err != nil {
		d.writeGroupError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, group)
}

func (d Deps) handleDeleteSubscriptionGroup(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	id, err := pathInt64(r, "groupID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid group id")
		return
	}
	if err := subscriptions.DeleteGroup(r.Context(), d.Store, *actor, id); err != nil {
		d.writeGroupError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAssignSubscriptionGroup puts a subject on a group, or takes them off.
//
// PUT /api/v1/subjects/{subjectID}/subscription-group
// Body: {"group_id": 3} or {"group_id": null} to clear.
func (d Deps) handleAssignSubscriptionGroup(w http.ResponseWriter, r *http.Request) {
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

	var req struct {
		GroupID *int64 `json:"group_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}

	ctx := r.Context()
	// A group the caller cannot see is refused rather than assigned. Without
	// this an operator could put their customer on a competitor's private
	// tier by guessing an id, and would then inherit whatever that tier
	// carries when its owner changed it.
	if req.GroupID != nil {
		if _, err := subscriptions.GetGroup(ctx, d.Store, *actor, *req.GroupID); err != nil {
			d.writeGroupError(w, err)
			return
		}
	}

	var value any
	if req.GroupID != nil {
		value = *req.GroupID
	}
	if err := d.Store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE subjects SET subscription_group_id = ? WHERE id = ?`, value, subjectID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return sql.ErrNoRows
		}
		return nil
	}); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not assign group")
		return
	}

	// No node republish: a group changes what the PANEL hands out, not what
	// any node serves. The subject's entitlement is unchanged, so a revision
	// bump here would restart services for a change no node can observe.
	w.WriteHeader(http.StatusNoContent)
}

func (d Deps) writeGroupError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, subscriptions.ErrGroupNotFound):
		WriteError(w, http.StatusNotFound, "not_found", "subscription group not found")
	default:
		// Validation failures carry the reason, which names the unknown
		// protocol -- an operator who typed "quic" needs to be told that
		// rather than "invalid request".
		WriteError(w, http.StatusUnprocessableEntity, "validation", err.Error())
	}
}
