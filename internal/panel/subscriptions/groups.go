package subscriptions

import "sort"

// Filter decides which of a subject's inbounds a subscription carries.
//
// Held as a type rather than a bare []string so the "empty means everything"
// rule lives in one place. Spelling that rule out at each call site is how one
// of them ends up reading an empty list as "nothing" and cutting a customer
// off.
type Filter struct {
	// Protocols to include. EMPTY MEANS EVERY PROTOCOL, not none -- a group
	// with nothing selected hands the customer their whole entitlement rather
	// than an empty subscription. See migration 00039.
	Protocols []string
}

// NoFilter carries everything, which is what a subject with no group gets.
func NoFilter() Filter { return Filter{} }

// Allows reports whether this protocol may appear.
func (f Filter) Allows(protocol string) bool {
	if len(f.Protocols) == 0 {
		return true
	}
	for _, p := range f.Protocols {
		if p == protocol {
			return true
		}
	}
	return false
}

// IsEmpty reports whether the filter restricts anything, so a caller can tell
// "carries everything" from "carries these three" without inspecting the slice
// and re-deriving the rule.
func (f Filter) IsEmpty() bool { return len(f.Protocols) == 0 }

// Apply drops the servers this filter excludes.
//
// Filtering happens on the mapped Server, not on the raw inbound, because the
// protocol of an Xray inbound lives inside its params -- an adapter-kind match
// would treat vless and trojan on the same adapter as one thing and make a
// protocol filter useless for exactly the case it exists for.
func (f Filter) Apply(servers []Server) []Server {
	if f.IsEmpty() {
		return servers
	}
	out := make([]Server, 0, len(servers))
	for _, s := range servers {
		if f.Allows(s.Protocol) {
			out = append(out, s)
		}
	}
	return out
}

// ApplyConfigs is the same rule over per-inbound client configurations.
//
// The panel's per-inbound view and the aggregated subscription must agree
// about what a group excludes, or an operator sees a WireGuard entry in the
// panel that the customer's subscription does not contain and has nothing to
// explain the difference.
func (f Filter) ApplyConfigs(configs []ClientConfig) []ClientConfig {
	if f.IsEmpty() {
		return configs
	}
	out := make([]ClientConfig, 0, len(configs))
	for _, c := range configs {
		if f.Allows(c.Protocol) {
			out = append(out, c)
		}
	}
	return out
}

// KnownProtocols lists everything a group may select, derived from what the
// config builder can actually produce.
//
// Hardcoded here rather than read from the adapters, and the distinction is
// worth stating: this is the set of PROTOCOL names a subscription can filter
// on, which is not the same as the set of adapter kinds a node can run. Xray
// runs three of these from one adapter, and WireGuard's adapter produces a
// protocol no aggregated format carries but which a group may still legitimately
// include or exclude from the per-inbound view.
func KnownProtocols() []string {
	out := []string{
		"vless", "vmess", "trojan", "shadowsocks", "hysteria2",
		"wireguard", "openvpn", "ocserv", "l2tp",
	}
	sort.Strings(out)
	return out
}
