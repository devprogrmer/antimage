package wireguard

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

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

		// The config is rendered HERE, in Plan, and carried to Apply in the
		// step payload. Apply has no access to desired state -- that is the
		// whole of AD-3 -- and re-deriving it there would mean two renders of
		// the same service that could disagree. Xray and sing-box already work
		// this way; this follows them rather than inventing a third route.
		payload, err := a.buildPayload(dsvc, desired.Subjects)
		if err != nil {
			// An unrenderable service cannot be installed or repaired, and
			// planning a step that is certain to fail buries the real reason in
			// an apply-run. Skipping leaves the node as it is and the next pass
			// tries again once the params are fixed.
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

		// Service exists, check if it needs updates
		needsRestart, needsReload, reason := a.needsUpdate(dsvc, obs, desired.Subjects)

		if needsRestart {
			steps = append(steps, adapter.Step{
				Kind:       "restart",
				ServiceID:  dsvc.ID,
				Disruption: adapter.DisruptRestart,
				Payload:    withReason(payload, reason),
			})
		} else if needsReload {
			steps = append(steps, adapter.Step{
				Kind:       "reload",
				ServiceID:  dsvc.ID,
				Disruption: adapter.DisruptNone,
				Payload:    withReason(payload, reason),
			})
		}
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

// stepPayload is what Apply needs to execute a step without re-deriving it.
//
// Same role as the Xray adapter's type of the same name: Plan renders, Apply
// writes. Apply never sees adapter.Desired, so everything it needs travels
// here.
type stepPayload struct {
	// Config is the fully rendered interface config, marker included.
	Config string `json:"config,omitempty"`
	// Checksum is Config's body checksum, in the domain Observe reports, so
	// the applied sidecar and the marker can be compared directly.
	Checksum string `json:"checksum,omitempty"`
	// Shape is the checksum of this config with no peers, recorded in the
	// sidecar so a later Plan can tell membership from structure.
	Shape string `json:"shape,omitempty"`
	// Peers is the public-key set this config serves, carried through so the
	// sidecar records who the interface is actually serving.
	Peers []string `json:"peers,omitempty"`
	// Reason is why the step was planned. It reaches the operator through the
	// apply-run record and is the only explanation of WHY a session dropped.
	Reason string `json:"reason,omitempty"`
}

// buildPayload renders a service and everything Apply needs to install it.
func (a *Adapter) buildPayload(svc adapter.Service, subjects []adapter.Subject) (json.RawMessage, error) {
	var params ServiceParams
	if err := json.Unmarshal(svc.Params, &params); err != nil {
		return nil, fmt.Errorf("service %d params: %w", svc.ID, err)
	}
	peers := a.buildPeerList(svc, subjects)
	rendered, err := GenerateConfig(svc.ID, params, peers)
	if err != nil {
		return nil, fmt.Errorf("render service %d: %w", svc.ID, err)
	}
	shape, err := shapeChecksum(svc.ID, params)
	if err != nil {
		return nil, fmt.Errorf("shape for service %d: %w", svc.ID, err)
	}
	keys := extractPublicKeys(peers)
	sort.Strings(keys)
	body, err := json.Marshal(stepPayload{
		Config:   rendered,
		Checksum: renderedChecksum(rendered),
		Shape:    shape,
		Peers:    keys,
	})
	if err != nil {
		return nil, fmt.Errorf("encode payload for service %d: %w", svc.ID, err)
	}
	return body, nil
}

// withReason returns the payload with Reason set. Planning decides why; Apply
// only reports it, so it is attached after the fact rather than threaded
// through buildPayload's signature.
func withReason(payload json.RawMessage, reason string) json.RawMessage {
	if reason == "" {
		return payload
	}
	var p stepPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return payload
	}
	p.Reason = reason
	body, err := json.Marshal(p)
	if err != nil {
		return payload
	}
	return body
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
	// renderedChecksum, not checksumConfigBody: `rendered` still carries its
	// marker line, and observed.Checksum is read OUT of that marker, so it
	// covers the body alone. Hashing the whole string produced a value that
	// could never match, and every converged service planned a restart forever.
	want := renderedChecksum(rendered)
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
	if applied.Checksum != "" && a.onlyMembershipChanged(desired.ID, params, applied) {
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
		// The keypair credential holds the subject's PRIVATE key -- the panel
		// stores it sealed, and both the Descriptor and the peer registry say
		// so. The public key has to be derived from it.
		//
		// This used to write cred.Value straight into the peer's PublicKey
		// field, which was wrong twice over. The peer entry then named a key
		// no client possesses, so nobody could connect; and it wrote every
		// subscriber's PRIVATE key into a config file on the node, which is a
		// credential the node has no business holding. Accounting could not
		// match either, because the registry keys on the derived public key.
		//
		// PublicKeyFromPrivate is the pure-Go derivation, already tested
		// against the RFC 7748 vector. It is used rather than `wg pubkey` so
		// planning does not shell out once per peer.
		var privKey string
		for _, cred := range subj.Credentials {
			if cred.Kind == string(adapter.CredKeypair) {
				privKey = cred.Value
				break
			}
		}
		if privKey == "" {
			continue
		}
		pubKey, err := PublicKeyFromPrivate(privKey)
		if err != nil {
			// An unusable key means this subject cannot be served. Skipping
			// leaves the rest of the peer list intact, which is better than
			// failing the whole service over one bad credential.
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

// onlyMembershipChanged reports whether the desired config differs from the
// applied one in its peer list alone.
//
// It compares SHAPES -- each config's checksum rendered with no peers. If the
// interface came up with the same structure it is being asked for now, then
// whatever changed is in the peer list, and `wg syncconf` can apply it without
// dropping the sessions of everyone already connected.
//
// The previous version compared the desired shape against the applied config's
// FULL checksum, which are different things: they matched only when the applied
// config happened to have no peers at all. So the hot path fired once in a
// service's life, on the very first peer, and every subsequent user was added by
// tearing the interface down -- disconnecting every existing user to admit one
// new one. That is the failure the shape field exists to prevent, and it is why
// the sidecar now records it.
//
// An applied state with no recorded shape predates this and is treated as
// unknown, which restarts. Safe direction, and self-correcting: the restart
// records a shape.
func (a *Adapter) onlyMembershipChanged(serviceID int64, params ServiceParams, applied appliedState) bool {
	if applied.Shape == "" {
		return false
	}
	shape, err := shapeChecksum(serviceID, params)
	if err != nil {
		return false
	}
	return shape == applied.Shape
}
