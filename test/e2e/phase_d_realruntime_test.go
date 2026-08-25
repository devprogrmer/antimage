//go:build e2e && realruntime

// Real-runtime verification for the WireGuard and Hysteria2 adapters (AD-3).
//
// Everything else covering these two drives a fake Runtime. That proves the
// adapter's logic and never once asks the real tooling whether the bytes the
// adapter writes are loadable -- which is the question that matters, because
// both adapters were shipped with Apply returning "not yet implemented" and
// nothing ever executed their ExecRuntime at all.
//
// Requires -tags "e2e realruntime" AND the binaries. The tag is the opt-in;
// once it is set a missing binary FAILS rather than skips, because skipping is
// how a real-runtime gap hides.
package e2e

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/node/adapter"
	"github.com/amyrm/antimage/internal/node/adapter/hysteria2"
	"github.com/amyrm/antimage/internal/node/adapter/wireguard"
)

// ---------------------------------------------------------------- WireGuard

// TestRealRuntimeWireGuardConfigIsAccepted proves the real wg tooling parses
// what the adapter generates.
//
// `wg-quick strip` runs wg-quick's own parser over the file and prints the
// wg-compatible result, without touching the network or needing root. It is the
// same class of check the xray and sing-box tests make with `check -C`: the
// binary itself, not our reading of its documentation, decides whether the
// config is valid.
func TestRealRuntimeWireGuardConfigIsAccepted(t *testing.T) {
	wgQuick := binaryFor(t, "WG_QUICK_BINARY", "wg-quick")
	wgBin := binaryFor(t, "WG_BINARY", "wg")

	dir := t.TempDir()
	a := wireguard.New(wireguard.NewExecRuntime(), dir, t.TempDir())

	desired := wireguardDesired(t, 2)
	plan, err := a.Plan(context.Background(), desired, adapter.Observed{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Steps) != 1 || plan.Steps[0].Kind != "install" {
		t.Fatalf("expected one install step, got %+v", plan.Steps)
	}
	if len(plan.Steps[0].Payload) == 0 {
		t.Fatal("install step carries no payload: Apply would have nothing to write")
	}

	// Write the config the way Apply does, then hand it to the real parser.
	// The interface is deliberately NOT brought up: that needs root and a
	// kernel module, and is covered by the lifecycle test below.
	cfg := payloadConfig(t, plan.Steps[0].Payload)
	path := filepath.Join(dir, "antimage-10.conf")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	out, err := exec.Command(wgQuick, "strip", path).CombinedOutput()
	if err != nil {
		t.Fatalf("wg-quick rejected the generated config: %v\n%s\n--- config ---\n%s",
			err, strings.TrimSpace(string(out)), cfg)
	}
	stripped := string(out)

	// Both peers must survive the round trip. A config wg-quick parses but that
	// carries no peers would serve nobody while looking healthy.
	for _, subj := range desired.Subjects {
		pub := publicKeyOf(t, wgBin, credentialOf(t, subj, "keypair"))
		if !strings.Contains(stripped, pub) {
			t.Errorf("peer %d (%s) is missing from the parsed config:\n%s",
				subj.ID, pub, stripped)
		}
	}

	// And wg itself must accept the stripped form -- `wg setconf` against a
	// non-existent interface fails on the interface, not the file, so parse
	// errors surface distinctly.
	strippedPath := filepath.Join(dir, "stripped.conf")
	if err := os.WriteFile(strippedPath, out, 0o600); err != nil {
		t.Fatalf("write stripped: %v", err)
	}
	check := exec.Command(wgBin, "setconf", "antimage-nonexistent", strippedPath)
	checkOut, _ := check.CombinedOutput()
	if strings.Contains(strings.ToLower(string(checkOut)), "line") ||
		strings.Contains(strings.ToLower(string(checkOut)), "parse") {
		t.Errorf("wg reported a parse error in the generated config: %s", checkOut)
	}
}

// TestRealRuntimeWireGuardLifecycle brings a real interface up, reconfigures it
// through the adapter, and tears it down.
//
// This needs root and the WireGuard kernel module, so the job runs it under
// sudo. It FAILS rather than skips without them, for the reason in the package
// comment: an install/restart path that has never actually run is exactly what
// AD-3 was about.
func TestRealRuntimeWireGuardLifecycle(t *testing.T) {
	requireRoot(t)
	_ = binaryFor(t, "WG_QUICK_BINARY", "wg-quick")

	ctx := context.Background()
	dir := t.TempDir()
	rt := wireguard.NewExecRuntime()
	a := wireguard.New(rt, dir, t.TempDir())

	if err := rt.Available(ctx); err != nil {
		t.Fatalf("wireguard runtime unavailable: %v", err)
	}

	desired := wireguardDesired(t, 1)
	svcID := desired.Services[0].ID
	iface := fmt.Sprintf("antimage-%d", svcID)
	t.Cleanup(func() {
		_ = exec.Command("wg-quick", "down", filepath.Join(dir, iface+".conf")).Run()
	})

	// INSTALL: from nothing to a live interface.
	plan, err := a.Plan(ctx, desired, adapter.Observed{})
	if err != nil {
		t.Fatalf("Plan install: %v", err)
	}
	mustApplyAll(t, a, plan, "install")

	if !interfaceIsUp(t, iface) {
		t.Fatalf("interface %s is not up after a successful install", iface)
	}

	// OBSERVE: the adapter must see what it just built, and call it managed.
	obs, err := a.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(obs.Services) != 1 || obs.Services[0].ID != svcID {
		t.Fatalf("Observe did not see the installed service: %+v", obs.Services)
	}
	if !obs.Services[0].Managed {
		t.Error("the adapter reported its own freshly written config as unmanaged")
	}

	// CONVERGENCE: a second Plan over that observation must be empty. This is
	// the defect this phase fixed, checked against a real interface rather than
	// a constructed Observed.
	settled, err := a.Plan(ctx, desired, obs)
	if err != nil {
		t.Fatalf("Plan converged: %v", err)
	}
	if !settled.IsEmpty() {
		t.Fatalf("a live, correct interface still planned %d step(s) (%s); "+
			"every reconcile would drop every session",
			len(settled.Steps), settled.Steps[0].Kind)
	}

	// ADD A PEER: must be applied, and the interface must still be up after.
	grown := wireguardDesired(t, 2)
	growPlan, err := a.Plan(ctx, grown, obs)
	if err != nil {
		t.Fatalf("Plan grow: %v", err)
	}
	if growPlan.IsEmpty() {
		t.Fatal("adding a peer planned nothing; the new user would never connect")
	}
	mustApplyAll(t, a, growPlan, "add a peer")
	if !interfaceIsUp(t, iface) {
		t.Fatalf("interface %s went down while adding a peer", iface)
	}

	// The real interface must actually be serving both peers now.
	peers := listPeers(t, iface)
	if len(peers) != 2 {
		t.Errorf("interface is serving %d peers after adding one, want 2: %v",
			len(peers), peers)
	}

	// And it has converged again.
	obs2, err := a.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe after grow: %v", err)
	}
	if settled2, err := a.Plan(ctx, grown, obs2); err != nil {
		t.Fatalf("Plan after grow: %v", err)
	} else if !settled2.IsEmpty() {
		t.Errorf("still planning %d step(s) after a successful peer addition",
			len(settled2.Steps))
	}

	// REMOVE: the interface goes away and so does the config.
	removePlan, err := a.Plan(ctx, adapter.Desired{SchemaVersion: 1, NodeID: 1}, obs2)
	if err != nil {
		t.Fatalf("Plan remove: %v", err)
	}
	mustApplyAll(t, a, removePlan, "remove")
	if interfaceIsUp(t, iface) {
		t.Errorf("interface %s is still up after removal", iface)
	}
}

