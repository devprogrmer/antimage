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

// TestLoadOrCreateCARefusesAWrongMasterKey guards against a regression with
// the same failure shape secrets.LoadKey vs. LoadOrCreateKey exists to
// prevent: if a future refactor collapsed "no CA row yet" and "CA row
// exists but its key won't decrypt" into one case, the panel would mint a
// fresh CA whenever it was pointed at the wrong master key (e.g. a restored
// database backup paired with the wrong master.key file). Every enrolled
// node's certificate chains to the old CA, so all of them would be locked
// out permanently while the panel looked healthy. Assertions 3 and 4 below
// are what actually catch that: an implementation that errored only after
// overwriting the row would still pass 1 and 2.
func TestLoadOrCreateCARefusesAWrongMasterKey(t *testing.T) {
	ca, s := newCA(t)
	wantCertDER := ca.CertDER()

	var wantRowCount int
	if err := s.Read().QueryRow(`SELECT count(*) FROM panel_ca`).Scan(&wantRowCount); err != nil {
		t.Fatalf("count panel_ca rows: %v", err)
	}
	if wantRowCount != 1 {
		t.Fatalf("panel_ca has %d rows before the wrong-key load, want 1", wantRowCount)
	}

	wrongBox, err := secrets.NewBox(bytes.Repeat([]byte{7}, secrets.KeySize))
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}

	got, err := LoadOrCreateCA(context.Background(), s, wrongBox)

	// 1. A non-nil error.
	if err == nil {
		t.Error("LoadOrCreateCA with the wrong master key returned a nil error")
	}
	// 2. A nil *CA, so a caller cannot use a half-built value.
	if got != nil {
		t.Error("LoadOrCreateCA with the wrong master key returned a non-nil *CA")
	}

	// 3. panel_ca still holds exactly one row — no fresh CA was minted.
	var gotRowCount int
	if err := s.Read().QueryRow(`SELECT count(*) FROM panel_ca`).Scan(&gotRowCount); err != nil {
		t.Fatalf("count panel_ca rows after wrong-key load: %v", err)
	}
	if gotRowCount != 1 {
		t.Errorf("panel_ca has %d rows after a failed load, want 1 — "+
			"a wrong master key must never mint a replacement CA", gotRowCount)
	}

	// 4. The stored cert_der is byte-for-byte the original — read directly
	// from the database, not re-derived through another LoadOrCreateCA call,
	// so this observes the stored state rather than the loader's own view.
	var gotCertDER []byte
	if err := s.Read().QueryRow(`SELECT cert_der FROM panel_ca WHERE id = 1`).Scan(&gotCertDER); err != nil {
		t.Fatalf("read cert_der after wrong-key load: %v", err)
	}
	if !bytes.Equal(gotCertDER, wantCertDER) {
		t.Error("panel_ca.cert_der changed after a failed load with the wrong master key — " +
			"every enrolled node's certificate would now chain to a CA that no longer exists")
	}
}

func TestSignRejectsMalformedCSR(t *testing.T) {
	ca, _ := newCA(t)
	if _, _, err := ca.SignNodeCert([]byte("not a csr"), 1, time.Now()); err == nil {
		t.Fatal("SignNodeCert accepted garbage")
	}
}
