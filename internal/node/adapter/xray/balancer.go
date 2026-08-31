package xray

import (
	"fmt"
	"sort"
	"strings"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// leastPingStrategy is the one Strategy value that requires live latency
// data -- see renderObservatory.
const leastPingStrategy = "least_ping"

// defaultProbeURL and defaultProbeInterval are Xray's own commonly-used
// observatory defaults. Not operator-configurable here: the only thing a
// balancer's own settings determine is WHETHER a least_ping strategy needs
// live data at all, not how that data gets collected, and hardcoding a
// known-good probe keeps this feature from depending on an operator having
// picked a working URL.
const (
	defaultProbeURL      = "https://www.google.com/generate_204"
	defaultProbeInterval = "10s"
)

// renderBalancers turns the document's balancers into Xray's wire shape,
// and returns the set of balancer tags a routing rule may now legally
// reference.
//
// knownOutbounds is required because a balancer's Selector names outbound
// tag PREFIXES -- Xray does prefix matching, not exact -- and a selector
// that cannot match anything currently defined is almost certainly a typo:
// refusing it here is cheaper for the operator than discovering, after the
// node fails to converge, that traffic meant for a balancer silently never
// left through the outbounds it was supposed to pick among.
func renderBalancers(balancers []adapter.Balancer, knownOutbounds map[string]bool) ([]any, map[string]bool, error) {
	if len(balancers) == 0 {
		return nil, map[string]bool{}, nil
	}

	rendered := make([]any, 0, len(balancers))
	knownBalancers := make(map[string]bool, len(balancers))
	seen := map[string]int64{}

	for _, b := range balancers {
		if strings.TrimSpace(b.Tag) == "" {
			return nil, nil, fmt.Errorf("balancer %d has no tag", b.ID)
		}
		if prev, dup := seen[b.Tag]; dup {
			return nil, nil, fmt.Errorf(
				"balancers %d and %d share tag %q; a routing rule naming it would be ambiguous",
				prev, b.ID, b.Tag)
		}
		seen[b.Tag] = b.ID

		if len(b.Selector) == 0 {
			return nil, nil, fmt.Errorf("balancer %d (%s) has no selector; it would match no outbound", b.ID, b.Tag)
		}
		matchesSomething := false
		for _, prefix := range b.Selector {
			for tag := range knownOutbounds {
				if strings.HasPrefix(tag, prefix) {
					matchesSomething = true
					break
				}
			}
		}
		if !matchesSomething {
			return nil, nil, fmt.Errorf(
				"balancer %d (%s): selector %v matches no outbound this node has",
				b.ID, b.Tag, b.Selector)
		}

		strategyType := "random"
		switch b.Strategy {
		case "", "random":
		case leastPingStrategy:
			strategyType = "leastPing"
		default:
			return nil, nil, fmt.Errorf(
				"balancer %d (%s): strategy %q is not random or least_ping", b.ID, b.Tag, b.Strategy)
		}

		rendered = append(rendered, map[string]any{
			"tag":      b.Tag,
			"selector": toAny(b.Selector),
			"strategy": map[string]any{"type": strategyType},
		})
		knownBalancers[b.Tag] = true
	}

	return rendered, knownBalancers, nil
}

// renderObservatory derives Xray's observatory block from whichever
// balancers use the least_ping strategy -- the only one that consults live
// latency data. A "random" balancer needs nothing probed, so a document
// using only that strategy gets no observatory block at all.
//
// subjectSelector is the union of those balancers' own selectors, sorted so
// the rendered document stays byte-identical across builds: Go does not
// guarantee slice order from a map-backed union, and this file's checksum
// is what planEgress diffs against to decide whether anything changed.
func renderObservatory(balancers []adapter.Balancer) map[string]any {
	subjects := map[string]bool{}
	for _, b := range balancers {
		if b.Strategy != leastPingStrategy {
			continue
		}
		for _, prefix := range b.Selector {
			subjects[prefix] = true
		}
	}
	if len(subjects) == 0 {
		return nil
	}

	selector := make([]string, 0, len(subjects))
	for s := range subjects {
		selector = append(selector, s)
	}
	sort.Strings(selector)

	return map[string]any{
		"subjectSelector": toAny(selector),
		"probeUrl":        defaultProbeURL,
		"probeInterval":   defaultProbeInterval,
	}
}
