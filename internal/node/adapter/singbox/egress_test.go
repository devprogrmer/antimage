package singbox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amyrm/antimage/internal/node/adapter"
)

func decodeDoc(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("generated document is not valid JSON: %v\n%s", err, raw)
	}
	return doc
}

func TestNoEgressProducesNoDocument(t *testing.T) {
	raw, err := GenerateEgressConfig(nil, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if raw != nil {
		t.Errorf("empty egress produced a document: %s", raw)
	}
}

// sing-box names the field "type" where Xray uses "protocol", and calls direct
// and block exactly that rather than freedom and blackhole. The document's
// vocabulary is the panel's, so the mapping has to happen here.
func TestOutboundKindsMapToSingBoxTypes(t *testing.T) {
	cases := []struct {
		kind   string
		params string
		sbType string
	}{
		{"direct", ``, "direct"},
		{"block", ``, "block"},
		{"socks", `{"address":"1.2.3.4","port":1080}`, "socks"},
		{"http", `{"address":"1.2.3.4","port":8080}`, "http"},
		{"wireguard", `{"private_key":"k","peer_public_key":"p","endpoint":"1.2.3.4:51820"}`, "wireguard"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			o := adapter.Outbound{ID: 1, Tag: "t", Kind: tc.kind}
			if tc.params != "" {
				o.Params = json.RawMessage(tc.params)
			}
			raw, err := GenerateEgressConfig([]adapter.Outbound{o}, nil)
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			outs, _ := decodeDoc(t, raw)["outbounds"].([]any)
			if len(outs) != 1 {
				t.Fatalf("want 1 outbound, got %d", len(outs))
			}
			got, _ := outs[0].(map[string]any)
			if got["type"] != tc.sbType {
				t.Errorf("type = %v, want %q", got["type"], tc.sbType)
			}
		})
	}
}

