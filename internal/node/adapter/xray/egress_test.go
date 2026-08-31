package xray

import (
	"encoding/json"
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

func rulesOf(t *testing.T, doc map[string]any) []any {
	t.Helper()
	routing, ok := doc["routing"].(map[string]any)
	if !ok {
		t.Fatalf("document has no routing block: %+v", doc)
	}
	rules, ok := routing["rules"].([]any)
	if !ok {
		t.Fatalf("routing has no rules array: %+v", routing)
	}
	return rules
}

// Nothing desired must produce nothing, not an empty document.
//
// An empty routing block is not inert in Xray, and a file that exists with no
// rules is harder to reason about during an incident than no file at all.
func TestNoEgressProducesNoDocument(t *testing.T) {
	raw, err := GenerateEgressConfig(nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if raw != nil {
		t.Errorf("empty egress produced a document: %s", raw)
	}
}

// THE ordering hazard. Xray merges confdir documents in filename order and
// APPENDS rule arrays, and "antimage-egress.json" sorts before
// "antimage-stats.json". A plain operator rule carrying no inboundTag would
// therefore be evaluated before the rule that keeps the accounting API's own
// traffic on the api outbound -- silently sending stats queries through WARP,
// or wherever, and breaking accounting for the whole node.
//
// The egress document repeats the accounting rule first to make its own
// ordering independent of what the confdir files happen to be called.
func TestAccountingRuleIsEvaluatedFirst(t *testing.T) {
	raw, err := GenerateEgressConfig(
		[]adapter.Outbound{{ID: 1, Tag: "warp", Kind: "block"}},
		&adapter.Routing{Rules: []adapter.RoutingRule{
			// No inboundTag: matches every inbound, including api-inbound.
			{ID: 1, Priority: 1, Domains: []string{"example.com"}, OutboundTag: "warp"},
		}}, nil, nil,
	)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	rules := rulesOf(t, decodeDoc(t, raw))
	if len(rules) < 2 {
		t.Fatalf("expected the accounting rule plus the operator rule, got %d", len(rules))
	}

	first, _ := rules[0].(map[string]any)
	if first["outboundTag"] != tagAPI {
		t.Errorf("first rule does not protect the accounting API: %+v\n"+
			"An operator rule evaluated before it would capture stats traffic and "+
			"break accounting for the whole node.", first)
	}
	inbound, _ := first["inboundTag"].([]any)
	if len(inbound) != 1 || inbound[0] != tagAPIInbound {
		t.Errorf("first rule does not match the api inbound: %+v", first)
	}
}

// The stats document already defines "direct" and "api". Xray appends rather
// than overrides, and resolves duplicate tags by first match, so an operator
// outbound with one of those tags would silently never be used.
func TestReservedTagsAreRefused(t *testing.T) {
	for _, tag := range []string{tagDirect, tagAPI} {
		t.Run(tag, func(t *testing.T) {
			_, err := GenerateEgressConfig(
				[]adapter.Outbound{{ID: 1, Tag: tag, Kind: "direct"}}, nil, nil, nil)
			if err == nil {
				t.Errorf("accepted reserved tag %q; it would be silently shadowed "+
					"by the accounting document's outbound", tag)
			}
		})
	}
}

// But a rule may still REFERENCE direct: sending traffic straight out is the
// most common rule an operator writes, and the stats document already provides
// exactly that outbound.
func TestDirectMayBeReferencedByARule(t *testing.T) {
	raw, err := GenerateEgressConfig(nil, &adapter.Routing{
		Rules: []adapter.RoutingRule{
			{ID: 1, GeoIP: []string{"private"}, OutboundTag: tagDirect},
		},
	}, nil, nil)
	if err != nil {
		t.Fatalf("refused a rule targeting the pre-existing direct outbound: %v", err)
	}
	if !strings.Contains(string(raw), `"geoip:private"`) {
		t.Errorf("geoip matcher not rendered: %s", raw)
	}
}

func TestDuplicateTagsAreRefused(t *testing.T) {
	_, err := GenerateEgressConfig([]adapter.Outbound{
		{ID: 1, Tag: "same", Kind: "direct"},
		{ID: 2, Tag: "same", Kind: "block"},
	}, nil, nil, nil)
	if err == nil {
		t.Error("accepted two outbounds sharing a tag; Xray resolves duplicates " +
			"by first match, so one would silently never be used")
	}
}

// A rule selecting an outbound that does not exist is refused at render time.
// Xray would start happily and let the traffic fall through to the default,
// which looks like the rule working until somebody checks where packets went.
func TestRuleReferencingUnknownOutboundIsRefused(t *testing.T) {
	_, err := GenerateEgressConfig(nil, &adapter.Routing{
		Rules: []adapter.RoutingRule{{ID: 1, Domains: []string{"x.com"}, OutboundTag: "nope"}},
	}, nil, nil)
	if err == nil {
		t.Error("accepted a rule selecting an undefined outbound")
	}
}

// A rule with no matchers matches everything in Xray. An operator who left all
// matchers empty did not mean "route all traffic here".
func TestRuleWithoutMatchersIsRefused(t *testing.T) {
	_, err := GenerateEgressConfig(
		[]adapter.Outbound{{ID: 1, Tag: "t", Kind: "block"}},
		&adapter.Routing{Rules: []adapter.RoutingRule{{ID: 1, OutboundTag: "t"}}}, nil, nil,
	)
	if err == nil {
		t.Error("accepted a rule with no matchers; Xray would apply it to all traffic")
	}
}

// An unknown kind must never reach Xray: it produces a process that refuses to
// start, which takes down every inbound on the node, not just this outbound.
func TestUnknownOutboundKindIsRefused(t *testing.T) {
	_, err := GenerateEgressConfig(
		[]adapter.Outbound{{ID: 1, Tag: "t", Kind: "quantum-tunnel"}}, nil, nil, nil)
	if err == nil {
		t.Error("accepted an unsupported outbound kind")
	}
}

func TestOutboundKindsRender(t *testing.T) {
	cases := []struct {
		kind     string
		params   string
		protocol string
	}{
		{"direct", ``, "freedom"},
		{"block", ``, "blackhole"},
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
			raw, err := GenerateEgressConfig([]adapter.Outbound{o}, nil, nil, nil)
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			outs, _ := decodeDoc(t, raw)["outbounds"].([]any)
			if len(outs) != 1 {
				t.Fatalf("want 1 outbound, got %d", len(outs))
			}
			got, _ := outs[0].(map[string]any)
			if got["protocol"] != tc.protocol {
				t.Errorf("protocol = %v, want %q", got["protocol"], tc.protocol)
			}
		})
	}
}

