package adapter

import (
	"encoding/json"
	"testing"
)

func TestDisruptionOrdersBySeverity(t *testing.T) {
	if DisruptNone >= DisruptReload || DisruptReload >= DisruptRestart {
		t.Fatal("Disruption constants must order none < reload < restart so " +
			"MaxDisruption can compare them")
	}
}

func TestDisruptionStrings(t *testing.T) {
	for d, want := range map[Disruption]string{
		DisruptNone: "none", DisruptReload: "reload", DisruptRestart: "restart",
	} {
		if got := d.String(); got != want {
			t.Errorf("Disruption(%d).String() = %q, want %q", d, got, want)
		}
	}
	if got := Disruption(99).String(); got != "unknown" {
		t.Errorf("unknown disruption = %q, want \"unknown\"", got)
	}
}

func TestEmptyPlanIsEmpty(t *testing.T) {
	if !(Plan{}).IsEmpty() {
		t.Error("zero Plan is not empty")
	}
	if (Plan{Steps: []Step{{Seq: 1}}}).IsEmpty() {
		t.Error("plan with a step reported empty")
	}
}

func TestMaxDisruptionPicksWorstStep(t *testing.T) {
	p := Plan{Steps: []Step{
		{Seq: 1, Disruption: DisruptNone},
		{Seq: 2, Disruption: DisruptRestart},
		{Seq: 3, Disruption: DisruptReload},
	}}
	if got := p.MaxDisruption(); got != DisruptRestart {
		t.Errorf("MaxDisruption = %v, want restart", got)
	}
	if got := (Plan{}).MaxDisruption(); got != DisruptNone {
		t.Errorf("empty plan MaxDisruption = %v, want none", got)
	}
}

// The desired document arrives as canonical JSON from the panel. Round
// tripping must preserve every field, including nulls, because the agent
// re-verifies the hash before applying.
func TestDesiredRoundTripsCanonicalJSON(t *testing.T) {
	raw := `{"node_id":7,"revision":3,"schema_version":1,"services":[{"enabled":true,` +
		`"id":10,"kind":"stub","params":{"port":443}}],"subjects":null}`
	var d Desired
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d.Revision != 3 || d.NodeID != 7 || len(d.Services) != 1 {
		t.Fatalf("decoded = %+v, want revision 3, node 7, one service", d)
	}
	if d.Services[0].Kind != "stub" || !d.Services[0].Enabled {
		t.Errorf("service = %+v", d.Services[0])
	}
}

// TestDesiredJSONTagsMatchPanelDocument asserts, byte-for-byte, that the wire
// tags on Desired/Service/Subject/Credential match internal/panel/nodes.
// Document exactly. adapter.Desired is a deliberately separate type from the
// panel's nodes.Document (this package must not import internal/panel), so
// nothing but this test keeps the two aligned. Subject and Credential are
// never populated in this sub-project, so nothing else exercises their tags
// until a later sub-project does -- by which point a mismatch would surface
// as a cross-package wire break in an integration test rather than here.
func TestDesiredJSONTagsMatchPanelDocument(t *testing.T) {
	d := Desired{
		SchemaVersion: 1,
		Revision:      3,
		NodeID:        7,
		Services: []Service{
			{ID: 10, Kind: "stub", Enabled: true, Params: json.RawMessage(`{"port":443}`)},
		},
		Subjects: []Subject{
			{
				ID: 5,
				Credentials: []Credential{
					{Kind: "uuid", Value: "abc-123"},
				},
			},
		},
	}

	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var generic map[string]json.RawMessage
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("unmarshal to generic map: %v", err)
	}

	// Document: schema_version, revision, node_id, services, subjects
	for _, key := range []string{"schema_version", "revision", "node_id", "services", "subjects"} {
		if _, ok := generic[key]; !ok {
			t.Errorf("Desired JSON missing key %q; got keys %v", key, keysOf(generic))
		}
	}
	if len(generic) != 5 {
		t.Errorf("Desired JSON has %d top-level keys, want 5; got %v", len(generic), keysOf(generic))
	}

	var services []map[string]json.RawMessage
	if err := json.Unmarshal(generic["services"], &services); err != nil {
		t.Fatalf("unmarshal services: %v", err)
	}
	if len(services) != 1 {
		t.Fatalf("want 1 service, got %d", len(services))
	}
	// Service: id, kind, enabled, params
	for _, key := range []string{"id", "kind", "enabled", "params"} {
		if _, ok := services[0][key]; !ok {
			t.Errorf("Service JSON missing key %q; got keys %v", key, keysOf(services[0]))
		}
	}
	if len(services[0]) != 4 {
		t.Errorf("Service JSON has %d keys, want 4; got %v", len(services[0]), keysOf(services[0]))
	}

	var subjects []map[string]json.RawMessage
	if err := json.Unmarshal(generic["subjects"], &subjects); err != nil {
		t.Fatalf("unmarshal subjects: %v", err)
	}
	if len(subjects) != 1 {
		t.Fatalf("want 1 subject, got %d", len(subjects))
	}
	// Subject: id, credentials
	for _, key := range []string{"id", "credentials"} {
		if _, ok := subjects[0][key]; !ok {
			t.Errorf("Subject JSON missing key %q; got keys %v", key, keysOf(subjects[0]))
		}
	}
	if len(subjects[0]) != 2 {
		t.Errorf("Subject JSON has %d keys, want 2; got %v", len(subjects[0]), keysOf(subjects[0]))
	}

	var credentials []map[string]json.RawMessage
	if err := json.Unmarshal(subjects[0]["credentials"], &credentials); err != nil {
		t.Fatalf("unmarshal credentials: %v", err)
	}
	if len(credentials) != 1 {
		t.Fatalf("want 1 credential, got %d", len(credentials))
	}
	// Credential: kind, value
	for _, key := range []string{"kind", "value"} {
		if _, ok := credentials[0][key]; !ok {
			t.Errorf("Credential JSON missing key %q; got keys %v", key, keysOf(credentials[0]))
		}
	}
	if len(credentials[0]) != 2 {
		t.Errorf("Credential JSON has %d keys, want 2; got %v", len(credentials[0]), keysOf(credentials[0]))
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
