package httpapi

import (
	"database/sql"
	"net/http"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
	"strconv"
	"time"

	"github.com/amyrm/antimage/internal/panel/dashboard"
)

func (d Deps) handleDashboardOverview(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}

	stats, err := dashboard.GetStats(r.Context(), d.Store, *actor)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "failed to retrieve dashboard stats")
		return
	}

	resp := map[string]any{
		"nodes": map[string]any{
			"total":    stats.NodesTotal,
			"online":   stats.NodesOnline,
			"degraded": stats.NodesDegraded,
			"offline":  stats.NodesOffline,
		},
		"subjects": map[string]any{
			"total":   stats.SubjectsTotal,
			"active":  stats.SubjectsActive,
			"expired": stats.SubjectsExpired,
			"frozen":  stats.SubjectsFrozen,
		},
		"traffic_24h": map[string]any{
			"uplink_bytes":   stats.Traffic24hUplink,
			"downlink_bytes": stats.Traffic24hDownlink,
			"total_bytes":    stats.Traffic24hUplink + stats.Traffic24hDownlink,
		},
		"quota": map[string]any{
			"total_bytes":     stats.QuotaTotalBytes,
			"used_bytes":      stats.QuotaUsedBytes,
			"utilization_pct": stats.QuotaUtilizationPct,
		},
		"computed_at": stats.ComputedAt,
	}

	WriteJSON(w, http.StatusOK, resp)
}

func (d Deps) handleDashboardTrafficChart(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	// Rollups carry a subject_id, so the caller's subject scope is what decides
	// whose traffic this is. Unscoped, this charted every tenant's traffic for
	// anyone who asked.
	scopeArgs := store.ScopeArgs(rbac.ScopeOf(actor))

	period := r.URL.Query().Get("period")
	if period == "" {
		period = "24h"
	}

	var cutoff int64
	var granularity string
	var query string

	switch period {
	case "24h":
		cutoff = time.Now().Add(-24 * time.Hour).Unix()
		granularity = "hour"
		query = `
			SELECT hour_start, COALESCE(SUM(uplink_bytes),0), COALESCE(SUM(downlink_bytes),0)
			FROM usage_rollups_hourly
			JOIN subjects ON subjects.id = usage_rollups_hourly.subject_id
			WHERE hour_start >= ? AND ` + store.SubjectScopeSQL + `
			GROUP BY hour_start
			ORDER BY hour_start ASC
			LIMIT 24`
	case "7d":
		cutoff = time.Now().Add(-7 * 24 * time.Hour).Unix()
		granularity = "hour"
		query = `
			SELECT hour_start, COALESCE(SUM(uplink_bytes),0), COALESCE(SUM(downlink_bytes),0)
			FROM usage_rollups_hourly
			JOIN subjects ON subjects.id = usage_rollups_hourly.subject_id
			WHERE hour_start >= ? AND ` + store.SubjectScopeSQL + `
			GROUP BY hour_start
			ORDER BY hour_start ASC
			LIMIT 168`
	case "30d":
		cutoff = time.Now().Add(-30 * 24 * time.Hour).Unix()
		granularity = "day"
		query = `
			SELECT day_start, COALESCE(SUM(uplink_bytes),0), COALESCE(SUM(downlink_bytes),0)
			FROM usage_rollups_daily
			JOIN subjects ON subjects.id = usage_rollups_daily.subject_id
			WHERE day_start >= ? AND ` + store.SubjectScopeSQL + `
			GROUP BY day_start
			ORDER BY day_start ASC
			LIMIT 30`
	default:
		WriteError(w, http.StatusBadRequest, "bad_request", "period must be one of: 24h, 7d, 30d")
		return
	}

	rows, err := d.Store.Read().QueryContext(r.Context(), query,
		append([]any{cutoff}, scopeArgs...)...)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "failed to query traffic data")
		return
	}
	defer func() { _ = rows.Close() }()

	type DataPoint struct {
		Timestamp     int64 `json:"timestamp"`
		UplinkBytes   int64 `json:"uplink_bytes"`
		DownlinkBytes int64 `json:"downlink_bytes"`
	}

	dataPoints := []DataPoint{}
	for rows.Next() {
		var dp DataPoint
		if err := rows.Scan(&dp.Timestamp, &dp.UplinkBytes, &dp.DownlinkBytes); err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "failed to scan traffic data")
			return
		}
		dataPoints = append(dataPoints, dp)
	}
	if err := rows.Err(); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "failed to read traffic data")
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"period":      period,
		"granularity": granularity,
		"data_points": dataPoints,
	})
}

func (d Deps) handleDashboardTopUsers(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}

	limitStr := r.URL.Query().Get("limit")
	limit := 10
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil {
			if l < 1 || l > 50 {
				WriteError(w, http.StatusBadRequest, "bad_request", "limit must be between 1 and 50")
				return
			}
			limit = l
		} else {
			WriteError(w, http.StatusBadRequest, "bad_request", "limit must be an integer")
			return
		}
	}

	now := time.Now().Unix()

	// Aggregate total bytes per subject from usage rollups, join with subject info.
	query := `
		SELECT
			subjects.id,
			subjects.name,
			COALESCE(u.uplink_bytes, 0) + COALESCE(u.downlink_bytes, 0) AS total_bytes,
			subjects.quota_bytes,
			CASE
				WHEN subjects.frozen_at IS NOT NULL THEN 'frozen'
				WHEN subjects.expired_at IS NOT NULL THEN 'expired'
				WHEN subjects.expires_at IS NOT NULL AND subjects.expires_at <= ? THEN 'expired'
				WHEN subjects.enabled = 0 THEN 'disabled'
				ELSE 'active'
			END AS status
		FROM subjects
		LEFT JOIN (
			SELECT subject_id,
			       SUM(uplink_bytes)   AS uplink_bytes,
			       SUM(downlink_bytes) AS downlink_bytes
			FROM usage_rollups_daily
			GROUP BY subject_id
		) u ON u.subject_id = subjects.id
		-- Without this, the busiest subjects ACROSS EVERY TENANT were listed by
		-- name to anyone who asked. Of the four dashboard routes this was the
		-- most direct disclosure: not an aggregate but the customers themselves.
		WHERE ` + store.SubjectScopeSQL + `
		ORDER BY total_bytes DESC
		LIMIT ?`

	rows, err := d.Store.Read().QueryContext(r.Context(), query,
		append(append([]any{now}, store.ScopeArgs(rbac.ScopeOf(actor))...), limit)...)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "failed to query top users")
		return
	}
	defer func() { _ = rows.Close() }()

	type TopUser struct {
		SubjectID      int64    `json:"subject_id"`
		Name           string   `json:"name"`
		TotalBytes     int64    `json:"total_bytes"`
		QuotaBytes     *int64   `json:"quota_bytes"`
		UtilizationPct *float64 `json:"utilization_pct"`
		Status         string   `json:"status"`
	}

	topUsers := []TopUser{}
	for rows.Next() {
		var u TopUser
		var quotaBytes sql.NullInt64
		if err := rows.Scan(&u.SubjectID, &u.Name, &u.TotalBytes, &quotaBytes, &u.Status); err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "failed to scan top users")
			return
		}
		if quotaBytes.Valid {
			v := quotaBytes.Int64
			u.QuotaBytes = &v
			if v > 0 {
				pct := float64(u.TotalBytes) / float64(v) * 100.0
				u.UtilizationPct = &pct
			}
		}
		topUsers = append(topUsers, u)
	}
	if err := rows.Err(); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "failed to read top users")
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"top_users": topUsers,
	})
}
