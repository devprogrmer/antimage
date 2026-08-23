package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

// POST /api/v1/nodes/:id/restart
func (d Deps) handleRestartNode(w http.ResponseWriter, r *http.Request) {
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

	if !d.authorize(w, r, actor, rbac.PermNodeWrite, rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	// Verify node exists
	var nodeName, status string
	err = d.Store.Read().QueryRowContext(ctx,
		`SELECT name, status FROM nodes WHERE id = ?`, nodeID).Scan(&nodeName, &status)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "not_found", "node not found")
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not load node")
		return
	}

	// Record restart request event
	details := map[string]interface{}{
		"action":      "restart",
		"admin_id":    actor.AdminID,
		"node_name":   nodeName,
		"node_status": status,
	}

	if err := nodes.RecordNodeEvent(ctx, d.Store, nodeID, "restart_requested", "info", details, &actor.AdminID); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "failed to record event")
		return
	}

	// Audit log
	if err := d.Store.Write(ctx, func(tx *sql.Tx) error {
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action:     "node.restart",
			TargetType: "node",
			TargetID:   sql.NullInt64{Int64: nodeID, Valid: true},
			Result:     "ok",
			After:      map[string]any{"node": nodeName, "status": status},
		})
	}); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "audit failed")
		return
	}

	response := map[string]interface{}{
		"node_id":   nodeID,
		"node_name": nodeName,
		"action":    "restart",
		"status":    "requested",
		"message":   "restart request recorded, node will restart on next heartbeat",
	}

	WriteJSON(w, http.StatusOK, response)
}

// POST /api/v1/nodes/:id/sync
func (d Deps) handleSyncNode(w http.ResponseWriter, r *http.Request) {
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

	if !d.authorize(w, r, actor, rbac.PermNodeWrite, rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	// Verify node exists
	var nodeName string
	err = d.Store.Read().QueryRowContext(ctx,
		`SELECT name FROM nodes WHERE id = ?`, nodeID).Scan(&nodeName)
	if err == sql.ErrNoRows {
		WriteError(w, http.StatusNotFound, "not_found", "node not found")
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not load node")
		return
	}

	// Record sync request event
	details := map[string]interface{}{
		"action":    "sync",
		"admin_id":  actor.AdminID,
		"node_name": nodeName,
	}

	if err := nodes.RecordNodeEvent(ctx, d.Store, nodeID, "sync_requested", "info", details, &actor.AdminID); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "failed to record event")
		return
	}

	// Audit log
	if err := d.Store.Write(ctx, func(tx *sql.Tx) error {
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action:     "node.sync",
			TargetType: "node",
			TargetID:   sql.NullInt64{Int64: nodeID, Valid: true},
			Result:     "ok",
			After:      map[string]any{"node": nodeName},
		})
	}); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "audit failed")
		return
	}

	response := map[string]interface{}{
		"node_id":   nodeID,
		"node_name": nodeName,
		"action":    "sync",
		"status":    "requested",
		"message":   "sync request recorded, node will apply latest configuration",
	}

	WriteJSON(w, http.StatusOK, response)
}

// POST /api/v1/nodes/:id/maintenance
func (d Deps) handleSetNodeMaintenance(w http.ResponseWriter, r *http.Request) {
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

	// Parse request body
	var req struct {
		Enable bool   `json:"enable"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}

	if !d.authorize(w, r, actor, rbac.PermNodeWrite, rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
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
			if err != nil {
				return err
			}
		} else {
			_, err := tx.ExecContext(ctx, `
				UPDATE nodes
				SET maintenance_mode = 0,
				    maintenance_reason = NULL,
				    maintenance_entered_at = NULL,
				    status = 'online'
				WHERE id = ?
			`, nodeID)
			if err != nil {
				return err
			}
		}

		// Audit log
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action:     "node.maintenance",
			TargetType: "node",
			TargetID:   sql.NullInt64{Int64: nodeID, Valid: true},
			Result:     "ok",
			After:      map[string]any{"enable": req.Enable, "reason": req.Reason},
		})
	})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "failed to update maintenance mode")
		return
	}

	// Record event
	eventType := "maintenance_exit"
	if req.Enable {
		eventType = "maintenance_enter"
	}

	details := map[string]interface{}{
		"action":   eventType,
		"reason":   req.Reason,
		"admin_id": actor.AdminID,
	}

	if err := nodes.RecordNodeEvent(ctx, d.Store, nodeID, eventType, "info", details, &actor.AdminID); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "failed to record event")
		return
	}

	response := map[string]interface{}{
		"node_id":          nodeID,
		"maintenance_mode": req.Enable,
		"status":           "updated",
	}

	WriteJSON(w, http.StatusOK, response)
}

// POST /api/v1/nodes/:id/enable
func (d Deps) handleEnableNode(w http.ResponseWriter, r *http.Request) {
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

	if !d.authorize(w, r, actor, rbac.PermNodeWrite, rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	// Update node status from disabled to pending
	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE nodes
			SET status = 'pending'
			WHERE id = ? AND status = 'disabled'
		`, nodeID)
		if err != nil {
			return err
		}

		rows, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if rows == 0 {
			return sql.ErrNoRows
		}

		// Audit log
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action:     "node.enable",
			TargetType: "node",
			TargetID:   sql.NullInt64{Int64: nodeID, Valid: true},
			Result:     "ok",
		})
	})
	if err == sql.ErrNoRows {
		WriteError(w, http.StatusConflict, "conflict", "node not found or not disabled")
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "failed to enable node")
		return
	}

	// Record event
	details := map[string]interface{}{
		"action":   "enable",
		"admin_id": actor.AdminID,
	}

	if err := nodes.RecordNodeEvent(ctx, d.Store, nodeID, "node_enabled", "info", details, &actor.AdminID); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "failed to record event")
		return
	}

	response := map[string]interface{}{
		"node_id": nodeID,
		"action":  "enable",
		"status":  "enabled",
		"message": "node enabled, will reconnect on next heartbeat",
	}

	WriteJSON(w, http.StatusOK, response)
}

// POST /api/v1/nodes/:id/disable
func (d Deps) handleDisableNode(w http.ResponseWriter, r *http.Request) {
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

	// Parse request body
	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}

	if !d.authorize(w, r, actor, rbac.PermNodeWrite, rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	// Update node status to disabled
	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE nodes
			SET status = 'disabled'
			WHERE id = ?
		`, nodeID)
		if err != nil {
			return err
		}

		// Audit log
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action:     "node.disable",
			TargetType: "node",
			TargetID:   sql.NullInt64{Int64: nodeID, Valid: true},
			Result:     "ok",
			After:      map[string]any{"reason": req.Reason},
		})
	})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "failed to disable node")
		return
	}

	// Record event
	details := map[string]interface{}{
		"action":   "disable",
		"reason":   req.Reason,
		"admin_id": actor.AdminID,
	}

	if err := nodes.RecordNodeEvent(ctx, d.Store, nodeID, "node_disabled", "warning", details, &actor.AdminID); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "failed to record event")
		return
	}

	response := map[string]interface{}{
		"node_id": nodeID,
		"action":  "disable",
		"status":  "disabled",
		"message": "node disabled, will not accept new connections",
	}

	WriteJSON(w, http.StatusOK, response)
}
