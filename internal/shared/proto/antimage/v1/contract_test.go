// Package antimagev1_test holds hand-written contract tests for the generated
// wire types. It is an external test package so `buf generate` can never
// overwrite it: generation only emits *.pb.go.
package antimagev1_test

import (
	"context"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/amyrm/antimage/internal/shared/proto/antimage/v1"
)

func TestHelloRoundTrips(t *testing.T) {
	in := &pb.Hello{
		NodeId:          7,
		AgentVersion:    "v0.1.0",
		ProtocolVersion: 1,
		AppliedRevision: 3,
		DocSha256:       "abc",
		Adapters: []*pb.AdapterDescriptor{
			{Kind: "stub", Version: "1", HotUserAdd: true},
		},
	}

	raw, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out pb.Hello
	if err := proto.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if out.NodeId != 7 || out.AppliedRevision != 3 || len(out.Adapters) != 1 {
		t.Fatalf("round trip lost data: %+v", &out)
	}
	if out.Adapters[0].Kind != "stub" || !out.Adapters[0].HotUserAdd {
		t.Errorf("adapter descriptor = %+v", out.Adapters[0])
	}
}

// The agent re-hashes SnapshotResponse.Document before applying it, so the
// field must stay `bytes`. A structured message could re-encode and produce a
// different digest than the panel recorded, which surfaces as a phantom
// integrity fault on every node.
func TestSnapshotResponseCarriesExactBytes(t *testing.T) {
	const exact = `{"node_id":7,"revision":4,"schema_version":1,"services":null,"subjects":null}`

	in := &pb.SnapshotResponse{Revision: 4, Document: []byte(exact), Sha256: "d"}
	raw, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out pb.SnapshotResponse
	if err := proto.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if string(out.Document) != exact {
		t.Errorf("document = %q, want the exact bytes preserved", out.Document)
	}
}

// Both oneof wrappers must accept every variant the control plane sends.
// A missing variant would compile at the call site and fail only at runtime,
// when a node is already enrolled and streaming.
func TestAgentMessageOneofVariants(t *testing.T) {
	cases := map[string]*pb.AgentMessage{
		"hello":        {Payload: &pb.AgentMessage_Hello{Hello: &pb.Hello{NodeId: 1}}},
		"heartbeat":    {Payload: &pb.AgentMessage_Heartbeat{Heartbeat: &pb.Heartbeat{Load1: 0.5}}},
		"apply_report": {Payload: &pb.AgentMessage_ApplyReport{ApplyReport: &pb.ApplyReport{TargetRevision: 2}}},
	}

	for name, msg := range cases {
		raw, err := proto.Marshal(msg)
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		var out pb.AgentMessage
		if err := proto.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s: unmarshal: %v", name, err)
		}
		if out.Payload == nil {
			t.Errorf("%s: payload lost in round trip", name)
		}
	}
}

func TestPanelMessageOneofVariants(t *testing.T) {
	cases := map[string]*pb.PanelMessage{
		"revision_bump": {Payload: &pb.PanelMessage_RevisionBump{RevisionBump: &pb.RevisionBump{Revision: 9}}},
		"fetch_now":     {Payload: &pb.PanelMessage_FetchNow{FetchNow: &pb.FetchNow{}}},
		"upgrade":       {Payload: &pb.PanelMessage_UpgradeRequired{UpgradeRequired: &pb.UpgradeRequired{PanelProtocolVersion: 2}}},
	}

	for name, msg := range cases {
		raw, err := proto.Marshal(msg)
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		var out pb.PanelMessage
		if err := proto.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s: unmarshal: %v", name, err)
		}
		if out.Payload == nil {
			t.Errorf("%s: payload lost in round trip", name)
		}
	}
}

// The services the later tasks register must exist with the names those tasks
// expect. A rename in the .proto would otherwise surface as a compile failure
// several tasks downstream, far from its cause.
func TestGeneratedServiceSurface(t *testing.T) {
	var (
		_ pb.EnrollmentServer = (*enrollStub)(nil)
		_ pb.ControlServer    = (*controlStub)(nil)
	)
	if pb.Enrollment_ServiceDesc.ServiceName != "antimage.v1.Enrollment" {
		t.Errorf("enrollment service name = %q", pb.Enrollment_ServiceDesc.ServiceName)
	}
	if pb.Control_ServiceDesc.ServiceName != "antimage.v1.Control" {
		t.Errorf("control service name = %q", pb.Control_ServiceDesc.ServiceName)
	}
}

type enrollStub struct{}

func (enrollStub) Enroll(_ context.Context, _ *pb.EnrollRequest) (*pb.EnrollResponse, error) {
	return nil, nil
}

type controlStub struct{}

func (controlStub) Stream(_ pb.Control_StreamServer) error { return nil }

func (controlStub) GetDesiredSnapshot(_ context.Context, _ *pb.SnapshotRequest) (*pb.SnapshotResponse, error) {
	return nil, nil
}
