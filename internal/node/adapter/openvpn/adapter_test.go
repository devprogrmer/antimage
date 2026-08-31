package openvpn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/amyrm/antimage/internal/node/adapter"
)

type fakeRuntime struct {
	calls  []string
	active bool
	status string
}

func (f *fakeRuntime) Available(context.Context) error { return nil }
func (f *fakeRuntime) Start(context.Context) error     { f.calls = append(f.calls, "start"); return nil }
func (f *fakeRuntime) Stop(context.Context) error      { f.calls = append(f.calls, "stop"); return nil }
func (f *fakeRuntime) Restart(context.Context) error {
	f.calls = append(f.calls, "restart")
	return nil
}
func (f *fakeRuntime) Active(context.Context) bool { return f.active }
func (f *fakeRuntime) ReadStatus(context.Context, string) (string, error) {
	return f.status, nil
}

const goodParams = `{
  "port": 1194,
  "proto": "udp",
  "ca": "/etc/openvpn/ca.crt",
  "server_cert": "/etc/openvpn/server.crt",
  "server_key": "/etc/openvpn/server.key",
  "dh": "none",
  "subnet": "10.8.0.0",
  "netmask": "255.255.255.0"
}`

func newTestAdapter(t *testing.T) (*Adapter, *fakeRuntime) {
	t.Helper()
	rt := &fakeRuntime{active: true}
	return New(rt, t.TempDir(), t.TempDir()), rt
}

func desiredWith(params string, subjectIDs ...int64) adapter.Desired {
	d := adapter.Desired{
		SchemaVersion: 3,
		Services: []adapter.Service{{
			ID: 5, Kind: string(Kind), Enabled: true, Params: json.RawMessage(params),
		}},
	}
	for _, id := range subjectIDs {
		d.Subjects = append(d.Subjects, adapter.Subject{
			ID: id,
			Credentials: []adapter.Credential{
				{Kind: "password", Value: fmt.Sprintf("pw-for-%d", id)},
			},
		})
	}
	return d
}

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

func TestConvergesAndStaysConverged(t *testing.T) {
	a, _ := newTestAdapter(t)
	d := desiredWith(goodParams, 1, 2)
	converge(t, a, d)

	obs, _ := a.Observe(context.Background())
	plan, _ := a.Plan(context.Background(), d, obs)
	if !plan.IsEmpty() {
		t.Errorf("a second plan after convergence wants %d more steps, so the "+
			"adapter would rewrite its config and restart on every pass",
			len(plan.Steps))
	}
}

// Adding a user must not restart the tunnel. The verify script reads the user
// file per login, so a new account costs nothing -- and a restart would drop
// every connected customer to let one new one in.
func TestAddingAUserIsFree(t *testing.T) {
	a, rt := newTestAdapter(t)
	ctx := context.Background()
	converge(t, a, desiredWith(goodParams, 1))
	before := len(rt.calls)

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
		if _, err := a.Apply(ctx, s); err != nil {
			t.Fatalf("Apply: %v", err)
		}
	}
	if len(rt.calls) != before {
		t.Errorf("adding a user touched the service: %v", rt.calls[before:])
	}
}

// A config change restarts. OpenVPN cannot re-read server.conf without one,
// and calling it a reload would tell the reconciler the change is cheaper than
// it is and skip the maintenance window it should have waited for.
func TestAConfigChangeRestarts(t *testing.T) {
	a, _ := newTestAdapter(t)
	ctx := context.Background()
	converge(t, a, desiredWith(goodParams, 1))

	changed := strings.Replace(goodParams, `"port": 1194`, `"port": 1195`, 1)
	obs, _ := a.Observe(ctx)
	plan, err := a.Plan(ctx, desiredWith(changed, 1), obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := plan.MaxDisruption(); got != adapter.DisruptRestart {
		t.Errorf("a config change costs %v, want %v", got, adapter.DisruptRestart)
	}
}

// The three files that decide who may log in must not be readable by other
// users on the host. A salted single-round SHA-256 is only adequate while the
// digest file cannot be read, so this is asserted rather than assumed.
func TestSecretFilesAreNotWorldReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not meaningful on Windows")
	}
	a, _ := newTestAdapter(t)
	converge(t, a, desiredWith(goodParams, 1))

	for name, want := range map[string]os.FileMode{
		usersName:  usersMode,
		verifyName: verifyMode,
		confName:   confMode,
	} {
		info, err := os.Stat(filepath.Join(a.dir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s has mode %04o, want %04o", name, got, want)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s is readable by other users on the host", name)
		}
	}
}

