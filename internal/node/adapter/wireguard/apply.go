package wireguard

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

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
func (a *Adapter) writeConfigAndApply(ctx context.Context, serviceID int64, config string, peers []PeerConfig) (adapter.StepResult, error) {
	configPath := a.configPath(serviceID)

	// Ensure /etc/wireguard exists
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return adapter.StepResult{
			OK:  false,
			Err: fmt.Sprintf("create config dir: %v", err),
		}, nil
	}

	// Write config atomically (tmp + rename)
	tmpPath := configPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(config), 0o600); err != nil {
		return adapter.StepResult{
			OK:  false,
			Err: fmt.Sprintf("write config: %v", err),
		}, nil
	}

	if err := os.Rename(tmpPath, configPath); err != nil {
		_ = os.Remove(tmpPath) // cleanup
		return adapter.StepResult{
			OK:  false,
			Err: fmt.Sprintf("install config: %v", err),
		}, nil
	}

	// Extract checksum from config
	lines := strings.Split(config, "\n")
	_, checksum, _ := parseMarker(lines[0])

	// Bring up interface
	iface := interfaceName(serviceID)
	if err := a.rt.InterfaceUp(ctx, iface); err != nil {
		return adapter.StepResult{
			OK:  false,
			Err: fmt.Sprintf("interface up failed: %v", err),
		}, nil
	}

	// Record applied state
	peerKeys := extractPublicKeys(peers)
	if err := a.recordApplied(serviceID, checksum, peerKeys); err != nil {
		// Non-fatal, just means next reconcile might restart unnecessarily
		return adapter.StepResult{
			OK:  true,
			Err: fmt.Sprintf("warning: failed to record applied state: %v", err),
		}, nil
	}

	return adapter.StepResult{OK: true}, nil
}

// applyWithDesired is what Apply would look like if it had access to desired state.
// This shows the intended implementation structure.
func (a *Adapter) applyWithDesired(ctx context.Context, step adapter.Step, desired adapter.Service, subjects []adapter.Subject) (adapter.StepResult, error) {
	var params ServiceParams
	if err := json.Unmarshal(desired.Params, &params); err != nil {
		return adapter.StepResult{
			OK:  false,
			Err: fmt.Sprintf("invalid service params: %v", err),
		}, nil
	}

	peers := a.buildPeerList(desired, subjects)
	config, err := GenerateConfig(desired.ID, params, peers)
	if err != nil {
		return adapter.StepResult{
			OK:  false,
			Err: fmt.Sprintf("config generation failed: %v", err),
		}, nil
	}

	switch step.Kind {
	case "install", "restart":
		// For restart, bring down first
		if step.Kind == "restart" {
			iface := interfaceName(step.ServiceID)
			_, up, err := a.rt.InterfaceStatus(ctx, iface)
			if err == nil && up {
				_ = a.rt.InterfaceDown(ctx, iface) // Best effort
			}
		}
		return a.writeConfigAndApply(ctx, desired.ID, config, peers)

	case "reload":
		configPath := a.configPath(desired.ID)
		iface := interfaceName(desired.ID)

		// Write new config
		if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
			return adapter.StepResult{
				OK:  false,
				Err: fmt.Sprintf("write config: %v", err),
			}, nil
		}

		// Try hot sync
		synced, err := a.rt.SyncPeers(ctx, iface, configPath)
		if err != nil || !synced {
			// Hot sync failed, fall back to restart
			if err := a.rt.InterfaceDown(ctx, iface); err != nil {
				return adapter.StepResult{
					OK:  false,
					Err: fmt.Sprintf("restart fallback failed: %v", err),
				}, nil
			}
			if err := a.rt.InterfaceUp(ctx, iface); err != nil {
				return adapter.StepResult{
					OK:  false,
					Err: fmt.Sprintf("restart fallback failed: %v", err),
				}, nil
			}
		}

		// Record applied state
		lines := strings.Split(config, "\n")
		_, checksum, _ := parseMarker(lines[0])
		peerKeys := extractPublicKeys(peers)
		_ = a.recordApplied(desired.ID, checksum, peerKeys) // Best effort

		return adapter.StepResult{OK: true}, nil

	default:
		return adapter.StepResult{
			OK:  false,
			Err: fmt.Sprintf("unknown step kind: %s", step.Kind),
		}, nil
	}
}
