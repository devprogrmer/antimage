package wireguard

import (
	"context"
	"fmt"
	"os"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Apply executes a single reconciliation step.
func (a *Adapter) Apply(ctx context.Context, step adapter.Step) (adapter.StepResult, error) {
	switch step.Kind {
	case "install":
		return a.applyInstall(ctx, step)
	case "restart":
		return a.applyRestart(ctx, step)
	case "reload":
		return a.applyReload(ctx, step)
	case "remove":
		return a.applyRemove(ctx, step)
	default:
		return adapter.StepResult{
			OK:  false,
			Err: fmt.Sprintf("unknown step kind: %s", step.Kind),
		}, nil
	}
}

// applyInstall creates a new WireGuard service.
func (a *Adapter) applyInstall(ctx context.Context, step adapter.Step) (adapter.StepResult, error) {
	// This requires the full desired service, which Apply doesn't have.
	// In practice, the reconciler passes desired state through step.Context or
	// we need to fetch it. For now, return an error indicating we need more context.
	return adapter.StepResult{
		OK:  false,
		Err: "install requires desired service context (not yet implemented)",
	}, nil
}

// applyRestart tears down and recreates a WireGuard interface.
func (a *Adapter) applyRestart(ctx context.Context, step adapter.Step) (adapter.StepResult, error) {
	// Same issue: need desired service params to generate config
	return adapter.StepResult{
		OK:  false,
		Err: "restart requires desired service context (not yet implemented)",
	}, nil
}

// applyReload updates peers on a running interface without restarting.
func (a *Adapter) applyReload(ctx context.Context, step adapter.Step) (adapter.StepResult, error) {
	// Same issue
	return adapter.StepResult{
		OK:  false,
		Err: "reload requires desired service context (not yet implemented)",
	}, nil
}

// applyRemove tears down and deletes a WireGuard interface.
func (a *Adapter) applyRemove(ctx context.Context, step adapter.Step) (adapter.StepResult, error) {
	iface := interfaceName(step.ServiceID)
	configPath := a.configPath(step.ServiceID)

	// Check if interface exists
	_, up, err := a.rt.InterfaceStatus(ctx, iface)
	if err != nil {
		return adapter.StepResult{
			OK:  false,
			Err: fmt.Sprintf("status check failed: %v", err),
		}, nil
	}

	// Bring down interface if it's up
	if up {
		if err := a.rt.InterfaceDown(ctx, iface); err != nil {
			return adapter.StepResult{
				OK:  false,
				Err: fmt.Sprintf("failed to bring down interface: %v", err),
			}, nil
		}
	}

	// Remove config file
	if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
		return adapter.StepResult{
			OK:  false,
			Err: fmt.Sprintf("interface down but config removal failed: %v", err),
		}, nil
	}

	// Remove applied state file
	appliedPath := a.appliedPath(step.ServiceID)
	_ = os.Remove(appliedPath) // Best effort, don't fail if this errors

	return adapter.StepResult{
		OK: true,
	}, nil
}

// writeConfigAndApply is a helper that writes config, brings up the interface,
// and records applied state. Used by both install and restart.

// applyWithDesired is what Apply would look like if it had access to desired state.
// This shows the intended implementation structure.
