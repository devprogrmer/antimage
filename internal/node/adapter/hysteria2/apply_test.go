package hysteria2

import (
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

// fakeRuntime records what the adapter asked the host to do, in order.
//
// The order carries the meaning here: "write the config, then get the process
// onto it, then record it" is what makes a failed start recoverable, and a
// reordering shows up as a node reporting converged while serving something
// else.
type fakeRuntime struct {
	mu      sync.Mutex
	calls   []string
	running bool

	startErr   error
	stopErr    error
	restartErr error
	statusErr  error
}

func newFakeRuntime() *fakeRuntime { return &fakeRuntime{} }

func (f *fakeRuntime) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeRuntime) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeRuntime) Available(context.Context) error { return nil }

func (f *fakeRuntime) ServerStart(_ context.Context, _ string) error {
	f.record("start")
	if f.startErr != nil {
		return f.startErr
	}
	f.running = true
	return nil
}

func (f *fakeRuntime) ServerStop(_ context.Context, _ string) error {
	f.record("stop")
	if f.stopErr != nil {
		return f.stopErr
	}
	f.running = false
	return nil
}

func (f *fakeRuntime) ServerRestart(_ context.Context, _ string) error {
	f.record("restart")
	if f.restartErr != nil {
		return f.restartErr
	}
	f.running = true
	return nil
}

func (f *fakeRuntime) ServerStatus(_ context.Context, _ string) (bool, error) {
	f.record("status")
	if f.statusErr != nil {
		return false, f.statusErr
	}
	return f.running, nil
}

type applyEnv struct {
	a   *Adapter
	rt  *fakeRuntime
	dir string
}

func newApplyEnv(t *testing.T) *applyEnv {
	t.Helper()
	rt := newFakeRuntime()
	cfgDir := t.TempDir()
	return &applyEnv{a: New(rt, cfgDir, t.TempDir()), rt: rt, dir: cfgDir}
}

const testParams = `{"port":8443,"password":"supersecret","cert_file":"/tmp/c.pem","key_file":"/tmp/k.pem"}`

func desiredWith(t *testing.T, users int) adapter.Desired {
	t.Helper()
	d := adapter.Desired{
		SchemaVersion: 1, Revision: 1, NodeID: 1,
		Services: []adapter.Service{
			{ID: 10, Kind: "hysteria2", Enabled: true, Params: json.RawMessage(testParams)},
		},
	}
	for i := 1; i <= users; i++ {
		d.Subjects = append(d.Subjects, adapter.Subject{
			ID:          int64(i),
			Credentials: []adapter.Credential{{Kind: "password", Value: "pw-secret-" + string(rune('a'+i))}},
		})
	}
	return d
}

func payloadFor(t *testing.T, a *Adapter, d adapter.Desired) stepPayload {
	t.Helper()
	raw, err := a.buildPayload(d.Services[0], d.Subjects)
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	var p stepPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return p
}

