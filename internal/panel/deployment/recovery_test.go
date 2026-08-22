package deployment

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

func TestRecoverStaleDeployments(t *testing.T) {
	st, cleanup := setupRecoveryTest(t)
	defer cleanup()

	ctx := context.Background()
	orchestrator := NewOrchestrator(st)

	// Create test node
	var nodeID int64
	err := st.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, status, created_at)
			 VALUES ('test-node', '10.0.0.1:8443', 'online', ?)`,
			time.Now().Unix())
		if err != nil {
			return err
		}
		nodeID, err = result.LastInsertId()
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
		t.Fatalf("setup: %v", err)
	}

	// Create stale deployment stuck in in_progress
	var staleDeploymentID int64
	err = st.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`INSERT INTO deployments (revision_id, strategy, status, created_by, created_at, started_at)
			 VALUES (1, 'all_at_once', 'in_progress', 1, ?, ?)`,
			time.Now().Add(-10*time.Minute).Unix(), time.Now().Add(-10*time.Minute).Unix())
		if err != nil {
			return err
		}
		staleDeploymentID, err = result.LastInsertId()
		if err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO deployment_node_status (deployment_id, node_id, status)
			 VALUES (?, ?, 'applying')`,
			staleDeploymentID, nodeID)
		return err
	})
	if err != nil {
		t.Fatalf("create stale deployment: %v", err)
	}

	// Run recovery
	err = orchestrator.RecoverStaleDeployments(ctx)
	if err != nil {
		t.Fatalf("RecoverStaleDeployments: %v", err)
	}

	// Verify deployment marked as failed
	var status string
	var completedAt sql.NullInt64
	err = st.Read().QueryRowContext(ctx,
		`SELECT status, completed_at FROM deployments WHERE id = ?`,
		staleDeploymentID).Scan(&status, &completedAt)
	if err != nil {
		t.Fatalf("query deployment: %v", err)
	}

	if status != string(StatusFailed) {
		t.Errorf("expected status=failed, got %s", status)
	}

	if !completedAt.Valid {
		t.Error("expected completed_at to be set")
	}

	// Verify node status marked as failed
	var nodeStatus string
	err = st.Read().QueryRowContext(ctx,
		`SELECT status FROM deployment_node_status WHERE deployment_id = ? AND node_id = ?`,
		staleDeploymentID, nodeID).Scan(&nodeStatus)
	if err != nil {
		t.Fatalf("query node status: %v", err)
	}

	if nodeStatus != string(NodeStatusFailed) {
		t.Errorf("expected node status=failed, got %s", nodeStatus)
	}
}

func TestRecoverStaleDeploymentsMultiple(t *testing.T) {
	st, cleanup := setupRecoveryTest(t)
	defer cleanup()

	ctx := context.Background()
	orchestrator := NewOrchestrator(st)

	// Create test nodes
	var node1ID, node2ID int64
	err := st.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, status, created_at)
			 VALUES ('node1', '10.0.0.1:8443', 'online', ?)`,
			time.Now().Unix())
		if err != nil {
			return err
		}
		node1ID, err = result.LastInsertId()
		if err != nil {
			return err
		}

		result, err = tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, status, created_at)
			 VALUES ('node2', '10.0.0.2:8443', 'online', ?)`,
			time.Now().Unix())
		if err != nil {
			return err
		}
		node2ID, err = result.LastInsertId()
		if err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO node_revisions (node_id, revision, created_at, actor_type, actor_label, reason, doc_sha256)
			 VALUES (?, 1, ?, 'system', 'test', 'initial', 'sha256-test1')`,
			node1ID, time.Now().Unix())
		if err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO node_revisions (node_id, revision, created_at, actor_type, actor_label, reason, doc_sha256)
			 VALUES (?, 1, ?, 'system', 'test', 'initial', 'sha256-test2')`,
			node2ID, time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Create two stale deployments
	var dep1ID, dep2ID int64
	err = st.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`INSERT INTO deployments (revision_id, strategy, status, created_by, created_at, started_at)
			 VALUES (1, 'canary', 'in_progress', 1, ?, ?)`,
			time.Now().Add(-5*time.Minute).Unix(), time.Now().Add(-5*time.Minute).Unix())
		if err != nil {
			return err
		}
		dep1ID, err = result.LastInsertId()
		if err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO deployment_node_status (deployment_id, node_id, status)
			 VALUES (?, ?, 'pending')`,
			dep1ID, node1ID)
		if err != nil {
			return err
		}

		result, err = tx.ExecContext(ctx,
			`INSERT INTO deployments (revision_id, strategy, status, created_by, created_at, started_at)
			 VALUES (2, 'rolling', 'in_progress', 1, ?, ?)`,
			time.Now().Add(-3*time.Minute).Unix(), time.Now().Add(-3*time.Minute).Unix())
		if err != nil {
			return err
		}
		dep2ID, err = result.LastInsertId()
		if err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO deployment_node_status (deployment_id, node_id, status)
			 VALUES (?, ?, 'applying')`,
			dep2ID, node2ID)
		return err
	})
	if err != nil {
		t.Fatalf("create stale deployments: %v", err)
	}

	// Run recovery
	err = orchestrator.RecoverStaleDeployments(ctx)
	if err != nil {
		t.Fatalf("RecoverStaleDeployments: %v", err)
	}

	// Verify both deployments marked as failed
	var count int
	err = st.Read().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM deployments WHERE status = ? AND id IN (?, ?)`,
		StatusFailed, dep1ID, dep2ID).Scan(&count)
	if err != nil {
		t.Fatalf("query failed deployments: %v", err)
	}

	if count != 2 {
		t.Errorf("expected 2 failed deployments, got %d", count)
	}
}

func TestRecoverStaleDeploymentsNoStale(t *testing.T) {
	st, cleanup := setupRecoveryTest(t)
	defer cleanup()

	ctx := context.Background()
	orchestrator := NewOrchestrator(st)

	// Create completed deployment
	err := st.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, status, created_at)
			 VALUES ('test-node', '10.0.0.1:8443', 'online', ?)`,
			time.Now().Unix())
		if err != nil {
			return err
		}
		nodeID, err := result.LastInsertId()
		if err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO node_revisions (node_id, revision, created_at, actor_type, actor_label, reason, doc_sha256)
			 VALUES (?, 1, ?, 'system', 'test', 'initial', 'sha256-test')`,
			nodeID, time.Now().Unix())
		if err != nil {
			return err
		}

		result, err = tx.ExecContext(ctx,
			`INSERT INTO deployments (revision_id, strategy, status, created_by, created_at, started_at, completed_at)
			 VALUES (1, 'all_at_once', 'completed', 1, ?, ?, ?)`,
			time.Now().Unix(), time.Now().Unix(), time.Now().Unix())
		if err != nil {
			return err
		}
		depID, err := result.LastInsertId()
		if err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO deployment_node_status (deployment_id, node_id, status)
			 VALUES (?, ?, 'completed')`,
			depID, nodeID)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Run recovery - should not fail
	err = orchestrator.RecoverStaleDeployments(ctx)
	if err != nil {
		t.Fatalf("RecoverStaleDeployments: %v", err)
	}

	// Verify deployment still completed
	var status string
	err = st.Read().QueryRowContext(ctx,
		`SELECT status FROM deployments WHERE id = 1`).Scan(&status)
	if err != nil {
		t.Fatalf("query deployment: %v", err)
	}

	if status != string(StatusCompleted) {
		t.Errorf("expected status=completed, got %s", status)
	}
}

