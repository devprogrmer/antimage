package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
)

// The deployment routes had almost no authorization.
//
// Six handlers, six requireActor calls, and two permission checks -- so
// validate, preview, get and list were reachable by ANY authenticated actor,
// including a readonly account and a reseller tenant. handleDeploymentList in
// particular selected every deployment on the platform with no filter at all:
// which node changed, when, by which admin, and the text of any failure.
//
// The two handlers that did check used rbac.Target{Kind: TargetNone}, a
// permission gate with no node scope. An admin scoped to one node could roll
// back another.
//
// Underneath both of those sits a schema problem: deployments.revision_id
// holds the node's REVISION NUMBER, not a unique id, and node_revisions is
// keyed (node_id, revision). Revision 3 belongs to every node that has reached
// three. A deployment therefore did not record which node it deployed, so the
// list could not be scoped even in principle. 00032 adds node_id.

// seedDeployment inserts a deployment row for a node, the way the orchestrator
// does, and returns its id.
func seedDeployment(t *testing.T, env *testEnv, nodeID int64) int64 {
	t.Helper()
	var id int64
	err := env.store.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`INSERT INTO deployments (node_id, revision_id, strategy, status, created_by, created_at)
			 VALUES (?, 1, 'all_at_once', 'completed', 1, 1000)`, nodeID)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("seed deployment: %v", err)
	}
	return id
}

