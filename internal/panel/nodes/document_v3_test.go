package nodes

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/amyrm/antimage/internal/shared/canonical"
)

// Schema v3 adds Outbounds and Routing. The compatibility claim is that a node
// given neither produces exactly what a v2 panel produced -- same bytes, same
// hash, no reconcile. These tests are that claim, stated so it cannot quietly
// stop being true.

// v2Document is the document shape as it stood before v3, reproduced here on
// purpose rather than imported.
//
// It is the control in the comparison below: if the live Document ever gains a
// field without omitempty, or emits schema_version 3 for a node with no egress
// state, the bytes diverge from this and the test fails. A test that built both
// sides from the same struct could not detect either.
type v2Document struct {
	SchemaVersion int       `json:"schema_version"`
	Revision      int64     `json:"revision"`
	NodeID        int64     `json:"node_id"`
	Services      []Service `json:"services"`
	Subjects      []Subject `json:"subjects"`
}

func TestDocumentWithoutEgressIsByteIdenticalToV2(t *testing.T) {
	services := []Service{{ID: 1, Kind: "xray", Enabled: true, Params: json.RawMessage(`{"port":443}`)}}
	subjects := []Subject{{ID: 7, Credentials: []Credential{{Kind: "uuid", Value: "abc"}}}}

	live := Document{Revision: 42, NodeID: 3, Services: services, Subjects: subjects}
	live.SchemaVersion = effectiveSchemaVersion(live)

	old := v2Document{
		SchemaVersion: 2, Revision: 42, NodeID: 3,
		Services: services, Subjects: subjects,
	}

	liveBytes, liveSum, err := canonical.Hash(live)
	if err != nil {
		t.Fatalf("hash live: %v", err)
	}
	oldBytes, oldSum, err := canonical.Hash(old)
	if err != nil {
		t.Fatalf("hash v2: %v", err)
	}

	if string(liveBytes) != string(oldBytes) {
		t.Errorf("v3 panel emits different bytes for a node with no egress state.\n v3: %s\n v2: %s\n"+
			"Every node in every fleet would see a new hash and reconcile for a feature it does not use.",
			liveBytes, oldBytes)
	}
	if liveSum != oldSum {
		t.Errorf("hash moved: %s != %s", liveSum, oldSum)
	}
}

// The test above proves effectiveSchemaVersion is correct. This proves
// BuildDesiredSnapshot actually USES it.
//
// The distinction is not academic: replacing the call with the constant
// DocumentSchemaVersion leaves every unit test on the helper passing while
// every node in every fleet gets a new hash. Only a test that goes through the
// real builder catches that, so this one does.
func TestBuiltSnapshotDeclaresV2WithoutEgress(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	snap := snapshot(t, s, nodeID)

	if snap.Document.SchemaVersion != 2 {
		t.Errorf("BuildDesiredSnapshot emitted schema v%d for a node with no egress state; "+
			"want v2, or every existing node reconciles for a feature it does not use",
			snap.Document.SchemaVersion)
	}
	if !strings.Contains(string(snap.Bytes), `"schema_version":2`) {
		t.Errorf("document bytes do not declare v2: %s", snap.Bytes)
	}
	for _, key := range []string{"outbounds", "routing"} {
		if strings.Contains(string(snap.Bytes), key) {
			t.Errorf("built document mentions %q despite having none: %s", key, snap.Bytes)
		}
	}
}

