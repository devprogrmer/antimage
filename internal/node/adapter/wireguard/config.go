package wireguard

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"sort"
	"strings"
	"text/template"
)

// ServiceParams are the operator-supplied settings for a WireGuard service.
type ServiceParams struct {
	Port       int      `json:"port"`
	Subnet     string   `json:"subnet"`
	PrivateKey string   `json:"private_key"`
	DNS        []string `json:"dns,omitempty"`
	MTU        int      `json:"mtu,omitempty"`
	Keepalive  int      `json:"keepalive,omitempty"`
}

// Validate checks service params are well-formed.
func (p ServiceParams) Validate() error {
	if p.Port < 1 || p.Port > 65535 {
		return fmt.Errorf("port must be 1-65535, got %d", p.Port)
	}
	if _, _, err := net.ParseCIDR(p.Subnet); err != nil {
		return fmt.Errorf("invalid subnet %q: %w", p.Subnet, err)
	}
	if len(p.PrivateKey) != 44 {
		return fmt.Errorf("private key must be 44 base64 characters")
	}
	// Verify it's valid base64
	if _, err := base64.StdEncoding.DecodeString(p.PrivateKey); err != nil {
		return fmt.Errorf("private key must be valid base64: %w", err)
	}
	if p.MTU != 0 && (p.MTU < 1280 || p.MTU > 9000) {
		return fmt.Errorf("mtu must be 1280-9000 or 0 (default)")
	}
	if p.Keepalive < 0 || p.Keepalive > 300 {
		return fmt.Errorf("keepalive must be 0-300 seconds")
	}
	return nil
}

// PublicKey derives the WireGuard public key from the private key.
// Note: This requires golang.org/x/crypto/curve25519 which may not be available.
// For production, use `wg pubkey` command or the wireguard-go library.
func (p ServiceParams) PublicKey() (string, error) {
	// Simplified implementation - in production, shell out to `wg pubkey`
	// or use proper WireGuard key derivation library
	privBytes, err := base64.StdEncoding.DecodeString(p.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("decode private key: %w", err)
	}
	if len(privBytes) != 32 {
		return "", fmt.Errorf("private key must be 32 bytes, got %d", len(privBytes))
	}
	// For now, just return a placeholder - real implementation would use curve25519
	return "", fmt.Errorf("public key derivation not implemented - use wg command")
}

// PeerConfig describes a single WireGuard peer (client).
type PeerConfig struct {
	PublicKey  string
	AllowedIPs string // CIDR, typically a /32 for this peer's IP
	Keepalive  int
}

// GenerateConfig renders a wg-quick configuration file.
func GenerateConfig(serviceID int64, params ServiceParams, peers []PeerConfig) (string, error) {
	if err := params.Validate(); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}

	// Sort peers by public key for deterministic output
	sorted := append([]PeerConfig(nil), peers...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].PublicKey < sorted[j].PublicKey
	})

	mtu := params.MTU
	if mtu == 0 {
		mtu = 1420 // WireGuard default
	}

	data := struct {
		ServiceID  int64
		PrivateKey string
		Port       int
		Subnet     string
		DNS        []string
		MTU        int
		Peers      []PeerConfig
	}{
		ServiceID:  serviceID,
		PrivateKey: params.PrivateKey,
		Port:       params.Port,
		Subnet:     params.Subnet,
		DNS:        params.DNS,
		MTU:        mtu,
		Peers:      sorted,
	}

	var buf bytes.Buffer
	if err := configTemplate.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render template: %w", err)
	}

	body := buf.String()
	checksum := checksumConfigBody(body)

	// Prepend marker comment for drift detection
	marker := fmt.Sprintf("%s service=%d checksum=%s\n", markerPrefix, serviceID, checksum)
	return marker + body, nil
}

var configTemplate = template.Must(template.New("wg").Parse(`# WireGuard configuration managed by antimage
# Service ID: {{.ServiceID}}
# DO NOT EDIT - changes will be detected as drift and overwritten

[Interface]
PrivateKey = {{.PrivateKey}}
Address = {{.Subnet}}
ListenPort = {{.Port}}
MTU = {{.MTU}}
{{- if .DNS}}
DNS = {{join .DNS ", "}}
{{- end}}

{{- range .Peers}}

[Peer]
PublicKey = {{.PublicKey}}
AllowedIPs = {{.AllowedIPs}}
{{- if .Keepalive}}
PersistentKeepalive = {{.Keepalive}}
{{- end}}
{{- end}}
`))

func init() {
	configTemplate.Funcs(template.FuncMap{
		"join": strings.Join,
	})
}

// checksumConfigBody computes SHA-256 of the config body (without the marker line).
func checksumConfigBody(body string) string {
	h := sha256.Sum256([]byte(body))
	return hex.EncodeToString(h[:])
}

// AllocatePeerIP allocates a unique IP within the service subnet for a peer.
// Uses subject ID as the offset to ensure deterministic allocation.
func AllocatePeerIP(subnet string, subjectID int64) (string, error) {
	_, ipnet, err := net.ParseCIDR(subnet)
	if err != nil {
		return "", fmt.Errorf("parse subnet: %w", err)
	}

	// Extract base IP and calculate host portion
	baseIP := ipnet.IP.To4()
	if baseIP == nil {
		return "", fmt.Errorf("only IPv4 subnets supported")
	}

	// Convert subnet mask to host bits available
	ones, bits := ipnet.Mask.Size()
	hostBits := bits - ones
	maxHosts := (1 << hostBits) - 2 // -2 for network and broadcast

	// Use subjectID as offset (skip .0 network address, start at .2)
	// .1 is typically the gateway/server
	offset := int(subjectID % int64(maxHosts))
	if offset == 0 {
		offset = 2 // Start at .2 if subjectID maps to .0
	} else {
		offset++ // Shift everything by 1 to skip .1
	}

	// Apply offset to base IP
	ip := make(net.IP, 4)
	copy(ip, baseIP)

	// Add offset to the last octet(s)
	carry := offset
	for i := 3; i >= 0 && carry > 0; i-- {
		sum := int(ip[i]) + carry
		ip[i] = byte(sum & 0xFF)
		carry = sum >> 8
	}

	// Verify result is within subnet
	if !ipnet.Contains(ip) {
		return "", fmt.Errorf("allocated IP %s outside subnet %s", ip, subnet)
	}

	return fmt.Sprintf("%s/32", ip.String()), nil
}

// PublicKeyFromPrivate is a convenience wrapper for deriving public keys.
func PublicKeyFromPrivate(privateKey string) (string, error) {
	p := ServiceParams{PrivateKey: privateKey, Port: 51820, Subnet: "10.0.0.1/24"}
	return p.PublicKey()
}
