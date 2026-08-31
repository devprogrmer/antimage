package ocserv

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Probe is the cheap liveness check run on the health cadence.
//
// "No config" is reported as healthy, not as a failure. A node with no ocserv
// service is a legitimate state -- most nodes will never have one -- and
// reporting it as unhealthy would put every node in the fleet permanently red
// for not running a protocol nobody asked it to run.
func (a *Adapter) Probe(ctx context.Context) (adapter.Health, error) {
	if _, err := os.Stat(filepath.Join(a.dir, confName)); err != nil {
		return adapter.Health{OK: true, Detail: "no ocserv service configured"}, nil
	}
	if !a.rt.Active(ctx) {
		return adapter.Health{OK: false, Detail: "ocserv is configured but not running"}, nil
	}
	return adapter.Health{OK: true, Detail: "ocserv running"}, nil
}

// Restart bounces the ocserv unit on demand. A node with no ocserv service
// configured has nothing to restart, which is a distinct outcome from a
// restart that was attempted and failed -- and the caller (an operator who
// clicked a button expecting a specific protocol to bounce) needs to be told
// which one happened.
func (a *Adapter) Restart(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(a.dir, confName)); err != nil {
		return fmt.Errorf("%w: no ocserv service configured", adapter.ErrRestartUnsupported)
	}
	return a.rt.Restart(ctx)
}
