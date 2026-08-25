// Package adapterfactory builds a node's adapter registry from its config.
//
// It is the one place that knows every adapter's constructor, and it exists as
// its own package so the agent does not. The agent drives adapters through the
// adapter.Adapter interface and is deliberately ignorant of which protocols
// exist; putting the wiring here keeps that true, and keeps cmd/antimage-node
// down to reading a file and starting a client.
package adapterfactory

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/amyrm/antimage/internal/node/adapter"
	"github.com/amyrm/antimage/internal/node/adapter/hysteria2"
	"github.com/amyrm/antimage/internal/node/adapter/l2tp"
	"github.com/amyrm/antimage/internal/node/adapter/singbox"
	"github.com/amyrm/antimage/internal/node/adapter/stub"
	"github.com/amyrm/antimage/internal/node/adapter/wireguard"
	"github.com/amyrm/antimage/internal/node/adapter/xray"
	"github.com/amyrm/antimage/internal/node/agent"
)

// Build constructs the adapters this host declares, in declaration order.
//
// A config with no adapters section gets the stub, which is what the node ran
// before adapters were declarable at all. That keeps an existing node.yaml
// working across the upgrade rather than refusing to start, but the stub serves
// no traffic -- so Build reports that through NoAdaptersConfigured and the
// caller says so out loud. A node that quietly serves nothing is the failure
// this whole phase is about.
func Build(cfg *agent.Config) (*agent.Registry, error) {
	specs := cfg.Adapters
	if len(specs) == 0 {
		specs = []agent.AdapterConfig{{Kind: string(stub.Kind)}}
	}

	ads := make([]adapter.Adapter, 0, len(specs))
	for i, spec := range specs {
		ad, err := build(cfg, spec)
		if err != nil {
			return nil, fmt.Errorf("adapters[%d] (%s): %w", i, spec.Kind, err)
		}
		ads = append(ads, ad)
	}

	// NewRegistry refuses duplicate kinds: two adapters of one kind would
	// manage the same files and overwrite each other on every pass.
	return agent.NewRegistry(ads...)
}

// NoAdaptersConfigured reports whether the config declared none, so the caller
// can warn. It is not an error: a node with no adapters is a legitimate state
// during enrolment, and refusing to start would strand a host that has been
// registered but not yet provisioned.
func NoAdaptersConfigured(cfg *agent.Config) bool { return len(cfg.Adapters) == 0 }

// SupportedKinds lists what Build accepts, for error messages and for the
// installer. Sorted so the message is stable.
func SupportedKinds() []string {
	kinds := []string{
		string(stub.Kind), string(xray.Kind), string(singbox.Kind),
		string(wireguard.Kind), string(hysteria2.Kind), string(l2tp.Kind),
	}
	sort.Strings(kinds)
	return kinds
}

// build constructs one adapter.
//
// An unknown kind is refused rather than skipped. An operator who typed
// "wiregaurd" has a node that serves nothing, and the difference between
// "refused to start with the reason" and "started and quietly ignored one line
// of your config" is the difference between a five-minute fix and an outage
// nobody can explain.
func build(cfg *agent.Config, spec agent.AdapterConfig) (adapter.Adapter, error) {
	// State is per adapter so two of them cannot collide over one sidecar file.
	stateDir := stateDirFor(cfg, spec.Kind)

	switch adapter.Kind(strings.ToLower(strings.TrimSpace(spec.Kind))) {
	case stub.Kind:
		return stub.New(orDefault(spec.ConfigDir, filepath.Join(cfg.StateDir, "services"))), nil

	case xray.Kind:
		rt := xray.NewExecRuntime(
			orDefault(spec.Unit, "xray"),
			spec.APIAddress, // empty is supported: it costs a restart per user change
			orDefault(spec.Binary, "xray"),
		)
		// HotAddSupported is reported per NODE rather than per adapter type,
		// because whether Xray can add a user without a restart depends on this
		// host having configured the management API.
		return xray.New(orDefault(spec.ConfigDir, defaultXrayDir), rt, rt.HotAddSupported()), nil

	case singbox.Kind:
		rt := singbox.NewExecRuntime(
			orDefault(spec.Unit, "sing-box"),
			orDefault(spec.Binary, "sing-box"),
		)
		return singbox.New(orDefault(spec.ConfigDir, defaultSingboxDir), rt), nil

	case wireguard.Kind:
		return wireguard.New(wireguard.NewExecRuntime(),
			orDefault(spec.ConfigDir, wireguard.DefaultConfigDir), stateDir), nil

	case hysteria2.Kind:
		return hysteria2.New(hysteria2.NewExecRuntime(),
			orDefault(spec.ConfigDir, hysteria2.DefaultConfigDir), stateDir), nil

	case l2tp.Kind:
		return l2tp.New(orDefault(spec.ConfigDir, defaultL2TPDir), stateDir), nil
	}

	return nil, fmt.Errorf("unknown adapter kind %q; this node supports %s",
		spec.Kind, strings.Join(SupportedKinds(), ", "))
}

// Defaults for adapters that do not export one.
const (
	defaultXrayDir    = "/usr/local/etc/xray"
	defaultSingboxDir = "/etc/sing-box"
	defaultL2TPDir    = "/etc"
)

func orDefault(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

// stateDirFor is the per-adapter state directory rule, exposed so a test can
// assert the rule rather than reach inside a constructed adapter.
func stateDirFor(cfg *agent.Config, kind string) string {
	return filepath.Join(cfg.StateDir, "adapters", strings.ToLower(kind))
}
