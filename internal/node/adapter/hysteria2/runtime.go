package hysteria2

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ExecRuntime implements Runtime by shelling out to hysteria server binary
type ExecRuntime struct {
	Binary         string
	CommandTimeout time.Duration
}

// NewExecRuntime returns a Runtime using system hysteria binary
func NewExecRuntime() *ExecRuntime {
	return &ExecRuntime{
		Binary:         "hysteria",
		CommandTimeout: 30 * time.Second,
	}
}

func (r *ExecRuntime) timeout() time.Duration {
	if r.CommandTimeout <= 0 {
		return 30 * time.Second
	}
	return r.CommandTimeout
}

func (r *ExecRuntime) run(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout())
	defer cancel()
	out, err := exec.CommandContext(ctx, r.Binary, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s",
			r.Binary, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (r *ExecRuntime) Available(ctx context.Context) error {
	if _, err := exec.LookPath(r.Binary); err != nil {
		return fmt.Errorf("%w: %s not found in PATH: %w", ErrRuntimeUnavailable, r.Binary, err)
	}
	return nil
}

func (r *ExecRuntime) ServerStart(ctx context.Context, configPath string) error {
	// hysteria server -c <config>
	// In production, this would use systemd: systemctl start hysteria-server@<name>
	// For now, simplified
	_, err := r.run(ctx, "server", "-c", configPath)
	return err
}

func (r *ExecRuntime) ServerStop(ctx context.Context, configPath string) error {
	// In production: systemctl stop hysteria-server@<name>
	// For now, simplified - would need PID tracking
	return nil
}

func (r *ExecRuntime) ServerRestart(ctx context.Context, configPath string) error {
	// In production: systemctl restart hysteria-server@<name>
	if err := r.ServerStop(ctx, configPath); err != nil {
		return err
	}
	return r.ServerStart(ctx, configPath)
}

func (r *ExecRuntime) ServerStatus(ctx context.Context, configPath string) (bool, error) {
	// In production: systemctl is-active hysteria-server@<name>
	// For now, simplified - assume running if no error
	return false, nil
}
