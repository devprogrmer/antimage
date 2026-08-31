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

func setupTestStore(t *testing.T) *store.Store {
	st, err := storetest.OpenCopy(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	// Create test admin and role for foreign key constraints
	ctx := context.Background()
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
		t.Fatalf("create test admin: %v", err)
	}

	return st
}

func createTestNode(t *testing.T, st *store.Store, id int64, name string) {
	ctx := context.Background()
	err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (id, name, address, status, created_at)
			 VALUES (?, ?, ?, 'online', ?)`,
			id, name, "10.0.0.1:8443", time.Now().Unix())
		if err != nil {
			return err
		}
		// Create initial revision
		_, err = tx.ExecContext(ctx,
			`INSERT INTO node_revisions (node_id, revision, created_at, actor_type, actor_label, reason, doc_sha256)
			 VALUES (?, 1, ?, 'system', 'test', 'initial', 'sha256-initial')`,
			id, time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("create test node: %v", err)
	}
}

func TestOrchestratorAllAtOnce(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()

	ctx := context.Background()
	createTestNode(t, st, 1, "node-1")

	orch := NewOrchestrator(st)
	deploymentID, err := orch.CreateDeployment(ctx, 1, StrategyAllAtOnce, 1)
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	if deploymentID == 0 {
		t.Error("expected non-zero deployment ID")
	}

	// Check deployment was created
	var status string
	err = st.Read().QueryRowContext(ctx,
		`SELECT status FROM deployments WHERE id = ?`,
		deploymentID).Scan(&status)
	if err != nil {
		t.Fatalf("query deployment: %v", err)
	}

	if status != string(StatusPending) {
		t.Errorf("expected status %s, got %s", StatusPending, status)
	}

	// Check node status was created
	var nodeStatus string
	err = st.Read().QueryRowContext(ctx,
		`SELECT status FROM deployment_node_status WHERE deployment_id = ? AND node_id = ?`,
		deploymentID, 1).Scan(&nodeStatus)
	if err != nil {
		t.Fatalf("query node status: %v", err)
	}

	if nodeStatus != string(NodeStatusPending) {
		t.Errorf("expected node status %s, got %s", NodeStatusPending, nodeStatus)
	}
}

func TestOrchestratorCanaryStrategy(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()

	ctx := context.Background()
	createTestNode(t, st, 1, "canary-node")

	orch := NewOrchestrator(st)
	deploymentID, err := orch.CreateDeployment(ctx, 1, StrategyCanary, 1)
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	if deploymentID == 0 {
		t.Error("expected non-zero deployment ID")
	}

	var strategy string
	err = st.Read().QueryRowContext(ctx,
		`SELECT strategy FROM deployments WHERE id = ?`,
		deploymentID).Scan(&strategy)
	if err != nil {
		t.Fatalf("query deployment: %v", err)
	}

	if strategy != string(StrategyCanary) {
		t.Errorf("expected strategy %s, got %s", StrategyCanary, strategy)
	}
}

func TestOrchestratorStagedStrategy(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()

	ctx := context.Background()
	createTestNode(t, st, 1, "staged-node")

	orch := NewOrchestrator(st)
	deploymentID, err := orch.CreateDeployment(ctx, 1, StrategyStaged, 1)
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	if deploymentID == 0 {
		t.Error("expected non-zero deployment ID")
	}

	var strategy string
	err = st.Read().QueryRowContext(ctx,
		`SELECT strategy FROM deployments WHERE id = ?`,
		deploymentID).Scan(&strategy)
	if err != nil {
		t.Fatalf("query deployment: %v", err)
	}

	if strategy != string(StrategyStaged) {
		t.Errorf("expected strategy %s, got %s", StrategyStaged, strategy)
	}
}

func TestOrchestratorRollingStrategy(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()

	ctx := context.Background()
	createTestNode(t, st, 1, "rolling-node")

	orch := NewOrchestrator(st)
	deploymentID, err := orch.CreateDeployment(ctx, 1, StrategyRolling, 1)
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	if deploymentID == 0 {
		t.Error("expected non-zero deployment ID")
	}

	var strategy string
	err = st.Read().QueryRowContext(ctx,
		`SELECT strategy FROM deployments WHERE id = ?`,
		deploymentID).Scan(&strategy)
	if err != nil {
		t.Fatalf("query deployment: %v", err)
	}

	if strategy != string(StrategyRolling) {
		t.Errorf("expected strategy %s, got %s", StrategyRolling, strategy)
	}
}

func TestOrchestratorValidationFailure(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()

	ctx := context.Background()
	// Don't create node or revision - deployment should fail validation

	orch := NewOrchestrator(st)
	_, err := orch.CreateDeployment(ctx, 999, StrategyAllAtOnce, 1)
	if err == nil {
		t.Error("expected error for nonexistent node")
	}
}

func TestDeploymentPersistence(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()

	ctx := context.Background()
	createTestNode(t, st, 1, "persist-node")

	orch := NewOrchestrator(st)
	deploymentID, err := orch.CreateDeployment(ctx, 1, StrategyAllAtOnce, 1)
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	// Verify all fields persisted correctly
	var (
		storedRevisionID int64
		storedStrategy   string
		storedStatus     string
		storedCreatedBy  int64
		storedCreatedAt  int64
	)

	err = st.Read().QueryRowContext(ctx,
		`SELECT revision_id, strategy, status, created_by, created_at
		 FROM deployments WHERE id = ?`,
		deploymentID).Scan(&storedRevisionID, &storedStrategy, &storedStatus, &storedCreatedBy, &storedCreatedAt)
	if err != nil {
		t.Fatalf("query deployment: %v", err)
	}

	if storedRevisionID != 1 {
		t.Errorf("expected revision_id 1, got %d", storedRevisionID)
	}

	if storedStrategy != string(StrategyAllAtOnce) {
		t.Errorf("expected strategy %s, got %s", StrategyAllAtOnce, storedStrategy)
	}

	if storedStatus != string(StatusPending) {
		t.Errorf("expected status %s, got %s", StatusPending, storedStatus)
	}

	if storedCreatedBy != 1 {
		t.Errorf("expected created_by 1, got %d", storedCreatedBy)
	}

	if storedCreatedAt == 0 {
		t.Error("expected created_at to be set")
	}
}

func TestDeploymentAuditTrail(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()

	ctx := context.Background()
	createTestNode(t, st, 1, "audit-node")

	// Admin already created by setupTestStore with ID 1
	orch := NewOrchestrator(st)
	deploymentID, err := orch.CreateDeployment(ctx, 1, StrategyAllAtOnce, 1)
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	// Verify deployment can be traced back to admin
	var createdBy int64
	err = st.Read().QueryRowContext(ctx,
		`SELECT created_by FROM deployments WHERE id = ?`,
		deploymentID).Scan(&createdBy)
	if err != nil {
		t.Fatalf("query deployment: %v", err)
	}

	if createdBy != 1 {
		t.Errorf("expected created_by 1, got %d", createdBy)
	}
}
