package agent

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	DefaultStateDir = "/var/lib/antimage"
	// DefaultPanelPort is used when panel_url names no port. It matches the
	// panel's default gRPC listener.
	DefaultPanelPort = "8443"
)

// Config is /etc/antimage/node.yaml, written by the bootstrap script.
type Config struct {
	// PanelURL is written by humans, so it is accepted in the form humans
	// write: "https://panel.example:8443". It is NOT a gRPC target; see
	// GRPCTarget.
	PanelURL string `yaml:"panel_url"`
	// Token is the single-use enrollment token. It is consumed on first run
	// and cleared from the file afterwards.
	Token string `yaml:"token"`
	// CAFingerprint pins the panel's CA. Without it a hijacked DNS record
	// could impersonate the panel, so a config lacking it is refused.
	CAFingerprint string `yaml:"ca_fingerprint"`
	StateDir      string `yaml:"state_dir"`
	NodeID        int64  `yaml:"node_id"`
}

func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.PanelURL == "" {
		return nil, errors.New("panel_url is required")
	}
	if cfg.CAFingerprint == "" {
		return nil, errors.New("ca_fingerprint is required: refusing to trust the system CA pool")
	}
	if cfg.StateDir == "" {
		cfg.StateDir = DefaultStateDir
	}
	// Resolve the dial target now so a malformed panel_url is a startup
	// failure with a readable message, rather than an Unavailable on every
	// RPC that the reconnect loop retries forever.
	if _, err := cfg.GRPCTarget(); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}

// Save rewrites the config, used to clear the consumed token and record the
// node id after enrollment.
func (c *Config) Save(path string) error {
	body, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// GRPCTarget converts PanelURL into a target grpc.NewClient actually accepts.
//
// A gRPC target is "host:port", or "scheme:///endpoint" for a registered
// resolver. "https://panel.example:8443" is neither. grpc.NewClient does not
// reject it: it constructs the client happily and then fails every RPC with
//
//	Unavailable: invalid target address https://panel.example:8443,
//	error info: address https://panel.example:8443:443: too many colons in address
//
// which is indistinguishable from a panel that is merely down, so the agent
// would reconnect-loop forever against a typo. Converting once, here, turns
// that into a single legible error at load time.
//
// A path, query, or fragment is refused rather than dropped: gRPC addresses a
// host, and silently discarding "/api" would connect somewhere the operator
// did not ask for.
func (c *Config) GRPCTarget() (string, error) {
	raw := strings.TrimSpace(c.PanelURL)
	if raw == "" {
		return "", errors.New("panel_url is required")
	}

	authority := raw
	if i := strings.Index(raw, "://"); i >= 0 {
		scheme := strings.ToLower(raw[:i])
		if scheme != "https" {
			return "", fmt.Errorf(
				"panel_url %q: scheme %q is not supported; the agent speaks gRPC over TLS, "+
					"so panel_url must be https:// or a bare host:port", raw, scheme)
		}
		authority = raw[i+len("://"):]
	} else if strings.Contains(raw, "//") {
		return "", fmt.Errorf("panel_url %q is neither an https:// URL nor a host:port", raw)
	}

	if authority == "" {
		return "", fmt.Errorf("panel_url %q has no host", raw)
	}
	if strings.ContainsAny(authority, "/?#") {
		return "", fmt.Errorf(
			"panel_url %q must not contain a path, query, or fragment: "+
				"gRPC dials a host, not a URL path", raw)
	}
	if strings.Contains(authority, "@") {
		return "", fmt.Errorf("panel_url %q must not contain userinfo", raw)
	}

	host, port := authority, ""
	switch {
	case strings.HasPrefix(authority, "["):
		// Bracketed IPv6 literal, optionally followed by ":port".
		end := strings.Index(authority, "]")
		if end < 0 {
			return "", fmt.Errorf("panel_url %q: unbalanced [ in IPv6 literal", raw)
		}
		host = authority[1:end]
		switch tail := authority[end+1:]; {
		case tail == "":
		case strings.HasPrefix(tail, ":"):
			port = tail[1:]
		default:
			return "", fmt.Errorf("panel_url %q: trailing %q after IPv6 literal", raw, tail)
		}
	case strings.Count(authority, ":") == 1:
		host, port, _ = strings.Cut(authority, ":")
	case strings.Count(authority, ":") > 1:
		// An unbracketed IPv6 literal. It cannot carry a port unambiguously,
		// so the default applies.
		host = authority
	}

	if host == "" {
		return "", fmt.Errorf("panel_url %q has no host", raw)
	}
	if port == "" {
		port = DefaultPanelPort
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("panel_url %q: %q is not a valid port", raw, port)
	}

	return net.JoinHostPort(host, port), nil
}
