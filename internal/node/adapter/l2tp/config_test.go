package l2tp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/amyrm/antimage/internal/node/adapter"
)

func TestParseServiceParams(t *testing.T) {
	raw := json.RawMessage(`{
		"ip_range": "10.8.0.2-10.8.0.254",
		"local_ip": "10.8.0.1",
		"psk": "my-secret-psk-key-16chars",
		"dns_servers": ["8.8.8.8", "8.8.4.4"]
	}`)

	params, err := parseServiceParams(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if params.IPRange != "10.8.0.2-10.8.0.254" {
		t.Errorf("want ip_range %q, got %q", "10.8.0.2-10.8.0.254", params.IPRange)
	}
	if params.LocalIP != "10.8.0.1" {
		t.Errorf("want local_ip %q, got %q", "10.8.0.1", params.LocalIP)
	}
	if params.PSK != "my-secret-psk-key-16chars" {
		t.Errorf("want psk %q, got %q", "my-secret-psk-key-16chars", params.PSK)
	}
	if len(params.DNSServers) != 2 {
		t.Fatalf("want 2 dns servers, got %d", len(params.DNSServers))
	}
	if params.DNSServers[0] != "8.8.8.8" || params.DNSServers[1] != "8.8.4.4" {
		t.Errorf("wrong dns servers: %v", params.DNSServers)
	}
}

func TestRenderIPsecConf(t *testing.T) {
	params := ServiceParams{
		IPRange: "10.8.0.2-10.8.0.254",
		LocalIP: "10.8.0.1",
		PSK:     "test-psk-secret",
	}

	result := renderIPsecConf(123, params)

	// Check marker line
	if !strings.HasPrefix(result, markerPrefix) {
		t.Error("missing marker prefix")
	}
	if !strings.Contains(result, "service_id=123") {
		t.Error("missing service_id in marker")
	}
	if !strings.Contains(result, "checksum=") {
		t.Error("missing checksum in marker")
	}

	// Check config content
	if !strings.Contains(result, "conn antimage-l2tp") {
		t.Error("missing connection config")
	}
	if !strings.Contains(result, "keyexchange=ikev2") {
		t.Error("missing ikev2 config")
	}
	if !strings.Contains(result, "authby=secret") {
		t.Error("missing PSK auth config")
	}
	if !strings.Contains(result, "rightsubnet=10.8.0.1/24") {
		t.Error("missing or wrong subnet config")
	}
}

func TestRenderIPsecSecrets(t *testing.T) {
	params := ServiceParams{
		PSK: "my-psk-secret-key",
	}

	result := renderIPsecSecrets(456, params)

	if !strings.HasPrefix(result, markerPrefix) {
		t.Error("missing marker prefix")
	}
	if !strings.Contains(result, "service_id=456") {
		t.Error("missing service_id")
	}
	if !strings.Contains(result, `PSK "my-psk-secret-key"`) {
		t.Error("missing or wrong PSK line")
	}
}

func TestRenderXL2TPDConf(t *testing.T) {
	params := ServiceParams{
		IPRange: "10.8.0.2-10.8.0.254",
		LocalIP: "10.8.0.1",
	}

	result := renderXL2TPDConf(123, params)

	// xl2tpd uses semicolon for comments
	if !strings.HasPrefix(result, markerXL2TPD) {
		t.Error("missing xl2tpd marker prefix")
	}
	if !strings.Contains(result, "service_id=123") {
		t.Error("missing service_id")
	}
	if !strings.Contains(result, "[lns default]") {
		t.Error("missing lns section")
	}
	if !strings.Contains(result, "ip range = 10.8.0.2-10.8.0.254") {
		t.Error("missing or wrong ip range")
	}
	if !strings.Contains(result, "local ip = 10.8.0.1") {
		t.Error("missing or wrong local ip")
	}
	if !strings.Contains(result, "require chap = yes") {
		t.Error("missing CHAP requirement")
	}
}

func TestRenderCHAPSecrets(t *testing.T) {
	subjects := []adapter.Subject{
		{
			ID: 1,
			Credentials: []adapter.Credential{
				{Kind: string(adapter.CredPassword), Value: "secret123"},
			},
		},
		{
			ID: 2,
			Credentials: []adapter.Credential{
				{Kind: string(adapter.CredPassword), Value: "secret456"},
			},
		},
	}

	result := renderCHAPSecrets(123, subjects)

	if !strings.HasPrefix(result, markerPrefix) {
		t.Error("missing marker prefix")
	}
	if !strings.Contains(result, "service_id=123") {
		t.Error("missing service_id")
	}
	if !strings.Contains(result, "user1") {
		t.Error("missing user1")
	}
	if !strings.Contains(result, "secret123") {
		t.Error("missing password for user1")
	}
	if !strings.Contains(result, "user2") {
		t.Error("missing user2")
	}
	if !strings.Contains(result, "secret456") {
		t.Error("missing password for user2")
	}

	// Verify format: username * password *
	lines := strings.Split(strings.TrimSpace(extractPayload(result)), "\n")
	if len(lines) != 2 {
		t.Errorf("want 2 user lines, got %d", len(lines))
	}
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 4 {
			t.Errorf("want 4 fields per line, got %d in %q", len(fields), line)
		}
		if fields[1] != "*" || fields[3] != "*" {
			t.Errorf("wrong CHAP format: %q", line)
		}
	}
}

