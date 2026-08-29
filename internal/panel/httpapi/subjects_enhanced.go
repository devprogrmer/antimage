package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/subjects"
)

// handleGetSubjectTraffic returns traffic history for a subject
func (d Deps) handleGetSubjectTraffic(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "subjectID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid subject id")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSubjectRead, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	if !d.requireSubjectInScope(w, r, actor, id) {
		return
	}

	// Get subject for quota info
	subj, err := d.subjectStore().Get(r.Context(), rbac.ScopeOf(actor), id)
	if err != nil {
		d.writeServiceError(w, r, actor, "subject.traffic", err)
		return
	}

	// Query hourly rollups last 30 days
	cutoff := time.Now().Add(-30 * 24 * time.Hour).Unix()
	rows, err := d.Store.Read().QueryContext(r.Context(),
		`SELECT hour_start, uplink_bytes, downlink_bytes
		   FROM usage_rollups_hourly
		  WHERE subject_id = ? AND hour_start >= ?
		  ORDER BY hour_start ASC`, id, cutoff)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not query traffic")
		return
	}
	defer rows.Close()

	type point struct {
		Hour     int64 `json:"hour"`
		Uplink   int64 `json:"uplink"`
		Downlink int64 `json:"downlink"`
		Total    int64 `json:"total"`
	}
	var history []point
	var totalUp, totalDown int64
	for rows.Next() {
		var hour, up, down int64
		if err := rows.Scan(&hour, &up, &down); err != nil {
			continue
		}
		history = append(history, point{Hour: hour, Uplink: up, Downlink: down, Total: up + down})
		totalUp += up
		totalDown += down
	}

	// Daily rollups
	dailyRows, err := d.Store.Read().QueryContext(r.Context(),
		`SELECT day_start, uplink_bytes, downlink_bytes
		   FROM usage_rollups_daily
		  WHERE subject_id = ?
		  ORDER BY day_start DESC LIMIT 30`, id)
	if err == nil {
		defer dailyRows.Close()
	}

	type daily struct {
		Day      int64 `json:"day"`
		Uplink   int64 `json:"uplink"`
		Downlink int64 `json:"downlink"`
		Total    int64 `json:"total"`
	}
	var dailyHistory []daily
	if dailyRows != nil {
		for dailyRows.Next() {
			var day, up, down int64
			if err := dailyRows.Scan(&day, &up, &down); err != nil {
				continue
			}
			dailyHistory = append(dailyHistory, daily{Day: day, Uplink: up, Downlink: down, Total: up + down})
		}
	}

	// Node breakdown
	nodeRows, err := d.Store.Read().QueryContext(r.Context(),
		`SELECT node_id, SUM(uplink_bytes + downlink_bytes) as total
		   FROM usage_deltas
		  WHERE subject_id = ?
		  GROUP BY node_id
		  ORDER BY total DESC`, id)
	var nodeBreakdown []map[string]any
	if err == nil {
		defer nodeRows.Close()
		for nodeRows.Next() {
			var nid, total int64
			if err := nodeRows.Scan(&nid, &total); err != nil {
				continue
			}
			// Get node name
			var nname string
			_ = d.Store.Read().QueryRowContext(r.Context(), `SELECT name FROM nodes WHERE id = ?`, nid).Scan(&nname)
			nodeBreakdown = append(nodeBreakdown, map[string]any{
				"node_id": nid, "node_name": nname, "total": total,
			})
		}
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"subject_id":        id,
		"quota_bytes":       subj.QuotaBytes,
		"quota_used_bytes":  subj.QuotaUsedBytes,
		"lifetime_used":     subj.LifetimeUsedBytes,
		"total_uplink":      totalUp,
		"total_downlink":    totalDown,
		"total":             totalUp + totalDown,
		"hourly":            history,
		"daily":             dailyHistory,
		"node_breakdown":    nodeBreakdown,
	})
}

func (d Deps) handleGetSubjectActivity(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "subjectID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid subject id")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSubjectRead, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	if !d.requireSubjectInScope(w, r, actor, id) {
		return
	}

	// Connection audit log
	rows, err := d.Store.Read().QueryContext(r.Context(),
		`SELECT event_type, source_ip, rejection_reason, timestamp, metadata
		   FROM connection_audit_log
		  WHERE subject_id = ?
		  ORDER BY timestamp DESC LIMIT 100`, id)
	var events []map[string]any
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var etype, ip, reason, meta sql.NullString
			var ts int64
			if err := rows.Scan(&etype, &ip, &reason, &ts, &meta); err != nil {
				continue
			}
			events = append(events, map[string]any{
				"event_type": etype.String, "source_ip": ip.String,
				"rejection_reason": reason.String, "timestamp": ts, "metadata": meta.String,
			})
		}
	}

	// Also get devices last seen
	devRows, err := d.Store.Read().QueryContext(r.Context(),
		`SELECT hwid, name, first_seen_at, last_seen_at, last_ip, is_active
		   FROM subject_devices
		  WHERE subject_id = ?
		  ORDER BY last_seen_at DESC LIMIT 50`, id)
	var devices []map[string]any
	if err == nil {
		defer devRows.Close()
		for devRows.Next() {
			var hwid, name, lastIP string
			var first, last sql.NullInt64
			var active int
			if err := devRows.Scan(&hwid, &name, &first, &last, &lastIP, &active); err != nil {
				continue
			}
			devices = append(devices, map[string]any{
				"hwid": hwid, "name": name, "first_seen_at": first.Int64,
				"last_seen_at": last.Int64, "last_ip": lastIP, "is_active": active == 1,
			})
		}
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"subject_id": id,
		"events":     events,
		"devices":    devices,
	})
}

