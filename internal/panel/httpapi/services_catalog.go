package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

type catalogServiceDTO struct {
	ID          int64           `json:"id"`
	NodeID      int64           `json:"node_id"`
	NodeName    string          `json:"node_name"`
	AdapterKind string          `json:"adapter_kind"`
	Params      json.RawMessage `json:"params"`
	Enabled     bool            `json:"enabled"`
}

// handleListAllServices is the inbound catalogue used when creating a user:
// pick which inbounds this person may use, across every node the caller can see.
func (d Deps) handleListAllServices(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermServiceRead, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	args := store.ScopeArgs(rbac.ScopeOf(actor))
	rows, err := d.Store.Read().QueryContext(r.Context(),
		`SELECT s.id, s.node_id, nodes.name, s.adapter_kind, s.params, s.enabled
		   FROM services s JOIN nodes ON nodes.id = s.node_id
		  WHERE `+store.NodeScopeSQL+`
		  ORDER BY nodes.name, s.id`, args...)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not list services")
		return
	}
	defer func() { _ = rows.Close() }()
	out := make([]catalogServiceDTO, 0)
	for rows.Next() {
		var (
			dto     catalogServiceDTO
			params  string
			enabled int
		)
		if err := rows.Scan(&dto.ID, &dto.NodeID, &dto.NodeName, &dto.AdapterKind, &params, &enabled); err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "could not read services")
			return
		}
		if params == "" {
			params = "{}"
		}
		dto.Params = json.RawMessage(params)
		dto.Enabled = enabled == 1
		out = append(out, dto)
	}
	if err := rows.Err(); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not read services")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"services": out})
}
