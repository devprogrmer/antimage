package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func createNodeWithRevision(t *testing.T, env *testEnv, nodeID int64, name string) {
	t.Helper()
	ctx := context.Background()
	err := env.store.Write(ctx, func(tx *sql.Tx) error {
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
		t.Fatalf("create node with revision: %v", err)
	}
}

func TestDeploymentValidate(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "admin", "pw", "super_admin")
	token := env.login(t, "admin", "pw")

	createNodeWithRevision(t, env, 1, "test-node")

	res := env.post(t, "/api/v1/deployments/validate", `{"node_id":1,"revision":1}`, token)
	if res.Code != http.StatusOK {
		t.Fatalf("validate status = %d, body = %s", res.Code, res.Body)
	}

	var body struct {
		Valid bool `json:"valid"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Valid {
		t.Errorf("expected valid=true, got false")
	}
}

func TestDeploymentValidateUnauthorized(t *testing.T) {
	env := newTestEnv(t)

	res := env.post(t, "/api/v1/deployments/validate", `{"node_id":1,"revision":1}`, "")
	if res.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", res.Code)
	}
}

func TestDeploymentPreview(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "admin", "pw", "super_admin")
	token := env.login(t, "admin", "pw")

	createNodeWithRevision(t, env, 1, "test-node")

	res := env.post(t, "/api/v1/deployments/preview", `{"node_id":1,"revision":1}`, token)
	if res.Code != http.StatusOK {
		t.Fatalf("preview status = %d, body = %s", res.Code, res.Body)
	}

	var body struct {
		NodeID int64 `json:"node_id"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.NodeID != 1 {
		t.Errorf("expected node_id=1, got %d", body.NodeID)
	}
}

func TestDeploymentCreate(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "admin", "pw", "super_admin")
	token := env.login(t, "admin", "pw")

	createNodeWithRevision(t, env, 1, "test-node")

	res := env.post(t, "/api/v1/deployments", `{"node_id":1,"strategy":"all_at_once"}`, token)
	if res.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", res.Code, res.Body)
	}

	var body struct {
		DeploymentID int64  `json:"deployment_id"`
		Status       string `json:"status"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.DeploymentID == 0 {
		t.Error("expected deployment_id > 0")
	}
	if body.Status != "pending" {
		t.Errorf("expected status=pending, got %s", body.Status)
	}
}

func TestDeploymentCreateInvalidStrategy(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "admin", "pw", "super_admin")
	token := env.login(t, "admin", "pw")

	createNodeWithRevision(t, env, 1, "test-node")

	res := env.post(t, "/api/v1/deployments", `{"node_id":1,"strategy":"invalid"}`, token)
	if res.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", res.Code)
	}
}

func TestDeploymentCreateRequiresPermission(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "readonly", "pw", "readonly")
	token := env.login(t, "readonly", "pw")

	createNodeWithRevision(t, env, 1, "test-node")

	res := env.post(t, "/api/v1/deployments", `{"node_id":1,"strategy":"all_at_once"}`, token)
	if res.Code != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", res.Code)
	}
}

func TestDeploymentList(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.seedAdmin(t, "admin", "pw", "super_admin")
	token := env.login(t, "admin", "pw")

	createNodeWithRevision(t, env, 1, "test-node")

	// Create deployment directly
	ctx := context.Background()
	err := env.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO deployments (revision_id, strategy, status, created_by, created_at)
			 VALUES (1, 'all_at_once', 'completed', ?, ?)`,
			adminID, time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	res := env.get(t, "/api/v1/deployments", token)
	if res.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", res.Code, res.Body)
	}

	var body struct {
		Deployments []map[string]interface{} `json:"deployments"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Deployments) == 0 {
		t.Error("expected at least one deployment")
	}
}

func TestDeploymentGet(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.seedAdmin(t, "admin", "pw", "super_admin")
	token := env.login(t, "admin", "pw")

	createNodeWithRevision(t, env, 1, "test-node")

	// Create deployment
	ctx := context.Background()
	var deploymentID int64
	err := env.store.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`INSERT INTO deployments (revision_id, strategy, status, created_by, created_at)
			 VALUES (1, 'canary', 'pending', ?, ?)`,
			adminID, time.Now().Unix())
		if err != nil {
			return err
		}
		deploymentID, err = result.LastInsertId()
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO deployment_node_status (deployment_id, node_id, status)
			 VALUES (?, 1, 'pending')`,
			deploymentID)
		return err
	})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	res := env.get(t, "/api/v1/deployments/"+itoa64(deploymentID), token)
	if res.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", res.Code, res.Body)
	}

	var body struct {
		Deployment struct {
			ID       int64  `json:"id"`
			Strategy string `json:"strategy"`
		} `json:"deployment"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Deployment.ID != deploymentID {
		t.Errorf("expected id=%d, got %d", deploymentID, body.Deployment.ID)
	}
	if body.Deployment.Strategy != "canary" {
		t.Errorf("expected strategy=canary, got %s", body.Deployment.Strategy)
	}
}