// The digest file must never contain the password itself.
func TestPasswordsAreNotStoredInPlaintext(t *testing.T) {
	a, _ := newTestAdapter(t)
	converge(t, a, desiredWith(goodParams, 7))

	body, err := os.ReadFile(a.usersPath())
	if err != nil {
		t.Fatalf("read users: %v", err)
	}
	if strings.Contains(string(body), "pw-for-7") {
		t.Error("a password appears in the user file in plaintext")
	}
	if !strings.Contains(string(body), "subject-7:") {
		t.Errorf("the account is missing from the user file:\n%s", body)
	}
}

// Two users with the SAME password must not produce the same digest, or the
// file leaks which customers share a password.
func TestSaltsDifferPerUser(t *testing.T) {
	_, d1 := hashPassword(5, "subject-1", "same-password")
	_, d2 := hashPassword(5, "subject-2", "same-password")
	if d1 == d2 {
		t.Error("two accounts with the same password hash identically, so the " +
			"file reveals which customers share one")
	}
	// And the same user on a different service, so one leaked file does not
	// cover another installation.
	_, other := hashPassword(6, "subject-1", "same-password")
	if d1 == other {
		t.Error("the digest does not depend on the service, so one precomputed " +
			"table would cover every installation")
	}
}

// The verify script runs as root on every login with a client-supplied
// username. It must never reach a shell word.
func TestVerifyScriptDoesNotInterpolateTheUsername(t *testing.T) {
	script := renderVerify(5, "/etc/openvpn/server/antimage-users")

	if !strings.Contains(script, `awk -F: -v u="$user"`) {
		t.Error("the username is not passed to awk through -v, so a crafted " +
			"name could be read as part of a command")
	}
	if !strings.Contains(script, "subject-[0-9]*)") {
		t.Error("the script does not reject names this panel never issued " +
			"before using them")
	}
	// via-file, not via-env: with via-env the password sits in an environment
	// every local user can read out of /proc.
	conf := renderConf(5, mustParams(t), "/etc/openvpn/server")
	if !strings.Contains(conf, "via-file") {
		t.Error("auth-user-pass-verify does not use via-file")
	}
	if strings.Contains(conf, "via-env") {
		t.Error("via-env puts the password in the process environment")
	}
}

// A path containing a quote would otherwise close the string in the generated
// script and let the rest be read as code.
func TestUsersPathIsQuotedInTheScript(t *testing.T) {
	script := renderVerify(5, `/etc/o'pn/users`)
	if strings.Contains(script, `'/etc/o'pn/users'`) {
		t.Error("a quote in the path was not escaped, so the rest of the path " +
			"would be read as script")
	}
	if !strings.Contains(script, `'\''`) {
		t.Errorf("expected the quote to be escaped; got:\n%s", script)
	}
}

// Without these three directives every subject fails to connect, and the
// failure looks like a credential problem rather than a config one.
func TestConfigEnablesPasswordAuth(t *testing.T) {
	conf := renderConf(5, mustParams(t), "/etc/openvpn/server")
	for _, want := range []string{
		"verify-client-cert none",
		"username-as-common-name",
		"script-security 2",
		"status-version 2",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("server.conf is missing %q", want)
		}
	}
}

func TestAHandEditedFileIsNotOverwritten(t *testing.T) {
	a, _ := newTestAdapter(t)
	ctx := context.Background()
	d := desiredWith(goodParams, 1)
	converge(t, a, d)

	// The verify script is the one that matters most: it decides who may log
	// in, and silently reverting an operator's change to it would be the worst
	// of the three.
	path := filepath.Join(a.dir, verifyName)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := os.WriteFile(path, append(body, []byte("\n# local change\n")...), verifyMode); err != nil {
		t.Fatalf("hand edit: %v", err)
	}

	obs, err := a.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.Services[0].Managed {
		t.Error("an edited verify script is reported as managed, so the next " +
			"plan would revert it")
	}
	plan, _ := a.Plan(ctx, d, obs)
	if !plan.IsEmpty() {
		t.Errorf("planned %d steps against a hand-edited script", len(plan.Steps))
	}
}

