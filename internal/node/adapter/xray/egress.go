package xray

import (
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// egressFile is the node-scoped document carrying outbounds and routing.
//
// Node-scoped, not per-service: an outbound is not owned by any one inbound,
// and a routing rule matches across them. Giving it its own file is what lets
// it change without rewriting every service.
const egressFile = "antimage-egress.json"

// Tags Xray's own infrastructure already occupies.
//
// GenerateStatsConfig writes outbounds tagged "direct" and "api" and an inbound
// tagged "api-inbound". Xray merges confdir documents by APPENDING arrays, so a
// second outbound with one of these tags is not an override -- it is a
// duplicate, and Xray resolves duplicates by first match. An operator outbound
// named "direct" would therefore be silently ignored in favour of the stats
// document's freedom outbound.
const (
	tagDirect     = "direct"
	tagAPI        = "api"
	tagAPIInbound = "api-inbound"
)

// reservedTags may be REFERENCED by a routing rule but never DEFINED by an
// operator outbound. "direct" in particular is worth keeping referenceable:
// sending traffic straight out is the single most common rule an operator
// writes, and the stats document already provides exactly that outbound.
var reservedTags = map[string]bool{
	tagDirect: true,
	tagAPI:    true,
}

// outboundParams is the union of what each supported kind needs.
//
// Deliberately a closed struct rather than a passthrough map: params reaching
// here have already been validated against OutboundSchema by the panel, and
// decoding into a known shape means a field the adapter does not understand is
// dropped at the boundary instead of being handed to Xray unexamined.
type outboundParams struct {
	// socks and http.
	Address  string `json:"address,omitempty"`
	Port     int    `json:"port,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`

	// wireguard.
	PrivateKey     string   `json:"private_key,omitempty"`
	PeerPublicKey  string   `json:"peer_public_key,omitempty"`
	Endpoint       string   `json:"endpoint,omitempty"`
	LocalAddresses []string `json:"local_addresses,omitempty"`
	MTU            int      `json:"mtu,omitempty"`
}

// renderOutbound turns one document outbound into Xray's wire shape.
func renderOutbound(o adapter.Outbound) (map[string]any, error) {
	if strings.TrimSpace(o.Tag) == "" {
		return nil, fmt.Errorf("outbound %d has no tag; routing rules select outbounds by tag", o.ID)
	}
	if reservedTags[o.Tag] {
		return nil, fmt.Errorf(
			"outbound %d uses reserved tag %q: Xray's accounting configuration already defines it, "+
				"and confdir merge would append rather than override, leaving this one unused",
			o.ID, o.Tag)
	}

	var p outboundParams
	if len(o.Params) > 0 {
		if err := json.Unmarshal(o.Params, &p); err != nil {
			return nil, fmt.Errorf("outbound %d (%s) params: %w", o.ID, o.Tag, err)
		}
	}

	out := map[string]any{"tag": o.Tag}

	switch o.Kind {
	case "direct":
		out["protocol"] = "freedom"

	case "block":
		out["protocol"] = "blackhole"

	case "socks", "http":
		if p.Address == "" || p.Port == 0 {
			return nil, fmt.Errorf("outbound %d (%s): %s requires address and port", o.ID, o.Tag, o.Kind)
		}
		server := map[string]any{"address": p.Address, "port": p.Port}
		if p.Username != "" {
			// Xray takes credentials as a one-element users array for both
			// socks and http.
			server["users"] = []any{map[string]any{
				"user": p.Username,
				"pass": p.Password,
			}}
		}
		out["protocol"] = o.Kind
		out["settings"] = map[string]any{"servers": []any{server}}

	case "wireguard":
		if p.PrivateKey == "" || p.PeerPublicKey == "" || p.Endpoint == "" {
			return nil, fmt.Errorf(
				"outbound %d (%s): wireguard requires private_key, peer_public_key and endpoint",
				o.ID, o.Tag)
		}
		peer := map[string]any{
			"publicKey": p.PeerPublicKey,
			"endpoint":  p.Endpoint,
		}
		settings := map[string]any{
			"secretKey": p.PrivateKey,
			"peers":     []any{peer},
		}
		if len(p.LocalAddresses) > 0 {
			settings["address"] = toAny(p.LocalAddresses)
		}
		if p.MTU > 0 {
			settings["mtu"] = p.MTU
		}
		out["protocol"] = "wireguard"
		out["settings"] = settings

	default:
		// Refused rather than passed through. An unknown kind reaching Xray
		// produces a process that fails to start, which takes the whole node
		// down -- including inbounds that were working.
		return nil, fmt.Errorf("outbound %d (%s): unsupported kind %q", o.ID, o.Tag, o.Kind)
	}

	return out, nil
}

// renderRule turns one routing rule into Xray's wire shape.
//
// Xray requires at least one matcher on a field rule. A rule with none would
// match everything, which is not what an operator who left every matcher empty
// meant -- they meant the rule to be inert, or they made a mistake. Either way
// silently routing all traffic somewhere is the wrong reading.
func renderRule(r adapter.RoutingRule, known map[string]bool, serviceIDs []int64) (map[string]any, error) {
	if strings.TrimSpace(r.OutboundTag) == "" {
		return nil, fmt.Errorf("routing rule %d selects no outbound", r.ID)
	}
	if !known[r.OutboundTag] {
		return nil, fmt.Errorf(
			"routing rule %d selects outbound %q, which this node does not have; "+
				"traffic matching it would fall through to the default instead",
			r.ID, r.OutboundTag)
	}

	rule := map[string]any{
		"type":        "field",
		"outboundTag": r.OutboundTag,
	}

	matchers := 0
	// Xray takes domains and geosite in one field, distinguished by prefix.
	domains := append([]string{}, r.Domains...)
	for _, g := range r.GeoSite {
		domains = append(domains, "geosite:"+g)
	}
	if len(domains) > 0 {
		rule["domain"] = toAny(domains)
		matchers++
	}

	// Same for IP CIDRs and GeoIP.
	ips := append([]string{}, r.IPCIDRs...)
	for _, g := range r.GeoIP {
		ips = append(ips, "geoip:"+g)
	}
	if len(ips) > 0 {
		rule["ip"] = toAny(ips)
		matchers++
	}

	if len(r.Ports) > 0 {
		// Xray wants ports as a single comma-joined string.
		rule["port"] = strings.Join(r.Ports, ",")
		matchers++
	}
	if r.Network != "" {
		rule["network"] = r.Network
		matchers++
	}
	if len(r.InboundTags) > 0 {
		rule["inboundTag"] = toAny(r.InboundTags)
		matchers++
	}
	if len(r.SubjectIDs) > 0 {
		// Xray matches users by the same email tag accounting aggregates by, so
		// the two cannot disagree about who a rule applies to.
		//
		// Since C2 that tag is per-service, so ONE subject now has one email
		// per inbound they are on and a rule naming a subject has to list all
		// of them. Listing only the legacy node-wide form here would leave a
		// rule that matches nobody: the operator's routing policy would sit in
		// the config looking correct and never fire.
		emails := make([]string, 0, len(r.SubjectIDs)*max(len(serviceIDs), 1))
		for _, id := range r.SubjectIDs {
			if len(serviceIDs) == 0 {
				// No inbounds on this node, so nothing can match whatever is
				// written. The legacy form keeps the document well-formed.
				emails = append(emails, subjectEmail(id, 0))
				continue
			}
			for _, svcID := range serviceIDs {
				emails = append(emails, subjectEmail(id, svcID))
			}
		}
		sort.Strings(emails)
		rule["user"] = toAny(emails)
		matchers++
	}

	if matchers == 0 {
		return nil, fmt.Errorf(
			"routing rule %d has no matchers; Xray would apply it to all traffic", r.ID)
	}
	return rule, nil
}

// renderDNS turns the document's DNS config into Xray's dns object.
//
// A server with no Domains and no SkipFallback renders as a bare address
// string rather than an object -- the plain form Xray's own examples use,
// and indistinguishable in effect from the object form with both fields
// empty, so there is no reason to make every config carry the more verbose
// shape just because the struct always could.
func renderDNS(d *adapter.DNSConfig) (map[string]any, error) {
	if d == nil {
		return nil, nil
	}

	out := map[string]any{}

	if len(d.Servers) > 0 {
		servers := make([]any, 0, len(d.Servers))
		for i, s := range d.Servers {
			if strings.TrimSpace(s.Address) == "" {
				return nil, fmt.Errorf("dns server %d has no address", i)
			}
			if len(s.Domains) == 0 && !s.SkipFallback {
				servers = append(servers, s.Address)
				continue
			}
			obj := map[string]any{"address": s.Address}
			if len(s.Domains) > 0 {
				obj["domains"] = toAny(s.Domains)
			}
			if s.SkipFallback {
				obj["skipFallback"] = true
			}
			servers = append(servers, obj)
		}
		out["servers"] = servers
	}

	if len(d.Hosts) > 0 {
		hosts := map[string]any{}
		// Sorted so the rendered document is byte-identical across builds --
		// Go map iteration order is random, and this file's checksum is what
		// planEgress diffs against to decide whether anything changed.
		domains := make([]string, 0, len(d.Hosts))
		for domain := range d.Hosts {
			domains = append(domains, domain)
		}
		sort.Strings(domains)
		for _, domain := range domains {
			ips := d.Hosts[domain]
			if len(ips) == 0 {
				return nil, fmt.Errorf("dns host %q has no addresses", domain)
			}
			if len(ips) == 1 {
				hosts[domain] = ips[0]
			} else {
				hosts[domain] = toAny(ips)
			}
		}
		out["hosts"] = hosts
	}

	if len(d.FakeDNS) > 0 {
		pools := make([]any, 0, len(d.FakeDNS))
		for i, p := range d.FakeDNS {
			if _, _, err := net.ParseCIDR(p.IPPool); err != nil {
				return nil, fmt.Errorf("fakedns pool %d: %q is not a valid CIDR: %w", i, p.IPPool, err)
			}
			if p.PoolSize <= 0 {
				return nil, fmt.Errorf("fakedns pool %d (%s): pool_size must be positive", i, p.IPPool)
			}
			pools = append(pools, map[string]any{"ipPool": p.IPPool, "poolSize": p.PoolSize})
		}
		out["fakedns"] = pools
	}

	switch d.QueryStrategy {
	case "", "UseIP", "UseIPv4", "UseIPv6":
	default:
		return nil, fmt.Errorf("dns query_strategy %q is not one of UseIP, UseIPv4, UseIPv6", d.QueryStrategy)
	}
	if d.QueryStrategy != "" {
		out["queryStrategy"] = d.QueryStrategy
	}
	if d.DisableCache {
		out["disableCache"] = true
	}

	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// GenerateEgressConfig renders outbounds, routing, and DNS as one Xray
// document.
//
// Returns nil when there is nothing to write, which the caller treats as
// "remove the file" rather than "write an empty document": an empty routing
// block is not inert in Xray, and a file that exists with no rules is harder to
// reason about than no file.
func GenerateEgressConfig(
	outbounds []adapter.Outbound, routing *adapter.Routing, serviceIDs []int64,
	dns *adapter.DNSConfig,
) ([]byte, error) {
	if len(outbounds) == 0 && routing == nil && dns == nil {
		return nil, nil
	}

	// Tags a rule may legally reference: everything defined here, plus the
	// infrastructure tags the stats document already provides.
	known := map[string]bool{tagDirect: true, tagAPI: true}

	rendered := make([]any, 0, len(outbounds))
	seen := map[string]int64{}
	for _, o := range outbounds {
		if prev, dup := seen[o.Tag]; dup {
			return nil, fmt.Errorf(
				"outbounds %d and %d share tag %q; Xray resolves duplicates by first match, "+
					"so one of them would silently never be used", prev, o.ID, o.Tag)
		}
		obj, err := renderOutbound(o)
		if err != nil {
			return nil, err
		}
		seen[o.Tag] = o.ID
		known[o.Tag] = true
		rendered = append(rendered, obj)
	}

	doc := map[string]any{}
	if len(rendered) > 0 {
		doc["outbounds"] = rendered
	}

	// The accounting rule is repeated here, first, ON PURPOSE.
	//
	// GenerateStatsConfig already emits it, but Xray merges confdir documents
	// in filename order and appends rule arrays, and "antimage-egress.json"
	// sorts before "antimage-stats.json". Without this copy, an operator rule
	// carrying no inboundTag -- a plain "send *.example.com through WARP", say
	// -- would be evaluated first and would also match the accounting API's own
	// inbound, sending its traffic to the wrong outbound and breaking stats
	// collection for the whole node. Repeating the rule makes egress ordering
	// independent of what the confdir happens to be called.
	rules := []any{map[string]any{
		"type":        "field",
		"inboundTag":  []any{tagAPIInbound},
		"outboundTag": tagAPI,
	}}

	if routing != nil {
		ordered := append([]adapter.RoutingRule{}, routing.Rules...)
		sort.SliceStable(ordered, func(i, j int) bool {
			if ordered[i].Priority != ordered[j].Priority {
				return ordered[i].Priority < ordered[j].Priority
			}
			return ordered[i].ID < ordered[j].ID
		})
		for _, r := range ordered {
			obj, err := renderRule(r, known, serviceIDs)
			if err != nil {
				return nil, err
			}
			rules = append(rules, obj)
		}

		if routing.DefaultOutboundTag != "" {
			if !known[routing.DefaultOutboundTag] {
				return nil, fmt.Errorf(
					"default outbound %q is not defined on this node",
					routing.DefaultOutboundTag)
			}
			// Xray has no "default outbound" field: unmatched traffic goes to
			// the first outbound in the list. A catch-all rule at the END of
			// the chain expresses the same intent without depending on
			// outbound ordering across merged documents, which the operator
			// does not control.
			rules = append(rules, map[string]any{
				"type":        "field",
				"network":     "tcp,udp",
				"outboundTag": routing.DefaultOutboundTag,
			})
		}
	}

	doc["routing"] = map[string]any{"rules": rules}

	dnsObj, err := renderDNS(dns)
	if err != nil {
		return nil, err
	}
	if dnsObj != nil {
		doc["dns"] = dnsObj
	}

	// MarshalIndent for an operator reading this during an incident;
	// encoding/json sorts map keys, which is what keeps it deterministic.
	return json.MarshalIndent(doc, "", "  ")
}

func toAny[T any](in []T) []any {
	out := make([]any, 0, len(in))
	for _, v := range in {
		out = append(out, v)
	}
	return out
}

// egressKind is the marker's shape value for the node-scoped egress document.
const egressKind = "egress"

// egressMarker is the marker line for the egress document.
//
// It carries no service id, because there is no service. parseMarker requires
// a non-zero one -- it treats service=0 as malformed -- so egress gets its own
// marker and its own parser rather than a sentinel id smuggled through the
// service field, which would leave "-1" appearing wherever service ids are
// logged or reported.
func egressMarker(checksum string) string {
	return fmt.Sprintf("%s kind=%s sha256=%s", markerPrefix, egressKind, checksum)
}

// parseEgressMarker reports whether this adapter wrote the egress document.
//
// Returns ok=false for a file a human created, which must never be overwritten
// -- the same contract parseMarker has for service files.
func parseEgressMarker(body string) (checksum string, ok bool) {
	line, _, _ := strings.Cut(body, "\n")
	if !strings.HasPrefix(line, markerPrefix) {
		return "", false
	}
	var sawKind bool
	for _, field := range strings.Fields(strings.TrimPrefix(line, markerPrefix)) {
		key, value, found := strings.Cut(field, "=")
		if !found {
			continue
		}
		switch key {
		case "kind":
			sawKind = value == egressKind
		case "sha256":
			checksum = value
		}
	}
	return checksum, sawKind && checksum != ""
}
