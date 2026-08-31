package xray

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ErrRuntimeUnavailable means the Xray process or its management API could not
// be reached. It is distinct from a config error: the config may be perfect
// and the binary simply absent.
var ErrRuntimeUnavailable = errors.New("xray runtime unavailable")

// Runtime is the adapter's contact with the running Xray instance.
//
// It is an interface because the adapter must be testable without an Xray
// binary, and because the hot-add path genuinely differs from the restart
// path: AddUser talks to Xray's gRPC HandlerService and leaves sessions alone,
// while Restart replaces the process and drops them. Faking this at the
// process boundary rather than at the adapter boundary keeps Plan and Apply
// under test with their real logic intact.
type Runtime interface {
	// Available reports whether the Xray binary and its management endpoint
	// can be reached at all. HotUserAdd depends on it.
	Available(ctx context.Context) error

	// AddUser adds one user to a running inbound without touching other
	// sessions. Must be idempotent: adding a user who is already present is a
	// success, because Apply is retried after a partial failure.
	AddUser(ctx context.Context, inboundTag string, u User, protocol Protocol) error

	// RemoveUser removes one user from a running inbound. Also idempotent:
	// removing an absent user succeeds.
	RemoveUser(ctx context.Context, inboundTag, email string) error

	// Reload asks Xray to re-read configuration without dropping sessions.
	Reload(ctx context.Context) error

	// Restart replaces the process. Active sessions drop.
	Restart(ctx context.Context) error

	// Healthy is the cheap liveness check behind Probe.
	Healthy(ctx context.Context) (bool, string)

	// QueryStats reads per-user cumulative traffic counters. SP3 accounting.
	QueryStats(ctx context.Context) ([]UserStat, error)

	// BinaryPath resolves the absolute path of the binary this runtime
	// actually execs -- not just the bare name it was configured with.
	// UpgradeCore needs the real file path to swap; Available already knows
	// how to find it via exec.LookPath, and this is that same resolution
	// exposed for a caller that needs the path itself, not just a yes/no.
	BinaryPath(ctx context.Context) (string, error)
}

// ExecRuntime drives Xray through systemd and its gRPC management API.
//
// The API endpoint is only consulted for user mutation; liveness goes through
// systemd, because an Xray that is running but whose API inbound is
// misconfigured is still serving traffic and must not be reported as down.
type ExecRuntime struct {
	// Unit is the systemd unit name, e.g. "xray".
	Unit string
	// APIAddress is the Xray gRPC management endpoint, e.g. "127.0.0.1:10085".
	// Empty disables hot user add, which downgrades user changes to restarts.
	APIAddress string
	// Binary is the xray executable, used for config validation.
	Binary string
	// CommandTimeout bounds every shell-out so a hung systemctl cannot wedge
	// the reconcile loop.
	CommandTimeout time.Duration
}

