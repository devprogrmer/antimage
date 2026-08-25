//go:build e2e && realruntime

// Real-runtime verification for the Inbound Studio's API path (Phase E).
//
// Phase E shipped three layers -- the multi-adapter registry, the
// service-schema endpoint, and the Studio UI -- and every test covering them
// stopped short of the same line. The registry tests drive fake adapters. The
// schema and validation tests drive a node whose Hello is synthesised by
// reportHello. The Studio's component tests drive a mocked fetch. All of them
// pass against a control plane that could not actually apply anything a real
// adapter would accept, because none of them ever asked a real adapter, and
// none of them ever asked a real binary.
//
// That was the stated exit criterion for Phase E and it was not met: no test
// drove a node through the Studio's API path against real binaries. This file
// is that test. It uses the SAME panel, mTLS listener, agent and reconciler as
// the acceptance harness, swapping only the stub adapter for real ones, and it
// finishes by handing what the adapter wrote to the real xray binary and
// letting the binary decide whether it is valid.
//
// Requires -tags "e2e realruntime" AND the binaries. The tag is the opt-in;
// once it is set a missing binary FAILS rather than skips, because skipping is
// how a real-runtime gap hides.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/node/adapter/wireguard"
	"github.com/amyrm/antimage/internal/node/adapter/xray"
	"github.com/amyrm/antimage/internal/node/agent"
)

// studioEnv is the full acceptance harness with real adapters behind it.
type studioEnv struct {
	*env
	confDir string
	rt      *xray.ExecRuntime
	port    int
	// schemas as the adapters themselves publish them, for comparison against
	// what the panel serves.
	xraySchema json.RawMessage
	wgSchema   json.RawMessage
}

// startStudioNode brings up panel + enrolled node + a real agent running TWO
// real adapters concurrently.
//
// Both are registered on purpose. A single-adapter node cannot show the
// property Phase E was scoped around -- that one host runs several adapters and
// each keeps its own Kind isolation -- and it cannot show the Studio offering
// exactly the set the node reported rather than the set the panel was built
// with.
func startStudioNode(t *testing.T) *studioEnv {
	t.Helper()

	xrayBin := binaryFor(t, "XRAY_BINARY", "xray")
	// The WireGuard adapter's Observe calls Available(), which needs the real
	// tooling. Resolved here so a missing binary fails at setup with a clear
	// message rather than as an opaque convergence timeout.
	_ = binaryFor(t, "WG_BINARY", "wg")

	shimDir := buildShim(t)
	shimState := t.TempDir()
	t.Setenv("SHIM_STATE", shimState)
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ANTIMAGE_REALRUNTIME", "1")

	confDir := t.TempDir()
	port := freePort(t)
	const unit = "antimage-xray"

	// Point the shim's unit at the real binary, reading the directory the
	// adapter writes into. When the adapter restarts the unit, the real xray
	// process starts against the real generated config.
	spec, err := json.Marshal(map[string]any{
		"path": xrayBin,
		"args": []string{"run", "-confdir", confDir},
		"port": port,
	})
	if err != nil {
		t.Fatalf("encode unit: %v", err)
	}
	if err := os.WriteFile(filepath.Join(shimState, unit+".json"), spec, 0o600); err != nil {
		t.Fatalf("write unit: %v", err)
	}

	e := startPanel(t)
	e.createNodeAndEnroll()

	rt := xray.NewExecRuntime(unit, "", xrayBin)
	xa := xray.New(confDir, rt, rt.HotAddSupported())
	wga := wireguard.New(wireguard.NewExecRuntime(), t.TempDir(), t.TempDir())

	e.startAgentWith(agent.MustRegistry(xa, wga))
	e.waitForStatus("online", 30*time.Second)

	return &studioEnv{
		env: e, confDir: confDir, rt: rt, port: port,
		xraySchema: xa.Descriptor().Caps.ServiceSchema,
		wgSchema:   wga.Descriptor().Caps.ServiceSchema,
	}
}

// schemaEntry mirrors one element of the /service-schemas response.
type schemaEntry struct {
	Kind        string          `json:"kind"`
	Version     string          `json:"version"`
	Schema      json.RawMessage `json:"schema"`
	Offerable   bool            `json:"offerable"`
	Reason      string          `json:"reason"`
	HotUserAdd  bool            `json:"hot_user_add"`
	RequiresPKI bool            `json:"requires_pki"`
}

