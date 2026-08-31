package ocserv

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Step kinds for the ocserv adapter.
const (
	// StepInstall writes ocserv.conf and the full user set, then starts.
	StepInstall = "install"
	// StepUpdateConfig rewrites ocserv.conf and reloads. Established sessions
	// survive a reload, so this is DisruptReload rather than DisruptRestart --
	// except for the port, which ocserv can only rebind by restarting.
	StepUpdateConfig = "update_config"
	// StepSyncUsers reconciles the passwd file. No disruption at all: ocserv
	// consults it per connection.
	StepSyncUsers = "sync_users"
	// StepRemove takes the service off the host.
	StepRemove = "remove"
)

type installPayload struct {
	Params json.RawMessage `json:"params"`
	Users  []userEntry     `json:"users"`
}

type configPayload struct {
	Params json.RawMessage `json:"params"`
}

type usersPayload struct {
	// Desired is the full account set, not a delta. Apply computes the
	// difference against the host at execution time, which is what makes the
	// step idempotent under retry: a delta computed at Plan time would be
	// wrong by the time a retry ran it.
	Desired []userEntry `json:"desired"`
}

// Plan diffs desired against observed. It is pure and repeatable: the same
// inputs always produce the same steps, which the convergence property test
// depends on.
func (a *Adapter) Plan(
	ctx context.Context, desired adapter.Desired, observed adapter.Observed,
) (adapter.Plan, error) {
	var want *adapter.Service
	for i := range desired.Services {
		s := &desired.Services[i]
		if s.Kind != string(Kind) || !s.Enabled {
			continue
		}
		if want != nil {
			// One ocserv per node. Two would fight over ocserv.conf and the
			// single system unit, and whichever applied last would win --
			// silently, and differently on every pass.
			return adapter.Plan{}, fmt.Errorf(
				"multiple ocserv services on one node are not supported")
		}
		want = s
	}

	var have *adapter.ObservedService
	if len(observed.Services) > 0 {
		have = &observed.Services[0]
	}

	switch {
	case want == nil && (have == nil || !have.Present):
		return adapter.Plan{}, nil

	case want == nil:
		// Not desired but present. Only remove what we own: a config a human
		// wrote is theirs, and deleting it because the panel has no service
		// for it would destroy work the panel never made.
		if !have.Managed {
			return adapter.Plan{}, nil
		}
		return adapter.Plan{Steps: []adapter.Step{{
			Seq:        1,
			Kind:       StepRemove,
			Disruption: adapter.DisruptRestart,
			ServiceID:  have.ID,
		}}}, nil

	case have == nil || !have.Present:
		// Validated here rather than only in Apply, so params that cannot
		// render fail the plan instead of failing halfway through an apply
		// with the config already written.
		if _, err := validated(want); err != nil {
			return adapter.Plan{}, err
		}
		payload, err := json.Marshal(installPayload{
			Params: want.Params,
			Users:  desiredUsers(desired.Subjects),
		})
		if err != nil {
			return adapter.Plan{}, err
		}
		return adapter.Plan{Steps: []adapter.Step{{
			Seq:        1,
			Kind:       StepInstall,
			Disruption: adapter.DisruptRestart,
			ServiceID:  want.ID,
			Payload:    payload,
		}}}, nil
	}

	// Present and desired. A file somebody else owns, or one edited by hand,
	// is left alone: Managed is false in both cases and rewriting it would
	// discard the change instead of reporting it.
	if !have.Managed {
		return adapter.Plan{}, nil
	}

	params, err := validated(want)
	if err != nil {
		return adapter.Plan{}, err
	}

	wantConf := checksumOf(bodyOf(renderConf(want.ID, params, a.passwdPath())))
	wantUsers := desiredUsers(desired.Subjects)
	wantUsersSum := usersChecksum(wantUsers)

	haveConf, haveUsers, ok := splitServiceChecksum(have.Checksum)
	if !ok {
		// A checksum this adapter did not write: it cannot say which half
		// differs, so it rewrites both rather than guessing. Rewriting is
		// safe; guessing wrong would leave one file stale indefinitely.
		haveConf, haveUsers = "", ""
	}

	if haveConf == wantConf && haveUsers == wantUsersSum {
		return adapter.Plan{}, nil
	}

	var steps []adapter.Step
	seq := 1

	// Config and users are separate steps because they cost differently. A
	// config change reloads the service; a user change costs nothing at all.
	// Collapsing them into one step would charge every added user the price of
	// a reload, and the reconciler debounces on the worst cost in the plan.
	if haveConf != wantConf {
		payload, err := json.Marshal(configPayload{Params: want.Params})
		if err != nil {
			return adapter.Plan{}, err
		}
		steps = append(steps, adapter.Step{
			Seq:        seq,
			Kind:       StepUpdateConfig,
			Disruption: adapter.DisruptReload,
			ServiceID:  want.ID,
			Payload:    payload,
		})
		seq++
	}

	if haveUsers != wantUsersSum {
		payload, err := json.Marshal(usersPayload{Desired: wantUsers})
		if err != nil {
			return adapter.Plan{}, err
		}
		steps = append(steps, adapter.Step{
			Seq:        seq,
			Kind:       StepSyncUsers,
			Disruption: adapter.DisruptNone,
			ServiceID:  want.ID,
			Payload:    payload,
		})
	}

	return adapter.Plan{Steps: steps}, nil
}

func (a *Adapter) passwdPath() string { return filepath.Join(a.dir, passwdName) }

// bodyOf strips the marker line, leaving what the checksum is computed over.
func bodyOf(rendered string) string {
	for i := 0; i < len(rendered); i++ {
		if rendered[i] == '\n' {
			return rendered[i+1:]
		}
	}
	return ""
}

func validated(s *adapter.Service) (serviceParams, error) {
	p, err := parseServiceParams(s.Params)
	if err != nil {
		return serviceParams{}, err
	}
	return p, nil
}
