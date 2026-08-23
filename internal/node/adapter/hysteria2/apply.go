package hysteria2

import (
	"context"
	"fmt"
	"os"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Apply executes a single reconciliation step
func (a *Adapter) Apply(ctx context.Context, step adapter.Step) (adapter.StepResult, error) {
	switch step.Kind {
	case "install":
		return adapter.StepResult{
			OK:  false,
			Err: "install requires desired service context (not yet implemented)",
		}, nil
	case "restart":
		return adapter.StepResult{
			OK:  false,
			Err: "restart requires desired service context (not yet implemented)",
		}, nil
	case "remove":
		return a.applyRemove(ctx, step)
	default:
		return adapter.StepResult{
			OK:  false,
			Err: fmt.Sprintf("unknown step kind: %s", step.Kind),
		}, nil
	}
}

// applyRemove tears down and deletes a Hysteria2 service
func (a *Adapter) applyRemove(ctx context.Context, step adapter.Step) (adapter.StepResult, error) {
	configPath := a.configPath(step.ServiceID)

	// Check if server is running
	running, err := a.rt.ServerStatus(ctx, configPath)
	if err == nil && running {
		// Stop server
		if err := a.rt.ServerStop(ctx, configPath); err != nil {
			return adapter.StepResult{
				OK:  false,
				Err: fmt.Sprintf("failed to stop server: %v", err),
			}, nil
		}
	}

	// Remove config file
	if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
		return adapter.StepResult{
			OK:  false,
			Err: fmt.Sprintf("server stopped but config removal failed: %v", err),
		}, nil
	}

	// Remove applied state
	os.Remove(a.appliedPath(step.ServiceID)) // Best effort

	return adapter.StepResult{OK: true}, nil
}

// writeConfigAndApply is a helper for install/restart

// applyWithDesired shows intended structure with full desired state access
