//go:build e2e

package e2e

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Egress end to end: an operator creates an outbound through the API, the panel
// carries it in the desired document, and the real Xray adapter renders it onto
// the node's confdir.
//
// The adapter suites prove rendering and the panel suites prove the API. What
// only this can prove is that the two halves agree -- that what the panel writes
// is what the adapter can consume, across the document boundary and the schema
// version negotiation that came with it.

// egressCapable makes the node advertise xray, which is what the panel's
// capability gate looks for before it will store an outbound.
func egressCapable(t *testing.T, s *sp2Env) {
	t.Helper()
	if err := s.store.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE nodes SET adapter_kinds = ? WHERE id = ?`,
			`["xray"]`, s.nodeID)
		return err
	}); err != nil {
		t.Fatalf("advertise xray: %v", err)
	}
}

func (s *sp2Env) createOutbound(t *testing.T, body string) int64 {
	t.Helper()
	var out struct {
		ID int64 `json:"id"`
	}
	path := "/api/v1/nodes/" + itoa(s.nodeID) + "/outbounds"
	if code := s.apiJSON("POST", path, body, &out); code != http.StatusCreated {
		t.Fatalf("create outbound: %d", code)
	}
	return out.ID
}

func (s *sp2Env) createRoutingRule(t *testing.T, body string) int64 {
	t.Helper()
	var out struct {
		ID int64 `json:"id"`
	}
	path := "/api/v1/nodes/" + itoa(s.nodeID) + "/routing"
	if code := s.apiJSON("POST", path, body, &out); code != http.StatusCreated {
		t.Fatalf("create routing rule: %d", code)
	}
	return out.ID
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// The compatibility promise the whole v3 design rests on: a node with no egress
// keeps emitting v2 and its document hash does not move.
//
// If this ever fails, every node in every fleet reconciles once for a feature
// none of them use.
func TestEgressAbsentKeepsDocumentAtV2(t *testing.T) {
	s := startSP2(t, "xray")

	snap := s.snapshot(t)
	if snap.Document.SchemaVersion != 2 {
		t.Errorf("node with no egress emits schema v%d, want v2",
			snap.Document.SchemaVersion)
	}
	for _, key := range []string{"outbounds", "routing"} {
		if strings.Contains(string(snap.Bytes), key) {
			t.Errorf("document mentions %q despite having none: %s", key, snap.Bytes)
		}
	}

	before := snap.SHA256
	// Something unrelated changes; the hash moves for that reason and no other.
	s.createSubject(t, "someone", nil)
	if s.snapshot(t).SHA256 == before {
		t.Error("adding a subject did not change the document hash")
	}
}

// The whole loop: API -> document -> real adapter -> file on disk.
func TestOutboundReachesTheNodeConfdir(t *testing.T) {
	s := startSP2(t, "xray")
	egressCapable(t, s)

	s.createOutbound(t, `{"tag":"warp","kind":"block"}`)
	s.createRoutingRule(t,
		`{"priority":10,"domains":["example.com"],"outbound_tag":"warp"}`)

	// The document must have moved to v3, and must carry both.
	snap := s.snapshot(t)
	if snap.Document.SchemaVersion != 3 {
		t.Fatalf("document with egress emits schema v%d, want v3",
			snap.Document.SchemaVersion)
	}
	if len(snap.Document.Outbounds) != 1 || snap.Document.Outbounds[0].Tag != "warp" {
		t.Fatalf("outbound missing from the document: %+v", snap.Document.Outbounds)
	}
	if snap.Document.Routing == nil || len(snap.Document.Routing.Rules) != 1 {
		t.Fatalf("routing missing from the document: %+v", snap.Document.Routing)
	}

	// Now the real adapter, over the real document.
	plan := s.reconcile(t)
	var wroteEgress bool
	for _, step := range plan.Steps {
		if step.Kind == "write_egress" {
			wroteEgress = true
			if step.Disruption != adapter.DisruptRestart {
				t.Errorf("egress step is %v, want restart", step.Disruption)
			}
		}
	}
	if !wroteEgress {
		t.Fatalf("adapter planned no egress work: %+v", plan.Steps)
	}

	body, err := os.ReadFile(filepath.Join(s.confDir, "antimage-egress.json"))
	if err != nil {
		t.Fatalf("egress config not written to the confdir: %v", err)
	}
	for _, want := range []string{`"warp"`, `"example.com"`, `"blackhole"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("rendered egress config is missing %s:\n%s", want, body)
		}
	}

	// The accounting rule has to come first, or an operator rule with no
	// inboundTag captures the stats API's own traffic and silently breaks
	// accounting for the node.
	var doc struct {
		Routing struct {
			Rules []map[string]any `json:"rules"`
		} `json:"routing"`
	}
	payload := body
	if idx := strings.IndexByte(string(body), '\n'); idx >= 0 {
		payload = body[idx+1:] // skip the marker line
	}
	if err := json.Unmarshal(payload, &doc); err != nil {
		t.Fatalf("rendered egress config is not valid JSON: %v\n%s", err, payload)
	}
	if len(doc.Routing.Rules) < 2 {
		t.Fatalf("want the accounting rule plus the operator rule, got %d", len(doc.Routing.Rules))
	}
	if doc.Routing.Rules[0]["outboundTag"] != "api" {
		t.Errorf("first rule does not protect the accounting API: %+v", doc.Routing.Rules[0])
	}
}

// Removing the egress configuration must take the file with it, rather than
// leaving a node routing by a policy the panel no longer holds.
func TestRemovingEgressReturnsTheNodeToV2(t *testing.T) {
	s := startSP2(t, "xray")
	egressCapable(t, s)

	id := s.createOutbound(t, `{"tag":"warp","kind":"block"}`)
	s.reconcile(t)
	if _, err := os.Stat(filepath.Join(s.confDir, "antimage-egress.json")); err != nil {
		t.Fatalf("egress config not written: %v", err)
	}

	path := "/api/v1/nodes/" + itoa(s.nodeID) + "/outbounds/" + itoa(id)
	if code := s.apiJSON("DELETE", path, "", nil); code != http.StatusNoContent {
		t.Fatalf("delete outbound: %d", code)
	}

	snap := s.snapshot(t)
	if snap.Document.SchemaVersion != 2 {
		t.Errorf("document stayed at v%d after the last outbound was removed, want v2",
			snap.Document.SchemaVersion)
	}

	s.reconcile(t)
	if _, err := os.Stat(filepath.Join(s.confDir, "antimage-egress.json")); !os.IsNotExist(err) {
		t.Errorf("egress config survived removal: %v", err)
	}
}

// A node whose adapters cannot route must be refused at the API, not left to
// fail on the node.
func TestEgressRefusedOnNodeWithoutARoutingEngine(t *testing.T) {
	s := startSP2(t, "xray")
	if err := s.store.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE nodes SET adapter_kinds = ? WHERE id = ?`,
			`["wireguard"]`, s.nodeID)
		return err
	}); err != nil {
		t.Fatalf("advertise wireguard: %v", err)
	}

	path := "/api/v1/nodes/" + itoa(s.nodeID) + "/outbounds"
	code := s.apiJSON("POST", path, `{"tag":"warp","kind":"block"}`, nil)
	if code != http.StatusUnprocessableEntity {
		t.Errorf("create outbound on a wireguard-only node = %d, want 422", code)
	}
}
