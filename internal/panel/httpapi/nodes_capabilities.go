package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/go-chi/chi/v5"
)

// GET /api/v1/nodes/:id/capabilities
func (d Deps) handleGetNodeCapabilities(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract node ID
	nodeIDStr := chi.URLParam(r, "nodeID")
	nodeID, err := strconv.ParseInt(nodeIDStr, 10, 64)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node id")
		return
	}

	// Authorization. The comment here used to read "handled by middleware",
	// and it was not: the private group installs auth, read-only and rate-limit
	// middleware and nothing that checks a permission. This endpoint returned
	// 200 with the node's name and capability set to any authenticated caller,
	// including a reseller scoped to no node at all.
	//
	// TargetNode is what makes the scope bind. rbac.Check treats a non-super
	// actor's NodeIDs as an exhaustive allow-list, so a node the caller does
	// not hold is refused even when they carry node:read.
	if !d.requirePermission(w, r, rbac.PermNodeRead,
		rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	// Verify node exists
	var nodeName string
	err = d.Store.Read().QueryRowContext(ctx, `SELECT name FROM nodes WHERE id = ?`, nodeID).Scan(&nodeName)
	if err != nil {
		WriteError(w, http.StatusNotFound, "not_found", "node not found")
		return
	}

	// Get all capabilities
	capabilities, err := nodes.GetNodeCapabilities(ctx, d.Store, nodeID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "failed to retrieve capabilities")
		return
	}

	// Transform to JSON-friendly format
	results := make([]map[string]interface{}, 0, len(capabilities))
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
	if err := json.NewEncoder(w).Encode(response); err != nil {
		slog.ErrorContext(r.Context(), "encode response", "error", err)
	}
}
