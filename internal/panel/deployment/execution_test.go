package deployment

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/testutil/storetest"
)

// TestExecuteDeploymentAllAtOnce verifies successful all_at_once execution
func TestExecuteDeploymentAllAtOnce(t *testing.T) {
	st, cleanup := setupExecutionTest(t)
	defer cleanup()

	ctx := context.Background()
	orchestrator := NewOrchestrator(st)

	nodeID := createTestNodeWithRevision(t, st, 1, "test-node")

	deploymentID, err := orchestrator.CreateDeployment(ctx, nodeID, StrategyAllAtOnce, 1)
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	// Execute deployment
	err = orchestrator.ExecuteDeployment(ctx, deploymentID)
	if err != nil {
		t.Errorf("execute deployment: %v", err)
	}

	// Verify deployment completed
	deployment, err := orchestrator.getDeployment(ctx, deploymentID)
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}

	if deployment.Status != StatusCompleted {
		t.Errorf("expected status=%s, got %s", StatusCompleted, deployment.Status)
	}
	if deployment.StartedAt == nil {
		t.Error("started_at should be set")
	}
	if deployment.CompletedAt == nil {
		t.Error("completed_at should be set")
	}
	if deployment.Error != "" {
		t.Errorf("unexpected error: %s", deployment.Error)
	}

	// Verify node status
	var nodeStatus string
	err = st.Read().QueryRowContext(ctx,
		`SELECT status FROM deployment_node_status WHERE deployment_id = ? AND node_id = ?`,
		deploymentID, nodeID).Scan(&nodeStatus)
	if err != nil {
		t.Fatalf("get node status: %v", err)
	}

	if nodeStatus != string(NodeStatusCompleted) {
		t.Errorf("expected node status=%s, got %s", NodeStatusCompleted, nodeStatus)
	}
}

// TestExecuteDeploymentCanarySuccess verifies successful canary deployment
func TestExecuteDeploymentCanarySuccess(t *testing.T) {
	st, cleanup := setupExecutionTest(t)
	defer cleanup()

	ctx := context.Background()
	orchestrator := NewOrchestrator(st)

	// Create two nodes
	node1 := createTestNodeWithRevision(t, st, 1, "node-1")
	node2 := createTestNodeWithRevision(t, st, 2, "node-2")

	// Mark nodes as online for health checks
	err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE nodes SET status = 'online' WHERE id IN (?, ?)`, node1, node2)
		return err
	})
	if err != nil {
		t.Fatalf("update node status: %v", err)
	}

	// Create deployment with both nodes
	var deploymentID int64
	err = st.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`INSERT INTO deployments (revision_id, strategy, status, created_by, created_at)
			 VALUES (1, ?, ?, 1, ?)`,
			StrategyCanary, StatusPending, time.Now().Unix())
		if err != nil {
			return err
		}
		deploymentID, err = result.LastInsertId()
		if err != nil {
			return err
		}

		// Add both nodes
		_, err = tx.ExecContext(ctx,
			`INSERT INTO deployment_node_status (deployment_id, node_id, status)
			 VALUES (?, ?, ?), (?, ?, ?)`,
			deploymentID, node1, NodeStatusPending,
			deploymentID, node2, NodeStatusPending)
		return err
	})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	// Execute - should deploy to canary first, wait, check health, then deploy to rest
	// This will take ~30s due to canary wait
	start := time.Now()
	err = orchestrator.ExecuteDeployment(ctx, deploymentID)
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("execute deployment: %v", err)
	}

	// Should have waited at least 30 seconds for canary
	if elapsed < 30*time.Second {
		t.Errorf("canary deployment completed too quickly: %v (expected >= 30s)", elapsed)
	}

	// Verify both nodes completed
	var completedCount int
	err = st.Read().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM deployment_node_status
		 WHERE deployment_id = ? AND status = ?`,
		deploymentID, NodeStatusCompleted).Scan(&completedCount)
	if err != nil {
		t.Fatalf("count completed nodes: %v", err)
	}

	if completedCount != 2 {
		t.Errorf("expected 2 completed nodes, got %d", completedCount)
	}
}

