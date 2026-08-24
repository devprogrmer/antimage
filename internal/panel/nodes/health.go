package nodes

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

// HealthMetrics represents real-time health data from a node.
type HealthMetrics struct {
	NodeID            int64
	Timestamp         time.Time
	CPUPercent        *float64
	MemoryUsedBytes   *int64
	MemoryTotalBytes  *int64
	DiskUsedBytes     *int64
	DiskTotalBytes    *int64
	NetworkRxBytes    *int64
	NetworkTxBytes    *int64
	ActiveConnections *int
	LatencyMS         *int
}

// HealthStatus calculates the overall health status from metrics.
type HealthStatus struct {
	Status         Status
	CPUHealthy     bool
	MemoryHealthy  bool
	DiskHealthy    bool
	LatencyHealthy bool
	LastHeartbeat  time.Time
	Message        string
}

// HealthThresholds defines when a node is considered degraded.
type HealthThresholds struct {
	CPUPercentWarning        float64
	CPUPercentCritical       float64
	MemoryPercentWarning     float64
	MemoryPercentCritical    float64
	DiskPercentWarning       float64
	DiskPercentCritical      float64
	LatencyMSWarning         int
	LatencyMSCritical        int
	HeartbeatTimeoutDegraded time.Duration
	HeartbeatTimeoutOffline  time.Duration
}

// DefaultHealthThresholds returns production-ready thresholds.
func DefaultHealthThresholds() HealthThresholds {
	return HealthThresholds{
		CPUPercentWarning:        75.0,
		CPUPercentCritical:       90.0,
		MemoryPercentWarning:     80.0,
		MemoryPercentCritical:    95.0,
		DiskPercentWarning:       85.0,
		DiskPercentCritical:      95.0,
		LatencyMSWarning:         500,
		LatencyMSCritical:        2000,
		HeartbeatTimeoutDegraded: 2 * time.Minute,
		HeartbeatTimeoutOffline:  5 * time.Minute,
	}
}

// RecordMetrics stores health metrics for a node.
func RecordMetrics(ctx context.Context, s *store.Store, metrics HealthMetrics) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO node_metrics (
				node_id, timestamp, cpu_percent, memory_used_bytes, memory_total_bytes,
				disk_used_bytes, disk_total_bytes, network_rx_bytes, network_tx_bytes,
				active_connections, latency_ms
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			metrics.NodeID,
			metrics.Timestamp.Unix(),
			metrics.CPUPercent,
			metrics.MemoryUsedBytes,
			metrics.MemoryTotalBytes,
			metrics.DiskUsedBytes,
			metrics.DiskTotalBytes,
			metrics.NetworkRxBytes,
			metrics.NetworkTxBytes,
			metrics.ActiveConnections,
			metrics.LatencyMS,
		)
		return err
	})
}

// GetLatestMetrics retrieves the most recent health metrics for a node.
func GetLatestMetrics(ctx context.Context, s *store.Store, nodeID int64) (*HealthMetrics, error) {
	row := s.Read().QueryRowContext(ctx, `
		SELECT node_id, timestamp, cpu_percent, memory_used_bytes, memory_total_bytes,
		       disk_used_bytes, disk_total_bytes, network_rx_bytes, network_tx_bytes,
		       active_connections, latency_ms
		FROM node_metrics
		WHERE node_id = ?
		ORDER BY timestamp DESC
		LIMIT 1
	`, nodeID)

	var m HealthMetrics
	var ts int64
	err := row.Scan(
		&m.NodeID,
		&ts,
		&m.CPUPercent,
		&m.MemoryUsedBytes,
		&m.MemoryTotalBytes,
		&m.DiskUsedBytes,
		&m.DiskTotalBytes,
		&m.NetworkRxBytes,
		&m.NetworkTxBytes,
		&m.ActiveConnections,
		&m.LatencyMS,
	)
	if err == sql.ErrNoRows {
		return nil, nil // No metrics recorded yet
	}
	if err != nil {
		return nil, err
	}

	m.Timestamp = time.Unix(ts, 0)
	return &m, nil
}

