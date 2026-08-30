package openvpn

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Step kinds for the OpenVPN adapter.
const (
	// StepInstall writes all three files and starts the service.
	StepInstall = "install"
	// StepUpdateConfig rewrites server.conf and restarts.
	//
	// RESTART, not reload. OpenVPN has no reload that re-reads server.conf:
	// SIGHUP restarts the tunnel anyway, so calling this a reload would tell
	// the reconciler a change is cheaper than it is and skip the maintenance
	// window it should have waited for.
	StepUpdateConfig = "update_config"
	// StepSyncUsers rewrites the user file and, when it changed, the verify
	// script. Costs nothing: the script reads the file per login.
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
	// The full desired set, not a delta. Apply rewrites the file wholesale,
	// which is what makes a retry after a partial failure land in the same
	// place as a first run.
	Desired []userEntry `json:"desired"`
}

// Plan diffs desired against observed. Pure and repeatable.
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
			// Two would fight over one server.conf and one unit, and whichever
			// applied last would win -- differently on every pass.
			return adapter.Plan{}, fmt.Errorf(
				"multiple openvpn services on one node are not supported")
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
		// Only remove what we own. A config a human wrote is theirs.
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
		// Validated here so params that cannot render fail the plan rather
		// than failing mid-apply with the config already written.
		if _, err := parseServiceParams(want.Params); err != nil {
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
	// is left alone -- Managed is false in both cases, and rewriting would
	// discard the change instead of reporting it.
	if !have.Managed {
		return adapter.Plan{}, nil
	}

	params, err := parseServiceParams(want.Params)
	if err != nil {
		return adapter.Plan{}, err
	}

	wantUsers := desiredUsers(desired.Subjects)
	wantConf := checksumOf(bodyOf(renderConf(want.ID, params, a.dir)))
	wantVerify := checksumOf(bodyOf(renderVerify(want.ID, a.usersPath())))
	wantUsersSum := checksumOf(bodyOf(renderUsers(want.ID, wantUsers)))

	haveConf, haveVerify, haveUsers, ok := splitServiceChecksum(have.Checksum)
	if !ok {
		// A checksum this adapter did not write. It cannot tell which file
		// differs, so it rewrites all of them rather than guessing: rewriting
		// is safe, guessing wrong leaves one stale indefinitely.
		haveConf, haveVerify, haveUsers = "", "", ""
	}

	if haveConf == wantConf && haveVerify == wantVerify && haveUsers == wantUsersSum {
		return adapter.Plan{}, nil
	}

	var steps []adapter.Step
	seq := 1

	if haveConf != wantConf {
		payload, err := json.Marshal(configPayload{Params: want.Params})
		if err != nil {
			return adapter.Plan{}, err
		}
		steps = append(steps, adapter.Step{
			Seq:        seq,
			Kind:       StepUpdateConfig,
			Disruption: adapter.DisruptRestart,
			ServiceID:  want.ID,
			Payload:    payload,
		})
		seq++
	}

	// The verify script and the user file move together: the script names the
	// file it reads, so a change to either is one free step rather than two.
	if haveUsers != wantUsersSum || haveVerify != wantVerify {
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
