package hysteria2

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ExecRuntime drives a Hysteria2 server through systemd.
//
// It used to shell out to `hysteria server -c <config>` directly, which could
// not work: that command runs the server in the FOREGROUND, so ServerStart
// blocked until the command timeout and then reported failure for a server that
// had actually started. ServerStop did nothing at all and ServerStatus always
// answered "not running". Building Apply on that would have produced a node
// that reported converged having started nothing.
//
// systemd is what the Xray and sing-box runtimes already use, for the reasons
// that apply here too: something has to own the process across a panel restart,
// restart it when it dies, and answer whether it is up. Reimplementing that
// with PID files would be a second supervisor.
//
// The unit is templated per service -- hysteria-server@antimage-<id> -- because
// one node runs one Hysteria2 server per service, unlike Xray which multiplexes
// every inbound into a single process.
type ExecRuntime struct {
	// Binary is kept for Available: a missing hysteria binary is worth
	// reporting distinctly from a unit that will not start, because the
	// operator fixes them in different places.
	Binary string
	// UnitPrefix names the systemd template. The service id is appended.
	UnitPrefix     string
	CommandTimeout time.Duration
}

// NewExecRuntime returns a Runtime driving hysteria through systemd.
func NewExecRuntime() *ExecRuntime {
	return &ExecRuntime{
		Binary:         "hysteria",
		UnitPrefix:     "hysteria-server@" + filePrefix,
		CommandTimeout: 30 * time.Second,
	}
}

func (r *ExecRuntime) timeout() time.Duration {
	if r.CommandTimeout <= 0 {
		return 30 * time.Second
	}
	return r.CommandTimeout
}

func (r *ExecRuntime) run(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout())
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s",
			name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// unitFor derives the systemd unit from a config path.
//
// The config filename carries the service id (antimage-<id>.yaml) and the unit
// is named from it, so the two cannot drift: there is one source for which
// service a command refers to, and it is the path the caller already has.
func (r *ExecRuntime) unitFor(configPath string) string {
	base := configPath
	if idx := strings.LastIndexAny(base, `/\`); idx >= 0 {
		base = base[idx+1:]
	}
	base = strings.TrimSuffix(base, fileSuffix)
	base = strings.TrimPrefix(base, filePrefix)
	return r.UnitPrefix + base
}

func (r *ExecRuntime) Available(ctx context.Context) error {
	if _, err := exec.LookPath(r.Binary); err != nil {
		return fmt.Errorf("%w: %s not found in PATH: %w", ErrRuntimeUnavailable, r.Binary, err)
	}
	return nil
}

// ServerStart starts the unit for this config.
func (r *ExecRuntime) ServerStart(ctx context.Context, configPath string) error {
	_, err := r.run(ctx, "systemctl", "start", r.unitFor(configPath))
	return err
}

// ServerStop stops the unit for this config.
//
// A unit that is already stopped is not an error: `systemctl stop` on an
// inactive unit succeeds, which is what makes removal idempotent under the
// reconciler's retries.
func (r *ExecRuntime) ServerStop(ctx context.Context, configPath string) error {
	_, err := r.run(ctx, "systemctl", "stop", r.unitFor(configPath))
	return err
}

// ServerRestart restarts the unit, which is how this adapter applies every
// change: Hysteria2 has no hot reload, so there is nothing else to do.
func (r *ExecRuntime) ServerRestart(ctx context.Context, configPath string) error {
	_, err := r.run(ctx, "systemctl", "restart", r.unitFor(configPath))
	return err
}

// ServerStatus reports whether the unit is active.
//
// `systemctl is-active` exits non-zero for anything that is not active, so the
// error is expected and the STATE is the answer. Returning an error there would
// make "the server is stopped" indistinguishable from "systemd is unreachable",
// and Apply treats those differently: the first starts, the second restarts.
func (r *ExecRuntime) ServerStatus(ctx context.Context, configPath string) (bool, error) {
	out, err := r.run(ctx, "systemctl", "is-active", r.unitFor(configPath))
	state := strings.TrimSpace(out)
	switch state {
	case "active":
		return true, nil
	case "inactive", "failed", "activating", "deactivating", "unknown":
		return false, nil
	}
	if err != nil {
		// No recognisable state came back, so systemd itself could not be
		// asked. Report it rather than guessing "stopped", which would send
		// Apply down the start path against a server that may well be running.
		return false, fmt.Errorf("query %s: %w", r.unitFor(configPath), err)
	}
	return false, nil
}