func mustJSON(t *testing.T, p stepPayload) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return body
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// THE convergence guarantee.
//
// needsUpdate compared observed.Checksum -- read out of the config's marker,
// which covers the body alone -- against a hash of the whole rendered string,
// marker included. Those can never be equal, so a service that was exactly
// right asked for a restart on every pass, forever. It was harmless only while
// Apply refused to perform one, which is precisely what this phase changed.
func TestConvergedServicePlansNothing(t *testing.T) {
	e := newApplyEnv(t)
	d := desiredWith(t, 2)
	p := payloadFor(t, e.a, d)

	if err := e.a.recordApplied(d.Services[0].ID, p.Checksum, p.Users); err != nil {
		t.Fatalf("recordApplied: %v", err)
	}
	obs := adapter.Observed{Services: []adapter.ObservedService{
		{ID: d.Services[0].ID, Present: true, Managed: true, Checksum: p.Checksum},
	}}

	plan, err := e.a.Plan(context.Background(), d, obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !plan.IsEmpty() {
		t.Errorf("a converged service planned %d step(s) (%s); every reconcile "+
			"would restart the server and drop every session",
			len(plan.Steps), plan.Steps[0].Kind)
	}
}

// The marker's checksum and what Plan compares against must be the same thing.
func TestMarkerChecksumIsWhatPlanCompares(t *testing.T) {
	e := newApplyEnv(t)
	p := payloadFor(t, e.a, desiredWith(t, 1))

	_, markerChecksum, ok := parseMarker(strings.Split(p.Config, "\n")[0])
	if !ok {
		t.Fatal("rendered config has no marker")
	}
	if markerChecksum != p.Checksum {
		t.Errorf("marker says %s but the payload carries %s; Observe reads the "+
			"marker and Plan compares the payload, so these must agree",
			markerChecksum[:16], p.Checksum[:16])
	}
}

// Adding a user must be planned. The adapter previously installed once and then
// ignored the world, so a new subscriber never reached the config.
func TestAddingAUserIsPlanned(t *testing.T) {
	e := newApplyEnv(t)
	one := desiredWith(t, 1)
	p := payloadFor(t, e.a, one)
	if err := e.a.recordApplied(10, p.Checksum, p.Users); err != nil {
		t.Fatalf("recordApplied: %v", err)
	}
	obs := adapter.Observed{Services: []adapter.ObservedService{
		{ID: 10, Present: true, Managed: true, Checksum: p.Checksum},
	}}

	plan, err := e.a.Plan(context.Background(), desiredWith(t, 2), obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.IsEmpty() {
		t.Fatal("adding a user planned nothing; the new subscriber would never " +
			"be written to the config")
	}
	if plan.Steps[0].Kind != "restart" {
		t.Errorf("step kind = %q, want restart: this adapter has no hot path",
			plan.Steps[0].Kind)
	}
	if len(plan.Steps[0].Payload) == 0 {
		t.Error("step carries no payload; Apply has no access to desired state, " +
			"so it would have nothing to write")
	}
}

// Revoking a user must be planned too, or a cancelled subscriber keeps working.
func TestRevokingAUserIsPlanned(t *testing.T) {
	e := newApplyEnv(t)
	two := desiredWith(t, 2)
	p := payloadFor(t, e.a, two)
	if err := e.a.recordApplied(10, p.Checksum, p.Users); err != nil {
		t.Fatalf("recordApplied: %v", err)
	}
	obs := adapter.Observed{Services: []adapter.ObservedService{
		{ID: 10, Present: true, Managed: true, Checksum: p.Checksum},
	}}

	plan, err := e.a.Plan(context.Background(), desiredWith(t, 1), obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.IsEmpty() {
		t.Fatal("revoking a user planned nothing; they would stay connected")
	}
}

// Install on a stopped server starts it; the sidecar is written only after.
func TestInstallStartsAndThenRecords(t *testing.T) {
	e := newApplyEnv(t)
	d := desiredWith(t, 2)
	p := payloadFor(t, e.a, d)
	svcID := d.Services[0].ID

	res, err := e.a.Apply(context.Background(), adapter.Step{
		Seq: 1, Kind: "install", ServiceID: svcID,
		Disruption: adapter.DisruptRestart, Payload: mustJSON(t, p),
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !res.OK {
		t.Fatalf("install failed: %s", res.Err)
	}
	if res.Kind != "install" || res.Disruption != adapter.DisruptRestart {
		t.Errorf("result does not echo the step: kind=%q disruption=%v",
			res.Kind, res.Disruption)
	}
	if got := e.rt.Calls(); !equalStrings(got, []string{"status", "start"}) {
		t.Errorf("runtime calls = %v, want [status start]", got)
	}

	// The config really is on disk, and it is the one the payload carried.
	body, err := os.ReadFile(e.a.configPath(svcID))
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if string(body) != p.Config {
		t.Error("the config on disk is not the one the step carried")
	}

	applied := e.a.applied(svcID)
	if applied.Checksum != p.Checksum {
		t.Errorf("applied checksum = %q, want %q", applied.Checksum, p.Checksum)
	}
	if len(applied.Users) != 2 {
		t.Errorf("applied users = %v, want 2", applied.Users)
	}
}

// A server already running must be RESTARTED, not started again. Hysteria2 has
// no hot reload, so starting over a live process either fails or leaves the old
// config in memory -- converged on disk and wrong in memory, which is the state
// the sidecar exists to catch.
func TestInstallRestartsWhenAlreadyRunning(t *testing.T) {
	e := newApplyEnv(t)
	e.rt.running = true
	d := desiredWith(t, 1)
	p := payloadFor(t, e.a, d)

	res, _ := e.a.Apply(context.Background(), adapter.Step{
		Kind: "restart", ServiceID: d.Services[0].ID, Payload: mustJSON(t, p),
	})
	if !res.OK {
		t.Fatalf("restart failed: %s", res.Err)
	}
	if got := e.rt.Calls(); !equalStrings(got, []string{"status", "restart"}) {
		t.Errorf("runtime calls = %v, want [status restart]", got)
	}
}

// An unreadable status must restart rather than assume the server is down.
// Restart is the operation that ends with the process on this config whatever
// it was doing before; a start would fail against a live process and leave the
// node stuck.
func TestUnknownStatusRestarts(t *testing.T) {
	e := newApplyEnv(t)
	e.rt.statusErr = errors.New("systemctl: connection refused")
	d := desiredWith(t, 1)
	p := payloadFor(t, e.a, d)

	res, _ := e.a.Apply(context.Background(), adapter.Step{
		Kind: "install", ServiceID: d.Services[0].ID, Payload: mustJSON(t, p),
	})
	if !res.OK {
		t.Fatalf("install failed: %s", res.Err)
	}
	if got := e.rt.Calls(); !equalStrings(got, []string{"status", "restart"}) {
		t.Errorf("runtime calls = %v, want [status restart]", got)
	}
}

// A failed start must NOT record applied state, or the node reports converged
// while serving nothing.
func TestFailedStartDoesNotRecordApplied(t *testing.T) {
	e := newApplyEnv(t)
	e.rt.startErr = errors.New("hysteria: bind: address already in use")
	d := desiredWith(t, 1)
	p := payloadFor(t, e.a, d)
	svcID := d.Services[0].ID

	res, _ := e.a.Apply(context.Background(), adapter.Step{
		Kind: "install", ServiceID: svcID, Payload: mustJSON(t, p),
	})
	if res.OK {
		t.Fatal("install reported success after the server failed to start")
	}
	if !strings.Contains(res.Err, "address already in use") {
		t.Errorf("error does not carry the runtime's reason: %q", res.Err)
	}
	if got := e.a.applied(svcID); got.Checksum != "" {
		t.Errorf("applied state recorded despite a failed start: %+v", got)
	}
}

// A step with no rendered config must be refused. Writing an empty file would
// take every user off the service on the next start.
func TestInstallRefusesAnEmptyConfig(t *testing.T) {
	e := newApplyEnv(t)
	res, _ := e.a.Apply(context.Background(), adapter.Step{
		Kind: "install", ServiceID: 10, Payload: mustJSON(t, stepPayload{Checksum: "x"}),
	})
	if res.OK {
		t.Fatal("install accepted a step carrying no config")
	}
	if len(e.rt.Calls()) != 0 {
		t.Errorf("runtime was touched despite an empty config: %v", e.rt.Calls())
	}
}

// Retrying a step must converge rather than fail. The reconciler retries after
// a partial failure.
func TestApplyIsIdempotent(t *testing.T) {
	e := newApplyEnv(t)
	d := desiredWith(t, 2)
	p := payloadFor(t, e.a, d)
	svcID := d.Services[0].ID
	step := adapter.Step{Kind: "install", ServiceID: svcID, Payload: mustJSON(t, p)}

	for i := 0; i < 3; i++ {
		res, _ := e.a.Apply(context.Background(), step)
		if !res.OK {
			t.Fatalf("apply %d failed: %s", i, res.Err)
		}
	}
	if got := e.a.applied(svcID); got.Checksum != p.Checksum {
		t.Errorf("applied checksum drifted across retries: %q", got.Checksum)
	}
}

// Remove stops the server and deletes both the config and the sidecar. A
// surviving sidecar would make a later reinstall look already-applied.
func TestRemoveStopsAndCleansUp(t *testing.T) {
	e := newApplyEnv(t)
	d := desiredWith(t, 1)
	p := payloadFor(t, e.a, d)
	svcID := d.Services[0].ID

	if res, _ := e.a.Apply(context.Background(), adapter.Step{
		Kind: "install", ServiceID: svcID, Payload: mustJSON(t, p),
	}); !res.OK {
		t.Fatalf("install: %s", res.Err)
	}
	res, _ := e.a.Apply(context.Background(), adapter.Step{Kind: "remove", ServiceID: svcID})
	if !res.OK {
		t.Fatalf("remove failed: %s", res.Err)
	}
	if _, err := os.Stat(e.a.configPath(svcID)); !os.IsNotExist(err) {
		t.Error("config survived removal")
	}
	if got := e.a.applied(svcID); got.Checksum != "" {
		t.Errorf("applied state survived removal: %+v", got)
	}
}

// Unknown step kinds are refused rather than silently succeeding.
func TestUnknownStepKindIsRefused(t *testing.T) {
	e := newApplyEnv(t)
	res, _ := e.a.Apply(context.Background(), adapter.Step{Kind: "teleport", ServiceID: 1})
	if res.OK {
		t.Fatal("an unknown step kind reported success")
	}
	if !strings.Contains(res.Err, "teleport") {
		t.Errorf("error does not name the unknown kind: %q", res.Err)
	}
}

// Observe must see every service the adapter installed, whatever its id.
func TestObserveSeesEveryServiceItWrote(t *testing.T) {
	e := newApplyEnv(t)
	ctx := context.Background()

	want := map[int64]string{}
	for _, id := range []int64{7, 42} {
		d := desiredWith(t, 1)
		d.Services[0].ID = id
		p := payloadFor(t, e.a, d)
		want[id] = p.Checksum
		if res, _ := e.a.Apply(ctx, adapter.Step{
			Kind: "install", ServiceID: id, Payload: mustJSON(t, p),
		}); !res.OK {
			t.Fatalf("install %d: %s", id, res.Err)
		}
	}

	obs, err := e.a.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(obs.Services) != len(want) {
		t.Fatalf("Observe saw %d services, want %d", len(obs.Services), len(want))
	}
	for _, svc := range obs.Services {
		expect, ok := want[svc.ID]
		if !ok {
			t.Errorf("Observe reported service %d, never written", svc.ID)
			continue
		}
		if !svc.Managed {
			t.Errorf("service %d reported unmanaged; the adapter wrote it", svc.ID)
		}
		if svc.Checksum != expect {
			t.Errorf("service %d checksum = %q, want %q", svc.ID, svc.Checksum, expect)
		}
	}
}

// The config holds every subscriber's password, so it must not be readable by
// other users on the host.
func TestWrittenConfigIsNotWorldReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Go maps Chmod onto the read-only attribute on Windows; POSIX mode
		// bits are not enforced there. The property is real on the platform
		// nodes actually run on, so assert it there rather than pretending.
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	e := newApplyEnv(t)
	path := filepath.Join(e.dir, "h2-perm.yaml")
	if err := e.a.writeConfig(path, "# antimage: service=1 checksum=x\nbody\n"); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("config mode = %04o; every subscriber password on this node is "+
			"readable by other local users", perm)
	}
}

// Writing must replace, not append, and must leave no temp file behind.
func TestWriteConfigReplacesAtomically(t *testing.T) {
	e := newApplyEnv(t)
	path := filepath.Join(e.dir, "h2-atomic.yaml")
	if err := e.a.writeConfig(path, "first"); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	if err := e.a.writeConfig(path, "second"); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(body) != "second" {
		t.Errorf("config = %q, want %q", body, "second")
	}
	entries, err := os.ReadDir(e.dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".antimage-") {
			t.Errorf("temp file %q left behind", entry.Name())
		}
	}
}

// A service belonging to another adapter must be left alone.
//
// Same defect and same reasoning as the WireGuard adapter: a node runs several
// adapters over one desired document, each handling only its own kind. Without
// the filter this adapter reads every service, and one whose params happen to
// satisfy the Hysteria2 schema is written out as a Hysteria2 server. Foreign
// params usually fail validation, which hid it -- a coincidence, not a rule.
func TestForeignServicesAreNotPlanned(t *testing.T) {
	e := newApplyEnv(t)

	d := desiredWith(t, 1)
	ours := d.Services[0].ID
	d.Services = append(d.Services, adapter.Service{
		ID: 77, Kind: "wireguard", Enabled: true,
		// Deliberately shaped so Hysteria2's own schema accepts it.
		Params: json.RawMessage(testParams),
	})

	plan, err := e.a.Plan(context.Background(), d, adapter.Observed{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, step := range plan.Steps {
		if step.ServiceID != ours {
			t.Errorf("planned %q for service %d, which belongs to another adapter",
				step.Kind, step.ServiceID)
		}
	}
	if len(plan.Steps) != 1 {
		t.Errorf("planned %d steps for one Hysteria2 service", len(plan.Steps))
	}
}

// A foreign service sharing an id must not keep our service alive through the
// removal pass.
func TestForeignServicesDoNotAffectRemoval(t *testing.T) {
	e := newApplyEnv(t)

	d := desiredWith(t, 1)
	d.Services = []adapter.Service{{
		ID: 10, Kind: "wireguard", Enabled: true, Params: json.RawMessage(testParams),
	}}
	obs := adapter.Observed{Services: []adapter.ObservedService{
		{ID: 10, Present: true, Managed: true, Checksum: "whatever"},
	}}

	plan, err := e.a.Plan(context.Background(), d, obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Steps) != 1 || plan.Steps[0].Kind != "remove" {
		t.Fatalf("want a single remove step, got %+v", plan.Steps)
	}
}

// A node with no Hysteria2 services configured has nothing to restart. This
// must not silently succeed: reporting ok=true here would tell an operator
// their restart worked when nothing was running to begin with.
func TestRestart_NoServicesReturnsUnsupported(t *testing.T) {
	e := newApplyEnv(t)
	err := e.a.Restart(context.Background())
	if !errors.Is(err, adapter.ErrRestartUnsupported) {
		t.Errorf("Restart with nothing configured = %v, want ErrRestartUnsupported", err)
	}
}

// Restart bounces every configured unit -- this adapter runs one systemd
// unit PER service rather than one process multiplexing every inbound (see
// the Restart doc comment), so a node running two Hysteria2 services must
// see two ServerRestart calls, not one.
func TestRestart_BouncesEveryConfiguredService(t *testing.T) {
	e := newApplyEnv(t)
	d := desiredWith(t, 1)
	d.Services = append(d.Services, adapter.Service{
		ID: 11, Kind: "hysteria2", Enabled: true, Params: json.RawMessage(testParams),
	})

	for _, svc := range d.Services {
		p := payloadFor(t, e.a, adapter.Desired{Services: []adapter.Service{svc}, Subjects: d.Subjects})
		if _, err := e.a.Apply(context.Background(), adapter.Step{
			Seq: 1, Kind: "install", ServiceID: svc.ID,
			Disruption: adapter.DisruptRestart, Payload: mustJSON(t, p),
		}); err != nil {
			t.Fatalf("install service %d: %v", svc.ID, err)
		}
	}
	e.rt.mu.Lock()
	e.rt.calls = nil
	e.rt.mu.Unlock()

	if err := e.a.Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	calls := e.rt.Calls()
	restarts := 0
	for _, c := range calls {
		if c == "restart" {
			restarts++
		}
	}
	if restarts != 2 {
		t.Errorf("restart calls = %d (calls=%v), want 2 -- one per configured service", restarts, calls)
	}
}
