package nodes

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/shared/secrets"
)

func newCA(t *testing.T) (*CA, *store.Store) {
	t.Helper()
	s, _ := newNodeFixture(t)
	box, err := secrets.NewBox(bytes.Repeat([]byte{3}, secrets.KeySize))
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}
	ca, err := LoadOrCreateCA(context.Background(), s, box)
	if err != nil {
		t.Fatalf("LoadOrCreateCA: %v", err)
	}
	return ca, s
}

func TestCAIsStableAcrossLoads(t *testing.T) {
	ca, s := newCA(t)
	box, _ := secrets.NewBox(bytes.Repeat([]byte{3}, secrets.KeySize))
	again, err := LoadOrCreateCA(context.Background(), s, box)
	if err != nil {
		t.Fatalf("second LoadOrCreateCA: %v", err)
	}
	if ca.FingerprintSHA256() != again.FingerprintSHA256() {
		t.Fatal("a second load regenerated the CA; every enrolled node would be locked out")
	}
}

func TestCAKeyIsNotStoredInPlaintext(t *testing.T) {
	ca, s := newCA(t)
	var sealed []byte
	if err := s.Read().QueryRow(`SELECT key_sealed FROM panel_ca WHERE id = 1`).Scan(&sealed); err != nil {
		t.Fatalf("read sealed key: %v", err)
	}
	if _, err := x509.ParseECPrivateKey(sealed); err == nil {
		t.Fatal("the CA private key is readable straight from the database")
	}
	if len(ca.CertDER()) == 0 {
		t.Error("CertDER is empty")
	}
}

func TestSignNodeCertProducesUsableClientCert(t *testing.T) {
	ca, _ := newCA(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: "ignored"}}, key)
	if err != nil {
		t.Fatalf("create CSR: %v", err)
	}

	// Unlike the other tests in this package, this one cannot use the fixed
	// historical timestamp (time.Unix(1_700_000_000, 0)): LoadOrCreateCA has
	// no clock parameter and always anchors the CA's NotBefore to real
	// wall-clock time at creation. A leaf signed at a fixed past "now" would
	// have a validity window that ends long before the CA's window begins,
	// so cert.Verify could never find a CurrentTime where both are valid —
	// unsatisfiable by construction, not a fluke of when the test happens to
	// run. Using real time here keeps the leaf's window inside the CA's.
	now := time.Now().UTC()
	certDER, fingerprint, err := ca.SignNodeCert(csrDER, 42, now)
	if err != nil {
		t.Fatalf("SignNodeCert: %v", err)
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatalf("parse issued cert: %v", err)
	}
	if cert.Subject.CommonName != "42" {
		t.Errorf("CN = %q, want the node id 42 — the panel names the node, not the CSR",
			cert.Subject.CommonName)
	}
	if len(fingerprint) != 64 {
		t.Errorf("fingerprint = %q, want 64 hex chars", fingerprint)
	}
	if got := cert.NotAfter.Sub(cert.NotBefore); got != NodeCertLifetime {
		t.Errorf("lifetime = %v, want %v", got, NodeCertLifetime)
	}
	hasClientAuth := false
	for _, u := range cert.ExtKeyUsage {
		if u == x509.ExtKeyUsageClientAuth {
			hasClientAuth = true
		}
	}
	if !hasClientAuth {
		t.Error("issued cert lacks ClientAuth extended key usage")
	}

	// It must chain to the CA.
	pool := x509.NewCertPool()
	caCert, _ := x509.ParseCertificate(ca.CertDER())
	pool.AddCert(caCert)
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots: pool, CurrentTime: now.Add(time.Hour),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Errorf("issued cert does not chain to the CA: %v", err)
	}
}

func TestSignRejectsMalformedCSR(t *testing.T) {
	ca, _ := newCA(t)
	if _, _, err := ca.SignNodeCert([]byte("not a csr"), 1, time.Now()); err == nil {
		t.Fatal("SignNodeCert accepted garbage")
	}
}
