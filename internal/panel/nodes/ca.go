package nodes

import (
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
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strconv"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/shared/secrets"
)

// NodeCertLifetime is one year; agents auto-renew at the halfway mark.
const NodeCertLifetime = 365 * 24 * time.Hour

// NodeCommonName is the subject the CA puts in a node certificate.
//
// One definition because two places need to agree on it: SignNodeCert writes
// it, and the certificates API renders it for display. The panel does not keep
// the certificates it signs, so the displayed subject is reconstructed -- and a
// reconstruction that drifts from what was signed is a subject that quietly
// stops matching the certificate on the host.
func NodeCommonName(nodeID int64) string { return strconv.FormatInt(nodeID, 10) }

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

// Certificate exposes the CA's own certificate for display.
//
// Returned rather than copied field by field because the caller wants several
// unrelated details of it (subject, validity, serial) and a getter per field
// would have to grow every time the UI shows one more. Callers must not mutate
// it; x509.Certificate has no copy helper, and the alternative -- re-parsing
// certDER on every read -- costs more than the discipline is worth.
func (c *CA) Certificate() *x509.Certificate { return c.cert }

// CertPEM is the CA certificate in the form an operator can paste elsewhere:
// into a client's trust store, a curl --cacert, another tool's config.
func (c *CA) CertPEM() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.certDER}))
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
		Subject:      pkix.Name{CommonName: NodeCommonName(nodeID)},
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

// ServerCertLifetime is deliberately shorter than a node certificate. The
// panel reissues its own on every start, so a short life costs nothing and
// bounds the damage from a key that leaks off the panel host.
const ServerCertLifetime = 90 * 24 * time.Hour

// IssueServerCert mints a TLS server certificate for the panel's own gRPC
// listener, signed by this CA.
//
// The agent verifies the panel two different ways, and the certificate has to
// satisfy both. During enrolment it has no CA file yet, so it dials with
// InsecureSkipVerify and a VerifyPeerCertificate callback that walks the
// presented chain looking for a certificate whose SHA-256 matches the pinned
// fingerprint — which means the CA certificate itself must be in the chain the
// server sends, not just the leaf. Afterwards it dials normally with the CA in
// RootCAs, which requires the leaf to carry a SAN matching the dial target.
//
// hosts are the DNS names and IPs agents will dial. An empty list is refused:
// a certificate with no SAN is rejected by every modern TLS client, so the
// failure would surface as an opaque handshake error on every node at once
// rather than here.
func (c *CA) IssueServerCert(hosts []string, now time.Time) (tls.Certificate, error) {
	if len(hosts) == 0 {
		return tls.Certificate{}, errors.New("refusing to issue a server certificate with no hostnames")
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate server key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate serial: %w", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "antimage-panel"},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(-5 * time.Minute).Add(ServerCertLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
			continue
		}
		tmpl.DNSNames = append(tmpl.DNSNames, h)
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("sign server cert: %w", err)
	}

	return tls.Certificate{
		// Leaf first, then the CA. The CA certificate is included precisely so
		// an enrolling agent — which has no CA file yet — can find its pinned
		// fingerprint in the presented chain.
		Certificate: [][]byte{certDER, c.certDER},
		PrivateKey:  key,
	}, nil
}

// ClientCAPool returns a pool containing this CA, for verifying node
// certificates presented on the control stream.
func (c *CA) ClientCAPool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(c.cert)
	return pool
}
