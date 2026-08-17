package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "node.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadConfigParsesAllFields(t *testing.T) {
	path := writeConfig(t, `
panel_url: https://panel.example:8443
token: abc123
ca_fingerprint: `+"deadbeef"+`
state_dir: /var/lib/antimage
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.PanelURL != "https://panel.example:8443" {
		t.Errorf("PanelURL = %q", cfg.PanelURL)
	}
	if cfg.Token != "abc123" || cfg.CAFingerprint != "deadbeef" {
		t.Errorf("token/fingerprint = %q/%q", cfg.Token, cfg.CAFingerprint)
	}
	if cfg.StateDir != "/var/lib/antimage" {
		t.Errorf("StateDir = %q", cfg.StateDir)
	}
}

func TestLoadConfigRejectsMissingPanelURL(t *testing.T) {
	path := writeConfig(t, "token: abc\nca_fingerprint: dead\n")
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("LoadConfig accepted a config with no panel_url")
	}
}

// The pinned fingerprint is what protects the agent from a hijacked DNS
// record, so a config without one must be refused rather than defaulting to
// system trust.
func TestLoadConfigRejectsMissingCAFingerprint(t *testing.T) {
	path := writeConfig(t, "panel_url: https://p.example\ntoken: abc\n")
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("LoadConfig accepted a config with no ca_fingerprint")
	}
}

func TestLoadConfigDefaultsStateDir(t *testing.T) {
	path := writeConfig(t, "panel_url: https://p.example\ntoken: abc\nca_fingerprint: dead\n")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.StateDir != DefaultStateDir {
		t.Errorf("StateDir = %q, want %q", cfg.StateDir, DefaultStateDir)
	}
}

func TestLoadConfigMissingFileIsAnError(t *testing.T) {
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("LoadConfig accepted a missing file")
	}
}

// A panel_url that cannot become a gRPC target must fail at load, not on
// every RPC. grpc.NewClient accepts nonsense targets and only fails later,
// with an Unavailable the reconnect loop cannot distinguish from downtime.
func TestLoadConfigRejectsUnusablePanelURL(t *testing.T) {
	path := writeConfig(t, "panel_url: https://p.example/api\nca_fingerprint: dead\n")
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("LoadConfig accepted a panel_url that cannot be dialled")
	}
}

func TestGRPCTargetDerivesHostPort(t *testing.T) {
	for _, tc := range []struct {
		name     string
		panelURL string
		want     string
	}{
		{"scheme and port", "https://panel.example:8443", "panel.example:8443"},
		{"scheme without port", "https://panel.example", "panel.example:" + DefaultPanelPort},
		{"bare host and port", "panel.example:9443", "panel.example:9443"},
		{"bare host without port", "panel.example", "panel.example:" + DefaultPanelPort},
		{"surrounding whitespace", "  https://panel.example:8443  ", "panel.example:8443"},
		{"ipv4 literal", "https://10.0.0.7:8443", "10.0.0.7:8443"},
		{"bracketed ipv6 with port", "https://[2001:db8::1]:8443", "[2001:db8::1]:8443"},
		{"bracketed ipv6 without port", "https://[2001:db8::1]", "[2001:db8::1]:" + DefaultPanelPort},
		{"bare ipv6 literal", "::1", "[::1]:" + DefaultPanelPort},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{PanelURL: tc.panelURL}
			got, err := cfg.GRPCTarget()
			if err != nil {
				t.Fatalf("GRPCTarget(%q): %v", tc.panelURL, err)
			}
			if got != tc.want {
				t.Errorf("GRPCTarget(%q) = %q, want %q", tc.panelURL, got, tc.want)
			}
		})
	}
}

// The scheme, the path and the port are all load-bearing: gRPC would accept
// the raw string and then fail every RPC, so each of these must be caught
// here where the message can say what is actually wrong.
func TestGRPCTargetRejectsUnusableURLs(t *testing.T) {
	for _, tc := range []struct{ name, panelURL string }{
		{"empty", ""},
		{"trailing path", "https://panel.example:8443/api"},
		{"root path", "https://panel.example:8443/"},
		{"query string", "https://panel.example:8443?x=1"},
		{"fragment", "https://panel.example:8443#frag"},
		{"plaintext scheme", "http://panel.example:8443"},
		{"grpc scheme", "grpc://panel.example:8443"},
		{"no host", "https://"},
		{"userinfo", "https://user@panel.example:8443"},
		{"non-numeric port", "https://panel.example:https"},
		{"port out of range", "https://panel.example:70000"},
		{"port zero", "https://panel.example:0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{PanelURL: tc.panelURL}
			got, err := cfg.GRPCTarget()
			if err == nil {
				t.Fatalf("GRPCTarget(%q) = %q, want an error", tc.panelURL, got)
			}
		})
	}
}
