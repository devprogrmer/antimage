package singbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/amyrm/antimage/internal/node/adapter"
)

type fakeRuntime struct {
	mu        sync.Mutex
	restarts  int
	available error
	healthy   bool
	detail    string
	failRst   error
}

func newFakeRuntime() *fakeRuntime { return &fakeRuntime{healthy: true, detail: "active"} }

func (f *fakeRuntime) Available(context.Context) error { return f.available }

func (f *fakeRuntime) Restart(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failRst != nil {
		return f.failRst
	}
	f.restarts++
	return nil
}

func (f *fakeRuntime) Healthy(context.Context) (bool, string) { return f.healthy, f.detail }

func (f *fakeRuntime) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.restarts
}

func newAdapter(t *testing.T) (*Adapter, *fakeRuntime, string) {
	t.Helper()
	dir := t.TempDir()
	rt := newFakeRuntime()
	return New(dir, rt), rt, dir
}

const tlsParams = `{"protocol":"vless","port":443,"tls":true,"cert_file":"/c","key_file":"/k"}`

func desiredWith(users int, svcID int64, params string) adapter.Desired {
	d := adapter.Desired{
		SchemaVersion: 1, Revision: 1, NodeID: 1,
		Services: []adapter.Service{
			{ID: svcID, Kind: "singbox", Enabled: true, Params: json.RawMessage(params)},
		},
	}
	for i := 1; i <= users; i++ {
		d.Subjects = append(d.Subjects, adapter.Subject{
			ID: int64(i),
			Credentials: []adapter.Credential{
				{Kind: "uuid", Value: "11111111-2222-3333-4444-55555555555" + string(rune('0'+i))},
			},
		})
	}
	return d
}

