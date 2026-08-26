package nodes

import (
	"encoding/base64"
	"encoding/json"
)

// SealOutboundParams encrypts outbound params using AES-256-GCM before storage.
//
// Outbound params carry upstream credentials: WireGuard private_key, socks/http
// passwords, the platform operator's secrets with third parties. They were
// stored as plaintext TEXT, readable by anyone with filesystem access to the
// database.
//
// This function seals them using the same box that protects the CA private key
// and TOTP secrets (internal/panel/nodes/ca.go, internal/panel/auth/totp.go).
//
// The sealed format is base64-encoded nonce||ciphertext, stored as TEXT for
// SQLite compatibility. The base64 wrapper lets the column stay TEXT rather
// than becoming BLOB, avoiding a schema migration that would require sealing
// every existing row in a single atomic operation.
//
// sealer is typically *secrets.Box; the interface keeps outbounds.go free
// of a direct dependency on the secrets package.
type Sealer interface {
	Seal(plaintext []byte) ([]byte, error)
}

func SealOutboundParams(sealer Sealer, params json.RawMessage) (string, error) {
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	sealed, err := sealer.Seal([]byte(params))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// OpenOutboundParams decrypts sealed outbound params.
//
// The input is base64-encoded nonce||ciphertext as written by SealOutboundParams.
// Returns the plaintext JSON params.
//
// For backward compatibility during rollout, if the input is valid JSON (starts
// with '{'), it is treated as plaintext and returned as-is. This lets the panel
// read both sealed and unsealed rows during the transition period, so sealing
// can be deployed without a flag day.
//
// Once all rows are sealed, the plaintext fallback can be removed.
//
// unsealer is typically *secrets.Box; the interface keeps buildOutbounds free
// of a direct dependency on the secrets package.
func OpenOutboundParams(unsealer Unsealer, sealed string) (json.RawMessage, error) {
	// Backward compatibility: if the value is already valid JSON (plaintext),
	// return it as-is. This lets the panel read both sealed and unsealed rows
	// during the rollout period.
	//
	// The heuristic is simple: sealed values are base64 (alphanumeric + / and =),
	// while JSON objects start with '{'. A sealed value will never start with
	// '{' because base64 encoding of random bytes produces a different
	// character set.
	if len(sealed) > 0 && sealed[0] == '{' {
		// Plaintext JSON, not sealed. Return as-is.
		return json.RawMessage(sealed), nil
	}

	ciphertext, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return nil, err
	}
	plaintext, err := unsealer.Open(ciphertext)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(plaintext), nil
}
