package wireguard

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Plan computes the steps needed to move from observed to desired state.
func (a *Adapter) Plan(ctx context.Context, desired adapter.Desired, observed adapter.Observed) (adapter.Plan, error) {
	// Rebuild peer registry from current desired state
	a.registry.update(desired.Subjects)

	var steps []adapter.Step

	// Build maps for efficient lookup
	desiredMap := make(map[int64]adapter.Service)
	for _, svc := range desired.Services {
		desiredMap[svc.ID] = svc
	}
	observedMap := make(map[int64]adapter.ObservedService)
	for _, svc := range observed.Services {
		observedMap[svc.ID] = svc
	}

	// 1. Handle services that should exist
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

		// Service exists, check if it needs updates
		needsRestart, needsReload, reason := a.needsUpdate(dsvc, obs, desired.Subjects)

		if needsRestart {
			steps = append(steps, adapter.Step{
				Kind:       "restart",
				ServiceID:  dsvc.ID,
				Disruption: adapter.DisruptRestart,
			})
		} else if needsReload {
			steps = append(steps, adapter.Step{
				Kind:       "reload",
				ServiceID:  dsvc.ID,
				Disruption: adapter.DisruptNone,
			})
		}
		_ = reason // surfaced in step payloads by a later change
	}

	// 2. Handle services that should be removed
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

// needsUpdate decides whether an existing interface has to change, and how
// disruptively. Returns (needsRestart, needsReload, reason).
//
// This previously returned "no change needed" for every managed service, which
// meant WireGuard never converged after the initial install: a peer added to
// the desired document was never added to the interface, and a revoked peer was
// never removed. The helpers it needed -- buildPeerList, removedPeers and the
// applied sidecar -- already existed and were simply never called, which is why
// the linter reported them as dead.
//
// The classification mirrors the Xray adapter:
//
//   - the file matches desired but the interface never came up with it ->
//     restart, because a correct file the runtime never loaded is not
//     convergence;
//   - any peer removed -> restart, because wg keeps serving a peer until it is
//     explicitly told otherwise, so dropping one from the file alone leaves a
//     revoked user connected;
//   - peers added only -> reload, which applies without dropping sessions;
//   - anything else (port, key, subnet) -> restart.
func (a *Adapter) needsUpdate(
	desired adapter.Service, observed adapter.ObservedService, subjects []adapter.Subject,
) (bool, bool, string) {
	// Somebody edited the file by hand. Restore it rather than trusting it.
	if !observed.Managed {
		return true, false, "config drift detected"
	}

	var params ServiceParams
	if err := json.Unmarshal(desired.Params, &params); err != nil {
		return true, false, "service params are unreadable"
	}

	peers := a.buildPeerList(desired, subjects)
	rendered, err := GenerateConfig(desired.ID, params, peers)
	if err != nil {
		return true, false, "config could not be rendered"
	}
	want := checksumConfigBody(rendered)
	applied := a.applied(desired.ID)

	if observed.Checksum == want {
		// The file is right. Did the interface actually come up with it?
		if applied.Checksum != want {
			return true, false, "interface never came up with this configuration"
		}
		return false, false, ""
	}

	// Content differs. A removal is restart-class, the same property the Xray
	// adapter enforces and for the same reason.
	if len(removedPeers(applied.Peers, extractPublicKeys(peers))) > 0 {
		return true, false, "peer removed"
	}

	// Purely additive, and only when we know what the interface is serving. An
	// unknown applied state forces a restart, which is the safe direction.
	if applied.Checksum != "" && onlyMembershipChanged(params, applied.Checksum, want) {
		return false, true, "peers added"
	}
	return true, false, "configuration changed"
}

// buildPeerList constructs PeerConfig entries from desired subjects.
func (a *Adapter) buildPeerList(desired adapter.Service, subjects []adapter.Subject) []PeerConfig {
	var peers []PeerConfig

	// Parse params to get subnet
	var params ServiceParams
	if err := json.Unmarshal(desired.Params, &params); err != nil {
		return nil
	}

	for _, subj := range subjects {
		// Extract peer public key from credentials
		// WireGuard credential is stored as "keypair" credential kind
		var pubKey string
		for _, cred := range subj.Credentials {
			if cred.Kind == string(adapter.CredKeypair) {
				pubKey = cred.Value
				break
			}
		}
		if pubKey == "" {
			continue
		}

		// Allocate IP for this peer
		allowedIP, err := AllocatePeerIP(params.Subnet, subj.ID)
		if err != nil {
			continue
		}

		keepalive := params.Keepalive
		if keepalive == 0 {
			keepalive = 25 // Default: keep NAT mappings alive
		}

		peers = append(peers, PeerConfig{
			PublicKey:  pubKey,
			AllowedIPs: allowedIP,
			Keepalive:  keepalive,
		})
	}

	return peers
}

// extractPublicKeys returns just the public keys from peer configs.
func extractPublicKeys(peers []PeerConfig) []string {
	keys := make([]string, len(peers))
	for i, p := range peers {
		keys[i] = p.PublicKey
	}
	return keys
}

// onlyMembershipChanged checks if the difference between two configs is purely
// in the peer list, with no structural changes.
func onlyMembershipChanged(params ServiceParams, oldChecksum, newChecksum string) bool {
	// Generate config with NO peers to get the structural checksum
	emptyPeers := []PeerConfig{}
	structuralConfig, err := GenerateConfig(0, params, emptyPeers)
	if err != nil {
		return false
	}

	// If the structural part matches, then the difference is only membership
	lines := strings.Split(structuralConfig, "\n")
	if len(lines) < 2 {
		return false
	}
	_, structChecksum, ok := parseMarker(lines[0])
	if !ok {
		return false
	}

	// This is a simplified heuristic. A proper implementation would parse
	// both configs and compare the [Interface] section.
	// For now, if checksums differ, assume structural change unless we
	// can prove otherwise through more detailed parsing.
	return structChecksum == oldChecksum
}
