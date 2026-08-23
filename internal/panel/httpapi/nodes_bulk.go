package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

// BulkNodeAction represents a bulk operation request
type BulkNodeActionRequest struct {
	NodeIDs []int64 `json:"node_ids"`
	Action  string  `json:"action"` // "restart", "sync", "enable", "disable", "maintenance"
	// Maintenance-specific fields
	MaintenanceEnable bool   `json:"maintenance_enable,omitempty"`
	MaintenanceReason string `json:"maintenance_reason,omitempty"`
	// Disable-specific fields
	DisableReason string `json:"disable_reason,omitempty"`
}

// BulkNodeActionResponse represents the result of a bulk operation
type BulkNodeActionResponse struct {
	TotalNodes   int                    `json:"total_nodes"`
	SuccessCount int                    `json:"success_count"`
	FailureCount int                    `json:"failure_count"`
	Results      []BulkNodeActionResult `json:"results"`
}

// BulkNodeActionResult represents the result for a single node
type BulkNodeActionResult struct {
	NodeID   int64  `json:"node_id"`
	NodeName string `json:"node_name,omitempty"`
	Success  bool   `json:"success"`
	Error    string `json:"error,omitempty"`
}

// POST /api/v1/nodes/bulk/action
func (d Deps) handleBulkNodeAction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	actor, ok := requireActor(w, r)
	if !ok {
		return
	}

	// Parse request
	var req BulkNodeActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}

	// Validate action
	validActions := map[string]bool{
		"restart":     true,
		"sync":        true,
		"enable":      true,
		"disable":     true,
		"maintenance": true,
	}
	if !validActions[req.Action] {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid action")
		return
	}

	// Validate node IDs
	if len(req.NodeIDs) == 0 {
		WriteError(w, http.StatusBadRequest, "bad_request", "node_ids cannot be empty")
		return
	}

	if len(req.NodeIDs) > 100 {
		WriteError(w, http.StatusBadRequest, "bad_request", "maximum 100 nodes per bulk operation")
		return
	}

	// Process each node with authorization checks
	response := BulkNodeActionResponse{
		TotalNodes: len(req.NodeIDs),
		Results:    make([]BulkNodeActionResult, 0, len(req.NodeIDs)),
	}

	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, nodeID := range req.NodeIDs {
		wg.Add(1)
		go func(nID int64) {
			defer wg.Done()

			result := BulkNodeActionResult{NodeID: nID}

			// Check authorization for this specific node
			if err := rbac.Check(actor, rbac.PermNodeWrite, rbac.Target{Kind: rbac.TargetNode, ID: nID}); err != nil {
				result.Success = false
				result.Error = "unauthorized"

				mu.Lock()
				response.Results = append(response.Results, result)
				response.FailureCount++
				mu.Unlock()

				// Log authorization denial
				audit.BestEffort(ctx, d.Store, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
					Action:     "authz.deny",
					TargetType: "node",
					TargetID:   sql.NullInt64{Int64: nID, Valid: true},
					Result:     "denied",
					After: map[string]any{
						"permission":  string(rbac.PermNodeWrite),
						"bulk_action": req.Action,
					},
				})
				return
			}

			// Get node name
			var nodeName string
			err := d.Store.Read().QueryRowContext(ctx, `SELECT name FROM nodes WHERE id = ?`, nID).Scan(&nodeName)
			if err != nil {
				result.Success = false
				if err == sql.ErrNoRows {
					result.Error = "node not found"
				} else {
					result.Error = "database error"
				}

				mu.Lock()
				response.Results = append(response.Results, result)
				response.FailureCount++
				mu.Unlock()
				return
			}

			result.NodeName = nodeName

			// Execute action
			var actionErr error
			switch req.Action {
			case "restart":
				actionErr = d.executeBulkRestart(ctx, actor, nID, nodeName)
			case "sync":
				actionErr = d.executeBulkSync(ctx, actor, nID, nodeName)
			case "enable":
				actionErr = d.executeBulkEnable(ctx, actor, nID)
			case "disable":
				actionErr = d.executeBulkDisable(ctx, actor, nID, req.DisableReason)
			case "maintenance":
				actionErr = d.executeBulkMaintenance(ctx, actor, nID, req.MaintenanceEnable, req.MaintenanceReason)
			}

			if actionErr != nil {
				result.Success = false
				result.Error = actionErr.Error()

				mu.Lock()
				response.Results = append(response.Results, result)
				response.FailureCount++
				mu.Unlock()
				return
			}

			result.Success = true
			mu.Lock()
			response.Results = append(response.Results, result)
			response.SuccessCount++
			mu.Unlock()
		}(nodeID)
	}

	wg.Wait()

	// Log bulk operation summary
	audit.BestEffort(ctx, d.Store, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
		Action:     "nodes.bulk." + req.Action,
		TargetType: "node",
		Result:     "ok",
		After: map[string]any{
			"total_nodes":   response.TotalNodes,
			"success_count": response.SuccessCount,
			"failure_count": response.FailureCount,
		},
	})

	WriteJSON(w, http.StatusOK, response)
}

