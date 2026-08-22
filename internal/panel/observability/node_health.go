package observability

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"
)

// AlertTypeNodeOffline is raised when a node has not sent a heartbeat for too long.
const AlertTypeNodeOffline AlertType = "node_offline"

// NodeOfflineThreshold is the duration after which a node is considered offline.
// This matches the panel's node sweeper threshold (default 5 minutes).
const NodeOfflineThreshold = 5 * time.Minute

// checkNodeHealth creates alerts for nodes that have gone offline.
// A node is considered offline if:
// - last_seen_at is older than NodeOfflineThreshold
// - OR status = 'offline'
func (sw *Sweeper) checkNodeHealth(ctx context.Context, now time.Time) error {
	cutoffTime := now.Add(-NodeOfflineThreshold).Unix()

	// Query nodes that are offline (by status or stale heartbeat)
	rows, err := sw.store.Read().QueryContext(ctx, `
		SELECT id, name, status, last_seen_at, last_reconcile_at
		FROM nodes
		WHERE status = 'offline'
		   OR (status NOT IN ('offline', 'disabled') AND last_seen_at < ?)
	`, cutoffTime)
	if err != nil {
		return fmt.Errorf("query offline nodes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var nodeID int64
		var nodeName, status string
		var lastSeenAt, lastReconcileAt sql.NullInt64

		if err := rows.Scan(&nodeID, &nodeName, &status, &lastSeenAt, &lastReconcileAt); err != nil {
			log.Printf("[observability] scan offline node: %v", err)
			continue
		}

		// Calculate how long the node has been offline
		var offlineDuration time.Duration
		if lastSeenAt.Valid {
			lastSeen := time.Unix(lastSeenAt.Int64, 0).UTC()
			offlineDuration = now.Sub(lastSeen)
		} else {
			// Never seen, consider offline since enrollment
			offlineDuration = 24 * time.Hour // Placeholder
		}

		severity := SeverityWarning
		if offlineDuration > 30*time.Minute {
			severity = SeverityCritical
		}

		metadata := map[string]interface{}{
			"node_name":        nodeName,
			"status":           status,
			"offline_duration": offlineDuration.String(),
		}
		if lastSeenAt.Valid {
			metadata["last_seen_at"] = time.Unix(lastSeenAt.Int64, 0).UTC().Format(time.RFC3339)
		}
		if lastReconcileAt.Valid {
			metadata["last_reconcile_at"] = time.Unix(lastReconcileAt.Int64, 0).UTC().Format(time.RFC3339)
		}

		alert := Alert{
			AlertType:      AlertTypeNodeOffline,
			Severity:       severity,
			TargetType:     TargetNode,
			TargetID:       nodeID,
			DedupKey:       fmt.Sprintf("node_offline:node:%d", nodeID),
			ThresholdValue: NodeOfflineThreshold.String(),
			CurrentValue:   offlineDuration.String(),
			Metadata:       metadata,
		}

		if _, _, err := CreateOrUpdateAlert(ctx, sw.store, alert, now); err != nil {
			log.Printf("[observability] create/update node offline alert for node %d: %v", nodeID, err)
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate offline nodes: %w", err)
	}

	// Resolve alerts for nodes that are back online
	onlineRows, err := sw.store.Read().QueryContext(ctx, `
		SELECT id
		FROM nodes
		WHERE status IN ('online', 'degraded')
		  AND last_seen_at >= ?
	`, cutoffTime)
	if err != nil {
		return fmt.Errorf("query online nodes: %w", err)
	}
	defer func() { _ = onlineRows.Close() }()

	for onlineRows.Next() {
		var nodeID int64
		if err := onlineRows.Scan(&nodeID); err != nil {
			log.Printf("[observability] scan online node: %v", err)
			continue
		}

		dedupKey := fmt.Sprintf("node_offline:node:%d", nodeID)
		if err := ResolveAlert(ctx, sw.store, dedupKey, now); err != nil {
			log.Printf("[observability] resolve node offline alert for node %d: %v", nodeID, err)
		}
	}

	if err := onlineRows.Err(); err != nil {
		return fmt.Errorf("iterate online nodes: %w", err)
	}

	return nil
}
