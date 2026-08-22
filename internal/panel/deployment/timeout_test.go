package deployment

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

func TestEnforceTimeoutsAllAtOnce(t *testing.T) {
	st, cleanup := setupTimeoutTest(t)
	defer cleanup()

	ctx := context.Background()
	orchestrator := NewOrchestrator(st)

	// Create node and deployment that started 10 minutes ago
	nodeID := createTestNodeWithRevision(t, st, 1, "test-node")
	startTime := time.Now().Add(-10 * time.Minute)
	deploymentID := createInProgressDeployment(t, st, nodeID, StrategyAllAtOnce, startTime)

	// Run timeout enforcement with 5 minute timeout for all_at_once
	config := TimeoutConfig{
		AllAtOnce: 5 * time.Minute,
		Canary:    15 * time.Minute,
		Staged:    30 * time.Minute,
		Rolling:   45 * time.Minute,
	}

	err := orchestrator.EnforceTimeouts(ctx, config, time.Now())
	if err != nil {
		t.Fatalf("EnforceTimeouts: %v", err)
	}

	// Verify deployment marked as failed
	var status string
	var errorMsg sql.NullString
	var completedAt sql.NullInt64
	err = st.Read().QueryRowContext(ctx,
		`SELECT status, error, completed_at FROM deployments WHERE id = ?`,
		deploymentID).Scan(&status, &errorMsg, &completedAt)
	if err != nil {
		t.Fatalf("query deployment: %v", err)
	}

	if status != string(StatusFailed) {
		t.Errorf("expected status=failed, got %s", status)
	}

	if !errorMsg.Valid || errorMsg.String == "" {
		t.Error("expected error message to be set")
	}

	if !completedAt.Valid {
		t.Error("expected completed_at to be set")
	}

	// Verify node marked as failed
	var nodeStatus string
	err = st.Read().QueryRowContext(ctx,
		`SELECT status FROM deployment_node_status WHERE deployment_id = ? AND node_id = ?`,
		deploymentID, nodeID).Scan(&nodeStatus)
	if err != nil {
		t.Fatalf("query node status: %v", err)
	}

	if nodeStatus != string(NodeStatusFailed) {
		t.Errorf("expected node status=failed, got %s", nodeStatus)
	}
}

func TestEnforceTimeoutsCanaryNotTimedOut(t *testing.T) {
	st, cleanup := setupTimeoutTest(t)
	defer cleanup()

	ctx := context.Background()
	orchestrator := NewOrchestrator(st)

	// Create deployment that started 5 minutes ago (within 15 minute canary timeout)
	nodeID := createTestNodeWithRevision(t, st, 1, "test-node")
	startTime := time.Now().Add(-5 * time.Minute)
	deploymentID := createInProgressDeployment(t, st, nodeID, StrategyCanary, startTime)

	config := DefaultTimeoutConfig()

	err := orchestrator.EnforceTimeouts(ctx, config, time.Now())
	if err != nil {
		t.Fatalf("EnforceTimeouts: %v", err)
	}

	// Verify deployment still in_progress
	var status string
	err = st.Read().QueryRowContext(ctx,
		`SELECT status FROM deployments WHERE id = ?`,
		deploymentID).Scan(&status)
	if err != nil {
		t.Fatalf("query deployment: %v", err)
	}

	if status != string(StatusInProgress) {
		t.Errorf("expected status=in_progress, got %s", status)
	}
}

