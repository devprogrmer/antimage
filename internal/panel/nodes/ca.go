package nodes

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/shared/secrets"
)

// NodeCertLifetime is one year; agents auto-renew at the halfway mark.
const NodeCertLifetime = 365 * 24 * time.Hour

const caLifetime = 10 * 365 * 24 * time.Hour

// CA is the panel's private certificate authority. The panel is the only
// verifier of node certificates, so revocation is an allow-list check against
// nodes.cert_fingerprint rather than a CRL.
type CA struct {
	cert    *x509.Certificate
	certDER []byte
	key     *ecdsa.PrivateKey
}

func (c *CA) CertDER() []byte { return c.certDER }

// FingerprintSHA256 is pinned into node.yaml at bootstrap so an agent can
// verify the panel even if DNS is hijacked.
func (c *CA) FingerprintSHA256() string {
	sum := sha256.Sum256(c.certDER)
	return hex.EncodeToString(sum[:])
}

// LoadOrCreateCA reads the CA, generating one on first run. The private key
// is sealed under the master key before it touches the database.
func LoadOrCreateCA(ctx context.Context, s *store.Store, box *secrets.Box) (*CA, error) {
	var certDER, sealed []byte
	err := s.Read().QueryRowContext(ctx,
		`SELECT cert_der, key_sealed FROM panel_ca WHERE id = 1`).Scan(&certDER, &sealed)

	switch {
	case err == nil:
		keyDER, err := box.Open(sealed)
		if err != nil {
			return nil, fmt.Errorf("decrypt CA key (wrong master key?): %w", err)
		}
		key, err := x509.ParseECPrivateKey(keyDER)
		if err != nil {
			return nil, fmt.Errorf("parse CA key: %w", err)
		}
		cert, err := x509.ParseCertificate(certDER)
		if err != nil {
			return nil, fmt.Errorf("parse CA cert: %w", err)
		}
		return &CA{cert: cert, certDER: certDER, key: key}, nil

	case errors.Is(err, sql.ErrNoRows):
		return createCA(ctx, s, box)

	default:
		return nil, fmt.Errorf("read CA: %w", err)
	}
}

func createCA(ctx context.Context, s *store.Store, box *secrets.Box) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate CA key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "antimage panel CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(caLifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("self-sign CA: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal CA key: %w", err)
	}
	sealed, err := box.Seal(keyDER)
	if err != nil {
		return nil, fmt.Errorf("seal CA key: %w", err)
	}

	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO panel_ca (id, cert_der, key_sealed, created_at) VALUES (1, ?, ?, ?)`,
			certDER, sealed, now.Unix())
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("persist CA: %w", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("parse new CA cert: %w", err)
	}
	return &CA{cert: cert, certDER: certDER, key: key}, nil
}

// SignNodeCert issues a client certificate whose CN is the node id.
//
// The CSR's subject is deliberately ignored: the panel decides which node an
// enrolling agent is, based on the token it redeemed, so a node cannot name
// itself.
func (c *CA) SignNodeCert(csrDER []byte, nodeID int64, now time.Time) ([]byte, string, error) {
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return nil, "", fmt.Errorf("parse CSR: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, "", fmt.Errorf("CSR signature invalid: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, "", fmt.Errorf("generate serial: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: fmt.Sprintf("%d", nodeID)},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(-5 * time.Minute).Add(NodeCertLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, csr.PublicKey, c.key)
	if err != nil {
		return nil, "", fmt.Errorf("sign node cert: %w", err)
	}
	sum := sha256.Sum256(certDER)
	return certDER, hex.EncodeToString(sum[:]), nil
}
