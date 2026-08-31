package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/amyrm/antimage/internal/panel/control"
	"github.com/amyrm/antimage/internal/panel/rbac"
	pb "github.com/amyrm/antimage/internal/shared/proto/antimage/v1"
)

// xrayLogsCommandTimeout bounds one journalctl round trip through the
// command channel. Reading a few thousand lines from the local journal is
// fast; this is generous mainly so a node under heavy load does not turn a
// slow-but-working read into a false timeout.
const xrayLogsCommandTimeout = 30 * time.Second

const (
	defaultXrayLogLines = 200
	maxXrayLogLines     = 2000
)

// GET /api/v1/nodes/:id/xray-logs?lines=200
//
// Read-only, so unlike restart/geo-update/core-upgrade this records no
// audit entry and no node event: fetching a log is not an action taken
// against the node, and an operator refreshing this view repeatedly would
// otherwise flood both tables with rows nobody needs. It still goes
// through the same on-demand command channel as those three, because
// there is no other path from the panel to a connected agent -- the
// response is delivered=false with an honest message when the node is
// offline, exactly like they report it, never a canned success.
func (d Deps) handleGetXrayLogs(w http.ResponseWriter, r *http.Request) {
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

	lines := parseLimit(r.URL.Query().Get("lines"), defaultXrayLogLines, maxXrayLogLines)

	cmd := &pb.AgentCommand{
		CommandId: uuid.NewString(),
		Body: &pb.AgentCommand_FetchLogs{
			FetchLogs: &pb.FetchLogs{Kind: "xray", Lines: int32(lines)},
		},
	}

	var (
		delivered bool
		ok2       bool
		logs      string
		cmdErr    string
	)
	if d.Hub != nil {
		result, sendErr := d.Hub.SendCommand(ctx, nodeID, cmd, xrayLogsCommandTimeout)
		switch {
		case sendErr == nil:
			delivered = true
			if fl, isFL := result.Body.(*pb.AgentCommandResult_FetchLogs); isFL {
				ok2 = fl.FetchLogs.Ok
				logs = fl.FetchLogs.Logs
				cmdErr = fl.FetchLogs.Error
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

	message := "the node is offline; no logs were fetched"
	switch {
	case cmdErr != "":
		message = cmdErr
	case delivered && ok2:
		message = "logs fetched"
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"node_id":   nodeID,
		"node_name": nodeName,
		"delivered": delivered,
		"ok":        ok2,
		"logs":      logs,
		"error":     cmdErr,
		"message":   message,
	})
}
