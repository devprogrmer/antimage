package l2tp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Step kinds for the L2TP adapter.
const (
	StepInstallConfigs    = "install_configs"
	StepUpdateConfigs     = "update_configs"
	StepReloadCredentials = "reload_credentials"
	StepRemoveConfigs     = "remove_configs"
)

// Plan diffs desired state against observed state and emits steps.
func (a *Adapter) Plan(ctx context.Context, desired adapter.Desired, observed adapter.Observed) (adapter.Plan, error) {
	var steps []adapter.Step

	// Find L2TP service in desired state.
	var l2tpService *adapter.Service
	for i := range desired.Services {
		if desired.Services[i].Kind == string(Kind) && desired.Services[i].Enabled {
			if l2tpService != nil {
				// SP6 design decision 7: one L2TP service per node.
				return adapter.Plan{}, fmt.Errorf("multiple L2TP services not supported on one node")
			}
			l2tpService = &desired.Services[i]
		}
	}

	// Find observed L2TP service.
	var observedService *adapter.ObservedService
	if len(observed.Services) > 0 {
		observedService = &observed.Services[0]
		observedService.ID = 0
		if l2tpService != nil {
			observedService.ID = l2tpService.ID
		}
	}

	// Case 1: Service desired but not present → install.
	if l2tpService != nil && (observedService == nil || !observedService.Present) {
		payload, err := a.buildInstallPayload(l2tpService.ID, l2tpService.Params, desired.Subjects)
		if err != nil {
			return adapter.Plan{}, err
		}
		steps = append(steps, adapter.Step{
			Seq:        1,
			Kind:       StepInstallConfigs,
			Disruption: adapter.DisruptRestart,
			ServiceID:  l2tpService.ID,
			Payload:    payload,
		})
		return adapter.Plan{Steps: steps}, nil
	}

	// Case 2: Service not desired but present → remove.
	if l2tpService == nil && observedService != nil && observedService.Present {
		steps = append(steps, adapter.Step{
			Seq:        1,
			Kind:       StepRemoveConfigs,
			Disruption: adapter.DisruptRestart,
			ServiceID:  observedService.ID,
		})
		return adapter.Plan{Steps: steps}, nil
	}

	// Case 3: Service present and desired → check for changes.
	if l2tpService != nil && observedService != nil && observedService.Managed {
		params, err := parseServiceParams(l2tpService.Params)
		if err != nil {
			return adapter.Plan{}, err
		}

		// Render all configs with desired state.
		desiredIPsec := renderIPsecConf(l2tpService.ID, params)
		desiredSecrets := renderIPsecSecrets(l2tpService.ID, params)
		desiredXL2TPD := renderXL2TPDConf(l2tpService.ID, params)
		desiredCHAP := renderCHAPSecrets(l2tpService.ID, desired.Subjects)
		desiredOpts := renderPPPOptions(l2tpService.ID, params)

		combined := checksumOf(desiredIPsec) + ":" + checksumOf(desiredSecrets) + ":" +
			checksumOf(desiredXL2TPD) + ":" + checksumOf(desiredCHAP) + ":" + checksumOf(desiredOpts)
		desiredChecksum := checksumOf(combined)

		if desiredChecksum != observedService.Checksum {
			// Determine if only users changed (reload) or params changed (restart).
			// For simplicity: check if params-dependent files changed.
			paramsChanged := a.detectParamsChange(l2tpService.ID, params)

			if paramsChanged {
				// Service params changed → full restart.
				payload, err := a.buildInstallPayload(l2tpService.ID, l2tpService.Params, desired.Subjects)
				if err != nil {
					return adapter.Plan{}, err
				}
				steps = append(steps, adapter.Step{
					Seq:        1,
					Kind:       StepUpdateConfigs,
					Disruption: adapter.DisruptRestart,
					ServiceID:  l2tpService.ID,
					Payload:    payload,
				})
			} else {
				// Only users changed → hot reload.
				payload, err := json.Marshal(desired.Subjects)
				if err != nil {
					return adapter.Plan{}, fmt.Errorf("marshal subjects: %w", err)
				}
				steps = append(steps, adapter.Step{
					Seq:        1,
					Kind:       StepReloadCredentials,
					Disruption: adapter.DisruptReload,
					ServiceID:  l2tpService.ID,
					Payload:    payload,
				})
			}
		}
	}

	// Case 4: Service present but not managed (drift).
	// Drift is reported via Probe, not Plan.
	// Plan returns empty to avoid overwriting unmanaged files without operator approval.

	return adapter.Plan{Steps: steps}, nil
}

// buildInstallPayload combines service params and subjects into one payload.
type installPayload struct {
	Params   json.RawMessage   `json:"params"`
	Subjects []adapter.Subject `json:"subjects"`
}

func (a *Adapter) buildInstallPayload(serviceID int64, params json.RawMessage, subjects []adapter.Subject) (json.RawMessage, error) {
	payload := installPayload{
		Params:   params,
		Subjects: subjects,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal install payload: %w", err)
	}
	return data, nil
}

// detectParamsChange checks if service params (not users) changed.
// For now, we simplify: if CHAP changed but nothing else, it's user-only.
func (a *Adapter) detectParamsChange(serviceID int64, params ServiceParams) bool {
	// TODO: Read current files and compare params-dependent sections.
	// For Phase C, we conservatively assume params changed if checksums differ.
	// This means we might restart when a reload would suffice, but we're always safe.
	return true
}
