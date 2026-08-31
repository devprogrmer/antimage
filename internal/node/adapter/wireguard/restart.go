package wireguard

import (
	"context"
	"errors"
	"fmt"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Restart cycles every WireGuard interface this node currently runs.
//
// WireGuard has no daemon to bounce: each service is its own kernel
// interface brought up by wg-quick, and there can be several running at
// once (one per port), unlike xray/singbox which multiplex everything into
// one process. "Restart" therefore means down-then-up per interface, and
// which interfaces exist comes from Observe -- the same enumeration Apply
// already trusts -- rather than re-scanning the config directory a second
// way that could disagree with it.
func (a *Adapter) Restart(ctx context.Context) error {
	observed, err := a.Observe(ctx)
	if err != nil {
		return fmt.Errorf("observe before restart: %w", err)
	}
	if len(observed.Services) == 0 {
		return fmt.Errorf("%w: no WireGuard interfaces configured", adapter.ErrRestartUnsupported)
	}

	var errs []error
	for _, svc := range observed.Services {
		iface := interfaceName(svc.ID)
		configPath := a.configPath(svc.ID)
		if err := a.rt.InterfaceDown(ctx, iface, configPath); err != nil {
			errs = append(errs, fmt.Errorf("%s down: %w", iface, err))
			// Not bringing it back up after a failed down: wg-quick up on an
			// interface that is still live would fail anyway, and reporting
			// one clear error is more useful than a second, confusing one
			// from the up side of the same interface.
			continue
		}
		if err := a.rt.InterfaceUp(ctx, iface, configPath); err != nil {
			errs = append(errs, fmt.Errorf("%s up: %w", iface, err))
		}
	}
	return errors.Join(errs...)
}
