package deployment

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"
)

// RecoverStaleDeployments finds deployments stuck in in_progress state
// and marks them as failed. This handles process crashes or restarts.
func (o *Orchestrator) RecoverStaleDeployments(ctx context.Context) error {
	var staleDeployments []int64

	rows, err := o.store.Read().QueryContext(ctx,
		`SELECT id FROM deployments WHERE status = ?`,
		StatusInProgress)
	if err != nil {
		return fmt.Errorf("query stale deployments: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("scan deployment id: %w", err)
		}
		staleDeployments = append(staleDeployments, id)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate deployments: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close rows: %w", err)
	}

	if len(staleDeployments) == 0 {
		return nil
	}

	log.Printf("Found %d stale in-progress deployments, marking as failed", len(staleDeployments))

	for _, deploymentID := range staleDeployments {
		err := o.store.Write(ctx, func(tx *sql.Tx) error {
			now := time.Now().Unix()

			// Mark deployment as failed
			_, err := tx.ExecContext(ctx,
				`UPDATE deployments SET status = ?, completed_at = ? WHERE id = ?`,
				StatusFailed, now, deploymentID)
			if err != nil {
				return fmt.Errorf("update deployment %d: %w", deploymentID, err)
			}

			// Mark all pending or applying nodes as failed
			_, err = tx.ExecContext(ctx,
				`UPDATE deployment_node_status
				 SET status = ?
				 WHERE deployment_id = ?
				 AND status IN (?, ?)`,
				NodeStatusFailed, deploymentID, NodeStatusPending, NodeStatusApplying)
			if err != nil {
				return fmt.Errorf("update node status for deployment %d: %w", deploymentID, err)
			}

			return nil
		})
		if err != nil {
			log.Printf("Failed to recover deployment %d: %v", deploymentID, err)
			continue
		}

		log.Printf("Marked deployment %d as failed due to interruption", deploymentID)
	}

	return nil
}
