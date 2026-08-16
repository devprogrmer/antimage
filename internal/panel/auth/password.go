// Package auth implements password hashing, sessions, rate limiting, and TOTP.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Parameters from the spec's global constraints.
const (
	argonMemory  = 64 * 1024 // KiB
	argonTime    = 3
	argonThreads = 4
	argonSaltLen = 16
	argonKeyLen  = 32
)

var b64 = base64.RawStdEncoding

// HashPassword returns a PHC-encoded argon2id hash:
// $argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>
func HashPassword(plain string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	sum := argon2.IDKey([]byte(plain), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(sum)), nil
}

// VerifyPassword compares plain against a PHC-encoded hash in constant time.
// It returns an error only for malformed encodings, never for a mismatch, so
// callers cannot accidentally distinguish the two cases in a response.
func VerifyPassword(encoded, plain string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errors.New("malformed argon2id encoding")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, fmt.Errorf("unsupported argon2 version %q", parts[2])
	}
	var memory uint32
	var time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false, fmt.Errorf("malformed argon2id parameters %q", parts[3])
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("malformed salt: %w", err)
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("malformed hash: %w", err)
	}
	got := argon2.IDKey([]byte(plain), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
