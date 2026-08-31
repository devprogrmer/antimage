package xray

import (
	"strings"
	"testing"

	"github.com/amyrm/antimage/internal/node/adapter"
)

func TestNoBalancersProducesNoBalancersKey(t *testing.T) {
	raw, err := GenerateEgressConfig(
		[]adapter.Outbound{{ID: 1, Tag: "t", Kind: "direct"}}, nil, nil, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	routing, _ := decodeDoc(t, raw)["routing"].(map[string]any)
	if _, ok := routing["balancers"]; ok {
		t.Errorf("balancers key present with none defined: %s", raw)
	}
	if _, ok := decodeDoc(t, raw)["observatory"]; ok {
		t.Errorf("observatory key present with no balancers: %s", raw)
	}
}

func TestBalancerRendersTagSelectorAndRandomStrategy(t *testing.T) {
	raw, err := GenerateEgressConfig(
		[]adapter.Outbound{
			{ID: 1, Tag: "warp-1", Kind: "direct"},
			{ID: 2, Tag: "warp-2", Kind: "direct"},
		},
		&adapter.Routing{Balancers: []adapter.Balancer{
			{Tag: "b1", Selector: []string{"warp-"}},
		}}, nil, nil,
	)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	routing, _ := decodeDoc(t, raw)["routing"].(map[string]any)
	balancers, _ := routing["balancers"].([]any)
	if len(balancers) != 1 {
		t.Fatalf("want 1 balancer, got %d", len(balancers))
	}
	b, _ := balancers[0].(map[string]any)
	if b["tag"] != "b1" {
		t.Errorf("tag = %v, want b1", b["tag"])
	}
	selector, _ := b["selector"].([]any)
	if len(selector) != 1 || selector[0] != "warp-" {
		t.Errorf("selector = %+v, want [\"warp-\"]", selector)
	}
	strategy, _ := b["strategy"].(map[string]any)
	if strategy["type"] != "random" {
		t.Errorf("strategy = %+v, want random (the default)", strategy)
	}
}

func TestBalancerRequiresATag(t *testing.T) {
	_, err := GenerateEgressConfig(nil, &adapter.Routing{
		Balancers: []adapter.Balancer{{Selector: []string{"x"}}},
	}, nil, nil)
	if err == nil {
		t.Error("accepted a balancer with no tag")
	}
}

func TestDuplicateBalancerTagsAreRefused(t *testing.T) {
	_, err := GenerateEgressConfig(
		[]adapter.Outbound{{ID: 1, Tag: "warp", Kind: "direct"}},
		&adapter.Routing{Balancers: []adapter.Balancer{
			{Tag: "same", Selector: []string{"warp"}},
			{Tag: "same", Selector: []string{"warp"}},
		}}, nil, nil,
	)
	if err == nil {
		t.Error("accepted two balancers sharing a tag; a routing rule naming it would be ambiguous")
	}
}

func TestBalancerRequiresANonEmptySelector(t *testing.T) {
	_, err := GenerateEgressConfig(
		[]adapter.Outbound{{ID: 1, Tag: "warp", Kind: "direct"}},
		&adapter.Routing{Balancers: []adapter.Balancer{{Tag: "b1"}}}, nil, nil,
	)
	if err == nil {
		t.Error("accepted a balancer with no selector; it would match no outbound")
	}
}

// A selector prefix that cannot match any outbound this node actually
// defines is almost certainly a typo -- refusing it here is cheaper than
// the node failing to converge silently for the wrong reason.
func TestBalancerSelectorMustMatchAKnownOutbound(t *testing.T) {
	_, err := GenerateEgressConfig(
		[]adapter.Outbound{{ID: 1, Tag: "warp", Kind: "direct"}},
		&adapter.Routing{Balancers: []adapter.Balancer{
			{Tag: "b1", Selector: []string{"totally-different-prefix"}},
		}}, nil, nil,
	)
	if err == nil {
		t.Error("accepted a balancer selector matching no outbound this node has")
	}
}

func TestBalancerRejectsUnknownStrategy(t *testing.T) {
	_, err := GenerateEgressConfig(
		[]adapter.Outbound{{ID: 1, Tag: "warp", Kind: "direct"}},
		&adapter.Routing{Balancers: []adapter.Balancer{
			{Tag: "b1", Selector: []string{"warp"}, Strategy: "round_robin"},
		}}, nil, nil,
	)
	if err == nil {
		t.Error("accepted an unknown balancer strategy")
	}
}

func TestRuleCanTargetABalancerInsteadOfAnOutbound(t *testing.T) {
	raw, err := GenerateEgressConfig(
		[]adapter.Outbound{{ID: 1, Tag: "warp", Kind: "direct"}},
		&adapter.Routing{
			Balancers: []adapter.Balancer{{Tag: "b1", Selector: []string{"warp"}}},
			Rules: []adapter.RoutingRule{
				{ID: 1, Domains: []string{"example.com"}, BalancerTag: "b1"},
			},
		}, nil, nil,
	)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	rules := rulesOf(t, decodeDoc(t, raw))
	var found bool
	for _, r := range rules {
		obj, _ := r.(map[string]any)
		if obj["balancerTag"] == "b1" {
			found = true
			if _, hasOutbound := obj["outboundTag"]; hasOutbound {
				t.Errorf("rule targeting a balancer also carries outboundTag: %+v", obj)
			}
		}
	}
	if !found {
		t.Errorf("no rule with balancerTag=b1 found: %+v", rules)
	}
}

func TestRuleWithBothOutboundAndBalancerTagIsRefused(t *testing.T) {
	_, err := GenerateEgressConfig(
		[]adapter.Outbound{{ID: 1, Tag: "warp", Kind: "direct"}},
		&adapter.Routing{
			Balancers: []adapter.Balancer{{Tag: "b1", Selector: []string{"warp"}}},
			Rules: []adapter.RoutingRule{
				{ID: 1, Domains: []string{"x.com"}, OutboundTag: "warp", BalancerTag: "b1"},
			},
		}, nil, nil,
	)
	if err == nil {
		t.Error("accepted a rule setting both outbound_tag and balancer_tag")
	}
}

func TestRuleReferencingUnknownBalancerIsRefused(t *testing.T) {
	_, err := GenerateEgressConfig(nil, &adapter.Routing{
		Rules: []adapter.RoutingRule{{ID: 1, Domains: []string{"x.com"}, BalancerTag: "nope"}},
	}, nil, nil)
	if err == nil {
		t.Error("accepted a rule selecting an undefined balancer")
	}
}

// The safety property the whole design exists for: a "random" balancer
// needs no live data, so a document using only that strategy must not carry
// an observatory block asking Xray to probe anything.
func TestRandomBalancerAloneProducesNoObservatory(t *testing.T) {
	raw, err := GenerateEgressConfig(
		[]adapter.Outbound{{ID: 1, Tag: "warp", Kind: "direct"}},
		&adapter.Routing{Balancers: []adapter.Balancer{
			{Tag: "b1", Selector: []string{"warp"}, Strategy: "random"},
		}}, nil, nil,
	)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if _, ok := decodeDoc(t, raw)["observatory"]; ok {
		t.Error("observatory present for a balancer that never asked for live latency data")
	}
}

func TestLeastPingBalancerProducesObservatoryWithItsSelector(t *testing.T) {
	raw, err := GenerateEgressConfig(
		[]adapter.Outbound{{ID: 1, Tag: "warp-1", Kind: "direct"}},
		&adapter.Routing{Balancers: []adapter.Balancer{
			{Tag: "b1", Selector: []string{"warp-"}, Strategy: "least_ping"},
		}}, nil, nil,
	)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	doc := decodeDoc(t, raw)
	obs, ok := doc["observatory"].(map[string]any)
	if !ok {
		t.Fatalf("no observatory block: %+v", doc)
	}
	subjects, _ := obs["subjectSelector"].([]any)
	if len(subjects) != 1 || subjects[0] != "warp-" {
		t.Errorf("subjectSelector = %+v, want [\"warp-\"]", subjects)
	}
	if obs["probeUrl"] == "" || obs["probeInterval"] == "" {
		t.Errorf("observatory missing probe settings: %+v", obs)
	}

	routing, _ := doc["routing"].(map[string]any)
	balancers, _ := routing["balancers"].([]any)
	b, _ := balancers[0].(map[string]any)
	strategy, _ := b["strategy"].(map[string]any)
	if strategy["type"] != "leastPing" {
		t.Errorf("strategy = %+v, want leastPing (Xray's own casing)", strategy)
	}
}

// A mix of strategies must only probe what least_ping actually needs -- a
// random balancer's selector has no business appearing in the observatory,
// even if some OTHER balancer on the same node needs one.
func TestObservatorySelectorExcludesRandomBalancers(t *testing.T) {
	raw, err := GenerateEgressConfig(
		[]adapter.Outbound{
			{ID: 1, Tag: "warp-1", Kind: "direct"},
			{ID: 2, Tag: "direct-1", Kind: "direct"},
		},
		&adapter.Routing{Balancers: []adapter.Balancer{
			{Tag: "pinged", Selector: []string{"warp-"}, Strategy: "least_ping"},
			{Tag: "randomized", Selector: []string{"direct-"}, Strategy: "random"},
		}}, nil, nil,
	)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	obs, _ := decodeDoc(t, raw)["observatory"].(map[string]any)
	subjects, _ := obs["subjectSelector"].([]any)
	if len(subjects) != 1 || subjects[0] != "warp-" {
		t.Errorf("subjectSelector = %+v, want only [\"warp-\"], not the random balancer's selector", subjects)
	}
}

func TestObservatoryRenderingIsDeterministic(t *testing.T) {
	outbounds := []adapter.Outbound{
		{ID: 1, Tag: "z-out", Kind: "direct"},
		{ID: 2, Tag: "a-out", Kind: "direct"},
	}
	routing := &adapter.Routing{Balancers: []adapter.Balancer{
		{Tag: "b1", Selector: []string{"z-"}, Strategy: "least_ping"},
		{Tag: "b2", Selector: []string{"a-"}, Strategy: "least_ping"},
	}}
	first, err := GenerateEgressConfig(outbounds, routing, nil, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	for i := 0; i < 20; i++ {
		again, err := GenerateEgressConfig(outbounds, routing, nil, nil)
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if string(again) != string(first) {
			t.Fatalf("call %d differs -- the document hash would never settle:\n%s\nvs\n%s",
				i, again, first)
		}
	}
}

func TestBalancerOnlyStillProducesADocument(t *testing.T) {
	raw, err := GenerateEgressConfig(
		[]adapter.Outbound{{ID: 1, Tag: "warp", Kind: "direct"}},
		&adapter.Routing{Balancers: []adapter.Balancer{
			{Tag: "b1", Selector: []string{"warp"}},
		}}, nil, nil,
	)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if raw == nil || !strings.Contains(string(raw), `"balancers"`) {
		t.Errorf("balancer-only routing produced no document mentioning balancers: %s", raw)
	}
}
