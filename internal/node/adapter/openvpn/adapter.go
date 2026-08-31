// Package openvpn implements the adapter contract for OpenVPN.
//
// AUTHENTICATION IS USERNAME/PASSWORD, NOT CLIENT CERTIFICATES, and that is a
// decision rather than a shortcut. OpenVPN's most common deployment issues an
// X.509 certificate per client, and adapter.CredX509 exists in the contract for
// it -- but the panel has never minted a client certificate for a subject, and
// nothing in it can. An adapter declaring CredX509 would advertise a credential
// kind no subject in this system has ever held, which is the "do not offer what
// the node cannot execute" rule pointed at the credential model instead of the
// UI. Subjects have passwords, so the adapter uses passwords.
//
// The adapter manages three files, all deterministic and all checksummed:
// server.conf, a verify script OpenVPN calls on each login, and a user file
// holding salted hashes. Unlike ocserv, nothing here is written by a third-party
// tool with a random salt, so drift detection is exact on all three.
package openvpn

import (
	"context"
	"encoding/json"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Kind is the adapter kind the panel stores in services.adapter_kind.
const Kind adapter.Kind = "openvpn"

// DefaultConfigDir is where the distribution packages put OpenVPN's config.
const DefaultConfigDir = "/etc/openvpn/server"

// Runtime is everything the adapter needs from the host.
type Runtime interface {
	// Available reports whether openvpn is installed and usable.
	Available(ctx context.Context) error
	// Start, Stop and Restart drive the service unit.
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Restart(ctx context.Context) error
	// Active reports whether the unit is running.
	Active(ctx context.Context) bool
	// ReadStatus returns the contents of OpenVPN's status file, which is where
	// per-client byte counters live.
	ReadStatus(ctx context.Context, path string) (string, error)
}

// Adapter implements the adapter contract for OpenVPN.
type Adapter struct {
	rt Runtime
	// dir holds server.conf, the verify script and the user file.
	dir string
	// stateDir persists the accounting cursor between agent restarts.
	stateDir string
}

func New(rt Runtime, dir, stateDir string) *Adapter {
	return &Adapter{rt: rt, dir: dir, stateDir: stateDir}
}

// serviceSchema is published to the panel, which validates operator input
// against it and renders the Studio form from it.
//
// Every field is one this adapter writes into server.conf. OpenVPN has
// hundreds of directives; publishing ones the adapter ignores would let an
// operator configure something that silently does nothing.
var serviceSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["port", "proto", "ca", "server_cert", "server_key", "dh", "subnet", "netmask"],
  "properties": {
    "port": {
      "type": "integer",
      "minimum": 1,
      "maximum": 65535,
      "description": "Port to listen on"
    },
    "proto": {
      "type": "string",
      "enum": ["udp", "tcp"],
      "description": "UDP performs better; TCP survives restrictive networks"
    },
    "ca": {
      "type": "string",
      "minLength": 1,
      "description": "Path to the CA certificate in PEM form"
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
    "dh": {
      "type": "string",
      "minLength": 1,
      "description": "Path to the Diffie-Hellman parameters, or the word none for ECDH"
    },
    "subnet": {
      "type": "string",
      "format": "ipv4",
      "description": "Client address pool network, e.g. 10.8.0.0"
    },
    "netmask": {
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
      "description": "Routes pushed to clients; omit to push a default route"
    },
    "cipher": {
      "type": "string",
      "description": "Data channel cipher, e.g. AES-256-GCM"
    },
    "max_clients": {
      "type": "integer",
      "minimum": 1,
      "description": "Total concurrent clients"
    },
    "duplicate_cn": {
      "type": "boolean",
      "description": "Allow one account several concurrent connections"
    }
  }
}`)

func (a *Adapter) Descriptor() adapter.Descriptor {
	return adapter.Descriptor{
		Kind:    Kind,
		Version: "1",
		Caps: adapter.Caps{
			// The verify script reads the user file on every login attempt, so
			// an account added now works on the next connection. No reload and
			// no restart, and nothing established is disturbed.
			HotUserAdd: true,
			// OpenVPN writes per-client byte counters to its status file, and
			// the adapter turns them into deltas. Declaring this true obliges
			// the type to implement adapter.UsageReporter, which
			// TestSelfAccountingMatchesTheImplementation enforces.
			SelfAccounting: true,
			// The SERVER needs a certificate, and the operator supplies its
			// path like any other OpenVPN deployment. RequiresPKI is about the
			// panel minting per-CLIENT certificates, which it cannot do -- see
			// the package comment.
			RequiresPKI:     false,
			CredentialKinds: []adapter.CredentialKind{adapter.CredPassword},
			ServiceSchema:   serviceSchema,
			// OpenVPN terminates tunnels; it does not select an upstream path.
			SupportsOutbounds: false,
			SupportsRouting:   false,
		},
	}
}
