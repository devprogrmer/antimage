package ocserv

import (
	"context"
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
