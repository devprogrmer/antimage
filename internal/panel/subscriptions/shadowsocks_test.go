package subscriptions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func ssServer() Server {
	return Server{
		NodeID: 3, NodeName: "sgp-1", NodeAddress: "203.0.113.30", ServiceID: 9,
		Protocol: "shadowsocks", Port: 8388,
		Password: "s3cret", Method: "chacha20-ietf-poly1305",
	}
}

// SIP002: base64url(method:password), UNPADDED, in the userinfo position. The
// older SIP001 form encoded the whole userinfo@host:port and is refused by
// current clients -- silently, which is why this is asserted by decoding
// rather than by eyeballing the string.
func TestShadowsocksURIIsSIP002(t *testing.T) {
	uri, err := shadowsocksURI(ssServer())
	if err != nil {
		t.Fatalf("shadowsocksURI: %v", err)
	}
	if !strings.HasPrefix(uri, "ss://") {
		t.Fatalf("not a shadowsocks link: %s", uri)
	}

	rest := strings.TrimPrefix(uri, "ss://")
	at := strings.Index(rest, "@")
	if at < 0 {
		t.Fatalf("SIP002 requires userinfo@host:port; got %s", uri)
	}
	raw, err := base64.RawURLEncoding.DecodeString(rest[:at])
	if err != nil {
		t.Fatalf("userinfo is not unpadded base64url: %v", err)
	}
	if string(raw) != "chacha20-ietf-poly1305:s3cret" {
		t.Errorf("userinfo = %q, want method:password", raw)
	}
	// The host must be in the clear, not folded into the base64 blob.
	if !strings.Contains(rest[at:], "203.0.113.30:8388") {
		t.Errorf("host and port are not in the clear: %s", uri)
	}
}

// Clash uses its own spelling. Using the protocol's names produces a proxy
// Clash silently ignores.
func TestClashShadowsocksUsesClashFieldNames(t *testing.T) {
	proxy, err := clashShadowsocks(ssServer())
	if err != nil {
		t.Fatalf("clashShadowsocks: %v", err)
	}
	if proxy["type"] != "ss" {
		t.Errorf(`type = %v, want "ss" -- Clash does not recognise "shadowsocks"`, proxy["type"])
	}
	if proxy["cipher"] != "chacha20-ietf-poly1305" {
		t.Errorf(`cipher = %v; Clash calls this "cipher", not "method"`, proxy["cipher"])
	}
	if _, wrong := proxy["method"]; wrong {
		t.Error(`the proxy carries a "method" key, which Clash ignores`)
	}
	// Shadowsocks relays UDP and Clash defaults it off; leaving it off breaks
	// DNS and every UDP application through the tunnel.
	if proxy["udp"] != true {
		t.Error("udp is not enabled, so DNS through the tunnel will fail")
	}
}

func TestSingBoxShadowsocksUsesSingBoxFieldNames(t *testing.T) {
	out, err := singboxShadowsocks(ssServer())
	if err != nil {
		t.Fatalf("singboxShadowsocks: %v", err)
	}
	if out["type"] != "shadowsocks" {
		t.Errorf(`type = %v, want "shadowsocks" -- sing-box does not recognise "ss"`, out["type"])
	}
	if out["method"] != "chacha20-ietf-poly1305" {
		t.Errorf(`method = %v; sing-box calls this "method", not "cipher"`, out["method"])
	}
	if _, wrong := out["cipher"]; wrong {
		t.Error(`the outbound carries a "cipher" key, which sing-box ignores`)
	}
	// Shadowsocks carries its own encryption. An empty TLS block would make
	// sing-box reject the outbound.
	if _, hasTLS := out["tls"]; hasTLS {
		t.Error("a TLS block was emitted for a protocol that has no TLS layer")
	}
	if out["server_port"] != 8388 {
		t.Errorf("server_port = %v", out["server_port"])
	}
}