// NewExecRuntime returns a runtime with production defaults.
func NewExecRuntime(unit, apiAddress, binary string) *ExecRuntime {
	if unit == "" {
		unit = "xray"
	}
	if binary == "" {
		binary = "xray"
	}
	return &ExecRuntime{
		Unit:           unit,
		APIAddress:     apiAddress,
		Binary:         binary,
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

// Available checks that the binary exists. A missing binary is the single most
// common cause of a node that enrols and then never converges, so it is
// reported as its own error rather than as a generic apply failure.
func (r *ExecRuntime) Available(ctx context.Context) error {
	if _, err := exec.LookPath(r.Binary); err != nil {
		return fmt.Errorf("%w: %s not found in PATH: %w", ErrRuntimeUnavailable, r.Binary, err)
	}
	return nil
}

// BinaryPath resolves r.Binary to the absolute file UpgradeCore needs to
// replace. LookPath handles an already-absolute r.Binary correctly too (it
// checks the path directly rather than consulting PATH), so this needs no
// special case for that.
func (r *ExecRuntime) BinaryPath(ctx context.Context) (string, error) {
	path, err := exec.LookPath(r.Binary)
	if err != nil {
		return "", fmt.Errorf("%w: %s not found in PATH: %w", ErrRuntimeUnavailable, r.Binary, err)
	}
	return path, nil
}

// HotAddSupported reports whether user mutation can avoid a restart. It is
// what the adapter's Descriptor reports as Caps.HotUserAdd.
func (r *ExecRuntime) HotAddSupported() bool { return r.APIAddress != "" }

// AddUser calls Xray's HandlerService through the xray CLI, which speaks the
// management gRPC API. Using the CLI rather than linking Xray's protobufs
// keeps this adapter free of a dependency on Xray's Go module, whose API
// surface changes between releases.
func (r *ExecRuntime) AddUser(ctx context.Context, inboundTag string, u User, protocol Protocol) error {
	if r.APIAddress == "" {
		return fmt.Errorf("%w: no management API address configured", ErrRuntimeUnavailable)
	}
	// `xray api adi` (AddInboundUser) is idempotent in practice: adding an
	// existing user returns an already-exists error, which is a success for
	// our purposes because Apply is retried after partial failure.
	out, err := r.run(ctx, r.Binary, "api", "adu",
		"--server="+r.APIAddress, buildUserJSON(inboundTag, u, protocol))
	if err != nil {
		if strings.Contains(strings.ToLower(out), "already exists") {
			return nil
		}
		return err
	}
	return nil
}

func (r *ExecRuntime) RemoveUser(ctx context.Context, inboundTag, email string) error {
	if r.APIAddress == "" {
		return fmt.Errorf("%w: no management API address configured", ErrRuntimeUnavailable)
	}
	out, err := r.run(ctx, r.Binary, "api", "rmu",
		"--server="+r.APIAddress, "-tag="+inboundTag, email)
	if err != nil {
		// Removing someone who is not there is the state we wanted.
		if strings.Contains(strings.ToLower(out), "not found") {
			return nil
		}
		return err
	}
	return nil
}

func (r *ExecRuntime) Reload(ctx context.Context) error {
	_, err := r.run(ctx, "systemctl", "reload-or-restart", r.Unit)
	return err
}

func (r *ExecRuntime) Restart(ctx context.Context) error {
	_, err := r.run(ctx, "systemctl", "restart", r.Unit)
	return err
}

// Healthy asks systemd rather than the API: an Xray whose management inbound
// is misconfigured is still serving traffic, and reporting it as down would
// make the panel show a healthy fleet as broken.
func (r *ExecRuntime) Healthy(ctx context.Context) (bool, string) {
	out, err := r.run(ctx, "systemctl", "is-active", r.Unit)
	state := strings.TrimSpace(out)
	if err != nil {
		return false, fmt.Sprintf("%s is %s", r.Unit, state)
	}
	if state != "active" {
		return false, fmt.Sprintf("%s is %s", r.Unit, state)
	}
	return true, "active"
}

// QueryStats queries Xray's StatsService for per-user traffic counters.
// Output format from `xray api statsquery` is one line per counter:
//
//	user>>>subject-1@antimage>>>traffic>>>uplink       12345
//	user>>>subject-1@antimage>>>traffic>>>downlink     67890
func (r *ExecRuntime) QueryStats(ctx context.Context) ([]UserStat, error) {
	if r.APIAddress == "" {
		return nil, fmt.Errorf("%w: no management API address configured", ErrRuntimeUnavailable)
	}

	out, err := r.run(ctx, r.Binary, "api", "statsquery",
		"--server="+r.APIAddress, "--pattern=user>>>")
	if err != nil {
		return nil, fmt.Errorf("query stats: %w", err)
	}

	// Parse output: each line is "<pattern> <value>".
	// We look for lines like:
	//   user>>>subject-N@antimage>>>traffic>>>uplink 12345
	//   user>>>subject-N@antimage>>>traffic>>>downlink 67890
	lines := strings.Split(strings.TrimSpace(out), "\n")
	users := make(map[string]*UserStat)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			continue
		}
		pattern, valueStr := parts[0], parts[1]

		// Parse value.
		var value uint64
		if _, err := fmt.Sscanf(valueStr, "%d", &value); err != nil {
			continue
		}

		// Extract email and direction from pattern.
		// Format: user>>>EMAIL>>>traffic>>>DIRECTION
		segments := strings.Split(pattern, ">>>")
		if len(segments) != 4 || segments[0] != "user" || segments[2] != "traffic" {
			continue
		}
		email := segments[1]
		direction := segments[3]

		// Ensure user entry exists.
		if users[email] == nil {
			users[email] = &UserStat{Email: email}
		}

		// Record counter.
		switch direction {
		case "uplink":
			users[email].Uplink = value
		case "downlink":
			users[email].Downlink = value
		}
	}

	// Convert map to slice.
	result := make([]UserStat, 0, len(users))
	for _, stat := range users {
		result = append(result, *stat)
	}
	return result, nil
}

// ValidateConfig asks Xray itself whether a config file is loadable. This is
// the difference between catching a bad config here and restarting into a
// crash loop that takes every user on the node offline.
func (r *ExecRuntime) ValidateConfig(ctx context.Context, dir string) error {
	if _, err := exec.LookPath(r.Binary); err != nil {
		// Cannot validate without the binary. Report it rather than pretending
		// the config is good.
		return fmt.Errorf("%w: cannot validate config, %s not found", ErrRuntimeUnavailable, r.Binary)
	}
	if _, err := r.run(ctx, r.Binary, "run", "-test", "-confdir", dir); err != nil {
		return fmt.Errorf("xray rejected the generated config: %w", err)
	}
	return nil
}

// buildUserJSON renders the argument `xray api adu` expects.
func buildUserJSON(tag string, u User, protocol Protocol) string {
	account := fmt.Sprintf(`{"id":%q}`, u.Credential)
	if protocol == Trojan {
		account = fmt.Sprintf(`{"password":%q}`, u.Credential)
	}
	return fmt.Sprintf(`{"tag":%q,"users":[{"email":%q,"account":%s}]}`, tag, u.Email, account)
}
