package control

import (
	"context"
	"crypto/x509"
	"database/sql"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/store"
	pb "github.com/amyrm/antimage/internal/shared/proto/antimage/v1"
	"github.com/amyrm/antimage/internal/shared/version"
)

// Deps is everything the control plane needs. It is a struct rather than
// positional arguments so adding a dependency does not churn call sites.
type Deps struct {
	Store       *store.Store
	CA          *nodes.CA
	Hub         *Hub
	Now         func() time.Time
	DownloadURL string
	// Box unseals per-subject credentials while a desired document is
	// assembled. A node that has subjects cannot get a snapshot without it,
	// by design: the alternative is serving a document that omits every
	// subject, which would deprovision the node and be recorded as a success.
	Box nodes.Unsealer
}

// snapshotOpts turns the configured Box into BuildDesiredSnapshot options.
// Nil stays nil so a panel with no subjects behaves exactly as it did in SP1.
func (d Deps) snapshotOpts() []nodes.SnapshotOption {
	if d.Box == nil {
		return nil
	}
	return []nodes.SnapshotOption{nodes.WithUnsealer(d.Box)}
}

func (d Deps) now() time.Time {
	if d.Now == nil {
		return time.Now().UTC()
	}
	return d.Now()
}

type EnrollmentService struct {
	deps Deps
}

// Compile-time check that EnrollmentService still satisfies the generated
// interface, so regeneration drift (e.g. a signature change from a fresh
// `buf generate`) fails here at build time instead of surfacing later at
// Task 27's server wiring.
var _ pb.EnrollmentServer = (*EnrollmentService)(nil)

func NewEnrollmentService(d Deps) *EnrollmentService { return &EnrollmentService{deps: d} }

// Enroll redeems a single-use token and issues a client certificate.
//
// The agent's private key never appears here: only its CSR does. The CSR's
// subject is ignored, because the token determines which node this is.
func (s *EnrollmentService) Enroll(ctx context.Context, req *pb.EnrollRequest) (*pb.EnrollResponse, error) {
	if req.ProtocolVersion != version.Protocol {
		return nil, status.Errorf(codes.FailedPrecondition,
			"agent speaks protocol %d, panel speaks %d: upgrade the agent",
			req.ProtocolVersion, version.Protocol)
	}

	now := s.deps.now()
	nodeID, err := nodes.RedeemEnrollToken(ctx, s.deps.Store, req.Token, now)
	if err != nil {
		// Deliberately vague: a caller must not learn whether a token exists.
		audit.BestEffort(ctx, s.deps.Store, "", audit.SystemActor("enrollment"), audit.Record{
			Action: "node.enroll", TargetType: "node", Result: "denied",
		})
		return nil, status.Error(codes.PermissionDenied, "enrollment token invalid")
	}

	certDER, fingerprint, err := s.deps.CA.SignNodeCert(req.CsrDer, nodeID, now)
	if err != nil {
		// The token is already burnt at this point. Without this record, a
		// fumbled CSR and a stolen-token probe are indistinguishable to an
		// operator who only sees the enrollment fail.
		audit.BestEffort(ctx, s.deps.Store, "", audit.SystemActor("enrollment"), audit.Record{
			Action:     "node.enroll",
			TargetType: "node",
			TargetID:   sql.NullInt64{Int64: nodeID, Valid: true},
			After: map[string]any{
				"reason": "csr rejected", "agent_version": req.AgentVersion,
			},
			Result: "failed",
		})
		return nil, status.Errorf(codes.InvalidArgument, "sign CSR: %v", err)
	}

	// Recorded so the panel can warn before a certificate lapses. Parsed from
	// the DER we just signed rather than recomputed from NodeCertLifetime: the
	// serial is generated inside SignNodeCert and the expiry is derived there
	// too, so anything reconstructed here would be a second opinion about a
	// fact the certificate already states. A parse failure is not fatal -- the
	// certificate is valid and the agent needs it; only the operator's warning
	// is lost -- so the columns stay NULL and the API reports them as unknown.
	var notAfter sql.NullInt64
	var serial sql.NullString
	if parsed, err := x509.ParseCertificate(certDER); err == nil {
		notAfter = sql.NullInt64{Int64: parsed.NotAfter.Unix(), Valid: true}
		serial = sql.NullString{String: parsed.SerialNumber.Text(16), Valid: true}
	}

	err = s.deps.Store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE nodes SET cert_fingerprint = ?, status = 'enrolling', enrolled_at = ?,
			        cert_not_after = ?, cert_serial = ?
			  WHERE id = ?`, fingerprint, now.Unix(), notAfter, serial, nodeID); err != nil {
			return fmt.Errorf("record fingerprint: %w", err)
		}
		return audit.InTx(ctx, tx, "", audit.SystemActor("enrollment"), audit.Record{
			Action:     "node.enroll",
			TargetType: "node",
			TargetID:   sql.NullInt64{Int64: nodeID, Valid: true},
			After: map[string]any{
				"fingerprint": fingerprint, "agent_version": req.AgentVersion,
			},
			Result: "ok",
		})
	})
	if err != nil {
		// Same reasoning as above: the token is burnt and the cert was
		// signed, but the fingerprint never made it into the allow-list.
		// BestEffort is called after Store.Write returns, never from inside
		// its callback — the store has a single write connection, and
		// nesting would block until BestEffort's own timeout.
		audit.BestEffort(ctx, s.deps.Store, "", audit.SystemActor("enrollment"), audit.Record{
			Action:     "node.enroll",
			TargetType: "node",
			TargetID:   sql.NullInt64{Int64: nodeID, Valid: true},
			After: map[string]any{
				"reason": "fingerprint not recorded", "agent_version": req.AgentVersion,
			},
			Result: "failed",
		})
		return nil, status.Errorf(codes.Internal, "complete enrollment: %v", err)
	}

	return &pb.EnrollResponse{
		CertDer: certDER,
		CaDer:   s.deps.CA.CertDER(),
		NodeId:  nodeID,
	}, nil
}
