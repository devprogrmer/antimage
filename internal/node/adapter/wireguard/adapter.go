// Package wireguard implements the adapter contract for WireGuard VPN.
//
// WireGuard is a modern, high-performance VPN protocol that operates at the
// kernel level (or userspace via wireguard-go). This adapter manages WireGuard
// interfaces through wg-quick and systemd, generating configuration files and
// tracking peer state.
//
// Design decisions:
// - Uses wg-quick for interface management (handles routing/iptables automatically)
// - Config files in /etc/wireguard/antimage-{port}.conf
// - systemd service per interface: wg-quick@antimage-{port}
// - Traffic accounting via `wg show {interface} transfer`
// - Peers identified by public key (deterministic from private key)
package wireguard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Kind is the adapter kind the panel stores in services.adapter_kind.
const Kind adapter.Kind = "wireguard"

const (
	// configDir is where WireGuard expects configuration files.
	configDir = "/etc/wireguard"
	// filePrefix namespaces our configs to avoid conflicts with manually
	// managed interfaces.
	filePrefix = "antimage-"
	fileSuffix = ".conf"
	// markerPrefix begins a comment line in every config we write, carrying
	// the service ID and content checksum for drift detection.
	markerPrefix = "# antimage:"
	// appliedSuffix names the sidecar recording the checksum the interface was
	// last successfully brought up with. Written only after wg-quick up succeeds.
	appliedSuffix = ".applied"
)

// ErrRuntimeUnavailable means wg-quick or the kernel module is missing.
var ErrRuntimeUnavailable = errors.New("wireguard runtime unavailable")

// Runtime is the adapter's contact with WireGuard tooling.
type Runtime interface {
	// Available checks if wg-quick and wg are in PATH.
	Available(ctx context.Context) error
	// InterfaceUp brings up an interface via wg-quick.
	InterfaceUp(ctx context.Context, iface string) error
	// InterfaceDown tears down an interface via wg-quick.
	InterfaceDown(ctx context.Context, iface string) error
	// InterfaceStatus checks if an interface exists and is up.
	InterfaceStatus(ctx context.Context, iface string) (exists, up bool, err error)
	// ShowTransfer returns per-peer RX/TX bytes for an interface.
	ShowTransfer(ctx context.Context, iface string) (map[string]PeerTransfer, error)
	// SyncPeers applies peer config changes without restarting the interface.
	// Returns true if hot-sync succeeded, false if a restart is required.
	SyncPeers(ctx context.Context, iface, configPath string) (bool, error)
}

// PeerTransfer holds traffic statistics for a single peer.
type PeerTransfer struct {
	PublicKey string
	RxBytes   uint64
	TxBytes   uint64
}

// Adapter implements the adapter contract for WireGuard.
type Adapter struct {
	rt Runtime
	// stateDir is where we persist accounting cursors and applied state.
	stateDir string
}

// New returns an adapter managing WireGuard interfaces through rt.
func New(rt Runtime, stateDir string) *Adapter {
	return &Adapter{rt: rt, stateDir: stateDir}
}

