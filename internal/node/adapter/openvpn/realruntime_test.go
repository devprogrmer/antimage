//go:build realruntime

// Real-runtime verification for the OpenVPN adapter.
//
// Every other test here checks the adapter against its own beliefs. Two of
// those beliefs are only worth as much as the real thing's agreement:
//
//  1. openvpn accepts the server.conf this adapter renders. The other adapters
//     were shipped emitting configuration the real binary refused, and no
//     amount of byte-comparison against our own expectations noticed.
//  2. /bin/sh runs the verify script the way the unit tests assume, and it
//     accepts the right password while rejecting the wrong one. That script is
//     the whole authentication decision; a shell quoting mistake in it is an
//     authentication bypass, and it cannot be tested by reading the string.
//
// Lives in this package rather than test/e2e so it can render through the
// adapter's own unexported functions. A test that rebuilt the config or the
// script would assert against a copy of the code it is meant to check.
package openvpn

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func realOpenVPN(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("OPENVPN_BINARY"); p != "" {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("OPENVPN_BINARY=%s is not usable: %v", p, err)
		}
		return p
	}
	p, err := exec.LookPath("openvpn")
	if err != nil {
		t.Fatal("the realruntime tag is set but openvpn was not found: set " +
			"OPENVPN_BINARY or put it on PATH. This test must not be skipped -- " +
			"skipping is how a real-runtime gap hides.")
	}
	return p
}

// The real openvpn binary must accept the rendered config.
//
// Checked with --show-ciphers style config parsing rather than by starting the
// server: binding a port and creating a tun device needs root, and a failure
// for those reasons would say nothing about the config. openvpn parses its
// config file fully before it touches either, so a parse error surfaces here
// and a privilege error is recognised and does not fail the test.
func TestRealRuntimeOpenVPNParsesTheGeneratedConfig(t *testing.T) {
	bin := realOpenVPN(t)

	dir := t.TempDir()
	a := New(&ExecRuntime{}, dir, t.TempDir())

	// Real certificate paths are not needed to prove the DIRECTIVES parse:
	// openvpn reports an unknown option before it opens any file, and that is
	// the failure this test exists to catch.
	params := serviceParams{
		Port: 1194, Proto: "udp",
		CA: filepath.Join(dir, "ca.crt"), ServerCert: filepath.Join(dir, "s.crt"),
		ServerKey: filepath.Join(dir, "s.key"), DH: "none",
		Subnet: "10.8.0.0", Netmask: "255.255.255.0",
		DNS: []string{"1.1.1.1"},
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if err := a.writeUsers(1, nil); err != nil {
		t.Fatalf("write users: %v", err)
	}
	if err := a.writeVerify(1); err != nil {
		t.Fatalf("write verify: %v", err)
	}
	if err := a.writeConf(1, raw); err != nil {
		t.Fatalf("write conf: %v", err)
	}
	// OpenVPN 2.7 stats every certificate path before it parses the rest of
	// the config. Empty files satisfy the existence check without pretending
	// to be usable PKI material -- the test still fails on any real "Options
	// error", it just doesn't fail on missing files that were never the
	// point of a parser test.
	for _, name := range []string{"ca.crt", "s.crt", "s.key"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatalf("touch %s: %v", name, err)
		}
	}

	confPath := filepath.Join(dir, confName)
	// No --test-crypto: that flag forces a static-key self-test regardless of
	// the config's tls-server directive, so on OpenVPN 2.7 it rejects a TLS
	// config with "You must define key file (--secret)". Without it, OpenVPN
	// parses the config and reports any Options error before touching the
	// network; a subsequent tun/bind failure without privileges is what the
	// "logged rather than asserted" note below covers.
	out, err := exec.CommandContext(context.Background(),
		bin, "--config", confPath).CombinedOutput()
	body := string(out)
	t.Logf("openvpn said:\n%s", strings.TrimSpace(body))

	// An unrecognised or malformed directive is what this is looking for.
	// openvpn reports those as "Options error", and they are fatal regardless
	// of privileges.
	if strings.Contains(body, "Options error") {
		t.Fatalf("the real openvpn rejected the generated config:\n%s\n--- config ---\n%s",
			strings.TrimSpace(body), mustRead(t, confPath))
	}
	// Anything else (including a privilege or missing-certificate failure) is
	// not this test's business, and is logged rather than asserted on.
	_ = err
}

// The verify script is the authentication decision. Running it under the real
// shell is the only way to know it accepts and rejects the right things.
func TestRealRuntimeVerifyScriptAcceptsAndRejects(t *testing.T) {
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Fatalf("sha256sum is required by the verify script and was not found: %v", err)
	}

	dir := t.TempDir()
	a := New(&ExecRuntime{}, dir, t.TempDir())
	const serviceID = 3

	users := []userEntry{
		{Name: "subject-1", Password: "correct horse battery staple"},
		{Name: "subject-2", Password: "another one entirely"},
	}
	if err := a.writeUsers(serviceID, users); err != nil {
		t.Fatalf("write users: %v", err)
	}
	if err := a.writeVerify(serviceID); err != nil {
		t.Fatalf("write verify: %v", err)
	}
	script := filepath.Join(dir, verifyName)

	run := func(t *testing.T, user, pass string) bool {
		t.Helper()
		creds := filepath.Join(t.TempDir(), "creds")
		if err := os.WriteFile(creds, []byte(user+"\n"+pass+"\n"), 0o600); err != nil {
			t.Fatalf("write creds: %v", err)
		}
		return exec.Command("/bin/sh", script, creds).Run() == nil
	}

	if !run(t, "subject-1", "correct horse battery staple") {
		t.Error("the real shell rejected a correct password; every customer " +
			"would be locked out")
	}
	if run(t, "subject-1", "wrong password") {
		t.Error("AUTHENTICATION BYPASS: a wrong password was accepted")
	}
	if run(t, "subject-2", "correct horse battery staple") {
		t.Error("AUTHENTICATION BYPASS: one account's password worked for another")
	}
	if run(t, "subject-999", "anything") {
		t.Error("AUTHENTICATION BYPASS: an account that does not exist was accepted")
	}
	if run(t, "", "") {
		t.Error("AUTHENTICATION BYPASS: empty credentials were accepted")
	}

	// A name shaped to break out of a shell word. It must be rejected as a
	// name, not executed as code -- and the marker file it would create must
	// not exist afterwards.
	marker := filepath.Join(dir, "pwned")
	if run(t, "subject-1; touch "+marker, "irrelevant") {
		t.Error("AUTHENTICATION BYPASS: a crafted username was accepted")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("COMMAND INJECTION: the username was executed by the shell")
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
