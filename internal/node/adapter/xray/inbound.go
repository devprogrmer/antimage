// Package xray implements the adapter contract for Xray-core.
//
// It models one service as one Xray inbound, generates the inbound's config
// deterministically, and maps each kind of change onto the disruption level it
// genuinely costs. See docs/superpowers/specs/2026-08-18-sp2-design-decisions.md.
package xray

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
)

// Protocol is an Xray inbound protocol this adapter can generate.
type Protocol string

const (
	VLESS  Protocol = "vless"
	VMess  Protocol = "vmess"
	Trojan Protocol = "trojan"
)

// Network is the transport under the protocol.
type Network string

const (
	TCP Network = "tcp"
	WS  Network = "ws"
	// grpcNetwork is unexported: an exported GRPC would read confusingly
	// beside the grpc package at call sites.
	grpcNetwork Network = "grpc"
)

// Security is the TLS layer.
type Security string

const (
	SecurityNone    Security = "none"
	SecurityTLS     Security = "tls"
	SecurityReality Security = "reality"
)

// ErrInvalidInbound means the service params do not describe a usable inbound.
var ErrInvalidInbound = errors.New("invalid xray inbound")

// Inbound is the panel-facing shape of an Xray service. It is what an operator
// fills in, validated against the adapter's published JSON Schema before it is
// ever stored.
//
// Field names are the wire contract with the panel: renaming one changes every
// stored service's params and therefore every node's document hash.
type Inbound struct {
	Protocol Protocol `json:"protocol"`
	Port     int      `json:"port"`
	Listen   string   `json:"listen"`
	Network  Network  `json:"network"`
	Security Security `json:"security"`

	// TLS material, required when Security is tls.
	CertFile string `json:"cert_file"`
	KeyFile  string `json:"key_file"`
	SNI      string `json:"sni"`

	// Reality material, required when Security is reality.
	// Reality is an anti-censorship protocol that disguises traffic as
	// legitimate TLS to a real destination without requiring certificates.
	Dest        string   `json:"dest"`         // destination address (e.g., "example.com:443")
	ServerNames []string `json:"server_names"` // SNI values to accept
	PrivateKey  string   `json:"private_key"`  // x25519 private key (base64)
	ShortIDs    []string `json:"short_ids"`    // client authentication short IDs

	// Transport detail.
	Path string `json:"path"` // ws and grpc
	Host string `json:"host"` // ws Host header

	// Sniffing routes by destination domain rather than by the address the
	// client asked for, which is what makes per-domain routing possible later.
	Sniffing bool `json:"sniffing"`
}

// ParseInbound decodes and validates service params.
//
// Validation is strict and happens on the node as well as in the panel: the
// panel validates against the published schema before storing, but an adapter
// that trusted the document would generate a broken config from a hand-edited
// database and restart Xray into a crash loop.
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
		in.Listen = "0.0.0.0"
	}
	if in.Network == "" {
		in.Network = TCP
	}
	if in.Security == "" {
		in.Security = SecurityNone
	}
	return in
}

// Validate rejects an inbound that would produce a config Xray cannot start.
func (in Inbound) Validate() error {
	switch in.Protocol {
	case VLESS, VMess, Trojan:
	default:
		return fmt.Errorf("%w: protocol %q is not supported", ErrInvalidInbound, in.Protocol)
	}
	if in.Port < 1 || in.Port > 65535 {
		return fmt.Errorf("%w: port %d out of range", ErrInvalidInbound, in.Port)
	}
	if in.Listen != "" && net.ParseIP(in.Listen) == nil {
		return fmt.Errorf("%w: listen %q is not an IP address", ErrInvalidInbound, in.Listen)
	}
	switch in.Network {
	case "", TCP, WS, grpcNetwork:
	default:
		return fmt.Errorf("%w: network %q is not supported", ErrInvalidInbound, in.Network)
	}
	switch in.Security {
	case "", SecurityNone, SecurityTLS, SecurityReality:
	default:
		return fmt.Errorf("%w: security %q is not supported", ErrInvalidInbound, in.Security)
	}
	if in.Security == SecurityTLS {
		if in.CertFile == "" || in.KeyFile == "" {
			return fmt.Errorf("%w: tls requires cert_file and key_file", ErrInvalidInbound)
		}
	}
	if in.Security == SecurityReality {
		if in.Dest == "" {
			return fmt.Errorf("%w: reality requires dest", ErrInvalidInbound)
		}
		if len(in.ServerNames) == 0 {
			return fmt.Errorf("%w: reality requires server_names", ErrInvalidInbound)
		}
		if in.PrivateKey == "" {
			return fmt.Errorf("%w: reality requires private_key", ErrInvalidInbound)
		}
		if len(in.ShortIDs) == 0 {
			return fmt.Errorf("%w: reality requires short_ids", ErrInvalidInbound)
		}
	}
	// Trojan without TLS is trivially fingerprintable and is almost always a
	// misconfiguration rather than a choice.
	if in.Protocol == Trojan && in.Security != SecurityTLS {
		return fmt.Errorf("%w: trojan requires tls", ErrInvalidInbound)
	}
	if (in.Network == WS || in.Network == grpcNetwork) && in.Path == "" {
		return fmt.Errorf("%w: network %s requires a path", ErrInvalidInbound, in.Network)
	}
	return nil
}