func (d Deps) executeBulkRestart(ctx context.Context, actor *rbac.Actor, nodeID int64, nodeName string) error {
	// Record restart request event
	details := map[string]interface{}{
		"action":    "restart",
		"admin_id":  actor.AdminID,
		"node_name": nodeName,
		"bulk":      true,
	}

	if err := nodes.RecordNodeEvent(ctx, d.Store, nodeID, "restart_requested", "info", details, &actor.AdminID); err != nil {
		return err
	}

	// Audit log
	return d.Store.Write(ctx, func(tx *sql.Tx) error {
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, nil), audit.Record{
			Action:     "node.restart",
			TargetType: "node",
			TargetID:   sql.NullInt64{Int64: nodeID, Valid: true},
			Result:     "ok",
			After:      map[string]any{"node": nodeName, "bulk": true},
		})
	})
}

func (d Deps) executeBulkSync(ctx context.Context, actor *rbac.Actor, nodeID int64, nodeName string) error {
	// Record sync request event
	details := map[string]interface{}{
		"action":    "sync",
		"admin_id":  actor.AdminID,
		"node_name": nodeName,
		"bulk":      true,
	}

	if err := nodes.RecordNodeEvent(ctx, d.Store, nodeID, "sync_requested", "info", details, &actor.AdminID); err != nil {
		return err
	}

	// Audit log
	return d.Store.Write(ctx, func(tx *sql.Tx) error {
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, nil), audit.Record{
			Action:     "node.sync",
			TargetType: "node",
			TargetID:   sql.NullInt64{Int64: nodeID, Valid: true},
			Result:     "ok",
			After:      map[string]any{"node": nodeName, "bulk": true},
		})
	})
}

func (d Deps) executeBulkEnable(ctx context.Context, actor *rbac.Actor, nodeID int64) error {
	// Update node status
	err := d.Store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE nodes SET status = 'pending' WHERE id = ? AND status = 'disabled'
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
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, nil), audit.Record{
			Action:     "node.enable",
			TargetType: "node",
			TargetID:   sql.NullInt64{Int64: nodeID, Valid: true},
			Result:     "ok",
			After:      map[string]any{"bulk": true},
		})
	})

	if err != nil {
		return err
	}

	// Record event
	details := map[string]interface{}{
		"action":   "enable",
		"admin_id": actor.AdminID,
		"bulk":     true,
	}

	return nodes.RecordNodeEvent(ctx, d.Store, nodeID, "node_enabled", "info", details, &actor.AdminID)
}

func (d Deps) executeBulkDisable(ctx context.Context, actor *rbac.Actor, nodeID int64, reason string) error {
	// Update node status
	err := d.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE nodes SET status = 'disabled' WHERE id = ?`, nodeID)
		if err != nil {
			return err
		}

		// Audit log
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, nil), audit.Record{
			Action:     "node.disable",
			TargetType: "node",
			TargetID:   sql.NullInt64{Int64: nodeID, Valid: true},
			Result:     "ok",
			After:      map[string]any{"reason": reason, "bulk": true},
		})
	})

	if err != nil {
		return err
	}

	// Record event
	details := map[string]interface{}{
		"action":   "disable",
		"reason":   reason,
		"admin_id": actor.AdminID,
		"bulk":     true,
	}

	return nodes.RecordNodeEvent(ctx, d.Store, nodeID, "node_disabled", "warning", details, &actor.AdminID)
}

func (d Deps) executeBulkMaintenance(ctx context.Context, actor *rbac.Actor, nodeID int64, enable bool, reason string) error {
	// Update maintenance mode
	err := d.Store.Write(ctx, func(tx *sql.Tx) error {
		if enable {
			_, err := tx.ExecContext(ctx, `
				UPDATE nodes
				SET maintenance_mode = 1,
				    maintenance_reason = ?,
				    maintenance_entered_at = ?,
				    status = 'maintenance'
				WHERE id = ?
			`, reason, d.Now().Unix(), nodeID)
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
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, nil), audit.Record{
			Action:     "node.maintenance",
			TargetType: "node",
			TargetID:   sql.NullInt64{Int64: nodeID, Valid: true},
			Result:     "ok",
			After:      map[string]any{"enable": enable, "reason": reason, "bulk": true},
		})
	})

	if err != nil {
		return err
	}

	// Record event
	eventType := "maintenance_exit"
	if enable {
		eventType = "maintenance_enter"
	}

	details := map[string]interface{}{
		"action":   eventType,
		"reason":   reason,
		"admin_id": actor.AdminID,
		"bulk":     true,
	}

	return nodes.RecordNodeEvent(ctx, d.Store, nodeID, eventType, "info", details, &actor.AdminID)
}
