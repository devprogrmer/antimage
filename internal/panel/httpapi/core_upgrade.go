package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/control"
	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/rbac"
	pb "github.com/amyrm/antimage/internal/shared/proto/antimage/v1"
)

// coreUpgradeCommandTimeout is generous: this involves a real binary
// download, a preflight exec, an atomic file swap, a restart, and up to
// coreHealthPollWindow (20s in the agent) of health polling -- and, on
// failure, a SECOND restart and health poll for the rollback. Shorter than
// this and a legitimate slow rollback could be reported as a bare timeout
// when the agent is still working through it correctly.
const coreUpgradeCommandTimeout = 5 * time.Minute

type coreUpgradeRequest struct {
	Kind            string `json:"kind"`
	BinaryURL       string `json:"binary_url"`
	BinarySHA256    string `json:"binary_sha256"`
	ExpectedVersion string `json:"expected_version"`
}

// POST /api/v1/nodes/:id/core-upgrade
//
// Unlike restart and geo-update, every field here is REQUIRED -- there is
// no default binary source for an executable the way there is for geo
// data. Kind picks which adapter (xray, in practice, today); the other
// three name an EXACT artifact the operator already chose, typically from
// GET /xray-core-versions. Goes through the same on-demand command channel
// as restart/geo-update: a real UpgradeCore command reaches a connected
// agent, which downloads, verifies, preflights, swaps, restarts, and rolls
// back on its own if the new binary does not come up healthy -- reported
// back honestly either way, never a canned "upgrade requested".
func (d Deps) handleUpgradeNodeCore(w http.ResponseWriter, r *http.Request) {
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

	var nodeName string
	err = d.Store.Read().QueryRowContext(ctx,
		`SELECT name FROM nodes WHERE id = ?`, nodeID).Scan(&nodeName)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "not_found", "node not found")
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not load node")
		return
	}

	var req coreUpgradeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if strings.TrimSpace(req.Kind) == "" || strings.TrimSpace(req.BinaryURL) == "" ||
		strings.TrimSpace(req.BinarySHA256) == "" {
		WriteError(w, http.StatusBadRequest, "bad_request",
			"kind, binary_url and binary_sha256 are all required -- there is no default core binary source")
		return
	}

	cmd := &pb.AgentCommand{
		CommandId: uuid.NewString(),
		Body: &pb.AgentCommand_UpgradeCore{
			UpgradeCore: &pb.UpgradeCore{
				Kind: req.Kind, BinaryUrl: req.BinaryURL,
				BinarySha256: req.BinarySHA256, ExpectedVersion: req.ExpectedVersion,
			},
		},
	}

	var (
		delivered        bool
		ok2              bool
		installedVersion string
		rolledBack       bool
		cmdErr           string
	)
	if d.Hub != nil {
		result, sendErr := d.Hub.SendCommand(ctx, nodeID, cmd, coreUpgradeCommandTimeout)
		switch {
		case sendErr == nil:
			delivered = true
			if core, isCore := result.Body.(*pb.AgentCommandResult_UpgradeCore); isCore {
				ok2 = core.UpgradeCore.Ok
				installedVersion = core.UpgradeCore.InstalledVersion
				rolledBack = core.UpgradeCore.RolledBack
				cmdErr = core.UpgradeCore.Error
				if ok2 {
					if _, err := nodes.RecordCoreUpgrade(ctx, d.Store, nodeID, req.Kind, installedVersion, d.now()); err != nil {
						audit.BestEffort(ctx, d.Store, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
							Action: "node.core_upgrade.record_failed", TargetType: "node",
							TargetID: sql.NullInt64{Int64: nodeID, Valid: true},
							Result:   "failed",
							After:    map[string]any{"kind": req.Kind, "error": err.Error()},
						})
					}
				}
			} else {
				delivered = false
				cmdErr = "the agent did not recognise this command; it may need an upgrade"
			}
		case errors.Is(sendErr, control.ErrCommandNotDelivered):
			// Not an error: the node is offline.
		case errors.Is(sendErr, control.ErrCommandTimeout):
			cmdErr = "the agent did not reply before the deadline"
		default:
			cmdErr = sendErr.Error()
		}
	}

	if err := nodes.RecordNodeEvent(ctx, d.Store, nodeID, "core_upgrade_requested", "info", map[string]interface{}{
		"action": "core_upgrade", "admin_id": actor.AdminID, "node_name": nodeName,
		"kind": req.Kind, "delivered": delivered, "ok": ok2, "rolled_back": rolledBack,
	}, &actor.AdminID); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "failed to record event")
		return
	}
	if err := d.Store.Write(ctx, func(tx *sql.Tx) error {
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action:     "node.core_upgrade",
			TargetType: "node",
			TargetID:   sql.NullInt64{Int64: nodeID, Valid: true},
			Result:     "ok",
			After: map[string]any{
				"node": nodeName, "kind": req.Kind, "delivered": delivered,
				"ok": ok2, "rolled_back": rolledBack,
			},
		})
	}); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "audit failed")
		return
	}

	message := "the node is offline; nothing was upgraded"
	switch {
	case cmdErr != "" && rolledBack:
		message = "the upgrade failed and was rolled back to the previous version: " + cmdErr
	case cmdErr != "":
		message = cmdErr
	case delivered && ok2:
		message = "core upgraded"
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"node_id":           nodeID,
		"node_name":         nodeName,
		"action":            "core_upgrade",
		"delivered":         delivered,
		"ok":                ok2,
		"installed_version": installedVersion,
		"rolled_back":       rolledBack,
		"error":             cmdErr,
		"message":           message,
	})
}
