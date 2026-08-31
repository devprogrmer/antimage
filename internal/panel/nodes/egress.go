package nodes

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// buildOutbounds assembles the egress paths this node may send traffic through.
//
// Disabled outbounds are omitted rather than carried with a flag. The document
// describes what the node should be running, and an outbound that is switched
// off is not part of that -- the same reasoning that keeps a disabled subject
// out of buildSubjects rather than shipping it with enabled=false.
//
// Params are stored sealed (encrypted with the master key) and are unsealed here
// before being placed in the document. If unsealer is nil and outbounds exist,
// the function returns an error rather than shipping params that may be sealed
// under a key the caller does not have.
//
// Ordered by id so the canonical document is byte-identical across builds.
func buildOutbounds(ctx context.Context, tx *sql.Tx, nodeID int64, unsealer Unsealer) ([]Outbound, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, tag, kind, params
		   FROM outbounds
		  WHERE node_id = ? AND enabled = 1
		  ORDER BY id`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("read outbounds for node %d: %w", nodeID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []Outbound
	for rows.Next() {
		var o Outbound
		var paramsSealed string
		if err := rows.Scan(&o.ID, &o.Tag, &o.Kind, &paramsSealed); err != nil {
			return nil, fmt.Errorf("scan outbound: %w", err)
		}

		// If we have outbounds but no unsealer, fail rather than potentially
		// shipping sealed data or omitting configured egress paths.
		if unsealer == nil {
			return nil, fmt.Errorf(
				"node %d has outbound(s) but no unsealer was supplied: "+
					"refusing to build a document that would omit them", nodeID)
		}

		params, err := OpenOutboundParams(unsealer, paramsSealed)
		if err != nil {
			return nil, fmt.Errorf("unseal outbound %d params: %w", o.ID, err)
		}
		o.Params = params
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate outbounds: %w", err)
	}
	return out, nil
}

// buildRouting assembles the node's rule table, its balancers, and its
// default.
//
// Returns nil when the node has none of the three, which is what keeps the
// document at schema v2 and its hash unmoved. A node with any one of them
// gets a non-nil Routing; see effectiveSchemaVersion for which version that
// then declares.
func buildRouting(ctx context.Context, tx *sql.Tx, nodeID int64) (*Routing, error) {
	var defaultTag string
	if err := tx.QueryRowContext(ctx,
		`SELECT default_outbound_tag FROM nodes WHERE id = ?`, nodeID).Scan(&defaultTag); err != nil {
		return nil, fmt.Errorf("read default outbound for node %d: %w", nodeID, err)
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT id, priority, domains, ip_cidrs, geoip, geosite, ports,
		        inbound_tags, subject_ids, network, outbound_tag, balancer_tag
		   FROM routing_rules
		  WHERE node_id = ? AND enabled = 1
		  ORDER BY priority, id`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("read routing rules for node %d: %w", nodeID, err)
	}
	defer func() { _ = rows.Close() }()

	var rules []RoutingRule
	for rows.Next() {
		var r RoutingRule
		var domains, ipCIDRs, geoIP, geoSite, ports, inboundTags, subjectIDs string
		if err := rows.Scan(
			&r.ID, &r.Priority, &domains, &ipCIDRs, &geoIP, &geoSite, &ports,
			&inboundTags, &subjectIDs, &r.Network, &r.OutboundTag, &r.BalancerTag,
		); err != nil {
			return nil, fmt.Errorf("scan routing rule: %w", err)
		}
		for _, f := range []struct {
			raw  string
			into any
			name string
		}{
			{domains, &r.Domains, "domains"},
			{ipCIDRs, &r.IPCIDRs, "ip_cidrs"},
			{geoIP, &r.GeoIP, "geoip"},
			{geoSite, &r.GeoSite, "geosite"},
			{ports, &r.Ports, "ports"},
			{inboundTags, &r.InboundTags, "inbound_tags"},
			{subjectIDs, &r.SubjectIDs, "subject_ids"},
		} {
			if err := json.Unmarshal([]byte(f.raw), f.into); err != nil {
				// Refused rather than skipped. A matcher that fails to decode
				// would silently widen the rule -- a rule meant for three
				// domains would match every domain -- and routing traffic
				// somewhere the operator did not ask for is worse than
				// refusing to build the document at all.
				return nil, fmt.Errorf("routing rule %d: decode %s: %w", r.ID, f.name, err)
			}
		}
		rules = append(rules, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate routing rules: %w", err)
	}

	balancers, err := buildBalancers(ctx, tx, nodeID)
	if err != nil {
		return nil, err
	}

	if len(rules) == 0 && defaultTag == "" && len(balancers) == 0 {
		return nil, nil
	}
	return &Routing{Rules: rules, DefaultOutboundTag: defaultTag, Balancers: balancers}, nil
}

// buildBalancers assembles the node's named outbound pools.
//
// Ordered by id so the canonical document is byte-identical across builds,
// the same reasoning buildOutbounds already documents.
func buildBalancers(ctx context.Context, tx *sql.Tx, nodeID int64) ([]Balancer, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, tag, selector, strategy
		   FROM balancers
		  WHERE node_id = ? AND enabled = 1
		  ORDER BY id`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("read balancers for node %d: %w", nodeID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []Balancer
	for rows.Next() {
		var b Balancer
		var selector string
		if err := rows.Scan(&b.ID, &b.Tag, &selector, &b.Strategy); err != nil {
			return nil, fmt.Errorf("scan balancer: %w", err)
		}
		if err := json.Unmarshal([]byte(selector), &b.Selector); err != nil {
			// Refused rather than skipped, the same reasoning a routing
			// rule's matchers use: a selector that fails to decode would
			// silently make the balancer pick among zero outbounds instead
			// of the ones the operator configured.
			return nil, fmt.Errorf("balancer %d: decode selector: %w", b.ID, err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate balancers: %w", err)
	}
	return out, nil
}
