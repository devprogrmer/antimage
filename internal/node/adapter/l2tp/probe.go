package l2tp

import (
	"context"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Probe checks L2TP/IPsec service health.
func (a *Adapter) Probe(ctx context.Context) (adapter.Health, error) {
	// Check if strongSwan is running.
	if !isServiceActive("strongswan") {
		return adapter.Health{
			OK:     false,
			Detail: "strongswan service not running",
		}, nil
	}

	// Check if xl2tpd is running.
	if !isServiceActive("xl2tpd") {
		return adapter.Health{
			OK:     false,
			Detail: "xl2tpd service not running",
		}, nil
	}

	// TODO: Check listening ports (UDP 500, 4500, 1701).
	// This requires parsing netstat/ss output or using a network library.
	// For Phase C, service status is sufficient.

	return adapter.Health{
		OK:     true,
		Detail: "strongswan and xl2tpd running",
	}, nil
}
