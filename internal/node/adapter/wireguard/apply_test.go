package wireguard

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
// The order is most of what these tests are checking. "Write the config, then
// bring the interface up, then record it" is not a style preference: each step
// is what makes the next one's failure recoverable, and a reordering shows up
// as a node that reports converged while serving something else.
type fakeRuntime struct {
	mu    sync.Mutex
	calls []string

	exists, up bool

	upErr     error
	downErr   error
	statusErr error
	syncErr   error
	syncOK    bool
}

func newFakeRuntime() *fakeRuntime { return &fakeRuntime{syncOK: true} }

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

func (f *fakeRuntime) InterfaceUp(_ context.Context, iface, _ string) error {
	f.record("up:" + iface)
	if f.upErr != nil {
		return f.upErr
	}
	f.exists, f.up = true, true
	return nil
}

func (f *fakeRuntime) InterfaceDown(_ context.Context, iface, _ string) error {
	f.record("down:" + iface)
	if f.downErr != nil {
		return f.downErr
	}
	f.up = false
	return nil
}

func (f *fakeRuntime) InterfaceStatus(_ context.Context, iface string) (bool, bool, error) {
	f.record("status:" + iface)
	if f.statusErr != nil {
		return false, false, f.statusErr
	}
	return f.exists, f.up, nil
}

func (f *fakeRuntime) ShowTransfer(context.Context, string) (map[string]PeerTransfer, error) {
	return nil, nil
}

func (f *fakeRuntime) SyncPeers(_ context.Context, iface, _ string) (bool, error) {
	f.record("sync:" + iface)
	if f.syncErr != nil {
		return false, f.syncErr
	}
	return f.syncOK, nil
}

// applyEnv points the adapter at temp directories so a test never writes to
// /etc/wireguard. That is not hygiene alone: a config left there is picked up
// by the accounting glob on the next run, against a nil runtime.
type applyEnv struct {
	a  *Adapter
	rt *fakeRuntime
	// dir is the adapter config directory, so a test can inspect what it wrote.
	dir string
}

func newApplyEnv(t *testing.T) *applyEnv {
	t.Helper()
	rt := newFakeRuntime()
	cfgDir := t.TempDir()
	return &applyEnv{a: New(rt, cfgDir, t.TempDir()), rt: rt, dir: cfgDir}
}

// payloadFor renders the step payload Plan would produce for a desired state.
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

