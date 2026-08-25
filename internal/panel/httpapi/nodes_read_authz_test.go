package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

// Node read endpoints must be permission-gated and node-scoped.
//
// Four of them were neither. handleGetNodeCapabilities carried the comment
// "Authorization: nodes:read permission (handled by middleware)", and the
// middleware in question does not exist: the private group installs auth,
// read-only and rate-limit and nothing that checks a permission. The other
// three had no comment and no check.
//
// So any authenticated caller could read a node's name, its detected protocol
// capabilities, its adapter inventory, its connection metrics and its
// reconciliation state -- for ANY node id, including one they hold no scope
// over. That is the same shape as the /v2/subjects leak: a private route whose
// authorization was assumed rather than written.
//
// The list is table-driven so a new node read route is one line away from
// being covered, and so a regression names the route it broke.
var nodeReadRoutes = []struct {
	name string
	path func(nodeID string) string
}{
	{"capabilities", func(id string) string { return "/api/v1/nodes/" + id + "/capabilities" }},
	{"reconciliation", func(id string) string { return "/api/v1/nodes/" + id + "/reconciliation" }},
	{"metrics", func(id string) string { return "/api/v1/nodes/" + id + "/metrics" }},
	{"adapters", func(id string) string { return "/api/v1/nodes/" + id + "/adapters" }},
}

// A tenant holding no node scope must be refused every node read route.
func TestNodeReadRoutesRefuseAnOutOfScopeCaller(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	nodeID := itoa64(env.seedNode(t, "secret-node"))

	env.seedAdmin(t, "vendor", "pw", "reseller")
	tenantToken := env.login(t, "vendor", "pw")

	for _, route := range nodeReadRoutes {
		path := route.path(nodeID)

		// The admin can read it, so a refusal below is authorization and not a
		// broken route. Without this the test would pass on a 404 for every
		// caller and prove nothing.
		if res := env.get(t, path, adminToken); res.Code != http.StatusOK {
			t.Fatalf("%s: admin got %d, want 200 — the route is broken, so the "+
				"denial below would prove nothing: %s", route.name, res.Code, res.Body)
		}

		res := env.get(t, path, tenantToken)
		if res.Code == http.StatusOK {
			t.Errorf("%s: a caller scoped to no node read it (200): %s",
				route.name, strings.TrimSpace(res.Body.String()))
			continue
		}
		if res.Code != http.StatusForbidden {
			t.Errorf("%s: got %d, want 403", route.name, res.Code)
		}
	}
}

// Holding node:read is not enough on its own: the node has to be in scope.
// rbac.Check treats a non-super actor's NodeIDs as an exhaustive allow-list,
// and these routes must pass TargetNode so that binds.
func TestNodeReadRoutesAreScopedNotJustPermissioned(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	mineID := env.seedNode(t, "mine")
	theirsID := env.seedNode(t, "theirs")
	mine, theirs := itoa64(mineID), itoa64(theirsID)

	// A readonly admin holds node:read, and is scoped to one node only.
	operatorID := env.seedAdmin(t, "operator", "pw", "readonly")
	env.grantNodeScope(t, operatorID, mineID)
	operatorToken := env.login(t, "operator", "pw")

	for _, route := range nodeReadRoutes {
		// Their own node: allowed, which proves the permission is present and
		// the refusal below is about scope rather than about the permission.
		if res := env.get(t, route.path(mine), operatorToken); res.Code != http.StatusOK {
			t.Fatalf("%s: scoped operator got %d on their own node, want 200: %s",
				route.name, res.Code, res.Body)
		}
		// Somebody else's node: refused.
		if res := env.get(t, route.path(theirs), operatorToken); res.Code != http.StatusForbidden {
			t.Errorf("%s: scoped operator read a node outside their scope (%d); "+
				"node:read is not a licence to read every node",
				route.name, res.Code)
		}
		// And the admin can still read both, so nothing was over-tightened.
		if res := env.get(t, route.path(theirs), adminToken); res.Code != http.StatusOK {
			t.Errorf("%s: admin lost access (%d)", route.name, res.Code)
		}
	}
}

// An unauthenticated caller gets 401, not data. The auth middleware already
// covers this; asserting it keeps a future "fix" that moves the permission
// check ahead of authentication from opening the route back up.
func TestNodeReadRoutesRefuseAnonymousCallers(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	nodeID := itoa64(env.seedNode(t, "secret-node"))

	for _, route := range nodeReadRoutes {
		path := route.path(nodeID)
		if res := env.get(t, path, adminToken); res.Code != http.StatusOK {
			t.Fatalf("%s: admin got %d, want 200", route.name, res.Code)
		}
		if res := env.get(t, path, ""); res.Code != http.StatusUnauthorized {
			t.Errorf("%s: anonymous caller got %d, want 401", route.name, res.Code)
		}
	}
}
