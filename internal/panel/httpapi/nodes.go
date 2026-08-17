package httpapi

import (
	"net/http"

	"github.com/amyrm/antimage/internal/panel/rbac"
)

// nodeSummary is the wire shape of a node row.
//
// It exists so the store's row struct never reaches the client verbatim:
// store.NodeRow carries sql.NullInt64, whose JSON encoding leaks the driver's
// representation, and a DTO keeps a future column from becoming an accidental
// part of the API.
type nodeSummary struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	Address         string `json:"address"`
	Status          string `json:"status"`
	DesiredRevision int64  `json:"desired_revision"`
	AppliedRevision int64  `json:"applied_revision"`
	LastSeenAt      *int64 `json:"last_seen_at"`
}

// handleListNodes returns the nodes visible to the caller.
//
// Both enforcement layers from spec section 6.3 apply: rbac.Check gates the
// permission here, and the store filters rows by scope, so a caller with
// node:read still sees only their own nodes.
func (d Deps) handleListNodes(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if err := rbac.Check(actor, rbac.PermNodeRead, rbac.Target{}); err != nil {
		WriteError(w, http.StatusForbidden, "forbidden", "permission denied")
		return
	}

	rows, err := d.Store.ListNodes(r.Context(), rbac.ScopeOf(actor))
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not list nodes")
		return
	}

	out := make([]nodeSummary, 0, len(rows))
	for _, n := range rows {
		summary := nodeSummary{
			ID:              n.ID,
			Name:            n.Name,
			Address:         n.Address,
			Status:          n.Status,
			DesiredRevision: n.DesiredRevision,
			AppliedRevision: n.AppliedRevision,
		}
		if n.LastSeenAt.Valid {
			seen := n.LastSeenAt.Int64
			summary.LastSeenAt = &seen
		}
		out = append(out, summary)
	}
	WriteJSON(w, http.StatusOK, map[string]any{"nodes": out})
}

// The handlers below are placeholders so the router in this task is complete
// and every route is reachable. Task 24 (nodes and services) and Task 25
// (audit, sessions, events) replace them with real implementations.
func notImplemented(w http.ResponseWriter, _ *http.Request) {
	WriteError(w, http.StatusNotImplemented, "not_implemented", "not implemented yet")
}

func (d Deps) handleCreateNode(w http.ResponseWriter, r *http.Request)       { notImplemented(w, r) }
func (d Deps) handleGetNode(w http.ResponseWriter, r *http.Request)          { notImplemented(w, r) }
func (d Deps) handleDeleteNode(w http.ResponseWriter, r *http.Request)       { notImplemented(w, r) }
func (d Deps) handleIssueEnrollToken(w http.ResponseWriter, r *http.Request) { notImplemented(w, r) }
func (d Deps) handleListRevisions(w http.ResponseWriter, r *http.Request)    { notImplemented(w, r) }
func (d Deps) handleListApplyRuns(w http.ResponseWriter, r *http.Request)    { notImplemented(w, r) }
func (d Deps) handleCreateService(w http.ResponseWriter, r *http.Request)    { notImplemented(w, r) }
func (d Deps) handleUpdateService(w http.ResponseWriter, r *http.Request)    { notImplemented(w, r) }
func (d Deps) handleDeleteService(w http.ResponseWriter, r *http.Request)    { notImplemented(w, r) }
func (d Deps) handleListAudit(w http.ResponseWriter, r *http.Request)        { notImplemented(w, r) }
func (d Deps) handleListSessions(w http.ResponseWriter, r *http.Request)     { notImplemented(w, r) }
func (d Deps) handleRevokeSession(w http.ResponseWriter, r *http.Request)    { notImplemented(w, r) }
func (d Deps) handleEvents(w http.ResponseWriter, r *http.Request)           { notImplemented(w, r) }
