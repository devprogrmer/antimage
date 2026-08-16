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

// LoadOrCreateKey reads the key, generating one at 0600 on first run.
//
// Key creation uses an exclusive create (O_EXCL) rather than an unconditional
// write, so that two processes racing to initialize a fresh install cannot
// silently clobber each other's key: whichever process wins the create keeps
// its generated key, and the loser discards its generated key and reads back
// whatever the winner wrote. Without this, the loser's key would be lost and
// anything already sealed under the winner's key would become permanently
// unreadable with no error at any point.
func LoadOrCreateKey(path string) ([]byte, error) {
	key, err := LoadKey(path)
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
		// both processes agree on one key.
		return LoadKey(path)
	}
	if err != nil {
		return nil, fmt.Errorf("create master key file: %w", err)
	}
	if _, err := f.Write([]byte(encoded)); err != nil {
		f.Close()
		return nil, fmt.Errorf("write master key: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("write master key: %w", err)
	}
	return key, nil
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
