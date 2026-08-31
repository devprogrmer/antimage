package xray

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// fakeRuntime records what the adapter asked of Xray, so a test can assert
// that a hot user add did NOT restart the process.
type fakeRuntime struct {
	mu        sync.Mutex
	restarts  int
	reloads   int
	added     []string
	removed   []string
	available error
	healthy   bool
	detail    string
	failAdd   error
	failRst   error
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{healthy: true, detail: "active"}
}

func (f *fakeRuntime) Available(context.Context) error { return f.available }

func (f *fakeRuntime) AddUser(_ context.Context, tag string, u User, _ Protocol) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failAdd != nil {
		return f.failAdd
	}
	f.added = append(f.added, tag+"/"+u.Email)
	return nil
}

func (f *fakeRuntime) RemoveUser(_ context.Context, tag, email string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, tag+"/"+email)
	return nil
}

func (f *fakeRuntime) Reload(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reloads++
	return nil
}

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

func (f *fakeRuntime) QueryStats(context.Context) ([]UserStat, error) {
	// Tests that don't care about accounting can leave this empty.
	return nil, nil
}

func (f *fakeRuntime) counts() (restarts, reloads int, added, removed []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.restarts, f.reloads, append([]string(nil), f.added...), append([]string(nil), f.removed...)
}

func newAdapter(t *testing.T, hotAdd bool) (*Adapter, *fakeRuntime, string) {
	t.Helper()
	dir := t.TempDir()
	rt := newFakeRuntime()
	return New(dir, rt, hotAdd), rt, dir
}

func desiredWith(users int, svcID int64, params string) adapter.Desired {
	d := adapter.Desired{
		SchemaVersion: 1, Revision: 1, NodeID: 1,
		Services: []adapter.Service{
			{ID: svcID, Kind: "xray", Enabled: true, Params: json.RawMessage(params)},
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

const tlsParams = `{"protocol":"vless","port":443,"security":"tls","cert_file":"/c","key_file":"/k"}`

// converge runs Observe -> Plan -> Apply once and returns the plan it applied.
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
			t.Fatalf("Apply step %d (%s): %v", step.Seq, step.Kind, err)
		}
	}
	return plan
}

func TestDescriptorReportsHotAddFromTheRuntime(t *testing.T) {
	hot, _, _ := newAdapter(t, true)
	cold, _, _ := newAdapter(t, false)

	if !hot.Descriptor().Caps.HotUserAdd {
		t.Error("hot-capable runtime reported HotUserAdd=false")
	}
	if cold.Descriptor().Caps.HotUserAdd {
		t.Error("runtime with no management API reported HotUserAdd=true")
	}
	if hot.Descriptor().Kind != Kind {
		t.Errorf("kind = %q", hot.Descriptor().Kind)
	}
	// The schema is what the panel validates operator input against.
	var schema map[string]any
	if err := json.Unmarshal(hot.Descriptor().Caps.ServiceSchema, &schema); err != nil {
		t.Fatalf("service schema is not valid JSON: %v", err)
	}
	if schema["additionalProperties"] != false {
		t.Error("service schema accepts unknown properties")
	}
}

