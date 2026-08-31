package wireguard

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
// about. Re-deriving the config here would mean two renders of one service that
// could disagree, and the disagreement would surface as a permanent drift loop.
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
		// One branch, because they are the same act. wg-quick has no notion of
		// "already up"; bringing an interface up that is already up fails, and
		// the difference between a first install and a repair is only whether
		// the interface happens to exist. Treating them separately would mean
		// two code paths that must stay in step with each other for no gain.
		if err := a.writeConfigAndApply(ctx, step.ServiceID, p); err != nil {
			return fail("%v", err)
		}
	case "reload":
		if err := a.syncPeers(ctx, step.ServiceID, p); err != nil {
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

// writeConfigAndApply writes the config, brings the interface up on it, and
// only then records what the interface is serving.
//
// The order is the point. The sidecar is the adapter's answer to "did the
// runtime ever load this?", so recording before the interface is actually up
// would assert convergence that has not happened, and the next Plan would
// believe it.
func (a *Adapter) writeConfigAndApply(ctx context.Context, serviceID int64, p stepPayload) error {
	if p.Config == "" {
		// Without a rendered config there is nothing to install. Refusing is
		// the only safe answer: writing an empty file would take every peer off
		// the interface on the next bring-up.
		return fmt.Errorf("service %d: step carries no rendered config", serviceID)
	}

	iface := interfaceName(serviceID)
	configPath := a.configPath(serviceID)

	// Down first if it is up. wg-quick up on a live interface fails, and a
	// retry after a partial failure has to be able to make progress.
	_, up, err := a.rt.InterfaceStatus(ctx, iface)
	if err != nil {
		return fmt.Errorf("check interface %s: %w", iface, err)
	}
	if up {
		if err := a.rt.InterfaceDown(ctx, iface, configPath); err != nil {
			return fmt.Errorf("bring down %s before reconfiguring: %w", iface, err)
		}
	}

	if err := a.writeConfig(configPath, p.Config); err != nil {
		return err
	}

	if err := a.rt.InterfaceUp(ctx, iface, configPath); err != nil {
		// The file is on disk and the interface is down. The sidecar is
		// deliberately NOT written, so the next Plan sees "never came up with
		// this configuration" and tries again rather than reporting success.
		return fmt.Errorf("bring up %s: %w", iface, err)
	}

	if err := a.recordApplied(serviceID, p.Checksum, p.Shape, p.Peers); err != nil {
		return fmt.Errorf("record applied state for service %d: %w", serviceID, err)
	}
	return nil
}

// syncPeers applies a peer-list change without tearing the interface down.
//
// This is the whole reason WireGuard has a non-disruptive path: adding a user
// must not disconnect everyone already connected. The config still has to be
// written first, because `wg syncconf` reads it from disk and because an
// interface that is hot-synced but whose file was not updated would revert on
// the next reboot.
//
// SyncPeers reports whether the hot path actually worked. When it did not, the
// step fails rather than silently leaving the interface serving the old peer
// set: the reconciler will plan a restart on the next pass, which is disruptive
// but correct, and a false success here would let a revoked peer keep its
// session forever.
func (a *Adapter) syncPeers(ctx context.Context, serviceID int64, p stepPayload) error {
	if p.Config == "" {
		return fmt.Errorf("service %d: step carries no rendered config", serviceID)
	}

	iface := interfaceName(serviceID)
	configPath := a.configPath(serviceID)

	if err := a.writeConfig(configPath, p.Config); err != nil {
		return err
	}

	synced, err := a.rt.SyncPeers(ctx, iface, configPath)
	if err != nil {
		return fmt.Errorf("sync peers on %s: %w", iface, err)
	}
	if !synced {
		return fmt.Errorf("interface %s could not be hot-synced; a restart is required", iface)
	}

	if err := a.recordApplied(serviceID, p.Checksum, p.Shape, p.Peers); err != nil {
		return fmt.Errorf("record applied state for service %d: %w", serviceID, err)
	}
	return nil
}

// writeConfig writes a config atomically, 0600.
//
// Atomic because the file holds the interface's private key and wg-quick may
// read it at any moment: a torn write is a broken interface, and a
// world-readable one is a leaked server key. Rename within the same directory
// is what makes the replacement atomic.
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

// applyRemove tears down and deletes a WireGuard interface.
func (a *Adapter) applyRemove(ctx context.Context, step adapter.Step) (adapter.StepResult, error) {
	result := adapter.StepResult{Seq: step.Seq, Kind: step.Kind, Disruption: step.Disruption}
	iface := interfaceName(step.ServiceID)
	configPath := a.configPath(step.ServiceID)

	// Check if interface exists
	_, up, err := a.rt.InterfaceStatus(ctx, iface)
	if err != nil {
		result.Err = fmt.Sprintf("status check failed: %v", err)
		return result, nil
	}

	// Bring down interface if it's up
	if up {
		if err := a.rt.InterfaceDown(ctx, iface, configPath); err != nil {
			result.Err = fmt.Sprintf("failed to bring down interface: %v", err)
			return result, nil
		}
	}

	// Remove config file
	if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
		result.Err = fmt.Sprintf("interface down but config removal failed: %v", err)
		return result, nil
	}

	// Remove applied state file
	appliedPath := a.appliedPath(step.ServiceID)
	_ = os.Remove(appliedPath) // Best effort, don't fail if this errors

	result.OK = true
	return result, nil
}