func TestAForeignConfigIsLeftAlone(t *testing.T) {
	a, _ := newTestAdapter(t)
	path := filepath.Join(a.dir, confName)
	if err := os.WriteFile(path, []byte("# hand rolled\nport 1194\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	obs, err := a.Observe(context.Background())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.Services[0].Managed {
		t.Fatal("a config with no marker is reported as managed")
	}
	plan, _ := a.Plan(context.Background(), desiredWith(goodParams, 1), obs)
	if !plan.IsEmpty() {
		t.Errorf("planned %d steps against somebody else's config", len(plan.Steps))
	}
}

func TestBadParamsFailThePlan(t *testing.T) {
	a, _ := newTestAdapter(t)
	for _, tc := range []struct{ name, params string }{
		{"bad proto", strings.Replace(goodParams, `"proto": "udp"`, `"proto": "sctp"`, 1)},
		{"port out of range", strings.Replace(goodParams, `"port": 1194`, `"port": 99999`, 1)},
		{"unknown field", strings.Replace(goodParams, `"port": 1194`, `"port": 1194, "nope": 1`, 1)},
		{"missing ca", `{"port":1194,"proto":"udp","server_cert":"c","server_key":"k","dh":"none","subnet":"10.8.0.0","netmask":"255.255.255.0"}`},
		// A newline in a path would inject a directive into server.conf.
		{"newline in path", strings.Replace(goodParams, `"/etc/openvpn/ca.crt"`, `"/etc/ca.crt\nverb 11"`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := a.Plan(context.Background(),
				desiredWith(tc.params, 1), adapter.Observed{}); err == nil {
				t.Error("Plan accepted params it should refuse")
			}
		})
	}
}

func TestTwoServicesAreRefused(t *testing.T) {
	a, _ := newTestAdapter(t)
	d := desiredWith(goodParams, 1)
	d.Services = append(d.Services, adapter.Service{
		ID: 6, Kind: string(Kind), Enabled: true, Params: json.RawMessage(goodParams),
	})
	if _, err := a.Plan(context.Background(), d, adapter.Observed{}); err == nil {
		t.Error("two openvpn services on one node were accepted")
	}
}

func TestASubjectWithNoPasswordGetsNoAccount(t *testing.T) {
	a, _ := newTestAdapter(t)
	d := desiredWith(goodParams)
	d.Subjects = []adapter.Subject{{ID: 9}}
	converge(t, a, d)

	body, err := os.ReadFile(a.usersPath())
	if err != nil {
		t.Fatalf("read users: %v", err)
	}
	if strings.Contains(string(body), "subject-9") {
		t.Error("a subject with no password credential was given an account, " +
			"which the verify script would accept with an empty password")
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	p := mustParams(t)
	first := renderConf(5, p, "/etc/openvpn/server")
	users := []userEntry{{Name: "subject-1", Password: "x"}}
	firstUsers := renderUsers(5, users)
	for i := 0; i < 5; i++ {
		if renderConf(5, p, "/etc/openvpn/server") != first {
			t.Fatal("renderConf is not deterministic, so every plan would see drift")
		}
		if renderUsers(5, users) != firstUsers {
			t.Fatal("renderUsers is not deterministic, so every plan would see drift")
		}
	}
}

func mustParams(t *testing.T) serviceParams {
	t.Helper()
	p, err := parseServiceParams(json.RawMessage(goodParams))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return p
}

// ACCOUNTING. OpenVPN's status-version 2 output is comma-separated with a
// leading record type; the byte counters sit at fixed indices and the column
// count has grown across releases.

const statusV2 = `TITLE,OpenVPN 2.6.0
TIME,2026-08-30 12:00:00,1756555200
HEADER,CLIENT_LIST,Common Name,Real Address,Virtual Address,Virtual IPv6 Address,Bytes Received,Bytes Sent,Connected Since,Connected Since (time_t),Username,Client ID,Peer ID,Data Channel Cipher
CLIENT_LIST,subject-1,203.0.113.5:51820,10.8.0.2,,1000,2000,2026-08-30 11:00:00,1756551600,subject-1,7,0,AES-256-GCM
CLIENT_LIST,subject-2,203.0.113.6:51821,10.8.0.3,,50,60,2026-08-30 11:30:00,1756553400,subject-2,8,1,AES-256-GCM
ROUTING_TABLE,10.8.0.2,subject-1,203.0.113.5:51820,2026-08-30 11:59:00,1756555140
GLOBAL_STATS,Max bcast/mcast queue length,0
END`

func TestParsesTheStatusFile(t *testing.T) {
	got := parseStatus(statusV2)
	if len(got) != 2 {
		t.Fatalf("parsed %d clients, want 2: %+v", len(got), got)
	}
	if got[0].CommonName != "subject-1" || got[0].Received != 1000 || got[0].Sent != 2000 {
		t.Errorf("first client = %+v", got[0])
	}
	if got[0].ClientID != "7" {
		t.Errorf("client id = %q, want 7; without it a reconnect cannot be told "+
			"from continued use", got[0].ClientID)
	}
	// ROUTING_TABLE and GLOBAL_STATS rows are not clients.
	for _, c := range got {
		if strings.HasPrefix(c.CommonName, "10.8.") {
			t.Errorf("a routing table row was read as a client: %+v", c)
		}
	}
}

// A short row from an older build must be skipped, not read as if the columns
// it does have were the ones we wanted.
func TestShortAndMalformedRowsAreSkipped(t *testing.T) {
	got := parseStatus(`CLIENT_LIST,subject-1,addr,10.8.0.2
CLIENT_LIST,subject-2,addr,10.8.0.3,,notanumber,20,x,y,subject-2,9
CLIENT_LIST,subject-3,addr,10.8.0.4,,5,6,x,y,subject-3,10`)
	if len(got) != 1 {
		t.Fatalf("parsed %d clients, want only the well-formed one: %+v", len(got), got)
	}
	if got[0].CommonName != "subject-3" {
		t.Errorf("kept the wrong row: %+v", got[0])
	}
}

func TestUsageReportsDeltasNotTotals(t *testing.T) {
	a, rt := newTestAdapter(t)
	ctx := context.Background()
	rt.status = statusV2

	first, err := a.Usage(ctx)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("first poll returned %d samples, want 2", len(first))
	}

	// Same clients, counters grown by 100/200.
	rt.status = strings.Replace(statusV2, ",1000,2000,", ",1100,2200,", 1)
	second, err := a.Usage(ctx)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("second poll returned %d samples, want 1 (only subject-1 moved)", len(second))
	}
	if second[0].UplinkBytes != 100 || second[0].DownlinkBytes != 200 {
		t.Errorf("second poll = %+v, want 100 up / 200 down; reporting the total "+
			"again would bill the customer twice", second[0])
	}
}