// ---------------------------------------------------------------- Hysteria2

// TestRealRuntimeHysteria2ConfigIsAccepted starts the real hysteria server on
// the generated config and requires it to come up.
//
// Hysteria has no offline "check this config" subcommand, so acceptance is
// proven the only way it can be: the real binary is asked to run it. A config
// it refuses exits almost immediately with the parse error on stderr, which is
// what this distinguishes from a server that started and is listening.
func TestRealRuntimeHysteria2ConfigIsAccepted(t *testing.T) {
	binary := binaryFor(t, "HYSTERIA_BINARY", "hysteria")

	dir := t.TempDir()
	certFile, keyFile := selfSignedCert(t, dir)
	port := freeUDPPort(t)

	a := hysteria2.New(hysteria2.NewExecRuntime(), dir, t.TempDir())
	desired := hysteria2Desired(t, 2, port, certFile, keyFile)

	plan, err := a.Plan(context.Background(), desired, adapter.Observed{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Steps) != 1 || plan.Steps[0].Kind != "install" {
		t.Fatalf("expected one install step, got %+v", plan.Steps)
	}
	cfg := payloadConfig(t, plan.Steps[0].Payload)

	path := filepath.Join(dir, "antimage-10.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "server", "--config", path)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start hysteria: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	// A rejected config dies fast. A listening server keeps running, so
	// surviving the wait IS the acceptance signal.
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	select {
	case err := <-exited:
		t.Fatalf("hysteria rejected the generated config (exited: %v):\n%s\n"+
			"--- config ---\n%s", err, strings.TrimSpace(out.String()), cfg)
	case <-time.After(5 * time.Second):
		// Still running: the config parsed and the listener bound.
	}

	// The marker comment must not have upset the YAML parser -- it is a comment
	// by construction, but that is exactly the kind of assumption this test
	// exists to check against the real thing.
	if !strings.HasPrefix(cfg, "# antimage:") {
		t.Errorf("config does not begin with the marker: %.60q", cfg)
	}
}

// TestRealRuntimeHysteria2Lifecycle drives install, converge, add a user and
// remove against the real binary under a systemd shim.
func TestRealRuntimeHysteria2Lifecycle(t *testing.T) {
	binary := binaryFor(t, "HYSTERIA_BINARY", "hysteria")

	shimDir := buildShim(t)
	stateDir := t.TempDir()
	t.Setenv("SHIM_STATE", stateDir)
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx := context.Background()
	dir := t.TempDir()
	certFile, keyFile := selfSignedCert(t, dir)
	port := freeUDPPort(t)

	rt := hysteria2.NewExecRuntime()
	a := hysteria2.New(rt, dir, t.TempDir())
	if err := rt.Available(ctx); err != nil {
		t.Fatalf("hysteria2 runtime unavailable: %v", err)
	}

	desired := hysteria2Desired(t, 1, port, certFile, keyFile)
	svcID := desired.Services[0].ID

	// Register a unit spec the shim can start, pointing at the real binary.
	// The shim reads <SHIM_STATE>/<unit>.json, which is the same convention the
	// SP2 real-runtime harness uses.
	unit := fmt.Sprintf("hysteria-server@antimage-%d", svcID)
	cfgPath := filepath.Join(dir, fmt.Sprintf("antimage-%d.yaml", svcID))
	writeUnitSpec(t, stateDir, unit, binary, []string{"server", "--config", cfgPath}, port, "udp")

	plan, err := a.Plan(ctx, desired, adapter.Observed{})
	if err != nil {
		t.Fatalf("Plan install: %v", err)
	}
	mustApplyAll(t, a, plan, "install")

	obs, err := a.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(obs.Services) != 1 {
		t.Fatalf("Observe saw %d services, want 1: %+v", len(obs.Services), obs.Services)
	}
	if !obs.Services[0].Managed {
		t.Error("the adapter reported its own freshly written config as unmanaged")
	}

	// Convergence, against real files.
	settled, err := a.Plan(ctx, desired, obs)
	if err != nil {
		t.Fatalf("Plan converged: %v", err)
	}
	if !settled.IsEmpty() {
		t.Fatalf("a correct, running service still planned %d step(s) (%s); "+
			"every reconcile would restart it",
			len(settled.Steps), settled.Steps[0].Kind)
	}

	// Adding a user is planned and applied, and the config on disk grows.
	grown := hysteria2Desired(t, 2, port, certFile, keyFile)
	growPlan, err := a.Plan(ctx, grown, obs)
	if err != nil {
		t.Fatalf("Plan grow: %v", err)
	}
	if growPlan.IsEmpty() {
		t.Fatal("adding a user planned nothing; the new subscriber would never authenticate")
	}
	mustApplyAll(t, a, growPlan, "add a user")

	body, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("antimage-%d.yaml", svcID)))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if got := strings.Count(string(body), "username:"); got != 2 {
		t.Errorf("config carries %d users after adding one, want 2:\n%s", got, body)
	}

	// Remove takes the config away.
	obs2, err := a.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe after grow: %v", err)
	}
	removePlan, err := a.Plan(ctx, adapter.Desired{SchemaVersion: 1, NodeID: 1}, obs2)
	if err != nil {
		t.Fatalf("Plan remove: %v", err)
	}
	mustApplyAll(t, a, removePlan, "remove")
	if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("antimage-%d.yaml", svcID))); !os.IsNotExist(err) {
		t.Error("config survived removal")
	}
}

