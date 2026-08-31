package httpapi

import (
	"database/sql"
	"net/http"
	"sort"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/amyrm/antimage/internal/panel/rbac"
)

// A subject's timeline: two sources, one endpoint.
//
// Antimage records what happens to a subject in two tables and neither one is
// the whole story: audit_log holds admin actions (create, disable, quota
// changes) with actor and reason; connection_audit_log holds the
// forensic-quality trace of every connect/disconnect/reject/kick, keyed on
// device and node. An operator investigating "what happened to this user
// yesterday" needs both, and the ActivityTimeline component was written for a
// route that returned them together -- which never existed. It exists now.
//
// The client-side shape is preserved intentionally: activities[] carries
// id, event_type, timestamp, ip_address, device_id, node_id, details, and
// bytes fields, so the existing component renders without a rewrite. Fields
// the underlying record doesn't populate stay zero/empty, and the component
// hides them.

type subjectActivity struct {
	ID        int64  `json:"id"`
	SubjectID int64  `json:"subject_id"`
	EventType string `json:"event_type"`
	Timestamp int64  `json:"timestamp"`
	Details   string `json:"details,omitempty"`
	IPAddress string `json:"ip_address,omitempty"`
	DeviceID  string `json:"device_id,omitempty"`
	NodeID    int64  `json:"node_id,omitempty"`
	BytesUp   int64  `json:"bytes_up"`
	BytesDown int64  `json:"bytes_down"`
}

// handleGetSubjectActivity returns the merged activity feed.
//
// Filtering by event_type happens in SQL where possible, then again at merge
// time for admin events -- the client's filter list mixes connection events
// ("connection_start") with admin actions ("disabled"), and both have to
// resolve to the same feed. Pagination is offset-based to match the
// component; changing it later means changing both.
func (d Deps) handleGetSubjectActivity(w http.ResponseWriter, r *http.Request) {
	actor := ActorFrom(r.Context())
	if err := rbac.Check(actor, rbac.PermSubjectRead, rbac.Target{Kind: rbac.TargetNone}); err != nil {
		WriteError(w, http.StatusForbidden, "forbidden", "insufficient permissions")
		return
	}
	subjectID, err := strconv.ParseInt(chi.URLParam(r, "subjectID"), 10, 64)
	if err != nil {
		// Fall back to the router-common name used elsewhere in this file.
		subjectID, err = strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			WriteError(w, http.StatusBadRequest, "bad_request", "invalid subject id")
			return
		}
	}
	// Second-layer scope guard. Without this, a reseller could pull any
	// subject's timeline by id and see IP addresses that are not theirs to
	// see. Anti-enumeration wants a 404 rather than a 403 here, but the
	// shared helper answers whichever the route needs.
	if !d.requireSubjectInScope(w, r, actor, subjectID) {
		return
	}

	limit := parseLimit(r.URL.Query().Get("limit"), 50, 500)
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 0 {
			offset = v
		}
	}
	filter := r.URL.Query().Get("event_type")

	ctx := r.Context()
	out := make([]subjectActivity, 0, limit)

	// Connection events. event_type in the table is one of connect/disconnect/
	// rejected/kicked, but the client filter uses connection_start/end. The
	// mapping is one-way and stated here rather than in the client so the
	// name change doesn't need every client to redeploy.
	dbFilter := connectionFilter(filter)
	connArgs := []any{subjectID}
	connSQL := `SELECT id, node_id, event_type, source_ip, rejection_reason, timestamp,
	                   COALESCE(device_id, 0)
	              FROM connection_audit_log
	             WHERE subject_id = ?`
	if dbFilter != "" {
		connSQL += ` AND event_type = ?`
		connArgs = append(connArgs, dbFilter)
	}
	// Pull enough to satisfy an offset-based page after the merge; the two
	// sources may deliver at very different rates, so limit alone is not a
	// safe cap on either.
	connSQL += ` ORDER BY timestamp DESC LIMIT ?`
	connArgs = append(connArgs, limit+offset)

	// Skip the connection query entirely if the filter is an admin-only event
	// -- there is no point scanning the table for rows that cannot match.
	if !isAdminOnlyFilter(filter) {
		rows, err := d.Store.Read().QueryContext(ctx, connSQL, connArgs...)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "internal",
				"could not read connection activity")
			return
		}
		func() {
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				var (
					id, nodeID, ts int64
					deviceRowID    int64
					eventType      string
					sourceIP       string
					reason         sql.NullString
				)
				if err := rows.Scan(&id, &nodeID, &eventType, &sourceIP, &reason, &ts, &deviceRowID); err != nil {
					return
				}
				out = append(out, subjectActivity{
					ID:        connectionRowKey(id),
					SubjectID: subjectID,
					EventType: mapConnectionEvent(eventType),
					Timestamp: ts,
					IPAddress: sourceIP,
					DeviceID:  formatDeviceID(deviceRowID),
					NodeID:    nodeID,
					Details:   reason.String,
				})
			}
			// rowserrcheck: silent mid-iteration failure returns a truncated
			// list as if complete. Best-effort log path lives upstream.
			_ = rows.Err()
		}()
	}

	// Admin actions from the audit log. Same subject_id filter, same offset
	// pull. The client already renders admin actions ("subject.disable", ...)
	// side by side with connection events -- one timeline, one source of
	// truth per operator, no separate audit tab needed to see them together.
	if !isConnectionOnlyFilter(filter) {
		rows, err := d.Store.Read().QueryContext(ctx,
			`SELECT id, at, actor_type, actor_label, action, result, COALESCE(after_json,'')
			   FROM audit_log
			  WHERE target_type = 'subject' AND target_id = ?
			  ORDER BY at DESC
			  LIMIT ?`, subjectID, limit+offset)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "internal",
				"could not read audit activity")
			return
		}
		func() {
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				var (
					id, ts     int64
					actorType  string
					actorLabel string
					action     string
					result     string
					afterJSON  string
				)
				if err := rows.Scan(&id, &ts, &actorType, &actorLabel, &action, &result, &afterJSON); err != nil {
					return
				}
				mapped := mapAdminAction(action, result)
				if filter != "" && mapped != filter {
					continue
				}
				out = append(out, subjectActivity{
					ID:        auditRowKey(id),
					SubjectID: subjectID,
					EventType: mapped,
					Timestamp: ts,
					Details:   auditMessage(actorType, actorLabel, action, result, afterJSON),
				})
			}
			_ = rows.Err()
		}()
	}

	// Merge, sort, paginate.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Timestamp > out[j].Timestamp })
	total := len(out)
	start := offset
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	page := out[start:end]

	WriteJSON(w, http.StatusOK, map[string]any{
		"activities": page,
		"has_more":   end < total,
	})
}

