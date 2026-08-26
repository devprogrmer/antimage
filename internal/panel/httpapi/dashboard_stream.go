package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/amyrm/antimage/internal/panel/auth"
	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

// DashboardMetrics represents real-time dashboard data.
type DashboardMetrics struct {
	Timestamp      int64        `json:"timestamp"`
	ActiveUsers    int          `json:"active_users"`
	TotalSubjects  int          `json:"total_subjects"`
	NodesOnline    int          `json:"nodes_online"`
	NodesTotal     int          `json:"nodes_total"`
	TrafficTodayGB float64      `json:"traffic_today_gb"`
	BandwidthMbps  float64      `json:"bandwidth_mbps"`
	AlertsCount    int          `json:"alerts_count"`
	FrozenCount    int          `json:"frozen_count"`
	Nodes          []NodeMetric `json:"nodes"`
}

// NodeMetric represents per-node metrics.
type NodeMetric struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	CPUPercent float64 `json:"cpu_percent"`
	RAMPercent float64 `json:"ram_percent"`
	UserCount  int     `json:"user_count"`
}

// handleDashboardStream provides real-time metrics via SSE.
// GET /api/v1/dashboard/stream
//
// Every figure on this stream is scoped to the caller, the same way the rest of
// the dashboard family is: the aggregates below are counts of the caller's own
// subjects and nodes, not the fleet's. Before this was so, an authenticated
// tenant with no nodes at all received live fleet-wide totals every five
// seconds.
func (d Deps) handleDashboardStream(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	// authMiddleware would have rejected a request without this cookie, so the
	// failure below is unreachable in practice; it is here because the
	// re-validation loop needs the token and must not invent one.
	cookie, err := r.Cookie(auth.CookieName)
	if err != nil {
		WriteError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
		return
	}
	scope := rbac.ScopeOf(actor)

	// Set SSE headers.
	//
	// Deliberately no Access-Control-Allow-Origin: this stream is authenticated
	// by cookie, and answering "*" invites any origin to read one tenant's live
	// figures out of their browser. Same-origin is what the panel serves.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // defeat proxy buffering

	ctx := r.Context()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Send initial metrics immediately. The session was validated microseconds
	// ago by the middleware, so this first snapshot needs no re-check.
	if err := d.sendDashboardMetrics(ctx, w, scope); err != nil {
		return
	}

	// Send heartbeat and metrics every 5 seconds
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// A stream outlives the session that opened it unless it re-checks.
			// Every rejection reason collapses into one meaning here: stop.
			if _, err := d.Sessions.Validate(ctx, cookie.Value); err != nil {
				return
			}
			if err := d.sendDashboardMetrics(ctx, w, scope); err != nil {
				return
			}

			// Send heartbeat
			_, _ = fmt.Fprintf(w, "event: heartbeat\n")
			_, _ = fmt.Fprintf(w, "data: {\"timestamp\": %d}\n\n", time.Now().Unix())
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}
}

func (d Deps) sendDashboardMetrics(ctx context.Context, w http.ResponseWriter, sc rbac.Scope) error {
	metrics, err := d.collectDashboardMetrics(ctx, sc)
	if err != nil {
		return err
	}

	data, err := json.Marshal(metrics)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(w, "event: metrics\n")
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	return nil
}