func converge(t *testing.T, a *Adapter, d adapter.Desired) adapter.Plan {
	t.Helper()
	ctx := context.Background()
	obs, err := a.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	plan, err := a.Plan(ctx, d, obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, step := range plan.Steps {
		if _, err := a.Apply(ctx, step); err != nil {
			t.Fatalf("Apply step %d: %v", step.Seq, err)
		}
	}
	return plan
}

// The capability must be FALSE and must be honest. Declaring hot add on a
// backend that cannot do it would let the panel promise an operator a
// non-disruptive change and then drop every session on the node.
func TestDescriptorDeclaresNoHotUserAdd(t *testing.T) {
	a, _, _ := newAdapter(t)
	d := a.Descriptor()

	if d.Caps.HotUserAdd {
		t.Error("sing-box declared HotUserAdd=true; it has no management API for user mutation")
	}
	if d.Kind != Kind {
		t.Errorf("kind = %q, want %q", d.Kind, Kind)
	}
	var schema map[string]any
	if err := json.Unmarshal(d.Caps.ServiceSchema, &schema); err != nil {
		t.Fatalf("service schema is not valid JSON: %v", err)
	}
	if schema["additionalProperties"] != false {
		t.Error("service schema accepts unknown properties")
	}
}

// Every user change is restart-class, and that classification must be visible
// in the plan so the reconciler's maintenance window applies.
func TestEveryUserChangeIsRestartClass(t *testing.T) {
	a, rt, _ := newAdapter(t)
	converge(t, a, desiredWith(1, 10, tlsParams))
	afterCreate := rt.count()

	plan := converge(t, a, desiredWith(2, 10, tlsParams))
	if plan.MaxDisruption() != adapter.DisruptRestart {
		t.Errorf("adding a user classified as %v, want restart", plan.MaxDisruption())
	}
	if rt.count() <= afterCreate {
		t.Error("a restart-class change did not restart the process")
	}
}

func TestConvergesAndThenPlansNothing(t *testing.T) {
	a, _, _ := newAdapter(t)
	d := desiredWith(2, 10, tlsParams)

	if len(converge(t, a, d).Steps) == 0 {
		t.Fatal("first convergence planned nothing")
	}
	obs, _ := a.Observe(context.Background())
	again, err := a.Plan(context.Background(), d, obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !again.IsEmpty() {
		t.Fatalf("converged state still plans %d step(s)", len(again.Steps))
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	a, _, dir := newAdapter(t)
	d := desiredWith(1, 10, tlsParams)
	plan := converge(t, a, d)

	before, err := os.ReadFile(filepath.Join(dir, "antimage-10.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for i := 0; i < 3; i++ {
		for _, step := range plan.Steps {
			if _, err := a.Apply(context.Background(), step); err != nil {
				t.Fatalf("re-apply: %v", err)
			}
		}
	}
	after, _ := os.ReadFile(filepath.Join(dir, "antimage-10.json"))
	if !bytes.Equal(before, after) {
		t.Error("repeated Apply changed the config")
	}
	obs, _ := a.Observe(context.Background())
	if again, _ := a.Plan(context.Background(), d, obs); !again.IsEmpty() {
		t.Errorf("not converged after repeated Apply: %+v", again.Steps)
	}
}

func TestPlanIsPureAndRepeatable(t *testing.T) {
	a, rt, _ := newAdapter(t)
	d := desiredWith(3, 10, tlsParams)
	obs, _ := a.Observe(context.Background())

	first, err := a.Plan(context.Background(), d, obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := a.Plan(context.Background(), d, obs)
		if err != nil {
			t.Fatalf("Plan %d: %v", i, err)
		}
		if len(again.Steps) != len(first.Steps) {
			t.Fatalf("plan %d differs in length", i)
		}
		for j := range again.Steps {
			if string(again.Steps[j].Payload) != string(first.Steps[j].Payload) {
				t.Fatalf("plan %d step %d payload differs", i, j)
			}
		}
	}
	if rt.count() != 0 {
		t.Error("Plan restarted the runtime; it must be pure")
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	in := Inbound{Protocol: VLESS, Port: 443, TLS: true, CertFile: "/c", KeyFile: "/k"}
	users := []User{
		{SubjectID: 2, Name: "b", Credential: "11111111-2222-3333-4444-555555555552"},
		{SubjectID: 1, Name: "a", Credential: "11111111-2222-3333-4444-555555555551"},
	}
	first, err := in.Generate(users)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for i := 0; i < 20; i++ {
		again, err := in.Generate(users)
		if err != nil {
			t.Fatalf("Generate %d: %v", i, err)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("run %d differed", i)
		}
	}
	// Reordering the input must not change the output.
	swapped, _ := in.Generate([]User{users[1], users[0]})
	if !bytes.Equal(first, swapped) {
		t.Error("output depends on input order")
	}
}

// The generated shape must be what sing-box expects: `users` with `uuid`, and
// `listen_port` rather than Xray's `port`. Getting this wrong produces a config
// that fails to load.
func TestGenerateProducesTheSingBoxShape(t *testing.T) {
	in := Inbound{Protocol: VLESS, Port: 443, TLS: true, CertFile: "/c", KeyFile: "/k", SNI: "e.com"}
	raw, err := in.Generate([]User{{SubjectID: 1, Name: "a", Credential: "11111111-2222-3333-4444-555555555551"}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if got["type"] != "vless" {
		t.Errorf("type = %v", got["type"])
	}
	if got["listen_port"].(float64) != 443 {
		t.Errorf("listen_port = %v (sing-box does not use `port`)", got["listen_port"])
	}
	users := got["users"].([]any)
	if len(users) != 1 {
		t.Fatalf("users = %d", len(users))
	}
	u := users[0].(map[string]any)
	if u["uuid"] == nil || u["name"] == nil {
		t.Errorf("user entry = %v, want uuid and name", u)
	}
	tls := got["tls"].(map[string]any)
	if tls["enabled"] != true || tls["certificate_path"] != "/c" || tls["server_name"] != "e.com" {
		t.Errorf("tls block = %v", tls)
	}
}

// Trojan and Shadowsocks use password, not uuid.
func TestPasswordProtocolsUsePassword(t *testing.T) {
	for _, proto := range []Protocol{Trojan, Shadowsocks} {
		t.Run(string(proto), func(t *testing.T) {
			in := Inbound{Protocol: proto, Port: 443, TLS: true, CertFile: "/c", KeyFile: "/k"}
			raw, err := in.Generate([]User{{SubjectID: 1, Name: "a", Credential: "a-long-password"}})
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			var got map[string]any
			_ = json.Unmarshal(raw, &got)
			u := got["users"].([]any)[0].(map[string]any)
			if u["password"] != "a-long-password" {
				t.Errorf("user = %v, want a password", u)
			}
			if _, hasUUID := u["uuid"]; hasUUID {
				t.Errorf("%s user carries a uuid: %v", proto, u)
			}
			if in.CredentialKind() != "password" {
				t.Errorf("CredentialKind = %q", in.CredentialKind())
			}
			if proto == Shadowsocks && got["method"] != defaultShadowsocksMethod {
				t.Errorf("shadowsocks method = %v, want the default", got["method"])
			}
		})
	}
}

func TestGenerateRejectsDuplicatesAndMissingCredentials(t *testing.T) {
	in := Inbound{Protocol: VLESS, Port: 443}

	_, err := in.Generate([]User{
		{SubjectID: 1, Name: "same", Credential: "a"},
		{SubjectID: 2, Name: "same", Credential: "b"},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("duplicate names: err = %v", err)
	}

	_, err = in.Generate([]User{{SubjectID: 9, Name: "ghost"}})
	if err == nil || !strings.Contains(err.Error(), "subject 9") {
		t.Errorf("missing credential: err = %v", err)
	}
}

func TestValidateRejectsUnusableInbounds(t *testing.T) {
	for name, in := range map[string]Inbound{
		"unknown protocol":  {Protocol: "wireguard", Port: 443},
		"port zero":         {Protocol: VLESS, Port: 0},
		"listen not an ip":  {Protocol: VLESS, Port: 443, Listen: "example.com"},
		"unknown network":   {Protocol: VLESS, Port: 443, Network: "quic"},
		"tls without cert":  {Protocol: VLESS, Port: 443, TLS: true},
		"trojan plaintext":  {Protocol: Trojan, Port: 443},
		"ws without path":   {Protocol: VLESS, Port: 443, Network: WS},
		"unknown ss method": {Protocol: Shadowsocks, Port: 443, Method: "rc4"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := in.Validate(); err == nil {
				t.Fatalf("accepted an unusable inbound: %+v", in)
			} else if !errors.Is(err, ErrInvalidInbound) {
				t.Errorf("err = %v, want ErrInvalidInbound", err)
			}
		})
	}
}

func TestParseInboundRejectsUnknownFields(t *testing.T) {
	_, err := ParseInbound(json.RawMessage(`{"protocol":"vless","port":443,"backdoor":true}`))
	if err == nil || !errors.Is(err, ErrInvalidInbound) {
		t.Fatalf("err = %v, want ErrInvalidInbound", err)
	}
}

func TestHandEditIsDetectedAndCorrected(t *testing.T) {
	a, _, dir := newAdapter(t)
	d := desiredWith(1, 10, tlsParams)
	converge(t, a, d)

	path := filepath.Join(dir, "antimage-10.json")
	body, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append(body, []byte("\n")...), 0o600); err != nil {
		t.Fatalf("edit: %v", err)
	}

	obs, _ := a.Observe(context.Background())
	plan, err := a.Plan(context.Background(), d, obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.IsEmpty() {
		t.Fatal("a hand edit produced no corrective plan")
	}
	converge(t, a, d)
	fixed, _ := os.ReadFile(path)
	if !bytes.Equal(fixed, body) {
		t.Error("the hand edit was not corrected")
	}
}

// A config with no marker is somebody else's and must not be overwritten.
func TestUnmanagedFileIsRefused(t *testing.T) {
	a, _, dir := newAdapter(t)
	path := filepath.Join(dir, "antimage-10.json")
	if err := os.WriteFile(path, []byte(`{"handmade":true}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	obs, err := a.Observe(context.Background())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(obs.Services) != 1 || obs.Services[0].Managed {
		t.Fatalf("observation = %+v, want one unmanaged service", obs.Services)
	}
	if _, err := a.Plan(context.Background(), desiredWith(1, 10, tlsParams), obs); err == nil {
		t.Fatal("planned over a file antimage did not write")
	}
	body, _ := os.ReadFile(path)
	if string(body) != `{"handmade":true}` {
		t.Error("the operator's file was modified")
	}
}

func TestDisableAndRemovalCleanUpBothFiles(t *testing.T) {
	a, _, dir := newAdapter(t)
	converge(t, a, desiredWith(1, 10, tlsParams))

	off := desiredWith(1, 10, tlsParams)
	off.Services[0].Enabled = false
	converge(t, a, off)

	for _, name := range []string{"antimage-10.json", "antimage-10.json.marker"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived disabling the service", name)
		}
	}
}

func TestInvalidConfigurationFailsPlanning(t *testing.T) {
	a, rt, _ := newAdapter(t)
	obs, _ := a.Observe(context.Background())
	_, err := a.Plan(context.Background(), desiredWith(1, 10, `{"protocol":"vless","port":99999}`), obs)
	if err == nil || !errors.Is(err, ErrInvalidInbound) {
		t.Fatalf("err = %v, want ErrInvalidInbound", err)
	}
	if rt.count() != 0 {
		t.Error("an invalid config reached the runtime")
	}
}

func TestFailedRestartSurfacesAndRecovers(t *testing.T) {
	a, rt, _ := newAdapter(t)
	rt.failRst = errors.New("systemctl: unit not found")
	d := desiredWith(1, 10, tlsParams)

	ctx := context.Background()
	obs, _ := a.Observe(ctx)
	plan, _ := a.Plan(ctx, d, obs)

	var failed bool
	for _, step := range plan.Steps {
		res, err := a.Apply(ctx, step)
		if err != nil {
			failed = true
			if res.OK {
				t.Error("a failed step reported OK")
			}
		}
	}
	if !failed {
		t.Fatal("a failing restart did not surface")
	}

	rt.failRst = nil
	converge(t, a, d)
	obs, _ = a.Observe(ctx)
	if again, _ := a.Plan(ctx, d, obs); !again.IsEmpty() {
		t.Errorf("did not converge after recovery: %+v", again.Steps)
	}
}

func TestProbeReportsMissingBinaryAndHealth(t *testing.T) {
	a, rt, _ := newAdapter(t)

	if h, _ := a.Probe(context.Background()); !h.OK {
		t.Errorf("healthy runtime probed as unhealthy: %+v", h)
	}

	rt.available = errors.New("sing-box not found in PATH")
	h, _ := a.Probe(context.Background())
	if h.OK || !strings.Contains(h.Detail, "not found") {
		t.Errorf("missing binary: %+v", h)
	}

	rt.available = nil
	rt.healthy, rt.detail = false, "inactive (dead)"
	h, _ = a.Probe(context.Background())
	if h.OK || !strings.Contains(h.Detail, "inactive") {
		t.Errorf("dead unit: %+v", h)
	}
}

func TestWrittenConfigIsNotWorldReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not representable on Windows; verified on Linux CI")
	}
	a, _, dir := newAdapter(t)
	converge(t, a, desiredWith(1, 10, tlsParams))

	info, err := os.Stat(filepath.Join(dir, "antimage-10.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("config mode = %04o, want no group or other access", perm)
	}
}

// The same security property the Xray adapter needs, asserted independently
// here rather than assumed from "sing-box has no hot path".
//
// sing-box cannot add or remove a user without a restart, so a revocation is
// structurally forced down the restart path -- but that is a property of the
// current implementation, not of the contract. If somebody later adds a hot
// path they must not reintroduce the hole where a revoked credential leaves
// the config file while the running process keeps serving the session.
func TestRevokingAUserActuallyReachesTheRuntime(t *testing.T) {
	a, rt, dir := newAdapter(t)

	converge(t, a, desiredWith(2, 10, tlsParams))
	restartsBefore := rt.count()

	revoked := "11111111-2222-3333-4444-555555555552"
	body, err := os.ReadFile(filepath.Join(dir, "antimage-10.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(body), revoked) {
		t.Fatal("precondition: the second user was never in the config")
	}

	plan := converge(t, a, desiredWith(1, 10, tlsParams))

	if got := plan.MaxDisruption(); got < adapter.DisruptRestart {
		t.Errorf("revocation planned as %v, want at least %v", got, adapter.DisruptRestart)
	}
	after, err := os.ReadFile(filepath.Join(dir, "antimage-10.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(after), revoked) {
		t.Error("the revoked credential is still in the config file")
	}
	if rt.count() == restartsBefore {
		t.Error("the revoked user left the config file but the process was never restarted, " +
			"so they stay connected")
	}
}

// A write that succeeds followed by a restart that fails must NOT look like
// convergence.
//
// The config file on disk is correct at that point, so a checksum comparison
// alone reports no drift and the adapter plans nothing -- while sing-box is
// still serving the previous configuration, including any user the write was
// meant to revoke. The applied sidecar is what separates "what should be
// running" from "what is running".
func TestFailedRestartDoesNotLookLikeConvergence(t *testing.T) {
	a, rt, _ := newAdapter(t)
	ctx := context.Background()
	d := desiredWith(1, 10, tlsParams)

	rt.failRst = errors.New("systemctl: job failed")
	obs, _ := a.Observe(ctx)
	plan, err := a.Plan(ctx, d, obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, s := range plan.Steps {
		_, _ = a.Apply(ctx, s) // staged to fail at the restart
	}

	rt.failRst = nil
	obs, _ = a.Observe(ctx)
	next, err := a.Plan(ctx, d, obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if next.IsEmpty() {
		t.Fatal("the adapter reported convergence after a restart that never happened; " +
			"the process is still running the old configuration")
	}
	if got := next.MaxDisruption(); got < adapter.DisruptRestart {
		t.Errorf("recovery planned as %v, want at least %v", got, adapter.DisruptRestart)
	}

	// And applying it converges for real.
	for _, s := range next.Steps {
		if _, err := a.Apply(ctx, s); err != nil {
			t.Fatalf("Apply %s: %v", s.Kind, err)
		}
	}
	if rt.count() == 0 {
		t.Error("recovery never restarted the runtime")
	}
	obs, _ = a.Observe(ctx)
	final, _ := a.Plan(ctx, d, obs)
	if !final.IsEmpty() {
		t.Errorf("did not settle after recovery: %+v", final.Steps)
	}
}
