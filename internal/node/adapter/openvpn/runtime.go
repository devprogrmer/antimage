package openvpn

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ExecRuntime drives the real host through systemd.
type ExecRuntime struct {
	unit   string
	binary string
}

// NewExecRuntime returns a runtime for a unit name and binary.
//
// The default unit is openvpn-server@antimage: the packaged
// openvpn-server@.service template takes the config name as its instance, so
// this is the unit that reads the antimage-server.conf this adapter writes.
// Hardcoding plain "openvpn" would drive whatever config the distribution
// shipped instead.
func NewExecRuntime(unit, binary string) *ExecRuntime {
	return &ExecRuntime{
		unit:   orDefault(unit, "openvpn-server@antimage-server"),
		binary: orDefault(binary, "openvpn"),
	}
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func (r *ExecRuntime) Available(ctx context.Context) error {
	if _, err := exec.LookPath(r.binary); err != nil {
		return fmt.Errorf("openvpn not found: %w", err)
	}
	return nil
}

func (r *ExecRuntime) Start(ctx context.Context) error   { return r.systemctl(ctx, "start") }
func (r *ExecRuntime) Stop(ctx context.Context) error    { return r.systemctl(ctx, "stop") }
func (r *ExecRuntime) Restart(ctx context.Context) error { return r.systemctl(ctx, "restart") }

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

// ReadStatus returns OpenVPN's status file.
//
// A missing file is empty output rather than an error: OpenVPN writes it on a
// timer after it starts, so a freshly started server legitimately has none,
// and failing the accounting poll for that would mark every new node broken.
func (r *ExecRuntime) ReadStatus(ctx context.Context, path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(body), nil
}
