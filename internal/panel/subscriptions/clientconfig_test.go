package subscriptions

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

var node = NodeRef{ID: 1, Name: "fra-1", Address: "203.0.113.10"}

func build(t *testing.T, kind string, params map[string]any, creds Credentials) ClientConfig {
	t.Helper()
	cfg, err := BuildClientConfig(
		Inbound{ServiceID: 7, AdapterKind: kind, Params: params}, node, "alice", creds)
	if err != nil {
		t.Fatalf("BuildClientConfig(%s): %v", kind, err)
	}
	return cfg
}

// THE bug this exists to remove.
//
// The previous builder read a "protocol" key that only Xray and sing-box have,
// and defaulted a missing one to "vless". So a WireGuard inbound was emitted
// as vless://<uuid>@host:51820 -- a plausible-looking link that no client can
// use and that nobody can debug from the link alone. Every non-Xray protocol
// was affected the same way.
func TestNonProxyProtocolsAreNeverEmittedAsProxyLinks(t *testing.T) {
	for _, tc := range []struct {
		kind   string
		params map[string]any
		want   Delivery
	}{
		{"wireguard", map[string]any{"port": 51820.0, "public_key": "srvpub="}, DeliveryFile},
		{"openvpn", map[string]any{"port": 1194.0, "proto": "udp"}, DeliveryFile},
		{"ocserv", map[string]any{"port": 443.0}, DeliveryManual},
		{"l2tp", map[string]any{"psk": "sharedsecret"}, DeliveryManual},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			cfg := build(t, tc.kind, tc.params, Credentials{UUID: "u-1", Password: "p-1"})

			if cfg.Delivery != tc.want {
				t.Errorf("delivery = %q, want %q", cfg.Delivery, tc.want)
			}
			if cfg.Protocol == "vless" {
				t.Errorf("a %s inbound was labelled vless", tc.kind)
			}
			for _, scheme := range []string{"vless://", "vmess://", "trojan://", "ss://"} {
				if strings.HasPrefix(cfg.URI, scheme) {
					t.Errorf("a %s inbound produced %s...; no client can use that",
						tc.kind, scheme)
				}
			}
		})
	}
}

// An Xray inbound with no protocol field is refused rather than defaulted.
// Guessing is exactly what produced the invalid links.
func TestAProxyInboundWithNoProtocolIsRefused(t *testing.T) {
	_, err := BuildClientConfig(
		Inbound{ServiceID: 7, AdapterKind: "xray", Params: map[string]any{"port": 443.0}},
		node, "alice", Credentials{UUID: "u"})
	if err == nil {
		t.Error("an inbound naming no protocol was accepted and would be guessed at")
	}
}

// TLS and network were hardcoded to true and "tcp". A plaintext WebSocket
// inbound therefore produced a link claiming TLS over TCP, which fails to
// connect and gives no clue why.
func TestTransportAndSecurityComeFromTheInbound(t *testing.T) {
	cfg := build(t, "xray", map[string]any{
		"protocol": "vless", "port": 8080.0,
		"network": "ws", "security": "none", "path": "/ray",
	}, Credentials{UUID: "u-1"})

	if !strings.Contains(cfg.URI, "type=ws") {
		t.Errorf("URI does not carry the websocket transport: %s", cfg.URI)
	}
	if !strings.Contains(cfg.URI, "security=none") {
		t.Errorf("URI claims security the inbound does not have: %s", cfg.URI)
	}
	if !strings.Contains(cfg.URI, "path=%2Fray") {
		t.Errorf("URI drops the websocket path: %s", cfg.URI)
	}
}

// REALITY needs its public key and short id, or the handshake cannot complete.
func TestRealityParametersSurviveIntoTheLink(t *testing.T) {
	cfg := build(t, "xray", map[string]any{
		"protocol": "vless", "port": 443.0, "network": "tcp",
		"security": "reality", "public_key": "PBK123", "short_id": "ab12",
		"sni": "www.example.com", "flow": "xtls-rprx-vision",
	}, Credentials{UUID: "u-1"})

	for _, want := range []string{"security=reality", "pbk=PBK123", "sid=ab12",
		"sni=www.example.com", "flow=xtls-rprx-vision"} {
		if !strings.Contains(cfg.URI, want) {
			t.Errorf("URI is missing %q: %s", want, cfg.URI)
		}
	}
}