func TestEnforceTimeoutsMultipleStrategies(t *testing.T) {
	st, cleanup := setupTimeoutTest(t)
	defer cleanup()

	ctx := context.Background()
	orchestrator := NewOrchestrator(st)

	// Create nodes
	node1ID := createTestNodeWithRevision(t, st, 1, "node1")
	node2ID := createTestNodeWithRevision(t, st, 2, "node2")
	node3ID := createTestNodeWithRevision(t, st, 3, "node3")

	// Create deployments with different strategies and start times
	now := time.Now()

	// AllAtOnce: started 10 min ago, timeout 5 min -> TIMED OUT
	dep1ID := createInProgressDeployment(t, st, node1ID, StrategyAllAtOnce, now.Add(-10*time.Minute))

	// Canary: started 20 min ago, timeout 15 min -> TIMED OUT
	dep2ID := createInProgressDeployment(t, st, node2ID, StrategyCanary, now.Add(-20*time.Minute))

	// Rolling: started 30 min ago, timeout 45 min -> NOT TIMED OUT
	dep3ID := createInProgressDeployment(t, st, node3ID, StrategyRolling, now.Add(-30*time.Minute))

	config := DefaultTimeoutConfig()

	err := orchestrator.EnforceTimeouts(ctx, config, now)
	if err != nil {
		t.Fatalf("EnforceTimeouts: %v", err)
	}

	// Verify dep1 failed
	var dep1Status string
	err = st.Read().QueryRowContext(ctx,
		`SELECT status FROM deployments WHERE id = ?`, dep1ID).Scan(&dep1Status)
	if err != nil {
		t.Fatalf("query dep1: %v", err)
	}
	if dep1Status != string(StatusFailed) {
		t.Errorf("expected dep1 status=failed, got %s", dep1Status)
	}

	// Verify dep2 failed
	var dep2Status string
	err = st.Read().QueryRowContext(ctx,
		`SELECT status FROM deployments WHERE id = ?`, dep2ID).Scan(&dep2Status)
	if err != nil {
		t.Fatalf("query dep2: %v", err)
	}
	if dep2Status != string(StatusFailed) {
		t.Errorf("expected dep2 status=failed, got %s", dep2Status)
	}

	// Verify dep3 still in_progress
	var dep3Status string
	err = st.Read().QueryRowContext(ctx,
		`SELECT status FROM deployments WHERE id = ?`, dep3ID).Scan(&dep3Status)
	if err != nil {
		t.Fatalf("query dep3: %v", err)
	}
	if dep3Status != string(StatusInProgress) {
		t.Errorf("expected dep3 status=in_progress, got %s", dep3Status)
	}
}

func TestEnforceTimeoutsNoInProgress(t *testing.T) {
	st, cleanup := setupTimeoutTest(t)
	defer cleanup()

	ctx := context.Background()
	orchestrator := NewOrchestrator(st)

	// Create completed deployment
	nodeID := createTestNodeWithRevision(t, st, 1, "test-node")
	startTime := time.Now().Add(-10 * time.Minute)
	deploymentID := createCompletedDeployment(t, st, nodeID, StrategyAllAtOnce, startTime)

	config := DefaultTimeoutConfig()

	// Should not fail - no in_progress deployments
	err := orchestrator.EnforceTimeouts(ctx, config, time.Now())
	if err != nil {
		t.Fatalf("EnforceTimeouts: %v", err)
	}

	// Verify deployment still completed
	var status string
	err = st.Read().QueryRowContext(ctx,
		`SELECT status FROM deployments WHERE id = ?`,
		deploymentID).Scan(&status)
	if err != nil {
		t.Fatalf("query deployment: %v", err)
	}

	if status != string(StatusCompleted) {
		t.Errorf("expected status=completed, got %s", status)
	}
}

