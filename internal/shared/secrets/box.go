// Package secrets holds the panel master key and the AES-256-GCM box used
// for TOTP secrets, recovery codes, and the CA private key.
//
// The key deliberately lives outside the database (spec section 6.1) so that
// a leaked database backup yields no usable secrets.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const (
	// KeySize is 32 bytes for AES-256.
	KeySize = 32
	// EnvVar overrides the key file, for operators injecting secrets at deploy time.
	EnvVar = "ANTIMAGE_MASTER_KEY"
)

// ErrKeyMissing is returned when no key exists and creation was not requested.
// The panel maps this to a refuse-to-start error whenever encrypted rows are
// present, rather than silently generating a fresh key and orphaning them.
var ErrKeyMissing = errors.New("master key not found")

func keyFromEnv() ([]byte, bool, error) {
	raw, ok := os.LookupEnv(EnvVar)
	if !ok || raw == "" {
		return nil, false, nil
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, true, fmt.Errorf("%s is not valid base64: %w", EnvVar, err)
	}
	if len(key) != KeySize {
		return nil, true, fmt.Errorf("%s decodes to %d bytes, want %d", EnvVar, len(key), KeySize)
	}
	return key, true, nil
}

// LoadKey reads an existing key. It never creates one.
func LoadKey(path string) ([]byte, error) {
	if key, ok, err := keyFromEnv(); ok {
		return key, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrKeyMissing
	}
	if err != nil {
		return nil, fmt.Errorf("read master key: %w", err)
	}
	key, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil {
		return nil, fmt.Errorf("master key file is not valid base64: %w", err)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("master key is %d bytes, want %d", len(key), KeySize)
	}
	return key, nil
}

// loserRetryAttempts and loserRetryDelay bound how long a losing process
// waits, after finding the key file already created by a winner, for that
// winner to finish writing the key bytes into it. The window between the
// winner's O_EXCL create and its write landing is sub-millisecond, so 10
// attempts at 5ms apart is generous.
const (
	loserRetryAttempts = 10
	loserRetryDelay    = 5 * time.Millisecond
)

// LoadOrCreateKey reads the key, generating one at 0600 on first run.
//
// Key creation uses an exclusive create (O_EXCL) rather than an unconditional
// write, so that two processes racing to initialize a fresh install cannot
// silently clobber each other's key: whichever process wins the create keeps
// its generated key, and every loser discards its generated key and reads
// back whatever the winner wrote. Without this, a loser's key would be lost
// and anything already sealed under the winner's key would become
// permanently unreadable with no error at any point.
//
// The winner calls f.Sync before closing so its bytes are durable before the
// handle is released, narrowing (but not eliminating, since O_EXCL create
// and the write are still two separate syscalls) the window in which a loser
// can observe the file after creation but before the key is written. Both
// the initial read and the post-O_ErrExist read therefore go through
// loadKeyRetrying rather than a single LoadKey call: a torn read (short
// content or invalid base64) is expected while a winner's write is still in
// flight and is retried, while ErrKeyMissing (no file, or the file vanished)
// is treated as a genuine, non-retryable outcome.
func LoadOrCreateKey(path string) ([]byte, error) {
	key, err := loadKeyRetrying(path)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, ErrKeyMissing) {
		return nil, err
	}

	key = make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create key directory: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		// Another process won the race and created the file first. Discard
		// the key we generated and read back whatever the winner wrote, so
		// both processes agree on one key. The winner's write (and sync) may
		// not have landed yet, so retry rather than reading once.
		return loadKeyRetrying(path)
	}
	if err != nil {
		return nil, fmt.Errorf("create master key file: %w", err)
	}
	if _, err := f.Write([]byte(encoded)); err != nil {
		f.Close()
		return nil, fmt.Errorf("write master key: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return nil, fmt.Errorf("write master key: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("write master key: %w", err)
	}
	return key, nil
}

// loadKeyRetrying reads the key, retrying on a torn read: the file exists
// but does not yet hold a complete, valid key, because some other process's
// O_EXCL create has landed but its write has not. It does not retry
// ErrKeyMissing — a file that does not exist (or existed a moment ago and
// now does not) is a genuine outcome, not a race window, so callers get that
// answer immediately with no added latency in the common no-contention case.
func loadKeyRetrying(path string) ([]byte, error) {
	var lastErr error
	for i := 0; i < loserRetryAttempts; i++ {
		key, err := LoadKey(path)
		if err == nil {
			return key, nil
		}
		if errors.Is(err, ErrKeyMissing) {
			return nil, err
		}
		lastErr = err
		if i < loserRetryAttempts-1 {
			time.Sleep(loserRetryDelay)
		}
	}
	return nil, fmt.Errorf("another process created the master key file but never wrote a valid key to it: %w", lastErr)
}

// Box seals and opens secrets with AES-256-GCM.
type Box struct {
	aead cipher.AEAD
}

// NewBox constructs a Box from a 32-byte AES-256 key.
func NewBox(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("key is %d bytes, want %d", len(key), KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new GCM: %w", err)
	}
	return &Box{aead: aead}, nil
}

// Seal returns nonce||ciphertext with a fresh random nonce per call.
func (b *Box) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	return b.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open reverses Seal, verifying the GCM authentication tag before returning
// plaintext. It rejects any tampered or truncated input.
func (b *Box) Open(sealed []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("ciphertext shorter than nonce")
	}
	plain, err := b.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return plain, nil
}
