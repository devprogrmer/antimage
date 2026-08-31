// Package nodes owns the node registry and the desired-state document.
//
// The document is derived from relational tables on demand, never stored as a
// blob (spec section 5). Its serialization is canonical per RFC 8785.
//
// Fields that have existed since v1 use no omitempty: every one is always
// present, and absent means an explicit null. Adding or removing one changes
// every node's hash and so requires a migration that recomputes stored hashes.
//
// Fields introduced by a LATER schema version do use omitempty, and the
// document declares the lowest version that fully describes its content. A node
// given no newer state therefore serialises exactly as the older panel did, so
// no hash moves and no fleet reconciles for a feature it does not use. Subject's
// v2 enforcement policies established this; v3's Outbounds and Routing follow
// it. See effectiveSchemaVersion.
package nodes

import "encoding/json"

// DocumentSchemaVersion is the highest version this panel can emit.
//
// v1: Initial schema (SP1-SP2)
// v2: Added enforcement policies to Subject (User Management Enhancements)
// v3: Added Outbounds and Routing
// v4: Added DNS
//
// The version a given document CARRIES is not this constant: see
// effectiveSchemaVersion. A document declares the lowest version that fully
// describes it, so a fleet using no v3 feature keeps emitting v2 and its
// hashes do not move.
const DocumentSchemaVersion = 4

// schemaVersionEnforcement is the version that introduced Subject enforcement
// policies, and the floor for every document this panel emits. Nothing older
// is produced, because every supported agent understands v2.
const schemaVersionEnforcement = 2

// schemaVersionEgress is the version that introduced Outbounds and Routing.
const schemaVersionEgress = 3

// schemaVersionDNS is the version that introduced DNS.
const schemaVersionDNS = 4

type Credential struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// Subject is wired but stays empty in SP1. SP2 populates it.
// Subject represents a user/device that may connect.
// Schema v1: ID + Credentials only
// Schema v2: Added enforcement policies (MaxDevices, MaxIPs, etc.)
type Subject struct {
	ID          int64        `json:"id"`
	Credentials []Credential `json:"credentials"`
	// Enforcement policies (schema v2+)
	MaxDevices         *int64 `json:"max_devices,omitempty"`
	MaxIPs             *int64 `json:"max_ips,omitempty"`
	MaxConnections     *int64 `json:"max_connections,omitempty"`
	SpeedLimitUpKbps   *int64 `json:"speed_limit_up_kbps,omitempty"`
	SpeedLimitDownKbps *int64 `json:"speed_limit_down_kbps,omitempty"`
}

type Service struct {
	ID      int64           `json:"id"`
	Kind    string          `json:"kind"`
	Enabled bool            `json:"enabled"`
	Params  json.RawMessage `json:"params"`
}

// Outbound is an egress path a node may send traffic through.
//
// The mirror image of Service: a Service is where traffic arrives, an Outbound
// is where it leaves. Params is adapter-specific and validated against the
// adapter's OutboundSchema, exactly as Service.Params is validated against
// ServiceSchema -- so a new outbound kind needs no change here.
//
// Tag, not ID, is what a routing rule references. Adapters address outbounds by
// name in their own configuration (Xray and sing-box both do), so carrying the
// tag in the document keeps the generated config a direct rendering of desired
// state rather than something that needs an id-to-name table to interpret.
type Outbound struct {
	ID     int64           `json:"id"`
	Tag    string          `json:"tag"`
	Kind   string          `json:"kind"`
	Params json.RawMessage `json:"params"`
}

// RoutingRule selects an outbound for traffic that matches every predicate set
// on it. An empty predicate is not a wildcard, it is simply not considered.
//
// The matcher set is deliberately conservative: it covers what Xray and
// sing-box can both actually express, so a rule the panel accepts is a rule the
// node can enforce. Widening it is an adapter question, not a document one.
type RoutingRule struct {
	ID int64 `json:"id"`
	// Priority orders evaluation, lowest first. Ties break on ID so the
	// canonical document is byte-identical across builds.
	Priority int `json:"priority"`

	Domains     []string `json:"domains,omitempty"`
	IPCIDRs     []string `json:"ip_cidrs,omitempty"`
	GeoIP       []string `json:"geoip,omitempty"`
	GeoSite     []string `json:"geosite,omitempty"`
	Ports       []string `json:"ports,omitempty"`
	Network     string   `json:"network,omitempty"`
	InboundTags []string `json:"inbound_tags,omitempty"`
	SubjectIDs  []int64  `json:"subject_ids,omitempty"`

	// OutboundTag names the Outbound this rule selects. Required: a rule that
	// matches but selects nothing is a rule that silently drops traffic.
	OutboundTag string `json:"outbound_tag"`
}

