package hysteria2

import (
	"context"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Probe checks health of Hysteria2 services
func (a *Adapter) Probe(ctx context.Context) (adapter.Health, error) {
	// Check if Hysteria2 binary is available
	if err := a.rt.Available(ctx); err != nil {
		return adapter.Health{
			OK:     false,
			Detail: err.Error(),
		}, nil
	}

	return adapter.Health{
		OK:     true,
		Detail: "hysteria2 runtime available",
	}, nil
}
