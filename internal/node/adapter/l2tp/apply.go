package l2tp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Apply executes exactly one step. Every step must be idempotent.
func (a *Adapter) Apply(ctx context.Context, step adapter.Step) (adapter.StepResult, error) {
	start := time.Now()

	var err error
	switch step.Kind {
	case StepInstallConfigs:
		err = a.applyInstallConfigs(ctx, step)
	case StepUpdateConfigs:
		err = a.applyUpdateConfigs(ctx, step)
	case StepReloadCredentials:
		err = a.applyReloadCredentials(ctx, step)
	case StepRemoveConfigs:
		err = a.applyRemoveConfigs(ctx, step)
	default:
		err = fmt.Errorf("unknown step kind: %s", step.Kind)
	}

	return adapter.StepResult{
		Seq:        step.Seq,
		Kind:       step.Kind,
		Disruption: step.Disruption,
		OK:         err == nil,
		Err:        errString(err),
		Duration:   time.Since(start),
	}, nil
}

func (a *Adapter) applyInstallConfigs(ctx context.Context, step adapter.Step) error {
	var payload installPayload
	if err := json.Unmarshal(step.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal payload: %w", err)
	}

	params, err := parseServiceParams(payload.Params)
	if err != nil {
		return err
	}

	// Write all config files.
	configs := map[string]string{
		"strongswan/ipsec.conf":      renderIPsecConf(step.ServiceID, params),
		"strongswan/ipsec.secrets":   renderIPsecSecrets(step.ServiceID, params),
		"xl2tpd/xl2tpd.conf":         renderXL2TPDConf(step.ServiceID, params),
		"ppp/chap-secrets":           renderCHAPSecrets(step.ServiceID, payload.Subjects),
		"ppp/options.xl2tpd":         renderPPPOptions(step.ServiceID, params),
	}

	for relPath, content := range configs {
		fullPath := filepath.Join(a.confDir, relPath)
		if err := a.writeFile(fullPath, content); err != nil {
			return fmt.Errorf("write %s: %w", relPath, err)
		}
	}

	// Start services (idempotent: if already running, this is a no-op).
	if err := startService("strongswan"); err != nil {
		return err
	}
	if err := startService("xl2tpd"); err != nil {
		return err
	}

	return nil
}

func (a *Adapter) applyUpdateConfigs(ctx context.Context, step adapter.Step) error {
	// Update is the same as install, but we restart instead of start.
	if err := a.applyInstallConfigs(ctx, step); err != nil {
		return err
	}

	if err := restartService("strongswan"); err != nil {
		return err
	}
	if err := restartService("xl2tpd"); err != nil {
		return err
	}

	return nil
}

func (a *Adapter) applyReloadCredentials(ctx context.Context, step adapter.Step) error {
	var subjects []adapter.Subject
	if err := json.Unmarshal(step.Payload, &subjects); err != nil {
		return fmt.Errorf("unmarshal subjects: %w", err)
	}

	// Write updated CHAP secrets.
	chapPath := filepath.Join(a.confDir, "ppp/chap-secrets")
	content := renderCHAPSecrets(step.ServiceID, subjects)
	if err := a.writeFile(chapPath, content); err != nil {
		return fmt.Errorf("write chap-secrets: %w", err)
	}

	// Reload strongSwan credentials (no tunnel disruption).
	if err := reloadStrongSwanCreds(); err != nil {
		return err
	}

	// SIGHUP xl2tpd to re-read CHAP secrets.
	if err := reloadService("xl2tpd"); err != nil {
		return err
	}

	return nil
}

func (a *Adapter) applyRemoveConfigs(ctx context.Context, step adapter.Step) error {
	// Stop services (best effort).
	_ = stopService("strongswan")
	_ = stopService("xl2tpd")

	// Remove managed files (best effort).
	paths := []string{
		"strongswan/ipsec.conf",
		"strongswan/ipsec.secrets",
		"xl2tpd/xl2tpd.conf",
		"ppp/chap-secrets",
		"ppp/options.xl2tpd",
	}

	for _, relPath := range paths {
		fullPath := filepath.Join(a.confDir, relPath)
		_ = os.Remove(fullPath)
	}

	return nil
}

// writeFile writes content to path atomically (temp + rename).
func (a *Adapter) writeFile(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	// Atomic write: temp + rename.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s to %s: %w", tmp, path, err)
	}

	return nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
