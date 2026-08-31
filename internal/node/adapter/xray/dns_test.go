package xray

import (
	"strings"
	"testing"

	"github.com/amyrm/antimage/internal/node/adapter"
)

func TestNilDNSProducesNoDNSBlock(t *testing.T) {
	raw, err := GenerateEgressConfig(
		[]adapter.Outbound{{ID: 1, Tag: "t", Kind: "direct"}}, nil, nil, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	doc := decodeDoc(t, raw)
	if _, ok := doc["dns"]; ok {
		t.Errorf("dns block present with no DNS config: %s", raw)
	}
}

// A DNS-only desired state (no outbounds, no routing) must still produce a
// document -- GenerateEgressConfig's "nothing to write" check has to know
// about DNS, not just outbounds and routing.
func TestDNSOnlyStillProducesADocument(t *testing.T) {
	raw, err := GenerateEgressConfig(nil, nil, nil, &adapter.DNSConfig{
		Servers: []adapter.DNSServer{{Address: "1.1.1.1"}},
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if raw == nil {
		t.Fatal("DNS-only config produced no document")
	}
	doc := decodeDoc(t, raw)
	dns, ok := doc["dns"].(map[string]any)
	if !ok {
		t.Fatalf("no dns block: %+v", doc)
	}
	servers, _ := dns["servers"].([]any)
	if len(servers) != 1 || servers[0] != "1.1.1.1" {
		t.Errorf("servers = %+v, want [\"1.1.1.1\"]", servers)
	}
}

// A server with no split-DNS scoping renders as a bare string, matching
// Xray's own plain form -- not an object, which would be indistinguishable
// in effect but needlessly verbose.
func TestPlainDNSServerRendersAsBareString(t *testing.T) {
	raw, err := GenerateEgressConfig(nil, nil, nil, &adapter.DNSConfig{
		Servers: []adapter.DNSServer{{Address: "8.8.8.8"}},
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	doc := decodeDoc(t, raw)
	servers, _ := doc["dns"].(map[string]any)["servers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("want 1 server, got %d", len(servers))
	}
	if _, isString := servers[0].(string); !isString {
		t.Errorf("server rendered as %T, want a bare string: %+v", servers[0], servers[0])
	}
}

// A server scoped to specific domains (split DNS) must render as an object
// carrying them, or Xray has no way to know it should only answer those
// queries.
func TestSplitDNSServerRendersDomainsAndSkipFallback(t *testing.T) {
	raw, err := GenerateEgressConfig(nil, nil, nil, &adapter.DNSConfig{
		Servers: []adapter.DNSServer{
			{Address: "10.0.0.1", Domains: []string{"internal.corp"}, SkipFallback: true},
		},
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	doc := decodeDoc(t, raw)
	servers, _ := doc["dns"].(map[string]any)["servers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("want 1 server, got %d", len(servers))
	}
	obj, ok := servers[0].(map[string]any)
	if !ok {
		t.Fatalf("server rendered as %T, want an object carrying its domain scope", servers[0])
	}
	if obj["address"] != "10.0.0.1" {
		t.Errorf("address = %v, want 10.0.0.1", obj["address"])
	}
	domains, _ := obj["domains"].([]any)
	if len(domains) != 1 || domains[0] != "internal.corp" {
		t.Errorf("domains = %+v, want [\"internal.corp\"]", domains)
	}
	if obj["skipFallback"] != true {
		t.Errorf("skipFallback = %v, want true", obj["skipFallback"])
	}
}

func TestDNSServerRequiresAnAddress(t *testing.T) {
	_, err := GenerateEgressConfig(nil, nil, nil, &adapter.DNSConfig{
		Servers: []adapter.DNSServer{{Domains: []string{"x.com"}}},
	})
	if err == nil {
		t.Error("accepted a DNS server with no address")
	}
}

// A single IP renders as a bare string, matching Xray's own accepted shape;
// multiple IPs render as an array. Both must actually reach the document, or
// the override does nothing and traffic keeps resolving normally.
func TestHostsRenderSingleAndMultipleIPs(t *testing.T) {
	raw, err := GenerateEgressConfig(nil, nil, nil, &adapter.DNSConfig{
		Hosts: map[string][]string{
			"one.example":  {"1.2.3.4"},
			"many.example": {"1.2.3.4", "5.6.7.8"},
		},
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	doc := decodeDoc(t, raw)
	hosts, ok := doc["dns"].(map[string]any)["hosts"].(map[string]any)
	if !ok {
		t.Fatalf("no hosts block: %+v", doc["dns"])
	}
	if hosts["one.example"] != "1.2.3.4" {
		t.Errorf("one.example = %v, want a bare string", hosts["one.example"])
	}
	many, ok := hosts["many.example"].([]any)
	if !ok || len(many) != 2 {
		t.Errorf("many.example = %+v, want a 2-element array", hosts["many.example"])
	}
}

func TestHostsRenderingIsDeterministic(t *testing.T) {
	dns := &adapter.DNSConfig{
		Hosts: map[string][]string{
			"z.example": {"1.1.1.1"},
			"a.example": {"2.2.2.2"},
			"m.example": {"3.3.3.3"},
		},
	}
	first, err := GenerateEgressConfig(nil, nil, nil, dns)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	for i := 0; i < 20; i++ {
		again, err := GenerateEgressConfig(nil, nil, nil, dns)
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if string(again) != string(first) {
			t.Fatalf("call %d differs -- Go map iteration order leaked into the document:\n%s\nvs\n%s",
				i, again, first)
		}
	}
}

func TestHostRequiresAtLeastOneAddress(t *testing.T) {
	_, err := GenerateEgressConfig(nil, nil, nil, &adapter.DNSConfig{
		Hosts: map[string][]string{"empty.example": {}},
	})
	if err == nil {
		t.Error("accepted a host override with no addresses")
	}
}

func TestFakeDNSPoolsRender(t *testing.T) {
	raw, err := GenerateEgressConfig(nil, nil, nil, &adapter.DNSConfig{
		FakeDNS: []adapter.FakeDNSPool{
			{IPPool: "198.18.0.0/15", PoolSize: 65535},
			{IPPool: "fc00::/18", PoolSize: 65535},
		},
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	doc := decodeDoc(t, raw)
	pools, ok := doc["dns"].(map[string]any)["fakedns"].([]any)
	if !ok || len(pools) != 2 {
		t.Fatalf("fakedns = %+v, want 2 pools", doc["dns"])
	}
	first, _ := pools[0].(map[string]any)
	if first["ipPool"] != "198.18.0.0/15" || first["poolSize"] != float64(65535) {
		t.Errorf("pool 0 = %+v", first)
	}
}

func TestFakeDNSPoolRequiresAValidCIDR(t *testing.T) {
	_, err := GenerateEgressConfig(nil, nil, nil, &adapter.DNSConfig{
		FakeDNS: []adapter.FakeDNSPool{{IPPool: "not-a-cidr", PoolSize: 100}},
	})
	if err == nil {
		t.Error("accepted a fakedns pool with a malformed CIDR")
	}
}

func TestFakeDNSPoolRequiresAPositivePoolSize(t *testing.T) {
	_, err := GenerateEgressConfig(nil, nil, nil, &adapter.DNSConfig{
		FakeDNS: []adapter.FakeDNSPool{{IPPool: "198.18.0.0/15", PoolSize: 0}},
	})
	if err == nil {
		t.Error("accepted a fakedns pool with pool_size 0")
	}
}

func TestQueryStrategyAndDisableCacheRender(t *testing.T) {
	raw, err := GenerateEgressConfig(nil, nil, nil, &adapter.DNSConfig{
		Servers:       []adapter.DNSServer{{Address: "1.1.1.1"}},
		QueryStrategy: "UseIPv4",
		DisableCache:  true,
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	doc := decodeDoc(t, raw)
	dns, _ := doc["dns"].(map[string]any)
	if dns["queryStrategy"] != "UseIPv4" {
		t.Errorf("queryStrategy = %v, want UseIPv4", dns["queryStrategy"])
	}
	if dns["disableCache"] != true {
		t.Errorf("disableCache = %v, want true", dns["disableCache"])
	}
}

func TestUnknownQueryStrategyIsRefused(t *testing.T) {
	_, err := GenerateEgressConfig(nil, nil, nil, &adapter.DNSConfig{
		QueryStrategy: "UseIPv5",
	})
	if err == nil {
		t.Error("accepted an unknown query_strategy")
	}
	if err != nil && !strings.Contains(err.Error(), "UseIPv5") {
		t.Errorf("error does not name the bad value: %v", err)
	}
}
