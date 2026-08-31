package nodes

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/amyrm/antimage/internal/shared/canonical"
)

// Schema v4 adds DNS. The compatibility claim is the same one v3 made for
// Outbounds and Routing: a node given no DNS state produces exactly what a
// v3 panel produced -- same bytes, same hash, no reconcile.

func TestDocumentWithoutDNSIsByteIdenticalToV3(t *testing.T) {
	outbounds := []Outbound{{ID: 1, Tag: "direct", Kind: "direct"}}

	live := Document{Revision: 5, NodeID: 2, Outbounds: outbounds}
	live.SchemaVersion = effectiveSchemaVersion(live)

	// v3Document reproduces the pre-v4 shape on purpose, the same way
	// v2Document does for v2 in document_v3_test.go: a struct that gains a
	// field without omitempty, or a version emitted for content that does not
	// need it, has to diverge from an independent control to be caught.
	type v3Document struct {
		SchemaVersion int        `json:"schema_version"`
		Revision      int64      `json:"revision"`
		NodeID        int64      `json:"node_id"`
		Services      []Service  `json:"services"`
		Subjects      []Subject  `json:"subjects"`
		Outbounds     []Outbound `json:"outbounds,omitempty"`
		Routing       *Routing   `json:"routing,omitempty"`
	}
	old := v3Document{SchemaVersion: 3, Revision: 5, NodeID: 2, Outbounds: outbounds}

	liveBytes, liveSum, err := canonical.Hash(live)
	if err != nil {
		t.Fatalf("hash live: %v", err)
	}
	oldBytes, oldSum, err := canonical.Hash(old)
	if err != nil {
		t.Fatalf("hash v3: %v", err)
	}

	if string(liveBytes) != string(oldBytes) {
		t.Errorf("v4 panel emits different bytes for a node with no DNS state.\n v4: %s\n v3: %s\n"+
			"Every node in every fleet would see a new hash and reconcile for a feature it does not use.",
			liveBytes, oldBytes)
	}
	if liveSum != oldSum {
		t.Errorf("hash moved: %s != %s", liveSum, oldSum)
	}
}

func TestSchemaVersionFollowsDNSContent(t *testing.T) {
	base := Document{Revision: 1, NodeID: 1}
	if got := effectiveSchemaVersion(base); got != 2 {
		t.Errorf("empty document declares v%d, want v2", got)
	}

	withDNS := base
	withDNS.DNS = &DNSConfig{Servers: []DNSServer{{Address: "1.1.1.1"}}}
	if got := effectiveSchemaVersion(withDNS); got != 4 {
		t.Errorf("document with DNS declares v%d, want v4", got)
	}

	// DNS alone, with no outbounds or routing, must still declare v4 -- the
	// version tracks the newest feature actually used, not a chain that
	// requires the previous one.
	withEgressAndDNS := base
	withEgressAndDNS.Outbounds = []Outbound{{ID: 1, Tag: "direct", Kind: "direct"}}
	withEgressAndDNS.DNS = &DNSConfig{Servers: []DNSServer{{Address: "1.1.1.1"}}}
	if got := effectiveSchemaVersion(withEgressAndDNS); got != 4 {
		t.Errorf("document with egress and DNS declares v%d, want v4", got)
	}
}

func TestDNSFieldAppearsOnlyWhenSet(t *testing.T) {
	base := Document{Revision: 1, NodeID: 1}
	base.SchemaVersion = effectiveSchemaVersion(base)
	plain, _, err := canonical.Hash(base)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if strings.Contains(string(plain), `"dns"`) {
		t.Errorf("dns key appears in a document with none: %s", plain)
	}

	withDNS := base
	withDNS.DNS = &DNSConfig{Servers: []DNSServer{{Address: "1.1.1.1"}}}
	withDNS.SchemaVersion = effectiveSchemaVersion(withDNS)
	full, _, err := canonical.Hash(withDNS)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !strings.Contains(string(full), `"dns"`) {
		t.Errorf("dns set but absent from the document: %s", full)
	}
	if !strings.Contains(string(full), `"schema_version":4`) {
		t.Errorf("document carrying DNS does not declare v4: %s", full)
	}
}

func TestBuiltSnapshotDeclaresV2WithoutDNS(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	snap := snapshot(t, s, nodeID)

	if snap.Document.SchemaVersion != 2 {
		t.Errorf("BuildDesiredSnapshot emitted schema v%d for a node with no DNS state; want v2",
			snap.Document.SchemaVersion)
	}
	if strings.Contains(string(snap.Bytes), `"dns"`) {
		t.Errorf("built document mentions dns despite having none: %s", snap.Bytes)
	}
}

// buildDNS reads the raw dns_config column a real PUT would have written --
// see handleSetNodeDNS in httpapi -- and the shape here has to match what
// that handler stores, or a config an operator saved would silently fail to
// build into any document at all.
func TestBuildDNS_EmptyBlobProducesNilConfig(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	var cfg *DNSConfig
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		cfg, err = buildDNS(context.Background(), tx, nodeID)
		return err
	})
	if err != nil {
		t.Fatalf("buildDNS: %v", err)
	}
	if cfg != nil {
		t.Errorf("cfg = %+v, want nil for a node whose dns_config is the default '{}'", cfg)
	}
}

func TestBuildDNS_ReadsStoredConfig(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	raw := `{"servers":[{"address":"1.1.1.1"}],"hosts":{"internal.corp":["10.0.0.5"]}}`
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE nodes SET dns_config = ? WHERE id = ?`, raw, nodeID)
		return err
	})
	if err != nil {
		t.Fatalf("seed dns_config: %v", err)
	}

	var cfg *DNSConfig
	err = s.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		cfg, err = buildDNS(context.Background(), tx, nodeID)
		return err
	})
	if err != nil {
		t.Fatalf("buildDNS: %v", err)
	}
	if cfg == nil {
		t.Fatal("cfg = nil, want the stored config")
	}
	if len(cfg.Servers) != 1 || cfg.Servers[0].Address != "1.1.1.1" {
		t.Errorf("Servers = %+v, want [{Address: 1.1.1.1}]", cfg.Servers)
	}
	if len(cfg.Hosts["internal.corp"]) != 1 || cfg.Hosts["internal.corp"][0] != "10.0.0.5" {
		t.Errorf("Hosts = %+v", cfg.Hosts)
	}
}

func TestBuiltSnapshotIncludesDNS(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE nodes SET dns_config = ? WHERE id = ?`,
			`{"servers":[{"address":"8.8.8.8"}]}`, nodeID)
		return err
	})
	if err != nil {
		t.Fatalf("seed dns_config: %v", err)
	}

	snap := snapshot(t, s, nodeID)
	if snap.Document.SchemaVersion != 4 {
		t.Errorf("schema version = %d, want 4 for a node with DNS state", snap.Document.SchemaVersion)
	}
	if snap.Document.DNS == nil || len(snap.Document.DNS.Servers) != 1 {
		t.Errorf("built snapshot did not carry the stored DNS config: %+v", snap.Document.DNS)
	}
}
