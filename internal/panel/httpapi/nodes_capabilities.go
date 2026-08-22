package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/go-chi/chi/v5"
)

// GET /api/v1/nodes/:id/capabilities
func (d Deps) handleGetNodeCapabilities(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract node ID
	nodeIDStr := chi.URLParam(r, "nodeID")
	nodeID, err := strconv.ParseInt(nodeIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid node ID", http.StatusBadRequest)
		return
	}

	// Authorization: nodes:read permission (handled by middleware)

	// Verify node exists
	var nodeName string
	err = d.Store.Read().QueryRowContext(ctx, `SELECT name FROM nodes WHERE id = ?`, nodeID).Scan(&nodeName)
	if err != nil {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}

	// Get all capabilities
	capabilities, err := nodes.GetNodeCapabilities(ctx, d.Store, nodeID)
	if err != nil {
		http.Error(w, "failed to retrieve capabilities", http.StatusInternalServerError)
		return
	}

	// Transform to JSON-friendly format
	var results []map[string]interface{}
	for _, cap := range capabilities {
		result := map[string]interface{}{
			"protocol":      cap.Protocol,
			"available":     cap.Available,
			"detected_at":   cap.DetectedAt.Unix(),
			"last_check_at": cap.LastCheckAt.Unix(),
		}
		if cap.Version != nil {
			result["version"] = *cap.Version
		}
		results = append(results, result)
	}

	response := map[string]interface{}{
		"node_id":      nodeID,
		"node_name":    nodeName,
		"capabilities": results,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