func TestRecoverStaleDeploymentsPartiallyComplete(t *testing.T) {
	st, cleanup := setupRecoveryTest(t)
	defer cleanup()

	ctx := context.Background()
	orchestrator := NewOrchestrator(st)

	// Create nodes
	var node1ID, node2ID int64
	err := st.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, status, created_at)
			 VALUES ('node1', '10.0.0.1:8443', 'online', ?)`,
			time.Now().Unix())
		if err != nil {
			return err
		}
		node1ID, err = result.LastInsertId()
		if err != nil {
			return err
		}

		result, err = tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, status, created_at)
			 VALUES ('node2', '10.0.0.2:8443', 'online', ?)`,
			time.Now().Unix())
		if err != nil {
			return err
		}
		node2ID, err = result.LastInsertId()
		if err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO node_revisions (node_id, revision, created_at, actor_type, actor_label, reason, doc_sha256)
			 VALUES (?, 1, ?, 'system', 'test', 'initial', 'sha256-test1')`,
			node1ID, time.Now().Unix())
		if err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO node_revisions (node_id, revision, created_at, actor_type, actor_label, reason, doc_sha256)
			 VALUES (?, 1, ?, 'system', 'test', 'initial', 'sha256-test2')`,
			node2ID, time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Create deployment with one node completed and one pending
	var depID int64
	err = st.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`INSERT INTO deployments (revision_id, strategy, status, created_by, created_at, started_at)
			 VALUES (1, 'rolling', 'in_progress', 1, ?, ?)`,
			time.Now().Add(-2*time.Minute).Unix(), time.Now().Add(-2*time.Minute).Unix())
		if err != nil {
			return err
		}
		depID, err = result.LastInsertId()
		if err != nil {
			return err
		}

		// First node completed successfully
		_, err = tx.ExecContext(ctx,
			`INSERT INTO deployment_node_status (deployment_id, node_id, status)
			 VALUES (?, ?, 'completed')`,
			depID, node1ID)
		if err != nil {
			return err
		}

		// Second node still pending
		_, err = tx.ExecContext(ctx,
			`INSERT INTO deployment_node_status (deployment_id, node_id, status)
			 VALUES (?, ?, 'pending')`,
			depID, node2ID)
		return err
	})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	// Run recovery
	err = orchestrator.RecoverStaleDeployments(ctx)
	if err != nil {
		t.Fatalf("RecoverStaleDeployments: %v", err)
	}

	// Verify deployment marked as failed
	var status string
	err = st.Read().QueryRowContext(ctx,
		`SELECT status FROM deployments WHERE id = ?`,
		depID).Scan(&status)
	if err != nil {
		t.Fatalf("query deployment: %v", err)
	}

	if status != string(StatusFailed) {
		t.Errorf("expected status=failed, got %s", status)
	}

	// Verify completed node stays completed
	var node1Status string
	err = st.Read().QueryRowContext(ctx,
		`SELECT status FROM deployment_node_status WHERE deployment_id = ? AND node_id = ?`,
		depID, node1ID).Scan(&node1Status)
	if err != nil {
		t.Fatalf("query node1 status: %v", err)
	}

	if node1Status != string(NodeStatusCompleted) {
		t.Errorf("expected node1 status=completed, got %s", node1Status)
	}

	// Verify pending node marked as failed
	var node2Status string
	err = st.Read().QueryRowContext(ctx,
		`SELECT status FROM deployment_node_status WHERE deployment_id = ? AND node_id = ?`,
		depID, node2ID).Scan(&node2Status)
	if err != nil {
		t.Fatalf("query node2 status: %v", err)
	}

	if node2Status != string(NodeStatusFailed) {
		t.Errorf("expected node2 status=failed, got %s", node2Status)
	}
}

func setupRecoveryTest(t *testing.T) (*store.Store, func()) {
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