// CredentialKind is the credential a subject needs for this protocol. It is
// what ties an inbound to the panel's credential model.
func (in Inbound) CredentialKind() string {
	if in.Protocol == Trojan {
		return "password"
	}
	return "uuid"
}

// User is one subject as it appears inside a generated inbound.
type User struct {
	SubjectID int64
	// Email is Xray's per-user tag. SP3 aggregates traffic by it, so it must
	// be stable across config regenerations and unique within an inbound.
	Email      string
	Credential string
}

// clientEntry is the generated per-user object. Field order in the struct is
// irrelevant to determinism because the whole config is canonicalised, but the
// omitempty choices are not: a field that appears or disappears based on the
// value changes the bytes and therefore the checksum an Observe compares.
type clientEntry struct {
	ID       string `json:"id,omitempty"`
	Password string `json:"password,omitempty"`
	Email    string `json:"email"`
	Flow     string `json:"flow,omitempty"`
}

// Generate renders the inbound and its users into Xray's inbound config shape.
//
// It is deterministic: users are sorted by subject id, and the output is
// produced by encoding/json with sorted map keys, so the same inputs always
// yield byte-identical output. The adapter's drift detection compares a
// checksum of this output against what is on disk, so any nondeterminism here
// would show up as permanent, uncorrectable drift.
func (in Inbound) Generate(users []User) ([]byte, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	in = in.withDefaults()

	sorted := make([]User, len(users))
	copy(sorted, users)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].SubjectID < sorted[j].SubjectID })

	clients := make([]clientEntry, 0, len(sorted))
	seen := make(map[string]struct{}, len(sorted))
	for _, u := range sorted {
		if u.Credential == "" {
			return nil, fmt.Errorf("%w: subject %d has no credential", ErrInvalidInbound, u.SubjectID)
		}
		// A duplicate email would make SP3's per-user accounting ambiguous and
		// Xray's behaviour on it is not defined; refuse rather than generate.
		if _, dup := seen[u.Email]; dup {
			return nil, fmt.Errorf("%w: duplicate user tag %q", ErrInvalidInbound, u.Email)
		}
		seen[u.Email] = struct{}{}

		entry := clientEntry{Email: u.Email}
		if in.Protocol == Trojan {
			entry.Password = u.Credential
		} else {
			entry.ID = u.Credential
		}
		// XTLS flow is valid on VLESS with TCP transport and TLS/Reality security
		if in.Protocol == VLESS && in.Network == TCP {
			switch in.Security {
			case SecurityTLS, SecurityReality:
				entry.Flow = "xtls-rprx-vision"
			}
		}
		clients = append(clients, entry)
	}

	settings := map[string]any{"clients": clients}
	if in.Protocol == VLESS {
		settings["decryption"] = "none"
	}

	stream := map[string]any{
		"network":  string(in.Network),
		"security": string(in.Security),
	}
	if in.Security == SecurityTLS {
		cert := map[string]any{
			"certificateFile": in.CertFile,
			"keyFile":         in.KeyFile,
		}
		tls := map[string]any{"certificates": []any{cert}}
		if in.SNI != "" {
			tls["serverName"] = in.SNI
		}
		stream["tlsSettings"] = tls
	}
	if in.Security == SecurityReality {
		reality := map[string]any{
			"show": false,
			"dest": in.Dest,
			"xver": 0,
		}
		if len(in.ServerNames) > 0 {
			reality["serverNames"] = in.ServerNames
		}
		if in.PrivateKey != "" {
			reality["privateKey"] = in.PrivateKey
		}
		if len(in.ShortIDs) > 0 {
			reality["shortIds"] = in.ShortIDs
		}
		stream["realitySettings"] = reality
	}
	switch in.Network {
	case WS:
		ws := map[string]any{"path": in.Path}
		if in.Host != "" {
			ws["headers"] = map[string]any{"Host": in.Host}
		}
		stream["wsSettings"] = ws
	case grpcNetwork:
		stream["grpcSettings"] = map[string]any{"serviceName": strings.TrimPrefix(in.Path, "/")}
	}

	inbound := map[string]any{
		"tag":            in.Tag(),
		"listen":         in.Listen,
		"port":           in.Port,
		"protocol":       string(in.Protocol),
		"settings":       settings,
		"streamSettings": stream,
	}
	if in.Sniffing {
		inbound["sniffing"] = map[string]any{
			"enabled":      true,
			"destOverride": []any{"http", "tls"},
		}
	}

	// Xray reads a confdir as a set of complete configuration documents and
	// merges them, appending array fields. A bare inbound object is not a
	// document: Xray parses it, finds no "inbounds" key, and starts with no
	// inbounds at all -- logging "Xray started" while binding nothing. That is
	// the worst available failure, because the process is up and healthy by
	// every check the node makes while no user can connect. The envelope is
	// what makes the file a document Xray will actually act on.
	doc := map[string]any{"inbounds": []any{inbound}}

	// MarshalIndent for an operator who has to read the file during an
	// incident. encoding/json sorts map keys, which is what makes this
	// deterministic.
	return json.MarshalIndent(doc, "", "  ")
}

// Tag is the inbound's stable identifier inside Xray. It is derived from the
// port because a node cannot have two inbounds on one port, so the tag is
// unique without depending on a database id the node does not have.
func (in Inbound) Tag() string {
	return fmt.Sprintf("antimage-%d", in.Port)
}
