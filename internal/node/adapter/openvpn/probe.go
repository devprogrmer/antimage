package openvpn

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Probe is the cheap liveness check run on the health cadence.
//
// A node with no OpenVPN service is healthy, not broken. Most nodes will never
// have one, and reporting them red for not running a protocol nobody asked
// them to run would make the health signal useless.
func (a *Adapter) Probe(ctx context.Context) (adapter.Health, error) {
	if _, err := os.Stat(filepath.Join(a.dir, confName)); err != nil {
		return adapter.Health{OK: true, Detail: "no openvpn service configured"}, nil
	}
	// The verify script is checked separately from the unit because its
	// absence is silent: OpenVPN starts happily without it and then refuses
	// every login, which looks like a credential problem to everyone involved.
	if _, err := os.Stat(filepath.Join(a.dir, verifyName)); err != nil {
		return adapter.Health{
			OK:     false,
			Detail: "openvpn is configured but its verify script is missing; every login will fail",
		}, nil
	}
	if !a.rt.Active(ctx) {
		return adapter.Health{OK: false, Detail: "openvpn is configured but not running"}, nil
	}
	return adapter.Health{OK: true, Detail: "openvpn running"}, nil
}

// Restart bounces the openvpn unit on demand.
func (a *Adapter) Restart(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(a.dir, confName)); err != nil {
		return fmt.Errorf("%w: no openvpn service configured", adapter.ErrRestartUnsupported)
	}
	return a.rt.Restart(ctx)
}
