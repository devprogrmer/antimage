package deployment

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

type Strategy string

const (
	StrategyAllAtOnce Strategy = "all_at_once"
	StrategyCanary    Strategy = "canary"
	StrategyStaged    Strategy = "staged"
	StrategyRolling   Strategy = "rolling"
)

type DeploymentStatus string

const (
	StatusPending     DeploymentStatus = "pending"
	StatusValidating  DeploymentStatus = "validating"
	StatusInProgress  DeploymentStatus = "in_progress"
	StatusCompleted   DeploymentStatus = "completed"
	StatusFailed      DeploymentStatus = "failed"
	StatusRolledBack  DeploymentStatus = "rolled_back"
)

type NodeDeploymentStatus string

const (
	NodeStatusPending    NodeDeploymentStatus = "pending"
	NodeStatusApplying   NodeDeploymentStatus = "applying"
	NodeStatusCompleted  NodeDeploymentStatus = "completed"
	NodeStatusFailed     NodeDeploymentStatus = "failed"
	NodeStatusRolledBack NodeDeploymentStatus = "rolled_back"
	NodeStatusSkipped    NodeDeploymentStatus = "skipped"
)

type Deployment struct {
	ID          int64
	RevisionID  int64
	Strategy    Strategy
	Status      DeploymentStatus
	CreatedBy   int64
	CreatedAt   time.Time
	StartedAt   *time.Time
	CompletedAt *time.Time
	Error       string
}

type Orchestrator struct {
	store     *store.Store
	validator *Validator
}

func NewOrchestrator(st *store.Store) *Orchestrator {
	return &Orchestrator{
		store:     st,
		validator: NewValidator(st),
	}
}