// The version must describe the content, not the panel's maximum.
func TestSchemaVersionFollowsContent(t *testing.T) {
	base := Document{Revision: 1, NodeID: 1}

	if got := effectiveSchemaVersion(base); got != 2 {
		t.Errorf("empty document declares v%d, want v2: a node with no egress "+
			"state must keep its existing hash", got)
	}

	withOutbound := base
	withOutbound.Outbounds = []Outbound{{ID: 1, Tag: "direct", Kind: "direct"}}
	if got := effectiveSchemaVersion(withOutbound); got != 3 {
		t.Errorf("document with an outbound declares v%d, want v3", got)
	}

	withRouting := base
	withRouting.Routing = &Routing{Rules: []RoutingRule{{ID: 1, OutboundTag: "direct"}}}
	if got := effectiveSchemaVersion(withRouting); got != 3 {
		t.Errorf("document with routing declares v%d, want v3", got)
	}

	// An empty, non-nil Routing still counts. It is a deliberate statement
	// that the node has a rule table with nothing in it, which is different
	// from "this panel never mentioned routing", and only a v3 agent can tell
	// those apart.
	withEmptyRouting := base
	withEmptyRouting.Routing = &Routing{}
	if got := effectiveSchemaVersion(withEmptyRouting); got != 3 {
		t.Errorf("document with an empty rule table declares v%d, want v3", got)
	}
}

func TestEgressFieldsAppearOnlyWhenSet(t *testing.T) {
	base := Document{Revision: 1, NodeID: 1}
	base.SchemaVersion = effectiveSchemaVersion(base)
	plain, _, err := canonical.Hash(base)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	for _, key := range []string{"outbounds", "routing"} {
		if strings.Contains(string(plain), key) {
			t.Errorf("%q appears in a document that sets neither: %s", key, plain)
		}
	}

	withEgress := base
	withEgress.Outbounds = []Outbound{{ID: 1, Tag: "warp", Kind: "wireguard"}}
	withEgress.SchemaVersion = effectiveSchemaVersion(withEgress)
	full, _, err := canonical.Hash(withEgress)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !strings.Contains(string(full), `"outbounds"`) {
		t.Errorf("outbounds set but absent from the document: %s", full)
	}
	if !strings.Contains(string(full), `"schema_version":3`) {
		t.Errorf("document carrying an outbound does not declare v3: %s", full)
	}
}

// Canonical serialization is only deterministic if the collections are ordered.
// Services and subjects already are; the new collections must be too, or two
// builds of identical state produce different hashes and the node reconciles
// forever.
func TestEgressOrderingIsDeterministic(t *testing.T) {
	scrambled := Document{
		Revision: 1, NodeID: 1,
		Outbounds: []Outbound{
			{ID: 9, Tag: "c", Kind: "direct"},
			{ID: 2, Tag: "a", Kind: "direct"},
			{ID: 5, Tag: "b", Kind: "direct"},
		},
		Routing: &Routing{Rules: []RoutingRule{
			{ID: 3, Priority: 20, OutboundTag: "c"},
			{ID: 1, Priority: 10, OutboundTag: "a"},
			{ID: 2, Priority: 10, OutboundTag: "b"},
		}},
	}
	sortOutbounds(scrambled.Outbounds)
	sortRoutingRules(scrambled.Routing.Rules)

	gotOut := []int64{}
	for _, o := range scrambled.Outbounds {
		gotOut = append(gotOut, o.ID)
	}
	if gotOut[0] != 2 || gotOut[1] != 5 || gotOut[2] != 9 {
		t.Errorf("outbounds not sorted by id: %v", gotOut)
	}

	// Priority first, then id to break the tie between rules 1 and 2.
	gotRules := []int64{}
	for _, r := range scrambled.Routing.Rules {
		gotRules = append(gotRules, r.ID)
	}
	if gotRules[0] != 1 || gotRules[1] != 2 || gotRules[2] != 3 {
		t.Errorf("rules not ordered by (priority, id): %v", gotRules)
	}
}

// DocumentSchemaVersion is the ceiling the panel may emit, and the agent
// declares its own. If the panel's ceiling ever exceeds what the shipped agent
// understands, every node refuses its document -- which is safe, but it is a
// fleet-wide outage, so the two constants moving together is worth pinning.
func TestPanelCeilingMatchesTheNewestSchemaVersion(t *testing.T) {
	if DocumentSchemaVersion != schemaVersionBalancer {
		t.Errorf("DocumentSchemaVersion = %d but the newest schema is v%d",
			DocumentSchemaVersion, schemaVersionBalancer)
	}
}