// TestExecuteDeploymentCanaryHealthFailure verifies canary deployment stops on health failure
func TestExecuteDeploymentCanaryHealthFailure(t *testing.T) {
	st, cleanup := setupExecutionTest(t)
	defer cleanup()

	ctx := context.Background()
	orchestrator := NewOrchestrator(st)

	// Create two nodes
	node1 := createTestNodeWithRevision(t, st, 1, "node-1")
	node2 := createTestNodeWithRevision(t, st, 2, "node-2")

	// Mark canary node as offline (will fail health check)
	err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE nodes SET status = 'offline' WHERE id = ?`, node1)
		return err
	})
	if err != nil {
		t.Fatalf("update node status: %v", err)
	}

	// Create deployment with both nodes
	var deploymentID int64
	err = st.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`INSERT INTO deployments (revision_id, strategy, status, created_by, created_at)
			 VALUES (1, ?, ?, 1, ?)`,
			StrategyCanary, StatusPending, time.Now().Unix())
		if err != nil {
			return err
		}
		deploymentID, err = result.LastInsertId()
		if err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO deployment_node_status (deployment_id, node_id, status)
			 VALUES (?, ?, ?), (?, ?, ?)`,
			deploymentID, node1, NodeStatusPending,
			deploymentID, node2, NodeStatusPending)
		return err
	})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	// Execute - should fail on canary health check
	err = orchestrator.ExecuteDeployment(ctx, deploymentID)
	if err == nil {
		t.Fatal("expected deployment to fail on canary health check")
	}
	if err.Error() != "canary node unhealthy after deployment" {
		t.Errorf("unexpected error: %v", err)
	}

	// Verify deployment rolled back (automatic rollback on failure)
	deployment, err := orchestrator.getDeployment(ctx, deploymentID)
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if deployment.Status != StatusRolledBack {
		t.Errorf("expected status=%s, got %s (error: %s)", StatusRolledBack, deployment.Status, deployment.Error)
	}

	// Verify canary node marked as rolled back (due to automatic rollback)
	var node1Status string
	err = st.Read().QueryRowContext(ctx,
		`SELECT status FROM deployment_node_status WHERE deployment_id = ? AND node_id = ?`,
		deploymentID, node1).Scan(&node1Status)
	if err != nil {
		t.Fatalf("get node1 status: %v", err)
	}
	// Node status could be failed or rolled_back depending on rollback sequence
	if node1Status != string(NodeStatusFailed) && node1Status != string(NodeStatusRolledBack) {
		t.Errorf("expected canary status=%s or %s, got %s", NodeStatusFailed, NodeStatusRolledBack, node1Status)
	}

	// Verify second node NOT deployed (still pending)
	var node2Status string
	err = st.Read().QueryRowContext(ctx,
		`SELECT status FROM deployment_node_status WHERE deployment_id = ? AND node_id = ?`,
		deploymentID, node2).Scan(&node2Status)
	if err != nil {
		t.Fatalf("get node2 status: %v", err)
	}
	if node2Status != string(NodeStatusPending) {
		t.Errorf("expected node2 status=%s (not deployed), got %s", NodeStatusPending, node2Status)
	}
}

// TestExecuteDeploymentInvalidState verifies deployment cannot execute in non-pending state
func TestExecuteDeploymentInvalidState(t *testing.T) {
	st, cleanup := setupExecutionTest(t)
	defer cleanup()

	ctx := context.Background()
	orchestrator := NewOrchestrator(st)

	nodeID := createTestNodeWithRevision(t, st, 1, "test-node")

	// Create deployment
	deploymentID, err := orchestrator.CreateDeployment(ctx, nodeID, StrategyAllAtOnce, 1)
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	// Mark as in_progress manually
	err = st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE deployments SET status = ? WHERE id = ?`,
			StatusInProgress, deploymentID)
		return err
	})
	if err != nil {
		t.Fatalf("update status: %v", err)
	}

	// Try to execute - should fail
	err = orchestrator.ExecuteDeployment(ctx, deploymentID)
	if err == nil {
		t.Fatal("expected error when executing non-pending deployment")
	}
	if err.Error() != "deployment 1 is not pending (status: in_progress)" {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestExecuteDeploymentRollingWithHealthChecks verifies rolling deployment with per-node health checks
func TestExecuteDeploymentRollingWithHealthChecks(t *testing.T) {
	st, cleanup := setupExecutionTest(t)
	defer cleanup()

	ctx := context.Background()
	orchestrator := NewOrchestrator(st)

	// Create three nodes
	node1 := createTestNodeWithRevision(t, st, 1, "node-1")
	node2 := createTestNodeWithRevision(t, st, 2, "node-2")
	node3 := createTestNodeWithRevision(t, st, 3, "node-3")

	// Mark all as online
	err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE nodes SET status = 'online' WHERE id IN (?, ?, ?)`,
			node1, node2, node3)
		return err
	})
	if err != nil {
		t.Fatalf("update node status: %v", err)
	}

	// Create deployment with all nodes
	var deploymentID int64
	err = st.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`INSERT INTO deployments (revision_id, strategy, status, created_by, created_at)
			 VALUES (1, ?, ?, 1, ?)`,
			StrategyRolling, StatusPending, time.Now().Unix())
		if err != nil {
			return err
		}
		deploymentID, err = result.LastInsertId()
		if err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO deployment_node_status (deployment_id, node_id, status)
			 VALUES (?, ?, ?), (?, ?, ?), (?, ?, ?)`,
			deploymentID, node1, NodeStatusPending,
			deploymentID, node2, NodeStatusPending,
			deploymentID, node3, NodeStatusPending)
		return err
	})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	// Execute - should deploy one at a time with 10s wait between
	start := time.Now()
	err = orchestrator.ExecuteDeployment(ctx, deploymentID)
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("execute deployment: %v", err)
	}

	// Should have waited at least 20 seconds (10s between each of 3 nodes)
	if elapsed < 20*time.Second {
		t.Errorf("rolling deployment completed too quickly: %v (expected >= 20s)", elapsed)
	}

	// Verify all nodes completed
	var completedCount int
	err = st.Read().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM deployment_node_status
		 WHERE deployment_id = ? AND status = ?`,
		deploymentID, NodeStatusCompleted).Scan(&completedCount)
	if err != nil {
		t.Fatalf("count completed nodes: %v", err)
	}

	if completedCount != 3 {
		t.Errorf("expected 3 completed nodes, got %d", completedCount)
	}
}

