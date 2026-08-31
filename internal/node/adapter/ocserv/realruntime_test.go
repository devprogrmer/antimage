//go:build realruntime

// Real-runtime verification for the ocserv adapter.
//
// Every other test here drives a fake Runtime whose SetPassword writes a file
// shaped the way this package BELIEVES ocpasswd writes one. That proves the
// adapter's logic against its own assumption, which is worth nothing if the
// assumption is wrong -- and it is the riskiest thing in the adapter, because
// the whole user-management path depends on parsing a format nobody here
// controls.
//
// Lives in this package rather than test/e2e so it can drive the adapter's own
// unexported reader. A test that reimplemented the parser would assert against
// a copy of the code it is meant to be checking.
//
// WHAT THIS COVERS: the real ocpasswd binary accepting the stdin the adapter
// feeds it, writing a file the adapter's parser reads back, and deleting the
// account the adapter asked it to delete.
//
// WHAT IT DOES NOT COVER, stated rather than implied: whether the ocserv
// DAEMON accepts the generated ocserv.conf. Starting ocserv needs root, a tun
// device and a bindable privileged port; a test that started it without them
// would fail for reasons unrelated to the config, which is worse than no test
// because it teaches everyone to ignore the failure. The config's shape is
// covered by unit tests. Its acceptance by the daemon is not covered anywhere,
// and that gap is real.
package ocserv

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// realOcpasswd resolves the binary. The tag is the opt-in; once it is set, a
// missing binary FAILS rather than skips, because skipping is how a
// real-runtime gap hides.
func realOcpasswd(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("OCPASSWD_BINARY"); p != "" {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("OCPASSWD_BINARY=%s is not usable: %v", p, err)
		}
		return p
	}
	p, err := exec.LookPath("ocpasswd")
	if err != nil {
		t.Fatal("the realruntime tag is set but ocpasswd was not found: set " +
			"OCPASSWD_BINARY or put it on PATH. This test must not be skipped -- " +
			"skipping is how a real-runtime gap hides.")
	}
	return p
}

func TestRealRuntimeOcpasswdRoundTrip(t *testing.T) {
	bin := realOcpasswd(t)

	dir := t.TempDir()
	rt := NewExecRuntime("ocserv", bin, "occtl")
	a := New(rt, dir, t.TempDir())
	passwd := a.passwdPath()
	ctx := context.Background()

	if err := rt.Available(ctx); err != nil {
		t.Fatalf("ocpasswd reported unavailable while sitting on PATH: %v", err)
	}

	const secret = "correct horse battery staple"
	for _, u := range []struct{ name, pw string }{
		{"subject-1", secret},
		{"subject-2", "another-strong-password"},
	} {
		if err := rt.SetPassword(ctx, passwd, u.name, u.pw); err != nil {
			t.Fatalf("real ocpasswd refused the stdin the adapter feeds it for "+
				"%s: %v", u.name, err)
		}
	}

	body, err := os.ReadFile(passwd)
	if err != nil {
		t.Fatalf("ocpasswd wrote no file at %s: %v", passwd, err)
	}
	t.Logf("real ocpasswd wrote:\n%s", strings.TrimSpace(string(body)))

	// The adapter's own reader, against the real file. This is the assertion
	// the fake runtime cannot make.
	names, err := a.readUsernames()
	if err != nil {
		t.Fatalf("the adapter could not read a real ocpasswd file: %v", err)
	}
	if !contains(names, "subject-1") || !contains(names, "subject-2") {
		t.Fatalf("the adapter parsed %v from a real ocpasswd file that should hold "+
			"subject-1 and subject-2; the assumed \"user:group:hash\" format is wrong",
			names)
	}

	// ocpasswd stores a salted hash. An adapter that somehow caused a plaintext
	// write would put every customer's password into every backup.
	if strings.Contains(string(body), secret) {
		t.Error("a password appears in the passwd file in plaintext")
	}

	// The salt is why the passwd file is checksummed by NAME rather than by
	// content: writing the same user twice must not look like drift.
	before := a.mustUsernames(t)
	if err := rt.SetPassword(ctx, passwd, "subject-1", secret); err != nil {
		t.Fatalf("rewriting an existing account failed: %v", err)
	}
	if after := a.mustUsernames(t); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Errorf("rewriting the same account changed the account set from %v to %v",
			before, after)
	}

	// Delete one, and only one.
	if err := rt.DeletePassword(ctx, passwd, "subject-1"); err != nil {
		t.Fatalf("real ocpasswd refused the delete the adapter issues: %v", err)
	}
	names = a.mustUsernames(t)
	if contains(names, "subject-1") {
		t.Error("subject-1 survived a delete through the real ocpasswd")
	}
	if !contains(names, "subject-2") {
		t.Error("deleting subject-1 removed subject-2 as well")
	}
}

// syncUsers against the real binary: the reconciliation, not just one write.
func TestRealRuntimeSyncUsersConvergesAgainstTheRealTool(t *testing.T) {
	bin := realOcpasswd(t)

	dir := t.TempDir()
	a := New(NewExecRuntime("ocserv", bin, "occtl"), dir, t.TempDir())
	ctx := context.Background()

	want := []userEntry{
		{Name: "subject-10", Password: "pw-ten-is-long-enough"},
		{Name: "subject-11", Password: "pw-eleven-is-long-enough"},
	}
	if err := a.syncUsers(ctx, want); err != nil {
		t.Fatalf("syncUsers: %v", err)
	}
	if got := a.mustUsernames(t); len(got) != 2 {
		t.Fatalf("after sync the host holds %v, want two accounts", got)
	}

	// Drop one from desired; it must go, and only it.
	if err := a.syncUsers(ctx, want[:1]); err != nil {
		t.Fatalf("syncUsers (shrink): %v", err)
	}
	got := a.mustUsernames(t)
	if contains(got, "subject-11") {
		t.Error("an account removed from desired state survived on the host")
	}
	if !contains(got, "subject-10") {
		t.Error("an account still in desired state was removed")
	}

	// Running the same sync twice must do nothing the second time -- the step
	// is retried after a partial failure, so it has to be idempotent against
	// the real tool, not just against the fake.
	if err := a.syncUsers(ctx, want[:1]); err != nil {
		t.Fatalf("syncUsers (repeat): %v", err)
	}
	if again := a.mustUsernames(t); strings.Join(again, ",") != strings.Join(got, ",") {
		t.Errorf("a repeated sync changed the account set from %v to %v", got, again)
	}
}

func (a *Adapter) mustUsernames(t *testing.T) []string {
	t.Helper()
	names, err := a.readUsernames()
	if err != nil {
		t.Fatalf("read usernames: %v", err)
	}
	return names
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
