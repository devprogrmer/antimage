package ocserv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// OcctlUser is one connected session as occtl reports it.
//
// Field names follow occtl's JSON output. Bytes are cumulative for the
// SESSION, not for the user: a reconnect starts them at zero, which is what
// accounting.go has to allow for.
type OcctlUser struct {
	Username string `json:"Username"`
	// occtl reports these as strings in some builds and numbers in others,
	// so they are decoded leniently rather than as int64 -- a type mismatch
	// would fail the whole report and lose every user's traffic, not just the
	// one field.
	RX flexInt `json:"RX"`
	TX flexInt `json:"TX"`
	// Session identifies one connection. A new session id for the same user
	// means the counters restarted.
	Session string `json:"Session"`
}

// flexInt decodes a JSON number or a JSON string containing a number.
type flexInt int64

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	var n int64
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		// Not a number we understand. Zero rather than an error: one
		// unparseable counter must not discard the whole report.
		*f = 0
		return nil
	}
	*f = flexInt(n)
	return nil
}

// ExecRuntime drives the real host: systemctl for the unit, ocpasswd for the
// user database, occtl for accounting.
type ExecRuntime struct {
	unit     string
	ocpasswd string
	occtl    string
}

// NewExecRuntime returns a runtime using the standard tool names. Overridable
// so a host that installs them elsewhere can still be driven.
func NewExecRuntime(unit, ocpasswd, occtl string) *ExecRuntime {
	return &ExecRuntime{
		unit:     orDefault(unit, "ocserv"),
		ocpasswd: orDefault(ocpasswd, "ocpasswd"),
		occtl:    orDefault(occtl, "occtl"),
	}
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// Available reports whether the tooling this adapter drives is present.
//
// Checks ocpasswd rather than the ocserv daemon: the daemon is started through
// systemd and may legitimately be stopped, while ocpasswd is what Apply needs
// in order to do anything at all.
func (r *ExecRuntime) Available(ctx context.Context) error {
	if _, err := exec.LookPath(r.ocpasswd); err != nil {
		return fmt.Errorf("ocpasswd not found: %w", err)
	}
	return nil
}

func (r *ExecRuntime) Start(ctx context.Context) error   { return r.systemctl(ctx, "start") }
func (r *ExecRuntime) Stop(ctx context.Context) error    { return r.systemctl(ctx, "stop") }
func (r *ExecRuntime) Restart(ctx context.Context) error { return r.systemctl(ctx, "restart") }

// Reload asks ocserv to re-read its config without dropping sessions.
//
// Falls back to a restart when the unit does not support reload. Returning the
// error instead would leave the node with a new config file and the old config
// running -- converged as far as the panel could tell, and serving the
// previous settings.
func (r *ExecRuntime) Reload(ctx context.Context) error {
	if err := r.systemctl(ctx, "reload"); err != nil {
		return r.systemctl(ctx, "restart")
	}
	return nil
}

func (r *ExecRuntime) Active(ctx context.Context) bool {
	return exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", r.unit).Run() == nil
}

func (r *ExecRuntime) systemctl(ctx context.Context, verb string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "systemctl", verb, r.unit)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("systemctl %s %s: %w: %s",
			verb, r.unit, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// SetPassword creates or replaces one account.
//
// The password goes in on stdin, never as an argument: an argument is visible
// in /proc to every user on the host for as long as the process runs. ocpasswd
// prompts twice, so it is written twice.
func (r *ExecRuntime) SetPassword(ctx context.Context, passwdFile, username, password string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, r.ocpasswd, "-c", passwdFile, username)
	cmd.Stdin = strings.NewReader(password + "\n" + password + "\n")
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// The password is NOT included in the error, which is audited and
		// logged.
		return fmt.Errorf("ocpasswd -c %s %s: %w: %s",
			passwdFile, username, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (r *ExecRuntime) DeletePassword(ctx context.Context, passwdFile, username string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, r.ocpasswd, "-c", passwdFile, "-d", username)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ocpasswd -d %s: %w: %s",
			username, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// ShowUsers returns the connected sessions occtl knows about.
//
// No sessions is not an error: an idle server legitimately has none, and
// treating an empty list as a failure would make every quiet node look broken.
func (r *ExecRuntime) ShowUsers(ctx context.Context) ([]OcctlUser, error) {
	var out, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, r.occtl, "-j", "show", "users")
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("occtl -j show users: %w: %s",
			err, strings.TrimSpace(stderr.String()))
	}
	body := strings.TrimSpace(out.String())
	if body == "" || body == "null" {
		return nil, nil
	}
	var users []OcctlUser
	if err := json.Unmarshal([]byte(body), &users); err != nil {
		return nil, fmt.Errorf("parse occtl output: %w", err)
	}
	return users, nil
}
