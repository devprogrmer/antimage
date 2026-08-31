package agent

import (
	"context"
	"fmt"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Registry is the set of adapters one node runs.
//
// A node serves several protocols at once, each by its own adapter, and they
// share one desired document: every adapter reads the whole thing and acts on
// the services whose Kind is its own. That is why every adapter carries a
// `svc.Kind != string(Kind)` filter in Plan.
//
// Registry deliberately does NOT implement adapter.Adapter, though it would be
// the obvious shape. Observe and Plan cannot be merged across adapters:
//
//   - adapter.ObservedService carries no Kind. A merged Observed handed to an
//     adapter is indistinguishable from its own, and every adapter's removal
//     pass plans a removal for observed services it does not recognise. Xray
//     given a merged Observed would plan remove_service for a WireGuard
//     interface, and mean it.
//   - Plan is a pure function of ONE adapter's desired-versus-observed. Fusing
//     two adapters' plans and handing the result back to either is not a
//     reconciliation of anything.
//
// So the fan-out lives in the Reconciler, which runs a complete, isolated
// Observe -> Plan -> Apply cycle per adapter and merges only the outcomes.
// Isolation is structural here rather than a rule each adapter must remember:
// an adapter is never shown another's observations at all.
type Registry struct {
	adapters []adapter.Adapter
}

// NewRegistry returns the adapters in the order given.
//
// Order is preserved and matters: it fixes the order of steps in an apply run,
// and a plan that is not deterministic cannot be compared between passes --
// which is what the convergence check does.
//
// Two adapters of the same kind are refused. They would manage the same config
// directory and the same units, so each would see the other's files as drift
// and rewrite them, forever.
func NewRegistry(ads ...adapter.Adapter) (*Registry, error) {
	seen := make(map[adapter.Kind]struct{}, len(ads))
	for _, ad := range ads {
		if ad == nil {
			return nil, fmt.Errorf("nil adapter in registry")
		}
		kind := ad.Descriptor().Kind
		if _, dup := seen[kind]; dup {
			return nil, fmt.Errorf("two adapters registered for kind %q: they would "+
				"manage the same files and overwrite each other on every pass", kind)
		}
		seen[kind] = struct{}{}
	}
	return &Registry{adapters: ads}, nil
}

// MustRegistry is NewRegistry for callers that construct a fixed set and cannot
// meaningfully handle a failure -- tests, and the node's own startup, where a
// duplicate kind is a programming error rather than a runtime condition.
func MustRegistry(ads ...adapter.Adapter) *Registry {
	r, err := NewRegistry(ads...)
	if err != nil {
		panic(err)
	}
	return r
}

// Adapters returns the registered adapters in order.
func (r *Registry) Adapters() []adapter.Adapter {
	if r == nil {
		return nil
	}
	return r.adapters
}

// Len reports how many adapters this node runs.
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	return len(r.adapters)
}

// Descriptors returns every adapter's descriptor, in registration order.
//
// This is what the node reports at Hello. The Hello message's Adapters field
// has always been repeated; it simply only ever carried one entry, because the
// node only ever held one adapter. The panel therefore learns exactly which
// protocols this node can execute -- which is what lets an editor offer only
// those, rather than everything the panel was compiled to know about.
func (r *Registry) Descriptors() []adapter.Descriptor {
	if r == nil {
		return nil
	}
	out := make([]adapter.Descriptor, 0, len(r.adapters))
	for _, ad := range r.adapters {
		out = append(out, ad.Descriptor())
	}
	return out
}

// AdapterHealth pairs a probe result with the adapter that produced it.
type AdapterHealth struct {
	Kind   adapter.Kind
	Health adapter.Health
}

// Probe asks every adapter for its health.
//
// One adapter being unwell does not stop the others from being asked: the
// heartbeat reports per-kind health, and an operator needs to see which
// protocol is down, not merely that something is.
func (r *Registry) Probe(ctx context.Context) []AdapterHealth {
	if r == nil {
		return nil
	}
	out := make([]AdapterHealth, 0, len(r.adapters))
	for _, ad := range r.adapters {
		health, err := ad.Probe(ctx)
		if err != nil {
			// A probe that errored is a probe that failed. Reporting it as
			// unhealthy with the reason is more use than dropping the entry,
			// which would read as "this adapter is fine".
			health = adapter.Health{OK: false, Detail: err.Error()}
		}
		out = append(out, AdapterHealth{Kind: ad.Descriptor().Kind, Health: health})
	}
	return out
}

// AdapterRestartOutcome pairs a restart result with the adapter that
// produced it. Named to match the wire message it becomes
// (pb.AdapterRestartOutcome), so control.dispatchCommand can build one
// straight from the other without an intermediate translation the two could
// drift out of step with.
type AdapterRestartOutcome struct {
	Kind adapter.Kind
	OK   bool
	Err  error
}

// RestartAll bounces every adapter matching kinds, or every adapter this
// node runs if kinds is empty.
//
// One adapter failing to restart does not stop the others from being asked,
// same reasoning as Probe: an operator who asked to restart everything and
// got back "xray restarted, wireguard has no restart concept" learned
// something true about both. Stopping at the first failure would have told
// them nothing about wireguard at all.
func (r *Registry) RestartAll(ctx context.Context, kinds []string) []AdapterRestartOutcome {
	if r == nil {
		return nil
	}
	want := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		want[k] = true
	}
	out := make([]AdapterRestartOutcome, 0, len(r.adapters))
	for _, ad := range r.adapters {
		kind := ad.Descriptor().Kind
		if len(want) > 0 && !want[string(kind)] {
			continue
		}
		err := ad.Restart(ctx)
		out = append(out, AdapterRestartOutcome{Kind: kind, OK: err == nil, Err: err})
	}
	return out
}

// UsageReporters returns the adapters that account for their own traffic.
//
// Not every adapter does -- Caps.SelfAccounting says whether it claims to, and
// the type assertion says whether it actually implements the interface. Both
// are checked, because a descriptor that claims accounting while the type does
// not implement it is a bug that should not silently produce zero usage.
func (r *Registry) UsageReporters() []adapter.UsageReporter {
	if r == nil {
		return nil
	}
	var out []adapter.UsageReporter
	for _, ad := range r.adapters {
		if reporter, ok := ad.(adapter.UsageReporter); ok {
			out = append(out, reporter)
		}
	}
	return out
}