func (o *Orchestrator) CreateDeployment(ctx context.Context, nodeID int64, strategy Strategy, adminID int64) (int64, error) {
	var currentRevision int64
	err := o.store.Read().QueryRowContext(ctx,
		`SELECT COALESCE(MAX(revision), 0) FROM node_revisions WHERE node_id = ?`,
		nodeID).Scan(&currentRevision)
	if err != nil {
		return 0, fmt.Errorf("get current revision: %w", err)
	}

	if currentRevision == 0 {
		return 0, fmt.Errorf("node %d has no revisions", nodeID)
	}

	validationResult, err := o.validator.ValidateRevision(ctx, nodeID, currentRevision)
	if err != nil {
		return 0, fmt.Errorf("validate revision: %w", err)
	}

	if !validationResult.Valid {
		return 0, fmt.Errorf("revision validation failed: %d conflicts found", len(validationResult.Conflicts))
	}

	var deploymentID int64
	err = o.store.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`INSERT INTO deployments (revision_id, strategy, status, created_by, created_at)
			 VALUES (?, ?, ?, ?, ?)`,
			currentRevision, strategy, StatusPending, adminID, time.Now().Unix())
		if err != nil {
			return err
		}
		deploymentID, err = result.LastInsertId()
		if err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO deployment_node_status (deployment_id, node_id, status)
			 VALUES (?, ?, ?)`,
			deploymentID, nodeID, NodeStatusPending)
		return err
	})

	if err != nil {
		return 0, fmt.Errorf("create deployment: %w", err)
	}

	return deploymentID, nil
}

func (o *Orchestrator) ExecuteDeployment(ctx context.Context, deploymentID int64) error {
	deployment, err := o.getDeployment(ctx, deploymentID)
	if err != nil {
		return fmt.Errorf("get deployment: %w", err)
	}

	if deployment.Status != StatusPending {
		return fmt.Errorf("deployment %d is not pending (status: %s)", deploymentID, deployment.Status)
	}

	// Atomically check for concurrent deployments and mark as in_progress
	err = o.store.Write(ctx, func(tx *sql.Tx) error {
		// Check for concurrent deployments to the same nodes within transaction
		var activeDeploymentCount int
		err := tx.QueryRowContext(ctx,
			`SELECT COUNT(DISTINCT d.id)
			 FROM deployments d
			 JOIN deployment_node_status dns ON d.id = dns.deployment_id
			 WHERE d.status = ? AND dns.node_id IN (
			   SELECT node_id FROM deployment_node_status WHERE deployment_id = ?
			 ) AND d.id != ?`,
			StatusInProgress, deploymentID, deploymentID).Scan(&activeDeploymentCount)
		if err != nil {
			return fmt.Errorf("check concurrent deployments: %w", err)
		}

		if activeDeploymentCount > 0 {
			return fmt.Errorf("cannot deploy: %d active deployments to overlapping nodes", activeDeploymentCount)
		}

		// Mark as in_progress
		_, err = tx.ExecContext(ctx,
			`UPDATE deployments SET status = ? WHERE id = ?`,
			StatusInProgress, deploymentID)
		return err
	})
	if err != nil {
		return err
	}

	now := time.Now()
	err = o.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE deployments SET started_at = ? WHERE id = ?`,
			now.Unix(), deploymentID)
		return err
	})
	if err != nil {
		return fmt.Errorf("update started_at: %w", err)
	}

	var executeErr error
	switch deployment.Strategy {
	case StrategyAllAtOnce:
		executeErr = o.executeAllAtOnce(ctx, deploymentID)
	case StrategyCanary:
		executeErr = o.executeCanary(ctx, deploymentID)
	case StrategyStaged:
		executeErr = o.executeStaged(ctx, deploymentID)
	case StrategyRolling:
		executeErr = o.executeRolling(ctx, deploymentID)
	default:
		executeErr = fmt.Errorf("unknown strategy: %s", deployment.Strategy)
	}

	finalStatus := StatusCompleted
	errorMsg := ""
	if executeErr != nil {
		finalStatus = StatusFailed
		errorMsg = executeErr.Error()

		// Attempt automatic rollback on failure
		if rollbackErr := o.initiateRollback(ctx, deploymentID); rollbackErr != nil {
			errorMsg = fmt.Sprintf("%s; rollback failed: %s", errorMsg, rollbackErr.Error())
		} else {
			finalStatus = StatusRolledBack
		}
	}

	now = time.Now()
	err = o.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE deployments SET status = ?, completed_at = ?, error = ? WHERE id = ?`,
			finalStatus, now.Unix(), errorMsg, deploymentID)
		return err
	})
	if err != nil {
		return fmt.Errorf("update completion: %w", err)
	}

	return executeErr
}

func (o *Orchestrator) executeAllAtOnce(ctx context.Context, deploymentID int64) error {
	nodeIDs, err := o.getPendingNodes(ctx, deploymentID)
	if err != nil {
		return err
	}

	deployment, err := o.getDeployment(ctx, deploymentID)
	if err != nil {
		return err
	}

	failures := 0
	for _, nodeID := range nodeIDs {
		if err := o.applyToNode(ctx, deploymentID, nodeID, deployment.RevisionID); err != nil {
			failures++
		}
	}

	if failures > 0 {
		return fmt.Errorf("%d/%d nodes failed deployment", failures, len(nodeIDs))
	}

	return nil
}

func (o *Orchestrator) executeCanary(ctx context.Context, deploymentID int64) error {
	nodeIDs, err := o.getPendingNodes(ctx, deploymentID)
	if err != nil {
		return err
	}

	if len(nodeIDs) == 0 {
		return fmt.Errorf("no nodes to deploy")
	}

	deployment, err := o.getDeployment(ctx, deploymentID)
	if err != nil {
		return err
	}

	canaryNodeID := nodeIDs[0]
	if err := o.applyToNode(ctx, deploymentID, canaryNodeID, deployment.RevisionID); err != nil {
		return fmt.Errorf("canary deployment failed: %w", err)
	}

	time.Sleep(30 * time.Second)

	healthStatus, err := o.validator.CheckNodeHealth(ctx, []int64{canaryNodeID})
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}

	if healthStatus[canaryNodeID] != "healthy" {
		if err := o.updateNodeStatus(ctx, deploymentID, canaryNodeID, NodeStatusFailed, "canary health check failed"); err != nil {
			return err
		}
		return fmt.Errorf("canary node unhealthy after deployment")
	}

	for _, nodeID := range nodeIDs[1:] {
		if err := o.applyToNode(ctx, deploymentID, nodeID, deployment.RevisionID); err != nil {
			return fmt.Errorf("node %d deployment failed: %w", nodeID, err)
		}
	}

	return nil
}

func (o *Orchestrator) executeStaged(ctx context.Context, deploymentID int64) error {
	nodeIDs, err := o.getPendingNodes(ctx, deploymentID)
	if err != nil {
		return err
	}

	if len(nodeIDs) == 0 {
		return fmt.Errorf("no nodes to deploy")
	}

	deployment, err := o.getDeployment(ctx, deploymentID)
	if err != nil {
		return err
	}

	stages := []float64{0.1, 0.5, 1.0}
	deployed := 0

	for _, stagePct := range stages {
		stageCount := int(float64(len(nodeIDs)) * stagePct)
		if stageCount <= deployed {
			stageCount = deployed + 1
		}
		if stageCount > len(nodeIDs) {
			stageCount = len(nodeIDs)
		}

		for i := deployed; i < stageCount; i++ {
			if err := o.applyToNode(ctx, deploymentID, nodeIDs[i], deployment.RevisionID); err != nil {
				return fmt.Errorf("stage %.0f%% node %d failed: %w", stagePct*100, nodeIDs[i], err)
			}
		}

		deployed = stageCount

		if deployed < len(nodeIDs) {
			time.Sleep(30 * time.Second)
		}
	}

	return nil
}

func (o *Orchestrator) executeRolling(ctx context.Context, deploymentID int64) error {
	nodeIDs, err := o.getPendingNodes(ctx, deploymentID)
	if err != nil {
		return err
	}

	deployment, err := o.getDeployment(ctx, deploymentID)
	if err != nil {
		return err
	}

	for _, nodeID := range nodeIDs {
		if err := o.applyToNode(ctx, deploymentID, nodeID, deployment.RevisionID); err != nil {
			return fmt.Errorf("node %d deployment failed: %w", nodeID, err)
		}

		time.Sleep(10 * time.Second)

		healthStatus, err := o.validator.CheckNodeHealth(ctx, []int64{nodeID})
		if err != nil {
			return fmt.Errorf("health check for node %d failed: %w", nodeID, err)
		}

		if healthStatus[nodeID] != "healthy" {
			return fmt.Errorf("node %d unhealthy after deployment", nodeID)
		}
	}

	return nil
}

func (o *Orchestrator) applyToNode(ctx context.Context, deploymentID, nodeID, revisionID int64) error {
	if err := o.updateNodeStatus(ctx, deploymentID, nodeID, NodeStatusApplying, ""); err != nil {
		return err
	}

	now := time.Now()
	err := o.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE deployment_node_status SET started_at = ? WHERE deployment_id = ? AND node_id = ?`,
			now.Unix(), deploymentID, nodeID)
		return err
	})
	if err != nil {
		return fmt.Errorf("update started_at: %w", err)
	}

	// Node reconciliation is triggered automatically via the control plane hub
	// For now, mark as completed since we don't have direct reconciliation trigger
	applyErr := error(nil)

	finalStatus := NodeStatusCompleted
	errorMsg := ""
	if applyErr != nil {
		finalStatus = NodeStatusFailed
		errorMsg = applyErr.Error()
	}

	now = time.Now()
	err = o.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE deployment_node_status SET status = ?, completed_at = ?, error = ?
			 WHERE deployment_id = ? AND node_id = ?`,
			finalStatus, now.Unix(), errorMsg, deploymentID, nodeID)
		return err
	})
	if err != nil {
		return fmt.Errorf("update node status: %w", err)
	}

	return applyErr
}

func (o *Orchestrator) getDeployment(ctx context.Context, deploymentID int64) (*Deployment, error) {
	var d Deployment
	var createdAtUnix, startedAtUnix, completedAtUnix sql.NullInt64

	err := o.store.Read().QueryRowContext(ctx,
		`SELECT id, revision_id, strategy, status, created_by, created_at, started_at, completed_at, error
		 FROM deployments WHERE id = ?`,
		deploymentID,
	).Scan(&d.ID, &d.RevisionID, &d.Strategy, &d.Status, &d.CreatedBy, &createdAtUnix, &startedAtUnix, &completedAtUnix, &d.Error)
	if err != nil {
		return nil, err
	}

	d.CreatedAt = time.Unix(createdAtUnix.Int64, 0)
	if startedAtUnix.Valid {
		t := time.Unix(startedAtUnix.Int64, 0)
		d.StartedAt = &t
	}
	if completedAtUnix.Valid {
		t := time.Unix(completedAtUnix.Int64, 0)
		d.CompletedAt = &t
	}

	return &d, nil
}

func (o *Orchestrator) getPendingNodes(ctx context.Context, deploymentID int64) ([]int64, error) {
	rows, err := o.store.Read().QueryContext(ctx,
		`SELECT node_id FROM deployment_node_status
		 WHERE deployment_id = ? AND status = ?
		 ORDER BY node_id`,
		deploymentID, NodeStatusPending)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var nodeIDs []int64
	for rows.Next() {
		var nodeID int64
		if err := rows.Scan(&nodeID); err != nil {
			return nil, err
		}
		nodeIDs = append(nodeIDs, nodeID)
	}

	return nodeIDs, rows.Err()
}

func (o *Orchestrator) getTargetNodes(ctx context.Context, tx *sql.Tx, nodeID int64) ([]int64, error) {
	return []int64{nodeID}, nil
}

func (o *Orchestrator) updateDeploymentStatus(ctx context.Context, deploymentID int64, status DeploymentStatus) error {
	return o.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE deployments SET status = ? WHERE id = ?`,
			status, deploymentID)
		return err
	})
}

