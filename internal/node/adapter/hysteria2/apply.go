package hysteria2

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

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
	_ = os.Remove(a.appliedPath(step.ServiceID)) // Best effort

	return adapter.StepResult{OK: true}, nil
}

// writeConfigAndApply is a helper for install/restart
func (a *Adapter) writeConfigAndApply(ctx context.Context, serviceID int64, config string, users []UserAuth) (adapter.StepResult, error) {
	configPath := a.configPath(serviceID)

	// Ensure config directory exists
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return adapter.StepResult{
			OK:  false,
			Err: fmt.Sprintf("create config dir: %v", err),
		}, nil
	}

	// Write config atomically
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

	// Extract checksum
	lines := strings.Split(config, "\n")
	_, checksum, _ := parseMarker(lines[0])

	// Start server
	if err := a.rt.ServerStart(ctx, configPath); err != nil {
		return adapter.StepResult{
			OK:  false,
			Err: fmt.Sprintf("server start failed: %v", err),
		}, nil
	}

	// Record applied state
	usernames := make([]string, len(users))
	for i, u := range users {
		usernames[i] = u.Username
	}
	if err := a.recordApplied(serviceID, checksum, usernames); err != nil {
		return adapter.StepResult{
			OK:  true,
			Err: fmt.Sprintf("warning: failed to record applied state: %v", err),
		}, nil
	}

	return adapter.StepResult{OK: true}, nil
}

// applyWithDesired shows intended structure with full desired state access
func (a *Adapter) applyWithDesired(ctx context.Context, step adapter.Step, desired adapter.Service, subjects []adapter.Subject) (adapter.StepResult, error) {
	var params ServiceParams
	if err := json.Unmarshal(desired.Params, &params); err != nil {
		return adapter.StepResult{
			OK:  false,
			Err: fmt.Sprintf("invalid service params: %v", err),
		}, nil
	}

	users := UserAuthFromSubjects(subjects)
	config, err := GenerateConfig(desired.ID, params, users)
	if err != nil {
		return adapter.StepResult{
			OK:  false,
			Err: fmt.Sprintf("config generation failed: %v", err),
		}, nil
	}

	switch step.Kind {
	case "install", "restart":
		// For restart, stop first
		if step.Kind == "restart" {
			configPath := a.configPath(desired.ID)
			running, err := a.rt.ServerStatus(ctx, configPath)
			if err == nil && running {
				if stopErr := a.rt.ServerStop(ctx, configPath); stopErr != nil {
					return adapter.StepResult{
						OK:  false,
						Err: fmt.Sprintf("failed to stop server: %v", stopErr),
					}, nil
				}
			}
		}
		return a.writeConfigAndApply(ctx, desired.ID, config, users)

	default:
		return adapter.StepResult{
			OK:  false,
			Err: fmt.Sprintf("unknown step kind: %s", step.Kind),
		}, nil
	}
}