// A reconnect is a new client id whose counters start at zero.
func TestAReconnectIsNotACounterReset(t *testing.T) {
	a, rt := newTestAdapter(t)
	ctx := context.Background()
	rt.status = `CLIENT_LIST,subject-1,a,10.8.0.2,,9000,9000,x,y,subject-1,7,0,c`
	if _, err := a.Usage(ctx); err != nil {
		t.Fatalf("Usage: %v", err)
	}

	rt.status = `CLIENT_LIST,subject-1,a,10.8.0.2,,25,35,x,y,subject-1,8,0,c`
	got, err := a.Usage(ctx)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if len(got) != 1 || got[0].UplinkBytes != 25 || got[0].DownlinkBytes != 35 {
		t.Errorf("after a reconnect = %+v, want 25 up / 35 down", got)
	}
}

// duplicate-cn lets one customer hold several connections at once.
func TestConcurrentConnectionsSumToOneSubject(t *testing.T) {
	a, rt := newTestAdapter(t)
	rt.status = `CLIENT_LIST,subject-4,a,10.8.0.2,,10,20,x,y,subject-4,1,0,c
CLIENT_LIST,subject-4,b,10.8.0.3,,5,7,x,y,subject-4,2,0,c`
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

func TestForeignCommonNamesAreNotBilled(t *testing.T) {
	a, rt := newTestAdapter(t)
	rt.status = `CLIENT_LIST,ops-tester,a,10.8.0.9,,999,999,x,y,ops-tester,1,0,c`
	got, err := a.Usage(context.Background())
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("billed %d samples for a common name the panel never issued: %+v",
			len(got), got)
	}
}

func TestUsageAttributesTrafficToTheService(t *testing.T) {
	a, rt := newTestAdapter(t)
	ctx := context.Background()
	converge(t, a, desiredWith(goodParams, 1))

	rt.status = `CLIENT_LIST,subject-1,a,10.8.0.2,,10,10,x,y,subject-1,1,0,c`
	got, err := a.Usage(ctx)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d samples, want 1", len(got))
	}
	if got[0].ServiceID != 5 {
		t.Errorf("ServiceID = %d, want 5; unattributed traffic cannot be priced "+
			"with the service coefficient", got[0].ServiceID)
	}
}

// A missing status file is an idle server, not a broken one.
func TestUsageOnAServerThatHasWrittenNoStatusYet(t *testing.T) {
	a, _ := newTestAdapter(t)
	got, err := a.Usage(context.Background())
	if err != nil {
		t.Fatalf("Usage on an empty status file failed: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d samples from no status file", len(got))
	}
}

