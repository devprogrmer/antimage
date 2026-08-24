package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
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
func (d Deps) handleDashboardStream(w http.ResponseWriter, r *http.Request) {
	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ctx := r.Context()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Send initial metrics immediately
	if err := d.sendDashboardMetrics(ctx, w); err != nil {
		return
	}

	// Send heartbeat and metrics every 5 seconds
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := d.sendDashboardMetrics(ctx, w); err != nil {
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

func (d Deps) sendDashboardMetrics(ctx context.Context, w http.ResponseWriter) error {
	metrics, err := d.collectDashboardMetrics(ctx)
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

func (d Deps) collectDashboardMetrics(ctx context.Context) (*DashboardMetrics, error) {
	metrics := &DashboardMetrics{
		Timestamp: time.Now().Unix(),
	}

	// Count total subjects
	err := d.Store.Read().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM subjects
	`).Scan(&metrics.TotalSubjects)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	// Count active users (not disabled, not frozen, not expired)
	now := time.Now().Unix()
	err = d.Store.Read().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM subjects
		WHERE disabled = 0 AND frozen = 0
		AND (expires_at IS NULL OR expires_at > ?)
	`, now).Scan(&metrics.ActiveUsers)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	// Count frozen subjects
	err = d.Store.Read().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM subjects WHERE frozen = 1
	`).Scan(&metrics.FrozenCount)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	// Count alerts
	err = d.Store.Read().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM alerts WHERE resolved_at IS NULL
	`).Scan(&metrics.AlertsCount)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		metrics.AlertsCount = 0
	}

	// Count nodes
	err = d.Store.Read().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM nodes
	`).Scan(&metrics.NodesTotal)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	// Count online nodes (heartbeat within last 60 seconds)
	cutoff := time.Now().Add(-60 * time.Second).Unix()
	err = d.Store.Read().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM nodes WHERE last_seen >= ?
	`, cutoff).Scan(&metrics.NodesOnline)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	// Calculate today's traffic
	todayStart := time.Now().Truncate(24 * time.Hour).Unix()
	var totalBytes sql.NullInt64
	err = d.Store.Read().QueryRowContext(ctx, `
		SELECT SUM(quota_used_bytes) FROM subjects
		WHERE updated_at >= ?
	`, todayStart).Scan(&totalBytes)
	if err == nil && totalBytes.Valid {
		metrics.TrafficTodayGB = float64(totalBytes.Int64) / (1024 * 1024 * 1024)
	}

	// Get per-node metrics
	rows, err := d.Store.Read().QueryContext(ctx, `
		SELECT n.id, n.name, n.last_seen,
		       (SELECT COUNT(*) FROM subject_services ss WHERE ss.node_id = n.id) as user_count
		FROM nodes n
		ORDER BY n.name
	`)
	if err != nil {
		return metrics, nil // Return what we have so far
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var nm NodeMetric
		var lastSeen int64
		if err := rows.Scan(&nm.ID, &nm.Name, &lastSeen, &nm.UserCount); err != nil {
			continue
		}

		// Determine status based on last seen
		if time.Now().Unix()-lastSeen < 60 {
			nm.Status = "online"
		} else if time.Now().Unix()-lastSeen < 300 {
			nm.Status = "degraded"
		} else {
			nm.Status = "offline"
		}

		// TODO: Get actual CPU/RAM from node metrics table
		nm.CPUPercent = 0
		nm.RAMPercent = 0

		metrics.Nodes = append(metrics.Nodes, nm)
	}
	if err := rows.Err(); err != nil {
		// A mid-iteration failure would silently return a truncated list as if
		// it were complete.
		return nil, err
	}

	return metrics, nil
}

// handleDashboardMetrics provides a snapshot of dashboard metrics.
// GET /api/v1/dashboard/metrics