func TestVMessIsBase64JSONNotAQueryString(t *testing.T) {
	cfg := build(t, "xray", map[string]any{
		"protocol": "vmess", "port": 443.0, "network": "ws", "path": "/v",
	}, Credentials{UUID: "u-vmess"})

	if !strings.HasPrefix(cfg.URI, "vmess://") {
		t.Fatalf("not a vmess link: %s", cfg.URI)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(cfg.URI, "vmess://"))
	if err != nil {
		t.Fatalf("vmess payload is not base64: %v", err)
	}
	var blob map[string]any
	if err := json.Unmarshal(raw, &blob); err != nil {
		t.Fatalf("vmess payload is not JSON: %v", err)
	}
	if blob["id"] != "u-vmess" {
		t.Errorf("vmess id = %v, want the subject's uuid", blob["id"])
	}
	if blob["net"] != "ws" || blob["path"] != "/v" {
		t.Errorf("vmess transport lost: %v", blob)
	}
}

// SIP002 encodes method:password as base64url without padding. Getting this
// wrong produces a link clients silently refuse.
func TestShadowsocksUsesSIP002(t *testing.T) {
	cfg := build(t, "singbox", map[string]any{
		"protocol": "shadowsocks", "port": 8388.0, "method": "aes-128-gcm",
	}, Credentials{Password: "s3cret"})

	if !strings.HasPrefix(cfg.URI, "ss://") {
		t.Fatalf("not a shadowsocks link: %s", cfg.URI)
	}
	userinfo := strings.TrimPrefix(cfg.URI, "ss://")
	userinfo = userinfo[:strings.Index(userinfo, "@")]
	raw, err := base64.RawURLEncoding.DecodeString(userinfo)
	if err != nil {
		t.Fatalf("userinfo is not unpadded base64url: %v", err)
	}
	if string(raw) != "aes-128-gcm:s3cret" {
		t.Errorf("userinfo = %q, want method:password", raw)
	}
}

func TestHysteria2ProducesItsOwnScheme(t *testing.T) {
	cfg := build(t, "hysteria2", map[string]any{
		"port": 443.0, "sni": "example.com", "obfs": "salamander",
		"obfs_password": "o-pw",
	}, Credentials{Password: "user-pw"})

	if !strings.HasPrefix(cfg.URI, "hy2://") {
		t.Fatalf("hysteria2 did not produce a hy2 link: %s", cfg.URI)
	}
	// The SUBJECT's password, not the inbound's shared one.
	if !strings.Contains(cfg.URI, "user-pw") {
		t.Errorf("link does not carry the subject's password: %s", cfg.URI)
	}
	for _, want := range []string{"sni=example.com", "obfs=salamander", "obfs-password=o-pw"} {
		if !strings.Contains(cfg.URI, want) {
			t.Errorf("link is missing %q: %s", want, cfg.URI)
		}
	}
}

func TestWireGuardProducesAUsableConf(t *testing.T) {
	cfg := build(t, "wireguard", map[string]any{
		"port": 51820.0, "public_key": "SRVPUB=", "mtu": 1380.0,
		"client_address": "10.8.0.2/32",
		"dns":            []any{"1.1.1.1", "9.9.9.9"},
	}, Credentials{})

	if cfg.Delivery != DeliveryFile {
		t.Fatalf("delivery = %q, want file", cfg.Delivery)
	}
	if !strings.HasSuffix(cfg.FileName, ".conf") {
		t.Errorf("file name = %q, want a .conf", cfg.FileName)
	}
	for _, want := range []string{
		"[Interface]", "[Peer]", "PublicKey = SRVPUB=",
		"Endpoint = 203.0.113.10:51820", "Address = 10.8.0.2/32",
		"DNS = 1.1.1.1, 9.9.9.9", "MTU = 1380",
	} {
		if !strings.Contains(cfg.FileBody, want) {
			t.Errorf("conf is missing %q:\n%s", want, cfg.FileBody)
		}
	}
}

// The panel stores the server's PRIVATE key; if it has no public half
// recorded, the file must say so rather than ship an empty field that fails
// at connect time with nothing pointing back here.
func TestWireGuardSaysWhenTheServerKeyIsUnknown(t *testing.T) {
	cfg := build(t, "wireguard", map[string]any{"port": 51820.0}, Credentials{})
	if cfg.Note != "noServerPublicKey" {
		t.Errorf("note = %q, want the missing-key warning", cfg.Note)
	}
	if strings.Contains(cfg.FileBody, "PublicKey = \n") {
		t.Error("the conf carries an empty PublicKey, which fails silently")
	}
}

func TestOpenVPNProducesAnOvpnProfile(t *testing.T) {
	cfg := build(t, "openvpn", map[string]any{
		"port": 1194.0, "proto": "tcp", "cipher": "AES-256-GCM",
	}, Credentials{Password: "pw"})

	if cfg.Delivery != DeliveryFile || !strings.HasSuffix(cfg.FileName, ".ovpn") {
		t.Fatalf("delivery=%q name=%q, want a .ovpn file", cfg.Delivery, cfg.FileName)
	}
	for _, want := range []string{
		"client", "dev tun", "proto tcp", "remote 203.0.113.10 1194",
		// The adapter authenticates with a username and password, so the
		// profile must ask for them; without this line the client offers a
		// certificate the server is not configured to want.
		"auth-user-pass", "data-ciphers AES-256-GCM",
	} {
		if !strings.Contains(cfg.FileBody, want) {
			t.Errorf("profile is missing %q:\n%s", want, cfg.FileBody)
		}
	}
}

