package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	pb "github.com/amyrm/antimage/internal/shared/proto/antimage/v1"
	"github.com/amyrm/antimage/internal/shared/version"
)

// pinnedCAVerifier accepts the panel only if its certificate chain contains a
// certificate whose SHA-256 matches the pinned fingerprint.
func pinnedCAVerifier(fingerprint string) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		for _, raw := range rawCerts {
			sum := sha256.Sum256(raw)
			if hex.EncodeToString(sum[:]) == fingerprint {
				return nil
			}
		}
		return fmt.Errorf("panel certificate does not match the pinned CA fingerprint %s", fingerprint)
	}
}

// Enroll generates a keypair locally, sends only the CSR, and stores the
// signed certificate. The private key never leaves this host.
func Enroll(ctx context.Context, cfg *Config) (tls.Certificate, []byte, int64, error) {
	var zero tls.Certificate

	target, err := cfg.GRPCTarget()
	if err != nil {
		return zero, nil, 0, err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return zero, nil, 0, fmt.Errorf("generate node key: %w", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: "antimage-node"}}, key)
	if err != nil {
		return zero, nil, 0, fmt.Errorf("create CSR: %w", err)
	}

	creds := credentials.NewTLS(&tls.Config{
		InsecureSkipVerify:    true, //nolint:gosec // replaced by the pinned verifier below
		VerifyPeerCertificate: pinnedCAVerifier(cfg.CAFingerprint),
		MinVersion:            tls.VersionTLS13,
	})
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(creds))
	if err != nil {
		return zero, nil, 0, fmt.Errorf("dial panel: %w", err)
	}
	defer func() { _ = conn.Close() }()

	resp, err := pb.NewEnrollmentClient(conn).Enroll(ctx, &pb.EnrollRequest{
		Token:           cfg.Token,
		CsrDer:          csrDER,
		AgentVersion:    version.Version,
		ProtocolVersion: version.Protocol,
	})
	if err != nil {
		return zero, nil, 0, fmt.Errorf("enroll: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return zero, nil, 0, fmt.Errorf("marshal node key: %w", err)
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return zero, nil, 0, fmt.Errorf("create state dir: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: resp.CertDer})
	if err := os.WriteFile(filepath.Join(cfg.StateDir, "node.key"), keyPEM, 0o600); err != nil {
		return zero, nil, 0, fmt.Errorf("write node key: %w", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.StateDir, "node.crt"), certPEM, 0o600); err != nil {
		return zero, nil, 0, fmt.Errorf("write node cert: %w", err)
	}

	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return zero, nil, 0, fmt.Errorf("load issued keypair: %w", err)
	}
	return pair, resp.CaDer, resp.NodeId, nil
}