func (d Deps) handleGetSubjectIPs(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "subjectID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid subject id")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSubjectRead, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	if !d.requireSubjectInScope(w, r, actor, id) {
		return
	}

	rows, err := d.Store.Read().QueryContext(r.Context(),
		`SELECT node_id, connection_id, source_ip, connected_at, last_seen_at, protocol_info
		   FROM active_connections
		  WHERE subject_id = ?
		  ORDER BY last_seen_at DESC`, id)
	var conns []map[string]any
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var nid int64
			var connID, ip, protoInfo string
			var connected, lastSeen int64
			if err := rows.Scan(&nid, &connID, &ip, &connected, &lastSeen, &protoInfo); err != nil {
				continue
			}
			var nname string
			_ = d.Store.Read().QueryRowContext(r.Context(), `SELECT name FROM nodes WHERE id = ?`, nid).Scan(&nname)
			conns = append(conns, map[string]any{
				"node_id": nid, "node_name": nname, "connection_id": connID,
				"source_ip": ip, "connected_at": connected, "last_seen_at": lastSeen,
				"protocol_info": protoInfo,
			})
		}
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"subject_id": id,
		"connections": conns,
		"total": len(conns),
	})
}

func (d Deps) handleGetSubjectAudit(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "subjectID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid subject id")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSubjectRead, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	if !d.requireSubjectInScope(w, r, actor, id) {
		return
	}

	rows, err := d.Store.Read().QueryContext(r.Context(),
		`SELECT id, action, actor_type, actor_id, target_type, result, before_json, after_json, created_at, request_id
		   FROM audit_log
		  WHERE target_type = 'subject' AND target_id = ?
		  ORDER BY created_at DESC LIMIT 100`, id)
	var logs []map[string]any
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var action, actorType, targetType, result, before, after, reqID sql.NullString
			var actorID sql.NullInt64
			var createdAt int64
			var logID int64
			if err := rows.Scan(&logID, &action, &actorType, &actorID, &targetType, &result, &before, &after, &createdAt, &reqID); err != nil {
				continue
			}
			logs = append(logs, map[string]any{
				"id": logID, "action": action.String, "actor_type": actorType.String,
				"actor_id": actorID.Int64, "target_type": targetType.String, "result": result.String,
				"before": before.String, "after": after.String, "created_at": createdAt, "request_id": reqID.String,
			})
		}
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"subject_id": id,
		"audit": logs,
		"total": len(logs),
	})
}

// Bulk add traffic
func (d Deps) handleBulkAddTraffic(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	var req struct {
		SubjectIDs []int64 `json:"subject_ids"`
		Bytes      int64   `json:"bytes"`
		GB         *int64  `json:"gb"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed body")
		return
	}
	if len(req.SubjectIDs) == 0 {
		WriteError(w, http.StatusBadRequest, "bad_request", "subject_ids required")
		return
	}
	delta := req.Bytes
	if req.GB != nil {
		delta = *req.GB * 1024 * 1024 * 1024
	}
	if delta == 0 {
		WriteError(w, http.StatusBadRequest, "bad_request", "bytes or gb required")
		return
	}
	scoped, err := d.scopeFilterSubjectIDs(r, req.SubjectIDs)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "scope check failed")
		return
	}
	svc := d.subjectService()
	sa := d.svcActor(r, actor)
	added := 0
	failed := 0
	for _, sid := range scoped {
		if err := svc.AddTraffic(r.Context(), sa, sid, delta); err != nil {
			failed++
		} else {
			added++
		}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"added": added, "failed": failed})
}

func (d Deps) handleBulkAddDays(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	var req struct {
		SubjectIDs []int64 `json:"subject_ids"`
		Days       int     `json:"days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed body")
		return
	}
	if len(req.SubjectIDs) == 0 || req.Days == 0 {
		WriteError(w, http.StatusBadRequest, "bad_request", "subject_ids and days required")
		return
	}
	scoped, err := d.scopeFilterSubjectIDs(r, req.SubjectIDs)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "scope check failed")
		return
	}
	svc := d.subjectService()
	sa := d.svcActor(r, actor)
	extended := 0
	failed := 0
	for _, sid := range scoped {
		if err := svc.AddDays(r.Context(), sa, sid, req.Days); err != nil {
			failed++
		} else {
			extended++
		}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"extended": extended, "failed": failed})
}

// Ensure subjects import not unused
var _ = subjects.GenerateToken