// TestExecuteDeploymentRollingNodeFailure verifies rolling deployment stops on node failure
func TestExecuteDeploymentRollingNodeFailure(t *testing.T) {
	st, cleanup := setupExecutionTest(t)
	defer cleanup()

	ctx := context.Background()
	orchestrator := NewOrchestrator(st)

	// Create three nodes
	node1 := createTestNodeWithRevision(t, st, 1, "node-1")
	node2 := createTestNodeWithRevision(t, st, 2, "node-2")
	node3 := createTestNodeWithRevision(t, st, 3, "node-3")

	// Mark first node as online, second as offline (will fail health check)
	err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE nodes SET status = 'online' WHERE id = ?`, node1)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE nodes SET status = 'offline' WHERE id = ?`, node2)
		return err
	})
	if err != nil {
		t.Fatalf("update node status: %v", err)
	}

	// Create deployment
	var deploymentID int64
	err = st.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`INSERT INTO deployments (revision_id, strategy, status, created_by, created_at)
			 VALUES (1, ?, ?, 1, ?)`,
			StrategyRolling, StatusPending, time.Now().Unix())
		if err != nil {
			return err
		}
		deploymentID, err = result.LastInsertId()
		if err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO deployment_node_status (deployment_id, node_id, status)
			 VALUES (?, ?, ?), (?, ?, ?), (?, ?, ?)`,
			deploymentID, node1, NodeStatusPending,
			deploymentID, node2, NodeStatusPending,
			deploymentID, node3, NodeStatusPending)
		return err
	})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	// Execute - should fail on second node health check
	err = orchestrator.ExecuteDeployment(ctx, deploymentID)
	if err == nil {
		t.Fatal("expected deployment to fail on node 2 health check")
	}

	// Verify deployment failed
	deployment, err := orchestrator.getDeployment(ctx, deploymentID)
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if deployment.Status != StatusFailed {
		t.Errorf("expected status=%s, got %s", StatusFailed, deployment.Status)
	}

	// Verify first node completed
	var node1Status string
	err = st.Read().QueryRowContext(ctx,
		`SELECT status FROM deployment_node_status WHERE deployment_id = ? AND node_id = ?`,
		deploymentID, node1).Scan(&node1Status)
	if err != nil {
		t.Fatalf("get node1 status: %v", err)
	}
	if node1Status != string(NodeStatusCompleted) {
		t.Errorf("expected node1 status=%s, got %s", NodeStatusCompleted, node1Status)
	}

	// Verify third node NOT deployed (still pending)
	var node3Status string
	err = st.Read().QueryRowContext(ctx,
		`SELECT status FROM deployment_node_status WHERE deployment_id = ? AND node_id = ?`,
		deploymentID, node3).Scan(&node3Status)
	if err != nil {
		t.Fatalf("get node3 status: %v", err)
	}
	if node3Status != string(NodeStatusPending) {
		t.Errorf("expected node3 status=%s (not deployed), got %s", NodeStatusPending, node3Status)
	}
}

// Helper functions

func setupExecutionTest(t *testing.T) (*store.Store, func()) {
	t.Helper()
	st, err := storetest.OpenCopy(filepath.Join(t.TempDir(), "test.db"))
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

func createTestNodeWithRevision(t *testing.T, st *store.Store, nodeID int64, name string) int64 {
	t.Helper()
	ctx := context.Background()
	err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (id, name, address, status, created_at)
			 VALUES (?, ?, '10.0.0.1:8443', 'online', ?)`,
			nodeID, name, time.Now().Unix())
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO node_revisions (node_id, revision, created_at, actor_type, actor_label, reason, doc_sha256)
			 VALUES (?, 1, ?, 'system', 'test', 'initial', 'sha256-test')`,
			nodeID, time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	return nodeID
}
