// Package metrics provides Prometheus metric collection for the panel.
package metrics

import (
	"context"
	"database/sql"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/amyrm/antimage/internal/panel/store"
)

// Collector implements prometheus.Collector for antimage panel metrics.
type Collector struct {
	store *store.Store

	nodesTotal           *prometheus.GaugeVec
	heartbeatAgeSeconds  prometheus.Gauge
	reconnectTotal       prometheus.Gauge
	reconcileDuration    prometheus.Gauge
	failedReconcileNodes prometheus.Gauge
}

// NewCollector creates a Prometheus collector that queries the panel database.
func NewCollector(s *store.Store) *Collector {
	return &Collector{
		store: s,
		nodesTotal: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "antimage_nodes_total",
				Help: "Total number of nodes by status",
			},
			[]string{"status"},
		),
		heartbeatAgeSeconds: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "antimage_node_heartbeat_age_seconds_max",
				Help: "Maximum time since last heartbeat across all online nodes",
			},
		),
		reconnectTotal: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "antimage_node_reconnect_total",
				Help: "Total number of reconnections across all nodes",
			},
		),
		reconcileDuration: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "antimage_node_reconcile_duration_ms_avg",
				Help: "Average reconciliation duration across nodes with recent reconciliations",
			},
		),
		failedReconcileNodes: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "antimage_nodes_with_failed_reconcile_streak",
				Help: "Number of nodes with active failed reconciliation streaks",
			},
		),
	}
}

// Describe implements prometheus.Collector.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	c.nodesTotal.Describe(ch)
	c.heartbeatAgeSeconds.Describe(ch)
	c.reconnectTotal.Describe(ch)
	c.reconcileDuration.Describe(ch)
	c.failedReconcileNodes.Describe(ch)
}

// Collect implements prometheus.Collector.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Reset gauges
	c.nodesTotal.Reset()

	// Query node counts by status
	rows, err := c.store.Read().QueryContext(ctx,
		`SELECT status, COUNT(*) FROM nodes GROUP BY status`)
	if err == nil {
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var status string
			var count int
			if err := rows.Scan(&status, &count); err == nil {
				c.nodesTotal.WithLabelValues(status).Set(float64(count))
			}
		}
		_ = rows.Close()
		_ = rows.Err()
	}

	// Query max heartbeat age for online nodes
	var maxAge sql.NullInt64
	err = c.store.Read().QueryRowContext(ctx, `
		SELECT MAX(unixepoch() - last_seen_at)
		FROM nodes
		WHERE status IN ('online', 'degraded') AND last_seen_at IS NOT NULL`).Scan(&maxAge)
	if err == nil && maxAge.Valid {
		c.heartbeatAgeSeconds.Set(float64(maxAge.Int64))
	}

	// Query total reconnects across all nodes
	var totalReconnects sql.NullInt64
	err = c.store.Read().QueryRowContext(ctx,
		`SELECT SUM(reconnect_count) FROM nodes`).Scan(&totalReconnects)
	if err == nil && totalReconnects.Valid {
		c.reconnectTotal.Set(float64(totalReconnects.Int64))
	}

	// Query average reconcile duration (nodes with non-null duration)
	var avgDuration sql.NullFloat64
	err = c.store.Read().QueryRowContext(ctx, `
		SELECT AVG(last_reconcile_duration_ms)
		FROM nodes
		WHERE last_reconcile_duration_ms IS NOT NULL`).Scan(&avgDuration)
	if err == nil && avgDuration.Valid {
		c.reconcileDuration.Set(avgDuration.Float64)
	}

	// Query count of nodes with failed reconcile streaks > 0
	var failedCount int
	err = c.store.Read().QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM nodes
		WHERE failed_reconcile_streak > 0`).Scan(&failedCount)
	if err == nil {
		c.failedReconcileNodes.Set(float64(failedCount))
	}

	// Collect all metrics
	c.nodesTotal.Collect(ch)
	c.heartbeatAgeSeconds.Collect(ch)
	c.reconnectTotal.Collect(ch)
	c.reconcileDuration.Collect(ch)
	c.failedReconcileNodes.Collect(ch)
}
