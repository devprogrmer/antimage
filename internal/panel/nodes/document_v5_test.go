package nodes

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/amyrm/antimage/internal/shared/canonical"
)

// Schema v5 adds Balancers. The compatibility claim is the same one v3 and
// v4 made for their own additions: a node given no balancer state produces
// exactly what a v4 panel produced -- same bytes, same hash, no reconcile.

func TestDocumentWithoutBalancersIsByteIdenticalToV4(t *testing.T) {
	rule := RoutingRule{ID: 1, Domains: []string{"x.com"}, OutboundTag: "direct"}

	live := Document{Revision: 9, NodeID: 4, Routing: &Routing{Rules: []RoutingRule{rule}}}
	live.SchemaVersion = effectiveSchemaVersion(live)

	// v4Document reproduces the pre-v5 shape on purpose, the same way
	// v3Document does for v3 in document_v4_test.go.
	type v4RoutingRule struct {
		ID          int64    `json:"id"`
		Priority    int      `json:"priority"`
		Domains     []string `json:"domains,omitempty"`
		OutboundTag string   `json:"outbound_tag,omitempty"`
	}
	type v4Routing struct {
		Rules              []v4RoutingRule `json:"rules"`
		DefaultOutboundTag string          `json:"default_outbound_tag,omitempty"`
	}
	type v4Document struct {
		SchemaVersion int        `json:"schema_version"`
		Revision      int64      `json:"revision"`
		NodeID        int64      `json:"node_id"`
		Services      []Service  `json:"services"`
		Subjects      []Subject  `json:"subjects"`
		Outbounds     []Outbound `json:"outbounds,omitempty"`
		Routing       *v4Routing `json:"routing,omitempty"`
	}
	old := v4Document{
		SchemaVersion: 3, Revision: 9, NodeID: 4,
		Routing: &v4Routing{Rules: []v4RoutingRule{{ID: 1, Domains: []string{"x.com"}, OutboundTag: "direct"}}},
	}

	liveBytes, liveSum, err := canonical.Hash(live)
	if err != nil {
		t.Fatalf("hash live: %v", err)
	}
	oldBytes, oldSum, err := canonical.Hash(old)
	if err != nil {
		t.Fatalf("hash v4: %v", err)
	}

	if string(liveBytes) != string(oldBytes) {
		t.Errorf("v5 panel emits different bytes for a node with no balancer state.\n v5: %s\n v4: %s\n"+
			"Every node in every fleet would see a new hash and reconcile for a feature it does not use.",
			liveBytes, oldBytes)
	}
	if liveSum != oldSum {
		t.Errorf("hash moved: %s != %s", liveSum, oldSum)
	}
}

func TestSchemaVersionFollowsBalancerContent(t *testing.T) {
	base := Document{Revision: 1, NodeID: 1}
	if got := effectiveSchemaVersion(base); got != 2 {
		t.Errorf("empty document declares v%d, want v2", got)
	}

	withBalancer := base
	withBalancer.Routing = &Routing{Balancers: []Balancer{{Tag: "b1", Selector: []string{"warp"}}}}
	if got := effectiveSchemaVersion(withBalancer); got != 5 {
		t.Errorf("document with a balancer declares v%d, want v5", got)
	}

	// A balancer alone, with no rules and no DNS, must still declare v5 --
	// the version tracks the newest feature actually used.
	withDNSAndBalancer := base
	withDNSAndBalancer.DNS = &DNSConfig{Servers: []DNSServer{{Address: "1.1.1.1"}}}
	withDNSAndBalancer.Routing = &Routing{Balancers: []Balancer{{Tag: "b1", Selector: []string{"warp"}}}}
	if got := effectiveSchemaVersion(withDNSAndBalancer); got != 5 {
		t.Errorf("document with DNS and a balancer declares v%d, want v5", got)
	}
}

func TestBalancerFieldAppearsOnlyWhenSet(t *testing.T) {
	base := Document{Revision: 1, NodeID: 1, Routing: &Routing{
		Rules: []RoutingRule{{ID: 1, Domains: []string{"x.com"}, OutboundTag: "direct"}},
	}}
	base.SchemaVersion = effectiveSchemaVersion(base)
	plain, _, err := canonical.Hash(base)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if strings.Contains(string(plain), `"balancers"`) {
		t.Errorf("balancers key appears in a document with none: %s", plain)
	}

	withBalancer := base
	withBalancer.Routing = &Routing{
		Rules:     base.Routing.Rules,
		Balancers: []Balancer{{Tag: "b1", Selector: []string{"warp"}}},
	}
	withBalancer.SchemaVersion = effectiveSchemaVersion(withBalancer)
	full, _, err := canonical.Hash(withBalancer)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !strings.Contains(string(full), `"balancers"`) {
		t.Errorf("balancers set but absent from the document: %s", full)
	}
	if !strings.Contains(string(full), `"schema_version":5`) {
		t.Errorf("document carrying a balancer does not declare v5: %s", full)
	}
}

