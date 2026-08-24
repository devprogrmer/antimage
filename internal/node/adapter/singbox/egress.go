package singbox

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// egressFile is the node-scoped document carrying outbounds and routing.
//
// Node-scoped, not per-service, for the same reason as in the Xray adapter: an
// outbound belongs to no single inbound, and a routing rule matches across
// them.
const egressFile = "antimage-egress.json"

// outboundParams is the union of what each supported kind needs. Closed rather
// than a passthrough map: params have already been validated against
// OutboundSchema, and decoding into a known shape drops anything this adapter
// does not understand at the boundary instead of handing it to sing-box.
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

// renderOutbound turns one document outbound into sing-box's wire shape.
//
// sing-box names the field "type" where Xray uses "protocol", and its direct
// and block types are called exactly that rather than freedom and blackhole.
// The document's vocabulary is the panel's, so the mapping lives here.
func renderOutbound(o adapter.Outbound) (map[string]any, error) {
	if strings.TrimSpace(o.Tag) == "" {
		return nil, fmt.Errorf("outbound %d has no tag; routing rules select outbounds by tag", o.ID)
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
		out["type"] = "direct"

	case "block":
		out["type"] = "block"

	case "socks", "http":
		if p.Address == "" || p.Port == 0 {
			return nil, fmt.Errorf("outbound %d (%s): %s requires address and port", o.ID, o.Tag, o.Kind)
		}
		out["type"] = o.Kind
		out["server"] = p.Address
		out["server_port"] = p.Port
		if p.Username != "" {
			out["username"] = p.Username
			out["password"] = p.Password
		}

	case "wireguard":
		if p.PrivateKey == "" || p.PeerPublicKey == "" || p.Endpoint == "" {
			return nil, fmt.Errorf(
				"outbound %d (%s): wireguard requires private_key, peer_public_key and endpoint",
				o.ID, o.Tag)
		}
		host, port, err := splitEndpoint(p.Endpoint)
		if err != nil {
			return nil, fmt.Errorf("outbound %d (%s): %w", o.ID, o.Tag, err)
		}
		out["type"] = "wireguard"
		out["private_key"] = p.PrivateKey
		out["peer_public_key"] = p.PeerPublicKey
		out["server"] = host
		out["server_port"] = port
		if len(p.LocalAddresses) > 0 {
			out["local_address"] = toAny(p.LocalAddresses)
		}
		if p.MTU > 0 {
			out["mtu"] = p.MTU
		}

	default:
		// Refused rather than passed through: sing-box rejects an unknown
		// outbound type at startup, and the process failing to start takes
		// every inbound on the node with it.
		return nil, fmt.Errorf("outbound %d (%s): unsupported kind %q", o.ID, o.Tag, o.Kind)
	}

	return out, nil
}

// splitEndpoint separates host:port, which sing-box wants as two fields where
// the document and WireGuard itself carry one string.
func splitEndpoint(endpoint string) (string, int, error) {
	host, portStr, found := strings.Cut(endpoint, ":")
	if !found || host == "" {
		return "", 0, fmt.Errorf("endpoint %q is not host:port", endpoint)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("endpoint %q has no valid port", endpoint)
	}
	return host, port, nil
}

