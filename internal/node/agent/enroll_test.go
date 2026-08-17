package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// pinnedCAVerifier is the whole of the agent's protection against connecting
// to something that is not the panel: Enroll dials with InsecureSkipVerify,
// so this callback is the only thing standing between a hijacked DNS record
// and the agent handing over its CSR. Nothing else in the suite exercises it,
// and a version that returns nil unconditionally passes every other test.
func TestPinnedCAVerifierAcceptsOnlyThePinnedCertificate(t *testing.T) {
	panelCert := []byte("panel certificate DER")
	sum := sha256.Sum256(panelCert)
	pinned := hex.EncodeToString(sum[:])

	imposterCert := []byte("imposter certificate DER")
	otherSum := sha256.Sum256(imposterCert)

	for _, tc := range []struct {
		name    string
		pin     string
		chain   [][]byte
		wantErr bool
	}{
		{
			name:  "the pinned certificate alone",
			pin:   pinned,
			chain: [][]byte{panelCert},
		},
		{
			name:  "pinned certificate deeper in the chain",
			chain: [][]byte{imposterCert, panelCert},
			pin:   pinned,
		},
		{
			name:    "a different certificate",
			pin:     pinned,
			chain:   [][]byte{imposterCert},
			wantErr: true,
		},
		{
			name:    "an empty chain",
			pin:     pinned,
			chain:   nil,
			wantErr: true,
		},
		{
			// A config that reached the verifier with no pin must fail
			// closed. LoadConfig rejects this too, but the verifier is the
			// last line and must not depend on that.
			name:    "an empty pin accepts nothing",
			pin:     "",
			chain:   [][]byte{panelCert},
			wantErr: true,
		},
		{
			name:    "a truncated pin is not a prefix match",
			pin:     pinned[:32],
			chain:   [][]byte{panelCert},
			wantErr: true,
		},
		{
			name:    "the imposter's own fingerprint does not unlock the panel's",
			pin:     hex.EncodeToString(otherSum[:]),
			chain:   [][]byte{panelCert},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := pinnedCAVerifier(tc.pin)(tc.chain, nil)
			if tc.wantErr && err == nil {
				t.Fatal("verifier accepted a chain it must reject: the agent would " +
					"enroll against an impostor and send it a CSR")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("verifier rejected the pinned panel: %v", err)
			}
		})
	}
}

// The pin the agent is given must be exactly what the panel publishes, or
// bootstrap produces a node that cannot connect and an error that reads like
// an attack rather than a formatting mistake.
func TestPinnedFingerprintFormatMatchesThePanel(t *testing.T) {
	der := []byte("some certificate DER")
	sum := sha256.Sum256(der)
	got := hex.EncodeToString(sum[:])

	if len(got) != 64 {
		t.Errorf("fingerprint is %d chars, want 64 hex chars", len(got))
	}
	if got != strings.ToLower(got) {
		t.Error("fingerprint must be lowercase hex to match nodes.CA.FingerprintSHA256")
	}
	if strings.Contains(got, ":") {
		t.Error("fingerprint must not be colon-separated; the panel emits bare hex")
	}
}
