//go:build e2e && realruntime

// Real-runtime verification for SP2.
//
// Everything else in this package drives a fake Runtime, which proves the
// adapter's logic but never executes ExecRuntime and never asks a real proxy
// whether the generated configuration is loadable. These tests close that gap:
// they run the actual xray and sing-box binaries against the actual bytes the
// adapter writes.
//
// Requires -tags "e2e realruntime" AND the binaries. The tag is the opt-in;
// once it is set, a missing binary FAILS rather than skips, so the verification
// cannot silently degrade into "compiled but never ran".
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/node/adapter"
	"github.com/amyrm/antimage/internal/node/adapter/singbox"
	"github.com/amyrm/antimage/internal/node/adapter/xray"
)

// binaryFor resolves a real proxy binary. Env var wins; PATH is the fallback.
func binaryFor(t *testing.T, envVar, name string) string {
	t.Helper()
	if p := os.Getenv(envVar); p != "" {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s=%s is not usable: %v", envVar, p, err)
		}
		return p
	}
	p, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("the realruntime tag is set but %s was not found: set %s or put it on PATH. "+
			"This test must not be skipped -- skipping is how a real-runtime gap hides.",
			name, envVar)
	}
	return p
}

// buildShim compiles the systemctl stand-in and returns the directory holding
// it, ready to be prepended to PATH.
func buildShim(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "systemctl")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, "./testdata/systemctlshim")
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build systemctl shim: %v\n%s", err, combined)
	}
	return dir
}

// freePort reserves a port by binding and releasing it.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// realEnv is an sp2Env wired to a real binary through ExecRuntime.
type realEnv struct {
	*sp2Env
	unit      string
	stateDir  string
	binary    string
	port      int
	specPath  string
	validator func(ctx context.Context, dir string) error
}

// writeUnit points the shim's unit at a command line.
func (r *realEnv) writeUnit(t *testing.T, binary string, args []string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"path": binary, "args": args, "port": r.port,
	})
	if err != nil {
		t.Fatalf("encode unit: %v", err)
	}
	if err := os.WriteFile(r.specPath, body, 0o600); err != nil {
		t.Fatalf("write unit: %v", err)
	}
}

// startReal builds the panel side exactly as startSP2 does, then swaps in a
// real adapter driven by ExecRuntime over the real binary.
func startReal(t *testing.T, kind string) *realEnv {
	t.Helper()

	shimDir := buildShim(t)
	stateDir := t.TempDir()
	t.Setenv("SHIM_STATE", stateDir)
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	port := freePort(t)
	unit := "antimage-" + kind

	var binary string
	switch kind {
	case "xray":
		binary = binaryFor(t, "XRAY_BINARY", "xray")
	case "singbox":
		binary = binaryFor(t, "SINGBOX_BINARY", "sing-box")
	default:
		t.Fatalf("unknown kind %q", kind)
	}

	t.Setenv("ANTIMAGE_REALRUNTIME", "1")
	e := startSP2WithPort(t, kind, port)
	r := &realEnv{
		sp2Env: e, unit: unit, stateDir: stateDir, binary: binary, port: port,
		specPath: filepath.Join(stateDir, unit+".json"),
	}

	switch kind {
	case "xray":
		rt := xray.NewExecRuntime(unit, "", binary) // no API inbound is generated
		r.validator = func(ctx context.Context, dir string) error {
			return rt.ValidateConfig(ctx, dir)
		}
		e.adapter = xray.New(e.confDir, rt, rt.HotAddSupported())
		r.writeUnit(t, binary, []string{"run", "-confdir", e.confDir})
	case "singbox":
		rt := singbox.NewExecRuntime(unit, binary)
		// sing-box has no ValidateConfig on its runtime, so acceptance is
		// proven by asking the real binary directly.
		r.validator = func(ctx context.Context, dir string) error {
			out, err := exec.CommandContext(ctx, binary, "check", "-C", dir).CombinedOutput()
			if err != nil {
				return fmt.Errorf("sing-box rejected the generated config: %w: %s",
					err, strings.TrimSpace(string(out)))
			}
			return nil
		}
		e.adapter = singbox.New(e.confDir, rt)
		r.writeUnit(t, binary, []string{"run", "-C", e.confDir})
	}
	t.Cleanup(func() {
		cmd := exec.Command("systemctl", "stop", unit)
		cmd.Env = append(os.Environ(), "SHIM_STATE="+stateDir)
		_ = cmd.Run()
	})
	return r
}

