package subscriptions

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSingBoxRenderer_SingleVLESS(t *testing.T) {
	r := &SingBoxRenderer{}
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

	if !strings.Contains(contentType, "json") {
		t.Errorf("wrong content type: %s", contentType)
	}

	// Parse JSON.
	var config map[string]interface{}
	if err := json.Unmarshal(result, &config); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}

	outbounds, ok := config["outbounds"].([]interface{})
	if !ok || len(outbounds) != 1 {
		t.Fatalf("expected 1 outbound, got: %v", config["outbounds"])
	}

	outbound := outbounds[0].(map[string]interface{})
	if outbound["type"] != "vless" {
		t.Errorf("wrong type: %v", outbound["type"])
	}
	if outbound["tag"] != "Test-VLESS" {
		t.Errorf("wrong tag: %v", outbound["tag"])
	}
	if outbound["server"] != "vless.example.com" {
		t.Errorf("wrong server: %v", outbound["server"])
	}
	if outbound["server_port"] != float64(443) {
		t.Errorf("wrong server_port: %v", outbound["server_port"])
	}
	if outbound["uuid"] != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("wrong uuid: %v", outbound["uuid"])
	}

	// Check TLS
	tls, ok := outbound["tls"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing tls config: %v", outbound)
	}
	if tls["enabled"] != true {
		t.Errorf("wrong tls enabled: %v", tls["enabled"])
	}
	if tls["server_name"] != "vless.example.com" {
		t.Errorf("wrong server_name: %v", tls["server_name"])
	}

	// Check ALPN array
	alpn, ok := tls["alpn"].([]interface{})
	if !ok || len(alpn) != 2 {
		t.Errorf("wrong alpn: %v", tls["alpn"])
	}
}

func TestSingBoxRenderer_MultipleServers(t *testing.T) {
	r := &SingBoxRenderer{}
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
	if err := json.Unmarshal(result, &config); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}

	outbounds, ok := config["outbounds"].([]interface{})
	if !ok || len(outbounds) != 3 {
		t.Fatalf("expected 3 outbounds, got: %v", len(outbounds))
	}

	// Verify each outbound type.
	outbound0 := outbounds[0].(map[string]interface{})
	if outbound0["type"] != "vless" {
		t.Errorf("outbound 0 should be vless, got: %v", outbound0["type"])
	}

	outbound1 := outbounds[1].(map[string]interface{})
	if outbound1["type"] != "vmess" {
		t.Errorf("outbound 1 should be vmess, got: %v", outbound1["type"])
	}

	outbound2 := outbounds[2].(map[string]interface{})
	if outbound2["type"] != "trojan" {
		t.Errorf("outbound 2 should be trojan, got: %v", outbound2["type"])
	}
}

func TestSingBoxRenderer_VMess_WebSocket(t *testing.T) {
	r := &SingBoxRenderer{}
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
	if err := json.Unmarshal(result, &config); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}

	outbounds := config["outbounds"].([]interface{})
	outbound := outbounds[0].(map[string]interface{})

	if outbound["network"] != "ws" {
		t.Errorf("wrong network: %v", outbound["network"])
	}

	transport, ok := outbound["transport"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing transport: %v", outbound)
	}

	if transport["type"] != "ws" {
		t.Errorf("wrong transport type: %v", transport["type"])
	}

	if transport["path"] != "/v2ray" {
		t.Errorf("wrong ws path: %v", transport["path"])
	}
}

func TestSingBoxRenderer_VLESS_gRPC(t *testing.T) {
	r := &SingBoxRenderer{}
	servers := []Server{
		{
			NodeName:    "VLESS-gRPC",
			NodeAddress: "grpc.example.com",
			Port:        443,
			Protocol:    "vless",
			UUID:        "grpc-uuid-1234",
			TLS:         true,
			Network:     "grpc",
			Path:        "GunService",
		},
	}

	result, _, err := r.Render(context.Background(), servers)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	var config map[string]interface{}
	if err := json.Unmarshal(result, &config); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}

	outbounds := config["outbounds"].([]interface{})
	outbound := outbounds[0].(map[string]interface{})

	transport, ok := outbound["transport"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing transport: %v", outbound)
	}

	if transport["type"] != "grpc" {
		t.Errorf("wrong transport type: %v", transport["type"])
	}

	if transport["service_name"] != "GunService" {
		t.Errorf("wrong service_name: %v", transport["service_name"])
	}
}

func TestSingBoxRenderer_Trojan(t *testing.T) {
	r := &SingBoxRenderer{}
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
	if err := json.Unmarshal(result, &config); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}

	outbounds := config["outbounds"].([]interface{})
	outbound := outbounds[0].(map[string]interface{})

	if outbound["type"] != "trojan" {
		t.Errorf("wrong type: %v", outbound["type"])
	}
	if outbound["password"] != "my-secure-password" {
		t.Errorf("wrong password: %v", outbound["password"])
	}

	tls := outbound["tls"].(map[string]interface{})
	if tls["server_name"] != "trojan.example.com" {
		t.Errorf("wrong server_name: %v", tls["server_name"])
	}

	alpn := tls["alpn"].([]interface{})
	if len(alpn) != 1 || alpn[0] != "h2" {
		t.Errorf("wrong alpn: %v", alpn)
	}
}

func TestSingBoxRenderer_EmptyServers(t *testing.T) {
	r := &SingBoxRenderer{}
	_, _, err := r.Render(context.Background(), []Server{})
	if err == nil {
		t.Error("expected error for empty servers list")
	}
}

func TestSingBoxRenderer_UnsupportedProtocol(t *testing.T) {
	r := &SingBoxRenderer{}
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

func TestSingBoxRenderer_ValidJSON(t *testing.T) {
	r := &SingBoxRenderer{}
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

	// Verify it's parseable JSON.
	var parsed interface{}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Errorf("result is not valid JSON: %v", err)
	}
}
