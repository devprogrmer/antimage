// Package ocserv implements the adapter contract for OpenConnect Server.
//
// ocserv speaks the AnyConnect protocol, which matters commercially rather
// than technically: it is the one protocol in this panel that every major
// desktop and mobile platform can already connect to with a stock client, and
// it is the protocol most commonly reached for where DPI blocks the others.
// It was the widest gap between Antimage and the panels it competes with.
//
// The adapter manages two files. ocserv.conf is rendered here and fully
// checksummed, so a hand edit is drift. The user database is written through
// ocserv's own ocpasswd tool, because its entries are salted crypt hashes that
// cannot be reproduced byte-for-byte -- see observe.go for how drift is still
// detected on it.
package ocserv

import (
	"context"
	"encoding/json"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Kind is the adapter kind the panel stores in services.adapter_kind.
const Kind adapter.Kind = "ocserv"

// DefaultConfigDir is where the distribution packages put ocserv's config.
const DefaultConfigDir = "/etc/ocserv"

// Runtime is everything the adapter needs from the host.
//
// Injected rather than called directly so the unit tests can drive a fake:
// Apply writes real files and runs real commands, and a test that shelled out
// to systemctl would restart the developer's own services.
type Runtime interface {
	// Available reports whether ocserv is installed and usable. The
	// realruntime build tag turns a missing binary into a failure rather than
	// a skip, so this is what that job asserts against.
	Available(ctx context.Context) error
	// Start, Stop and Restart drive the service unit.
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Restart(ctx context.Context) error
	// Reload re-reads ocserv.conf without dropping established sessions.
	Reload(ctx context.Context) error
	// Active reports whether the unit is running.
	Active(ctx context.Context) bool
	// SetPassword creates or replaces one user in the passwd file. ocserv
	// re-reads that file per connection, which is what makes HotUserAdd true.
	SetPassword(ctx context.Context, passwdFile, username, password string) error
	// DeletePassword removes one user from the passwd file.
	DeletePassword(ctx context.Context, passwdFile, username string) error
	// ShowUsers returns occtl's view of connected users, for accounting.
	ShowUsers(ctx context.Context) ([]OcctlUser, error)
}

// Adapter implements the adapter contract for ocserv.
type Adapter struct {
	rt Runtime
	// dir holds ocserv.conf and the passwd file. Taken at construction so a
	// test can point at a temp directory instead of /etc/ocserv.
	dir string
	// stateDir persists the accounting cursor between agent restarts.
	stateDir string
}

// New returns an adapter writing config to dir and state to stateDir.
func New(rt Runtime, dir, stateDir string) *Adapter {
	return &Adapter{rt: rt, dir: dir, stateDir: stateDir}
}

// serviceSchema is published to the panel, which validates operator input
// against it before a service is stored and renders the Studio form from it.
//
// Deliberately narrow. Every field here is one this adapter actually writes
// into ocserv.conf; ocserv has dozens more, and publishing options the adapter
// ignores would let an operator configure something that silently does
// nothing. Additions belong here and in renderConf together.
var serviceSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["port", "server_cert", "server_key", "ipv4_network", "ipv4_netmask"],
  "properties": {
    "port": {
      "type": "integer",
      "minimum": 1,
      "maximum": 65535,
      "description": "TCP and UDP port to listen on (443 blends with HTTPS)"
    },
    "server_cert": {
      "type": "string",
      "minLength": 1,
      "description": "Path to the server certificate in PEM form"
    },
    "server_key": {
      "type": "string",
      "minLength": 1,
      "description": "Path to the server private key in PEM form"
    },
    "ipv4_network": {
      "type": "string",
      "format": "ipv4",
      "description": "Client address pool network, e.g. 192.168.220.0"
    },
    "ipv4_netmask": {
      "type": "string",
      "format": "ipv4",
      "description": "Netmask for the client pool, e.g. 255.255.255.0"
    },
    "dns": {
      "type": "array",
      "items": {"type": "string", "format": "ipv4"},
      "description": "DNS servers pushed to clients"
    },
    "routes": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Routes pushed to clients; omit for a default route"
    },
    "max_clients": {
      "type": "integer",
      "minimum": 0,
      "description": "Total concurrent clients; 0 means unlimited"
    },
    "max_same_clients": {
      "type": "integer",
      "minimum": 0,
      "description": "Concurrent sessions per user; 0 means unlimited"
    },
    "udp_enabled": {
      "type": "boolean",
      "description": "Offer DTLS over UDP as well as TLS over TCP"
    },
    "tunnel_all_dns": {
      "type": "boolean",
      "description": "Force client DNS through the tunnel"
    }
  }
}`)

func (a *Adapter) Descriptor() adapter.Descriptor {
	return adapter.Descriptor{
		Kind:    Kind,
		Version: "1",
		Caps: adapter.Caps{
			// ocserv consults the passwd file on each connection attempt, so a
			// user added now can connect immediately. No reload, no restart,
			// and no established session disturbed.
			HotUserAdd: true,
			// occtl reports per-session byte counters, and the adapter turns
			// them into deltas -- see accounting.go. Declaring this true
			// obliges the type to implement adapter.UsageReporter, which
			// TestSelfAccountingMatchesTheImplementation enforces.
			SelfAccounting: true,
			// The server needs a certificate, but the panel does not issue it:
			// server_cert and server_key are paths the operator supplies, the
			// same way ocserv's own packaging expects. RequiresPKI is about the
			// panel minting per-CLIENT certificates, which this does not do --
			// clients authenticate with a username and password.
			RequiresPKI:     false,
			CredentialKinds: []adapter.CredentialKind{adapter.CredPassword},
			ServiceSchema:   serviceSchema,
			// No egress. ocserv terminates tunnels; it does not select an
			// upstream path, so declaring outbound support would put controls
			// in the UI that this adapter cannot honour.
			SupportsOutbounds: false,
			SupportsRouting:   false,
		},
	}
}
