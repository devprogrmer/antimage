package wireguard

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Plan computes the steps needed to move from observed to desired state.
func (a *Adapter) Plan(ctx context.Context, desired adapter.Desired, observed adapter.Observed) (adapter.Plan, error) {
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
		needsRestart, needsReload, reason := a.needsUpdate(dsvc, obs)

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
		_ = reason // unused for now
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

// needsUpdate determines if a service needs updating and why.
// Returns (needsRestart, needsReload, reason).
func (a *Adapter) needsUpdate(desired adapter.Service, observed adapter.ObservedService) (bool, bool, string) {
	// If config is drifted (modified externally), always restore it
	if !observed.Managed {
		return true, false, "config drift detected"
	}

	// For WireGuard, we need to check if observed checksum matches what we expect
	// A full implementation would regenerate the config and compare checksums
	// For now, trust that if it's managed and present, only explicit changes need updates
	if err != nil {
		return true, false, fmt.Sprintf("config generation failed: %v", err)
	}

	// Extract checksum from generated config
	lines := splitLines(desiredConfig)
	if len(lines) < 2 {
		return true, false, "invalid generated config"
	}
	_, desiredChecksum, ok := parseMarker(lines[0])
	if !ok {
		return true, false, "failed to parse marker"
	}

	// If checksums match, no update needed
	if desiredChecksum == observed.ConfigHash {
		// But check if interface is down when it should be up
		if observed.Status != "active" {
			return true, false, "interface is down"
		}
		return false, false, ""
	}

	// Checksums differ - determine if it's structural or just membership
	applied := a.applied(desired.ID)
	if applied.Checksum == "" {
		// Don't know what's running, must restart
		return true, false, "applied state unknown"
	}

	// Check if only peers changed
	desiredPeers := extractPublicKeys(peers)
	appliedPeers := applied.Peers

	if onlyMembershipChanged(params, applied.Checksum, desiredChecksum) {
		// Structural params unchanged, only peer list differs
		removed := removedPeers(appliedPeers, desiredPeers)
		if len(removed) > 0 {
			// Peer removal requires restart (wg doesn't support hot removal cleanly)
			return true, false, "peers removed"
		}
		// Only additions, can hot-reload
		return false, true, "peers added"
	}

	// Structural change (port, subnet, private key, etc.)
	return true, false, "structural configuration changed"
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
	lines := splitLines(structuralConfig)
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


