// Package hysteria2 implements the adapter contract for Hysteria2 protocol.
//
// Hysteria2 is a UDP-based high-performance proxy protocol built on QUIC,
// designed for censorship circumvention with TLS 1.3 encryption and optional
// obfuscation. It excels in lossy/unreliable network conditions.
//
// Key features:
// - UDP-based (QUIC protocol)
// - Bandwidth configuration (up/down limits)
// - Authentication via password
// - TLS 1.3 with certificate management
// - Optional masquerade as HTTP/3
// - Salamander obfuscation support
//
// Design decisions:
// - Config written to /etc/hysteria2/antimage-{port}.yaml
// - Binary managed via systemd: hysteria-server@antimage-{port}
// - No hot reload - structural changes require restart
// - Traffic accounting via external hooks
//
// References:
// - https://hysteria.network/docs/getting-started/Server/
// - https://v2.hysteria.network/docs/Changelog/
package hysteria2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Kind is the adapter kind the panel stores in services.adapter_kind.
const Kind adapter.Kind = "hysteria2"

const (
	// configDir is where Hysteria2 config files live
	configDir = "/etc/hysteria2"
	// filePrefix namespaces our configs
	filePrefix = "antimage-"
	fileSuffix = ".yaml"
	// markerPrefix is a YAML comment carrying service ID and checksum
	markerPrefix = "# antimage:"
)

// ErrRuntimeUnavailable means hysteria2 binary is missing
var ErrRuntimeUnavailable = errors.New("hysteria2 runtime unavailable")

// Runtime is the adapter's contact with the Hysteria2 server binary
type Runtime interface {
	// Available checks if hysteria server binary exists
	Available(ctx context.Context) error
	// ServerStart starts a hysteria2 server instance
	ServerStart(ctx context.Context, configPath string) error
	// ServerStop stops a hysteria2 server instance
	ServerStop(ctx context.Context, configPath string) error
	// ServerRestart restarts a hysteria2 server instance
	ServerRestart(ctx context.Context, configPath string) error
	// ServerStatus checks if server is running
	ServerStatus(ctx context.Context, configPath string) (running bool, err error)
}

// Adapter implements the adapter contract for Hysteria2
type Adapter struct {
	rt Runtime
	// stateDir holds applied state tracking
	stateDir string
}

// New returns a Hysteria2 adapter
func New(rt Runtime, stateDir string) *Adapter {
	return &Adapter{rt: rt, stateDir: stateDir}
}

// serviceSchema is published to the panel for validation
var serviceSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["port", "password"],
  "properties": {
    "port": {
      "type": "integer",
      "minimum": 1,
      "maximum": 65535,
      "description": "UDP listen port"
    },
    "password": {
      "type": "string",
      "minLength": 8,
      "description": "Authentication password"
    },
    "cert_file": {
      "type": "string",
      "description": "Path to TLS certificate"
    },
    "key_file": {
      "type": "string",
      "description": "Path to TLS private key"
    },
    "sni": {
      "type": "string",
      "description": "Server Name Indication"
    },
    "obfs": {
      "type": "string",
      "enum": ["", "salamander"],
      "description": "Obfuscation type (empty or salamander)"
    },
    "obfs_password": {
      "type": "string",
      "description": "Obfuscation password (required if obfs is salamander)"
    },
    "up_mbps": {
      "type": "integer",
      "minimum": 1,
      "description": "Upload bandwidth limit in Mbps"
    },
    "down_mbps": {
      "type": "integer",
      "minimum": 1,
      "description": "Download bandwidth limit in Mbps"
    },
    "masquerade": {
      "type": "string",
      "description": "Masquerade URL to proxy non-hysteria traffic"
    },
    "ignore_client_bandwidth": {
      "type": "boolean",
      "description": "Ignore client bandwidth hints"
    }
  }
}`)

func (a *Adapter) Descriptor() adapter.Descriptor {
	return adapter.Descriptor{
		Kind:    Kind,
		Version: "1",
		Caps: adapter.Caps{
			// Hysteria2 requires full restart for config changes
			HotUserAdd: false,
			// External accounting via traffic hooks
			SelfAccounting: false,
			// Requires TLS certificates
			RequiresPKI: true,
			// Password-based authentication
			CredentialKinds: []adapter.CredentialKind{adapter.CredPassword},
			ServiceSchema:   serviceSchema,
		},
	}
}

// configPath returns filesystem path for service config
func (a *Adapter) configPath(serviceID int64) string {
	return filepath.Join(configDir, fmt.Sprintf("%s%d%s", filePrefix, serviceID, fileSuffix))
}

// appliedPath returns path to applied state sidecar
func (a *Adapter) appliedPath(serviceID int64) string {
	return filepath.Join(a.stateDir, fmt.Sprintf("hysteria2-applied-%d.json", serviceID))
}

// appliedState records what config the server is actually running
type appliedState struct {
	Checksum string   `json:"checksum"`
	Users    []string `json:"users"` // sorted usernames
}

// recordApplied saves applied state after successful server start

// applied reads last known applied state
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

// parseMarker extracts service ID and checksum from config comment
func parseMarker(line string) (serviceID int64, checksum string, ok bool) {
	if !strings.HasPrefix(line, markerPrefix) {
		return 0, "", false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(line, markerPrefix))
	// Format: "service=123 checksum=abc..."
	var svcID int64
	var cksum string
	_, err := fmt.Sscanf(rest, "service=%d checksum=%s", &svcID, &cksum)
	if err != nil {
		return 0, "", false
	}
	return svcID, cksum, true
}

// checksumContent computes SHA-256 of config body
func checksumContent(body []byte) string {
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

// Observe, Plan, Apply, Probe implementations in separate files
