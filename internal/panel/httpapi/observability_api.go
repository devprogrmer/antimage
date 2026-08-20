package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/amyrm/antimage/internal/panel/observability"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

// AlertJSON represents an alert in API responses.
type AlertJSON struct {
	ID             int64                  `json:"id"`
	AlertType      string                 `json:"alert_type"`
	Severity       string                 `json:"severity"`
	TargetType     string                 `json:"target_type"`
	TargetID       int64                  `json:"target_id"`
	State          string                 `json:"state"`
	FirstSeenAt    string                 `json:"first_seen_at"`
	LastSeenAt     string                 `json:"last_seen_at"`
	ResolvedAt     *string                `json:"resolved_at"`
	ThresholdValue string                 `json:"threshold_value"`
	CurrentValue   string                 `json:"current_value"`
	Metadata       map[string]interface{} `json:"metadata"`
}

// AlertsResponse is the response structure for GET /api/v1/alerts.
type AlertsResponse struct {
	Alerts []AlertJSON `json:"alerts"`
	Total  int         `json:"total"`
	Limit  int         `json:"limit"`
	Offset int         `json:"offset"`
}

// handleListAlerts implements GET /api/v1/alerts.
// Enforces RBAC: requires alerts:read permission and filters by admin scopes.
func (d Deps) handleListAlerts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermAlertRead, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}

	scope := rbac.ScopeOf(actor)

	// Parse query parameters
	state := r.URL.Query().Get("state")
	if state == "" {
		state = "active"
	}

	alertType := r.URL.Query().Get("alert_type")
	severity := r.URL.Query().Get("severity")
	targetType := r.URL.Query().Get("target_type")

	var targetID *int64
	if targetIDStr := r.URL.Query().Get("target_id"); targetIDStr != "" {
		if id, err := strconv.ParseInt(targetIDStr, 10, 64); err == nil {
			targetID = &id
		}
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit == 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	// Build filters with scope enforcement
	filters := observability.AlertFilters{
		Scope:      scope,
		State:      observability.AlertState(state),
		AlertType:  observability.AlertType(alertType),
		Severity:   observability.Severity(severity),
		TargetType: observability.TargetType(targetType),
		TargetID:   targetID,
		Limit:      limit,
		Offset:     offset,
	}

	// Query alerts
	alerts, total, err := observability.ListAlerts(ctx, d.Store, filters)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Convert to JSON
	alertsJSON := make([]AlertJSON, len(alerts))
	for i, a := range alerts {
		alertJSON := AlertJSON{
			ID:             a.ID,
			AlertType:      string(a.AlertType),
			Severity:       string(a.Severity),
			TargetType:     string(a.TargetType),
			TargetID:       a.TargetID,
			State:          string(a.State),
			FirstSeenAt:    a.FirstSeenAt.Format(time.RFC3339),
			LastSeenAt:     a.LastSeenAt.Format(time.RFC3339),
			ThresholdValue: a.ThresholdValue,
			CurrentValue:   a.CurrentValue,
			Metadata:       a.Metadata,
		}
		if a.ResolvedAt != nil {
			resolved := a.ResolvedAt.Format(time.RFC3339)
			alertJSON.ResolvedAt = &resolved
		}
		alertsJSON[i] = alertJSON
	}

	response := AlertsResponse{
		Alerts: alertsJSON,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

// HistoryDataPoint represents a single metric data point.
type HistoryDataPoint struct {
	Timestamp string  `json:"timestamp"`
	Value     *int64  `json:"value,omitempty"`     // For raw samples
	Avg       *int64  `json:"avg,omitempty"`       // For rollups
	Min       *int64  `json:"min,omitempty"`       // For rollups (RTT only)
	Max       *int64  `json:"max,omitempty"`       // For rollups (RTT only)
	Samples   *int    `json:"samples,omitempty"`   // For rollups
}

// NodeHistoryResponse is the response structure for GET /api/v1/nodes/{nodeID}/history.
type NodeHistoryResponse struct {
	Metric      string             `json:"metric"`
	Granularity string             `json:"granularity"`
	NodeID      int64              `json:"node_id"`
	Data        []HistoryDataPoint `json:"data"`
	Total       int                `json:"total"`
	Limit       int                `json:"limit"`
	Offset      int                `json:"offset"`
}

// handleNodeHistory implements GET /api/v1/nodes/{nodeID}/history.
// Enforces RBAC: requires node:read permission and verifies node is in scope.
func (d Deps) handleNodeHistory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	nodeIDStr := chi.URLParam(r, "nodeID")

	var nodeID int64
	if _, err := fmt.Sscanf(nodeIDStr, "%d", &nodeID); err != nil {
		http.Error(w, "invalid node id", http.StatusBadRequest)
		return
	}

	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermNodeRead, rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	// Verify node exists and is accessible (scope check)
	scope := rbac.ScopeOf(actor)
	_, err := d.Store.GetNode(ctx, scope, nodeID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "not found", http.StatusNotFound)
		} else {
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	// Parse query parameters
	metric := r.URL.Query().Get("metric")
	if metric == "" {
		http.Error(w, "metric required", http.StatusBadRequest)
		return
	}

	granularity := r.URL.Query().Get("granularity")
	if granularity == "" {
		granularity = "raw"
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit == 0 {
		limit = 1000
	}
	// Apply max limits based on granularity
	if granularity == "raw" && limit > 5000 {
		limit = 5000
	} else if (granularity == "hourly" || granularity == "daily") && limit > 2000 {
		limit = 2000
	}

	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	var data []HistoryDataPoint
	var total int

	switch granularity {
	case "raw":
		data, total, err = d.queryRawHistory(ctx, nodeID, metric, limit, offset)
	case "hourly":
		data, total, err = d.queryHourlyHistory(ctx, nodeID, metric, limit, offset)
	case "daily":
		data, total, err = d.queryDailyHistory(ctx, nodeID, metric, limit, offset)
	default:
		http.Error(w, "invalid granularity", http.StatusBadRequest)
		return
	}

	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	response := NodeHistoryResponse{
		Metric:      metric,
		Granularity: granularity,
		NodeID:      nodeID,
		Data:        data,
		Total:       total,
		Limit:       limit,
		Offset:      offset,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func (d Deps) queryRawHistory(ctx context.Context, nodeID int64, metric string, limit, offset int) ([]HistoryDataPoint, int, error) {
	var query string
	var valueColumn string

	switch metric {
	case "rtt":
		query = `SELECT at, rtt_ms FROM node_health WHERE node_id = ? AND rtt_ms > 0 ORDER BY at DESC LIMIT ? OFFSET ?`
		valueColumn = "rtt_ms"
	case "load":
		query = `SELECT at, load1 FROM node_health WHERE node_id = ? ORDER BY at DESC LIMIT ? OFFSET ?`
		valueColumn = "load1"
	case "memory":
		query = `SELECT at, mem_used FROM node_health WHERE node_id = ? ORDER BY at DESC LIMIT ? OFFSET ?`
		valueColumn = "mem_used"
	case "uptime":
		query = `SELECT at, uptime_s FROM node_health WHERE node_id = ? ORDER BY at DESC LIMIT ? OFFSET ?`
		valueColumn = "uptime_s"
	default:
		return nil, 0, fmt.Errorf("unknown metric: %s", metric)
	}

	// Query total count
	var total int
	countQuery := `SELECT COUNT(*) FROM node_health WHERE node_id = ?`
	if metric == "rtt" {
		countQuery += ` AND rtt_ms > 0`
	}
	if err := d.Store.Read().QueryRowContext(ctx, countQuery, nodeID).Scan(&total); err != nil {
		return nil, 0, err
	}

	// Query data
	rows, err := d.Store.Read().QueryContext(ctx, query, nodeID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	var data []HistoryDataPoint
	for rows.Next() {
		var atUnix int64
		var value int64

		if valueColumn == "load1" {
			var loadValue float64
			if err := rows.Scan(&atUnix, &loadValue); err != nil {
				return nil, 0, err
			}
			value = int64(loadValue * 100) // Convert to centiles for JSON
		} else {
			if err := rows.Scan(&atUnix, &value); err != nil {
				return nil, 0, err
			}
		}

		data = append(data, HistoryDataPoint{
			Timestamp: time.Unix(atUnix, 0).UTC().Format(time.RFC3339),
			Value:     &value,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	return data, total, nil
}

func (d Deps) queryHourlyHistory(ctx context.Context, nodeID int64, metric string, limit, offset int) ([]HistoryDataPoint, int, error) {
	// Query total count
	var total int
	if err := d.Store.Read().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM node_health_rollups_hourly WHERE node_id = ?`,
		nodeID).Scan(&total); err != nil {
		return nil, 0, err
	}

	// Query data based on metric
	var query string
	switch metric {
	case "rtt":
		query = `SELECT hour_start, samples, min_rtt_ms, avg_rtt_ms, max_rtt_ms
		         FROM node_health_rollups_hourly WHERE node_id = ? ORDER BY hour_start DESC LIMIT ? OFFSET ?`
	case "load":
		query = `SELECT hour_start, samples, avg_load1
		         FROM node_health_rollups_hourly WHERE node_id = ? ORDER BY hour_start DESC LIMIT ? OFFSET ?`
	case "memory":
		query = `SELECT hour_start, samples, avg_mem_used
		         FROM node_health_rollups_hourly WHERE node_id = ? ORDER BY hour_start DESC LIMIT ? OFFSET ?`
	case "uptime":
		query = `SELECT hour_start, samples, uptime_seconds
		         FROM node_health_rollups_hourly WHERE node_id = ? ORDER BY hour_start DESC LIMIT ? OFFSET ?`
	default:
		return nil, 0, fmt.Errorf("unknown metric: %s", metric)
	}

	rows, err := d.Store.Read().QueryContext(ctx, query, nodeID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	var data []HistoryDataPoint
	for rows.Next() {
		var atUnix int64
		var samples int

		dp := HistoryDataPoint{Samples: &samples}

		switch metric {
		case "rtt":
			var minRTT, avgRTT, maxRTT int64
			if err := rows.Scan(&atUnix, &samples, &minRTT, &avgRTT, &maxRTT); err != nil {
				return nil, 0, err
			}
			dp.Min = &minRTT
			dp.Avg = &avgRTT
			dp.Max = &maxRTT
		case "load":
			var avgLoad float64
			if err := rows.Scan(&atUnix, &samples, &avgLoad); err != nil {
				return nil, 0, err
			}
			avgLoadInt := int64(avgLoad * 100)
			dp.Avg = &avgLoadInt
		case "memory", "uptime":
			var avgValue int64
			if err := rows.Scan(&atUnix, &samples, &avgValue); err != nil {
				return nil, 0, err
			}
			dp.Avg = &avgValue
		}

		dp.Timestamp = time.Unix(atUnix, 0).UTC().Format(time.RFC3339)
		data = append(data, dp)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	return data, total, nil
}

func (d Deps) queryDailyHistory(ctx context.Context, nodeID int64, metric string, limit, offset int) ([]HistoryDataPoint, int, error) {
	// Query total count
	var total int
	if err := d.Store.Read().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM node_health_rollups_daily WHERE node_id = ?`,
		nodeID).Scan(&total); err != nil {
		return nil, 0, err
	}

	// Query data based on metric
	var query string
	switch metric {
	case "rtt":
		query = `SELECT day_start, samples, min_rtt_ms, avg_rtt_ms, max_rtt_ms
		         FROM node_health_rollups_daily WHERE node_id = ? ORDER BY day_start DESC LIMIT ? OFFSET ?`
	case "load":
		query = `SELECT day_start, samples, avg_load1
		         FROM node_health_rollups_daily WHERE node_id = ? ORDER BY day_start DESC LIMIT ? OFFSET ?`
	case "memory":
		query = `SELECT day_start, samples, avg_mem_used
		         FROM node_health_rollups_daily WHERE node_id = ? ORDER BY day_start DESC LIMIT ? OFFSET ?`
	case "uptime":
		query = `SELECT day_start, samples, uptime_seconds
		         FROM node_health_rollups_daily WHERE node_id = ? ORDER BY day_start DESC LIMIT ? OFFSET ?`
	default:
		return nil, 0, fmt.Errorf("unknown metric: %s", metric)
	}

	rows, err := d.Store.Read().QueryContext(ctx, query, nodeID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	var data []HistoryDataPoint
	for rows.Next() {
		var atUnix int64
		var samples int

		dp := HistoryDataPoint{Samples: &samples}

		switch metric {
		case "rtt":
			var minRTT, avgRTT, maxRTT int64
			if err := rows.Scan(&atUnix, &samples, &minRTT, &avgRTT, &maxRTT); err != nil {
				return nil, 0, err
			}
			dp.Min = &minRTT
			dp.Avg = &avgRTT
			dp.Max = &maxRTT
		case "load":
			var avgLoad float64
			if err := rows.Scan(&atUnix, &samples, &avgLoad); err != nil {
				return nil, 0, err
			}
			avgLoadInt := int64(avgLoad * 100)
			dp.Avg = &avgLoadInt
		case "memory", "uptime":
			var avgValue int64
			if err := rows.Scan(&atUnix, &samples, &avgValue); err != nil {
				return nil, 0, err
			}
			dp.Avg = &avgValue
		}

		dp.Timestamp = time.Unix(atUnix, 0).UTC().Format(time.RFC3339)
		data = append(data, dp)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	return data, total, nil
}

// FleetSummaryJSON is the response structure for GET /api/v1/fleet/summary.
type FleetSummaryJSON struct {
	TotalNodes      int            `json:"total_nodes"`
	ByStatus        map[string]int `json:"by_status"`
	ActiveAlerts    map[string]int `json:"active_alerts"`
	AvgFleetRTTMs   *int64         `json:"avg_fleet_rtt_ms"`
	NodesWithIssues int            `json:"nodes_with_issues"`
}

// handleFleetSummary implements GET /api/v1/fleet/summary.
// Enforces RBAC: requires node:read permission and aggregates only accessible nodes.
func (d Deps) handleFleetSummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermNodeRead, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}

	scope := rbac.ScopeOf(actor)

	summary := FleetSummaryJSON{
		ByStatus:     make(map[string]int),
		ActiveAlerts: make(map[string]int),
	}

	// Build scope filter SQL
	var scopeFilter string
	var scopeArgs []interface{}
	if !scope.IsSuper {
		scopeFilter = ` WHERE id IN (SELECT node_id FROM admin_scopes WHERE admin_id = ?)`
		scopeArgs = append(scopeArgs, scope.AdminID)
	}

	// Count total nodes (scope-filtered)
	countQuery := `SELECT COUNT(*) FROM nodes` + scopeFilter
	if err := d.Store.Read().QueryRowContext(ctx, countQuery, scopeArgs...).Scan(&summary.TotalNodes); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Count nodes by status (scope-filtered)
	statusQuery := `SELECT status, COUNT(*) FROM nodes` + scopeFilter + ` GROUP BY status`
	rows, err := d.Store.Read().QueryContext(ctx, statusQuery, scopeArgs...)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		summary.ByStatus[status] = count
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	_ = rows.Close()

	// Count active alerts by severity (scope-filtered)
	// Use same scope logic as ListAlerts
	var alertQuery string
	var alertArgs []interface{}
	if !scope.IsSuper {
		alertQuery = `
			SELECT severity, COUNT(*)
			FROM alerts
			WHERE state = 'active' AND (
				(target_type = 'node' AND target_id IN (SELECT node_id FROM admin_scopes WHERE admin_id = ?))
				OR
				(target_type = 'subject' AND target_id IN (
					SELECT subjects.id FROM subjects
					JOIN services ON services.subject_id = subjects.id
					JOIN nodes ON services.node_id = nodes.id
					JOIN admin_scopes ON admin_scopes.node_id = nodes.id
					WHERE admin_scopes.admin_id = ?
				))
			)
			GROUP BY severity`
		alertArgs = append(alertArgs, scope.AdminID, scope.AdminID)
	} else {
		alertQuery = `SELECT severity, COUNT(*) FROM alerts WHERE state = 'active' GROUP BY severity`
	}

	rows, err = d.Store.Read().QueryContext(ctx, alertQuery, alertArgs...)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var severity string
		var count int
		if err := rows.Scan(&severity, &count); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		summary.ActiveAlerts[severity] = count
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	_ = rows.Close()

	// Calculate average fleet RTT from connection_metrics (scope-filtered, last 10 samples per node)
	var avgRTT *int64
	var rttQuery string
	var rttArgs []interface{}
	if !scope.IsSuper {
		rttQuery = `
			SELECT CAST(AVG(avg_rtt) AS INTEGER)
			FROM (
				SELECT node_id, AVG(rtt_ms) AS avg_rtt
				FROM (
					SELECT node_id, rtt_ms, ROW_NUMBER() OVER (PARTITION BY node_id ORDER BY measured_at DESC) AS rn
					FROM connection_metrics
					WHERE rtt_ms IS NOT NULL AND node_id IN (SELECT node_id FROM admin_scopes WHERE admin_id = ?)
				)
				WHERE rn <= 10
				GROUP BY node_id
			)`
		rttArgs = append(rttArgs, scope.AdminID)
	} else {
		rttQuery = `
			SELECT CAST(AVG(avg_rtt) AS INTEGER)
			FROM (
				SELECT node_id, AVG(rtt_ms) AS avg_rtt
				FROM (
					SELECT node_id, rtt_ms, ROW_NUMBER() OVER (PARTITION BY node_id ORDER BY measured_at DESC) AS rn
					FROM connection_metrics
					WHERE rtt_ms IS NOT NULL
				)
				WHERE rn <= 10
				GROUP BY node_id
			)`
	}

	err = d.Store.Read().QueryRowContext(ctx, rttQuery, rttArgs...).Scan(&avgRTT)
	if err == nil && avgRTT != nil {
		summary.AvgFleetRTTMs = avgRTT
	}

	// Count nodes with issues (offline, degraded, or have active alerts) - scope-filtered
	var issuesQuery string
	var issuesArgs []interface{}
	if !scope.IsSuper {
		issuesQuery = `
			SELECT COUNT(DISTINCT node_id)
			FROM (
				SELECT id AS node_id FROM nodes
				WHERE status IN ('offline', 'degraded') AND id IN (SELECT node_id FROM admin_scopes WHERE admin_id = ?)
				UNION
				SELECT target_id AS node_id FROM alerts
				WHERE state = 'active' AND target_type = 'node' AND target_id IN (SELECT node_id FROM admin_scopes WHERE admin_id = ?)
			)`
		issuesArgs = append(issuesArgs, scope.AdminID, scope.AdminID)
	} else {
		issuesQuery = `
			SELECT COUNT(DISTINCT node_id)
			FROM (
				SELECT id AS node_id FROM nodes WHERE status IN ('offline', 'degraded')
				UNION
				SELECT target_id AS node_id FROM alerts WHERE state = 'active' AND target_type = 'node'
			)`
	}

	err = d.Store.Read().QueryRowContext(ctx, issuesQuery, issuesArgs...).Scan(&summary.NodesWithIssues)
	if err != nil {
		// Non-fatal, just log and continue
		summary.NodesWithIssues = 0
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(summary)
}