// connectionFilter maps the client filter names onto connection_audit_log
// event_type values, or empty if the filter doesn't select a connection
// event at all.
func connectionFilter(f string) string {
	switch f {
	case "connection_start":
		return "connect"
	case "connection_end":
		return "disconnect"
	case "":
		return ""
	default:
		return ""
	}
}

func isAdminOnlyFilter(f string) bool {
	switch f {
	case "created", "deleted", "enabled", "disabled", "frozen", "unfrozen",
		"quota_exceeded":
		return true
	}
	return false
}

func isConnectionOnlyFilter(f string) bool {
	switch f {
	case "connection_start", "connection_end", "traffic_update":
		return true
	}
	return false
}

func mapConnectionEvent(dbEvent string) string {
	switch dbEvent {
	case "connect":
		return "connection_start"
	case "disconnect":
		return "connection_end"
	}
	return dbEvent
}

func mapAdminAction(action, result string) string {
	if result == "failed" || result == "denied" {
		return action
	}
	switch action {
	case "subject.disable":
		return "disabled"
	case "subject.enable":
		return "enabled"
	case "subject.freeze":
		return "frozen"
	case "subject.unfreeze":
		return "unfrozen"
	case "subject.create":
		return "created"
	case "subject.delete":
		return "deleted"
	}
	return action
}

func formatDeviceID(rowID int64) string {
	if rowID == 0 {
		return ""
	}
	return strconv.FormatInt(rowID, 10)
}

// The two source tables can share ids; keying them into disjoint ranges lets
// the frontend use `key={activity.id}` without collisions across sources.
func connectionRowKey(id int64) int64 { return id }
func auditRowKey(id int64) int64      { return -id }
