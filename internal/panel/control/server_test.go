package control

import (
	"context"
	"crypto/x509"
	"database/sql"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	"github.com/amyrm/antimage/internal/panel/store"
)

func fakePeerCtx(certDER []byte) context.Context {
	cert, _ := x509.ParseCertificate(certDER)
	return peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{
			State: tlsStateWith(cert),
		},
	})
}

func TestVerifyPeerAcceptsAllowListedFingerprint(t *testing.T) {
	s, nodeID, certDER, fingerprint := enrolledNodeFixture(t)
	if err := setFingerprint(s, nodeID, fingerprint); err != nil {
		t.Fatalf("set fingerprint: %v", err)
	}
	got, err := VerifyPeer(fakePeerCtx(certDER), s)
	if err != nil {
		t.Fatalf("VerifyPeer: %v", err)
	}
	if got != nodeID {
		t.Errorf("node id = %d, want %d", got, nodeID)
	}
}

// Deleting a node must lock it out instantly. This is the allow-list
// revocation model standing in for a CRL.
func TestVerifyPeerRejectsRevokedFingerprint(t *testing.T) {
	s, nodeID, certDER, fingerprint := enrolledNodeFixture(t)
	_ = setFingerprint(s, nodeID, fingerprint)

	if err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM nodes WHERE id = ?`, nodeID)
		return err
	}); err != nil {
		t.Fatalf("delete node: %v", err)
	}

	if _, err := VerifyPeer(fakePeerCtx(certDER), s); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("err = %v, want ErrNotEnrolled — a deleted node must be locked out at once", err)
	}
}

func TestVerifyPeerRejectsUnknownCertificate(t *testing.T) {
	s, _, certDER, _ := enrolledNodeFixture(t)
	// Never recorded in nodes.cert_fingerprint.
	if _, err := VerifyPeer(fakePeerCtx(certDER), s); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("err = %v, want ErrNotEnrolled", err)
	}
}

func TestVerifyPeerRejectsMissingPeer(t *testing.T) {
	s, _, _, _ := enrolledNodeFixture(t)
	if _, err := VerifyPeer(context.Background(), s); err == nil {
		t.Fatal("VerifyPeer accepted a context with no peer")
	}
}

func setFingerprint(s *store.Store, nodeID int64, fp string) error {
	return s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`UPDATE nodes SET cert_fingerprint = ?, status = 'online', enrolled_at = ? WHERE id = ?`,
			fp, time.Now().Unix(), nodeID)
		return err
	})
}
