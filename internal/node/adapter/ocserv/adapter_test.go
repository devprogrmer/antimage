package ocserv

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// fakeRuntime records what the adapter asked the host to do, and simulates
// ocpasswd's effect on the passwd file so Observe has something real to read.
type fakeRuntime struct {
	dir      string
	calls    []string
	users    map[string]string // username -> password
	active   bool
	sessions []OcctlUser
	failSet  bool
}

func newFake(dir string) *fakeRuntime {
	return &fakeRuntime{dir: dir, users: map[string]string{}, active: true}
}

func (f *fakeRuntime) Available(context.Context) error { return nil }
func (f *fakeRuntime) Start(context.Context) error     { f.calls = append(f.calls, "start"); return nil }
func (f *fakeRuntime) Stop(context.Context) error      { f.calls = append(f.calls, "stop"); return nil }
func (f *fakeRuntime) Restart(context.Context) error {
	f.calls = append(f.calls, "restart")
	return nil
}
func (f *fakeRuntime) Reload(context.Context) error {
	f.calls = append(f.calls, "reload")
	return nil
}
func (f *fakeRuntime) Active(context.Context) bool { return f.active }

// SetPassword writes a line shaped like ocpasswd's, including a salt that
// differs every call -- which is exactly the property the checksum design has
// to survive.
func (f *fakeRuntime) SetPassword(_ context.Context, path, user, pw string) error {
	if f.failSet {
		return os.ErrPermission
	}
	f.calls = append(f.calls, "set:"+user)
	f.users[user] = pw
	return f.flush(path)
}

func (f *fakeRuntime) DeletePassword(_ context.Context, path, user string) error {
	f.calls = append(f.calls, "del:"+user)
	delete(f.users, user)
	return f.flush(path)
}

var salt int

