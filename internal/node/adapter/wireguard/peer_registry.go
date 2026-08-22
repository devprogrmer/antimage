package wireguard

import (
	"os/exec"
	"strings"
	"sync"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// peerRegistry maintains the mapping from WireGuard public keys to subject IDs.
//
// This is built during Plan() from Desired state and queried during Usage().
// The registry is protected by a mutex since Plan() and Usage() run concurrently.
type peerRegistry struct {
	mu      sync.RWMutex
	mapping map[string]int64 // publicKey -> subjectID
}

func newPeerRegistry() *peerRegistry {
	return &peerRegistry{
		mapping: make(map[string]int64),
	}
}

// update rebuilds the registry from current desired state.
// Called during Plan() when processing subjects.
func (r *peerRegistry) update(subjects []adapter.Subject) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Clear and rebuild
	r.mapping = make(map[string]int64, len(subjects))

	for _, subj := range subjects {
		for _, cred := range subj.Credentials {
			if cred.Kind == string(adapter.CredKeypair) {
				// cred.Value is the WireGuard private key
				// Derive public key from private key
				publicKey := derivePublicKey(cred.Value)
				if publicKey != "" {
					r.mapping[publicKey] = subj.ID
				}
			}
		}
	}
}

// lookup returns the subject ID for a given public key.
func (r *peerRegistry) lookup(publicKey string) (int64, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	subjectID, ok := r.mapping[publicKey]
	return subjectID, ok
}

// derivePublicKey computes the WireGuard public key from a private key.
//
// WireGuard uses Curve25519 key pairs:
// - Private key: 32 bytes base64-encoded (44 chars)
// - Public key: derived via scalar multiplication
func derivePublicKey(privateKeyB64 string) string {
	// Shell out to `wg pubkey` command (most reliable, uses official WireGuard tooling)
	cmd := exec.Command("wg", "pubkey")
	cmd.Stdin = strings.NewReader(privateKeyB64 + "\n")

	output, err := cmd.Output()
	if err != nil {
		// wg command not available or invalid key format
		return ""
	}

	publicKey := strings.TrimSpace(string(output))
	return publicKey
}

// Add peerRegistry to Adapter struct
func (a *Adapter) initRegistry() {
	// This is called in New() or first Plan()
	if a.registry == nil {
		a.registry = newPeerRegistry()
	}
}

// Update publicKeyToSubject to use registry
func (a *Adapter) publicKeyToSubjectWithRegistry(publicKey string) (int64, bool) {
	if a.registry == nil {
		return 0, false
	}
	return a.registry.lookup(publicKey)
}
