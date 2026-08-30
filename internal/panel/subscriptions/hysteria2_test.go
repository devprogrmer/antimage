package subscriptions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func hy2Server() Server {
	return Server{
		NodeID: 1, NodeName: "fra-1", NodeAddress: "203.0.113.10", ServiceID: 7,
		Protocol: "hysteria2", Port: 443, Password: "user-pw",
		SNI: "example.com", Obfs: "salamander", ObfsPassword: "o-pw",
		UpMbps: 100, DownMbps: 500,
	}
}

func vlessServer() Server {
	return Server{
		NodeID: 2, NodeName: "ams-1", NodeAddress: "198.51.100.7", ServiceID: 8,
		Protocol: "vless", Port: 443, UUID: "uuid-1", TLS: true, Network: "tcp",
	}
}

// A protocol no aggregated format can express must not empty the document.
//
// The loop used to abort on the first unsupported protocol, so a user holding
// one VLESS inbound and one WireGuard inbound received an EMPTY subscription
// and nothing anywhere said why.
func TestAnUnrepresentableProtocolDoesNotEmptyTheSubscription(t *testing.T) {
	servers := []Server{
		vlessServer(),
		{NodeName: "wg-1", NodeAddress: "203.0.113.20", Protocol: "wireguard", Port: 51820},
		hy2Server(),
	}
	ctx := context.Background()

	t.Run("v2ray", func(t *testing.T) {
		body, _, err := (&V2RayRenderer{}).Render(ctx, servers)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		raw, err := base64.StdEncoding.DecodeString(string(body))
		if err != nil {
			t.Fatalf("subscription is not base64: %v", err)
		}
		lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
		if len(lines) != 2 {
			t.Fatalf("got %d lines, want 2 (vless and hysteria2): %q", len(lines), lines)
		}
		if !strings.HasPrefix(lines[0], "vless://") || !strings.HasPrefix(lines[1], "hy2://") {
			t.Errorf("unexpected lines: %q", lines)
		}
		if strings.Contains(string(raw), "wg-1") {
			t.Error("a WireGuard inbound reached a v2ray subscription")
		}
	})

	t.Run("clash", func(t *testing.T) {
		body, _, err := (&ClashRenderer{}).Render(ctx, servers)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if strings.Contains(string(body), "wg-1") {
			t.Error("a WireGuard inbound reached a Clash subscription")
		}
		if !strings.Contains(string(body), "hysteria2") {
			t.Errorf("hysteria2 is missing from the Clash document:\n%s", body)
		}
	})

	t.Run("singbox", func(t *testing.T) {
		body, _, err := (&SingBoxRenderer{}).Render(ctx, servers)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if strings.Contains(string(body), "wg-1") {
			t.Error("a WireGuard inbound reached a sing-box subscription")
		}
		if !strings.Contains(string(body), "hysteria2") {
			t.Errorf("hysteria2 is missing from the sing-box document:\n%s", body)
		}
	})
}

func TestClashHysteria2FieldNames(t *testing.T) {
	proxy, err := clashHysteria2(hy2Server())
	if err != nil {
		t.Fatalf("clashHysteria2: %v", err)
	}

	if proxy["type"] != "hysteria2" || proxy["password"] != "user-pw" {
		t.Errorf("proxy = %+v", proxy)
	}
	if proxy["sni"] != "example.com" {
		t.Errorf("sni = %v", proxy["sni"])
	}
	// Clash Meta's own spelling. Renaming these for consistency produces a
	// file Clash silently ignores.
	if proxy["obfs"] != "salamander" || proxy["obfs-password"] != "o-pw" {
		t.Errorf("obfs fields = %v / %v", proxy["obfs"], proxy["obfs-password"])
	}
	// Bandwidth is a string WITH A UNIT in Clash. A bare integer makes Clash
	// reject the proxy outright.
	if proxy["up"] != "100 Mbps" || proxy["down"] != "500 Mbps" {
		t.Errorf("bandwidth = %v / %v, want strings with units",
			proxy["up"], proxy["down"])
	}
}

func TestSingBoxHysteria2Shape(t *testing.T) {
	out, err := singboxHysteria2(hy2Server())
	if err != nil {
		t.Fatalf("singboxHysteria2: %v", err)
	}

	if out["type"] != "hysteria2" || out["server_port"] != 443 {
		t.Errorf("outbound = %+v", out)
	}
	// sing-box NESTS obfs, where Clash flattens it.
	obfs, ok := out["obfs"].(map[string]any)
	if !ok || obfs["type"] != "salamander" || obfs["password"] != "o-pw" {
		t.Errorf("obfs = %+v, want a nested object", out["obfs"])
	}
	tls, ok := out["tls"].(map[string]any)
	if !ok || tls["enabled"] != true || tls["server_name"] != "example.com" {
		t.Errorf("tls = %+v", out["tls"])
	}
	// Bare integers here, unlike Clash's strings.
	if out["up_mbps"] != 100 || out["down_mbps"] != 500 {
		t.Errorf("bandwidth = %v / %v, want integers", out["up_mbps"], out["down_mbps"])
	}
}

