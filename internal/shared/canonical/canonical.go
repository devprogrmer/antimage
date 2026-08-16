// Package canonical produces RFC 8785 (JSON Canonicalization Scheme) bytes
// and their SHA-256 digest.
//
// Every desired-state document hash in antimage flows through here. If two
// logically identical documents ever canonicalize differently, nodes will
// reconcile in a loop and the Integrity check in the spec will fire
// spuriously, so the package is deliberately tiny and heavily property-tested.
package canonical

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/gowebpki/jcs"
)

// Marshal encodes v as JSON and transforms it to RFC 8785 canonical form:
// keys sorted by UTF-16 code unit, no insignificant whitespace, defined
// number formatting.
func Marshal(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	out, err := jcs.Transform(raw)
	if err != nil {
		return nil, fmt.Errorf("canonicalize: %w", err)
	}
	return out, nil
}

// Hash returns the canonical bytes and their lowercase hex SHA-256.
//
// Callers must persist and transmit the returned bytes, never a re-encoding
// of v: spec invariant 4 requires the hash to describe the exact bytes the
// agent receives.
func Hash(v any) ([]byte, string, error) {
	b, err := Marshal(v)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(b)
	return b, hex.EncodeToString(sum[:]), nil
}
