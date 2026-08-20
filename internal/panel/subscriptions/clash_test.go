package subscriptions

import (
	"context"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestClashRenderer_SingleVLESS(t *testing.T) {
	r := &ClashRenderer{}
	servers := []Server{
		{
			NodeName:    "Test-VLESS",
			NodeAddress: "vless.example.com",
			Port:        443,
			Protocol:    "vless",
			UUID:        "11111111-2222-3333-4444-555555555555",
			TLS:         true,
			SNI:         "vless.example.com",
			Network:     "tcp",
			ALPN:        []string{"h2", "http/1.1"},
		},
	}

	result, contentType, err := r.Render(context.Background(), servers)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	if !strings.Contains(contentType, "yaml") {
		t.Errorf("wrong content type: %s", contentType)
	}

	// Parse YAML.
	var config map[string]interface{}
	if err := yaml.Unmarshal(result, &config); err != nil {
		t.Fatalf("unmarshal yaml: %v", err)
	}

	proxies, ok := config["proxies"].([]interface{})
	if !ok || len(proxies) != 1 {
		t.Fatalf("expected 1 proxy, got: %v", config["proxies"])
	}

	proxy := proxies[0].(map[string]interface{})
	if proxy["type"] != "vless" {
		t.Errorf("wrong type: %v", proxy["type"])
	}
	if proxy["name"] != "Test-VLESS" {
		t.Errorf("wrong name: %v", proxy["name"])
	}
	if proxy["server"] != "vless.example.com" {
		t.Errorf("wrong server: %v", proxy["server"])
	}
	if proxy["port"] != 443 {
		t.Errorf("wrong port: %v", proxy["port"])
	}
	if proxy["uuid"] != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("wrong uuid: %v", proxy["uuid"])
	}
	if proxy["tls"] != true {
		t.Errorf("wrong tls: %v", proxy["tls"])
	}
	if proxy["servername"] != "vless.example.com" {
		t.Errorf("wrong servername: %v", proxy["servername"])
	}

	// Check ALPN array
	alpn, ok := proxy["alpn"].([]interface{})
	if !ok || len(alpn) != 2 {
		t.Errorf("wrong alpn: %v", proxy["alpn"])
	}
}

func TestClashRenderer_MultipleServers(t *testing.T) {
	r := &ClashRenderer{}
	servers := []Server{
		{
			NodeName:    "Node-A-VLESS",
			NodeAddress: "a.example.com",
			Port:        443,
			Protocol:    "vless",
			UUID:        "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			TLS:         true,
		},
		{
			NodeName:    "Node-B-VMess",
			NodeAddress: "b.example.com",
			Port:        8443,
			Protocol:    "vmess",
			UUID:        "bbbbbbbb-cccc-dddd-eeee-ffffffffffff",
			TLS:         true,
			Network:     "ws",
			Path:        "/vmess",
		},
		{
			NodeName:    "Node-C-Trojan",
			NodeAddress: "c.example.com",
			Port:        443,
			Protocol:    "trojan",
			Password:    "trojan-pass",
			TLS:         true,
		},
	}

	result, _, err := r.Render(context.Background(), servers)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	var config map[string]interface{}
	if err := yaml.Unmarshal(result, &config); err != nil {
		t.Fatalf("unmarshal yaml: %v", err)
	}

	proxies, ok := config["proxies"].([]interface{})
	if !ok || len(proxies) != 3 {
		t.Fatalf("expected 3 proxies, got: %v", len(proxies))
	}

	// Verify each proxy type.
	proxy0 := proxies[0].(map[string]interface{})
	if proxy0["type"] != "vless" {
		t.Errorf("proxy 0 should be vless, got: %v", proxy0["type"])
	}

	proxy1 := proxies[1].(map[string]interface{})
	if proxy1["type"] != "vmess" {
		t.Errorf("proxy 1 should be vmess, got: %v", proxy1["type"])
	}

	proxy2 := proxies[2].(map[string]interface{})
	if proxy2["type"] != "trojan" {
		t.Errorf("proxy 2 should be trojan, got: %v", proxy2["type"])
	}
}

