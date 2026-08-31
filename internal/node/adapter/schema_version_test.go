package adapter_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// An agent must refuse a document from the future rather than apply part of it.
//
// This is not a theoretical concern. Fields added by a newer schema carry
// omitempty so that older documents stay byte-identical, and the same property
// means an old agent decodes a NEWER document without error and silently drops
// what it does not recognise. Without an explicit version check, an operator
// configuring an outbound would see the panel report convergence while the node
// routed traffic by its own defaults.
func TestAgentRefusesDocumentsFromTheFuture(t *testing.T) {
	if err := adapter.CheckSchemaVersion(adapter.MaxSchemaVersion + 1); err == nil {
		t.Error("accepted a document one version above what this agent understands")
	} else if !strings.Contains(err.Error(), "upgrade the agent") {
		t.Errorf("refusal does not tell the operator what to do: %v", err)
	}
}

func TestAgentAcceptsEverySupportedVersion(t *testing.T) {
	for v := 1; v <= adapter.MaxSchemaVersion; v++ {
		if err := adapter.CheckSchemaVersion(v); err != nil {
			t.Errorf("refused supported schema v%d: %v", v, err)
		}
	}
}

// A missing schema_version decodes as zero. Defaulting it to v1 would be a
// guess, and the guess is unsafe in exactly the case that matters: a document
// truncated or malformed in transit would be applied as if it were an old one.
func TestAgentRefusesAMissingVersion(t *testing.T) {
	if err := adapter.CheckSchemaVersion(0); err == nil {
		t.Error("accepted a document carrying no schema version")
	}
}

// The mirror types must actually round-trip the panel's wire format. They are
// hand-declared in this package rather than imported, so nothing but a test
// stops the two definitions drifting apart.
func TestDesiredDecodesEgressFields(t *testing.T) {
	const wire = `{
		"schema_version": 3,
		"revision": 9,
		"node_id": 2,
		"services": [],
		"subjects": [],
		"outbounds": [{"id": 1, "tag": "warp", "kind": "wireguard", "params": {"endpoint":"x"}}],
		"routing": {
			"rules": [{"id": 4, "priority": 10, "domains": ["example.com"], "outbound_tag": "warp"}],
			"default_outbound_tag": "direct"
		}
	}`

	var d adapter.Desired
	if err := json.Unmarshal([]byte(wire), &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.SchemaVersion != 3 {
		t.Errorf("SchemaVersion = %d, want 3", d.SchemaVersion)
	}
	if len(d.Outbounds) != 1 || d.Outbounds[0].Tag != "warp" {
		t.Fatalf("outbounds did not decode: %+v", d.Outbounds)
	}
	if string(d.Outbounds[0].Params) == "" {
		t.Error("outbound params dropped; the adapter would receive no configuration")
	}
	if d.Routing == nil {
		t.Fatal("routing did not decode")
	}
	if len(d.Routing.Rules) != 1 || d.Routing.Rules[0].OutboundTag != "warp" {
		t.Errorf("rules did not decode: %+v", d.Routing.Rules)
	}
	if d.Routing.Rules[0].Domains[0] != "example.com" {
		t.Errorf("rule matchers did not decode: %+v", d.Routing.Rules[0])
	}
	if d.Routing.DefaultOutboundTag != "direct" {
		t.Errorf("default outbound tag = %q, want direct", d.Routing.DefaultOutboundTag)
	}
}

// TestDesiredJSONTagsMatchPanelDocument pins the v2 shape at exactly five
// top-level keys, and still passes for v3 because the egress fields are
// omitempty and its fixture sets neither. That leaves the new fields outside
// the drift guard, so this covers the other half: a fully populated Desired
// must serialise to exactly the key set the panel's Document produces.
//
// The two types are declared separately on purpose -- this package must not
// import internal/panel -- so nothing but these two tests keeps them aligned.
func TestDesiredSerialisesEveryEgressKey(t *testing.T) {
	d := adapter.Desired{
		SchemaVersion: 3, Revision: 1, NodeID: 1,
		Services:  []adapter.Service{},
		Subjects:  []adapter.Subject{},
		Outbounds: []adapter.Outbound{{ID: 1, Tag: "t", Kind: "direct"}},
		Routing:   &adapter.Routing{Rules: []adapter.RoutingRule{{ID: 1, OutboundTag: "t"}}},
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := []string{"schema_version", "revision", "node_id", "services", "subjects", "outbounds", "routing"}
	for _, key := range want {
		if _, ok := generic[key]; !ok {
			t.Errorf("populated Desired is missing key %q", key)
		}
	}
	if len(generic) != len(want) {
		got := make([]string, 0, len(generic))
		for k := range generic {
			got = append(got, k)
		}
		t.Errorf("Desired has %d top-level keys, want %d; got %v",
			len(generic), len(want), got)
	}
}

// A v2 document must still decode cleanly, with the egress fields simply absent.
func TestDesiredDecodesV2WithoutEgress(t *testing.T) {
	const wire = `{"schema_version":2,"revision":1,"node_id":1,"services":[],"subjects":[]}`

	var d adapter.Desired
	if err := json.Unmarshal([]byte(wire), &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := adapter.CheckSchemaVersion(d.SchemaVersion); err != nil {
		t.Fatalf("v2 document refused: %v", err)
	}
	if d.Outbounds != nil {
		t.Errorf("outbounds should be nil for a v2 document, got %+v", d.Outbounds)
	}
	if d.Routing != nil {
		t.Errorf("routing should be nil for a v2 document, got %+v", d.Routing)
	}
}
