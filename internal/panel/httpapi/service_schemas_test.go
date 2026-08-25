package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/nodes"
)

// reportHello records what a node says about itself at Hello, which is the
// only source this endpoint draws on.
func reportHello(t *testing.T, env *testEnv, nodeID int64, infos ...nodes.AdapterInfo) {
	t.Helper()
	if err := nodes.RecordHello(context.Background(), env.store, nodeID, infos,
		0, "", time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatalf("RecordHello: %v", err)
	}
}

func schemasOf(t *testing.T, body string) []struct {
	Kind       string          `json:"kind"`
	Schema     json.RawMessage `json:"schema"`
	Offerable  bool            `json:"offerable"`
	Reason     string          `json:"reason"`
	HotUserAdd bool            `json:"hot_user_add"`
} {
	t.Helper()
	var out struct {
		Adapters []struct {
			Kind       string          `json:"kind"`
			Schema     json.RawMessage `json:"schema"`
			Offerable  bool            `json:"offerable"`
			Reason     string          `json:"reason"`
			HotUserAdd bool            `json:"hot_user_add"`
		} `json:"adapters"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode: %v (body %s)", err, body)
	}
	return out.Adapters
}

const wgSchema = `{"type":"object","required":["port"],"properties":{"port":{"type":"integer"}}}`

// The endpoint serves what the NODE reported, verbatim.
func TestServiceSchemasComeFromTheNodesOwnReport(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	nodeID := env.seedNode(t, "edge-1")
	reportHello(t, env, nodeID, nodes.AdapterInfo{
		Kind: "wireguard", Version: "1", ServiceSchema: []byte(wgSchema),
	})

	res := env.get(t, "/api/v1/nodes/"+itoa64(nodeID)+"/service-schemas", adminToken)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", res.Code, res.Body)
	}
	got := schemasOf(t, res.Body.String())
	if len(got) != 1 || got[0].Kind != "wireguard" {
		t.Fatalf("adapters = %+v, want one wireguard entry", got)
	}
	if !got[0].Offerable {
		t.Error("an adapter that reported a schema is not offerable")
	}
	// Verbatim: the panel validates against this exact document, so an editor
	// building a form from anything else could accept params the panel refuses.
	var served, reported map[string]any
	if err := json.Unmarshal(got[0].Schema, &served); err != nil {
		t.Fatalf("served schema is not JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(wgSchema), &reported); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if len(served) != len(reported) {
		t.Errorf("served schema differs from what the node reported:\n got %s\nwant %s",
			got[0].Schema, wgSchema)
	}
}

// THE distinction this endpoint exists for.
//
// nodes.KnownAdapters() says what this BUILD of the panel understands. A node's
// Hello says what that HOST can execute. Offering a protocol the panel knows
// and the node does not produces a service that can never be applied.
func TestOnlyProtocolsTheNodeReportsAreListed(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	nodeID := env.seedNode(t, "wireguard-only")

	// This host runs WireGuard alone, though the panel knows xray and singbox.
	reportHello(t, env, nodeID, nodes.AdapterInfo{
		Kind: "wireguard", Version: "1", ServiceSchema: []byte(wgSchema),
	})

	got := schemasOf(t, env.get(t,
		"/api/v1/nodes/"+itoa64(nodeID)+"/service-schemas", adminToken).Body.String())
	if len(got) != 1 {
		t.Fatalf("listed %d adapters for a node running one: %+v", len(got), got)
	}
	for _, a := range got {
		if a.Kind == "xray" || a.Kind == "singbox" {
			t.Errorf("offered %s, which the panel knows but this node does not run", a.Kind)
		}
	}
	// And the panel does know those kinds, so the absence above is the node's
	// report deciding rather than the panel being ignorant of them.
	if _, ok := nodes.KnownAdapters()["xray"]; !ok {
		t.Fatal("fixture assumption broken: the panel no longer knows xray, so " +
			"the assertion above proves nothing")
	}
}

// A node that has never connected reports nothing, and the answer is an empty
// list rather than a guess.
func TestNodeThatNeverConnectedOffersNothing(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	nodeID := env.seedNode(t, "never-seen")

	res := env.get(t, "/api/v1/nodes/"+itoa64(nodeID)+"/service-schemas", adminToken)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d", res.Code)
	}
	if got := schemasOf(t, res.Body.String()); len(got) != 0 {
		t.Errorf("offered %d protocols for a node that has never connected: %+v", len(got), got)
	}
}

// An adapter reported without a schema must not be offerable, and must say
// why. Without a schema the panel cannot validate params, so anything created
// would be unvalidated at best and unappliable at worst.
func TestAdapterWithoutASchemaIsNotOfferable(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	nodeID := env.seedNode(t, "old-agent")
	reportHello(t, env, nodeID, nodes.AdapterInfo{Kind: "wireguard", Version: "1"})

	got := schemasOf(t, env.get(t,
		"/api/v1/nodes/"+itoa64(nodeID)+"/service-schemas", adminToken).Body.String())
	if len(got) != 1 {
		t.Fatalf("adapters = %+v", got)
	}
	if got[0].Offerable {
		t.Error("an adapter that reported no schema is offerable; params could " +
			"not be validated and the service could never be applied")
	}
	if got[0].Reason == "" {
		t.Error("no reason given; the UI cannot explain why a protocol the node " +
			"runs is not on offer")
	}
}

// An agent too old to send a schema must not erase one already recorded.
// Blanking it would make a working protocol unofferable on upgrade.
func TestAnOlderAgentDoesNotEraseARecordedSchema(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	nodeID := env.seedNode(t, "edge-1")

	reportHello(t, env, nodeID, nodes.AdapterInfo{
		Kind: "wireguard", Version: "1", ServiceSchema: []byte(wgSchema),
	})
	// Reconnects reporting no schema at all.
	reportHello(t, env, nodeID, nodes.AdapterInfo{Kind: "wireguard", Version: "1"})

	got := schemasOf(t, env.get(t,
		"/api/v1/nodes/"+itoa64(nodeID)+"/service-schemas", adminToken).Body.String())
	if len(got) != 1 || !got[0].Offerable {
		t.Fatalf("the recorded schema was erased by a reconnect: %+v", got)
	}
}

// Capabilities are per node, not per adapter type: whether Xray can add a user
// without a restart depends on THAT host having configured a management API.
func TestCapabilitiesAreReportedPerNode(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	hot := env.seedNode(t, "with-api")
	cold := env.seedNode(t, "without-api")

	reportHello(t, env, hot, nodes.AdapterInfo{
		Kind: "xray", Version: "1", ServiceSchema: []byte(wgSchema), HotUserAdd: true,
	})
	reportHello(t, env, cold, nodes.AdapterInfo{
		Kind: "xray", Version: "1", ServiceSchema: []byte(wgSchema), HotUserAdd: false,
	})

	hotGot := schemasOf(t, env.get(t,
		"/api/v1/nodes/"+itoa64(hot)+"/service-schemas", adminToken).Body.String())
	coldGot := schemasOf(t, env.get(t,
		"/api/v1/nodes/"+itoa64(cold)+"/service-schemas", adminToken).Body.String())

	if !hotGot[0].HotUserAdd {
		t.Error("the node with a management API reports HotUserAdd = false")
	}
	if coldGot[0].HotUserAdd {
		t.Error("the node without a management API reports HotUserAdd = true; " +
			"the panel would plan hot adds that cannot work")
	}
}

// Same gate as every other node read.
func TestServiceSchemasRefuseAnOutOfScopeCaller(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	nodeID := env.seedNode(t, "secret-node")
	reportHello(t, env, nodeID, nodes.AdapterInfo{
		Kind: "wireguard", Version: "1", ServiceSchema: []byte(wgSchema),
	})
	env.seedAdmin(t, "vendor", "pw", "reseller")
	tenantToken := env.login(t, "vendor", "pw")

	path := "/api/v1/nodes/" + itoa64(nodeID) + "/service-schemas"
	if res := env.get(t, path, adminToken); res.Code != http.StatusOK {
		t.Fatalf("admin got %d, so the denial below would prove nothing", res.Code)
	}
	res := env.get(t, path, tenantToken)
	if res.Code == http.StatusOK {
		t.Errorf("a caller scoped to no node read it: %s",
			strings.TrimSpace(res.Body.String()))
	} else if res.Code != http.StatusForbidden {
		t.Errorf("got %d, want 403", res.Code)
	}
}