func TestManualProtocolsCarryWhatTheUserMustType(t *testing.T) {
	l2tp := build(t, "l2tp", map[string]any{"psk": "sharedsecret"},
		Credentials{Password: "user-pw"})
	if l2tp.Manual == nil {
		t.Fatal("l2tp produced no manual configuration")
	}
	if l2tp.Manual.PSK != "sharedsecret" {
		t.Errorf("PSK = %q; without it the IPsec phase cannot complete", l2tp.Manual.PSK)
	}
	if l2tp.Manual.Username != "alice" || l2tp.Manual.Password != "user-pw" {
		t.Errorf("manual credentials = %+v", l2tp.Manual)
	}

	// OpenConnect has no PSK, and inventing one would be a field the user
	// cannot fill in.
	oc := build(t, "ocserv", map[string]any{"port": 443.0}, Credentials{Password: "pw"})
	if oc.Manual == nil || oc.Manual.PSK != "" {
		t.Errorf("ocserv manual config = %+v, want no PSK", oc.Manual)
	}
}

// A Clash file containing a fabricated entry for a WireGuard tunnel is worse
// than one that omits it: the omission is visible, the fabrication is not.
func TestOnlyLinkableProtocolsReachAggregatedFormats(t *testing.T) {
	configs := []ClientConfig{
		build(t, "xray", map[string]any{"protocol": "vless", "port": 443.0}, Credentials{UUID: "u"}),
		build(t, "wireguard", map[string]any{"port": 51820.0}, Credentials{}),
		build(t, "openvpn", map[string]any{"port": 1194.0}, Credentials{}),
		build(t, "l2tp", map[string]any{}, Credentials{Password: "p"}),
		build(t, "hysteria2", map[string]any{"port": 443.0}, Credentials{Password: "p"}),
	}

	uris := AggregatableURIs(configs)
	if len(uris) != 2 {
		t.Fatalf("aggregated %d URIs, want 2 (vless and hysteria2): %v", len(uris), uris)
	}
	for _, u := range uris {
		if !strings.HasPrefix(u, "vless://") && !strings.HasPrefix(u, "hy2://") {
			t.Errorf("a non-linkable protocol reached the aggregate: %s", u)
		}
	}

	// And the ones left out say why, so the UI can explain the gap rather than
	// leaving the operator to discover it from a customer.
	for _, c := range configs {
		if c.Delivery == DeliveryManual && c.Note == "" {
			t.Errorf("%s is excluded from aggregates with no explanation", c.Protocol)
		}
	}
}

// A missing credential is refused, never substituted. Emitting a link with
// somebody else's secret, or an empty one, is the worst possible outcome.
func TestAMissingCredentialIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, proto string }{
		{"vless without uuid", "vless"},
		{"vmess without uuid", "vmess"},
		{"trojan without password", "trojan"},
		{"shadowsocks without password", "shadowsocks"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildClientConfig(
				Inbound{ServiceID: 7, AdapterKind: "xray",
					Params: map[string]any{"protocol": tc.proto, "port": 443.0}},
				node, "alice", Credentials{})
			if err == nil {
				t.Error("a config was produced with no credential")
			}
		})
	}
}

// An IPv6 node address must be bracketed or the link does not parse.
func TestIPv6AddressesAreBracketed(t *testing.T) {
	v6 := NodeRef{ID: 2, Name: "v6", Address: "2001:db8::1"}
	cfg, err := BuildClientConfig(
		Inbound{ServiceID: 8, AdapterKind: "xray",
			Params: map[string]any{"protocol": "vless", "port": 443.0}},
		v6, "alice", Credentials{UUID: "u"})
	if err != nil {
		t.Fatalf("BuildClientConfig: %v", err)
	}
	if !strings.Contains(cfg.URI, "[2001:db8::1]:443") {
		t.Errorf("IPv6 address is not bracketed: %s", cfg.URI)
	}
}

func TestAnUnknownAdapterHasNoClientConfig(t *testing.T) {
	_, err := BuildClientConfig(
		Inbound{ServiceID: 9, AdapterKind: "stub", Params: map[string]any{}},
		node, "alice", Credentials{})
	if err == nil {
		t.Error("an adapter with no client configuration produced one anyway")
	}
}
