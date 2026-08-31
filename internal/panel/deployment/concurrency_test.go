package deployment

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/testutil/storetest"
)

// TestConcurrentDeploymentToSameNode verifies concurrent deployments to the same node are rejected
func TestConcurrentDeploymentToSameNode(t *testing.T) {
	st, cleanup := setupConcurrencyTest(t)
	defer cleanup()

	ctx := context.Background()
	orchestrator := NewOrchestrator(st)

	// Create node and mark as online for health checks (will add delay)
	nodeID := int64(1)
	err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (id, name, address, status, created_at)
			 VALUES (?, 'test-node', '10.0.0.1:8443', 'online', ?)`,
			nodeID, time.Now().Unix())
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

	// Create two deployments for the same node using canary strategy (has 30s delay)
	var deployment1, deployment2 int64
	err = st.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`INSERT INTO deployments (revision_id, strategy, status, created_by, created_at)
			 VALUES (1, ?, ?, 1, ?)`,
			StrategyCanary, StatusPending, time.Now().Unix())
		if err != nil {
			return err
		}
		deployment1, err = result.LastInsertId()
		if err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO deployment_node_status (deployment_id, node_id, status)
			 VALUES (?, ?, ?)`,
			deployment1, nodeID, NodeStatusPending)
		if err != nil {
			return err
		}

		result, err = tx.ExecContext(ctx,
			`INSERT INTO deployments (revision_id, strategy, status, created_by, created_at)
			 VALUES (1, ?, ?, 1, ?)`,
			StrategyCanary, StatusPending, time.Now().Unix())
		if err != nil {
			return err
		}
		deployment2, err = result.LastInsertId()
		if err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO deployment_node_status (deployment_id, node_id, status)
			 VALUES (?, ?, ?)`,
			deployment2, nodeID, NodeStatusPending)
		return err
	})
	if err != nil {
		t.Fatalf("create deployments: %v", err)
	}

	// Start first deployment in background (will take 30s due to canary wait)
	var wg sync.WaitGroup
	var exec1Err error
	wg.Add(1)
	go func() {
		defer wg.Done()
		exec1Err = orchestrator.ExecuteDeployment(ctx, deployment1)
	}()

	// Give first deployment time to acquire in_progress status
	time.Sleep(200 * time.Millisecond)

	// Try to start second deployment - should fail
	exec2Err := orchestrator.ExecuteDeployment(ctx, deployment2)
	if exec2Err == nil {
		t.Fatal("expected error when starting concurrent deployment to same node")
	}
	if exec2Err.Error() != "cannot deploy: 1 active deployments to overlapping nodes" {
		t.Errorf("unexpected error: %v", exec2Err)
	}

	// Wait for first deployment to complete
	wg.Wait()

	if exec1Err != nil {
		t.Errorf("deployment 1 failed: %v", exec1Err)
	}

	// Verify first deployment completed
	d1, err := orchestrator.getDeployment(ctx, deployment1)
	if err != nil {
		t.Fatalf("get deployment 1: %v", err)
	}
	if d1.Status != StatusCompleted {
		t.Errorf("expected deployment 1 status=%s, got %s", StatusCompleted, d1.Status)
	}

	// Verify second deployment still pending (was rejected)
	d2, err := orchestrator.getDeployment(ctx, deployment2)
	if err != nil {
		t.Fatalf("get deployment 2: %v", err)
	}
	if d2.Status != StatusPending {
		t.Errorf("expected deployment 2 status=%s, got %s", StatusPending, d2.Status)
	}

	// Now second deployment can execute
	exec2Err = orchestrator.ExecuteDeployment(ctx, deployment2)
	if exec2Err != nil {
		t.Errorf("deployment 2 failed: %v", exec2Err)
	}
}

// TestConcurrentDeploymentToDifferentNodes verifies concurrent deployments to different nodes succeed
func TestConcurrentDeploymentToDifferentNodes(t *testing.T) {
	st, cleanup := setupConcurrencyTest(t)
	defer cleanup()

	ctx := context.Background()
	orchestrator := NewOrchestrator(st)

	node1 := createTestNodeWithRevision(t, st, 1, "node-1")
	node2 := createTestNodeWithRevision(t, st, 2, "node-2")

	// Create deployments for different nodes
	deployment1, err := orchestrator.CreateDeployment(ctx, node1, StrategyAllAtOnce, 1)
	if err != nil {
		t.Fatalf("create deployment 1: %v", err)
	}

	deployment2, err := orchestrator.CreateDeployment(ctx, node2, StrategyAllAtOnce, 1)
	if err != nil {
		t.Fatalf("create deployment 2: %v", err)
	}

	// Execute both concurrently
	var wg sync.WaitGroup
	var exec1Err, exec2Err error

	wg.Add(2)
	go func() {
		defer wg.Done()
		exec1Err = orchestrator.ExecuteDeployment(ctx, deployment1)
	}()

	go func() {
		defer wg.Done()
		exec2Err = orchestrator.ExecuteDeployment(ctx, deployment2)
	}()

	wg.Wait()

	// Both should succeed
	if exec1Err != nil {
		t.Errorf("deployment 1 failed: %v", exec1Err)
	}
	if exec2Err != nil {
		t.Errorf("deployment 2 failed: %v", exec2Err)
	}

	// Verify both completed
	d1, err := orchestrator.getDeployment(ctx, deployment1)
	if err != nil {
		t.Fatalf("get deployment 1: %v", err)
	}
	if d1.Status != StatusCompleted {
		t.Errorf("expected deployment 1 status=%s, got %s", StatusCompleted, d1.Status)
	}

	d2, err := orchestrator.getDeployment(ctx, deployment2)
	if err != nil {
		t.Fatalf("get deployment 2: %v", err)
	}
	if d2.Status != StatusCompleted {
		t.Errorf("expected deployment 2 status=%s, got %s", StatusCompleted, d2.Status)
	}
}

// TestConcurrentDeploymentPartialOverlap verifies deployments with overlapping nodes are rejected
func TestConcurrentDeploymentPartialOverlap(t *testing.T) {
	st, cleanup := setupConcurrencyTest(t)
	defer cleanup()

	ctx := context.Background()
	orchestrator := NewOrchestrator(st)

	node1 := createTestNodeWithRevision(t, st, 1, "node-1")
	node2 := createTestNodeWithRevision(t, st, 2, "node-2")
	node3 := createTestNodeWithRevision(t, st, 3, "node-3")

	// Mark nodes as online for health checks
	err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE nodes SET status = 'online' WHERE id IN (?, ?, ?)`, node1, node2, node3)
		return err
	})
	if err != nil {
		t.Fatalf("update node status: %v", err)
	}

	// Create deployment 1: nodes 1, 2 with rolling strategy (takes longer)
	var deployment1 int64
	err = st.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`INSERT INTO deployments (revision_id, strategy, status, created_by, created_at)
			 VALUES (1, ?, ?, 1, ?)`,
			StrategyRolling, StatusPending, time.Now().Unix())
		if err != nil {
			return err
		}
		deployment1, err = result.LastInsertId()
		if err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO deployment_node_status (deployment_id, node_id, status)
			 VALUES (?, ?, ?), (?, ?, ?)`,
			deployment1, node1, NodeStatusPending,
			deployment1, node2, NodeStatusPending)
		return err
	})
	if err != nil {
		t.Fatalf("create deployment 1: %v", err)
	}

	// Create deployment 2: nodes 2, 3 (overlaps with node 2)
	var deployment2 int64
	err = st.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`INSERT INTO deployments (revision_id, strategy, status, created_by, created_at)
			 VALUES (1, ?, ?, 1, ?)`,
			StrategyAllAtOnce, StatusPending, time.Now().Unix())
		if err != nil {
			return err
		}
		deployment2, err = result.LastInsertId()
		if err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO deployment_node_status (deployment_id, node_id, status)
			 VALUES (?, ?, ?), (?, ?, ?)`,
			deployment2, node2, NodeStatusPending,
			deployment2, node3, NodeStatusPending)
		return err
	})
	if err != nil {
		t.Fatalf("create deployment 2: %v", err)
	}

	// Start first deployment
	var wg sync.WaitGroup
	var exec1Err error
	wg.Add(1)
	go func() {
		defer wg.Done()
		exec1Err = orchestrator.ExecuteDeployment(ctx, deployment1)
	}()

	// Give first deployment time to start and mark as in_progress
	time.Sleep(500 * time.Millisecond)

	// Verify deployment 1 is in_progress
	var dep1Status string
	err = st.Read().QueryRowContext(ctx,
		`SELECT status FROM deployments WHERE id = ?`, deployment1).Scan(&dep1Status)
	if err != nil {
		t.Fatalf("query deployment 1 status: %v", err)
	}
	if dep1Status != string(StatusInProgress) {
		t.Fatalf("deployment 1 should be in_progress, got %s", dep1Status)
	}

	// Try to start second deployment - should fail due to overlap
	exec2Err := orchestrator.ExecuteDeployment(ctx, deployment2)
	if exec2Err == nil {
		t.Fatal("expected error when starting deployment with overlapping nodes")
	}
	if !strings.Contains(exec2Err.Error(), "active deployments to overlapping nodes") {
		t.Errorf("unexpected error: %v", exec2Err)
	}

	wg.Wait()

	if exec1Err != nil {
		t.Errorf("deployment 1 failed: %v", exec1Err)
	}
}

