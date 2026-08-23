package xray

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

const policyConfigFile = "antimage-policy.json"

// ensurePolicyConfig writes the policy configuration file with speed limits.
// This is a separate config document that Xray merges with inbound files.
func (a *Adapter) ensurePolicyConfig(ctx context.Context, desired []byte) error {
	if !a.hotAdd {
		// No API means no policy capability.
		return nil
	}

	rt, ok := a.rt.(*ExecRuntime)
	if !ok {
		// For testing or non-standard runtimes, check if we should skip
		return nil
	}

	if rt.APIAddress == "" {
		return nil
	}

	path := filepath.Join(a.dir, policyConfigFile)

	// Check if current policy is already correct
	existing, err := os.ReadFile(path)
	if err == nil && string(existing) == string(desired) {
		// Already current
		return nil
	}

	// Write new policy config
	if err := os.MkdirAll(a.dir, 0o700); err != nil {
		return fmt.Errorf("create xray confdir: %w", err)
	}

	tmp, err := os.CreateTemp(a.dir, "antimage-policy-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp policy config: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(desired); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp policy config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp policy config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp policy config: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("chmod temp policy config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("install policy config: %w", err)
	}

	return nil
}

// removePolicyConfig removes the policy configuration file when no policies exist.
