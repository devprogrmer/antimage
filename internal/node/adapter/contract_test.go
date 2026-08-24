// Package adapter_test holds contract tests that every concrete adapter must
// satisfy. It lives in the external test package so it can import the adapters
// themselves, which import this package.
package adapter_test

import (
	"testing"

	"github.com/amyrm/antimage/internal/node/adapter"
	"github.com/amyrm/antimage/internal/node/adapter/hysteria2"
	"github.com/amyrm/antimage/internal/node/adapter/l2tp"
	"github.com/amyrm/antimage/internal/node/adapter/singbox"
	"github.com/amyrm/antimage/internal/node/adapter/stub"
	"github.com/amyrm/antimage/internal/node/adapter/wireguard"
	"github.com/amyrm/antimage/internal/node/adapter/xray"
)

// allAdapters constructs every adapter the node can run.
//
// Constructed with zero-valued runtimes and temp directories: these tests only
// read Descriptor() and the static type, neither of which touches a runtime or
// the filesystem. A new adapter that is not listed here is invisible to every
// contract test below, so adding one is part of adding an adapter.
func allAdapters(t *testing.T) map[string]adapter.Adapter {
	t.Helper()
	dir := t.TempDir()
	return map[string]adapter.Adapter{
		"xray":      xray.New(dir, nil, true),
		"singbox":   singbox.New(dir, nil),
		"wireguard": wireguard.New(nil, dir),
		"hysteria2": hysteria2.New(nil, dir),
		"l2tp":      l2tp.New(dir, dir),
		"stub":      stub.New(dir),
	}
}

// A declared capability must match the implemented one.
//
// Caps.SelfAccounting is not a description of the protocol -- it is the
// panel's only way to know whether a node can account for itself. It is
// recorded at Hello and read by panel-side logic and UI.
//
// This test exists because wireguard and l2tp both implemented UsageReporter
// while declaring SelfAccounting: false. Collection still worked, because the
// agent type-asserts rather than reading the flag, so nothing failed loudly --
// the panel simply held a false picture of two of its five adapters. A
// mismatch in the other direction is worse: an adapter that advertises
// accounting it cannot perform would show every user at zero traffic and
// silently grant unlimited quota.
func TestSelfAccountingMatchesTheImplementation(t *testing.T) {
	for name, a := range allAdapters(t) {
		t.Run(name, func(t *testing.T) {
			declared := a.Descriptor().Caps.SelfAccounting
			_, implemented := a.(adapter.UsageReporter)

			switch {
			case declared && !implemented:
				t.Errorf("declares SelfAccounting but does not implement UsageReporter; "+
					"the panel would expect usage reports that never arrive and every "+
					"user on a %s service would appear to use no traffic", name)
			case !declared && implemented:
				t.Errorf("implements UsageReporter but declares SelfAccounting=false; " +
					"usage is collected, but the panel records this adapter as unable " +
					"to account for itself")
			}
		})
	}
}

// HotUserAdd is the other capability the panel acts on: it decides whether
// adding a user drops every session on the node. An adapter that advertises it
// must not be planning restarts for a plain user add, but that is a behavioural
// claim the per-adapter suites verify. Here we only pin the invariant that the
// descriptor is well formed, so a half-filled Caps cannot ship.
func TestDescriptorIsWellFormed(t *testing.T) {
	for name, a := range allAdapters(t) {
		t.Run(name, func(t *testing.T) {
			d := a.Descriptor()
			if d.Kind == "" {
				t.Error("adapter reports an empty Kind; the panel keys services by it")
			}
			if string(d.Kind) != name {
				t.Errorf("Kind = %q, want %q", d.Kind, name)
			}
			if d.Version == "" {
				t.Error("adapter reports an empty Version")
			}
			if len(d.Caps.CredentialKinds) == 0 {
				t.Error("adapter declares no credential kinds; the panel cannot " +
					"decide what to issue a subject on this service")
			}
		})
	}
}

// An adapter that declares egress support must actually be able to apply it.
//
// SupportsOutbounds and SupportsRouting are fail-closed: the panel will not
// send outbounds or routing to an adapter declaring false, so declaring false
// is always safe. Declaring TRUE is the dangerous direction -- the panel would
// build a v3 document, report convergence, and the node would route traffic by
// its own defaults while the UI showed an egress policy that was never applied.
//
// Two things are pinned here. An adapter claiming outbound support must publish
// an OutboundSchema, because the panel validates Outbound.Params against it
// exactly as it validates Service.Params against ServiceSchema, and a missing
// schema means writes are unvalidated rather than rejected. And routing without
// outbounds is incoherent: a routing rule selects an outbound by tag, so an
// adapter that cannot hold outbounds has nothing for a rule to name.
func TestEgressCapabilitiesAreHonest(t *testing.T) {
	for name, a := range allAdapters(t) {
		t.Run(name, func(t *testing.T) {
			caps := a.Descriptor().Caps

			if caps.SupportsOutbounds && len(caps.OutboundSchema) == 0 {
				t.Error("declares SupportsOutbounds but publishes no OutboundSchema; " +
					"the panel would accept unvalidated outbound params")
			}
			if !caps.SupportsOutbounds && len(caps.OutboundSchema) > 0 {
				t.Error("publishes an OutboundSchema but declares SupportsOutbounds=false; " +
					"the schema is unreachable and the pair disagree")
			}
			if caps.SupportsRouting && !caps.SupportsOutbounds {
				t.Error("declares SupportsRouting without SupportsOutbounds; " +
					"a routing rule selects an outbound by tag, so there would be " +
					"nothing for a rule to name")
			}
		})
	}
}