func TestClashRenderer_VMess_WebSocket(t *testing.T) {
	r := &ClashRenderer{}
	servers := []Server{
		{
			NodeName:    "VMess-WS",
			NodeAddress: "ws.example.com",
			Port:        443,
			Protocol:    "vmess",
			UUID:        "vmess-uuid-1234",
			TLS:         true,
			SNI:         "ws.example.com",
			Network:     "ws",
			Path:        "/v2ray",
		},
	}

	result, _, err := r.Render(context.Background(), servers)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	var config map[string]interface{}
	if err := yaml.Unmarshal(result, &config); err != nil {
		t.Fatalf("unmarshal yaml: %v", err)
	}

	proxies := config["proxies"].([]interface{})
	proxy := proxies[0].(map[string]interface{})

	if proxy["network"] != "ws" {
		t.Errorf("wrong network: %v", proxy["network"])
	}

	wsOpts, ok := proxy["ws-opts"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing ws-opts: %v", proxy)
	}

	if wsOpts["path"] != "/v2ray" {
		t.Errorf("wrong ws path: %v", wsOpts["path"])
	}
}

func TestClashRenderer_Trojan(t *testing.T) {
	r := &ClashRenderer{}
	servers := []Server{
		{
			NodeName:    "Trojan-Node",
			NodeAddress: "trojan.example.com",
			Port:        443,
			Protocol:    "trojan",
			Password:    "my-secure-password",
			TLS:         true,
			SNI:         "trojan.example.com",
			ALPN:        []string{"h2"},
		},
	}

	result, _, err := r.Render(context.Background(), servers)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	var config map[string]interface{}
	if err := yaml.Unmarshal(result, &config); err != nil {
		t.Fatalf("unmarshal yaml: %v", err)
	}

	proxies := config["proxies"].([]interface{})
	proxy := proxies[0].(map[string]interface{})

	if proxy["type"] != "trojan" {
		t.Errorf("wrong type: %v", proxy["type"])
	}
	if proxy["password"] != "my-secure-password" {
		t.Errorf("wrong password: %v", proxy["password"])
	}
	if proxy["sni"] != "trojan.example.com" {
		t.Errorf("wrong sni: %v", proxy["sni"])
	}

	alpn := proxy["alpn"].([]interface{})
	if len(alpn) != 1 || alpn[0] != "h2" {
		t.Errorf("wrong alpn: %v", alpn)
	}
}

func TestClashRenderer_EmptyServers(t *testing.T) {
	r := &ClashRenderer{}
	_, _, err := r.Render(context.Background(), []Server{})
	if err == nil {
		t.Error("expected error for empty servers list")
	}
}

func TestClashRenderer_UnsupportedProtocol(t *testing.T) {
	r := &ClashRenderer{}
	servers := []Server{
		{
			NodeName:    "Unknown",
			NodeAddress: "unknown.example.com",
			Port:        443,
			Protocol:    "shadowsocks",
		},
	}

	_, _, err := r.Render(context.Background(), servers)
	if err == nil {
		t.Error("expected error for unsupported protocol")
	}
}

func TestClashRenderer_ValidYAML(t *testing.T) {
	r := &ClashRenderer{}
	servers := []Server{
		{
			NodeName:    "Test",
			NodeAddress: "test.example.com",
			Port:        443,
			Protocol:    "vless",
			UUID:        "test-uuid",
			TLS:         true,
		},
	}

	result, _, err := r.Render(context.Background(), servers)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	// Verify it's parseable YAML.
	var parsed interface{}
	if err := yaml.Unmarshal(result, &parsed); err != nil {
		t.Errorf("result is not valid YAML: %v", err)
	}
}