func TestBuiltSnapshotDeclaresV2WithoutBalancers(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	snap := snapshot(t, s, nodeID)

	if snap.Document.SchemaVersion != 2 {
		t.Errorf("BuildDesiredSnapshot emitted schema v%d for a node with no balancer state; want v2",
			snap.Document.SchemaVersion)
	}
	if strings.Contains(string(snap.Bytes), `"balancers"`) {
		t.Errorf("built document mentions balancers despite having none: %s", snap.Bytes)
	}
}

func TestBuildBalancers_ReadsStoredRows(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO balancers (node_id, tag, selector, strategy, created_at, updated_at)
			 VALUES (?, 'b1', '["warp-"]', 'least_ping', 0, 0)`, nodeID)
		return err
	})
	if err != nil {
		t.Fatalf("seed balancer: %v", err)
	}

	var balancers []Balancer
	err = s.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		balancers, err = buildBalancers(context.Background(), tx, nodeID)
		return err
	})
	if err != nil {
		t.Fatalf("buildBalancers: %v", err)
	}
	if len(balancers) != 1 {
		t.Fatalf("got %d balancers, want 1", len(balancers))
	}
	if balancers[0].Tag != "b1" || balancers[0].Strategy != "least_ping" {
		t.Errorf("balancer = %+v", balancers[0])
	}
	if len(balancers[0].Selector) != 1 || balancers[0].Selector[0] != "warp-" {
		t.Errorf("selector = %+v, want [\"warp-\"]", balancers[0].Selector)
	}
}

func TestBuildBalancers_DisabledRowIsOmitted(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO balancers (node_id, tag, selector, strategy, enabled, created_at, updated_at)
			 VALUES (?, 'b1', '["warp"]', 'random', 0, 0, 0)`, nodeID)
		return err
	})
	if err != nil {
		t.Fatalf("seed balancer: %v", err)
	}

	var balancers []Balancer
	err = s.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		balancers, err = buildBalancers(context.Background(), tx, nodeID)
		return err
	})
	if err != nil {
		t.Fatalf("buildBalancers: %v", err)
	}
	if len(balancers) != 0 {
		t.Errorf("got %d balancers, want 0 -- a disabled balancer must not be desired state", len(balancers))
	}
}

func TestBuildRouting_ReadsBalancerTagOnARule(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`INSERT INTO balancers (node_id, tag, selector, strategy, created_at, updated_at)
			 VALUES (?, 'b1', '["warp"]', 'random', 0, 0)`, nodeID); err != nil {
			return err
		}
		_, err := tx.Exec(
			`INSERT INTO routing_rules
			   (node_id, priority, domains, ip_cidrs, geoip, geosite, ports,
			    inbound_tags, subject_ids, network, outbound_tag, balancer_tag,
			    created_at, updated_at)
			 VALUES (?, 0, '["x.com"]', '[]', '[]', '[]', '[]', '[]', '[]', '', '', 'b1', 0, 0)`,
			nodeID)
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	var routing *Routing
	err = s.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		routing, err = buildRouting(context.Background(), tx, nodeID)
		return err
	})
	if err != nil {
		t.Fatalf("buildRouting: %v", err)
	}
	if routing == nil || len(routing.Rules) != 1 {
		t.Fatalf("routing = %+v", routing)
	}
	if routing.Rules[0].BalancerTag != "b1" {
		t.Errorf("BalancerTag = %q, want b1", routing.Rules[0].BalancerTag)
	}
	if routing.Rules[0].OutboundTag != "" {
		t.Errorf("OutboundTag = %q, want empty for a balancer-targeting rule", routing.Rules[0].OutboundTag)
	}
}

func TestBuiltSnapshotIncludesBalancers(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO balancers (node_id, tag, selector, strategy, created_at, updated_at)
			 VALUES (?, 'b1', '["warp"]', 'random', 0, 0)`, nodeID)
		return err
	})
	if err != nil {
		t.Fatalf("seed balancer: %v", err)
	}

	snap := snapshot(t, s, nodeID)
	if snap.Document.SchemaVersion != 5 {
		t.Errorf("schema version = %d, want 5 for a node with balancer state", snap.Document.SchemaVersion)
	}
	if snap.Document.Routing == nil || len(snap.Document.Routing.Balancers) != 1 {
		t.Errorf("built snapshot did not carry the stored balancer: %+v", snap.Document.Routing)
	}
}
