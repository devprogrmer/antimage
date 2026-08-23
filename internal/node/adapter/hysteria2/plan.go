package hysteria2

import (
	"context"
	"encoding/json"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Plan computes steps to move from observed to desired state
func (a *Adapter) Plan(ctx context.Context, desired adapter.Desired, observed adapter.Observed) (adapter.Plan, error) {
	var steps []adapter.Step

	// Build lookup maps
	desiredMap := make(map[int64]adapter.Service)
	for _, svc := range desired.Services {
		desiredMap[svc.ID] = svc
	}
	observedMap := make(map[int64]adapter.ObservedService)
	for _, svc := range observed.Services {
		observedMap[svc.ID] = svc
	}

	// Handle services that should exist
	for _, dsvc := range desired.Services {
		obs, exists := observedMap[dsvc.ID]

		if !exists {
			// New service: install
			steps = append(steps, adapter.Step{
				Kind:       "install",
				ServiceID:  dsvc.ID,
				Disruption: adapter.DisruptRestart,
			})
			continue
		}

		// Service exists, check if update needed
		needsUpdate := a.needsUpdate(dsvc, obs, desired.Subjects)
		if needsUpdate {
			steps = append(steps, adapter.Step{
				Kind:       "restart",
				ServiceID:  dsvc.ID,
				Disruption: adapter.DisruptRestart,
			})
		}
	}

	// Handle services that should be removed
	for _, obs := range observed.Services {
		if _, wanted := desiredMap[obs.ID]; !wanted {
			steps = append(steps, adapter.Step{
				Kind:       "remove",
				ServiceID:  obs.ID,
				Disruption: adapter.DisruptRestart,
			})
		}
	}

	return adapter.Plan{Steps: steps}, nil
}

// needsUpdate determines if a service needs updating.
//
// This previously returned false for every managed service, so Hysteria2 never
// converged after the initial install: a user added to the desired document
// was never written to the config, and a revoked user was never removed. The
// helpers it needed -- UserAuthFromSubjects, GenerateConfig and the applied
// sidecar -- already existed and were simply never called, which is why the
// linter reported them as dead.
//
// Every change on this adapter is restart-class, so there is no hot path to get
// wrong; the only questions are whether the file differs from desired and
// whether the process ever loaded it.
func (a *Adapter) needsUpdate(
	desired adapter.Service, observed adapter.ObservedService, subjects []adapter.Subject,
) bool {
	// Somebody edited the file by hand. Restore it rather than trusting it.
	if !observed.Managed {
		return true
	}

	var params ServiceParams
	if err := json.Unmarshal(desired.Params, &params); err != nil {
		return true // unreadable params: rewrite rather than assume
	}
	rendered, err := GenerateConfig(desired.ID, params, UserAuthFromSubjects(subjects))
	if err != nil {
		return true
	}
	want := checksumContent([]byte(rendered))

	if observed.Checksum != want {
		return true
	}
	// The file is right. A correct file the process never loaded is not
	// convergence, so an applied state that does not match forces a restart.
	return a.applied(desired.ID).Checksum != want
}