func (f *fakeRuntime) flush(path string) error {
	names := make([]string, 0, len(f.users))
	for n := range f.users {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		salt++
		// A fresh salt per write, like the real ocpasswd.
		b.WriteString(n + ":ocserv:$5$" + itoa(salt) + "$hash\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func (f *fakeRuntime) ShowUsers(context.Context) ([]OcctlUser, error) { return f.sessions, nil }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}

const goodParams = `{
  "port": 443,
  "server_cert": "/etc/ssl/cert.pem",
  "server_key": "/etc/ssl/key.pem",
  "ipv4_network": "192.168.220.0",
  "ipv4_netmask": "255.255.255.0"
}`

func newTestAdapter(t *testing.T) (*Adapter, *fakeRuntime) {
	t.Helper()
	dir := t.TempDir()
	rt := newFake(dir)
	return New(rt, dir, t.TempDir()), rt
}

func desiredWith(params string, subjectIDs ...int64) adapter.Desired {
	d := adapter.Desired{
		SchemaVersion: 3,
		Services: []adapter.Service{{
			ID: 7, Kind: string(Kind), Enabled: true, Params: json.RawMessage(params),
		}},
	}
	for _, id := range subjectIDs {
		d.Subjects = append(d.Subjects, adapter.Subject{
			ID:          id,
			Credentials: []adapter.Credential{{Kind: "password", Value: "pw-" + itoa(int(id))}},
		})
	}
	return d
}

// converge runs Observe/Plan/Apply until the plan is empty, which is the
// property the reconciler depends on.
func converge(t *testing.T, a *Adapter, d adapter.Desired) int {
	t.Helper()
	ctx := context.Background()
	for pass := 1; pass <= 5; pass++ {
		obs, err := a.Observe(ctx)
		if err != nil {
			t.Fatalf("Observe: %v", err)
		}
		plan, err := a.Plan(ctx, d, obs)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if plan.IsEmpty() {
			return pass
		}
		for _, step := range plan.Steps {
			res, err := a.Apply(ctx, step)
			if err != nil {
				t.Fatalf("Apply %s: %v", step.Kind, err)
			}
			if !res.OK {
				t.Fatalf("Apply %s failed: %s", step.Kind, res.Err)
			}
		}
	}
	t.Fatal("did not converge in 5 passes")
	return 0
}

// The core property: apply, then plan again, and there is nothing left to do.
// An adapter that fails this rewrites its config on every reconcile pass and
// restarts the service each time.
func TestConvergesAndStaysConverged(t *testing.T) {
	a, rt := newTestAdapter(t)
	d := desiredWith(goodParams, 1, 2)

	if passes := converge(t, a, d); passes < 2 {
		t.Fatalf("converged in %d passes, want at least 2 (install then verify)", passes)
	}

	// The salt differs on every ocpasswd write, so a byte-checksum over the
	// passwd file would report drift forever. This is the regression guard for
	// that design.
	obs, _ := a.Observe(context.Background())
	plan, _ := a.Plan(context.Background(), d, obs)
	if !plan.IsEmpty() {
		t.Errorf("a second plan after convergence wants %d more steps; the passwd "+
			"file's random salt is being treated as drift", len(plan.Steps))
	}
	_ = rt
}

// Adding a user must not cost a reload. ocserv reads the passwd file per
// connection, and charging a reload would drop nobody's session but would make
// the reconciler debounce a change that is free.
func TestAddingAUserIsFree(t *testing.T) {
	a, _ := newTestAdapter(t)
	ctx := context.Background()
	converge(t, a, desiredWith(goodParams, 1))

	obs, _ := a.Observe(ctx)
	plan, err := a.Plan(ctx, desiredWith(goodParams, 1, 2), obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.IsEmpty() {
		t.Fatal("adding a subject produced no plan")
	}
	if got := plan.MaxDisruption(); got != adapter.DisruptNone {
		t.Errorf("adding a user costs %v, want %v", got, adapter.DisruptNone)
	}
	for _, s := range plan.Steps {
		if s.Kind != StepSyncUsers {
			t.Errorf("adding a user produced a %s step; only the user file changed", s.Kind)
		}
	}
}

// A config change reloads; it does not restart. Established sessions survive a
// reload, and restarting would drop every connected customer for a DNS change.
func TestAConfigChangeReloadsRatherThanRestarts(t *testing.T) {
	a, _ := newTestAdapter(t)
	ctx := context.Background()
	converge(t, a, desiredWith(goodParams, 1))

	changed := strings.Replace(goodParams, `"port": 443`, `"port": 8443`, 1)
	obs, _ := a.Observe(ctx)
	plan, err := a.Plan(ctx, desiredWith(changed, 1), obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.IsEmpty() {
		t.Fatal("a changed port produced no plan")
	}
	if got := plan.MaxDisruption(); got != adapter.DisruptReload {
		t.Errorf("a config change costs %v, want %v", got, adapter.DisruptReload)
	}
}

// A hand-edited config is reported, not overwritten. An operator who tuned
// ocserv.conf directly keeps their change; the panel reports drift.
func TestAHandEditedConfigIsNotOverwritten(t *testing.T) {
	a, _ := newTestAdapter(t)
	ctx := context.Background()
	d := desiredWith(goodParams, 1)
	converge(t, a, d)

	path := filepath.Join(a.dir, confName)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read conf: %v", err)
	}
	if err := os.WriteFile(path, append(body, []byte("\nmtu = 1200\n")...), 0o600); err != nil {
		t.Fatalf("hand edit: %v", err)
	}

	obs, err := a.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.Services[0].Managed {
		t.Error("a file whose body no longer matches its own marker is reported as " +
			"managed, so the next plan would silently overwrite the edit")
	}
	plan, err := a.Plan(ctx, d, obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !plan.IsEmpty() {
		t.Errorf("planned %d steps against a hand-edited config; the edit must be "+
			"reported as drift, not reverted", len(plan.Steps))
	}
}

// A config the adapter never wrote belongs to whoever did write it.
func TestAForeignConfigIsLeftAlone(t *testing.T) {
	a, _ := newTestAdapter(t)
	ctx := context.Background()
	path := filepath.Join(a.dir, confName)
	if err := os.WriteFile(path, []byte("# hand rolled\ntcp-port = 443\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	obs, err := a.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.Services[0].Managed {
		t.Fatal("a config with no marker is reported as managed")
	}
	plan, _ := a.Plan(ctx, desiredWith(goodParams, 1), obs)
	if !plan.IsEmpty() {
		t.Errorf("planned %d steps against somebody else's config", len(plan.Steps))
	}
}

// Removing the service removes only what the adapter owns.
func TestRemovingTheServiceLeavesForeignAccounts(t *testing.T) {
	a, rt := newTestAdapter(t)
	ctx := context.Background()
	converge(t, a, desiredWith(goodParams, 1))

	// Somebody's own account, not created by the panel.
	if err := rt.SetPassword(ctx, a.passwdPath(), "ops-tester", "x"); err != nil {
		t.Fatalf("seed foreign account: %v", err)
	}

	obs, _ := a.Observe(ctx)
	plan, _ := a.Plan(ctx, desiredWith(goodParams), obs) // no subjects
	for _, s := range plan.Steps {
		if _, err := a.Apply(ctx, s); err != nil {
			t.Fatalf("Apply: %v", err)
		}
	}

	names, err := a.readUsernames()
	if err != nil {
		t.Fatalf("read usernames: %v", err)
	}
	var found bool
	for _, n := range names {
		if n == "ops-tester" {
			found = true
		}
		if n == "subject-1" {
			t.Error("subject-1 survived removal from desired state")
		}
	}
	if !found {
		t.Error("an account the panel never created was deleted; only subject-N " +
			"accounts belong to this adapter")
	}
}

// A subject with no password credential is skipped, not created with an empty
// one -- ocserv would accept an empty password as a valid login.
func TestASubjectWithNoPasswordIsNotCreated(t *testing.T) {
	a, _ := newTestAdapter(t)
	d := desiredWith(goodParams)
	d.Subjects = []adapter.Subject{{ID: 9}} // no credentials at all

	converge(t, a, d)

	names, _ := a.readUsernames()
	for _, n := range names {
		if n == "subject-9" {
			t.Error("a subject with no password credential was given an ocserv " +
				"account, which ocserv would let log in with an empty password")
		}
	}
}

func TestBadParamsFailThePlanRatherThanTheApply(t *testing.T) {
	a, _ := newTestAdapter(t)
	for _, tc := range []struct{ name, params string }{
		{"no port", `{"server_cert":"c","server_key":"k","ipv4_network":"10.0.0.0","ipv4_netmask":"255.255.255.0"}`},
		{"port out of range", strings.Replace(goodParams, `"port": 443`, `"port": 70000`, 1)},
		{"unknown field", strings.Replace(goodParams, `"port": 443`, `"port": 443, "nope": 1`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := a.Plan(context.Background(), desiredWith(tc.params, 1), adapter.Observed{})
			if err == nil {
				t.Error("Plan accepted params it cannot render; the failure would " +
					"surface mid-apply with the config already half written")
			}
		})
	}
}

// Two ocserv services would fight over one config file and one system unit,
// and whichever applied last would win differently on every pass.
func TestTwoServicesAreRefused(t *testing.T) {
	a, _ := newTestAdapter(t)
	d := desiredWith(goodParams, 1)
	d.Services = append(d.Services, adapter.Service{
		ID: 8, Kind: string(Kind), Enabled: true, Params: json.RawMessage(goodParams),
	})
	if _, err := a.Plan(context.Background(), d, adapter.Observed{}); err == nil {
		t.Error("two ocserv services on one node were accepted")
	}
}

func TestRenderedConfigIsDeterministic(t *testing.T) {
	p, err := parseServiceParams(json.RawMessage(goodParams))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	first := renderConf(7, p, "/etc/ocserv/ocpasswd")
	for i := 0; i < 5; i++ {
		if renderConf(7, p, "/etc/ocserv/ocpasswd") != first {
			t.Fatal("renderConf is not deterministic, so every plan would see drift")
		}
	}
	if !strings.Contains(first, "udp-port = 443") {
		t.Error("UDP is off by default; without DTLS the connection is TCP-only " +
			"and feels far worse on a lossy link")
	}
}

// The username is the accounting key, so it must round-trip and must not
// accept anything the adapter did not mint.
func TestUsernameRoundTrip(t *testing.T) {
	for _, id := range []int64{1, 42, 999999} {
		got, ok := subjectIDFromUsername(usernameFor(id))
		if !ok || got != id {
			t.Errorf("usernameFor(%d) did not round-trip: got %d ok=%v", id, got, ok)
		}
	}
	for _, name := range []string{"ops-tester", "subject-", "subject-12abc", "root", ""} {
		if _, ok := subjectIDFromUsername(name); ok {
			t.Errorf("%q was accepted as a subject account; its traffic would be "+
				"billed to somebody", name)
		}
	}
}

// ACCOUNTING. occtl reports counters per SESSION, not per user, and a session
// that reconnects starts again at zero.

func TestUsageReportsDeltasNotTotals(t *testing.T) {
	a, rt := newTestAdapter(t)
	ctx := context.Background()

	rt.sessions = []OcctlUser{{Username: "subject-1", Session: "s1", RX: 100, TX: 200}}
	first, err := a.Usage(ctx)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if len(first) != 1 || first[0].UplinkBytes != 100 || first[0].DownlinkBytes != 200 {
		t.Fatalf("first poll = %+v, want 100 up / 200 down", first)
	}

	// Same session, counters grown. The delta is the growth, not the total.
	rt.sessions = []OcctlUser{{Username: "subject-1", Session: "s1", RX: 150, TX: 260}}
	second, err := a.Usage(ctx)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if len(second) != 1 || second[0].UplinkBytes != 50 || second[0].DownlinkBytes != 60 {
		t.Errorf("second poll = %+v, want 50 up / 60 down; reporting the total "+
			"again would bill the customer twice for the same bytes", second)
	}
}

// A reconnect is a new session whose counters start at zero. Keyed by username
// this would read as a counter going backwards on every reconnect.
func TestAReconnectIsNotACounterReset(t *testing.T) {
	a, rt := newTestAdapter(t)
	ctx := context.Background()

	rt.sessions = []OcctlUser{{Username: "subject-1", Session: "s1", RX: 1000, TX: 1000}}
	if _, err := a.Usage(ctx); err != nil {
		t.Fatalf("Usage: %v", err)
	}

	// Reconnected: new session id, counters from zero.
	rt.sessions = []OcctlUser{{Username: "subject-1", Session: "s2", RX: 30, TX: 40}}
	got, err := a.Usage(ctx)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if len(got) != 1 || got[0].UplinkBytes != 30 || got[0].DownlinkBytes != 40 {
		t.Errorf("after a reconnect = %+v, want 30 up / 40 down", got)
	}
}

// One customer with several devices holds several sessions. Reporting them
// separately would make the panel's arithmetic depend on how many happened to
// be connected at poll time.
func TestConcurrentSessionsSumToOneSubject(t *testing.T) {
	a, rt := newTestAdapter(t)
	rt.sessions = []OcctlUser{
		{Username: "subject-4", Session: "a", RX: 10, TX: 20},
		{Username: "subject-4", Session: "b", RX: 5, TX: 7},
	}
	got, err := a.Usage(context.Background())
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d samples for one subject, want 1", len(got))
	}
	if got[0].UplinkBytes != 15 || got[0].DownlinkBytes != 27 {
		t.Errorf("got %+v, want 15 up / 27 down", got[0])
	}
}

// Traffic from an account the panel did not create has no subject to bill.
func TestForeignAccountsAreNotBilled(t *testing.T) {
	a, rt := newTestAdapter(t)
	rt.sessions = []OcctlUser{{Username: "ops-tester", Session: "s1", RX: 999, TX: 999}}
	got, err := a.Usage(context.Background())
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("billed %d samples for an account the panel never created: %+v",
			len(got), got)
	}
}

// occtl builds differ on whether counters are JSON numbers or strings. A type
// mismatch must not discard the whole report.
func TestOcctlCountersDecodeFromStringsOrNumbers(t *testing.T) {
	var users []OcctlUser
	body := `[{"Username":"subject-1","Session":"s1","RX":"1024","TX":2048},
	          {"Username":"subject-2","Session":"s2","RX":null,"TX":"nonsense"}]`
	if err := json.Unmarshal([]byte(body), &users); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if users[0].RX != 1024 || users[0].TX != 2048 {
		t.Errorf("mixed string/number counters decoded as %+v", users[0])
	}
	if users[1].RX != 0 || users[1].TX != 0 {
		t.Errorf("an unparseable counter should read zero, not fail the report: %+v",
			users[1])
	}
}

// Traffic is attributed to the service that carried it (C2). Usage is never
// handed the desired document, so Apply records the id.
func TestUsageAttributesTrafficToTheService(t *testing.T) {
	a, rt := newTestAdapter(t)
	ctx := context.Background()
	converge(t, a, desiredWith(goodParams, 1))

	rt.sessions = []OcctlUser{{Username: "subject-1", Session: "s1", RX: 10, TX: 10}}
	got, err := a.Usage(ctx)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d samples, want 1", len(got))
	}
	if got[0].ServiceID != 7 {
		t.Errorf("ServiceID = %d, want 7; unattributed traffic cannot be priced "+
			"with the service coefficient", got[0].ServiceID)
	}
}

// A configured-but-stopped ocserv is unhealthy; a node with no ocserv at all
// is not. Otherwise every node in the fleet reports red for not running a
// protocol nobody asked it to run.
func TestProbeDistinguishesUnconfiguredFromDown(t *testing.T) {
	a, rt := newTestAdapter(t)
	ctx := context.Background()

	h, err := a.Probe(ctx)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !h.OK {
		t.Errorf("a node with no ocserv service reports unhealthy: %s", h.Detail)
	}

	converge(t, a, desiredWith(goodParams, 1))
	rt.active = false
	h, err = a.Probe(ctx)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if h.OK {
		t.Error("a configured ocserv that is not running reports healthy")
	}
}

// Restart on a node with no ocserv service configured must not silently
// succeed: nothing is running, and reporting ok=true would tell an operator
// their restart worked when there was nothing to restart.
func TestRestart_UnconfiguredReturnsUnsupported(t *testing.T) {
	a, _ := newTestAdapter(t)
	err := a.Restart(context.Background())
	if !errors.Is(err, adapter.ErrRestartUnsupported) {
		t.Errorf("Restart on an unconfigured node = %v, want ErrRestartUnsupported", err)
	}
}

// Restart on a configured node must reach the real systemctl restart call,
// not a reload or a start -- restart is the one that actually bounces the
// process even when the config has not changed.
func TestRestart_ConfiguredCallsRuntimeRestart(t *testing.T) {
	a, rt := newTestAdapter(t)
	converge(t, a, desiredWith(goodParams, 1))
	rt.calls = nil

	if err := a.Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if len(rt.calls) != 1 || rt.calls[0] != "restart" {
		t.Errorf("calls = %v, want exactly [restart]", rt.calls)
	}
}
