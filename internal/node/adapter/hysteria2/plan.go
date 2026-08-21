package hysteria2

import (
	"context"
	"encoding/json"
	"fmt"

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
		needsUpdate := a.needsUpdate(dsvc, obs)
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

// needsUpdate determines if service needs updating
func (a *Adapter) needsUpdate(desired adapter.Service, observed adapter.ObservedService) bool {
	// If config drifted (modified externally), restore it
	if !observed.Managed {
		return true
	}

	// For Hysteria2, any config change requires restart
	// Compare observed checksum with what we would generate
	// For now, simplified: assume update needed if not managed
	// Full implementation would regenerate and compare checksums

	return false
}
