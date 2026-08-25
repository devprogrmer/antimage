package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/go-chi/chi/v5"

	"github.com/amyrm/antimage/internal/panel/nodes"
)

// NodeMetricsJSON represents node metrics in API responses.
type NodeMetricsJSON struct {
	ReconnectCount          int    `json:"reconnect_count"`
	LastReconcileDurationMs *int64 `json:"last_reconcile_duration_ms"`
	FailedReconcileStreak   int    `json:"failed_reconcile_streak"`
	AvgRTTMs                *int64 `json:"avg_rtt_ms"` // Average of last 10 samples
}

// handleNodeMetrics implements GET /api/v1/nodes/{nodeID}/metrics.
func (d Deps) handleNodeMetrics(w http.ResponseWriter, r *http.Request) {
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

	metrics, err := nodes.GetMetrics(ctx, d.Store, id)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	response := NodeMetricsJSON{
		ReconnectCount:          metrics.ReconnectCount,
		LastReconcileDurationMs: metrics.LastReconcileDurationMs,
		FailedReconcileStreak:   metrics.FailedReconcileStreak,
		AvgRTTMs:                metrics.AvgRTTMs,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}
