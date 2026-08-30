package ocserv

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Apply executes exactly one step. Every step is idempotent, because a retry
// after a partial failure re-runs it.
func (a *Adapter) Apply(ctx context.Context, step adapter.Step) (adapter.StepResult, error) {
	start := time.Now()

	var err error
	switch step.Kind {
	case StepInstall:
		err = a.applyInstall(ctx, step)
	case StepUpdateConfig:
		err = a.applyUpdateConfig(ctx, step)
	case StepSyncUsers:
		err = a.applySyncUsers(ctx, step)
	case StepRemove:
		err = a.applyRemove(ctx, step)
	default:
		err = fmt.Errorf("unknown ocserv step kind %q", step.Kind)
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

func (a *Adapter) applyInstall(ctx context.Context, step adapter.Step) error {
	var payload installPayload
	if err := json.Unmarshal(step.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal install payload: %w", err)
	}
	a.rememberServiceID(step.ServiceID)
	if err := a.writeConf(step.ServiceID, payload.Params); err != nil {
		return err
	}
	if err := a.syncUsers(ctx, payload.Users); err != nil {
		return err
	}
	// Restart rather than start: install also covers the case where ocserv is
	// already running under a config we just replaced, and start on a running
	// unit is a no-op that would leave the old config live.
	return a.rt.Restart(ctx)
}

func (a *Adapter) applyUpdateConfig(ctx context.Context, step adapter.Step) error {
	var payload configPayload
	if err := json.Unmarshal(step.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal config payload: %w", err)
	}
	a.rememberServiceID(step.ServiceID)
	if err := a.writeConf(step.ServiceID, payload.Params); err != nil {
		return err
	}
	return a.rt.Reload(ctx)
}

func (a *Adapter) applySyncUsers(ctx context.Context, step adapter.Step) error {
	var payload usersPayload
	if err := json.Unmarshal(step.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal users payload: %w", err)
	}
	return a.syncUsers(ctx, payload.Desired)
}

func (a *Adapter) applyRemove(ctx context.Context, step adapter.Step) error {
	if err := a.rt.Stop(ctx); err != nil {
		return err
	}
	// Both files, and a missing one is success: remove has to be idempotent,
	// and a retry after a partial failure finds the first already gone.
	for _, name := range []string{confName, passwdName} {
		if err := os.Remove(filepath.Join(a.dir, name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", name, err)
		}
	}
	return nil
}

// writeConf renders and installs ocserv.conf.
//
// Written to a temp file in the same directory and renamed, so a crash midway
// leaves the previous config intact rather than a truncated one. ocserv reads
// this file on start and reload; a half-written file would stop the service
// coming back.
func (a *Adapter) writeConf(serviceID int64, raw json.RawMessage) error {
	params, err := parseServiceParams(raw)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(a.dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", a.dir, err)
	}

	rendered := renderConf(serviceID, params, a.passwdPath())
	final := filepath.Join(a.dir, confName)
	tmp := final + ".tmp"

	// 0600: ocserv.conf names the server key path and, on some deployments,
	// carries secrets directly. It is read by root before privileges are
	// dropped, so it does not need to be world-readable.
	if err := os.WriteFile(tmp, []byte(rendered), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("install %s: %w", final, err)
	}
	return nil
}

// syncUsers reconciles the passwd file with the desired account set.
//
// The difference is computed HERE, against the host as it is now, rather than
// at Plan time. That is what makes the step idempotent: a retry re-reads the
// file and does whatever is still outstanding, where a precomputed delta would
// re-add users that already exist and, worse, re-delete ones that had been
// legitimately added since.
//
// Only accounts this adapter owns are removed. A name that does not match the
// subject-N pattern belongs to somebody else -- an operator's own test account,
// or an ocserv installation that predates the agent -- and deleting it because
// the panel has no subject for it would destroy access nobody asked us to
// manage.
func (a *Adapter) syncUsers(ctx context.Context, desired []userEntry) error {
	existing, err := a.readUsernames()
	if err != nil {
		return fmt.Errorf("read passwd file: %w", err)
	}
	have := make(map[string]bool, len(existing))
	for _, n := range existing {
		have[n] = true
	}

	want := make(map[string]bool, len(desired))
	for _, u := range desired {
		want[u.Name] = true
		// Set unconditionally rather than only for missing accounts: the
		// password may have been rotated in the panel while the account name
		// stayed the same, and the name-only checksum cannot see that. Setting
		// an unchanged password is harmless.
		if err := a.rt.SetPassword(ctx, a.passwdPath(), u.Name, u.Password); err != nil {
			return fmt.Errorf("set password for %s: %w", u.Name, err)
		}
	}

	for name := range have {
		if want[name] {
			continue
		}
		if _, ours := subjectIDFromUsername(name); !ours {
			continue
		}
		if err := a.rt.DeletePassword(ctx, a.passwdPath(), name); err != nil {
			return fmt.Errorf("delete %s: %w", name, err)
		}
	}
	return nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