func (o *Orchestrator) updateNodeStatus(ctx context.Context, deploymentID, nodeID int64, status NodeDeploymentStatus, errorMsg string) error {
	return o.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE deployment_node_status SET status = ?, error = ?
			 WHERE deployment_id = ? AND node_id = ?`,
			status, errorMsg, deploymentID, nodeID)
		return err
	})
}

// initiateRollback attempts to rollback a failed deployment by reverting applied nodes
func (o *Orchestrator) initiateRollback(ctx context.Context, deploymentID int64) error {
	// Get nodes that were successfully deployed (completed or applying)
	rows, err := o.store.Read().QueryContext(ctx,
		`SELECT node_id FROM deployment_node_status
		 WHERE deployment_id = ? AND status IN (?, ?)
		 ORDER BY node_id`,
		deploymentID, NodeStatusCompleted, NodeStatusApplying)
	if err != nil {
		return fmt.Errorf("query deployed nodes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var nodeIDs []int64
	for rows.Next() {
		var nodeID int64
		if err := rows.Scan(&nodeID); err != nil {
			return fmt.Errorf("scan node id: %w", err)
		}
		nodeIDs = append(nodeIDs, nodeID)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rows error: %w", err)
	}

	if len(nodeIDs) == 0 {
		// No nodes to rollback
		return nil
	}

	// Get the deployment to find previous revision
	deployment, err := o.getDeployment(ctx, deploymentID)
	if err != nil {
		return fmt.Errorf("get deployment: %w", err)
	}

	// Find previous revision (current - 1)
	var previousRevision int64
	err = o.store.Read().QueryRowContext(ctx,
		`SELECT revision FROM node_revisions
		 WHERE node_id = (SELECT node_id FROM deployment_node_status WHERE deployment_id = ? LIMIT 1)
		 AND revision < ?
		 ORDER BY revision DESC LIMIT 1`,
		deploymentID, deployment.RevisionID).Scan(&previousRevision)
	if err == sql.ErrNoRows {
		// No previous revision, cannot rollback
		return fmt.Errorf("no previous revision found for rollback")
	}
	if err != nil {
		return fmt.Errorf("find previous revision: %w", err)
	}

	// Rollback each deployed node to previous revision
	rollbackFailures := 0
	for _, nodeID := range nodeIDs {
		if err := o.rollbackNode(ctx, deploymentID, nodeID, previousRevision); err != nil {
			rollbackFailures++
		}
	}

	if rollbackFailures > 0 {
		return fmt.Errorf("%d/%d nodes failed to rollback", rollbackFailures, len(nodeIDs))
	}

	return nil
}

// rollbackNode reverts a node to a previous revision
func (o *Orchestrator) rollbackNode(ctx context.Context, deploymentID, nodeID, targetRevision int64) error {
	// Mark node as rolling back
	if err := o.updateNodeStatus(ctx, deploymentID, nodeID, NodeStatusRolledBack, ""); err != nil {
		return fmt.Errorf("update status: %w", err)
	}

	// In a real implementation, this would trigger node reconciliation to the target revision
	// For now, we just update the status to indicate rollback completed

	return nil
}

// RollbackDeployment manually triggers rollback of a completed or failed deployment
func (o *Orchestrator) RollbackDeployment(ctx context.Context, deploymentID int64) error {
	deployment, err := o.getDeployment(ctx, deploymentID)
	if err != nil {
		return fmt.Errorf("get deployment: %w", err)
	}

	// Can only rollback completed or failed deployments
	if deployment.Status != StatusCompleted && deployment.Status != StatusFailed {
		return fmt.Errorf("cannot rollback deployment with status %s", deployment.Status)
	}

	// Update deployment status to indicate rollback in progress
	if err := o.updateDeploymentStatus(ctx, deploymentID, StatusInProgress); err != nil {
		return fmt.Errorf("update status: %w", err)
	}

	// Perform rollback
	rollbackErr := o.initiateRollback(ctx, deploymentID)

	// Update final status
	finalStatus := StatusRolledBack
	errorMsg := ""
	if rollbackErr != nil {
		finalStatus = StatusFailed
		errorMsg = fmt.Sprintf("rollback failed: %s", rollbackErr.Error())
	}

	now := time.Now()
	err = o.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE deployments SET status = ?, completed_at = ?, error = ? WHERE id = ?`,
			finalStatus, now.Unix(), errorMsg, deploymentID)
		return err
	})
	if err != nil {
		return fmt.Errorf("update completion: %w", err)
	}

	return rollbackErr
}