func (s *studioEnv) serviceSchemas(t *testing.T) map[string]schemaEntry {
	t.Helper()
	var out struct {
		Adapters []schemaEntry `json:"adapters"`
	}
	code := s.apiJSON("GET", fmt.Sprintf("/api/v1/nodes/%d/service-schemas", s.nodeID), "", &out)
	if code != http.StatusOK {
		t.Fatalf("GET service-schemas = %d", code)
	}
	byKind := make(map[string]schemaEntry, len(out.Adapters))
	for _, a := range out.Adapters {
		byKind[a.Kind] = a
	}
	return byKind
}

// sameJSON compares two documents by value, so formatting differences on the
// wire are not mistaken for a schema that travelled from somewhere else.
func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatalf("left is not JSON: %v", err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatalf("right is not JSON: %v", err)
	}
	return reflect.DeepEqual(x, y)
}

func (s *studioEnv) listServices(t *testing.T) []struct {
	ID          int64           `json:"id"`
	AdapterKind string          `json:"adapter_kind"`
	Params      json.RawMessage `json:"params"`
} {
	t.Helper()
	var out struct {
		Services []struct {
			ID          int64           `json:"id"`
			AdapterKind string          `json:"adapter_kind"`
			Params      json.RawMessage `json:"params"`
		} `json:"services"`
	}
	if code := s.apiJSON("GET",
		fmt.Sprintf("/api/v1/nodes/%d/services", s.nodeID), "", &out); code != http.StatusOK {
		t.Fatalf("GET services = %d", code)
	}
	return out.Services
}

// ------------------------------------------------------------------ the test

// THE Phase E exit criterion: an inbound created through the Studio's own API
// path reaches a real adapter and produces a config the real binary accepts.
//
// Each step is one link in the chain the Studio depends on, and the chain has
// never been exercised whole: the node's real adapters publish schemas at
// Hello, the panel serves exactly those, the panel validates submitted params
// against the node's copy, the reconciler converges, and xray itself passes
// judgement on the result.
func TestRealRuntimeStudioCreatesAnInboundOnARealNode(t *testing.T) {
	s := startStudioNode(t)

	// 1. The Studio's first call. Both real adapters must be offered.
	schemas := s.serviceSchemas(t)
	if len(schemas) != 2 {
		t.Fatalf("the node reported %d adapters, want 2 (xray and wireguard): %+v",
			len(schemas), schemas)
	}
	for _, kind := range []string{"xray", "wireguard"} {
		entry, ok := schemas[kind]
		if !ok {
			t.Fatalf("%s is missing from the Studio's adapter list; the node runs it", kind)
		}
		if !entry.Offerable {
			t.Errorf("%s is not offerable (%q), so the Studio cannot create one "+
				"even though the node's adapter is loaded", kind, entry.Reason)
		}
		if len(entry.Schema) == 0 {
			t.Errorf("%s was offered with an empty schema; the Studio would render "+
				"a form with no fields", kind)
		}
	}

	// 2. The schema served is the one the NODE's adapter published, not a copy
	// the panel was compiled with. This is the whole reason the endpoint reads
	// from the Hello report, and nothing until now has compared the two.
	if !sameJSON(t, schemas["xray"].Schema, s.xraySchema) {
		t.Errorf("the xray schema served by the panel is not the one the node's "+
			"adapter publishes.\n served: %s\n adapter: %s",
			schemas["xray"].Schema, s.xraySchema)
	}
	if !sameJSON(t, schemas["wireguard"].Schema, s.wgSchema) {
		t.Errorf("the wireguard schema served by the panel is not the one the "+
			"node's adapter publishes.\n served: %s\n adapter: %s",
			schemas["wireguard"].Schema, s.wgSchema)
	}

	// 3. Create the inbound the way the Studio does: same endpoint, same body
	// shape, params satisfying the schema the node just published.
	body := fmt.Sprintf(
		`{"adapter_kind":"xray","params":{"protocol":"vless","port":%d,"listen":"127.0.0.1","network":"tcp","sniffing":true}}`,
		s.port)
	var created struct {
		ID int64 `json:"id"`
	}
	if code := s.apiJSON("POST",
		fmt.Sprintf("/api/v1/nodes/%d/services", s.nodeID), body, &created); code != http.StatusCreated {
		t.Fatalf("the Studio's create call was refused (%d) for params that "+
			"satisfy the node's own schema", code)
	}
	if created.ID == 0 {
		t.Fatal("the create call returned no service id")
	}

	// 4. Give the inbound a subject, so the config the adapter renders carries a
	// real client. An inbound with nobody on it is not what an operator builds
	// in the Studio, and it would let this test pass against a config xray
	// accepts only because it is empty.
	if code := s.apiJSON("POST", "/api/v1/subjects",
		fmt.Sprintf(`{"name":"studio-customer","service_ids":[%d]}`, created.ID),
		nil); code != http.StatusCreated {
		t.Fatalf("assigning a subject to the new inbound failed (%d)", code)
	}

	// 5. The desired document must actually reach the node and converge. This
	// is the reconciler running the REAL adapter: Observe, Plan, Apply, and a
	// real xray process started by the unit.
	s.waitForConverged(120 * time.Second)

	// 6. THE assertion the whole file exists for. Everything above proves the
	// control plane agreed with itself; this asks xray.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := s.rt.ValidateConfig(ctx, s.confDir); err != nil {
		t.Fatalf("the real xray binary REJECTED the config produced from an "+
			"inbound created through the Studio: %v\n%s",
			err, studioConfDump(t, s.confDir))
	}

	// The port the operator typed into the Studio is the port that ended up in
	// the config. A form that silently drops a field would otherwise converge
	// happily onto the adapter's default.
	if dump := studioConfDump(t, s.confDir); !strings.Contains(dump, fmt.Sprint(s.port)) {
		t.Errorf("the port submitted through the Studio (%d) is not in the "+
			"generated config:\n%s", s.port, dump)
	}

	// 7. And the Studio can read back what it created.
	services := s.listServices(t)
	if len(services) != 1 {
		t.Fatalf("the Studio lists %d services after creating one: %+v",
			len(services), services)
	}
	if services[0].AdapterKind != "xray" {
		t.Errorf("service kind = %q, want xray", services[0].AdapterKind)
	}
	var params map[string]any
	if err := json.Unmarshal(services[0].Params, &params); err != nil {
		t.Fatalf("params are not JSON: %v", err)
	}
	// The Studio loads these straight back into the form, so a field reshaped
	// in transit is one the operator silently loses on the next edit.
	if params["protocol"] != "vless" {
		t.Errorf("params came back as %s, want the document that was submitted",
			services[0].Params)
	}
}

