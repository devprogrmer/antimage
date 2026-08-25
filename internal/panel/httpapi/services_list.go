package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/amyrm/antimage/internal/panel/rbac"
)

// serviceDTO is one inbound on a node.
type serviceDTO struct {
	ID          int64  `json:"id"`
	NodeID      int64  `json:"node_id"`
	AdapterKind string `json:"adapter_kind"`
	// Params is the adapter's own params document, verbatim. The panel stores
	// it opaquely and validates it against the adapter's schema; it has no
	// protocol knowledge of its own and must not acquire any by reshaping this.
	Params    json.RawMessage `json:"params"`
	Enabled   bool            `json:"enabled"`
	CreatedAt int64           `json:"created_at"`
}

// handleListServices serves GET /nodes/{nodeID}/services.
//
// Services could be created, updated and deleted but never READ, so nothing
// could show an operator what a node is already serving -- an editor cannot
// edit what it cannot list. Adding it here rather than folding services into
// the node detail response keeps the node summary about the node.
func (d Deps) handleListServices(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	nodeID, err := pathInt64(r, "nodeID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node id")
		return
	}
	// service:read rather than node:read, and scoped to the node: a service's
	// params can carry credentials an adapter chose to put there, so reading
	// them is a stronger right than seeing that a node exists.
	if !d.authorize(w, r, actor, rbac.PermServiceRead,
		rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	rows, err := d.Store.Read().QueryContext(r.Context(),
		`SELECT id, node_id, adapter_kind, params, enabled, created_at
		   FROM services WHERE node_id = ? ORDER BY id`, nodeID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not list services")
		return
	}
	defer func() { _ = rows.Close() }()

	out := make([]serviceDTO, 0)
	for rows.Next() {
		var (
			svc     serviceDTO
			params  string
			enabled int
		)
		if err := rows.Scan(&svc.ID, &svc.NodeID, &svc.AdapterKind,
			&params, &enabled, &svc.CreatedAt); err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "could not read services")
			return
		}
		if params == "" {
			params = "{}"
		}
		svc.Params = json.RawMessage(params)
		svc.Enabled = enabled == 1
		out = append(out, svc)
	}
	if err := rows.Err(); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not read services")
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{"services": out})
}
