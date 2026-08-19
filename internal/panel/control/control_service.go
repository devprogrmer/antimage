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

// Compile-time check that ControlService still satisfies the generated
// interface, so regeneration drift (e.g. a signature change from a fresh
// `buf generate`) fails here at build time instead of surfacing later at
// Task 27's server wiring.
var _ pb.ControlServer = (*ControlService)(nil)

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
		snap, err = nodes.BuildDesiredSnapshot(ctx, tx, callerID, s.deps.snapshotOpts()...)
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

	case *pb.AgentMessage_UsageReport:
		return s.onUsageReport(ctx, nodeID, p.UsageReport)

	default:
		return nil // forward compatible: ignore unknown payloads
	}
}

func (s *ControlService) onHello(ctx context.Context, nodeID int64, h *pb.Hello, srv pb.Control_StreamServer) error {
	adapters := make([]nodes.AdapterInfo, 0, len(h.Adapters))
	for _, a := range h.Adapters {
		adapters = append(adapters, nodes.AdapterInfo{Kind: a.Kind, Version: a.Version})
	}
	if err := nodes.RecordHello(ctx, s.deps.Store, nodeID, adapters,
		h.AppliedRevision, h.DocSha256, s.deps.now()); err != nil {
		return err
	}
	// Tell the agent to reconcile immediately after connecting, so a node
	// that was offline during a change converges without waiting for a timer.
	return srv.Send(&pb.PanelMessage{
		Payload: &pb.PanelMessage_FetchNow{FetchNow: &pb.FetchNow{}},
	})
}

func (s *ControlService) onHeartbeat(ctx context.Context, nodeID int64, hb *pb.Heartbeat) error {
	sample := nodes.HealthSample{
		Load1: hb.Load1, MemUsed: hb.MemUsedBytes, UptimeS: hb.UptimeSeconds,
	}
	for _, a := range hb.AdapterHealth {
		sample.Adapters = append(sample.Adapters, nodes.AdapterHealthSample{
			Kind: a.Kind, OK: a.Ok, Detail: a.Detail,
		})
	}
	return nodes.RecordHeartbeat(ctx, s.deps.Store, nodeID, sample, s.deps.now())
}

func (s *ControlService) onApplyReport(ctx context.Context, nodeID int64, r *pb.ApplyReport) error {
	in := nodes.ApplyRunInput{
		NodeID: nodeID, TargetRevision: r.TargetRevision,
		Converged: r.Converged, Deferred: r.Deferred,
		Err: r.Error, DocSHA256: r.DocSha256, Now: s.deps.now(),
	}
	for _, st := range r.Steps {
		in.Steps = append(in.Steps, nodes.StepOutcome{
			Seq: st.Seq, Kind: st.Kind, Disruption: st.Disruption,
			OK: st.Ok, Err: st.Error, DurationMS: st.DurationMs,
		})
	}
	_, err := nodes.RecordApplyRun(ctx, s.deps.Store, in)
	return err
}

func (s *ControlService) onUsageReport(ctx context.Context, nodeID int64, r *pb.UsageReport) error {
	// SP3 design decision 3: idempotency key is (node_id, sequence).
	// The panel ignores a repeated sequence number.
	samples := make([]nodes.UsageDelta, 0, len(r.Samples))
	for _, sample := range r.Samples {
		samples = append(samples, nodes.UsageDelta{
			SubjectID:     sample.SubjectId,
			UplinkBytes:   sample.UplinkBytes,
			DownlinkBytes: sample.DownlinkBytes,
		})
	}
	return nodes.IngestUsageReport(ctx, s.deps.Store, nodeID, r.Sequence, samples, s.deps.now().Unix())
}