// TestSequentialDeploymentsAllowed verifies sequential deployments to same node are allowed
func TestSequentialDeploymentsAllowed(t *testing.T) {
	st, cleanup := setupConcurrencyTest(t)
	defer cleanup()

	ctx := context.Background()
	orchestrator := NewOrchestrator(st)

	nodeID := createTestNodeWithRevision(t, st, 1, "test-node")

	// Execute first deployment
	deployment1, err := orchestrator.CreateDeployment(ctx, nodeID, StrategyAllAtOnce, 1)
	if err != nil {
		t.Fatalf("create deployment 1: %v", err)
	}

	err = orchestrator.ExecuteDeployment(ctx, deployment1)
	if err != nil {
		t.Fatalf("execute deployment 1: %v", err)
	}

	// Verify first completed
	d1, err := orchestrator.getDeployment(ctx, deployment1)
	if err != nil {
		t.Fatalf("get deployment 1: %v", err)
	}
	if d1.Status != StatusCompleted {
		t.Fatalf("expected deployment 1 completed, got %s", d1.Status)
	}

	// Execute second deployment to same node - should succeed
	deployment2, err := orchestrator.CreateDeployment(ctx, nodeID, StrategyAllAtOnce, 1)
	if err != nil {
		t.Fatalf("create deployment 2: %v", err)
	}

	err = orchestrator.ExecuteDeployment(ctx, deployment2)
	if err != nil {
		t.Errorf("execute deployment 2: %v", err)
	}

	// Verify second completed
	d2, err := orchestrator.getDeployment(ctx, deployment2)
	if err != nil {
		t.Fatalf("get deployment 2: %v", err)
	}
	if d2.Status != StatusCompleted {
		t.Errorf("expected deployment 2 completed, got %s", d2.Status)
	}
}

// Helper functions

func setupConcurrencyTest(t *testing.T) (*store.Store, func()) {
	t.Helper()
	st, err := storetest.OpenCopy(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	ctx := context.Background()

	// Create admin and role
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
