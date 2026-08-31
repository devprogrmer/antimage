package control

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/hex"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/shared/secrets"
	"github.com/amyrm/antimage/internal/testutil/storetest"
)

func tlsStateWith(cert *x509.Certificate) tls.ConnectionState {
	return tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
}

// enrolledNodeFixture returns a store, a node id, a certificate the panel CA
// signed for it, and that certificate's fingerprint.
func enrolledNodeFixture(t *testing.T) (*store.Store, int64, []byte, string) {
	t.Helper()
	s, err := storetest.OpenCopy(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var nodeID int64
	if err := s.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`INSERT INTO nodes (name, address, created_at) VALUES ('n1','1.2.3.4',?)`,
			time.Now().Unix())
		if err != nil {
			return err
		}
		nodeID, err = res.LastInsertId()
		return err
	}); err != nil {
		t.Fatalf("seed node: %v", err)
	}

	box, _ := secrets.NewBox(bytes.Repeat([]byte{9}, secrets.KeySize))
	ca, err := nodes.LoadOrCreateCA(context.Background(), s, box)
	if err != nil {
		t.Fatalf("LoadOrCreateCA: %v", err)
	}

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, _ := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: "x"}}, key)
	certDER, fingerprint, err := ca.SignNodeCert(csrDER, nodeID, time.Now().UTC())
	if err != nil {
		t.Fatalf("SignNodeCert: %v", err)
	}
	return s, nodeID, certDER, fingerprint
}

func depsFor(t *testing.T, s *store.Store, now time.Time) Deps {
	t.Helper()
	box, _ := secrets.NewBox(bytes.Repeat([]byte{9}, secrets.KeySize))
	ca, err := nodes.LoadOrCreateCA(context.Background(), s, box)
	if err != nil {
		t.Fatalf("LoadOrCreateCA: %v", err)
	}
	return Deps{
		Store: s, CA: ca, Hub: NewHub(),
		Now:         func() time.Time { return now },
		DownloadURL: "https://panel.example/agent",
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func itoa64(i int64) string { return strconv.FormatInt(i, 10) }