// WireGuard carries one endpoint string; sing-box wants server and server_port
// as separate fields.
func TestWireguardEndpointIsSplit(t *testing.T) {
	raw, err := GenerateEgressConfig([]adapter.Outbound{{
		ID: 1, Tag: "wg", Kind: "wireguard",
		Params: json.RawMessage(`{"private_key":"k","peer_public_key":"p","endpoint":"10.0.0.1:51820"}`),
	}}, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	outs, _ := decodeDoc(t, raw)["outbounds"].([]any)
	got, _ := outs[0].(map[string]any)
	if got["server"] != "10.0.0.1" {
		t.Errorf("server = %v, want 10.0.0.1", got["server"])
	}
	if got["server_port"] != float64(51820) {
		t.Errorf("server_port = %v, want 51820", got["server_port"])
	}
}

func TestMalformedWireguardEndpointIsRefused(t *testing.T) {
	for _, endpoint := range []string{"noport", "host:notanumber", ":51820", "host:0"} {
		t.Run(endpoint, func(t *testing.T) {
			_, err := GenerateEgressConfig([]adapter.Outbound{{
				ID: 1, Tag: "wg", Kind: "wireguard",
				Params: json.RawMessage(
					`{"private_key":"k","peer_public_key":"p","endpoint":"` + endpoint + `"}`),
			}}, nil)
			if err == nil {
				t.Errorf("accepted malformed endpoint %q", endpoint)
			}
		})
	}
}

// sing-box has a real "final" field for unmatched traffic, so the default is
// expressed directly rather than as the trailing catch-all rule Xray needs.
func TestDefaultOutboundBecomesFinal(t *testing.T) {
	raw, err := GenerateEgressConfig(
		[]adapter.Outbound{{ID: 1, Tag: "fallback", Kind: "block"}},
		&adapter.Routing{DefaultOutboundTag: "fallback"},
	)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	route, _ := decodeDoc(t, raw)["route"].(map[string]any)
	if route["final"] != "fallback" {
		t.Errorf("route.final = %v, want fallback", route["final"])
	}
}

// sing-box separates matchers Xray combines: domain and geosite are distinct
// fields, as are ip_cidr and geoip.
func TestMatchersUseSeparateFields(t *testing.T) {
	raw, err := GenerateEgressConfig(
		[]adapter.Outbound{{ID: 1, Tag: "t", Kind: "block"}},
		&adapter.Routing{Rules: []adapter.RoutingRule{{
			ID: 1, OutboundTag: "t",
			Domains: []string{"a.com"}, GeoSite: []string{"cn"},
			IPCIDRs: []string{"10.0.0.0/8"}, GeoIP: []string{"private"},
		}}},
	)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	route, _ := decodeDoc(t, raw)["route"].(map[string]any)
	rules, _ := route["rules"].([]any)
	rule, _ := rules[0].(map[string]any)

	for _, field := range []string{"domain", "geosite", "ip_cidr", "geoip"} {
		if _, ok := rule[field]; !ok {
			t.Errorf("rule is missing %q: %+v", field, rule)
		}
	}
}

// sing-box takes ports as numbers. A non-numeric port must be refused rather
// than dropped, because dropping the matcher widens the rule to every port.
func TestNonNumericPortIsRefused(t *testing.T) {
	_, err := GenerateEgressConfig(
		[]adapter.Outbound{{ID: 1, Tag: "t", Kind: "block"}},
		&adapter.Routing{Rules: []adapter.RoutingRule{
			{ID: 1, Ports: []string{"1000-2000"}, OutboundTag: "t"},
		}},
	)
	if err == nil {
		t.Error("accepted a port range; dropping the matcher would widen the rule to all ports")
	}
}

func TestRuleWithoutMatchersIsRefused(t *testing.T) {
	_, err := GenerateEgressConfig(
		[]adapter.Outbound{{ID: 1, Tag: "t", Kind: "block"}},
		&adapter.Routing{Rules: []adapter.RoutingRule{{ID: 1, OutboundTag: "t"}}},
	)
	if err == nil {
		t.Error("accepted a rule with no matchers; sing-box would apply it to all traffic")
	}
}

func TestRuleReferencingUnknownOutboundIsRefused(t *testing.T) {
	_, err := GenerateEgressConfig(nil, &adapter.Routing{
		Rules: []adapter.RoutingRule{{ID: 1, Domains: []string{"x.com"}, OutboundTag: "nope"}},
	})
	if err == nil {
		t.Error("accepted a rule selecting an undefined outbound")
	}
}

func TestUnknownOutboundKindIsRefused(t *testing.T) {
	_, err := GenerateEgressConfig(
		[]adapter.Outbound{{ID: 1, Tag: "t", Kind: "quantum-tunnel"}}, nil)
	if err == nil {
		t.Error("accepted an unsupported outbound kind")
	}
}

func TestDuplicateTagsAreRefused(t *testing.T) {
	_, err := GenerateEgressConfig([]adapter.Outbound{
		{ID: 1, Tag: "same", Kind: "direct"},
		{ID: 2, Tag: "same", Kind: "block"},
	}, nil)
	if err == nil {
		t.Error("accepted two outbounds sharing a tag; a rule naming it would be ambiguous")
	}
}

func TestGenerationIsDeterministic(t *testing.T) {
	outs := []adapter.Outbound{{ID: 1, Tag: "t", Kind: "block"}}
	routing := &adapter.Routing{Rules: []adapter.RoutingRule{
		{ID: 1, Domains: []string{"a.com"}, SubjectIDs: []int64{9, 3, 5}, OutboundTag: "t"},
	}}
	first, err := GenerateEgressConfig(outs, routing)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	for i := 0; i < 20; i++ {
		again, err := GenerateEgressConfig(outs, routing)
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if string(again) != string(first) {
			t.Fatalf("call %d differs — the document hash would never settle", i)
		}
	}
}

func TestRulesFollowPriorityThenID(t *testing.T) {
	raw, err := GenerateEgressConfig(
		[]adapter.Outbound{
			{ID: 1, Tag: "a", Kind: "block"},
			{ID: 2, Tag: "b", Kind: "block"},
			{ID: 3, Tag: "c", Kind: "block"},
		},
		&adapter.Routing{Rules: []adapter.RoutingRule{
			{ID: 30, Priority: 20, Domains: []string{"c.com"}, OutboundTag: "c"},
			{ID: 20, Priority: 10, Domains: []string{"b.com"}, OutboundTag: "b"},
			{ID: 10, Priority: 10, Domains: []string{"a.com"}, OutboundTag: "a"},
		}},
	)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	route, _ := decodeDoc(t, raw)["route"].(map[string]any)
	rules, _ := route["rules"].([]any)
	for i, tag := range []string{"a", "b", "c"} {
		got, _ := rules[i].(map[string]any)
		if got["outbound"] != tag {
			t.Errorf("rule %d selects %v, want %q", i, got["outbound"], tag)
		}
	}
}

// The generated document must be a complete sing-box document, not a bare
// fragment. sing-box decodes every file in its config directory against the
// top-level schema and exits fatally on an unknown field.
func TestDocumentIsAWholeConfig(t *testing.T) {
	raw, err := GenerateEgressConfig(
		[]adapter.Outbound{{ID: 1, Tag: "t", Kind: "direct"}}, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	doc := decodeDoc(t, raw)
	if _, ok := doc["outbounds"]; !ok {
		t.Errorf("document has no top-level outbounds key: %s", raw)
	}
	for key := range doc {
		switch key {
		case "outbounds", "route":
		default:
			t.Errorf("document carries unexpected top-level key %q; sing-box exits "+
				"fatally on an unknown field: %s", key, raw)
		}
	}
}

// -- lifecycle ---------------------------------------------------------------

func egressAdapter(t *testing.T) (*Adapter, *fakeRuntime, string) {
	t.Helper()
	dir := t.TempDir()
	rt := &fakeRuntime{healthy: true}
	return New(dir, rt), rt, dir
}

func desiredWithEgress() adapter.Desired {
	return adapter.Desired{
		SchemaVersion: 3, Revision: 1, NodeID: 1,
		Outbounds: []adapter.Outbound{{ID: 1, Tag: "warp", Kind: "block"}},
		Routing: &adapter.Routing{Rules: []adapter.RoutingRule{
			{ID: 1, Priority: 10, Domains: []string{"example.com"}, OutboundTag: "warp"},
		}},
	}
}

func applyAll(t *testing.T, a *Adapter, plan adapter.Plan) {
	t.Helper()
	for _, step := range plan.Steps {
		res, err := a.Apply(context.Background(), step)
		if err != nil {
			t.Fatalf("apply %s: %v", step.Kind, err)
		}
		if !res.OK {
			t.Fatalf("apply %s not ok: %s", step.Kind, res.Err)
		}
	}
}

func TestEgressConvergesAndStaysConverged(t *testing.T) {
	a, _, dir := egressAdapter(t)
	ctx := context.Background()
	desired := desiredWithEgress()

	obs, err := a.Observe(ctx)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	plan, err := a.Plan(ctx, desired, obs)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Steps) != 1 || plan.Steps[0].Kind != StepWriteEgress {
		t.Fatalf("want one write_egress step, got %+v", plan.Steps)
	}
	if plan.Steps[0].Disruption != adapter.DisruptRestart {
		t.Errorf("egress write is %v, want restart", plan.Steps[0].Disruption)
	}
	applyAll(t, a, plan)

	if _, err := os.Stat(filepath.Join(dir, egressFile)); err != nil {
		t.Fatalf("egress file not written: %v", err)
	}

	obs, err = a.Observe(ctx)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Egress == nil || !obs.Egress.Managed {
		t.Fatalf("written egress not observed as managed: %+v", obs.Egress)
	}
	plan, err = a.Plan(ctx, desired, obs)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Steps) != 0 {
		t.Errorf("converged state still plans %d steps: %+v", len(plan.Steps), plan.Steps)
	}
}

