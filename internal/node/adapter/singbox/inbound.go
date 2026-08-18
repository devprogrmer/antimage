// Package singbox implements the adapter contract for sing-box.
//
// It differs from the Xray adapter in one operationally important way: sing-box
// exposes no stable management API for mutating users on a running instance, so
// every user change is a config rewrite and a restart. The adapter declares
// Caps.HotUserAdd=false and classifies user changes as DisruptRestart, which is
// what lets the panel warn an operator before they drop every session on the
// node. Claiming otherwise would report convergence while the running process
// still served the old user set.
package singbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
)

// Protocol is a sing-box inbound type this adapter can generate.
type Protocol string

const (
	VLESS       Protocol = "vless"
	VMess       Protocol = "vmess"
	Trojan      Protocol = "trojan"
	Shadowsocks Protocol = "shadowsocks"
)

// Network is the transport under the protocol.
type Network string

const (
	TCP Network = "tcp"
	WS  Network = "ws"
)

// ErrInvalidInbound means the service params do not describe a usable inbound.
var ErrInvalidInbound = errors.New("invalid sing-box inbound")

// defaultShadowsocksMethod is the AEAD cipher used when none is given. 2022
// ciphers require a server-side PSK that the panel does not model yet, so the
// widely supported AEAD one is the honest default.
const defaultShadowsocksMethod = "aes-256-gcm"

// Inbound is the panel-facing shape of a sing-box service.
type Inbound struct {
	Protocol Protocol `json:"protocol"`
	Port     int      `json:"port"`
	Listen   string   `json:"listen"`
	Network  Network  `json:"network"`

	// TLS. sing-box requires a certificate pair when tls is enabled.
	TLS      bool   `json:"tls"`
	CertFile string `json:"cert_file"`
	KeyFile  string `json:"key_file"`
	SNI      string `json:"sni"`

	Path string `json:"path"` // ws
	Host string `json:"host"` // ws Host header

	// Method is the Shadowsocks cipher. Ignored by other protocols.
	Method string `json:"method"`

	Sniff bool `json:"sniff"`
}

// ParseInbound decodes and validates service params.
//
// Unknown fields are rejected: the node validates independently of the panel,
// so a hand-edited database cannot restart sing-box into a crash loop.
func ParseInbound(raw json.RawMessage) (Inbound, error) {
	var in Inbound
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return Inbound{}, fmt.Errorf("%w: %w", ErrInvalidInbound, err)
	}
	if err := in.Validate(); err != nil {
		return Inbound{}, err
	}
	return in.withDefaults(), nil
}

func (in Inbound) withDefaults() Inbound {
	if in.Listen == "" {
		in.Listen = "::"
	}
	if in.Network == "" {
		in.Network = TCP
	}
	if in.Protocol == Shadowsocks && in.Method == "" {
		in.Method = defaultShadowsocksMethod
	}
	return in
}

// Validate rejects an inbound sing-box could not start.
func (in Inbound) Validate() error {
	switch in.Protocol {
	case VLESS, VMess, Trojan, Shadowsocks:
	default:
		return fmt.Errorf("%w: protocol %q is not supported", ErrInvalidInbound, in.Protocol)
	}
	if in.Port < 1 || in.Port > 65535 {
		return fmt.Errorf("%w: port %d out of range", ErrInvalidInbound, in.Port)
	}
	// "::" and "0.0.0.0" are the wildcard forms sing-box accepts.
	if in.Listen != "" && in.Listen != "::" && net.ParseIP(in.Listen) == nil {
		return fmt.Errorf("%w: listen %q is not an IP address", ErrInvalidInbound, in.Listen)
	}
	switch in.Network {
	case "", TCP, WS:
	default:
		return fmt.Errorf("%w: network %q is not supported", ErrInvalidInbound, in.Network)
	}
	if in.TLS && (in.CertFile == "" || in.KeyFile == "") {
		return fmt.Errorf("%w: tls requires cert_file and key_file", ErrInvalidInbound)
	}
	if in.Protocol == Trojan && !in.TLS {
		return fmt.Errorf("%w: trojan requires tls", ErrInvalidInbound)
	}
	if in.Network == WS && in.Path == "" {
		return fmt.Errorf("%w: network ws requires a path", ErrInvalidInbound)
	}
	// Shadowsocks in sing-box carries one server-wide method; an unknown one
	// fails at startup rather than at config load, which is the worst time.
	if in.Protocol == Shadowsocks && in.Method != "" {
		switch in.Method {
		case "aes-128-gcm", "aes-256-gcm", "chacha20-ietf-poly1305":
		default:
			return fmt.Errorf("%w: shadowsocks method %q is not supported", ErrInvalidInbound, in.Method)
		}
	}
	return nil
}

// CredentialKind is the credential a subject needs for this protocol.
func (in Inbound) CredentialKind() string {
	switch in.Protocol {
	case Trojan, Shadowsocks:
		return "password"
	default:
		return "uuid"
	}
}

// User is one subject inside a generated inbound.
type User struct {
	SubjectID  int64
	Name       string
	Credential string
}

// Tag identifies the inbound inside sing-box and in SP3's accounting.
func (in Inbound) Tag() string {
	return fmt.Sprintf("antimage-%d", in.Port)
}

// Generate renders the inbound deterministically.
//
// Users are sorted by subject id and the output goes through encoding/json,
// which sorts map keys, so the same inputs always yield byte-identical bytes.
// The adapter compares a checksum of this against what is on disk, so any
// nondeterminism would present as permanent, uncorrectable drift.
func (in Inbound) Generate(users []User) ([]byte, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	in = in.withDefaults()

	sorted := make([]User, len(users))
	copy(sorted, users)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].SubjectID < sorted[j].SubjectID })

	entries := make([]map[string]any, 0, len(sorted))
	seen := make(map[string]struct{}, len(sorted))
	for _, u := range sorted {
		if u.Credential == "" {
			return nil, fmt.Errorf("%w: subject %d has no credential", ErrInvalidInbound, u.SubjectID)
		}
		if _, dup := seen[u.Name]; dup {
			return nil, fmt.Errorf("%w: duplicate user name %q", ErrInvalidInbound, u.Name)
		}
		seen[u.Name] = struct{}{}

		entry := map[string]any{"name": u.Name}
		switch in.Protocol {
		case VLESS, VMess:
			entry["uuid"] = u.Credential
		case Trojan, Shadowsocks:
			entry["password"] = u.Credential
		}
		entries = append(entries, entry)
	}

	inbound := map[string]any{
		"type":        string(in.Protocol),
		"tag":         in.Tag(),
		"listen":      in.Listen,
		"listen_port": in.Port,
		"users":       entries,
	}
	if in.Protocol == Shadowsocks {
		inbound["method"] = in.Method
	}
	if in.Sniff {
		inbound["sniff"] = true
	}
	if in.TLS {
		tls := map[string]any{
			"enabled":          true,
			"certificate_path": in.CertFile,
			"key_path":         in.KeyFile,
		}
		if in.SNI != "" {
			tls["server_name"] = in.SNI
		}
		inbound["tls"] = tls
	}
	if in.Network == WS {
		ws := map[string]any{"type": "ws", "path": in.Path}
		if in.Host != "" {
			ws["headers"] = map[string]any{"Host": in.Host}
		}
		inbound["transport"] = ws
	}

	return json.MarshalIndent(inbound, "", "  ")
}