// The user-facing rule for Phase E was that the Studio must not offer a
// protocol the real node cannot execute. Everything asserting that so far has
// done it against a synthesised Hello.
func TestRealRuntimeStudioRefusesWhatTheRealNodeCannotRun(t *testing.T) {
	s := startStudioNode(t)

	// sing-box is a real adapter in this build, and this node is not running
	// it. The panel must refuse on that basis rather than on its own build.
	res := s.api("POST", fmt.Sprintf("/api/v1/nodes/%d/services", s.nodeID),
		`{"adapter_kind":"singbox","params":{}}`)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("creating a singbox service on a node running xray+wireguard "+
			"= %d, want 422; it could never be applied", res.StatusCode)
	}

	// Params the node's REAL schema forbids: 70000 is past the maximum, and
	// the panel is checking against the document xray's adapter published.
	const bad = `{"adapter_kind":"xray","params":{"protocol":"vless","port":70000}}`
	if code := s.apiJSON("POST",
		fmt.Sprintf("/api/v1/nodes/%d/services", s.nodeID), bad, nil); code != http.StatusUnprocessableEntity {
		t.Errorf("params violating the real adapter's schema were accepted (%d)", code)
	}

	// An enum value xray does not implement. The Studio renders this field as a
	// select built from the schema, so this is what a JSON-mode submission does.
	worse := fmt.Sprintf(
		`{"adapter_kind":"xray","params":{"protocol":"shadowsocks","port":%d}}`, s.port)
	if code := s.apiJSON("POST",
		fmt.Sprintf("/api/v1/nodes/%d/services", s.nodeID), worse, nil); code != http.StatusUnprocessableEntity {
		t.Errorf("a protocol outside the adapter's enum was accepted (%d); xray "+
			"would fail to start and take the node's other inbounds with it", code)
	}

	// Nothing was stored. A refusal that nonetheless wrote a row would surface
	// later as a desired document the node cannot apply.
	if services := s.listServices(t); len(services) != 0 {
		t.Errorf("a refused submission was stored anyway: %+v", services)
	}
	// The node is still healthy. Deliberately not waitForConverged: no change
	// was ever committed here, so there is no revision to converge onto and
	// waiting for one would hang rather than assert anything.
	if got := s.nodeStatus(); got != "online" {
		t.Errorf("node status = %q after three refused submissions, want online", got)
	}
}

// studioConfDump returns what the adapter wrote, for a failure message. A
// rejection is only actionable next to the config that was rejected.
func studioConfDump(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "(config dir unreadable: " + err.Error() + ")"
	}
	out := "--- generated config ---\n"
	for _, en := range entries {
		if en.IsDir() {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, en.Name()))
		if err != nil {
			continue
		}
		out += en.Name() + ":\n" + string(body) + "\n"
	}
	return out
}