// The marker's checksum and what Plan compares against must be the same thing.
// This is the defect stated as an equality, so it fails loudly if either side
// changes domain again.
func TestMarkerChecksumIsWhatPlanCompares(t *testing.T) {
	a := New(newFakeRuntime(), t.TempDir(), t.TempDir())
	d := desiredWith(t, 1)
	p := payloadFor(t, a, d)

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

// Observe must read the file it is iterating over.
//
// It used to build the path from configPath(0) and ignore the directory entry,
// so it re-read antimage-0.conf once per file in the directory. Every service
// whose id was not 0 was therefore invisible: Plan saw it as missing and
// planned an install on every pass, which -- once Apply works -- means tearing
// down a healthy interface and rebuilding it forever.
//
// Round-tripping through Apply is what makes this a real test rather than a
// restatement of the fix: the files are the ones the adapter itself wrote.
func TestObserveSeesEveryServiceItWrote(t *testing.T) {
	e := newApplyEnv(t)
	ctx := context.Background()

	// Two services with ids that are deliberately not zero.
	want := map[int64]string{}
	for _, id := range []int64{7, 42} {
		d := desiredWith(t, 1)
		d.Services[0].ID = id
		p := payloadFor(t, e.a, d)
		want[id] = p.Checksum
		res, _ := e.a.Apply(ctx, adapter.Step{Kind: "install", ServiceID: id, Payload: mustJSON(t, p)})
		if !res.OK {
			t.Fatalf("install %d: %s", id, res.Err)
		}
	}

	obs, err := e.a.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(obs.Services) != len(want) {
		t.Fatalf("Observe saw %d services, want %d: %+v", len(obs.Services), len(want), obs.Services)
	}
	for _, svc := range obs.Services {
		expect, ok := want[svc.ID]
		if !ok {
			t.Errorf("Observe reported service %d, which was never written", svc.ID)
			continue
		}
		if !svc.Managed {
			t.Errorf("service %d reported unmanaged; the adapter wrote this file "+
				"itself, so its marker must verify", svc.ID)
		}
		if svc.Checksum != expect {
			t.Errorf("service %d checksum = %q, want %q", svc.ID, svc.Checksum, expect)
		}
	}
}

// A hand-edited config must be reported unmanaged, so Plan restores it rather
// than trusting it. This is the drift check the marker exists for.
func TestObserveReportsHandEditsAsUnmanaged(t *testing.T) {
	e := newApplyEnv(t)
	ctx := context.Background()
	d := desiredWith(t, 1)
	svcID := d.Services[0].ID
	p := payloadFor(t, e.a, d)
	if res, _ := e.a.Apply(ctx, adapter.Step{
		Kind: "install", ServiceID: svcID, Payload: mustJSON(t, p),
	}); !res.OK {
		t.Fatalf("install: %s", res.Err)
	}

	// Append a line, leaving the marker's checksum stale.
	path := e.a.configPath(svcID)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := os.WriteFile(path, append(body, []byte("\n# tampered\n")...), 0o600); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	obs, err := e.a.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(obs.Services) != 1 {
		t.Fatalf("Observe saw %d services, want 1", len(obs.Services))
	}
	if obs.Services[0].Managed {
		t.Error("a hand-edited config was reported as managed; the edit would " +
			"survive every reconcile")
	}
}

// Install writes the config, brings the interface up, and only then records
// applied state. The order is what makes a failed bring-up recoverable.
func TestInstallWritesThenBringsUpThenRecords(t *testing.T) {
	e := newApplyEnv(t)
	d := desiredWith(t, 2)
	p := payloadFor(t, e.a, d)
	svcID := d.Services[0].ID

	res, err := e.a.Apply(context.Background(), adapter.Step{
		Seq: 1, Kind: "install", ServiceID: svcID,
		Disruption: adapter.DisruptRestart,
		Payload:    mustJSON(t, p),
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !res.OK {
		t.Fatalf("install failed: %s", res.Err)
	}
	// Echoed back so an operator can read what was done and what it cost.
	if res.Kind != "install" || res.Disruption != adapter.DisruptRestart {
		t.Errorf("result does not echo the step: kind=%q disruption=%v",
			res.Kind, res.Disruption)
	}

	iface := interfaceName(svcID)
	want := []string{"status:" + iface, "up:" + iface}
	if got := e.rt.Calls(); !equalStrings(got, want) {
		t.Errorf("runtime calls = %v, want %v", got, want)
	}

	// The interface came up, so the sidecar records what it is serving.
	applied := e.a.applied(svcID)
	if applied.Checksum != p.Checksum {
		t.Errorf("applied checksum = %q, want %q", applied.Checksum, p.Checksum)
	}
	if len(applied.Peers) != 2 {
		t.Errorf("applied peers = %v, want 2", applied.Peers)
	}
	if applied.Shape == "" {
		t.Error("applied state has no shape; every later peer addition would " +
			"restart instead of hot-syncing")
	}
}

// A failed bring-up must NOT record applied state. The sidecar is the adapter's
// answer to "did the runtime ever load this?", and a false yes there is a node
// that reports converged while serving nothing.
func TestFailedBringUpDoesNotRecordApplied(t *testing.T) {
	e := newApplyEnv(t)
	e.rt.upErr = errors.New("wg-quick: RTNETLINK answers: operation not permitted")
	d := desiredWith(t, 1)
	p := payloadFor(t, e.a, d)
	svcID := d.Services[0].ID

	res, _ := e.a.Apply(context.Background(), adapter.Step{
		Kind: "install", ServiceID: svcID, Payload: mustJSON(t, p),
	})
	if res.OK {
		t.Fatal("install reported success after wg-quick failed")
	}
	if !strings.Contains(res.Err, "operation not permitted") {
		t.Errorf("error does not carry the runtime's reason: %q", res.Err)
	}
	if got := e.a.applied(svcID); got.Checksum != "" {
		t.Errorf("applied state recorded despite a failed bring-up: %+v", got)
	}
}

// A step with no rendered config must be refused, not written. An empty config
// would take every peer off the interface on the next bring-up.
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

// Restart on a live interface must bring it down first: wg-quick up against an
// interface that is already up fails, so a retry could never make progress.
func TestRestartTakesTheInterfaceDownFirst(t *testing.T) {
	e := newApplyEnv(t)
	e.rt.exists, e.rt.up = true, true
	d := desiredWith(t, 1)
	p := payloadFor(t, e.a, d)
	svcID := d.Services[0].ID
	iface := interfaceName(svcID)

	res, _ := e.a.Apply(context.Background(), adapter.Step{
		Kind: "restart", ServiceID: svcID, Payload: mustJSON(t, p),
	})
	if !res.OK {
		t.Fatalf("restart failed: %s", res.Err)
	}
	want := []string{"status:" + iface, "down:" + iface, "up:" + iface}
	if got := e.rt.Calls(); !equalStrings(got, want) {
		t.Errorf("runtime calls = %v, want %v", got, want)
	}
}

// Reload applies a peer change without touching the interface. This is the
// whole point of having a non-disruptive path: adding one user must not
// disconnect everyone already connected.
func TestReloadSyncsPeersWithoutRestarting(t *testing.T) {
	e := newApplyEnv(t)
	e.rt.exists, e.rt.up = true, true
	d := desiredWith(t, 3)
	p := payloadFor(t, e.a, d)
	svcID := d.Services[0].ID
	iface := interfaceName(svcID)

	res, _ := e.a.Apply(context.Background(), adapter.Step{
		Kind: "reload", ServiceID: svcID, Disruption: adapter.DisruptNone,
		Payload: mustJSON(t, p),
	})
	if !res.OK {
		t.Fatalf("reload failed: %s", res.Err)
	}
	if got := e.rt.Calls(); !equalStrings(got, []string{"sync:" + iface}) {
		t.Errorf("runtime calls = %v; a reload must not bring the interface "+
			"down or up", got)
	}
	if got := e.a.applied(svcID); len(got.Peers) != 3 {
		t.Errorf("applied peers = %v, want 3", got.Peers)
	}
}

// A hot sync the runtime could not perform must FAIL the step rather than
// report success. Reporting success would leave the interface serving the old
// peer set while the sidecar claims otherwise -- so a revoked peer would keep
// its session and nothing would ever notice.
func TestReloadFailsWhenHotSyncIsRefused(t *testing.T) {
	e := newApplyEnv(t)
	e.rt.exists, e.rt.up = true, true
	e.rt.syncOK = false
	d := desiredWith(t, 2)
	p := payloadFor(t, e.a, d)
	svcID := d.Services[0].ID

	res, _ := e.a.Apply(context.Background(), adapter.Step{
		Kind: "reload", ServiceID: svcID, Payload: mustJSON(t, p),
	})
	if res.OK {
		t.Fatal("reload reported success when the runtime refused to hot-sync; " +
			"a revoked peer would keep its session")
	}
	if got := e.a.applied(svcID); got.Checksum != "" {
		t.Errorf("applied state recorded after a refused sync: %+v", got)
	}
}

// Applying the same step twice must be safe. The reconciler retries a step
// after a partial failure, so a second install has to converge rather than
// fail on an interface that is already up.
func TestInstallIsIdempotent(t *testing.T) {
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

// Unknown step kinds are refused rather than silently succeeding, so a step
// this adapter does not understand cannot be recorded as done.
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

// The config carries the interface's private key, so it must never be readable
// by anyone but root. Checked on the real file the adapter wrote.
func TestWrittenConfigIsNotWorldReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Go maps Chmod onto the read-only attribute on Windows; POSIX mode
		// bits are not enforced there. The property is real on the platform
		// nodes actually run on, so assert it there rather than pretending.
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	e := newApplyEnv(t)
	path := filepath.Join(e.dir, "wg-perm.conf")
	if err := e.a.writeConfig(path, "# antimage: service=1 checksum=x\nbody\n"); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("config mode = %04o; the interface private key is readable by "+
			"other users on the host", perm)
	}
}

// Writing must not leave a temp file behind, and must replace the previous
// contents entirely rather than appending to them.
func TestWriteConfigReplacesAtomically(t *testing.T) {
	e := newApplyEnv(t)
	path := filepath.Join(e.dir, "wg-atomic.conf")
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

func mustJSON(t *testing.T, p stepPayload) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
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
