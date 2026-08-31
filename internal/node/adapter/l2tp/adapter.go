// Package l2tp implements the adapter contract for L2TP/IPsec VPN.
//
// This adapter manages strongSwan (IPsec layer) and xl2tpd (L2TP layer),
// writing configuration files for both services plus PPP CHAP secrets. Traffic
// accounting uses external nftables counters rather than self-reporting, since
// L2TP/IPsec has no built-in accounting API.
//
// Design decisions are recorded in
// docs/superpowers/specs/2026-08-20-sp6-l2tp-ipsec-adapter.md.
package l2tp

import (
	"encoding/json"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Kind is the adapter kind the panel stores in services.adapter_kind.
const Kind adapter.Kind = "l2tp"

// Adapter implements the adapter contract for L2TP/IPsec.
type Adapter struct {
	// confDir is the root for config files (typically /etc).
	confDir string
	// stateDir is where we persist accounting cursors.
	stateDir string
}

// New returns an adapter writing configs to confDir and state to stateDir.
func New(confDir, stateDir string) *Adapter {
	return &Adapter{
		confDir:  confDir,
		stateDir: stateDir,
	}
}

// serviceSchema is published to the panel, which validates operator input
// against it before a service is stored. Keeping it here means adding L2TP
// is an adapter change, not a panel change.
var serviceSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["ip_range", "local_ip", "psk"],
  "properties": {
    "ip_range": {
      "type": "string",
      "pattern": "^[0-9.]+\\-[0-9.]+$",
      "description": "Client IP range (e.g., 10.8.0.2-10.8.0.254)"
    },
    "local_ip": {
      "type": "string",
      "format": "ipv4",
      "description": "Server local IP for L2TP (e.g., 10.8.0.1)"
    },
    "psk": {
      "type": "string",
      "minLength": 16,
      "description": "Pre-shared key for IPsec (shared across all clients)"
    },
    "dns_servers": {
      "type": "array",
      "items": {"type": "string", "format": "ipv4"},
      "description": "DNS servers pushed to clients"
    }
  }
}`)

func (a *Adapter) Descriptor() adapter.Descriptor {
	return adapter.Descriptor{
		Kind:    Kind,
		Version: "1",
		Caps: adapter.Caps{
			// strongSwan supports credential reload without dropping tunnels
			// (swanctl --load-creds), and xl2tpd re-reads CHAP secrets on
			// SIGHUP.
			HotUserAdd: true,
			// SelfAccounting declares that this adapter reports its own usage,
			// not that the protocol ships an accounting API. L2TP has none, but
			// the adapter implements UsageReporter on top of nftables counters,
			// so the capability is true. Declaring false here told the panel
			// this adapter could not account for itself while it was doing
			// exactly that.
			SelfAccounting: true,
			RequiresPKI:    false,
			// PPP authentication uses CHAP username/password.
			CredentialKinds: []adapter.CredentialKind{adapter.CredPassword},
			ServiceSchema:   serviceSchema,
		},
	}
}

// Observe, Plan, Apply, and Probe implementations are in separate files:
// - observe.go: reads current config state from disk
// - plan.go: diffs desired vs observed, emits steps
// - apply.go: executes steps (install, update, reload, remove)
// - probe.go: checks service health