func TestEnforceTimeoutsPartiallyComplete(t *testing.T) {
	st, cleanup := setupTimeoutTest(t)
	defer cleanup()

	ctx := context.Background()
	orchestrator := NewOrchestrator(st)

	// Create nodes
	node1ID := createTestNodeWithRevision(t, st, 1, "node1")
	node2ID := createTestNodeWithRevision(t, st, 2, "node2")

	// Create deployment with mixed node status
	startTime := time.Now().Add(-50 * time.Minute)
	var deploymentID int64
	err := st.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`INSERT INTO deployments (revision_id, strategy, status, created_by, created_at, started_at)
			 VALUES (1, 'rolling', 'in_progress', 1, ?, ?)`,
			startTime.Unix(), startTime.Unix())
		if err != nil {
			return err
		}
		deploymentID, err = result.LastInsertId()
		if err != nil {
			return err
		}

		// Node 1: completed
		_, err = tx.ExecContext(ctx,
			`INSERT INTO deployment_node_status (deployment_id, node_id, status)
			 VALUES (?, ?, 'completed')`,
			deploymentID, node1ID)
		if err != nil {
			return err
		}

		// Node 2: applying (will be marked as failed)
		_, err = tx.ExecContext(ctx,
			`INSERT INTO deployment_node_status (deployment_id, node_id, status)
			 VALUES (?, ?, 'applying')`,
			deploymentID, node2ID)
		return err
	})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	config := DefaultTimeoutConfig()

	err = orchestrator.EnforceTimeouts(ctx, config, time.Now())
	if err != nil {
		t.Fatalf("EnforceTimeouts: %v", err)
	}

	// Verify deployment failed
	var status string
	err = st.Read().QueryRowContext(ctx,
		`SELECT status FROM deployments WHERE id = ?`,
		deploymentID).Scan(&status)
	if err != nil {
		t.Fatalf("query deployment: %v", err)
	}

	if status != string(StatusFailed) {
		t.Errorf("expected status=failed, got %s", status)
	}

	// Verify node1 stays completed
	var node1Status string
	err = st.Read().QueryRowContext(ctx,
		`SELECT status FROM deployment_node_status WHERE deployment_id = ? AND node_id = ?`,
		deploymentID, node1ID).Scan(&node1Status)
	if err != nil {
		t.Fatalf("query node1 status: %v", err)
	}

	if node1Status != string(NodeStatusCompleted) {
		t.Errorf("expected node1 status=completed, got %s", node1Status)
	}

	// Verify node2 marked as failed
	var node2Status string
	err = st.Read().QueryRowContext(ctx,
		`SELECT status FROM deployment_node_status WHERE deployment_id = ? AND node_id = ?`,
		deploymentID, node2ID).Scan(&node2Status)
	if err != nil {
		t.Fatalf("query node2 status: %v", err)
	}

	if node2Status != string(NodeStatusFailed) {
		t.Errorf("expected node2 status=failed, got %s", node2Status)
	}
}

func setupTimeoutTest(t *testing.T) (*store.Store, func()) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	ctx := context.Background()

	// Create admin and role for foreign key constraints
	err = st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO roles (id, name, permissions) VALUES (1, 'admin', '[]')`)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO admins (id, username, password_hash, role_id, status, created_at)
			 VALUES (1, 'test-admin', 'hash', 1, 'active', ?)`,
			time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("setup admin: %v", err)
	}

	cleanup := func() { st.Close() }
	return st, cleanup
}

func createInProgressDeployment(t *testing.T, st *store.Store, nodeID int64, strategy Strategy, startTime time.Time) int64 {
	t.Helper()
	ctx := context.Background()

	var deploymentID int64
	err := st.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`INSERT INTO deployments (revision_id, strategy, status, created_by, created_at, started_at)
			 VALUES (1, ?, 'in_progress', 1, ?, ?)`,
			strategy, startTime.Unix(), startTime.Unix())
		if err != nil {
			return err
		}
		deploymentID, err = result.LastInsertId()
		if err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO deployment_node_status (deployment_id, node_id, status)
			 VALUES (?, ?, 'applying')`,
			deploymentID, nodeID)
		return err
	})
	if err != nil {
		t.Fatalf("create in_progress deployment: %v", err)
	}

	return deploymentID
}

func createCompletedDeployment(t *testing.T, st *store.Store, nodeID int64, strategy Strategy, startTime time.Time) int64 {
	t.Helper()
	ctx := context.Background()

	var deploymentID int64
	err := st.Write(ctx, func(tx *sql.Tx) error {
		completedAt := startTime.Add(1 * time.Minute)
		result, err := tx.ExecContext(ctx,
			`INSERT INTO deployments (revision_id, strategy, status, created_by, created_at, started_at, completed_at)
			 VALUES (1, ?, 'completed', 1, ?, ?, ?)`,
			strategy, startTime.Unix(), startTime.Unix(), completedAt.Unix())
		if err != nil {
			return err
		}
		deploymentID, err = result.LastInsertId()
		if err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO deployment_node_status (deployment_id, node_id, status)
			 VALUES (?, ?, 'completed')`,
			deploymentID, nodeID)
		return err
	})
	if err != nil {
		t.Fatalf("create completed deployment: %v", err)
	}

	return deploymentID
}