// renderRule turns one routing rule into sing-box's wire shape.
//
// sing-box separates matchers that Xray combines: domain, geosite, ip_cidr and
// geoip are four fields rather than two prefixed lists, and ports are numbers
// rather than a comma-joined string.
func renderRule(r adapter.RoutingRule, known map[string]bool) (map[string]any, error) {
	if strings.TrimSpace(r.OutboundTag) == "" {
		return nil, fmt.Errorf("routing rule %d selects no outbound", r.ID)
	}
	if !known[r.OutboundTag] {
		return nil, fmt.Errorf(
			"routing rule %d selects outbound %q, which this node does not have; "+
				"traffic matching it would fall through to the final outbound instead",
			r.ID, r.OutboundTag)
	}

	rule := map[string]any{"outbound": r.OutboundTag}
	matchers := 0

	if len(r.Domains) > 0 {
		rule["domain"] = toAny(r.Domains)
		matchers++
	}
	if len(r.GeoSite) > 0 {
		rule["geosite"] = toAny(r.GeoSite)
		matchers++
	}
	if len(r.IPCIDRs) > 0 {
		rule["ip_cidr"] = toAny(r.IPCIDRs)
		matchers++
	}
	if len(r.GeoIP) > 0 {
		rule["geoip"] = toAny(r.GeoIP)
		matchers++
	}
	if len(r.Ports) > 0 {
		ports := make([]any, 0, len(r.Ports))
		for _, p := range r.Ports {
			n, err := strconv.Atoi(strings.TrimSpace(p))
			if err != nil {
				// sing-box has a separate port_range field for spans. Refusing
				// is better than silently dropping the matcher, which would
				// widen the rule to all ports.
				return nil, fmt.Errorf(
					"routing rule %d: sing-box takes ports as numbers, got %q", r.ID, p)
			}
			ports = append(ports, n)
		}
		rule["port"] = ports
		matchers++
	}
	if r.Network != "" {
		rule["network"] = r.Network
		matchers++
	}
	if len(r.InboundTags) > 0 {
		rule["inbound"] = toAny(r.InboundTags)
		matchers++
	}
	if len(r.SubjectIDs) > 0 {
		names := make([]string, 0, len(r.SubjectIDs))
		for _, id := range r.SubjectIDs {
			names = append(names, subjectName(id))
		}
		sort.Strings(names)
		rule["user"] = toAny(names)
		matchers++
	}

	if matchers == 0 {
		return nil, fmt.Errorf(
			"routing rule %d has no matchers; sing-box would apply it to all traffic", r.ID)
	}
	return rule, nil
}

// GenerateEgressConfig renders outbounds and routing as one sing-box document.
//
// Returns nil when there is nothing to write, which the caller treats as
// "remove the file" rather than "write an empty document".
func GenerateEgressConfig(outbounds []adapter.Outbound, routing *adapter.Routing) ([]byte, error) {
	if len(outbounds) == 0 && routing == nil {
		return nil, nil
	}

	known := map[string]bool{}
	rendered := make([]any, 0, len(outbounds))
	seen := map[string]int64{}
	for _, o := range outbounds {
		if prev, dup := seen[o.Tag]; dup {
			return nil, fmt.Errorf(
				"outbounds %d and %d share tag %q; a routing rule naming it would be ambiguous",
				prev, o.ID, o.Tag)
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

	if routing != nil {
		ordered := append([]adapter.RoutingRule{}, routing.Rules...)
		sort.SliceStable(ordered, func(i, j int) bool {
			if ordered[i].Priority != ordered[j].Priority {
				return ordered[i].Priority < ordered[j].Priority
			}
			return ordered[i].ID < ordered[j].ID
		})

		rules := make([]any, 0, len(ordered))
		for _, r := range ordered {
			obj, err := renderRule(r, known)
			if err != nil {
				return nil, err
			}
			rules = append(rules, obj)
		}

		route := map[string]any{"rules": rules}
		if routing.DefaultOutboundTag != "" {
			if !known[routing.DefaultOutboundTag] {
				return nil, fmt.Errorf(
					"default outbound %q is not defined on this node",
					routing.DefaultOutboundTag)
			}
			// sing-box has a real "final" field for unmatched traffic, so the
			// default is expressed directly rather than as the trailing
			// catch-all rule the Xray adapter has to synthesise.
			route["final"] = routing.DefaultOutboundTag
		}
		doc["route"] = route
	}

	return json.MarshalIndent(doc, "", "  ")
}

func toAny[T any](in []T) []any {
	out := make([]any, 0, len(in))
	for _, v := range in {
		out = append(out, v)
	}
	return out
}