// serviceSchema is published to the panel, which validates operator input
// against it before a service is stored.
var serviceSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["port", "subnet", "private_key"],
  "properties": {
    "port": {
      "type": "integer",
      "minimum": 1,
      "maximum": 65535,
      "description": "UDP listen port"
    },
    "subnet": {
      "type": "string",
      "pattern": "^[0-9.]+/[0-9]{1,2}$",
      "description": "Interface subnet in CIDR notation (e.g., 10.8.0.1/24)"
    },
    "private_key": {
      "type": "string",
      "minLength": 44,
      "maxLength": 44,
      "description": "Base64-encoded WireGuard private key"
    },
    "dns": {
      "type": "array",
      "items": {"type": "string"},
      "description": "DNS servers pushed to clients"
    },
    "mtu": {
      "type": "integer",
      "minimum": 1280,
      "maximum": 9000,
      "description": "Interface MTU (default: 1420)"
    },
    "keepalive": {
      "type": "integer",
      "minimum": 0,
      "maximum": 300,
      "description": "Persistent keepalive seconds (0 = disabled)"
    }
  }
}`)

func (a *Adapter) Descriptor() adapter.Descriptor {
	return adapter.Descriptor{
		Kind:    Kind,
		Version: "1",
		Caps: adapter.Caps{
			// WireGuard supports hot peer add/remove via `wg set` without
			// restarting the interface. Only structural changes (listen port,
			// private key, subnet) require a restart.
			HotUserAdd: true,
			// WireGuard has no built-in accounting API; we parse `wg show`.
			SelfAccounting: false,
			RequiresPKI:    false,
			// WireGuard peers are identified by their public key, which is
			// derived from a private key. The panel stores the private key
			// sealed, we derive the public key when generating configs.
			CredentialKinds: []adapter.CredentialKind{adapter.CredKeypair},
			ServiceSchema:   serviceSchema,
		},
	}
}

// configPath returns the filesystem path for a service's WireGuard config.
func (a *Adapter) configPath(serviceID int64) string {
	return filepath.Join(configDir, fmt.Sprintf("%s%d%s", filePrefix, serviceID, fileSuffix))
}

// appliedPath returns the path to the sidecar tracking what the interface is
// actually running.
func (a *Adapter) appliedPath(serviceID int64) string {
	return filepath.Join(a.stateDir, fmt.Sprintf("wg-applied-%d.json", serviceID))
}

// interfaceName returns the interface name for a service.
func interfaceName(serviceID int64) string {
	return fmt.Sprintf("%s%d", filePrefix, serviceID)
}

// appliedState records what configuration the WireGuard interface is currently
// running. The config file on disk says what should be running; this says what is.
type appliedState struct {
	Checksum string   `json:"checksum"`
	Peers    []string `json:"peers"` // sorted public keys
}

// recordApplied notes what the interface is now serving. Called only after
// wg-quick up or wg syncpeers succeeds.
func (a *Adapter) recordApplied(serviceID int64, checksum string, peers []string) error {
	sorted := append([]string(nil), peers...)
	sort.Strings(sorted)
	body, err := json.Marshal(appliedState{Checksum: checksum, Peers: sorted})
	if err != nil {
		return fmt.Errorf("encode applied state: %w", err)
	}
	path := a.appliedPath(serviceID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	return os.WriteFile(path, body, 0o600)
}

// applied reads what the interface was last successfully configured with.
// A zero value means "unknown", which forces a full restart.
func (a *Adapter) applied(serviceID int64) appliedState {
	body, err := os.ReadFile(a.appliedPath(serviceID))
	if err != nil {
		return appliedState{}
	}
	var st appliedState
	if err := json.Unmarshal(body, &st); err != nil {
		return appliedState{}
	}
	return st
}

// removedPeers reports which peers the interface is currently serving that
// the desired state no longer includes.
func removedPeers(applied, desired []string) []string {
	dm := make(map[string]bool, len(desired))
	for _, pk := range desired {
		dm[pk] = true
	}
	var removed []string
	for _, pk := range applied {
		if !dm[pk] {
			removed = append(removed, pk)
		}
	}
	return removed
}

// parseMarker extracts service ID and checksum from a marker comment line.
func parseMarker(line string) (serviceID int64, checksum string, ok bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, markerPrefix) {
		return 0, "", false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(line, markerPrefix))
	// Format: "service=123 checksum=abcd..."
	var svcID int64
	var cksum string
	_, err := fmt.Sscanf(rest, "service=%d checksum=%s", &svcID, &cksum)
	if err != nil {
		return 0, "", false
	}
	return svcID, cksum, true
}

// checksumContent computes SHA-256 of config body (everything after the marker).
func checksumContent(body []byte) string {
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

// Observe, Plan, Apply, Probe, and accounting implementations are in separate files.
