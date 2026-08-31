package hysteria2

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Apply executes a single reconciliation step.
//
// Every branch is idempotent, because the reconciler retries a step after a
// partial failure. The desired state each branch needs travels in step.Payload,
// rendered by Plan -- Apply never sees adapter.Desired, which is what AD-3 is
// about.
func (a *Adapter) Apply(ctx context.Context, step adapter.Step) (adapter.StepResult, error) {
	result := adapter.StepResult{Seq: step.Seq, Kind: step.Kind, Disruption: step.Disruption}

	fail := func(format string, args ...any) (adapter.StepResult, error) {
		result.OK = false
		result.Err = fmt.Sprintf(format, args...)
		return result, nil
	}

	var p stepPayload
	if len(step.Payload) > 0 {
		if err := json.Unmarshal(step.Payload, &p); err != nil {
			return fail("decode step payload: %v", err)
		}
	}

	switch step.Kind {
	case "install", "restart":
		// One branch, because they are the same act on this adapter. Hysteria2
		// has no hot reload -- the package comment says so and Caps.HotUserAdd
		// is false -- so every change is "write the file and get the process
		// onto it". The only difference between installing and restarting is
		// whether a process is already running, which the code below has to
		// check anyway for idempotency.
		if err := a.writeConfigAndApply(ctx, step.ServiceID, p); err != nil {
			return fail("%v", err)
		}
	case "remove":
		return a.applyRemove(ctx, step)
	default:
		return fail("unknown step kind: %s", step.Kind)
	}

	result.OK = true
	return result, nil
}

// writeConfigAndApply writes the config, gets the server running on it, and
// only then records what the server is serving.
//
// The order matters: the sidecar is the adapter's answer to "did the runtime
// ever load this?", so recording before the process is up would assert a
// convergence that has not happened and the next Plan would believe it.
func (a *Adapter) writeConfigAndApply(ctx context.Context, serviceID int64, p stepPayload) error {
	if p.Config == "" {
		// Without a rendered config there is nothing to install, and writing an
		// empty file would take every user off the service on the next start.
		return fmt.Errorf("service %d: step carries no rendered config", serviceID)
	}

	configPath := a.configPath(serviceID)
	if err := a.writeConfig(configPath, p.Config); err != nil {
		return err
	}

	// Restart if it is already running, start if it is not. A ServerStart
	// against a live process is not a reload -- it either fails or leaves the
	// old config in memory, which is the "converged on disk, wrong in memory"
	// state the sidecar exists to catch.
	running, err := a.rt.ServerStatus(ctx, configPath)
	if err != nil {
		// An unreadable status is not proof the server is down. Restart covers
		// both cases: it is the operation that ends with the process running
		// this config whatever it was doing before.
		running = true
	}
	if running {
		if err := a.rt.ServerRestart(ctx, configPath); err != nil {
			return fmt.Errorf("restart service %d: %w", serviceID, err)
		}
	} else if err := a.rt.ServerStart(ctx, configPath); err != nil {
		return fmt.Errorf("start service %d: %w", serviceID, err)
	}

	if err := a.recordApplied(serviceID, p.Checksum, p.Users); err != nil {
		return fmt.Errorf("record applied state for service %d: %w", serviceID, err)
	}
	return nil
}

// writeConfig writes a config atomically, 0600.
//
// Atomic because the file holds the service password and the server may read it
// at any moment: a torn write is a broken service. 0600 because a
// world-readable config hands every local user the credentials of every
// subscriber on the node.
func (a *Adapter) writeConfig(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".antimage-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if _, err := tmp.WriteString(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	// Durability before the rename: a rename that survives a crash pointing at
	// unflushed content leaves a truncated config where a valid one was.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("install config %s: %w", path, err)
	}
	return nil
}

// applyRemove tears down and deletes a Hysteria2 service
func (a *Adapter) applyRemove(ctx context.Context, step adapter.Step) (adapter.StepResult, error) {
	result := adapter.StepResult{Seq: step.Seq, Kind: step.Kind, Disruption: step.Disruption}
	configPath := a.configPath(step.ServiceID)

	// Check if server is running
	running, err := a.rt.ServerStatus(ctx, configPath)
	if err == nil && running {
		// Stop server
		if err := a.rt.ServerStop(ctx, configPath); err != nil {
			result.Err = fmt.Sprintf("failed to stop server: %v", err)
			return result, nil
		}
	}

	// Remove config file
	if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
		result.Err = fmt.Sprintf("server stopped but config removal failed: %v", err)
		return result, nil
	}

	// Remove applied state
	_ = os.Remove(a.appliedPath(step.ServiceID)) // Best effort

	result.OK = true
	return result, nil
}