// GetMetricsHistory retrieves historical metrics for a node within a time range.
func GetMetricsHistory(ctx context.Context, s *store.Store, nodeID int64, from, to time.Time, limit int) ([]HealthMetrics, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}

	rows, err := s.Read().QueryContext(ctx, `
		SELECT node_id, timestamp, cpu_percent, memory_used_bytes, memory_total_bytes,
		       disk_used_bytes, disk_total_bytes, network_rx_bytes, network_tx_bytes,
		       active_connections, latency_ms
		FROM node_metrics
		WHERE node_id = ? AND timestamp >= ? AND timestamp <= ?
		ORDER BY timestamp DESC
		LIMIT ?
	`, nodeID, from.Unix(), to.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var metrics []HealthMetrics
	for rows.Next() {
		var m HealthMetrics
		var ts int64
		err := rows.Scan(
			&m.NodeID,
			&ts,
			&m.CPUPercent,
			&m.MemoryUsedBytes,
			&m.MemoryTotalBytes,
			&m.DiskUsedBytes,
			&m.DiskTotalBytes,
			&m.NetworkRxBytes,
			&m.NetworkTxBytes,
			&m.ActiveConnections,
			&m.LatencyMS,
		)
		if err != nil {
			return nil, err
		}
		m.Timestamp = time.Unix(ts, 0)
		metrics = append(metrics, m)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	return metrics, nil
}

// CalculateHealthStatus determines node health from metrics and heartbeat.
func CalculateHealthStatus(metrics *HealthMetrics, lastSeenAt *time.Time, thresholds HealthThresholds) HealthStatus {
	now := time.Now()
	status := HealthStatus{
		Status:         StatusOnline,
		CPUHealthy:     true,
		MemoryHealthy:  true,
		DiskHealthy:    true,
		LatencyHealthy: true,
		Message:        "all systems operational",
	}

	if lastSeenAt != nil {
		status.LastHeartbeat = *lastSeenAt
	}

	// Check heartbeat first
	if lastSeenAt == nil {
		status.Status = StatusOffline
		status.Message = "no heartbeat received"
		return status
	}

	timeSinceHeartbeat := now.Sub(*lastSeenAt)
	if timeSinceHeartbeat > thresholds.HeartbeatTimeoutOffline {
		status.Status = StatusOffline
		status.Message = fmt.Sprintf("no heartbeat for %v", timeSinceHeartbeat.Round(time.Second))
		return status
	}

	if timeSinceHeartbeat > thresholds.HeartbeatTimeoutDegraded {
		status.Status = StatusDegraded
		status.Message = fmt.Sprintf("heartbeat delayed (%v)", timeSinceHeartbeat.Round(time.Second))
	}

	// If no metrics, can't assess resource health
	if metrics == nil {
		if status.Status == StatusOnline {
			status.Message = "online, metrics unavailable"
		}
		return status
	}

	// Check CPU
	if metrics.CPUPercent != nil {
		if *metrics.CPUPercent >= thresholds.CPUPercentCritical {
			status.Status = StatusDegraded
			status.CPUHealthy = false
			status.Message = fmt.Sprintf("CPU critical: %.1f%%", *metrics.CPUPercent)
		} else if *metrics.CPUPercent >= thresholds.CPUPercentWarning {
			if status.Status == StatusOnline {
				status.Status = StatusDegraded
				status.Message = fmt.Sprintf("CPU high: %.1f%%", *metrics.CPUPercent)
			}
			status.CPUHealthy = false
		}
	}

	// Check memory
	if metrics.MemoryUsedBytes != nil && metrics.MemoryTotalBytes != nil && *metrics.MemoryTotalBytes > 0 {
		memPercent := float64(*metrics.MemoryUsedBytes) / float64(*metrics.MemoryTotalBytes) * 100
		if memPercent >= thresholds.MemoryPercentCritical {
			status.Status = StatusDegraded
			status.MemoryHealthy = false
			status.Message = fmt.Sprintf("memory critical: %.1f%%", memPercent)
		} else if memPercent >= thresholds.MemoryPercentWarning {
			if status.Status == StatusOnline && status.Message == "all systems operational" {
				status.Status = StatusDegraded
				status.Message = fmt.Sprintf("memory high: %.1f%%", memPercent)
			}
			status.MemoryHealthy = false
		}
	}

	// Check disk
	if metrics.DiskUsedBytes != nil && metrics.DiskTotalBytes != nil && *metrics.DiskTotalBytes > 0 {
		diskPercent := float64(*metrics.DiskUsedBytes) / float64(*metrics.DiskTotalBytes) * 100
		if diskPercent >= thresholds.DiskPercentCritical {
			status.Status = StatusDegraded
			status.DiskHealthy = false
			status.Message = fmt.Sprintf("disk critical: %.1f%%", diskPercent)
		} else if diskPercent >= thresholds.DiskPercentWarning {
			if status.Status == StatusOnline && status.Message == "all systems operational" {
				status.Status = StatusDegraded
				status.Message = fmt.Sprintf("disk high: %.1f%%", diskPercent)
			}
			status.DiskHealthy = false
		}
	}

	// Check latency
	if metrics.LatencyMS != nil {
		if *metrics.LatencyMS >= thresholds.LatencyMSCritical {
			status.Status = StatusDegraded
			status.LatencyHealthy = false
			status.Message = fmt.Sprintf("latency critical: %dms", *metrics.LatencyMS)
		} else if *metrics.LatencyMS >= thresholds.LatencyMSWarning {
			if status.Status == StatusOnline && status.Message == "all systems operational" {
				status.Status = StatusDegraded
				status.Message = fmt.Sprintf("latency high: %dms", *metrics.LatencyMS)
			}
			status.LatencyHealthy = false
		}
	}

	return status
}

// RecordNodeEvent logs a node lifecycle event.
func RecordNodeEvent(ctx context.Context, s *store.Store, nodeID int64, eventType, severity string, details map[string]interface{}, adminID *int64) error {
	var detailsJSON []byte
	var err error
	if details != nil {
		detailsJSON, err = json.Marshal(details)
		if err != nil {
			return fmt.Errorf("marshal details: %w", err)
		}
	}

	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO node_events (node_id, event_type, timestamp, details, admin_id, severity)
			VALUES (?, ?, ?, ?, ?, ?)
		`, nodeID, eventType, time.Now().Unix(), detailsJSON, adminID, severity)
		return err
	})
}

// PruneOldMetrics removes metrics older than the retention period.
// Returns the number of rows deleted.
func PruneOldMetrics(ctx context.Context, s *store.Store, retentionDays int) (int64, error) {
	cutoff := time.Now().AddDate(0, 0, -retentionDays).Unix()

	var deleted int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `DELETE FROM node_metrics WHERE timestamp < ?`, cutoff)
		if err != nil {
			return err
		}
		deleted, err = result.RowsAffected()
		return err
	})

	return deleted, err
}