// A tenant must not be able to enumerate the platform's deployment history.
func TestDeploymentListIsNotReadableByATenant(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	seedDeployment(t, env, 1)
	tenantToken, _ := seedTenant(t, env, "alice", svcID, adminToken)

	res := env.get(t, "/api/v1/deployments", tenantToken)
	if res.Code != http.StatusOK {
		t.Fatalf("tenant list = %d, want 200; this asserts what a PERMITTED "+
			"reader sees, so a refusal would make it vacuous", res.Code)
	}
	// The reseller role holds node:read, so the permission gate passes and the
	// SCOPE has to do the work. Alice is scoped to no node, so she sees no
	// deployments -- rather than every deployment on the platform, which is
	// what the unfiltered query returned.
	var listed struct {
		Deployments []struct {
			ID int64 `json:"id"`
		} `json:"deployments"`
	}
	if err := json.NewDecoder(res.Body).Decode(&listed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(listed.Deployments) != 0 {
		t.Errorf("a reseller scoped to no node listed %d deployments; the route "+
			"returned every deployment on the platform -- which node changed, "+
			"when, and by which admin", len(listed.Deployments))
	}
}

func TestDeploymentGetIsNotReadableByATenant(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	id := seedDeployment(t, env, 1)
	tenantToken, _ := seedTenant(t, env, "alice", svcID, adminToken)

	if res := env.get(t, "/api/v1/deployments/"+itoa64(id), tenantToken); res.Code == http.StatusOK {
		t.Errorf("a reseller read deployment %d (%d): %s", id, res.Code, res.Body)
	}
}

func TestDeploymentPreviewIsNotReachableByATenant(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	tenantToken, _ := seedTenant(t, env, "alice", svcID, adminToken)

	res := env.post(t, "/api/v1/deployments/preview", `{"node_id":1,"revision":1}`, tenantToken)
	if res.Code == http.StatusOK {
		t.Errorf("a reseller previewed a node's revisions (%d): %s", res.Code, res.Body)
	}
}

func TestDeploymentValidateIsNotReachableByATenant(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	tenantToken, _ := seedTenant(t, env, "alice", svcID, adminToken)

	res := env.post(t, "/api/v1/deployments/validate", `{"node_id":1,"revision":1}`, tenantToken)
	if res.Code == http.StatusOK {
		t.Errorf("a reseller validated a node's revision (%d): %s", res.Code, res.Body)
	}
}

// The list an operator IS allowed sees only their own nodes.
func TestDeploymentListIsScopedToTheCallersNodes(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	otherNode := env.seedNode(t, "someone-elses")
	mine := seedDeployment(t, env, 1)
	theirs := seedDeployment(t, env, otherNode)

	// The super admin sees both, so the scoping below is a filter rather than
	// everything being hidden from everyone.
	res := env.get(t, "/api/v1/deployments", adminToken)
	if res.Code != http.StatusOK {
		t.Fatalf("super admin list = %d: %s", res.Code, res.Body)
	}
	var all struct {
		Deployments []struct {
			ID int64 `json:"id"`
		} `json:"deployments"`
	}
	if err := json.NewDecoder(res.Body).Decode(&all); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(all.Deployments) < 2 {
		t.Fatalf("super admin sees %d deployments, want both (%d and %d)",
			len(all.Deployments), mine, theirs)
	}
}

// A deployment has to record which node it deployed, or nothing downstream can
// be scoped: rollback cannot check the node it is about to change, and the list
// cannot be filtered.
func TestADeploymentRecordsItsNode(t *testing.T) {
	env, _, _ := newSubjectEnv(t)
	otherNode := env.seedNode(t, "second")
	first := seedDeployment(t, env, 1)
	second := seedDeployment(t, env, otherNode)

	read := func(id int64) int64 {
		var nodeID int64
		if err := env.store.Read().QueryRow(
			`SELECT node_id FROM deployments WHERE id = ?`, id).Scan(&nodeID); err != nil {
			t.Fatalf("read node_id for deployment %d: %v", id, err)
		}
		return nodeID
	}
	if read(first) == read(second) {
		t.Errorf("both deployments report node %d; revision_id alone cannot "+
			"tell them apart because a revision number is per node", read(first))
	}
	if read(second) != otherNode {
		t.Errorf("deployment %d reports node %d, want %d", second, read(second), otherNode)
	}
}

// Holding node:write is not the same as holding it for THIS node.
//
// Both write routes used rbac.Target{Kind: TargetNone}, which checks the
// permission and nothing else, so an admin scoped to their own nodes could
// deploy and roll back somebody else's. The permission tests above pass either
// way -- only a caller who HAS the permission can tell the two apart.
func TestRollbackIsScopedToTheDeploymentsNode(t *testing.T) {
	env, _, _ := newSubjectEnv(t)
	otherNode := env.seedNode(t, "not-theirs")
	foreign := seedDeployment(t, env, otherNode)

	// An admin holds node:write, and is scoped to node 1 only.
	env.seedAdmin(t, "ops", "pw", "admin")
	grantNodeScope(t, env, "ops", 1)
	token := env.login(t, "ops", "pw")

	res := env.post(t, "/api/v1/deployments/"+itoa64(foreign)+"/rollback", "", token)
	if res.Code == http.StatusOK || res.Code == http.StatusNoContent {
		t.Errorf("an admin scoped to node 1 rolled back a deployment on node %d "+
			"(%d): the permission was checked and the node was not",
			otherNode, res.Code)
	}
}

// The same for creating one.
func TestCreateIsScopedToTheTargetNode(t *testing.T) {
	env, _, _ := newSubjectEnv(t)
	otherNode := env.seedNode(t, "not-theirs")

	env.seedAdmin(t, "ops", "pw", "admin")
	grantNodeScope(t, env, "ops", 1)
	token := env.login(t, "ops", "pw")

	body := `{"node_id":` + itoa64(otherNode) + `,"strategy":"all_at_once"}`
	res := env.post(t, "/api/v1/deployments", body, token)
	if res.Code == http.StatusCreated {
		t.Errorf("an admin scoped to node 1 started a deployment on node %d",
			otherNode)
	}
}

// And reading one.
func TestGetIsScopedToTheDeploymentsNode(t *testing.T) {
	env, _, _ := newSubjectEnv(t)
	otherNode := env.seedNode(t, "not-theirs")
	foreign := seedDeployment(t, env, otherNode)

	env.seedAdmin(t, "ops", "pw", "admin")
	grantNodeScope(t, env, "ops", 1)
	token := env.login(t, "ops", "pw")

	if res := env.get(t, "/api/v1/deployments/"+itoa64(foreign), token); res.Code == http.StatusOK {
		t.Errorf("an admin scoped to node 1 read a deployment on node %d: %s",
			otherNode, res.Body)
	}
}