// Convergence: after applying a plan, re-planning must produce nothing. This
// is the property the reconciler's confirmation pass depends on.
func TestConvergesAndThenPlansNothing(t *testing.T) {
	a, rt, _ := newAdapter(t, true)
	d := desiredWith(2, 10, tlsParams)

	first := converge(t, a, d)
	if len(first.Steps) == 0 {
		t.Fatal("first convergence planned nothing")
	}

	obs, _ := a.Observe(context.Background())
	again, err := a.Plan(context.Background(), d, obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !again.IsEmpty() {
		t.Fatalf("converged state still plans %d step(s): %+v", len(again.Steps), again.Steps)
	}
	if restarts, _, _, _ := rt.counts(); restarts != 1 {
		t.Errorf("restarts = %d, want exactly 1 for a new inbound", restarts)
	}
}

// Idempotency: applying the same plan repeatedly must not change the outcome.
func TestApplyIsIdempotent(t *testing.T) {
	a, _, dir := newAdapter(t, true)
	d := desiredWith(1, 10, tlsParams)

	plan := converge(t, a, d)
	before, err := os.ReadFile(filepath.Join(dir, "antimage-10.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	// Re-apply every step from the same plan three more times.
	for i := 0; i < 3; i++ {
		for _, step := range plan.Steps {
			if _, err := a.Apply(context.Background(), step); err != nil {
				t.Fatalf("re-apply %d step %d: %v", i, step.Seq, err)
			}
		}
	}
	after, err := os.ReadFile(filepath.Join(dir, "antimage-10.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(before) != string(after) {
		t.Error("repeated Apply changed the config on disk")
	}

	obs, _ := a.Observe(context.Background())
	again, _ := a.Plan(context.Background(), d, obs)
	if !again.IsEmpty() {
		t.Errorf("state after repeated Apply is not converged: %+v", again.Steps)
	}
}

// DECISION 3, the central claim: adding a user to an existing inbound must not
// restart the service when the runtime supports hot add.
func TestAddingAUserDoesNotRestartWhenHotAddIsSupported(t *testing.T) {
	a, rt, _ := newAdapter(t, true)

	converge(t, a, desiredWith(1, 10, tlsParams))
	restartsAfterCreate, _, _, _ := rt.counts()

	// Now add a second subject.
	plan := converge(t, a, desiredWith(2, 10, tlsParams))

	restartsAfterAdd, _, added, _ := rt.counts()
	if restartsAfterAdd != restartsAfterCreate {
		t.Errorf("adding a user restarted the service: %d -> %d restarts",
			restartsAfterCreate, restartsAfterAdd)
	}
	if len(added) == 0 {
		t.Error("no user was added through the runtime API")
	}
	if plan.MaxDisruption() >= adapter.DisruptRestart {
		t.Errorf("user add classified as %v, want none/reload", plan.MaxDisruption())
	}
}

// The converse: without hot add, the same change must be classified as a
// restart. Mis-declaring it would let the reconciler apply it during business
// hours and drop every session on the node.
func TestAddingAUserIsRestartClassWhenHotAddIsUnsupported(t *testing.T) {
	a, rt, _ := newAdapter(t, false)

	converge(t, a, desiredWith(1, 10, tlsParams))
	before, _, _, _ := rt.counts()

	obs, _ := a.Observe(context.Background())
	plan, err := a.Plan(context.Background(), desiredWith(2, 10, tlsParams), obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.MaxDisruption() < adapter.DisruptRestart {
		t.Fatalf("user add without hot support classified as %v, want restart", plan.MaxDisruption())
	}
	for _, step := range plan.Steps {
		if _, err := a.Apply(context.Background(), step); err != nil {
			t.Fatalf("Apply: %v", err)
		}
	}
	after, _, added, _ := rt.counts()
	if after == before {
		t.Error("a restart-class change did not restart the service")
	}
	if len(added) != 0 {
		t.Error("the runtime API was used despite hot add being unsupported")
	}
}

// Changing the listen port is a restart on every backend; classifying it as
// anything less would report convergence while the old port is still bound.
func TestChangingThePortIsRestartClass(t *testing.T) {
	a, _, _ := newAdapter(t, true)
	converge(t, a, desiredWith(1, 10, tlsParams))

	moved := `{"protocol":"vless","port":8443,"security":"tls","cert_file":"/c","key_file":"/k"}`
	obs, _ := a.Observe(context.Background())
	plan, err := a.Plan(context.Background(), desiredWith(1, 10, moved), obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.MaxDisruption() != adapter.DisruptRestart {
		t.Errorf("port change classified as %v, want restart", plan.MaxDisruption())
	}
}

// Drift: a hand edit must be detected and corrected.
func TestHandEditIsDetectedAndCorrected(t *testing.T) {
	a, _, dir := newAdapter(t, true)
	d := desiredWith(1, 10, tlsParams)
	converge(t, a, d)

	path := filepath.Join(dir, "antimage-10.json")
	body, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append(body, []byte("\n// HAND EDIT\n")...), 0o600); err != nil {
		t.Fatalf("edit: %v", err)
	}

	obs, _ := a.Observe(context.Background())
	plan, err := a.Plan(context.Background(), d, obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.IsEmpty() {
		t.Fatal("a hand-edited config produced no corrective plan")
	}
	for _, step := range plan.Steps {
		if _, err := a.Apply(context.Background(), step); err != nil {
			t.Fatalf("Apply: %v", err)
		}
	}
	fixed, _ := os.ReadFile(path)
	if strings.Contains(string(fixed), "HAND EDIT") {
		t.Error("the hand edit survived convergence")
	}
}

// A file antimage did not write must never be overwritten: convergence must
// not destroy an operator's own configuration.
func TestUnmanagedFileIsRefusedRatherThanOverwritten(t *testing.T) {
	a, _, dir := newAdapter(t, true)
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
	_, err = a.Plan(context.Background(), desiredWith(1, 10, tlsParams), obs)
	if err == nil {
		t.Fatal("planned over a file antimage did not write")
	}
	if !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Errorf("err = %v, want it to name the refusal", err)
	}
	body, _ := os.ReadFile(path)
	if string(body) != `{"handmade":true}` {
		t.Error("the operator's file was modified")
	}
}

// A disabled service must stop serving, which means removing its config.
func TestDisablingAServiceRemovesItAndRestarts(t *testing.T) {
	a, rt, dir := newAdapter(t, true)
	converge(t, a, desiredWith(1, 10, tlsParams))

	off := desiredWith(1, 10, tlsParams)
	off.Services[0].Enabled = false
	plan := converge(t, a, off)

	if plan.MaxDisruption() != adapter.DisruptRestart {
		t.Errorf("disable classified as %v, want restart", plan.MaxDisruption())
	}
	if _, err := os.Stat(filepath.Join(dir, "antimage-10.json")); !errors.Is(err, os.ErrNotExist) {
		t.Error("the disabled service's config is still on disk")
	}
	if restarts, _, _, _ := rt.counts(); restarts < 2 {
		t.Errorf("restarts = %d, want a restart after removal", restarts)
	}
}

// A service removed from desired state entirely must be cleaned up.
func TestRemovedServiceIsCleanedUp(t *testing.T) {
	a, _, dir := newAdapter(t, true)
	converge(t, a, desiredWith(1, 10, tlsParams))

	empty := adapter.Desired{SchemaVersion: 1, Revision: 2, NodeID: 1}
	converge(t, a, empty)

	if _, err := os.Stat(filepath.Join(dir, "antimage-10.json")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a service no longer in desired state was left on disk")
	}
}

// Invalid params must fail Plan rather than generating a config that restarts
// Xray into a crash loop.
func TestInvalidConfigurationFailsPlanning(t *testing.T) {
	a, rt, _ := newAdapter(t, true)
	bad := desiredWith(1, 10, `{"protocol":"vless","port":99999}`)

	obs, _ := a.Observe(context.Background())
	_, err := a.Plan(context.Background(), bad, obs)
	if err == nil {
		t.Fatal("planned an inbound with an out-of-range port")
	}
	if !errors.Is(err, ErrInvalidInbound) {
		t.Errorf("err = %v, want ErrInvalidInbound", err)
	}
	if restarts, _, _, _ := rt.counts(); restarts != 0 {
		t.Error("an invalid config reached the runtime")
	}
}

// A subject with no credential must not silently become a client entry that
// authenticates nobody.
func TestSubjectWithoutCredentialFailsPlanning(t *testing.T) {
	a, _, _ := newAdapter(t, true)
	d := desiredWith(0, 10, tlsParams)
	d.Subjects = []adapter.Subject{{ID: 7}} // no credentials

	obs, _ := a.Observe(context.Background())
	_, err := a.Plan(context.Background(), d, obs)
	if err == nil {
		t.Fatal("planned a config for a subject with no credential")
	}
	if !strings.Contains(err.Error(), "subject 7") {
		t.Errorf("err = %v, want it to name the subject", err)
	}
}

// Recovery: a failed restart must surface as a failed step, and the next
// convergence must retry rather than believing it succeeded.
func TestFailedRestartSurfacesAndRecovers(t *testing.T) {
	a, rt, _ := newAdapter(t, true)
	rt.failRst = errors.New("systemctl: unit not found")

	d := desiredWith(1, 10, tlsParams)
	ctx := context.Background()
	obs, _ := a.Observe(ctx)
	plan, err := a.Plan(ctx, d, obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	var sawFailure bool
	for _, step := range plan.Steps {
		res, err := a.Apply(ctx, step)
		if err != nil {
			sawFailure = true
			if res.OK {
				t.Error("a failed step reported OK")
			}
			if !strings.Contains(res.Err, "unit not found") {
				t.Errorf("step error = %q, want the runtime cause", res.Err)
			}
		}
	}
	if !sawFailure {
		t.Fatal("a failing restart did not surface as a step failure")
	}

	// Recover: the runtime comes back, and convergence completes.
	rt.failRst = nil
	converge(t, a, d)
	obs, _ = a.Observe(ctx)
	again, _ := a.Plan(ctx, d, obs)
	if !again.IsEmpty() {
		t.Errorf("did not converge after the runtime recovered: %+v", again.Steps)
	}
}

// A failing hot add must fail the step rather than being silently swallowed,
// or the panel would record convergence while the user cannot connect.
func TestFailedHotAddSurfaces(t *testing.T) {
	a, rt, _ := newAdapter(t, true)
	converge(t, a, desiredWith(1, 10, tlsParams))

	rt.failAdd = errors.New("connection refused")
	ctx := context.Background()
	obs, _ := a.Observe(ctx)
	plan, _ := a.Plan(ctx, desiredWith(2, 10, tlsParams), obs)

	var failed bool
	for _, step := range plan.Steps {
		if step.Kind != StepAddUser {
			continue
		}
		res, err := a.Apply(ctx, step)
		if err == nil {
			t.Error("a failing hot add reported success")
		}
		if res.OK {
			t.Error("a failed step reported OK")
		}
		failed = true
	}
	if !failed {
		t.Fatal("no add_user step was planned")
	}
}

// Probe must report the runtime honestly, including a missing binary.
func TestProbeReportsMissingBinaryAndHealth(t *testing.T) {
	a, rt, _ := newAdapter(t, true)

	h, err := a.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !h.OK {
		t.Errorf("healthy runtime probed as unhealthy: %+v", h)
	}

	rt.available = errors.New("xray not found in PATH")
	h, err = a.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if h.OK {
		t.Error("probe reported OK with the binary missing")
	}
	if !strings.Contains(h.Detail, "not found") {
		t.Errorf("detail = %q, want it to name the missing binary", h.Detail)
	}

	rt.available = nil
	rt.healthy, rt.detail = false, "inactive (dead)"
	h, _ = a.Probe(context.Background())
	if h.OK || !strings.Contains(h.Detail, "inactive") {
		t.Errorf("probe did not report a dead unit: %+v", h)
	}
}

// Plan must be pure: calling it twice with the same inputs yields the same
// steps and touches nothing. The convergence property test depends on it.
func TestPlanIsPureAndRepeatable(t *testing.T) {
	a, rt, _ := newAdapter(t, true)
	d := desiredWith(3, 10, tlsParams)
	ctx := context.Background()

	obs, _ := a.Observe(ctx)
	first, err := a.Plan(ctx, d, obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := a.Plan(ctx, d, obs)
		if err != nil {
			t.Fatalf("Plan %d: %v", i, err)
		}
		if len(again.Steps) != len(first.Steps) {
			t.Fatalf("plan %d has %d steps, first had %d", i, len(again.Steps), len(first.Steps))
		}
		for j := range again.Steps {
			if again.Steps[j].Kind != first.Steps[j].Kind ||
				again.Steps[j].Disruption != first.Steps[j].Disruption ||
				string(again.Steps[j].Payload) != string(first.Steps[j].Payload) {
				t.Fatalf("plan %d step %d differs from the first", i, j)
			}
		}
	}
	if restarts, reloads, added, removed := rt.counts(); restarts+reloads+len(added)+len(removed) != 0 {
		t.Error("Plan touched the runtime; it must be pure")
	}
}

// Configs carry credentials in plaintext -- that is what Xray reads -- so they
// must not be world-readable.
//
// Verified on Unix only: Go's os.Chmod on Windows toggles just the read-only
// bit, so 0600 is not representable there and the assertion would fail on a
// correct implementation. CI runs Linux, which is also the only platform the
// agent supports, so this is checked where it matters.
func TestWrittenConfigIsNotWorldReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not representable on Windows; verified on Linux CI")
	}
	a, _, dir := newAdapter(t, true)
	converge(t, a, desiredWith(1, 10, tlsParams))

	info, err := os.Stat(filepath.Join(dir, "antimage-10.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("config mode = %04o, want no group or other access", perm)
	}
}

// Revocation is a security boundary, not a performance question.
//
// Xray keeps serving a user until it is explicitly told to stop. Deleting the
// credential from the config file does nothing to an established session, so a
// revocation planned as a hot, DisruptNone change would report converged while
// the revoked user stayed connected indefinitely. This test exists because an
// earlier implementation did exactly that: it saw "only the user set changed",
// took the hot-add path, rewrote the file without the revoked user, and never
// restarted or called RemoveUser.
func TestRevokingAUserActuallyReachesTheRuntime(t *testing.T) {
	a, rt, dir := newAdapter(t, true) // hot add supported: the tempting path

	converge(t, a, desiredWith(2, 10, tlsParams))
	restartsBefore, _, _, _ := rt.counts()

	revoked := "11111111-2222-3333-4444-555555555552"
	body, err := os.ReadFile(filepath.Join(dir, "antimage-10.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(body), revoked) {
		t.Fatal("precondition: the second user was never in the config")
	}

	// Desired drops to one subject: the second is revoked.
	plan := converge(t, a, desiredWith(1, 10, tlsParams))

	if got := plan.MaxDisruption(); got < adapter.DisruptRestart {
		t.Errorf("revocation planned as %v, want at least %v: the running process "+
			"would never learn about it", got, adapter.DisruptRestart)
	}

	after, err := os.ReadFile(filepath.Join(dir, "antimage-10.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(after), revoked) {
		t.Error("the revoked credential is still in the config file")
	}

	restartsAfter, _, _, removedFromRT := rt.counts()
	if restartsAfter == restartsBefore && len(removedFromRT) == 0 {
		t.Error("the revoked user was removed from the file but the running process was " +
			"neither restarted nor told to remove them, so they stay connected")
	}
}

// The applied sidecar has to carry the user set, because that is the only
// thing Plan can consult to tell an addition from a removal. If it recorded
// the checksum alone, revocation would silently become a hot change again.
func TestAppliedStateRecordsTheUserSet(t *testing.T) {
	a, _, dir := newAdapter(t, true)
	converge(t, a, desiredWith(2, 10, tlsParams))

	body, err := os.ReadFile(filepath.Join(dir, "antimage-10.json"+appliedSuffix))
	if err != nil {
		t.Fatalf("read applied sidecar: %v", err)
	}
	var st appliedState
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatalf("decode applied sidecar: %v", err)
	}
	if st.Checksum == "" {
		t.Error("applied sidecar records no checksum")
	}
	// Service-scoped tags since C2: Xray counts per email, so the same subject
	// on another inbound must not share this one's counter.
	want := []string{subjectEmail(1, 10), subjectEmail(2, 10)}
	if !reflect.DeepEqual(st.Users, want) {
		t.Errorf("applied users = %v, want %v; Plan cannot detect a removal without them",
			st.Users, want)
	}
}

// Adding a user while nobody is removed must still take the cheap path -- the
// revocation fix must not turn every membership change into a restart.
func TestAddingAUserIsStillHotAfterTheRevocationFix(t *testing.T) {
	a, rt, _ := newAdapter(t, true)
	converge(t, a, desiredWith(1, 10, tlsParams))
	restartsBefore, _, _, _ := rt.counts()

	plan := converge(t, a, desiredWith(2, 10, tlsParams))
	if got := plan.MaxDisruption(); got != adapter.DisruptNone {
		t.Errorf("pure addition planned as %v, want %v", got, adapter.DisruptNone)
	}
	if restartsAfter, _, _, _ := rt.counts(); restartsAfter != restartsBefore {
		t.Errorf("a pure addition restarted the runtime: %d -> %d", restartsBefore, restartsAfter)
	}
}

// A missing or unreadable sidecar means "unknown", and unknown must not be
// treated as "nobody was removed".
func TestUnknownAppliedStateForcesARestart(t *testing.T) {
	a, _, dir := newAdapter(t, true)
	converge(t, a, desiredWith(2, 10, tlsParams))

	if err := os.Remove(filepath.Join(dir, "antimage-10.json"+appliedSuffix)); err != nil {
		t.Fatalf("remove sidecar: %v", err)
	}

	ctx := context.Background()
	obs, err := a.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	plan, err := a.Plan(ctx, desiredWith(3, 10, tlsParams), obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := plan.MaxDisruption(); got < adapter.DisruptRestart {
		t.Errorf("planned %v with no applied state; want at least %v, because the "+
			"adapter cannot know who the runtime is serving", got, adapter.DisruptRestart)
	}
}

// The Xray half of the same property: a write that lands on disk but never
// reaches the process must not be reported as converged.
func TestFailedRestartDoesNotLookLikeConvergence(t *testing.T) {
	a, rt, _ := newAdapter(t, true)
	ctx := context.Background()
	d := desiredWith(1, 10, tlsParams)

	rt.failRst = errors.New("systemctl: job failed")
	obs, _ := a.Observe(ctx)
	plan, err := a.Plan(ctx, d, obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, s := range plan.Steps {
		_, _ = a.Apply(ctx, s)
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
	for _, s := range next.Steps {
		if _, err := a.Apply(ctx, s); err != nil {
			t.Fatalf("Apply %s: %v", s.Kind, err)
		}
	}
	if restarts, _, _, _ := rt.counts(); restarts == 0 {
		t.Error("recovery never restarted the runtime")
	}
	obs, _ = a.Observe(ctx)
	final, _ := a.Plan(ctx, d, obs)
	if !final.IsEmpty() {
		t.Errorf("did not settle after recovery: %+v", final.Steps)
	}
}

// The complete revocation sequence, including the part that only matters when
// something goes wrong.
//
// TestRevokingAUserActuallyReachesTheRuntime proves the classification. This
// proves what happens either side of it: that a revocation whose restart FAILS
// is not mistaken for convergence, and that once the restart succeeds the
// applied state reflects the smaller user set rather than the one that was
// revoked. Those two together are what stop a failed revocation from being
// retried into silence.
func TestRevocationDoesNotConvergeUntilTheRestartSucceeds(t *testing.T) {
	a, rt, dir := newAdapter(t, true) // hot add supported
	ctx := context.Background()

	// A and B are both applied and live.
	converge(t, a, desiredWith(2, 10, tlsParams))
	revoked := "11111111-2222-3333-4444-555555555552"

	st := a.applied(10)
	if len(st.Users) != 2 {
		t.Fatalf("precondition: applied state carries %v, want both users", st.Users)
	}

	// Desired drops B. Stage the restart to fail.
	rt.failRst = errors.New("systemctl: job failed")
	d := desiredWith(1, 10, tlsParams)

	obs, err := a.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	plan, err := a.Plan(ctx, d, obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := plan.MaxDisruption(); got < adapter.DisruptRestart {
		t.Fatalf("revocation planned as %v, want at least %v", got, adapter.DisruptRestart)
	}

	var sawFailure bool
	for _, step := range plan.Steps {
		if res, err := a.Apply(ctx, step); err != nil {
			sawFailure = true
			if res.OK {
				t.Error("a step whose restart failed reported OK")
			}
		}
	}
	if !sawFailure {
		t.Fatal("the staged restart failure never surfaced as a step failure")
	}

	// The file no longer lists B, but the process was never reloaded. The
	// adapter must NOT call that converged -- B is still connected.
	body, err := os.ReadFile(filepath.Join(dir, "antimage-10.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(body), revoked) {
		t.Error("the revoked credential is still in the config file")
	}
	// The applied state must still list the revoked user, because the runtime
	// IS still serving them. Recording the revocation here on the strength of a
	// restart that failed is what would make the next Plan believe nobody was
	// removed, quietly downgrading the retry to the hot path.
	if got := a.applied(10); !slices.Contains(got.Users, subjectEmail(2, 10)) {
		t.Errorf("applied state = %v; it dropped the revoked user after a FAILED "+
			"restart, so the next Plan would no longer see a removal", got.Users)
	}

	obs, _ = a.Observe(ctx)
	pending, err := a.Plan(ctx, d, obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if pending.IsEmpty() {
		t.Fatal("reported convergence while the revoked user is still being served " +
			"by a process that never reloaded")
	}

	// The runtime recovers.
	rt.failRst = nil
	for _, step := range pending.Steps {
		if _, err := a.Apply(ctx, step); err != nil {
			t.Fatalf("Apply %s after recovery: %v", step.Kind, err)
		}
	}

	// Applied state now reflects the smaller set.
	final := a.applied(10)
	want := []string{subjectEmail(1, 10)}
	if !reflect.DeepEqual(final.Users, want) {
		t.Errorf("applied users = %v, want %v", final.Users, want)
	}
	if restarts, _, _, _ := rt.counts(); restarts < 2 {
		t.Errorf("restarts = %d, want the revocation to have restarted the process", restarts)
	}

	// And it stays converged.
	for i := 0; i < 3; i++ {
		obs, _ = a.Observe(ctx)
		again, err := a.Plan(ctx, d, obs)
		if err != nil {
			t.Fatalf("Plan %d: %v", i, err)
		}
		if !again.IsEmpty() {
			t.Fatalf("not idempotent; pass %d still plans %+v", i, again.Steps)
		}
	}
}

// Restart is a direct forward to the runtime: xray runs as one process
// multiplexing every inbound, so there is exactly one thing to bounce.
func TestRestart_ForwardsToRuntime(t *testing.T) {
	rt := newFakeRuntime()
	a := New(t.TempDir(), rt, false)

	if err := a.Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if restarts, _, _, _ := rt.counts(); restarts != 1 {
		t.Errorf("restarts = %d, want 1", restarts)
	}
}
