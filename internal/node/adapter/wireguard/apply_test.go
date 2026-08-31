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

// The peer entry must carry the DERIVED public key, never the credential.
//
// buildPeerList used to write cred.Value straight into PublicKey. The keypair
// credential holds the subject's PRIVATE key -- the Descriptor says so and the
// peer registry derives from it -- so that was wrong twice over:
//
//   - the peer entry named a key no client possesses, so nobody could connect;
//   - every subscriber's private key was written into a config file on the
//     node, which is a credential the node has no business holding.
//
// Accounting could not match either: the registry keys on the derived public
// key, so `wg show transfer` output would never resolve to a subject.
//
// No test caught it because the fixtures used strings that were not valid keys
// at all, so copying them through looked the same as deriving from them.
func TestPeerCarriesTheDerivedPublicKeyNotThePrivateKey(t *testing.T) {
	a := New(newFakeRuntime(), t.TempDir(), t.TempDir())
	d := desiredWith(t, 2)
	p := payloadFor(t, a, d)

	for _, subj := range d.Subjects {
		priv := ""
		for _, c := range subj.Credentials {
			if c.Kind == "keypair" {
				priv = c.Value
			}
		}
		pub, err := PublicKeyFromPrivate(priv)
		if err != nil {
			t.Fatalf("subject %d has an underivable key: %v", subj.ID, err)
		}
		if pub == priv {
			t.Fatalf("test fixture is degenerate: subject %d derives to itself", subj.ID)
		}
		if !strings.Contains(p.Config, pub) {
			t.Errorf("subject %d: derived public key %s is missing from the config:\n%s",
				subj.ID, pub, p.Config)
		}
		// THE security assertion. A node must never hold a subscriber's
		// private key on disk.
		if strings.Contains(p.Config, priv) {
			t.Errorf("subject %d: the PRIVATE key was written into the config; "+
				"the node must never hold a subscriber's private key", subj.ID)
		}
		// And the recorded peer set must be the derived keys too, or the
		// removal check compares different alphabets.
		if !contains(p.Peers, pub) {
			t.Errorf("subject %d: derived key missing from the recorded peer set %v",
				subj.ID, p.Peers)
		}
	}
}

// A subject whose credential cannot be derived is skipped, not fatal: one bad
// credential must not take the whole service down with it.
func TestUnusableCredentialSkipsOnlyThatPeer(t *testing.T) {
	a := New(newFakeRuntime(), t.TempDir(), t.TempDir())
	d := desiredWith(t, 2)
	d.Subjects = append(d.Subjects, adapter.Subject{
		ID:          99,
		Credentials: []adapter.Credential{{Kind: "keypair", Value: "not-a-valid-key"}},
	})

	p := payloadFor(t, a, d)
	if len(p.Peers) != 2 {
		t.Errorf("peers = %d, want 2: a single unusable credential should skip "+
			"that subject and leave the rest served", len(p.Peers))
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// A service belonging to another adapter must be left alone.
//
// A node runs several adapters over one desired document, each handling only
// the services of its own kind -- Xray and sing-box have carried that check
// since they were written. WireGuard did not, and looked safe only by accident:
// foreign params usually fail ServiceParams.Validate, so buildPayload errors
// and the service is skipped.
//
// That is a coincidence, not a rule. The l2tp service below carries a port, a
// subnet and a private key, so it validates cleanly -- and before the filter it
// was planned as a WireGuard interface, and would have been installed as one.
func TestForeignServicesAreNotPlanned(t *testing.T) {
	a := New(newFakeRuntime(), t.TempDir(), t.TempDir())

	d := desiredWith(t, 1)
	ours := d.Services[0].ID
	d.Services = append(d.Services, adapter.Service{
		ID: 77, Kind: "l2tp", Enabled: true,
		// Deliberately shaped so WireGuard's own schema accepts it.
		Params: json.RawMessage(testParams),
	})

	plan, err := a.Plan(context.Background(), d, adapter.Observed{})
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
		t.Errorf("planned %d steps for one WireGuard service", len(plan.Steps))
	}
}

// And a foreign service must not make this adapter tear its own work down.
// The removal pass compares observed against desired; if desired still counted
// foreign services, an id collision between two adapters would spare a service
// that should have gone, and if it counted none, everything would be removed.
func TestForeignServicesDoNotAffectRemoval(t *testing.T) {
	a := New(newFakeRuntime(), t.TempDir(), t.TempDir())

	// Desired holds ONLY a foreign service; ours is gone and must be removed.
	d := desiredWith(t, 1)
	d.Services = []adapter.Service{{
		ID: 10, Kind: "l2tp", Enabled: true, Params: json.RawMessage(testParams),
	}}
	obs := adapter.Observed{Services: []adapter.ObservedService{
		{ID: 10, Present: true, Managed: true, Checksum: "whatever"},
	}}

	plan, err := a.Plan(context.Background(), d, obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Steps) != 1 || plan.Steps[0].Kind != "remove" {
		t.Fatalf("want a single remove step, got %+v; a foreign service sharing "+
			"an id must not keep our interface alive", plan.Steps)
	}
}

// A node with no WireGuard interfaces configured has nothing to restart.
func TestRestart_NoInterfacesReturnsUnsupported(t *testing.T) {
	e := newApplyEnv(t)
	err := e.a.Restart(context.Background())
	if !errors.Is(err, adapter.ErrRestartUnsupported) {
		t.Errorf("Restart with nothing configured = %v, want ErrRestartUnsupported", err)
	}
}

// Restart cycles every configured interface, down then up, because
// WireGuard has no daemon of its own to bounce -- each interface is its own
// kernel object. A node running two interfaces must see both go down and
// both come back up, in that order per interface.
func TestRestart_CyclesEveryConfiguredInterface(t *testing.T) {
	e := newApplyEnv(t)
	ctx := context.Background()

	for _, id := range []int64{7, 42} {
		d := desiredWith(t, 1)
		d.Services[0].ID = id
		p := payloadFor(t, e.a, d)
		if res, _ := e.a.Apply(ctx, adapter.Step{
			Kind: "install", ServiceID: id, Payload: mustJSON(t, p),
		}); !res.OK {
			t.Fatalf("install %d: %s", id, res.Err)
		}
	}
	e.rt.mu.Lock()
	e.rt.calls = nil
	e.rt.mu.Unlock()

	if err := e.a.Restart(ctx); err != nil {
		t.Fatalf("Restart: %v", err)
	}

	calls := e.rt.Calls()
	downs, ups := map[string]bool{}, map[string]bool{}
	for _, c := range calls {
		if name, ok := strings.CutPrefix(c, "down:"); ok {
			downs[name] = true
		}
		if name, ok := strings.CutPrefix(c, "up:"); ok {
			ups[name] = true
		}
	}
	for _, id := range []int64{7, 42} {
		iface := interfaceName(id)
		if !downs[iface] {
			t.Errorf("interface %s never brought down (calls=%v)", iface, calls)
		}
		if !ups[iface] {
			t.Errorf("interface %s never brought back up (calls=%v)", iface, calls)
		}
	}
}