// collectDashboardMetrics gathers one snapshot, restricted to what sc may see.
//
// Every query here filters on a shared scope predicate. None of them did
// before, and none of them could have: they were written against five columns
// that do not exist -- subjects.disabled, subjects.frozen, subjects.updated_at,
// nodes.last_seen and subject_services.node_id -- so the second query returned
// an error, the handler gave up, and the stream emitted an empty 200 for every
// caller since it was written. The column fixes are what make the scoping mean
// anything: correcting them without scoping would have turned a dead endpoint
// into a live fleet-wide disclosure.
func (d Deps) collectDashboardMetrics(ctx context.Context, sc rbac.Scope) (*DashboardMetrics, error) {
	metrics := &DashboardMetrics{
		Timestamp: time.Now().Unix(),
	}
	scopeArgs := store.ScopeArgs(sc)

	// Count total subjects
	err := d.Store.Read().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM subjects WHERE `+store.SubjectScopeSQL,
		scopeArgs...).Scan(&metrics.TotalSubjects)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	// Count active users (enabled, not frozen, not expired)
	now := time.Now().Unix()
	err = d.Store.Read().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM subjects
		WHERE enabled = 1 AND frozen_at IS NULL
		AND (expires_at IS NULL OR expires_at > ?)
		AND `+store.SubjectScopeSQL,
		append([]any{now}, scopeArgs...)...).Scan(&metrics.ActiveUsers)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	// Count frozen subjects
	err = d.Store.Read().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM subjects
		WHERE frozen_at IS NOT NULL AND `+store.SubjectScopeSQL,
		scopeArgs...).Scan(&metrics.FrozenCount)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	// Count alerts.
	//
	// An alert names its target, so it is scoped by whether the caller may see
	// that target -- through the very same two predicates, rather than a third
	// definition of ownership that could drift from them.
	err = d.Store.Read().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM alerts
		WHERE resolved_at IS NULL AND (
		  (alerts.target_type = 'node' AND EXISTS (
		      SELECT 1 FROM nodes
		       WHERE nodes.id = alerts.target_id AND `+store.NodeScopeSQL+`))
		  OR
		  (alerts.target_type = 'subject' AND EXISTS (
		      SELECT 1 FROM subjects
		       WHERE subjects.id = alerts.target_id AND `+store.SubjectScopeSQL+`)))`,
		append(append([]any{}, scopeArgs...), scopeArgs...)...).Scan(&metrics.AlertsCount)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		metrics.AlertsCount = 0
	}

	// Count nodes
	err = d.Store.Read().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM nodes WHERE `+store.NodeScopeSQL,
		scopeArgs...).Scan(&metrics.NodesTotal)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	// Count online nodes (heartbeat within last 60 seconds)
	cutoff := time.Now().Add(-60 * time.Second).Unix()
	err = d.Store.Read().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM nodes
		WHERE last_seen_at >= ? AND `+store.NodeScopeSQL,
		append([]any{cutoff}, scopeArgs...)...).Scan(&metrics.NodesOnline)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	// Calculate today's traffic.
	//
	// From the hourly rollups, which is what actually records traffic over a
	// window. The previous SUM(quota_used_bytes) was a lifetime counter, so it
	// would have reported every byte a subject had ever used as if it were
	// today's. Midnight is UTC, matching hour_start.
	todayStart := time.Now().UTC().Truncate(24 * time.Hour).Unix()
	var totalBytes sql.NullInt64
	err = d.Store.Read().QueryRowContext(ctx, `
		SELECT SUM(uplink_bytes + downlink_bytes)
		FROM usage_rollups_hourly
		JOIN subjects ON subjects.id = usage_rollups_hourly.subject_id
		WHERE hour_start >= ? AND `+store.SubjectScopeSQL,
		append([]any{todayStart}, scopeArgs...)...).Scan(&totalBytes)
	if err == nil && totalBytes.Valid {
		metrics.TrafficTodayGB = float64(totalBytes.Int64) / (1024 * 1024 * 1024)
	}

	// Get per-node metrics.
	//
	// user_count reaches subjects through services, because subject_services
	// records a service and only the service knows its node. It is scoped on
	// BOTH sides: a caller counting users on their own node must still not
	// learn how many of another tenant's subjects sit on it.
	rows, err := d.Store.Read().QueryContext(ctx, `
		SELECT nodes.id, nodes.name, nodes.last_seen_at,
		       (SELECT COUNT(*)
		          FROM subject_services ss
		          JOIN services sv ON sv.id = ss.service_id
		          JOIN subjects  ON subjects.id = ss.subject_id
		         WHERE sv.node_id = nodes.id AND `+store.SubjectScopeSQL+`) AS user_count
		FROM nodes
		WHERE `+store.NodeScopeSQL+`
		ORDER BY nodes.name`,
		append(append([]any{}, scopeArgs...), scopeArgs...)...)
	if err != nil {
		return metrics, nil // Return what we have so far
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var nm NodeMetric
		// last_seen_at is nullable: a node that has never called home has no
		// value at all. Scanning that into an int64 fails, and the continue
		// below would drop the node from the list entirely -- an enrolled node
		// would be missing from the dashboard rather than shown as offline.
		var lastSeen sql.NullInt64
		if err := rows.Scan(&nm.ID, &nm.Name, &lastSeen, &nm.UserCount); err != nil {
			continue
		}

		// Determine status based on last seen
		switch age := time.Now().Unix() - lastSeen.Int64; {
		case !lastSeen.Valid:
			nm.Status = "offline"
		case age < 60:
			nm.Status = "online"
		case age < 300:
			nm.Status = "degraded"
		default:
			nm.Status = "offline"
		}

		// Get actual CPU/RAM from node_metrics table
		var cpuPercent, memUsed, memTotal sql.NullFloat64
		_ = d.Store.Read().QueryRowContext(ctx, `
			SELECT cpu_percent, memory_used_bytes, memory_total_bytes
			FROM node_metrics
			WHERE node_id = ?
			ORDER BY timestamp DESC
			LIMIT 1`, nm.ID).Scan(&cpuPercent, &memUsed, &memTotal)

		if cpuPercent.Valid {
			nm.CPUPercent = cpuPercent.Float64
		}
		if memUsed.Valid && memTotal.Valid && memTotal.Float64 > 0 {
			nm.RAMPercent = (memUsed.Float64 / memTotal.Float64) * 100.0
		}

		metrics.Nodes = append(metrics.Nodes, nm)
	}
	if err := rows.Err(); err != nil {
		// A mid-iteration failure would silently return a truncated list as if
		// it were complete.
		return nil, err
	}

	return metrics, nil
}
