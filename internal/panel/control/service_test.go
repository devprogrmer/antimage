package control

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/nodes"
	pb "github.com/amyrm/antimage/internal/shared/proto/antimage/v1"
	"github.com/amyrm/antimage/internal/shared/version"
)

func TestEnrollIssuesCertAndRecordsFingerprint(t *testing.T) {
	s, nodeID, _, _ := enrolledNodeFixture(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()

	token, err := nodes.IssueEnrollToken(ctx, s, nodeID, audit.SystemActor("test"), "req", now)
	if err != nil {
		t.Fatalf("IssueEnrollToken: %v", err)
	}
	deps := depsFor(t, s, now)
	svc := NewEnrollmentService(deps)

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, _ := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: "self-chosen"}}, key)

	resp, err := svc.Enroll(ctx, &pb.EnrollRequest{
		Token: token, CsrDer: csrDER,
		AgentVersion: "v0.1.0", ProtocolVersion: version.Protocol,
	})
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if resp.NodeId != nodeID {
		t.Errorf("node id = %d, want %d", resp.NodeId, nodeID)
	}
	cert, err := x509.ParseCertificate(resp.CertDer)
	if err != nil {
		t.Fatalf("parse issued cert: %v", err)
	}
	if cert.Subject.CommonName != itoa64(nodeID) {
		t.Errorf("CN = %q, want %d — the panel names the node, not the CSR",
			cert.Subject.CommonName, nodeID)
	}

	var status, fingerprint string
	if err := s.Read().QueryRow(
		`SELECT status, COALESCE(cert_fingerprint,'') FROM nodes WHERE id = ?`, nodeID,
	).Scan(&status, &fingerprint); err != nil {
		t.Fatalf("read node: %v", err)
	}
	if fingerprint == "" {
		t.Error("cert_fingerprint was not recorded; the node could never authenticate")
	}
	if status != "enrolling" && status != "online" {
		t.Errorf("status = %q, want enrolling or online", status)
	}
}

func TestEnrollRejectsReusedToken(t *testing.T) {
	s, nodeID, _, _ := enrolledNodeFixture(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	token, _ := nodes.IssueEnrollToken(ctx, s, nodeID, audit.SystemActor("test"), "req", now)
	svc := NewEnrollmentService(depsFor(t, s, now))

	req := func() *pb.EnrollRequest {
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		csrDER, _ := x509.CreateCertificateRequest(rand.Reader,
			&x509.CertificateRequest{Subject: pkix.Name{CommonName: "x"}}, key)
		return &pb.EnrollRequest{Token: token, CsrDer: csrDER,
			AgentVersion: "v0.1.0", ProtocolVersion: version.Protocol}
	}
	if _, err := svc.Enroll(ctx, req()); err != nil {
		t.Fatalf("first Enroll: %v", err)
	}
	if _, err := svc.Enroll(ctx, req()); err == nil {
		t.Fatal("a burnt token was accepted a second time")
	}
}

func TestEnrollRejectsProtocolSkew(t *testing.T) {
	s, nodeID, _, _ := enrolledNodeFixture(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	token, _ := nodes.IssueEnrollToken(ctx, s, nodeID, audit.SystemActor("test"), "req", now)
	svc := NewEnrollmentService(depsFor(t, s, now))

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, _ := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: "x"}}, key)

	_, err := svc.Enroll(ctx, &pb.EnrollRequest{
		Token: token, CsrDer: csrDER,
		AgentVersion: "v0.0.1", ProtocolVersion: version.Protocol + 99,
	})
	if err == nil {
		t.Fatal("Enroll accepted an incompatible protocol version instead of failing loudly")
	}
}

func TestEnrollAuditsPostBurnFailure(t *testing.T) {
	s, nodeID, _, _ := enrolledNodeFixture(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()

	token, err := nodes.IssueEnrollToken(ctx, s, nodeID, audit.SystemActor("test"), "req", now)
	if err != nil {
		t.Fatalf("IssueEnrollToken: %v", err)
	}
	svc := NewEnrollmentService(depsFor(t, s, now))

	// The token is redeemed before the CSR is parsed, so a malformed CSR
	// burns the token without ever reaching SignNodeCert's success path.
	_, err = svc.Enroll(ctx, &pb.EnrollRequest{
		Token: token, CsrDer: []byte("not a csr"),
		AgentVersion: "v0.1.0", ProtocolVersion: version.Protocol,
	})
	if err == nil {
		t.Fatal("Enroll accepted a malformed CSR")
	}

	// The token must be burnt: a second attempt with the same token, even a
	// well-formed one, must also fail.
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, _ := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: "x"}}, key)
	if _, err := svc.Enroll(ctx, &pb.EnrollRequest{
		Token: token, CsrDer: csrDER,
		AgentVersion: "v0.1.0", ProtocolVersion: version.Protocol,
	}); err == nil {
		t.Fatal("a token burnt by a malformed CSR was accepted a second time")
	}

	// And the failed attempt itself must be on the record, distinct from a
	// silently-vanished token.
	var action, result string
	var targetID int64
	if err := s.Read().QueryRow(
		`SELECT action, result, target_id FROM audit_log
		  WHERE action = 'node.enroll' AND result = 'failed'
		  ORDER BY id DESC LIMIT 1`,
	).Scan(&action, &result, &targetID); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if action != "node.enroll" || result != "failed" || targetID != nodeID {
		t.Errorf("audit = %s/%s/%d, want node.enroll/failed/%d", action, result, targetID, nodeID)
	}
}

func TestGetDesiredSnapshotReturnsMatchingHash(t *testing.T) {
	s, nodeID, certDER, fingerprint := enrolledNodeFixture(t)
	_ = setFingerprint(s, nodeID, fingerprint)
	ctx := fakePeerCtx(certDER)
	now := time.Unix(1_700_000_000, 0).UTC()

	svc := NewControlService(depsFor(t, s, now))
	resp, err := svc.GetDesiredSnapshot(ctx, &pb.SnapshotRequest{NodeId: nodeID})
	if err != nil {
		t.Fatalf("GetDesiredSnapshot: %v", err)
	}
	if len(resp.Document) == 0 || len(resp.Sha256) != 64 {
		t.Fatalf("bad snapshot: %d document bytes, sha %q", len(resp.Document), resp.Sha256)
	}
	// Invariant 4: the agent re-hashes these exact bytes, so they must match.
	if got := sha256Hex(resp.Document); got != resp.Sha256 {
		t.Errorf("document hashes to %s but response claims %s", got, resp.Sha256)
	}
}

func TestGetDesiredSnapshotRefusesOtherNodes(t *testing.T) {
	s, nodeID, certDER, fingerprint := enrolledNodeFixture(t)
	_ = setFingerprint(s, nodeID, fingerprint)
	ctx := fakePeerCtx(certDER)

	svc := NewControlService(depsFor(t, s, time.Unix(1_700_000_000, 0).UTC()))
	if _, err := svc.GetDesiredSnapshot(ctx, &pb.SnapshotRequest{NodeId: nodeID + 500}); err == nil {
		t.Fatal("a node fetched another node's desired state")
	}
}