// OpenVPN starts happily without the verify script and then refuses every
// login, which looks like a credential problem to everyone involved.
func TestProbeCallsOutAMissingVerifyScript(t *testing.T) {
	a, rt := newTestAdapter(t)
	ctx := context.Background()

	h, _ := a.Probe(ctx)
	if !h.OK {
		t.Errorf("a node with no openvpn service reports unhealthy: %s", h.Detail)
	}

	converge(t, a, desiredWith(goodParams, 1))
	if err := os.Remove(filepath.Join(a.dir, verifyName)); err != nil {
		t.Fatalf("remove script: %v", err)
	}
	h, _ = a.Probe(ctx)
	if h.OK {
		t.Error("a configured openvpn with no verify script reports healthy; " +
			"every login would fail and nothing would say why")
	}

	rt.active = false
	h, _ = a.Probe(ctx)
	if h.OK {
		t.Error("a stopped openvpn reports healthy")
	}
}

// The verify script is the entire authentication decision, and a shell quoting
// mistake in it is an authentication bypass. Reading the string cannot find
// that; running it can.
//
// This is in the ORDINARY suite rather than only the realruntime job because
// it needs nothing but a POSIX shell, and an auth bypass should not wait for a
// job that only runs on one CI lane. On Linux -- the only platform a node runs
// on -- a missing shell is a failure, not a skip.
func TestVerifyScriptUnderARealShell(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("no POSIX shell on this Windows machine; the node platform is Linux")
		}
		t.Fatalf("sh is required to verify the login script and was not found: %v", err)
	}
	if _, err := exec.LookPath("sha256sum"); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("no sha256sum on this Windows machine")
		}
		t.Fatalf("sha256sum is required by the verify script: %v", err)
	}

	dir := t.TempDir()
	a := New(&fakeRuntime{}, dir, t.TempDir())
	const serviceID = 3

	if err := a.writeUsers(serviceID, []userEntry{
		{Name: "subject-1", Password: "correct horse battery staple"},
		{Name: "subject-2", Password: "another one entirely"},
	}); err != nil {
		t.Fatalf("write users: %v", err)
	}
	if err := a.writeVerify(serviceID); err != nil {
		t.Fatalf("write verify: %v", err)
	}
	script := filepath.Join(dir, verifyName)

	accepts := func(user, pass string) bool {
		creds := filepath.Join(t.TempDir(), "creds")
		if err := os.WriteFile(creds, []byte(user+"\n"+pass+"\n"), 0o600); err != nil {
			t.Fatalf("write creds: %v", err)
		}
		return exec.Command("sh", script, creds).Run() == nil
	}

	if !accepts("subject-1", "correct horse battery staple") {
		t.Error("the shell rejected a CORRECT password; every customer would be " +
			"locked out and the panel would look fine")
	}
	for _, tc := range []struct{ name, user, pass string }{
		{"wrong password", "subject-1", "wrong"},
		{"another account's password", "subject-2", "correct horse battery staple"},
		{"an account that does not exist", "subject-999", "anything"},
		{"empty credentials", "", ""},
	} {
		if accepts(tc.user, tc.pass) {
			t.Errorf("AUTHENTICATION BYPASS: %s was accepted", tc.name)
		}
	}

	// A username shaped to break out of a shell word. It must be rejected as a
	// name -- and must not have executed.
	marker := filepath.Join(dir, "pwned")
	if accepts("subject-1; touch "+marker, "irrelevant") {
		t.Error("AUTHENTICATION BYPASS: a crafted username was accepted")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("COMMAND INJECTION: the client-supplied username was executed by the shell")
	}
}

func TestRestart_UnconfiguredReturnsUnsupported(t *testing.T) {
	a, _ := newTestAdapter(t)
	err := a.Restart(context.Background())
	if !errors.Is(err, adapter.ErrRestartUnsupported) {
		t.Errorf("Restart on an unconfigured node = %v, want ErrRestartUnsupported", err)
	}
}

func TestRestart_ConfiguredCallsRuntimeRestart(t *testing.T) {
	a, rt := newTestAdapter(t)
	converge(t, a, desiredWith(goodParams))
	rt.calls = nil

	if err := a.Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if len(rt.calls) != 1 || rt.calls[0] != "restart" {
		t.Errorf("calls = %v, want exactly [restart]", rt.calls)
	}
}
