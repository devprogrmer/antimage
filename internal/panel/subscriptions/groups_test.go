package subscriptions

import "testing"

// EMPTY MEANS EVERYTHING, and it is the whole reason Filter is a type rather
// than a bare slice. A group with nothing selected, or one whose protocols
// were all removed, must hand the customer their full entitlement: an
// over-broad subscription is a support question, an empty one is an outage.
func TestAnEmptyFilterCarriesEverything(t *testing.T) {
	for _, f := range []Filter{NoFilter(), {Protocols: nil}, {Protocols: []string{}}} {
		if !f.IsEmpty() {
			t.Errorf("%+v does not report itself empty", f)
		}
		for _, p := range KnownProtocols() {
			if !f.Allows(p) {
				t.Errorf("%+v excluded %s; an empty selection must carry everything", f, p)
			}
		}
	}
}

func TestAFilterCarriesOnlyWhatItNames(t *testing.T) {
	f := Filter{Protocols: []string{"vless", "hysteria2"}}

	if !f.Allows("vless") || !f.Allows("hysteria2") {
		t.Error("a named protocol was excluded")
	}
	for _, p := range []string{"vmess", "trojan", "shadowsocks", "wireguard"} {
		if f.Allows(p) {
			t.Errorf("%s was carried by a filter that does not name it", p)
		}
	}
	if f.IsEmpty() {
		t.Error("a filter that names protocols reports itself empty")
	}
}

// Filtering happens on the mapped Server, not the raw inbound, because an Xray
// adapter serves vless AND trojan -- filtering by adapter kind would treat
// them as one thing and make a protocol selection useless for the case it
// exists for.
func TestFilteringDistinguishesProtocolsOnOneAdapter(t *testing.T) {
	servers := []Server{
		{NodeName: "a", Protocol: "vless", Port: 443},
		{NodeName: "b", Protocol: "trojan", Port: 443},
		{NodeName: "c", Protocol: "hysteria2", Port: 443},
	}
	got := Filter{Protocols: []string{"trojan"}}.Apply(servers)

	if len(got) != 1 || got[0].Protocol != "trojan" {
		t.Fatalf("got %+v, want only the trojan server", got)
	}
}

// The aggregated subscription and the panel's per-inbound view must agree
// about what a group excludes, or the operator sees an entry the customer's
// subscription does not contain and nothing explains the difference.
func TestServersAndConfigsFilterIdentically(t *testing.T) {
	f := Filter{Protocols: []string{"vless", "wireguard"}}

	servers := []Server{
		{NodeName: "a", Protocol: "vless"},
		{NodeName: "b", Protocol: "trojan"},
	}
	configs := []ClientConfig{
		{ServiceID: 1, Protocol: "vless"},
		{ServiceID: 2, Protocol: "trojan"},
		// WireGuard is in the group and has no aggregated representation, so
		// it survives the config filter and never reaches the server list at
		// all -- the two are consistent about the protocol, not about whether
		// a format can carry it.
		{ServiceID: 3, Protocol: "wireguard"},
	}

	gotServers := f.Apply(servers)
	gotConfigs := f.ApplyConfigs(configs)

	if len(gotServers) != 1 || gotServers[0].Protocol != "vless" {
		t.Errorf("servers = %+v", gotServers)
	}
	if len(gotConfigs) != 2 {
		t.Fatalf("configs = %+v, want vless and wireguard", gotConfigs)
	}
	for _, c := range gotConfigs {
		if c.Protocol == "trojan" {
			t.Error("trojan survived a filter that does not name it")
		}
	}
}

// A group naming a protocol the panel cannot produce would match nothing, so
// every subscription built from it comes out empty and the group itself gives
// no clue why. Refusing at write time is the last moment the operator is still
// looking at what they typed.
func TestAGroupNamingAnUnknownProtocolIsRefused(t *testing.T) {
	err := GroupInput{Name: "tier", Protocols: []string{"vless", "quic"}}.validate()
	if err == nil {
		t.Fatal("a group naming an unknown protocol was accepted")
	}
	// The message has to name the offending value; "invalid request" sends the
	// operator hunting.
	if !contains(err.Error(), "quic") {
		t.Errorf("error does not name the unknown protocol: %v", err)
	}
}

func TestAGroupNeedsAName(t *testing.T) {
	if err := (GroupInput{Name: "  "}).validate(); err == nil {
		t.Error("a group with a blank name was accepted")
	}
}

func TestEveryKnownProtocolIsAccepted(t *testing.T) {
	if err := (GroupInput{Name: "all", Protocols: KnownProtocols()}).validate(); err != nil {
		t.Errorf("a group naming every known protocol was refused: %v", err)
	}
}

// KnownProtocols is what the group form offers. If it drifts from what the
// config builder can produce, the form either offers a protocol that matches
// nothing or hides one an operator needs.
func TestKnownProtocolsMatchesWhatCanBeBuilt(t *testing.T) {
	node := NodeRef{ID: 1, Name: "n", Address: "203.0.113.1"}
	creds := Credentials{UUID: "u", Password: "p"}

	// Every protocol the builder produces must be selectable.
	produced := map[string]Inbound{
		"vless":       {AdapterKind: "xray", Params: map[string]any{"protocol": "vless", "port": 443.0}},
		"vmess":       {AdapterKind: "xray", Params: map[string]any{"protocol": "vmess", "port": 443.0}},
		"trojan":      {AdapterKind: "xray", Params: map[string]any{"protocol": "trojan", "port": 443.0}},
		"shadowsocks": {AdapterKind: "singbox", Params: map[string]any{"protocol": "shadowsocks", "port": 8388.0}},
		"hysteria2":   {AdapterKind: "hysteria2", Params: map[string]any{"port": 443.0}},
		"wireguard":   {AdapterKind: "wireguard", Params: map[string]any{"port": 51820.0}},
		"openvpn":     {AdapterKind: "openvpn", Params: map[string]any{"port": 1194.0}},
		"ocserv":      {AdapterKind: "ocserv", Params: map[string]any{"port": 443.0}},
		"l2tp":        {AdapterKind: "l2tp", Params: map[string]any{}},
	}

	selectable := map[string]bool{}
	for _, p := range KnownProtocols() {
		selectable[p] = true
	}

	for want, in := range produced {
		cfg, err := BuildClientConfig(in, node, "alice", creds)
		if err != nil {
			t.Errorf("the builder cannot produce %s: %v", want, err)
			continue
		}
		if cfg.Protocol != want {
			t.Errorf("builder reported protocol %q for %s", cfg.Protocol, want)
		}
		if !selectable[want] {
			t.Errorf("%s can be built but a group cannot select it", want)
		}
	}

	for p := range selectable {
		if _, ok := produced[p]; !ok {
			t.Errorf("a group can select %s but nothing produces it", p)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) &&
		(haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