// A wrong cipher does not look broken -- it connects and then fails to
// decrypt, which is far harder to diagnose than a refused connection.
func TestTheCipherSurvivesIntoEveryFormat(t *testing.T) {
	srv := ssServer()
	srv.Method = "aes-128-gcm"

	uri, err := shadowsocksURI(srv)
	if err != nil {
		t.Fatalf("shadowsocksURI: %v", err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(
		strings.TrimPrefix(uri, "ss://")[:strings.Index(strings.TrimPrefix(uri, "ss://"), "@")])
	if !strings.HasPrefix(string(raw), "aes-128-gcm:") {
		t.Errorf("URI lost the cipher: %q", raw)
	}

	proxy, _ := clashShadowsocks(srv)
	if proxy["cipher"] != "aes-128-gcm" {
		t.Errorf("Clash lost the cipher: %v", proxy["cipher"])
	}
	out, _ := singboxShadowsocks(srv)
	if out["method"] != "aes-128-gcm" {
		t.Errorf("sing-box lost the cipher: %v", out["method"])
	}
}

// An inbound naming no cipher gets the documented default rather than an empty
// one, which every client rejects.
func TestAnAbsentCipherFallsBackRatherThanEmpty(t *testing.T) {
	srv := ssServer()
	srv.Method = ""

	proxy, _ := clashShadowsocks(srv)
	if proxy["cipher"] == "" || proxy["cipher"] == nil {
		t.Error("Clash proxy has an empty cipher, which every client refuses")
	}
	out, _ := singboxShadowsocks(srv)
	if out["method"] == "" || out["method"] == nil {
		t.Error("sing-box outbound has an empty method")
	}
	if proxy["cipher"] != out["method"] {
		t.Errorf("the two formats defaulted differently: %v vs %v",
			proxy["cipher"], out["method"])
	}
}

func TestShadowsocksWithoutAPasswordIsRefused(t *testing.T) {
	srv := ssServer()
	srv.Password = ""

	if _, err := shadowsocksURI(srv); err == nil {
		t.Error("a ss URI was built with no password")
	}
	if _, err := clashShadowsocks(srv); err == nil {
		t.Error("a Clash proxy was built with no password")
	}
	if _, err := singboxShadowsocks(srv); err == nil {
		t.Error("a sing-box outbound was built with no password")
	}
}

// The cipher has to survive the mapper too, or the renderers get an empty one
// and silently fall back for an inbound that named something else.
func TestServerFromInboundCarriesTheCipher(t *testing.T) {
	srv, err := ServerFromInbound(
		Inbound{ServiceID: 1, AdapterKind: "singbox", Params: map[string]any{
			"protocol": "shadowsocks", "port": 8388.0, "method": "aes-128-gcm",
		}},
		NodeRef{ID: 1, Name: "n", Address: "203.0.113.1"},
		Credentials{Password: "pw"},
	)
	if err != nil {
		t.Fatalf("ServerFromInbound: %v", err)
	}
	if srv.Method != "aes-128-gcm" {
		t.Errorf("Method = %q, want the inbound's cipher", srv.Method)
	}
}

// End to end through all three renderers, alongside a protocol none of them
// can carry.
func TestShadowsocksReachesEveryAggregatedFormat(t *testing.T) {
	servers := []Server{
		ssServer(),
		{NodeName: "wg-1", NodeAddress: "203.0.113.20", Protocol: "wireguard", Port: 51820},
	}
	ctx := context.Background()

	t.Run("v2ray", func(t *testing.T) {
		body, _, err := (&V2RayRenderer{}).Render(ctx, servers)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		raw, err := base64.StdEncoding.DecodeString(string(body))
		if err != nil {
			t.Fatalf("not base64: %v", err)
		}
		if !strings.Contains(string(raw), "ss://") {
			t.Errorf("shadowsocks is missing: %q", raw)
		}
	})

	t.Run("clash", func(t *testing.T) {
		body, _, err := (&ClashRenderer{}).Render(ctx, servers)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if !strings.Contains(string(body), "type: ss") {
			t.Errorf("shadowsocks is missing from the Clash document:\n%s", body)
		}
	})

	t.Run("singbox", func(t *testing.T) {
		body, _, err := (&SingBoxRenderer{}).Render(ctx, servers)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		var doc map[string]any
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatalf("sing-box output is not JSON: %v", err)
		}
		if !strings.Contains(string(body), `"shadowsocks"`) {
			t.Errorf("shadowsocks is missing from the sing-box document:\n%s", body)
		}
		// And the skipped WireGuard server must not be named in the selector.
		if strings.Contains(string(body), "wg-1") {
			t.Errorf("a skipped server was named in the document:\n%s", body)
		}
	})
}
