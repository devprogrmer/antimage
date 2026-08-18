package nodes

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// newHostKey returns a signer plus its SHA256 fingerprint.
func newHostKey(t *testing.T) (ssh.Signer, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return signer, ssh.FingerprintSHA256(signer.PublicKey())
}

// clientKeyPEM returns an unencrypted ed25519 private key in PEM form.
func clientKeyPEM(t *testing.T) []byte {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen client key: %v", err)
	}
	der, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return pem.EncodeToMemory(der)
}

// startSSHServer accepts one connection and completes the handshake.
func startSSHServer(t *testing.T, hostKey ssh.Signer) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(hostKey)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					_ = conn.Close()
					return
				}
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					_ = nc.Reject(ssh.Prohibited, "probe")
				}
				_ = sc.Close()
			}()
		}
	}()
	return ln.Addr().String()
}

func TestHostKeyPromptReturnsTheServersFingerprint(t *testing.T) {
	hostKey, wantFP := newHostKey(t)
	addr := startSSHServer(t, hostKey)
	host, portStr, _ := net.SplitHostPort(addr)
	port := 0
	_, _ = fmt.Sscanf(portStr, "%d", &port)

	creds := SSHCredentials{
		Host: host, Port: port, User: "root",
		PrivateKeyPEM: clientKeyPEM(t),
	}
	got, err := HostKeyPrompt(context.Background(), creds)
	t.Logf("HostKeyPrompt -> fp=%q err=%v", got, err)
	if err != nil {
		t.Fatalf("HostKeyPrompt failed: %v -- the sentinel-error flow does NOT work", err)
	}
	if got != wantFP {
		t.Fatalf("fingerprint = %q, want %q", got, wantFP)
	}
}

func TestBootstrapRefusesAMismatchedHostKey(t *testing.T) {
	hostKey, realFP := newHostKey(t)
	addr := startSSHServer(t, hostKey)
	host, portStr, _ := net.SplitHostPort(addr)
	port := 0
	_, _ = fmt.Sscanf(portStr, "%d", &port)

	creds := SSHCredentials{
		Host: host, Port: port, User: "root",
		PrivateKeyPEM: clientKeyPEM(t),
	}
	_, err := BootstrapOverSSH(context.Background(), creds,
		"SHA256:definitelyNotTheRealKey", "echo hi")
	if err == nil {
		t.Fatal("connected despite a mismatched host key")
	}
	if !errors.Is(err, ErrHostKeyMismatch) {
		t.Fatalf("err = %v, want ErrHostKeyMismatch (real fp was %s)", err, realFP)
	}
	t.Logf("correctly rejected: %v", err)
}

// --- the invariant: credentials are never persisted, and key bytes are wiped ---

// Zero must overwrite the backing array, not merely drop the slice header.
// The test keeps its own reference to the original array, so an
// implementation that only nils the fields leaves the key readable here —
// and, in production, readable to anything that can read process memory.
func TestZeroWipesKeyMaterial(t *testing.T) {
	key := []byte("-----BEGIN OPENSSH PRIVATE KEY-----")
	pass := []byte("hunter2")
	creds := SSHCredentials{
		Host: "1.2.3.4", Port: 22, User: "root",
		PrivateKeyPEM: key, Passphrase: pass,
	}
	creds.Zero()

	if !bytes.Equal(key, make([]byte, len(key))) {
		t.Errorf("private key bytes survived Zero(): %q", key)
	}
	if !bytes.Equal(pass, make([]byte, len(pass))) {
		t.Errorf("passphrase bytes survived Zero(): %q", pass)
	}
	if creds.User != "" || creds.Host != "" || creds.Port != 0 {
		t.Error("Zero() left identifying fields populated")
	}
}

// The credentials type must not be serializable, so it cannot accidentally
// reach a database column, an audit payload, or a log line.
func TestCredentialsCannotBeMarshalled(t *testing.T) {
	src, err := os.ReadFile("bootstrap_ssh.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	for _, banned := range []string{`json:"`, `db:"`, `yaml:"`} {
		if strings.Contains(string(src), banned) {
			t.Errorf("bootstrap_ssh.go contains a %s struct tag; credentials must never serialize", banned)
		}
	}
}

// No migration may add a column that stores SSH secrets. This is the guard on
// the invariant itself: the type having no tags is worthless if a table
// appears to hold the same data.
func TestNoMigrationStoresSSHCredentials(t *testing.T) {
	dir := "../store/migrations"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	checked := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		checked++
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		lower := strings.ToLower(string(body))
		for _, banned := range []string{"ssh_key", "ssh_password", "private_key", "passphrase"} {
			if strings.Contains(lower, banned) {
				t.Errorf("%s declares %q; SSH credentials must never be persisted", e.Name(), banned)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no migrations were scanned; the guard is not actually running")
	}
}

// The panel must never accept an unverified host key. scripts/check-imports.sh
// greps the whole repo for this too; both layers are cheap.
func TestSourceNeverIgnoresHostKeys(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "bootstrap_ssh.go", nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ast.Inspect(file, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "InsecureIgnoreHostKey" {
			t.Error("InsecureIgnoreHostKey is referenced")
		}
		return true
	})
}

func TestVerifyHostKeyComparesExactly(t *testing.T) {
	err := verifyHostKey("SHA256:expected", "SHA256:actual")
	if err == nil {
		t.Fatal("mismatched host key accepted")
	}
	if !errors.Is(err, ErrHostKeyMismatch) {
		t.Errorf("err = %v, want ErrHostKeyMismatch", err)
	}
	if !strings.Contains(err.Error(), "SHA256:expected") {
		t.Errorf("error should name the expected fingerprint: %v", err)
	}
	if err := verifyHostKey("SHA256:same", "SHA256:same"); err != nil {
		t.Errorf("matching host key rejected: %v", err)
	}
	// A prefix must not pass: fingerprints are compared whole.
	if err := verifyHostKey("SHA256:abc", "SHA256:abcdef"); err == nil {
		t.Error("a prefix of the pinned fingerprint was accepted")
	}
}

// An empty pin must refuse outright rather than connecting to anything.
func TestBootstrapRefusesAnEmptyPin(t *testing.T) {
	_, err := BootstrapOverSSH(context.Background(),
		SSHCredentials{Host: "127.0.0.1", Port: 1, User: "root"}, "", "echo hi")
	if err == nil {
		t.Fatal("connected with no pinned host key")
	}
	if !strings.Contains(err.Error(), "pinned host key") {
		t.Errorf("err = %v, want it to name the missing pin", err)
	}
}
