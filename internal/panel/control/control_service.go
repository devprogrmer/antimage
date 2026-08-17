package control

import (
	"context"
	"database/sql"
	"errors"
	"io"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/amyrm/antimage/internal/panel/nodes"
	pb "github.com/amyrm/antimage/internal/shared/proto/antimage/v1"
	"github.com/amyrm/antimage/internal/shared/version"
)

type ControlService struct {
	deps Deps
}

func NewControlService(d Deps) *ControlService { return &ControlService{deps: d} }

// GetDesiredSnapshot returns the exact canonical bytes that were hashed.
func (s *ControlService) GetDesiredSnapshot(
	ctx context.Context, req *pb.SnapshotRequest,
) (*pb.SnapshotResponse, error) {
	callerID, err := VerifyPeer(ctx, s.deps.Store)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "not enrolled")
	}
	// A node may fetch only its own state.
	if req.NodeId != callerID {
		return nil, status.Error(codes.PermissionDenied, "node id mismatch")
	}

	var snap *nodes.Snapshot
	err = s.deps.Store.Write(ctx, func(tx *sql.Tx) error {
		var err error
		snap, err = nodes.BuildDesiredSnapshot(ctx, tx, callerID)
		return err
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "build snapshot: %v", err)
	}

	return &pb.SnapshotResponse{
		Revision: snap.Revision,
		Document: snap.Bytes,
		Sha256:   snap.SHA256,
	}, nil
}

// Stream holds the agent's long-lived connection. The agent dials in; the
// panel never dials the node.
func (s *ControlService) Stream(srv pb.Control_StreamServer) error {
	ctx := srv.Context()
	nodeID, err := VerifyPeer(ctx, s.deps.Store)
	if err != nil {
		return status.Error(codes.Unauthenticated, "not enrolled")
	}

	bumps, release := s.deps.Hub.Register(nodeID)
	defer release()

	// Receive loop feeds messages to the select below.
	type recvResult struct {
		msg *pb.AgentMessage
		err error
	}
	incoming := make(chan recvResult)
	go func() {
		defer close(incoming)
		for {
			msg, err := srv.Recv()
			select {
			case incoming <- recvResult{msg: msg, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case revision, ok := <-bumps:
			if !ok {
				// Superseded by a newer stream for this node.
				return status.Error(codes.Aborted, "stream superseded")
			}
			if err := srv.Send(&pb.PanelMessage{
				Payload: &pb.PanelMessage_RevisionBump{
					RevisionBump: &pb.RevisionBump{Revision: revision},
				},
			}); err != nil {
				return err
			}

		case in, ok := <-incoming:
			if !ok {
				return nil
			}
			if errors.Is(in.err, io.EOF) {
				return nil
			}
			if in.err != nil {
				return in.err
			}
			if err := s.handle(ctx, nodeID, in.msg, srv); err != nil {
				return err
			}
		}
	}
}

func (s *ControlService) handle(
	ctx context.Context, nodeID int64, msg *pb.AgentMessage, srv pb.Control_StreamServer,
) error {
	switch p := msg.Payload.(type) {
	case *pb.AgentMessage_Hello:
		if p.Hello.ProtocolVersion != version.Protocol {
			// Surface skew as an actionable state rather than misbehaving.
			return srv.Send(&pb.PanelMessage{
				Payload: &pb.PanelMessage_UpgradeRequired{
					UpgradeRequired: &pb.UpgradeRequired{
						PanelProtocolVersion: version.Protocol,
						DownloadUrl:          s.deps.DownloadURL,
					},
				},
			})
		}
		return s.onHello(ctx, nodeID, p.Hello, srv)

	case *pb.AgentMessage_Heartbeat:
		return s.onHeartbeat(ctx, nodeID, p.Heartbeat)

	case *pb.AgentMessage_ApplyReport:
		return s.onApplyReport(ctx, nodeID, p.ApplyReport)

	default:
		return nil // forward compatible: ignore unknown payloads
	}
}

// Implemented in Task 21.
func (s *ControlService) onHello(ctx context.Context, nodeID int64, h *pb.Hello, srv pb.Control_StreamServer) error {
	return nil
}

// Implemented in Task 21.
func (s *ControlService) onHeartbeat(ctx context.Context, nodeID int64, hb *pb.Heartbeat) error {
	return nil
}

// Implemented in Task 21.
func (s *ControlService) onApplyReport(ctx context.Context, nodeID int64, r *pb.ApplyReport) error {
	return nil
}