// ------------------------------------------------------------------ helpers

func requireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Fatalf("this test brings up a real WireGuard interface and needs root; " +
			"the realruntime job runs it under sudo. It fails rather than skips " +
			"because an install path that has never run is what AD-3 was about.")
	}
}

func mustApplyAll(t *testing.T, a adapter.Adapter, plan adapter.Plan, what string) {
	t.Helper()
	if plan.IsEmpty() {
		t.Fatalf("%s: nothing to apply", what)
	}
	for i, step := range plan.Steps {
		step.Seq = i
		res, err := a.Apply(context.Background(), step)
		if err != nil {
			t.Fatalf("%s: Apply step %d (%s) errored: %v", what, i, step.Kind, err)
		}
		if !res.OK {
			t.Fatalf("%s: Apply step %d (%s) failed: %s", what, i, step.Kind, res.Err)
		}
		if res.Kind != step.Kind {
			t.Errorf("%s: result echoes kind %q for a %q step", what, res.Kind, step.Kind)
		}
	}
}

func payloadConfig(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var p struct {
		Config string `json:"config"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if p.Config == "" {
		t.Fatal("step payload carries no config")
	}
	return p.Config
}

func credentialOf(t *testing.T, subj adapter.Subject, kind string) string {
	t.Helper()
	for _, c := range subj.Credentials {
		if c.Kind == kind {
			return c.Value
		}
	}
	t.Fatalf("subject %d has no %s credential", subj.ID, kind)
	return ""
}

// publicKeyOf derives a public key with the real wg binary, so the test does
// not reimplement the derivation it is checking.
func publicKeyOf(t *testing.T, wgBin, privateKey string) string {
	t.Helper()
	cmd := exec.Command(wgBin, "pubkey")
	cmd.Stdin = strings.NewReader(privateKey + "\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("wg pubkey: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func interfaceIsUp(t *testing.T, iface string) bool {
	t.Helper()
	out, err := exec.Command("wg", "show", "interfaces").Output()
	if err != nil {
		return false
	}
	for _, f := range strings.Fields(string(out)) {
		if f == iface {
			return true
		}
	}
	return false
}

func listPeers(t *testing.T, iface string) []string {
	t.Helper()
	out, err := exec.Command("wg", "show", iface, "peers").Output()
	if err != nil {
		t.Fatalf("wg show peers: %v", err)
	}
	return strings.Fields(string(out))
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = conn.Close() }()
	return conn.LocalAddr().(*net.UDPAddr).Port
}

// selfSignedCert writes a throwaway certificate. Hysteria2 requires TLS and
// refuses to start without a usable pair, so this is what makes the acceptance
// test test the config rather than the absence of a certificate.
func selfSignedCert(t *testing.T, dir string) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "antimage-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     []string{"antimage-test"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certFile, keyFile
}

// wireguardDesired builds a desired document with `users` peers. The private
// keys are valid base64 of 32 bytes, which is what wg requires.
func wireguardDesired(t *testing.T, users int) adapter.Desired {
	t.Helper()
	params := `{"port":51820,"subnet":"10.99.0.1/24","private_key":"` + wgKey(0) + `"}`
	d := adapter.Desired{
		SchemaVersion: 1, Revision: 1, NodeID: 1,
		Services: []adapter.Service{
			{ID: 10, Kind: "wireguard", Enabled: true, Params: json.RawMessage(params)},
		},
	}
	for i := 1; i <= users; i++ {
		d.Subjects = append(d.Subjects, adapter.Subject{
			ID:          int64(i),
			Credentials: []adapter.Credential{{Kind: "keypair", Value: wgKey(i)}},
		})
	}
	return d
}

// wgKey returns a deterministic, valid 32-byte base64 key. Deterministic so a
// failure is reproducible; valid so wg does not reject it for the wrong reason.
func wgKey(seed int) string {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte((i*7 + seed*31 + 1) % 251)
	}
	// Clamp as a curve25519 private key so wg accepts it.
	raw[0] &= 248
	raw[31] &= 127
	raw[31] |= 64
	return base64Std(raw)
}

func hysteria2Desired(t *testing.T, users, port int, certFile, keyFile string) adapter.Desired {
	t.Helper()
	params := fmt.Sprintf(`{"port":%d,"password":"supersecret1","cert_file":%q,"key_file":%q}`,
		port, certFile, keyFile)
	d := adapter.Desired{
		SchemaVersion: 1, Revision: 1, NodeID: 1,
		Services: []adapter.Service{
			{ID: 10, Kind: "hysteria2", Enabled: true, Params: json.RawMessage(params)},
		},
	}
	for i := 1; i <= users; i++ {
		d.Subjects = append(d.Subjects, adapter.Subject{
			ID:          int64(i),
			Credentials: []adapter.Credential{{Kind: "password", Value: fmt.Sprintf("pw-secret-%d", i)}},
		})
	}
	return d
}

// writeUnitSpec registers a unit with the systemctl shim, in the same JSON
// form the SP2 real-runtime harness uses.
func writeUnitSpec(t *testing.T, stateDir, unit, binary string, args []string, port int, network string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"path": binary, "args": args, "port": port, "network": network,
	})
	if err != nil {
		t.Fatalf("encode unit: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, unit+".json"), body, 0o600); err != nil {
		t.Fatalf("write unit spec: %v", err)
	}
}

// base64Std is the standard encoding wg expects for keys.
func base64Std(raw []byte) string { return base64.StdEncoding.EncodeToString(raw) }
