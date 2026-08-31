package hysteria2

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Plan computes steps to move from observed to desired state
func (a *Adapter) Plan(ctx context.Context, desired adapter.Desired, observed adapter.Observed) (adapter.Plan, error) {
	var steps []adapter.Step

	// Only this adapter's services, and that filter is load-bearing.
	//
	// A node runs several adapters over ONE desired document, each handling the
	// services of its own kind -- which is why Xray and sing-box have carried the
	// same check since they were written. Without it this adapter reads every
	// service in the document, and one belonging to another adapter whose params
	// happen to satisfy the Hysteria2 schema is written out as a Hysteria2
	// server. Foreign params usually fail ServiceParams.Validate, which hid this,
	// but that is a coincidence and not a rule.
	desiredMap := make(map[int64]adapter.Service)
	for _, svc := range desired.Services {
		if svc.Kind != string(Kind) {
			continue // another adapter owns it
		}
		desiredMap[svc.ID] = svc
	}
	observedMap := make(map[int64]adapter.ObservedService)
	for _, svc := range observed.Services {
		observedMap[svc.ID] = svc
	}

	// Handle services that should exist
	for _, dsvc := range desired.Services {
		if dsvc.Kind != string(Kind) {
			continue // another adapter owns it
		}
		obs, exists := observedMap[dsvc.ID]

		// Rendered HERE and carried to Apply in the step payload. Apply never
		// sees adapter.Desired -- that is what AD-3 is about -- and rendering
		// the same service twice invites the two copies to disagree.
		payload, err := a.buildPayload(dsvc, desired.Subjects)
		if err != nil {
			// An unrenderable service cannot be installed or repaired. Planning
			// a step certain to fail buries the reason in an apply-run; the next
			// pass retries once the params are fixed.
			continue
		}

		if !exists {
			// New service: install
			steps = append(steps, adapter.Step{
				Kind:       "install",
				ServiceID:  dsvc.ID,
				Disruption: adapter.DisruptRestart,
				Payload:    payload,
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
				Payload:    payload,
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

// stepPayload is what Apply needs to execute a step without re-deriving it.
//
// Same role as the Xray adapter's type of the same name: Plan renders, Apply
// writes.
type stepPayload struct {
	// Config is the fully rendered server config, marker included.
	Config string `json:"config,omitempty"`
	// Checksum is Config's body checksum, in the domain Observe reports, so the
	// applied sidecar and the marker can be compared directly.
	Checksum string `json:"checksum,omitempty"`
	// Users is the username set this config serves, recorded in the sidecar so
	// a later Plan can tell whether anybody is being removed.
	Users []string `json:"users,omitempty"`
}

// buildPayload renders a service and everything Apply needs to install it.
func (a *Adapter) buildPayload(svc adapter.Service, subjects []adapter.Subject) (json.RawMessage, error) {
	var params ServiceParams
	if err := json.Unmarshal(svc.Params, &params); err != nil {
		return nil, fmt.Errorf("service %d params: %w", svc.ID, err)
	}
	auths := UserAuthFromSubjects(subjects)
	rendered, err := GenerateConfig(svc.ID, params, auths)
	if err != nil {
		return nil, fmt.Errorf("render service %d: %w", svc.ID, err)
	}
	users := make([]string, 0, len(auths))
	for _, u := range auths {
		users = append(users, u.Username)
	}
	sort.Strings(users)
	body, err := json.Marshal(stepPayload{
		Config:   rendered,
		Checksum: renderedChecksum(rendered),
		Users:    users,
	})
	if err != nil {
		return nil, fmt.Errorf("encode payload for service %d: %w", svc.ID, err)
	}
	return body, nil
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
	// renderedChecksum, not checksumContent: `rendered` still carries its
	// marker line, and observed.Checksum is read OUT of that marker, so it
	// covers the body alone. Hashing the whole string produced a value that
	// could never match, so every converged service asked for a restart on
	// every pass -- harmless only while Apply refused to perform one.
	want := renderedChecksum(rendered)

	if observed.Checksum != want {
		return true
	}
	// The file is right. A correct file the process never loaded is not
	// convergence, so an applied state that does not match forces a restart.
	return a.applied(desired.ID).Checksum != want
}
