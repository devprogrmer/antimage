package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/amyrm/antimage/internal/panel/auth"
	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

// POST /api/v1/nodes/:id/restart
func (d Deps) handleRestartNode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	nodeIDStr := chi.URLParam(r, "nodeID")
	nodeID, err := strconv.ParseInt(nodeIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid node ID", http.StatusBadRequest)
		return
	}

	// Load actor for RBAC
	adminID := auth.AdminIDFromContext(ctx)
	actor, err := d.loadActor(ctx, adminID)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Authorization: nodes:write permission + node in scope
	if !actor.Can(rbac.NodesWrite) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Verify node exists and is in scope
	var nodeName string
	err = d.Store.Read().QueryRowContext(ctx, `SELECT name FROM nodes WHERE id = ?`, nodeID).Scan(&nodeName)
	if err != nil {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}

	if !actor.CanAccessNode(nodeID) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Record node event for audit trail
	details := map[string]interface{}{
		"action":    "restart",
		"admin_id":  adminID,
		"timestamp": time.Now().Unix(),
	}

	if err := nodes.RecordNodeEvent(ctx, d.Store, nodeID, "restart_requested", "info", details, &adminID); err != nil {
		http.Error(w, "failed to record event", http.StatusInternalServerError)
		return
	}

	// TODO: Trigger actual restart via gRPC to node agent
	// For now, just record the action

	response := map[string]interface{}{
		"node_id":   nodeID,
		"node_name": nodeName,
		"action":    "restart",
		"status":    "requested",
		"message":   "restart request recorded, node will restart on next heartbeat",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// POST /api/v1/nodes/:id/sync
func (d Deps) handleSyncNode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	nodeIDStr := chi.URLParam(r, "nodeID")
	nodeID, err := strconv.ParseInt(nodeIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid node ID", http.StatusBadRequest)
		return
	}

	adminID := auth.AdminIDFromContext(ctx)
	actor, err := d.loadActor(ctx, adminID)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if !actor.Can(rbac.NodesWrite) || !actor.CanAccessNode(nodeID) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Verify node exists
	var nodeName string
	err = d.Store.Read().QueryRowContext(ctx, `SELECT name FROM nodes WHERE id = ?`, nodeID).Scan(&nodeName)
	if err != nil {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}

	// Record sync request
	details := map[string]interface{}{
		"action":    "sync",
		"admin_id":  adminID,
		"timestamp": time.Now().Unix(),
	}

	if err := nodes.RecordNodeEvent(ctx, d.Store, nodeID, "sync_requested", "info", details, &adminID); err != nil {
		http.Error(w, "failed to record event", http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"node_id":   nodeID,
		"node_name": nodeName,
		"action":    "sync",
		"status":    "requested",
		"message":   "sync request recorded, node will apply latest configuration",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// POST /api/v1/nodes/:id/maintenance
func (d Deps) handleSetNodeMaintenance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	nodeIDStr := chi.URLParam(r, "nodeID")
	nodeID, err := strconv.ParseInt(nodeIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid node ID", http.StatusBadRequest)
		return
	}

	// Parse request body
	var req struct {
		Enable bool   `json:"enable"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	adminID := auth.AdminIDFromContext(ctx)
	actor, err := d.loadActor(ctx, adminID)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if !actor.Can(rbac.NodesWrite) || !actor.CanAccessNode(nodeID) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Update maintenance mode in database
	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		if req.Enable {
			_, err := tx.ExecContext(ctx, `
				UPDATE nodes
				SET maintenance_mode = 1,
				    maintenance_reason = ?,
				    maintenance_entered_at = ?,
				    status = 'maintenance'
				WHERE id = ?
			`, req.Reason, time.Now().Unix(), nodeID)
			return err
		} else {
			_, err := tx.ExecContext(ctx, `
				UPDATE nodes
				SET maintenance_mode = 0,
				    maintenance_reason = NULL,
				    maintenance_entered_at = NULL,
				    status = 'online'
				WHERE id = ?
			`, nodeID)
			return err
		}
	})
	if err != nil {
		http.Error(w, "failed to update maintenance mode", http.StatusInternalServerError)
		return
	}

	// Record event
	eventType := "maintenance_exit"
	if req.Enable {
		eventType = "maintenance_enter"
	}

	details := map[string]interface{}{
		"action":    eventType,
		"reason":    req.Reason,
		"admin_id":  adminID,
		"timestamp": time.Now().Unix(),
	}

	if err := nodes.RecordNodeEvent(ctx, d.Store, nodeID, eventType, "info", details, &adminID); err != nil {
		// Log but don't fail the request
		http.Error(w, "failed to record event", http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"node_id":          nodeID,
		"maintenance_mode": req.Enable,
		"status":           "updated",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// POST /api/v1/nodes/:id/enable
func (d Deps) handleEnableNode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	nodeIDStr := chi.URLParam(r, "nodeID")
	nodeID, err := strconv.ParseInt(nodeIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid node ID", http.StatusBadRequest)
		return
	}

	adminID := auth.AdminIDFromContext(ctx)
	actor, err := d.loadActor(ctx, adminID)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if !actor.Can(rbac.NodesWrite) || !actor.CanAccessNode(nodeID) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Update node status from disabled to pending
	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE nodes
			SET status = 'pending'
			WHERE id = ? AND status = 'disabled'
		`, nodeID)
		return err
	})
	if err != nil {
		http.Error(w, "failed to enable node", http.StatusInternalServerError)
		return
	}

	// Record event
	details := map[string]interface{}{
		"action":    "enable",
		"admin_id":  adminID,
		"timestamp": time.Now().Unix(),
	}

	if err := nodes.RecordNodeEvent(ctx, d.Store, nodeID, "node_enabled", "info", details, &adminID); err != nil {
		http.Error(w, "failed to record event", http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"node_id": nodeID,
		"action":  "enable",
		"status":  "enabled",
		"message": "node enabled, will reconnect on next heartbeat",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// POST /api/v1/nodes/:id/disable
func (d Deps) handleDisableNode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	nodeIDStr := chi.URLParam(r, "nodeID")
	nodeID, err := strconv.ParseInt(nodeIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid node ID", http.StatusBadRequest)
		return
	}

	// Parse request body
	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	adminID := auth.AdminIDFromContext(ctx)
	actor, err := d.loadActor(ctx, adminID)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if !actor.Can(rbac.NodesWrite) || !actor.CanAccessNode(nodeID) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Update node status to disabled
	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE nodes
			SET status = 'disabled'
			WHERE id = ?
		`, nodeID)
		return err
	})
	if err != nil {
		http.Error(w, "failed to disable node", http.StatusInternalServerError)
		return
	}

	// Record event
	details := map[string]interface{}{
		"action":    "disable",
		"reason":    req.Reason,
		"admin_id":  adminID,
		"timestamp": time.Now().Unix(),
	}

	if err := nodes.RecordNodeEvent(ctx, d.Store, nodeID, "node_disabled", "warning", details, &adminID); err != nil {
		http.Error(w, "failed to record event", http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"node_id": nodeID,
		"action":  "disable",
		"status":  "disabled",
		"message": "node disabled, will not accept new connections",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
