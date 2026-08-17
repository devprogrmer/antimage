package control

import (
	"context"
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
		return nil, status.Errorf(codes.InvalidArgument, "sign CSR: %v", err)
	}

	err = s.deps.Store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE nodes SET cert_fingerprint = ?, status = 'enrolling', enrolled_at = ?
			  WHERE id = ?`, fingerprint, now.Unix(), nodeID); err != nil {
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
		return nil, status.Errorf(codes.Internal, "complete enrollment: %v", err)
	}

	return &pb.EnrollResponse{
		CertDer: certDER,
		CaDer:   s.deps.CA.CertDER(),
		NodeId:  nodeID,
	}, nil
}