// Routing is the node's rule table plus the fallback for unmatched traffic.
type Routing struct {
	Rules []RoutingRule `json:"rules"`
	// DefaultOutboundTag receives traffic no rule matched. Empty means the
	// adapter's own default applies, which for both Xray and sing-box is the
	// first outbound.
	DefaultOutboundTag string `json:"default_outbound_tag,omitempty"`
}

// DNSServer is one resolver the node may query. Domains scopes it to a
// subset of lookups (split DNS) -- empty means it may answer any query.
type DNSServer struct {
	Address      string   `json:"address"`
	Domains      []string `json:"domains,omitempty"`
	SkipFallback bool     `json:"skip_fallback,omitempty"`
}

// FakeDNSPool is one address range the node hands out instead of resolving a
// domain for real, deferring the real lookup until the connection's
// destination is actually dialed. An IPv4 and an IPv6 pool are two separate
// entries because Xray keys pools by address family.
type FakeDNSPool struct {
	IPPool   string `json:"ip_pool"`
	PoolSize int    `json:"pool_size"`
}

// DNSConfig is the node's DNS resolution behavior: which servers to query,
// static overrides that skip resolution entirely, and fake-IP pools that
// defer resolution until a connection's real destination is known.
type DNSConfig struct {
	Servers []DNSServer `json:"servers,omitempty"`
	// Hosts maps a domain to the IP address(es) it always resolves to.
	Hosts         map[string][]string `json:"hosts,omitempty"`
	FakeDNS       []FakeDNSPool       `json:"fakedns,omitempty"`
	QueryStrategy string              `json:"query_strategy,omitempty"`
	DisableCache  bool                `json:"disable_cache,omitempty"`
}

// Document is what an agent converges against.
//
// Fields present since v1 carry no omitempty: absent means an explicit null,
// and adding or removing one changes every node's hash. Fields added by a
// later version DO carry omitempty, so a document using none of them
// serialises byte-for-byte as the older version did and no stored hash moves.
// Subject's v2 enforcement policies established this; Outbounds and Routing
// follow it.
type Document struct {
	SchemaVersion int       `json:"schema_version"`
	Revision      int64     `json:"revision"`
	NodeID        int64     `json:"node_id"`
	Services      []Service `json:"services"`
	Subjects      []Subject `json:"subjects"`

	// Egress (schema v3+).
	Outbounds []Outbound `json:"outbounds,omitempty"`
	Routing   *Routing   `json:"routing,omitempty"`

	// DNS (schema v4+).
	DNS *DNSConfig `json:"dns,omitempty"`
}

// effectiveSchemaVersion reports the lowest version that fully describes doc.
//
// Declaring the panel's maximum unconditionally would change schema_version on
// every document the moment the constant moved, and schema_version is inside
// the hashed bytes -- so every node in every fleet would see a new hash and
// reconcile, for a feature none of them use. Deriving it from content instead
// means a node gains v3 only when it is actually given v3 state.
//
// It also keeps the version honest in the other direction: an agent that
// refuses versions above the one it understands stays compatible while it has
// no egress configuration, and correctly refuses the document that first gives
// it some.
func effectiveSchemaVersion(doc Document) int {
	if doc.DNS != nil {
		return schemaVersionDNS
	}
	if len(doc.Outbounds) > 0 || doc.Routing != nil {
		return schemaVersionEgress
	}
	return schemaVersionEnforcement
}

// Snapshot bundles the three values that must always travel together.
// Callers must never recompute any of them independently (invariant 5).
type Snapshot struct {
	Revision int64
	Document Document
	Bytes    []byte
	SHA256   string
}