func TestEgressDriftIsDetected(t *testing.T) {
	a, _, dir := egressAdapter(t)
	ctx := context.Background()
	desired := desiredWithEgress()

	obs, _ := a.Observe(ctx)
	plan, _ := a.Plan(ctx, desired, obs)
	applyAll(t, a, plan)

	if err := os.WriteFile(filepath.Join(dir, egressFile),
		[]byte(`{"outbounds":[{"tag":"warp","type":"direct"}]}`), 0o600); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	obs, err := a.Observe(ctx)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	plan, err = a.Plan(ctx, desired, obs)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Steps) != 1 || plan.Steps[0].Kind != StepWriteEgress {
		t.Errorf("hand edit to the routing table produced no correction: %+v", plan.Steps)
	}
}

func TestUnmanagedEgressFileIsRefused(t *testing.T) {
	a, _, dir := egressAdapter(t)
	ctx := context.Background()

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// No marker sidecar: somebody put this here.
	if err := os.WriteFile(filepath.Join(dir, egressFile),
		[]byte(`{"outbounds":[{"tag":"hand","type":"direct"}]}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	obs, err := a.Observe(ctx)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Egress == nil || obs.Egress.Managed {
		t.Fatalf("hand-written egress reported as managed: %+v", obs.Egress)
	}
	if _, err := a.Plan(ctx, desiredWithEgress(), obs); err == nil {
		t.Error("planned over a hand-written egress file instead of refusing")
	}
}

// Removal must take the marker with it. A marker left behind would make a
// later hand-written file look managed, and this adapter would overwrite
// something a human put there.
func TestEgressRemovalTakesTheMarker(t *testing.T) {
	a, _, dir := egressAdapter(t)
	ctx := context.Background()

	obs, _ := a.Observe(ctx)
	plan, _ := a.Plan(ctx, desiredWithEgress(), obs)
	applyAll(t, a, plan)

	obs, _ = a.Observe(ctx)
	plan, err := a.Plan(ctx, adapter.Desired{SchemaVersion: 2, Revision: 2, NodeID: 1}, obs)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Steps) != 1 || plan.Steps[0].Kind != StepRemoveEgress {
		t.Fatalf("want one remove_egress step, got %+v", plan.Steps)
	}
	applyAll(t, a, plan)

	for _, name := range []string{egressFile, egressFile + markerSuffix} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s survived removal: %v", name, err)
		}
	}
}

func TestV2DocumentPlansNoEgressWork(t *testing.T) {
	a, _, _ := egressAdapter(t)
	ctx := context.Background()

	obs, _ := a.Observe(ctx)
	plan, err := a.Plan(ctx, adapter.Desired{SchemaVersion: 2, Revision: 1, NodeID: 1}, obs)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Steps) != 0 {
		t.Errorf("v2 document produced egress work: %+v", plan.Steps)
	}
}

// The egress file must not be mistaken for a service. Its name has no numeric
// id, so a planner that let it fall through to the service path would try to
// parse "egress" as an int64.
func TestEgressIsNotSeenAsAService(t *testing.T) {
	a, _, _ := egressAdapter(t)
	ctx := context.Background()

	obs, _ := a.Observe(ctx)
	plan, _ := a.Plan(ctx, desiredWithEgress(), obs)
	applyAll(t, a, plan)

	obs, err := a.Observe(ctx)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if len(obs.Services) != 0 {
		t.Errorf("egress file observed as %d service(s): %+v", len(obs.Services), obs.Services)
	}
}

func TestEgressDocumentMentionsNoSecretsInMarker(t *testing.T) {
	a, _, dir := egressAdapter(t)
	ctx := context.Background()

	desired := adapter.Desired{
		SchemaVersion: 3, Revision: 1, NodeID: 1,
		Outbounds: []adapter.Outbound{{
			ID: 1, Tag: "wg", Kind: "wireguard",
			Params: json.RawMessage(
				`{"private_key":"SUPERSECRET","peer_public_key":"p","endpoint":"1.2.3.4:51820"}`),
		}},
	}
	obs, _ := a.Observe(ctx)
	plan, err := a.Plan(ctx, desired, obs)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	applyAll(t, a, plan)

	marker, err := os.ReadFile(filepath.Join(dir, egressFile+markerSuffix))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if strings.Contains(string(marker), "SUPERSECRET") {
		t.Error("the marker sidecar contains the outbound private key")
	}
}