func TestRenderCHAPSecretsDeterministic(t *testing.T) {
	subjects := []adapter.Subject{
		{ID: 3, Credentials: []adapter.Credential{{Kind: "password", Value: "c"}}},
		{ID: 1, Credentials: []adapter.Credential{{Kind: "password", Value: "a"}}},
		{ID: 2, Credentials: []adapter.Credential{{Kind: "password", Value: "b"}}},
	}

	result1 := renderCHAPSecrets(123, subjects)
	result2 := renderCHAPSecrets(123, subjects)

	if result1 != result2 {
		t.Error("renderCHAPSecrets not deterministic")
	}

	// Verify sorted output
	lines := strings.Split(strings.TrimSpace(extractPayload(result1)), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d", len(lines))
	}
	// Should be sorted: user1, user2, user3
	if !strings.HasPrefix(lines[0], "user1") {
		t.Errorf("first line should be user1, got %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "user2") {
		t.Errorf("second line should be user2, got %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], "user3") {
		t.Errorf("third line should be user3, got %q", lines[2])
	}
}

func TestRenderPPPOptions(t *testing.T) {
	params := ServiceParams{
		DNSServers: []string{"8.8.8.8", "8.8.4.4"},
	}

	result := renderPPPOptions(123, params)

	if !strings.HasPrefix(result, markerPrefix) {
		t.Error("missing marker prefix")
	}
	if !strings.Contains(result, "service_id=123") {
		t.Error("missing service_id")
	}
	if !strings.Contains(result, "require-mschap-v2") {
		t.Error("missing mschap-v2 requirement")
	}
	if !strings.Contains(result, "ms-dns 8.8.8.8") {
		t.Error("missing first DNS server")
	}
	if !strings.Contains(result, "ms-dns 8.8.4.4") {
		t.Error("missing second DNS server")
	}
	if !strings.Contains(result, "proxyarp") {
		t.Error("missing proxyarp")
	}
}

func TestRenderPPPOptionsNoDNS(t *testing.T) {
	params := ServiceParams{
		DNSServers: nil,
	}

	result := renderPPPOptions(123, params)

	if strings.Contains(result, "ms-dns") {
		t.Error("should not contain DNS lines when no DNS servers configured")
	}
	if !strings.Contains(result, "require-mschap-v2") {
		t.Error("should still contain other options")
	}
}

func TestChecksumOf(t *testing.T) {
	payload := "test payload"
	checksum1 := checksumOf(payload)
	checksum2 := checksumOf(payload)

	if checksum1 != checksum2 {
		t.Error("checksumOf not deterministic")
	}
	if len(checksum1) != 64 {
		t.Errorf("want 64-char hex, got %d", len(checksum1))
	}

	// Different payload should produce different checksum
	checksum3 := checksumOf("different payload")
	if checksum1 == checksum3 {
		t.Error("different payloads should produce different checksums")
	}
}

func TestSanitizeUsername(t *testing.T) {
	tests := []struct {
		subjectID int64
		want      string
	}{
		{1, "user1"},
		{42, "user42"},
		{999, "user999"},
	}

	for _, tt := range tests {
		got := sanitizeUsername(tt.subjectID)
		if got != tt.want {
			t.Errorf("sanitizeUsername(%d) = %q, want %q", tt.subjectID, got, tt.want)
		}
	}
}

func TestIsManaged(t *testing.T) {
	tests := []struct {
		content string
		want    bool
	}{
		{markerPrefix + " service_id=1 checksum=abc\npayload", true},
		{markerXL2TPD + " service_id=1 checksum=abc\npayload", true},
		{"# regular comment\npayload", false},
		{"; regular comment\npayload", false},
		{"payload without marker", false},
		{"", false},
	}

	for _, tt := range tests {
		got := isManaged(tt.content)
		if got != tt.want {
			t.Errorf("isManaged(%q) = %v, want %v", tt.content[:min(20, len(tt.content))], got, tt.want)
		}
	}
}

func TestParseMarker(t *testing.T) {
	tests := []struct {
		line          string
		wantServiceID int64
		wantChecksum  string
		wantOK        bool
	}{
		{
			line:          markerPrefix + " service_id=123 checksum=abc123def",
			wantServiceID: 123,
			wantChecksum:  "abc123def",
			wantOK:        true,
		},
		{
			line:          markerXL2TPD + " service_id=456 checksum=xyz789",
			wantServiceID: 456,
			wantChecksum:  "xyz789",
			wantOK:        true,
		},
		{
			line:   "# regular comment",
			wantOK: false,
		},
		{
			line:   markerPrefix + " service_id=789",
			wantOK: false, // missing checksum
		},
		{
			line:   markerPrefix + " checksum=abc",
			wantOK: false, // missing service_id
		},
	}

	for _, tt := range tests {
		gotID, gotChecksum, gotOK := parseMarker(tt.line)
		if gotOK != tt.wantOK {
			t.Errorf("parseMarker(%q) ok = %v, want %v", tt.line, gotOK, tt.wantOK)
		}
		if tt.wantOK {
			if gotID != tt.wantServiceID {
				t.Errorf("parseMarker(%q) id = %d, want %d", tt.line, gotID, tt.wantServiceID)
			}
			if gotChecksum != tt.wantChecksum {
				t.Errorf("parseMarker(%q) checksum = %q, want %q", tt.line, gotChecksum, tt.wantChecksum)
			}
		}
	}
}

func TestExtractPayload(t *testing.T) {
	content := markerPrefix + " service_id=123 checksum=abc\nconfig line 1\nconfig line 2"
	payload := extractPayload(content)

	want := "config line 1\nconfig line 2"
	if payload != want {
		t.Errorf("extractPayload = %q, want %q", payload, want)
	}
}

func TestExtractPayloadEmpty(t *testing.T) {
	content := markerPrefix + " service_id=123 checksum=abc"
	payload := extractPayload(content)

	if payload != "" {
		t.Errorf("extractPayload (no newline) = %q, want empty", payload)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
