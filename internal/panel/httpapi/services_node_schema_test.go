package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/amyrm/antimage/internal/panel/nodes"
)

// A schema the panel was never compiled with: it demands a field no built-in
// adapter has, so accepting params without it proves the node's schema was
// ignored, and rejecting them proves it was used.
const nodeOnlySchema = `{
  "type":"object",
  "additionalProperties":false,
  "required":["listen_port","node_only_field"],
  "properties":{
    "listen_port":{"type":"integer"},
    "node_only_field":{"type":"string"}
  }
}`

func createService(t *testing.T, env *testEnv, token string, nodeID int64, kind, params string) int {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"adapter_kind": kind, "params": json.RawMessage(params),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return env.post(t, "/api/v1/nodes/"+itoa64(nodeID)+"/services", string(body), token).Code
}

// THE point of Step 4 reaching validation: the panel validates against what the
// NODE published, not what this build of the panel was compiled with.
func TestServiceIsValidatedAgainstTheNodesSchema(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	nodeID := env.seedNode(t, "edge-1")
	reportHello(t, env, nodeID, nodes.AdapterInfo{
		Kind: "xray", Version: "1", ServiceSchema: []byte(nodeOnlySchema),
	})

	// Satisfies the node's schema.
	if code := createService(t, env, adminToken, nodeID, "xray",
		`{"listen_port":443,"node_only_field":"present"}`); code != http.StatusCreated {
		t.Errorf("params matching the node's schema were rejected (%d)", code)
	}

	// Omits a field the node's schema requires. The panel's own xray schema
	// knows nothing of it, so accepting this would mean the node's schema was
	// never consulted.
	if code := createService(t, env, adminToken, nodeID, "xray",
		`{"listen_port":443}`); code != http.StatusUnprocessableEntity {
		t.Errorf("params violating the node's schema were accepted (%d); the "+
			"panel validated against its compiled-in schema instead", code)
	}
}

// A protocol the node does not run is refused here, where the reason is
// obvious, rather than at reconcile time where it is hardest to diagnose.
func TestServiceForAKindTheNodeDoesNotRunIsRefused(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	nodeID := env.seedNode(t, "wireguard-only")
	reportHello(t, env, nodeID, nodes.AdapterInfo{
		Kind: "wireguard", Version: "1", ServiceSchema: []byte(nodeOnlySchema),
	})

	res := env.post(t, "/api/v1/nodes/"+itoa64(nodeID)+"/services",
		`{"adapter_kind":"xray","params":{}}`, adminToken)
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("creating an xray service on a WireGuard-only node = %d, want 422; "+
			"it could never be applied", res.Code)
	}
	body := res.Body.String()
	if !strings.Contains(body, "wireguard") {
		t.Errorf("the refusal does not say what the node does run: %s", body)
	}
}

// A node that has never connected has reported nothing, and preparing its
// services before the agent first calls home is a real workflow. "We do not
// know" must not become "it cannot".
func TestServicesCanBePreparedBeforeANodeConnects(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	nodeID := env.seedNode(t, "not-yet-enrolled")

	if code := createService(t, env, adminToken, nodeID, "xray",
		`{"protocol":"vless","port":443}`); code != http.StatusCreated {
		t.Errorf("could not prepare a service on a node that has not connected (%d); "+
			"the panel refused on the basis of a report that does not exist yet", code)
	}
}

// An agent that runs an adapter but is too old to publish its schema must keep
// working: the protocol demonstrably works on that host, and refusing would
// break it on the upgrade path.
func TestAdapterReportedWithoutASchemaFallsBackToTheBuiltIn(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	nodeID := env.seedNode(t, "old-agent")
	reportHello(t, env, nodeID, nodes.AdapterInfo{Kind: "xray", Version: "1"})

	if code := createService(t, env, adminToken, nodeID, "xray",
		`{"protocol":"vless","port":443}`); code != http.StatusCreated {
		t.Errorf("an adapter reported without a schema became unusable (%d)", code)
	}
}

// ------------------------------------------------------------ listing

func TestServicesCanBeListed(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	nodeID := env.seedNode(t, "edge-1")
	reportHello(t, env, nodeID, nodes.AdapterInfo{
		Kind: "xray", Version: "1", ServiceSchema: []byte(nodeOnlySchema),
	})
	if code := createService(t, env, adminToken, nodeID, "xray",
		`{"listen_port":443,"node_only_field":"a"}`); code != http.StatusCreated {
		t.Fatalf("create = %d", code)
	}

	res := env.get(t, "/api/v1/nodes/"+itoa64(nodeID)+"/services", adminToken)
	if res.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", res.Code, res.Body)
	}
	var out struct {
		Services []struct {
			ID          int64           `json:"id"`
			AdapterKind string          `json:"adapter_kind"`
			Params      json.RawMessage `json:"params"`
			Enabled     bool            `json:"enabled"`
		} `json:"services"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Services) != 1 {
		t.Fatalf("listed %d services, want 1", len(out.Services))
	}
	if out.Services[0].AdapterKind != "xray" || !out.Services[0].Enabled {
		t.Errorf("service = %+v", out.Services[0])
	}
	// Params come back verbatim: an editor loads them straight back into the
	// form, so anything reshaped here is a field the operator silently loses.
	var got map[string]any
	if err := json.Unmarshal(out.Services[0].Params, &got); err != nil {
		t.Fatalf("params are not JSON: %v", err)
	}
	if got["node_only_field"] != "a" {
		t.Errorf("params = %s, want the document that was submitted", out.Services[0].Params)
	}
}

func TestListingServicesOnANodeWithNoneIsEmptyNotNull(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	nodeID := env.seedNode(t, "bare")

	res := env.get(t, "/api/v1/nodes/"+itoa64(nodeID)+"/services", adminToken)
	if res.Code != http.StatusOK {
		t.Fatalf("list = %d", res.Code)
	}
	// A JSON null would make the UI branch on it; an empty array does not.
	if !strings.Contains(res.Body.String(), `"services":[]`) {
		t.Errorf("body = %s, want an empty array", res.Body)
	}
}

// Reading a service's params is a stronger right than seeing a node exists,
// because an adapter is free to publish a schema with a credential field.
func TestListingServicesIsScopedAndPermissioned(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	nodeID := env.seedNode(t, "secret-node")
	env.seedAdmin(t, "vendor", "pw", "reseller")
	tenantToken := env.login(t, "vendor", "pw")

	path := "/api/v1/nodes/" + itoa64(nodeID) + "/services"
	if res := env.get(t, path, adminToken); res.Code != http.StatusOK {
		t.Fatalf("admin got %d, so the denial below would prove nothing", res.Code)
	}
	if res := env.get(t, path, tenantToken); res.Code != http.StatusForbidden {
		t.Errorf("a caller scoped to no node listed its services (%d)", res.Code)
	}
}
