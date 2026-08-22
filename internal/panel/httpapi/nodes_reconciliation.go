package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// GET /api/v1/nodes/:id/reconciliation
func (d Deps) handleGetNodeReconciliation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract node ID
	nodeIDStr := chi.URLParam(r, "nodeID")
	nodeID, err := strconv.ParseInt(nodeIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid node ID", http.StatusBadRequest)
		return
	}

	// Authorization: nodes:read permission (handled by middleware)

	// Get node with revision data
	var (
		nodeName        string
		desiredRevision *int64
		appliedRevision *int64
		lastSyncAt      *int64
		lastSyncError   *string
		configDrift     *bool
	)

	err = d.Store.Read().QueryRowContext(ctx, `
		SELECT name, desired_revision, applied_revision, last_sync_at, last_sync_error, config_drift
		FROM nodes
		WHERE id = ?
	`, nodeID).Scan(&nodeName, &desiredRevision, &appliedRevision, &lastSyncAt, &lastSyncError, &configDrift)

	if err != nil {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}

	// Calculate reconciliation status
	status := "unknown"
	driftDetected := false
	needsSync := false

	if desiredRevision != nil && appliedRevision != nil {
		if *desiredRevision == *appliedRevision {
			status = "converged"
			driftDetected = configDrift != nil && *configDrift
		} else if *appliedRevision < *desiredRevision {
			status = "pending"
			needsSync = true
		} else {
			status = "drift" // applied > desired (shouldn't happen)
			driftDetected = true
		}
	} else if desiredRevision != nil {
		status = "pending"
		needsSync = true
	}

	// Get recent apply runs for this node
	rows, err := d.Store.Read().QueryContext(ctx, `
		SELECT id, revision, outcome, started_at, finished_at, error
		FROM apply_runs
		WHERE node_id = ?
		ORDER BY started_at DESC
		LIMIT 10
	`, nodeID)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var runs []map[string]interface{}
	for rows.Next() {
		var (
			id         int64
			revision   int64
			outcome    string
			startedAt  int64
			finishedAt *int64
			errMsg     *string
		)
		if err := rows.Scan(&id, &revision, &outcome, &startedAt, &finishedAt, &errMsg); err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}

		run := map[string]interface{}{
			"id":         id,
			"revision":   revision,
			"outcome":    outcome,
			"started_at": startedAt,
		}
		if finishedAt != nil {
			run["finished_at"] = *finishedAt
		}
		if errMsg != nil {
			run["error"] = *errMsg
		}
		runs = append(runs, run)
	}

	response := map[string]interface{}{
		"node_id":          nodeID,
		"node_name":        nodeName,
		"status":           status,
		"desired_revision": desiredRevision,
		"applied_revision": appliedRevision,
		"drift_detected":   driftDetected,
		"needs_sync":       needsSync,
		"recent_runs":      runs,
	}

	if lastSyncAt != nil {
		response["last_sync_at"] = *lastSyncAt
	}
	if lastSyncError != nil {
		response["last_sync_error"] = *lastSyncError
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
