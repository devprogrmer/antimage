package subscriptions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestV2RayRenderer_SingleVLESS(t *testing.T) {
	r := &V2RayRenderer{}
	servers := []Server{
		{
			NodeName:    "Test-Node-1",
			NodeAddress: "node1.example.com",
			Port:        443,
			Protocol:    "vless",
			UUID:        "11111111-2222-3333-4444-555555555555",
			TLS:         true,
			SNI:         "node1.example.com",
			Network:     "tcp",
		},
	}

	result, contentType, err := r.Render(context.Background(), servers)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	if contentType != "text/plain; charset=utf-8" {
		t.Errorf("wrong content type: %s", contentType)
	}

	// Decode base64.
	decoded, err := base64.StdEncoding.DecodeString(string(result))
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}

	uri := string(decoded)
	if !strings.HasPrefix(uri, "vless://") {
		t.Errorf("expected vless:// prefix, got: %s", uri)
	}

	if !strings.Contains(uri, "node1.example.com") {
		t.Errorf("missing node address in URI: %s", uri)
	}

	if !strings.Contains(uri, "11111111-2222-3333-4444-555555555555") {
		t.Errorf("missing UUID in URI: %s", uri)
	}

	if !strings.Contains(uri, "security=tls") {
		t.Errorf("missing TLS parameter: %s", uri)
	}
}

func TestV2RayRenderer_MultipleServers(t *testing.T) {
	r := &V2RayRenderer{}
	servers := []Server{
		{
			NodeName:    "Node-A",
			NodeAddress: "a.example.com",
			Port:        443,
			Protocol:    "vless",
			UUID:        "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			TLS:         true,
		},
		{
			NodeName:    "Node-B",
			NodeAddress: "b.example.com",
			Port:        8443,
			Protocol:    "vmess",
			UUID:        "bbbbbbbb-cccc-dddd-eeee-ffffffffffff",
			TLS:         false,
			Network:     "ws",
			Path:        "/vmess",
		},
		{
			NodeName:    "Node-C",
			NodeAddress: "c.example.com",
			Port:        443,
			Protocol:    "trojan",
			Password:    "secure-password-123",
			TLS:         true,
		},
	}

	result, _, err := r.Render(context.Background(), servers)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	// Decode and verify it has 3 lines.
	decoded, err := base64.StdEncoding.DecodeString(string(result))
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}

	lines := strings.Split(string(decoded), "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d", len(lines))
	}

	// Check each protocol.
	if !strings.HasPrefix(lines[0], "vless://") {
		t.Errorf("first line should be vless://, got: %s", lines[0])
	}
	if !strings.HasPrefix(lines[1], "vmess://") {
		t.Errorf("second line should be vmess://, got: %s", lines[1])
	}
	if !strings.HasPrefix(lines[2], "trojan://") {
		t.Errorf("third line should be trojan://, got: %s", lines[2])
	}
}

func TestV2RayRenderer_VMess(t *testing.T) {
	r := &V2RayRenderer{}
	servers := []Server{
		{
			NodeName:    "VMess-Node",
			NodeAddress: "vmess.example.com",
			Port:        10086,
			Protocol:    "vmess",
			UUID:        "ffffffff-eeee-dddd-cccc-bbbbbbbbbbbb",
			TLS:         true,
			SNI:         "vmess.example.com",
			Network:     "ws",
			Path:        "/v2ray",
		},
	}

	result, _, err := r.Render(context.Background(), servers)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	// Decode outer base64.
	decoded, err := base64.StdEncoding.DecodeString(string(result))
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}

	uri := string(decoded)
	if !strings.HasPrefix(uri, "vmess://") {
		t.Errorf("expected vmess:// prefix, got: %s", uri)
	}

	// Decode inner base64 (vmess JSON).
	vmessB64 := strings.TrimPrefix(uri, "vmess://")
	vmessJSON, err := base64.StdEncoding.DecodeString(vmessB64)
	if err != nil {
		t.Fatalf("decode vmess base64: %v", err)
	}

	var vmess map[string]interface{}
	if err := json.Unmarshal(vmessJSON, &vmess); err != nil {
		t.Fatalf("unmarshal vmess json: %v", err)
	}

	// Verify fields.
	if vmess["add"] != "vmess.example.com" {
		t.Errorf("wrong address: %v", vmess["add"])
	}
	if vmess["port"] != float64(10086) {
		t.Errorf("wrong port: %v", vmess["port"])
	}
	if vmess["id"] != "ffffffff-eeee-dddd-cccc-bbbbbbbbbbbb" {
		t.Errorf("wrong UUID: %v", vmess["id"])
	}
	if vmess["net"] != "ws" {
		t.Errorf("wrong network: %v", vmess["net"])
	}
	if vmess["path"] != "/v2ray" {
		t.Errorf("wrong path: %v", vmess["path"])
	}
	if vmess["tls"] != "tls" {
		t.Errorf("wrong tls: %v", vmess["tls"])
	}
}

func TestV2RayRenderer_Trojan(t *testing.T) {
	r := &V2RayRenderer{}
	servers := []Server{
		{
			NodeName:    "Trojan-Node",
			NodeAddress: "trojan.example.com",
			Port:        443,
			Protocol:    "trojan",
			Password:    "my-trojan-password",
			TLS:         true,
			SNI:         "trojan.example.com",
			ALPN:        []string{"h2", "http/1.1"},
		},
	}

	result, _, err := r.Render(context.Background(), servers)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}

	decoded, err := base64.StdEncoding.DecodeString(string(result))
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}

	uri := string(decoded)
	if !strings.HasPrefix(uri, "trojan://") {
		t.Errorf("expected trojan:// prefix, got: %s", uri)
	}

	if !strings.Contains(uri, "my-trojan-password") {
		t.Errorf("missing password in URI: %s", uri)
	}

	if !strings.Contains(uri, "trojan.example.com") {
		t.Errorf("missing address in URI: %s", uri)
	}

	if !strings.Contains(uri, "security=tls") {
		t.Errorf("missing TLS parameter: %s", uri)
	}

	if !strings.Contains(uri, "alpn=h2,http/1.1") {
		t.Errorf("missing ALPN parameter: %s", uri)
	}
}

func TestV2RayRenderer_EmptyServers(t *testing.T) {
	r := &V2RayRenderer{}
	_, _, err := r.Render(context.Background(), []Server{})
	if err == nil {
		t.Error("expected error for empty servers list")
	}
}

func TestV2RayRenderer_UnsupportedProtocol(t *testing.T) {
	r := &V2RayRenderer{}
	servers := []Server{
		{
			NodeName:    "Unknown",
			NodeAddress: "unknown.example.com",
			Port:        443,
			Protocol:    "shadowsocks", // Not supported yet
		},
	}

	_, _, err := r.Render(context.Background(), servers)
	if err == nil {
		t.Error("expected error for unsupported protocol")
	}
}
