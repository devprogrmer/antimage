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
//
// RFC 8785 requires every number to be canonicalized as an IEEE-754 double,
// so integers beyond 2^53 (about 9.007e15) silently lose precision — the
// canonical bytes will not round-trip the original integer value. Do not
// put such values (an epoch-nanosecond timestamp, for instance, is around
// 1.7e18) into a document that gets hashed; a value that changes under
// canonicalization will not be detected here and will produce exactly the
// phantom hash mismatch this package exists to prevent.
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
//
// As with Marshal, numbers canonicalize as IEEE-754 doubles per RFC 8785:
// integers beyond 2^53 lose precision silently before hashing. Keep values
// like epoch-nanosecond timestamps or large counters out of hashed
// documents, or the digest will not describe the value you think it does.
func Hash(v any) ([]byte, string, error) {
	b, err := Marshal(v)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(b)
	return b, hex.EncodeToString(sum[:]), nil
}
