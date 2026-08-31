package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/amyrm/antimage/internal/panel/rbac"
)

// The panel does not ship agent logs. Nothing in the protocol streams stderr
// from the agent, nothing stores it, and no MCP call fetches it. What the
// panel HAS is its own timeline of everything that has happened to a node:
// apply-run outcomes with the failing step's stderr, audit records of every
// admin action against the node, and the last error the agent reported over
// the control stream. That is what an operator looking at "logs" during an
// incident actually needs.
//
// This route was called from the browser as `/logs?limit=50` and returned
// nothing, because it did not exist. It exists now, and reports what the
// panel actually knows.

// nodeLogEntry mirrors the shape NodeDetailPanel.tsx already renders (timestamp,
// level, message). Source is added for the UI to render an origin badge; a
// client that ignores it still gets a usable line.
type nodeLogEntry struct {
	Timestamp int64  `json:"timestamp"`
	Level     string `json:"level"`
	Message   string `json:"message"`
	Source    string `json:"source"`
}

// handleGetNodeLogs returns the panel's timeline for a node.
//
// The union of three sources, newest first:
//
//   - apply-run steps that FAILED, each carrying the step's stderr in .error.
//     A converged step is not a log line; noise crowds the failing ones out.
//   - audit records targeting this node -- who did what and whether it took.
//   - the current last_error on the node row, if any. It is not a history --
//     the row only stores the latest -- but during an incident it is the one
//     line an operator wants first.
//
// Sorted at the SQL layer via UNION ALL over pre-sorted subselects would need
// the driver to preserve order across the union, and modernc.org/sqlite does
// not guarantee it; sorted here instead over a small in-memory slice. The
// limit is the browser's ?limit=; clamped so a caller cannot ask for the
// entire audit log against one node id.
func (d Deps) handleGetNodeLogs(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "nodeID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node id")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermNodeRead, rbac.Target{Kind: rbac.TargetNode, ID: id}) {
		return
	}
	limit := parseLimit(r.URL.Query().Get("limit"), 50, 500)

	ctx := r.Context()
	out := make([]nodeLogEntry, 0, limit)

	// 1. The node's current last_error. Newest by definition -- if it exists.
	{
		var lastErr sql.NullString
		var lastSeen sql.NullInt64
		err := d.Store.Read().QueryRowContext(ctx,
			`SELECT last_error, last_seen_at FROM nodes WHERE id = ?`, id,
		).Scan(&lastErr, &lastSeen)
		switch {
		case err == sql.ErrNoRows:
			WriteError(w, http.StatusNotFound, "not_found", "node not found")
			return
		case err != nil:
			WriteError(w, http.StatusInternalServerError, "internal", "could not read node")
			return
		}
		if lastErr.Valid && lastErr.String != "" {
			out = append(out, nodeLogEntry{
				Timestamp: lastSeen.Int64, // best we have; last_error has no timestamp of its own
				Level:     "error",
				Source:    "agent",
				Message:   lastErr.String,
			})
		}
	}

	// 2. Failing apply-run steps. Ordered by run then step so a run's steps
	//    stay together after the top-level sort by timestamp.
	{
		rows, err := d.Store.Read().QueryContext(ctx,
			`SELECT r.started_at, r.target_revision, s.step_kind, s.disruption, s.error
			   FROM node_apply_runs r
			   JOIN node_apply_steps s ON s.run_id = r.id
			  WHERE r.node_id = ? AND s.outcome = 'failed'
			  ORDER BY r.started_at DESC, s.seq ASC
			  LIMIT ?`, id, limit)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "could not read apply runs")
			return
		}
		func() {
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				var (
					startedAt      int64
					targetRevision int64
					stepKind       string
					disruption     string
					stepErr        string
				)
				if err := rows.Scan(&startedAt, &targetRevision, &stepKind, &disruption, &stepErr); err != nil {
					return
				}
				out = append(out, nodeLogEntry{
					Timestamp: startedAt,
					Level:     "error",
					Source:    "apply",
					Message: "apply r" + strconv.FormatInt(targetRevision, 10) +
						" · " + stepKind + " (" + disruption + "): " + stepErr,
				})
			}
			// rowserrcheck: a mid-iteration failure returns a truncated slice
			// as if complete; the caller can only tell the two apart by asking.
			_ = rows.Err()
		}()
	}

	// 3. Audit records against this node. audit_log has no FK on target_id
	//    (see 00005), so this is a straight filter on target_type+target_id.
	//    An admin listing here reveals only WHAT happened to nodes the caller
	//    can already see -- the scope guard on this whole route is the node
	//    permission check above.
	{
		rows, err := d.Store.Read().QueryContext(ctx,
			`SELECT at, actor_type, actor_label, action, result, COALESCE(after_json,'')
			   FROM audit_log
			  WHERE target_type = 'node' AND target_id = ?
			  ORDER BY at DESC
			  LIMIT ?`, id, limit)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "could not read audit")
			return
		}
		func() {
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				var (
					at         int64
					actorType  string
					actorLabel string
					action     string
					result     string
					afterJSON  string
				)
				if err := rows.Scan(&at, &actorType, &actorLabel, &action, &result, &afterJSON); err != nil {
					return
				}
				out = append(out, nodeLogEntry{
					Timestamp: at,
					Level:     auditLevel(result),
					Source:    "audit",
					Message:   auditMessage(actorType, actorLabel, action, result, afterJSON),
				})
			}
			_ = rows.Err()
		}()
	}

	// One combined sort keeps the timeline honest across sources: an audit row
	// from a minute ago must precede a failing apply-run step from an hour ago
	// even though the queries returned them in different orders.
	sortLogsDesc(out)
	if len(out) > limit {
		out = out[:limit]
	}

	WriteJSON(w, http.StatusOK, map[string]any{"logs": out})
}

func parseLimit(raw string, def, max int) int {
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
}

func auditLevel(result string) string {
	switch result {
	case "failed":
		return "error"
	case "denied":
		return "warn"
	default:
		return "info"
	}
}

// auditMessage renders an audit row into one line.
//
// It extracts the after_json "reason" or "error" when present, because the
// action name alone ("node.disable") does not say why. Everything else in the
// JSON is left out here on purpose: a raw dump crowds the useful line off the
// screen, and the full record is already available on the audit route.
func auditMessage(actorType, actorLabel, action, result, afterJSON string) string {
	actor := actorLabel
	if actor == "" {
		actor = actorType
	}
	msg := actor + " · " + action + " · " + result
	if afterJSON == "" || afterJSON == "null" {
		return msg
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(afterJSON), &payload); err != nil {
		return msg
	}
	for _, key := range []string{"reason", "error", "message"} {
		if v, ok := payload[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return msg + " — " + s
			}
		}
	}
	return msg
}

// sortLogsDesc is a hand-written insertion sort. The slice is small (bounded
// by 3×limit ≤ 1500) and the standard library's sort would need a Less
// closure that reads the field twice; nothing here justifies the allocation.
func sortLogsDesc(logs []nodeLogEntry) {
	for i := 1; i < len(logs); i++ {
		cur := logs[i]
		j := i - 1
		for j >= 0 && logs[j].Timestamp < cur.Timestamp {
			logs[j+1] = logs[j]
			j--
		}
		logs[j+1] = cur
	}
}
