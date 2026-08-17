package control

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	"github.com/amyrm/antimage/internal/panel/store"
)

// ErrNotEnrolled means the presented certificate is not on the allow-list.
var ErrNotEnrolled = errors.New("node certificate is not enrolled")

// VerifyPeer authenticates a gRPC caller against nodes.cert_fingerprint.
//
// This is the revocation mechanism from spec section 7.3: the panel is the
// only verifier, so a connection is accepted only when its fingerprint is
// still recorded. Deleting a node locks it out immediately, with no CRL to
// distribute and no OCSP responder to run.
func VerifyPeer(ctx context.Context, s *store.Store) (int64, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return 0, errors.New("no peer information on context")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return 0, errors.New("connection is not mTLS")
	}
	if len(tlsInfo.State.PeerCertificates) == 0 {
		return 0, errors.New("peer presented no certificate")
	}

	sum := sha256.Sum256(tlsInfo.State.PeerCertificates[0].Raw)
	fingerprint := hex.EncodeToString(sum[:])

	var nodeID int64
	err := s.Read().QueryRowContext(ctx,
		`SELECT id FROM nodes WHERE cert_fingerprint = ?`, fingerprint).Scan(&nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotEnrolled
	}
	if err != nil {
		return 0, fmt.Errorf("look up node by fingerprint: %w", err)
	}
	return nodeID, nil
}