// Every kind whose params are mandatory must be refused without them, rather
// than rendering an outbound Xray cannot use.
func TestOutboundsRequireTheirParams(t *testing.T) {
	for _, kind := range []string{"socks", "http", "wireguard"} {
		t.Run(kind, func(t *testing.T) {
			_, err := GenerateEgressConfig(
				[]adapter.Outbound{{ID: 1, Tag: "t", Kind: kind}}, nil, nil, nil)
			if err == nil {
				t.Errorf("%s rendered with no params", kind)
			}
		})
	}
}

// Rule order is the operator's policy. It must follow priority, and ties must
// break deterministically or the document hash is unstable and the node never
// converges.
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
		}}, nil, nil,
	)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	rules := rulesOf(t, decodeDoc(t, raw))

	// rules[0] is the accounting rule.
	want := []string{"a", "b", "c"}
	for i, tag := range want {
		got, _ := rules[i+1].(map[string]any)
		if got["outboundTag"] != tag {
			t.Errorf("rule %d selects %v, want %q — priority then id ordering broken",
				i, got["outboundTag"], tag)
		}
	}
}

func TestGenerationIsDeterministic(t *testing.T) {
	outs := []adapter.Outbound{{ID: 1, Tag: "t", Kind: "block"}}
	routing := &adapter.Routing{Rules: []adapter.RoutingRule{
		{ID: 1, Domains: []string{"a.com", "b.com"}, SubjectIDs: []int64{9, 3, 5}, OutboundTag: "t"},
	}}
	first, err := GenerateEgressConfig(outs, routing, nil, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	for i := 0; i < 20; i++ {
		again, err := GenerateEgressConfig(outs, routing, nil, nil)
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if string(again) != string(first) {
			t.Fatalf("call %d differs — the document hash would never settle:\n%s\nvs\n%s",
				i, again, first)
		}
	}
}

// A default outbound becomes a trailing catch-all rule rather than relying on
// outbound ORDER, which the operator does not control across merged documents.
func TestDefaultOutboundBecomesATrailingRule(t *testing.T) {
	raw, err := GenerateEgressConfig(
		[]adapter.Outbound{{ID: 1, Tag: "fallback", Kind: "block"}},
		&adapter.Routing{DefaultOutboundTag: "fallback"}, nil, nil,
	)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	rules := rulesOf(t, decodeDoc(t, raw))
	last, _ := rules[len(rules)-1].(map[string]any)
	if last["outboundTag"] != "fallback" {
		t.Errorf("last rule is not the default: %+v", last)
	}
	if last["network"] != "tcp,udp" {
		t.Errorf("default rule does not match all networks: %+v", last)
	}
}

func TestUnknownDefaultOutboundIsRefused(t *testing.T) {
	_, err := GenerateEgressConfig(nil, &adapter.Routing{DefaultOutboundTag: "missing"}, nil, nil)
	if err == nil {
		t.Error("accepted a default outbound this node does not define")
	}
}

// Subject ids become the same email tags accounting aggregates by, so a rule
// and a usage report cannot disagree about who they refer to.
//
// Since C2 that tag is per-service, so a rule naming one subject has to name
// them once per inbound they are on. Listing only one would leave a rule that
// matches nothing on every other inbound: the operator's policy would sit in
// the config looking correct and silently never fire.
func TestSubjectRulesUseAccountingEmails(t *testing.T) {
	raw, err := GenerateEgressConfig(
		[]adapter.Outbound{{ID: 1, Tag: "t", Kind: "block"}},
		&adapter.Routing{Rules: []adapter.RoutingRule{
			{ID: 1, SubjectIDs: []int64{42}, OutboundTag: "t"},
		}}, []int64{7, 9}, nil,
	)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	for _, svc := range []int64{7, 9} {
		want := subjectEmail(42, svc)
		if !strings.Contains(string(raw), want) {
			t.Errorf("rule does not name %s, so it would not match subject 42 "+
				"on service %d: %s", want, svc, raw)
		}
	}
	// The node-wide form matches no user since C2, so emitting it would be a
	// rule that silently does nothing.
	if strings.Contains(string(raw), `"subject-42@antimage"`) {
		t.Errorf("rule still uses the legacy node-wide tag, which now matches "+
			"no user at all: %s", raw)
	}
}

// A node with no inbounds has nothing for a rule to match. The document still
// has to be well formed, because an invalid egress config is a process that
// will not start.
func TestSubjectRulesWithNoServicesStayWellFormed(t *testing.T) {
	raw, err := GenerateEgressConfig(
		[]adapter.Outbound{{ID: 1, Tag: "t", Kind: "block"}},
		&adapter.Routing{Rules: []adapter.RoutingRule{
			{ID: 1, SubjectIDs: []int64{42}, OutboundTag: "t"},
		}}, nil, nil,
	)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.Contains(string(raw), subjectEmail(42, 0)) {
		t.Errorf("no user matcher was emitted at all: %s", raw)
	}
}
