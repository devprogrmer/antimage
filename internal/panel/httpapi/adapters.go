package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/go-chi/chi/v5"

	"github.com/amyrm/antimage/internal/panel/nodes"
)

// AdapterJSON represents an adapter in API responses.
type AdapterJSON struct {
	Kind         string   `json:"kind"`
	Version      string   `json:"version"`
	Capabilities []string `json:"capabilities"`
	ReportedAt   int64    `json:"reported_at"`
}

// handleListAdapters implements GET /api/v1/nodes/{id}/adapters.
func (d Deps) handleListAdapters(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	nodeID := chi.URLParam(r, "nodeID")

	var id int64
	if _, err := fmt.Sscanf(nodeID, "%d", &id); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node id")
		return
	}

	// Authorization was absent entirely: any authenticated caller, including a
	// reseller scoped to no node, could read this. TargetNode binds the scope --
	// a non-super actor's NodeIDs are an exhaustive allow-list.
	if !d.requirePermission(w, r, rbac.PermNodeRead,
		rbac.Target{Kind: rbac.TargetNode, ID: id}) {
		return
	}

	entries, err := nodes.ListAdapters(ctx, d.Store, id)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	adapters := make([]AdapterJSON, 0, len(entries))
	for _, e := range entries {
		adapters = append(adapters, AdapterJSON{
			Kind:         e.Kind,
			Version:      e.Version,
			Capabilities: e.Capabilities,
			ReportedAt:   e.ReportedAt.Unix(),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string][]AdapterJSON{
		"adapters": adapters,
	})
}
