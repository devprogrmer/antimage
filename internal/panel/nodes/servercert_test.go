package nodes

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// serveTLS starts a TLS listener using the panel's server certificate and
// returns its address. It completes one handshake per connection and closes.
func serveTLS(t *testing.T, cfg *tls.Config) string {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				// Force the handshake, then drop it. The assertions are all
				// about whether the handshake itself succeeds.
				if tc, ok := conn.(*tls.Conn); ok {
					_ = tc.HandshakeContext(context.Background())
				}
				_ = conn.Close()
			}()
		}
	}()
	return ln.Addr().String()
}

func panelServerConfig(t *testing.T, ca *CA, hosts []string) *tls.Config {
	t.Helper()
	cert, err := ca.IssueServerCert(hosts, time.Now().UTC())
	if err != nil {
		t.Fatalf("IssueServerCert: %v", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    ca.ClientCAPool(),
	}
}

// An enrolling agent has no CA file yet. It dials with InsecureSkipVerify and
// a callback that looks for its pinned fingerprint in the presented chain, so
// the CA certificate must be IN that chain — a leaf-only chain would leave the
// agent nothing to match and enrolment would be impossible.
func TestServerCertChainCarriesTheCAForPinning(t *testing.T) {
	ca, _ := newCA(t)
	addr := serveTLS(t, panelServerConfig(t, ca, []string{"127.0.0.1"}))

	pinned := ca.FingerprintSHA256()
	var matched bool
	conn, err := tls.Dial("tcp", addr, &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // the pinning callback below is the check
		MinVersion:         tls.VersionTLS13,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			for _, raw := range rawCerts {
				sum := sha256.Sum256(raw)
				if hex.EncodeToString(sum[:]) == pinned {
					matched = true
				}
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("enrolment-style dial failed: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if !matched {
		t.Fatal("the pinned CA fingerprint was not in the presented chain: " +
			"an enrolling agent would have nothing to verify the panel against")
	}
}

// After enrolment the agent dials normally with the CA in RootCAs, which
// requires a SAN matching the name it dialled.
func TestServerCertVerifiesAgainstTheCAWithMatchingSAN(t *testing.T) {
	ca, _ := newCA(t)
	addr := serveTLS(t, panelServerConfig(t, ca, []string{"127.0.0.1"}))

	conn, err := tls.Dial("tcp", addr, &tls.Config{
		RootCAs:    ca.ClientCAPool(),
		MinVersion: tls.VersionTLS13,
	})
	if err != nil {
		t.Fatalf("steady-state dial failed: %v", err)
	}
	_ = conn.Close()
}

// A certificate issued for the wrong names must be rejected. This is the
// failure an operator hits when --grpc-hosts does not match what agents dial,
// and it has to be a clean verification error rather than a silent success.
func TestServerCertWithWrongSANIsRejected(t *testing.T) {
	ca, _ := newCA(t)
	// Issued for a name nobody will dial.
	addr := serveTLS(t, panelServerConfig(t, ca, []string{"panel.example.invalid"}))

	_, err := tls.Dial("tcp", addr, &tls.Config{
		RootCAs:    ca.ClientCAPool(),
		MinVersion: tls.VersionTLS13,
	})
	if err == nil {
		t.Fatal("a certificate with no SAN for the dialled address was accepted")
	}
	if !strings.Contains(err.Error(), "certificate is valid for") &&
		!strings.Contains(err.Error(), "not 127.0.0.1") {
		t.Logf("rejected, but with an unexpected message: %v", err)
	}
}

// An agent pinning the wrong fingerprint must not be able to verify the panel.
// This is the hijacked-DNS case the pinning exists for.
func TestPinningTheWrongFingerprintFindsNoMatch(t *testing.T) {
	ca, _ := newCA(t)
	addr := serveTLS(t, panelServerConfig(t, ca, []string{"127.0.0.1"}))

	const wrongPin = "0000000000000000000000000000000000000000000000000000000000000000"
	_, err := tls.Dial("tcp", addr, &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // the pinning callback below is the check
		MinVersion:         tls.VersionTLS13,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			for _, raw := range rawCerts {
				sum := sha256.Sum256(raw)
				if hex.EncodeToString(sum[:]) == wrongPin {
					return nil
				}
			}
			return errWrongPin
		},
	})
	if err == nil {
		t.Fatal("a client pinning the wrong CA fingerprint completed the handshake")
	}
}

// Enrolment happens before the node holds any certificate, so the listener
// must accept a connection with no client cert. VerifyPeer enforces the
// requirement per-RPC instead; requiring it here would make enrolment
// impossible and every node would fail at bootstrap.
func TestListenerAcceptsAClientWithNoCertificate(t *testing.T) {
	ca, _ := newCA(t)
	addr := serveTLS(t, panelServerConfig(t, ca, []string{"127.0.0.1"}))

	conn, err := tls.Dial("tcp", addr, &tls.Config{
		RootCAs:    ca.ClientCAPool(),
		MinVersion: tls.VersionTLS13,
		// No Certificates at all: this is an enrolling node.
	})
	if err != nil {
		t.Fatalf("a client with no certificate was refused, which makes enrolment impossible: %v", err)
	}
	_ = conn.Close()
}

// A certificate with no SAN is rejected by every modern TLS client, so an
// empty host list must fail here rather than at every node simultaneously.
func TestIssueServerCertRefusesAnEmptyHostList(t *testing.T) {
	ca, _ := newCA(t)
	if _, err := ca.IssueServerCert(nil, time.Now().UTC()); err == nil {
		t.Fatal("issued a server certificate with no hostnames")
	}
}

func TestIssueServerCertSeparatesIPsFromDNSNames(t *testing.T) {
	ca, _ := newCA(t)
	cert, err := ca.IssueServerCert([]string{"panel.example", "10.0.0.5"}, time.Now().UTC())
	if err != nil {
		t.Fatalf("IssueServerCert: %v", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "panel.example" {
		t.Errorf("DNSNames = %v, want [panel.example]", leaf.DNSNames)
	}
	if len(leaf.IPAddresses) != 1 || !leaf.IPAddresses[0].Equal(net.ParseIP("10.0.0.5")) {
		t.Errorf("IPAddresses = %v, want [10.0.0.5]", leaf.IPAddresses)
	}
	// An IP placed in DNSNames silently fails to match when a client dials by
	// address, which is the default in --grpc-hosts.
	for _, name := range leaf.DNSNames {
		if net.ParseIP(name) != nil {
			t.Errorf("IP %q was put in DNSNames", name)
		}
	}
}

var errWrongPin = errors.New("pinned fingerprint not present in chain")
