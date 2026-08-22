package observability

import (
	"context"
	"fmt"
	"log"
	"time"
)

// AlertTypeEnforcementFailure is raised when a node has persistent reconciliation failures.
const AlertTypeEnforcementFailure AlertType = "enforcement_failure"

// EnforcementFailureThreshold is the number of consecutive failed reconciliations
// that triggers a critical alert.
const EnforcementFailureThreshold = 3

// checkEnforcementFailures creates alerts for nodes with persistent reconciliation failures.
// A node triggers an alert if failed_reconcile_streak >= threshold.
func (sw *Sweeper) checkEnforcementFailures(ctx context.Context, now time.Time) error {
	// Query nodes with failed reconciliation streaks
	rows, err := sw.store.Read().QueryContext(ctx, `
		SELECT id, name, status, failed_reconcile_streak, last_reconcile_at, last_reconcile_error
		FROM nodes
		WHERE failed_reconcile_streak >= ?
		  AND status NOT IN ('disabled', 'pending', 'enrolling')
	`, EnforcementFailureThreshold)
	if err != nil {
		return fmt.Errorf("query nodes with enforcement failures: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var nodeID int64
		var nodeName, status string
		var failedStreak int
		var lastReconcileAt int64
		var lastError string

		if err := rows.Scan(&nodeID, &nodeName, &status, &failedStreak, &lastReconcileAt, &lastError); err != nil {
			log.Printf("[observability] scan enforcement failure node: %v", err)
			continue
		}

		lastReconcile := time.Unix(lastReconcileAt, 0).UTC()
		timeSinceFailure := now.Sub(lastReconcile)

		// Severity based on streak length
		severity := SeverityWarning
		if failedStreak >= 5 {
			severity = SeverityCritical
		}

		metadata := map[string]interface{}{
			"node_name":               nodeName,
			"status":                  status,
			"failed_reconcile_streak": failedStreak,
			"last_reconcile_at":       lastReconcile.Format(time.RFC3339),
			"time_since_failure":      timeSinceFailure.String(),
			"last_error":              lastError,
		}

		alert := Alert{
			AlertType:      AlertTypeEnforcementFailure,
			Severity:       severity,
			TargetType:     TargetNode,
			TargetID:       nodeID,
			DedupKey:       fmt.Sprintf("enforcement_failure:node:%d", nodeID),
			ThresholdValue: fmt.Sprintf("%d consecutive failures", EnforcementFailureThreshold),
			CurrentValue:   fmt.Sprintf("%d consecutive failures", failedStreak),
			Metadata:       metadata,
		}

		if _, _, err := CreateOrUpdateAlert(ctx, sw.store, alert, now); err != nil {
			log.Printf("[observability] create/update enforcement failure alert for node %d: %v", nodeID, err)
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate nodes with enforcement failures: %w", err)
	}

	// Resolve alerts for nodes that have recovered (streak < threshold)
	recoveredRows, err := sw.store.Read().QueryContext(ctx, `
		SELECT id
		FROM nodes
		WHERE failed_reconcile_streak < ?
		   OR failed_reconcile_streak IS NULL
	`, EnforcementFailureThreshold)
	if err != nil {
		return fmt.Errorf("query recovered nodes: %w", err)
	}
	defer func() { _ = recoveredRows.Close() }()

	for recoveredRows.Next() {
		var nodeID int64
		if err := recoveredRows.Scan(&nodeID); err != nil {
			log.Printf("[observability] scan recovered node: %v", err)
			continue
		}

		dedupKey := fmt.Sprintf("enforcement_failure:node:%d", nodeID)
		if err := ResolveAlert(ctx, sw.store, dedupKey, now); err != nil {
			log.Printf("[observability] resolve enforcement failure alert for node %d: %v", nodeID, err)
		}
	}

	if err := recoveredRows.Err(); err != nil {
		return fmt.Errorf("iterate recovered nodes: %w", err)
	}

	return nil
}
