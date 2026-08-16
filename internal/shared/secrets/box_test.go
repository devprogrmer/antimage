package secrets

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	box, err := NewBox(bytes.Repeat([]byte{7}, KeySize))
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}
	plain := []byte("JBSWY3DPEHPK3PXP")
	sealed, err := box.Seal(plain)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains(sealed, plain) {
		t.Fatal("ciphertext contains the plaintext")
	}
	got, err := box.Open(sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("round trip = %q, want %q", got, plain)
	}
}

func TestSealUsesFreshNonce(t *testing.T) {
	box, _ := NewBox(bytes.Repeat([]byte{7}, KeySize))
	a, _ := box.Seal([]byte("same"))
	b, _ := box.Seal([]byte("same"))
	if bytes.Equal(a, b) {
		t.Fatal("two seals of identical plaintext produced identical ciphertext — nonce reuse")
	}
}

func TestOpenRejectsTamperedCiphertext(t *testing.T) {
	box, _ := NewBox(bytes.Repeat([]byte{7}, KeySize))
	sealed, _ := box.Seal([]byte("secret"))
	sealed[len(sealed)-1] ^= 0xff
	if _, err := box.Open(sealed); err == nil {
		t.Fatal("Open accepted tampered ciphertext")
	}
}

func TestNewBoxRejectsWrongKeySize(t *testing.T) {
	if _, err := NewBox([]byte("short")); err == nil {
		t.Fatal("NewBox accepted a short key")
	}
}

func TestLoadOrCreateKeyIsStableAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	first, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatalf("LoadOrCreateKey: %v", err)
	}
	if len(first) != KeySize {
		t.Fatalf("key length = %d, want %d", len(first), KeySize)
	}
	second, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatalf("second LoadOrCreateKey: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("second call generated a new key — existing secrets would be orphaned")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("key file mode = %o, want 600", perm)
		}
	}
}

func TestLoadKeyDoesNotCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.key")
	if _, err := LoadKey(path); err == nil {
		t.Fatal("LoadKey created or accepted a missing key file")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("LoadKey must not create the file")
	}
}

func TestEnvOverrideWins(t *testing.T) {
	t.Setenv(EnvVar, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	path := filepath.Join(t.TempDir(), "unused.key")
	key, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatalf("LoadOrCreateKey: %v", err)
	}
	if len(key) != KeySize {
		t.Fatalf("key length = %d, want %d", len(key), KeySize)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("env override must not write a key file")
	}
}