// unitLog returns whatever the managed process wrote, for failure messages.
func (r *realEnv) unitLog() string {
	body, err := os.ReadFile(filepath.Join(r.stateDir, r.unit+".log"))
	if err != nil {
		return "(no unit log)"
	}
	return strings.TrimSpace(string(body))
}

// THE real-runtime lifecycle, for each adapter, end to end:
//
//	Create -> assign -> credential -> desired state -> revision/hash -> Node
//	-> Plan -> Apply -> a real proxy process -> Probe -> healthy -> convergence
func TestRealRuntimeLifecycle(t *testing.T) {
	for _, kind := range []string{"xray", "singbox"} {
		t.Run(kind, func(t *testing.T) {
			r := startReal(t, kind)
			e := r.sp2Env
			ctx := context.Background()
			t.Logf("binary: %s (unit %s, port %d)", r.binary, r.unit, r.port)

			// --- Create, assign, credential. ---
			id := e.createSubject(t, "alice", nil)
			cred := e.credential(t, id)
			if cred == "" {
				t.Fatal("no credential was issued")
			}

			// --- Desired state, revision, hash. ---
			snap := e.snapshot(t)
			if snap.Revision == 0 {
				t.Fatal("desired revision never moved off zero")
			}
			if snap.SHA256 == "" {
				t.Fatal("desired document has no hash")
			}
			var desired adapter.Desired
			if err := json.Unmarshal(snap.Bytes, &desired); err != nil {
				t.Fatalf("decode desired document: %v", err)
			}
			if len(desired.Subjects) != 1 {
				t.Fatalf("document carries %d subjects, want 1", len(desired.Subjects))
			}
			t.Logf("desired revision %d hash %s", snap.Revision, snap.SHA256[:12])

			// --- Plan and Apply, which starts the real process. ---
			obs, err := e.adapter.Observe(ctx)
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			plan, err := e.adapter.Plan(ctx, desired, obs)
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if plan.IsEmpty() {
				t.Fatal("nothing planned for a node with no config at all")
			}
			for _, step := range plan.Steps {
				if _, err := e.adapter.Apply(ctx, step); err != nil {
					t.Fatalf("Apply %s: %v\ngenerated config:\n%s\nunit log:\n%s",
						step.Kind, err, e.generatedConfig(t), r.unitLog())
				}
			}

			// --- The real binary accepted the generated configuration. ---
			config := e.generatedConfig(t)
			if !strings.Contains(config, cred) {
				t.Fatal("the credential never reached the generated config")
			}
			if r.validator != nil {
				if err := r.validator(ctx, e.confDir); err != nil {
					t.Fatalf("the real binary rejected the generated config: %v", err)
				}
				t.Log("the real binary validated the generated config")
			}

			// --- A real process is listening on the configured port. ---
			conn, err := net.DialTimeout("tcp",
				fmt.Sprintf("127.0.0.1:%d", r.port), 3*time.Second)
			if err != nil {
				t.Fatalf("no real process is serving on port %d: %v\nunit log:\n%s",
					r.port, err, r.unitLog())
			}
			_ = conn.Close()
			t.Logf("a real %s process is serving on port %d", kind, r.port)

			// --- Probe, through the real runtime. ---
			health, err := e.adapter.Probe(ctx)
			if err != nil {
				t.Fatalf("Probe: %v", err)
			}
			if !health.OK {
				t.Fatalf("Probe reports unhealthy against a running process: %+v\nunit log:\n%s",
					health, r.unitLog())
			}
			t.Logf("Probe: OK=%v detail=%q", health.OK, health.Detail)

			// --- Convergence. ---
			obs, err = e.adapter.Observe(ctx)
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			again, err := e.adapter.Plan(ctx, desired, obs)
			if err != nil {
				t.Fatalf("re-Plan: %v", err)
			}
			if !again.IsEmpty() {
				t.Fatalf("did not converge against the real runtime: %+v", again.Steps)
			}
			t.Log("converged against the real runtime")
		})
	}
}

