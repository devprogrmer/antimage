package openvpn

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// File modes.
//
// The user file holds password digests and the verify script decides who may
// log in; both are read by OpenVPN as root before it drops privileges, so
// neither needs to be readable by anyone else. 0600 and 0700 are asserted in a
// test rather than left to convention, because a salted single-round SHA-256
// is only adequate while the file cannot be read.
const (
	confMode   os.FileMode = 0o600
	verifyMode os.FileMode = 0o700
	usersMode  os.FileMode = 0o600
)

// Apply executes exactly one step. Every step is idempotent.
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
		err = fmt.Errorf("unknown openvpn step kind %q", step.Kind)
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

	// Users and the verify script BEFORE the config, then start.
	//
	// Order matters on a first install: OpenVPN reads server.conf at startup
	// and execs the verify script on the first login. Writing the config first
	// and starting before the script existed would leave a server that refuses
	// every connection until the next pass.
	if err := a.writeUsers(step.ServiceID, payload.Users); err != nil {
		return err
	}
	if err := a.writeVerify(step.ServiceID); err != nil {
		return err
	}
	if err := a.writeConf(step.ServiceID, payload.Params); err != nil {
		return err
	}
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
	// Restart, not reload: OpenVPN has no way to re-read server.conf short of
	// restarting, and pretending otherwise would leave the new config on disk
	// and the old one running.
	return a.rt.Restart(ctx)
}

func (a *Adapter) applySyncUsers(ctx context.Context, step adapter.Step) error {
	var payload usersPayload
	if err := json.Unmarshal(step.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal users payload: %w", err)
	}
	if err := a.writeUsers(step.ServiceID, payload.Desired); err != nil {
		return err
	}
	// The script names the file it reads, so it is rewritten alongside. No
	// restart: OpenVPN execs it fresh on every login attempt.
	return a.writeVerify(step.ServiceID)
}

func (a *Adapter) applyRemove(ctx context.Context, step adapter.Step) error {
	if err := a.rt.Stop(ctx); err != nil {
		return err
	}
	// A missing file is success: remove has to be idempotent, and a retry
	// after a partial failure finds the earlier ones already gone.
	for _, name := range []string{confName, verifyName, usersName, statusName} {
		if err := os.Remove(filepath.Join(a.dir, name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", name, err)
		}
	}
	return nil
}

func (a *Adapter) writeConf(serviceID int64, raw json.RawMessage) error {
	params, err := parseServiceParams(raw)
	if err != nil {
		return err
	}
	return a.writeFile(confName, renderConf(serviceID, params, a.dir), confMode)
}

func (a *Adapter) writeVerify(serviceID int64) error {
	return a.writeFile(verifyName, renderVerify(serviceID, a.usersPath()), verifyMode)
}

func (a *Adapter) writeUsers(serviceID int64, users []userEntry) error {
	return a.writeFile(usersName, renderUsers(serviceID, users), usersMode)
}

// writeFile installs one file atomically.
//
// Written to a temp file in the same directory and renamed, so a crash midway
// leaves the previous version intact rather than a truncated one. That matters
// most for the verify script: a half-written script is a server that refuses
// every login, and for the config, which a truncated copy stops from starting.
//
// The mode is set on the temp file BEFORE the rename, so the final file is
// never briefly world-readable -- WriteFile's mode is subject to the umask,
// and Chmod after the rename leaves a window.
func (a *Adapter) writeFile(name, content string, mode os.FileMode) error {
	if err := os.MkdirAll(a.dir, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", a.dir, err)
	}
	final := filepath.Join(a.dir, name)
	tmp := final + ".tmp"

	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("install %s: %w", final, err)
	}
	return nil
}

func (a *Adapter) usersPath() string  { return filepath.Join(a.dir, usersName) }
func (a *Adapter) statusPath() string { return filepath.Join(a.dir, statusName) }

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
