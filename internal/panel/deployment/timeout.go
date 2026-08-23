package deployment

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"
)

// Default timeout values per strategy
const (
	DefaultTimeoutAllAtOnce = 5 * time.Minute
	DefaultTimeoutCanary    = 15 * time.Minute
	DefaultTimeoutStaged    = 30 * time.Minute
	DefaultTimeoutRolling   = 45 * time.Minute
)

// TimeoutConfig holds timeout settings for deployment execution
type TimeoutConfig struct {
	AllAtOnce time.Duration
	Canary    time.Duration
	Staged    time.Duration
	Rolling   time.Duration
}

// DefaultTimeoutConfig returns reasonable timeout defaults
func DefaultTimeoutConfig() TimeoutConfig {
	return TimeoutConfig{
		AllAtOnce: DefaultTimeoutAllAtOnce,
		Canary:    DefaultTimeoutCanary,
		Staged:    DefaultTimeoutStaged,
		Rolling:   DefaultTimeoutRolling,
	}
}

// EnforceTimeouts finds deployments that have exceeded their timeout and marks them as failed.
// This handles deployments that are stuck due to network issues, slow nodes, or other problems.
func (o *Orchestrator) EnforceTimeouts(ctx context.Context, config TimeoutConfig, now time.Time) error {
	// Query all in-progress deployments
	rows, err := o.store.Read().QueryContext(ctx,
		`SELECT d.id, d.strategy, d.started_at
		 FROM deployments d
		 WHERE d.status = ? AND d.started_at IS NOT NULL`,
		StatusInProgress)
	if err != nil {
		return fmt.Errorf("query in-progress deployments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var timedOutDeployments []struct {
		id       int64
		strategy Strategy
		elapsed  time.Duration
	}

	for rows.Next() {
		var id int64
		var strategy string
		var startedAt int64
		if err := rows.Scan(&id, &strategy, &startedAt); err != nil {
			return fmt.Errorf("scan deployment: %w", err)
		}

		startTime := time.Unix(startedAt, 0)
		elapsed := now.Sub(startTime)

		// Check if deployment has exceeded timeout for its strategy
		var timeout time.Duration
		switch Strategy(strategy) {
		case StrategyAllAtOnce:
			timeout = config.AllAtOnce
		case StrategyCanary:
			timeout = config.Canary
		case StrategyStaged:
			timeout = config.Staged
		case StrategyRolling:
			timeout = config.Rolling
		default:
			log.Printf("Unknown strategy %s for deployment %d, using default timeout", strategy, id)
			timeout = DefaultTimeoutAllAtOnce
		}

		if elapsed > timeout {
			timedOutDeployments = append(timedOutDeployments, struct {
				id       int64
				strategy Strategy
				elapsed  time.Duration
			}{id, Strategy(strategy), elapsed})
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate deployments: %w", err)
	}

	if len(timedOutDeployments) == 0 {
		return nil
	}

	log.Printf("Found %d timed-out deployments, marking as failed", len(timedOutDeployments))

	// Mark timed-out deployments as failed
	for _, dep := range timedOutDeployments {
		err := o.store.Write(ctx, func(tx *sql.Tx) error {
			errorMsg := fmt.Sprintf("deployment timed out after %s (strategy: %s)",
				dep.elapsed.Round(time.Second), dep.strategy)

			_, err := tx.ExecContext(ctx,
				`UPDATE deployments SET status = ?, completed_at = ?, error = ? WHERE id = ?`,
				StatusFailed, now.Unix(), errorMsg, dep.id)
			if err != nil {
				return fmt.Errorf("update deployment %d: %w", dep.id, err)
			}

			// Mark all pending or applying nodes as failed
			_, err = tx.ExecContext(ctx,
				`UPDATE deployment_node_status
				 SET status = ?
				 WHERE deployment_id = ?
				 AND status IN (?, ?)`,
				NodeStatusFailed, dep.id, NodeStatusPending, NodeStatusApplying)
			if err != nil {
				return fmt.Errorf("update node status for deployment %d: %w", dep.id, err)
			}

			return nil
		})
		if err != nil {
			log.Printf("Failed to mark deployment %d as timed out: %v", dep.id, err)
			continue
		}

		log.Printf("Marked deployment %d as failed due to timeout (elapsed: %s, strategy: %s)",
			dep.id, dep.elapsed.Round(time.Second), dep.strategy)
	}

	return nil
}