// Failure and recovery against the real runtime.
//
// The config write succeeds and the unit fails to start -- the real production
// case where the binary is missing or broken after an upgrade. The adapter must
// not call that converged, and must converge once the runtime is healthy again.
func TestRealRuntimeFailureAndRecovery(t *testing.T) {
	for _, kind := range []string{"xray", "singbox"} {
		t.Run(kind, func(t *testing.T) {
			r := startReal(t, kind)
			e := r.sp2Env
			ctx := context.Background()

			e.createSubject(t, "alice", nil)
			desiredOf := func() adapter.Desired {
				var d adapter.Desired
				if err := json.Unmarshal(e.snapshot(t).Bytes, &d); err != nil {
					t.Fatalf("decode: %v", err)
				}
				return d
			}

			// Break the unit: the config will be written, the process will not
			// start. This is a real exec failure, not an injected error value.
			r.writeUnit(t, filepath.Join(r.stateDir, "definitely-not-a-binary"), nil)

			desired := desiredOf()
			obs, err := e.adapter.Observe(ctx)
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			plan, err := e.adapter.Plan(ctx, desired, obs)
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			var sawFailure bool
			for _, step := range plan.Steps {
				if _, err := e.adapter.Apply(ctx, step); err != nil {
					sawFailure = true
				}
			}
			if !sawFailure {
				t.Fatal("a unit that cannot start did not surface as a step failure")
			}

			// The config is on disk but nothing is serving it.
			if e.generatedConfig(t) == "" {
				t.Fatal("the config was not written before the restart failed")
			}
			if health, _ := e.adapter.Probe(ctx); health.OK {
				t.Error("Probe reports healthy with no process running")
			}

			// MUST remain non-converged.
			obs, err = e.adapter.Observe(ctx)
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			pending, err := e.adapter.Plan(ctx, desired, obs)
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if pending.IsEmpty() {
				t.Fatal("reported converged while no process is running the config")
			}
			t.Logf("failed unit: still plans %d step(s), Probe unhealthy", len(pending.Steps))

			// --- Recover: restore the real binary. ---
			switch kind {
			case "xray":
				r.writeUnit(t, r.binary, []string{"run", "-confdir", e.confDir})
			case "singbox":
				r.writeUnit(t, r.binary, []string{"run", "-C", e.confDir})
			}

			for _, step := range pending.Steps {
				if _, err := e.adapter.Apply(ctx, step); err != nil {
					t.Fatalf("Apply %s after recovery: %v\nunit log:\n%s",
						step.Kind, err, r.unitLog())
				}
			}

			health, err := e.adapter.Probe(ctx)
			if err != nil {
				t.Fatalf("Probe after recovery: %v", err)
			}
			if !health.OK {
				t.Fatalf("still unhealthy after recovery: %+v\nunit log:\n%s", health, r.unitLog())
			}

			obs, err = e.adapter.Observe(ctx)
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			final, err := e.adapter.Plan(ctx, desiredOf(), obs)
			if err != nil {
				t.Fatalf("final Plan: %v", err)
			}
			if !final.IsEmpty() {
				t.Fatalf("did not converge after recovery: %+v", final.Steps)
			}
			t.Log("recovered: real process healthy and converged")
		})
	}
}
