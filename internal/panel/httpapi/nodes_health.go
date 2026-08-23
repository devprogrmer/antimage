package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

// GET /api/v1/nodes/:id/health/latest
func (d Deps) handleGetNodeHealthLatest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	actor, ok := requireActor(w, r)
	if !ok {
		return
	}

	nodeID, err := pathInt64(r, "nodeID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node ID")
		return
	}

	if !d.authorize(w, r, actor, rbac.PermNodeRead, rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	// Verify node exists and get last_seen_at
	var lastSeenUnix *int64
	err = d.Store.Read().QueryRowContext(ctx, `SELECT last_seen_at FROM nodes WHERE id = ?`, nodeID).Scan(&lastSeenUnix)
	if err == sql.ErrNoRows {
		WriteError(w, http.StatusNotFound, "not_found", "node not found")
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not load node")
		return
	}

	// Get latest metrics
	metrics, err := nodes.GetLatestMetrics(ctx, d.Store, nodeID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "failed to retrieve metrics")
		return
	}

	// Calculate health status
	var lastSeen *time.Time
	if lastSeenUnix != nil {
		t := time.Unix(*lastSeenUnix, 0)
		lastSeen = &t
	}

	healthStatus := nodes.CalculateHealthStatus(metrics, lastSeen, nodes.DefaultHealthThresholds())

	// Build response
	response := map[string]interface{}{
		"node_id": nodeID,
		"status":  healthStatus.Status,
		"message": healthStatus.Message,
		"healthy": map[string]bool{
			"cpu":     healthStatus.CPUHealthy,
			"memory":  healthStatus.MemoryHealthy,
			"disk":    healthStatus.DiskHealthy,
			"latency": healthStatus.LatencyHealthy,
		},
		"last_heartbeat": healthStatus.LastHeartbeat.Unix(),
	}

	if metrics != nil {
		response["metrics"] = map[string]interface{}{
			"timestamp":          metrics.Timestamp.Unix(),
			"cpu_percent":        metrics.CPUPercent,
			"memory_used_bytes":  metrics.MemoryUsedBytes,
			"memory_total_bytes": metrics.MemoryTotalBytes,
			"disk_used_bytes":    metrics.DiskUsedBytes,
			"disk_total_bytes":   metrics.DiskTotalBytes,
			"network_rx_bytes":   metrics.NetworkRxBytes,
			"network_tx_bytes":   metrics.NetworkTxBytes,
			"active_connections": metrics.ActiveConnections,
			"latency_ms":         metrics.LatencyMS,
		}
	}

	WriteJSON(w, http.StatusOK, response)
}

// GET /api/v1/nodes/:id/health/history?from=<unix>&to=<unix>&limit=<int>
func (d Deps) handleGetNodeHealthHistory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	actor, ok := requireActor(w, r)
	if !ok {
		return
	}

	nodeID, err := pathInt64(r, "nodeID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node ID")
		return
	}

	if !d.authorize(w, r, actor, rbac.PermNodeRead, rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	// Parse query parameters
	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")
	limitStr := r.URL.Query().Get("limit")

	from := time.Now().Add(-24 * time.Hour) // Default: last 24 hours
	if fromStr != "" {
		fromUnix, err := strconv.ParseInt(fromStr, 10, 64)
		if err != nil {
			http.Error(w, "invalid 'from' timestamp", http.StatusBadRequest)
			return
		}
		from = time.Unix(fromUnix, 0)
	}

	to := time.Now()
	if toStr != "" {
		toUnix, err := strconv.ParseInt(toStr, 10, 64)
		if err != nil {
			http.Error(w, "invalid 'to' timestamp", http.StatusBadRequest)
			return
		}
		to = time.Unix(toUnix, 0)
	}

	limit := 100 // Default limit
	if limitStr != "" {
		parsedLimit, err := strconv.Atoi(limitStr)
		if err != nil || parsedLimit <= 0 || parsedLimit > 1000 {
			http.Error(w, "invalid 'limit' (must be 1-1000)", http.StatusBadRequest)
			return
		}
		limit = parsedLimit
	}

	// Get historical metrics
	history, err := nodes.GetMetricsHistory(ctx, d.Store, nodeID, from, to, limit)
	if err != nil {
		http.Error(w, "failed to retrieve history", http.StatusInternalServerError)
		return
	}

	// Transform to JSON-friendly format
	var results []map[string]interface{}
	for _, m := range history {
		results = append(results, map[string]interface{}{
			"timestamp":          m.Timestamp.Unix(),
			"cpu_percent":        m.CPUPercent,
			"memory_used_bytes":  m.MemoryUsedBytes,
			"memory_total_bytes": m.MemoryTotalBytes,
			"disk_used_bytes":    m.DiskUsedBytes,
			"disk_total_bytes":   m.DiskTotalBytes,
			"network_rx_bytes":   m.NetworkRxBytes,
			"network_tx_bytes":   m.NetworkTxBytes,
			"active_connections": m.ActiveConnections,
			"latency_ms":         m.LatencyMS,
		})
	}

	response := map[string]interface{}{
		"node_id": nodeID,
		"from":    from.Unix(),
		"to":      to.Unix(),
		"count":   len(results),
		"metrics": results,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}