// Without an SNI the certificate has to match the address anyway, so saying so
// beats leaving server_name blank and letting the handshake fail obscurely.
func TestSingBoxFallsBackToTheAddressForSNI(t *testing.T) {
	srv := hy2Server()
	srv.SNI = ""
	out, err := singboxHysteria2(srv)
	if err != nil {
		t.Fatalf("singboxHysteria2: %v", err)
	}
	tls := out["tls"].(map[string]any)
	if tls["server_name"] != "203.0.113.10" {
		t.Errorf("server_name = %v, want the node address", tls["server_name"])
	}
}

// Obfs password without obfs is meaningless and must not appear on its own.
func TestObfsPasswordIsOmittedWithoutObfs(t *testing.T) {
	srv := hy2Server()
	srv.Obfs = ""

	proxy, _ := clashHysteria2(srv)
	if _, present := proxy["obfs-password"]; present {
		t.Error("Clash proxy carries an obfs password with no obfs mode")
	}
	out, _ := singboxHysteria2(srv)
	if _, present := out["obfs"]; present {
		t.Error("sing-box outbound carries an obfs object with no mode")
	}

	uri, err := hysteria2URI(srv)
	if err != nil {
		t.Fatalf("hysteria2URI: %v", err)
	}
	if strings.Contains(uri, "obfs") {
		t.Errorf("URI carries obfs with no mode: %s", uri)
	}
}

// A link that looks importable and cannot authenticate is worse than none.
func TestHysteria2WithoutAPasswordIsRefused(t *testing.T) {
	srv := hy2Server()
	srv.Password = ""

	if _, err := hysteria2URI(srv); err == nil {
		t.Error("a hy2 URI was built with no password")
	}
	if _, err := clashHysteria2(srv); err == nil {
		t.Error("a Clash proxy was built with no password")
	}
	if _, err := singboxHysteria2(srv); err == nil {
		t.Error("a sing-box outbound was built with no password")
	}
}

// The subscription and the per-inbound panel must not disagree about what a
// protocol is: two readings of the same params is how one emits a vless link
// for a WireGuard tunnel while the other does not.
func TestServerFromInboundAgreesWithTheClientConfigBuilder(t *testing.T) {
	nodeRef := NodeRef{ID: 1, Name: "fra-1", Address: "203.0.113.10"}
	creds := Credentials{UUID: "u-1", Password: "p-1"}

	for _, tc := range []struct {
		kind       string
		params     map[string]any
		wantServer bool
	}{
		{"xray", map[string]any{"protocol": "vless", "port": 443.0}, true},
		{"hysteria2", map[string]any{"port": 443.0}, true},
		{"wireguard", map[string]any{"port": 51820.0}, false},
		{"openvpn", map[string]any{"port": 1194.0}, false},
		{"ocserv", map[string]any{"port": 443.0}, false},
		{"l2tp", map[string]any{}, false},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			in := Inbound{ServiceID: 1, AdapterKind: tc.kind, Params: tc.params}
			_, err := ServerFromInbound(in, nodeRef, creds)
			got := err == nil
			if got != tc.wantServer {
				t.Errorf("ServerFromInbound representable = %v, want %v (err=%v)",
					got, tc.wantServer, err)
			}

			// Whether or not it can be aggregated, the per-inbound builder
			// must still produce something for every runnable protocol.
			if _, err := BuildClientConfig(in, nodeRef, "alice", creds); err != nil {
				t.Errorf("BuildClientConfig produced nothing for %s: %v", tc.kind, err)
			}
		})
	}
}

// Transport and security come from the inbound, not from an assumption.
func TestServerFromInboundReadsTransport(t *testing.T) {
	srv, err := ServerFromInbound(
		Inbound{ServiceID: 1, AdapterKind: "xray", Params: map[string]any{
			"protocol": "vless", "port": 8080.0, "network": "ws",
			"security": "none", "path": "/ray",
		}},
		NodeRef{ID: 1, Name: "n", Address: "203.0.113.10"},
		Credentials{UUID: "u"},
	)
	if err != nil {
		t.Fatalf("ServerFromInbound: %v", err)
	}
	if srv.TLS {
		t.Error("a plaintext inbound was reported as TLS")
	}
	if srv.Network != "ws" || srv.Path != "/ray" {
		t.Errorf("transport lost: network=%q path=%q", srv.Network, srv.Path)
	}
}

// The sing-box document must stay valid JSON with the new outbound in it.
func TestSingBoxDocumentRemainsValidJSON(t *testing.T) {
	body, _, err := (&SingBoxRenderer{}).Render(
		context.Background(), []Server{vlessServer(), hy2Server()})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("sing-box output is not JSON: %v\n%s", err, body)
	}
	outbounds, ok := doc["outbounds"].([]any)
	if !ok || len(outbounds) < 2 {
		t.Fatalf("outbounds = %v", doc["outbounds"])
	}
}
