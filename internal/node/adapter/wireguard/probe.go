package wireguard

import (
	"context"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Probe checks the health of WireGuard services.
func (a *Adapter) Probe(ctx context.Context) (adapter.Health, error) {
	// Check if WireGuard tooling is available
	if err := a.rt.Available(ctx); err != nil {
		return adapter.Health{
			OK:     false,
			Detail: err.Error(),
		}, nil
	}

	// For now, just report that the runtime is available
	// A more sophisticated probe would:
	// - List all managed interfaces
	// - Check each is up and has active peers
	// - Detect handshake failures
	// - Report peer counts

	return adapter.Health{
		OK:     true,
		Detail: "wireguard runtime available",
	}, nil
}
