package agent

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/amyrm/antimage/internal/node/adapter"
	"github.com/amyrm/antimage/internal/node/adapter/stub"
	pb "github.com/amyrm/antimage/internal/shared/proto/antimage/v1"
)

// The agent re-hashes the snapshot before applying it, as cheap insurance
// against a truncated or buggy response.
func TestVerifySnapshotAcceptsMatchingHash(t *testing.T) {
	doc := []byte(`{"schema_version":1}`)
	sum := sha256.Sum256(doc)
	if err := verifySnapshot(doc, hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("verifySnapshot rejected a valid snapshot: %v", err)
	}
}

func TestVerifySnapshotRejectsMismatch(t *testing.T) {
	doc := []byte(`{"schema_version":1}`)
	err := verifySnapshot(doc, "0000000000000000000000000000000000000000000000000000000000000000")
	if !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("err = %v, want ErrHashMismatch", err)
	}
}

func TestVerifySnapshotRejectsTruncatedDocument(t *testing.T) {
	doc := []byte(`{"schema_version":1,"services":[]}`)
	sum := sha256.Sum256(doc)
	if err := verifySnapshot(doc[:10], hex.EncodeToString(sum[:])); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("truncated document accepted: %v", err)
	}
}

func TestBackoffGrowsAndCaps(t *testing.T) {
	var last time.Duration
	for attempt := 1; attempt <= 20; attempt++ {
		d := reconnectBackoff(attempt)
		if d > MaxReconnectBackoff {
			t.Fatalf("attempt %d backoff %v exceeds cap %v", attempt, d, MaxReconnectBackoff)
		}
		if d < last {
			t.Fatalf("backoff shrank from %v to %v", last, d)
		}
		last = d
	}
	if last != MaxReconnectBackoff {
		t.Errorf("final backoff = %v, want the %v cap", last, MaxReconnectBackoff)
	}
}

// A reconcile must report WHAT it did and what that cost, not just whether it
// worked. Kind and Disruption originate in the adapter's Plan, pass through
// the reconciler's StepResult, and land in the panel's node_apply_steps rows;
// dropping either anywhere on that path leaves an operator staring at
// "step 1 failed" with no way to tell a hot user add from a service restart.
// The disruption string must also be one the panel's CHECK constraint admits.
func TestApplyReportPreservesStepKindAndDisruption(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		PanelURL: "panel.example:8443", CAFingerprint: "dead",
		StateDir: dir, NodeID: 7,
	}
	c := NewClient(cfg, MustRegistry(stub.New(filepath.Join(dir, "services"))), SystemClock{}, tls.Certificate{}, nil)

	doc, err := json.Marshal(adapter.Desired{
		SchemaVersion: 1, Revision: 3, NodeID: 7,
		Services: []adapter.Service{{
			ID: 10, Kind: "stub", Enabled: true, Params: json.RawMessage(`{"port":443}`),
		}},
	})
	if err != nil {
		t.Fatalf("marshal desired: %v", err)
	}
	sum := sha256.Sum256(doc)

	client := &fakeControlClient{snapshot: &pb.SnapshotResponse{
		Revision: 3, Document: doc, Sha256: hex.EncodeToString(sum[:]),
	}}
	stream := &recordingStream{}

	if err := c.reconcileOnce(context.Background(), client, stream); err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}

	report := stream.lastApplyReport()
	if report == nil {
		t.Fatal("no ApplyReport was sent")
	}
	if len(report.Steps) != 1 {
		t.Fatalf("report carries %d steps, want 1", len(report.Steps))
	}
	step := report.Steps[0]

	// stub.Plan emits this exact step for a service it has never written.
	if step.Kind != "write_service" {
		t.Errorf("StepResult.Kind = %q, want %q — the step's identity was dropped between Plan and the wire",
			step.Kind, "write_service")
	}
	if step.Disruption != "restart" {
		t.Errorf("StepResult.Disruption = %q, want %q — the step's cost was dropped between Plan and the wire",
			step.Disruption, "restart")
	}
	// node_apply_steps.disruption is CHECKed against exactly these.
	switch step.Disruption {
	case "none", "reload", "restart":
	default:
		t.Errorf("StepResult.Disruption = %q, which node_apply_steps.disruption would reject", step.Disruption)
	}
	if !step.Ok {
		t.Errorf("step reported not ok: %s", step.Error)
	}
}

// handleCommand's result must always carry the command's own id back,
// regardless of outcome -- it is what lets the panel's Hub correlate the
// reply to whichever HTTP request is waiting, and a reply with the wrong id
// wakes the wrong caller (or none at all). The stub adapter's Restart
// returns ErrRestartUnsupported (it manages plain files, no daemon), which
// this also uses to prove a failing adapter's reason reaches the wire result
// rather than being swallowed.
func TestHandleCommand_RestartAdapters_EchoesCommandID(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{PanelURL: "panel.example:8443", CAFingerprint: "dead", StateDir: dir, NodeID: 7}
	c := NewClient(cfg, MustRegistry(stub.New(filepath.Join(dir, "services"))), SystemClock{}, tls.Certificate{}, nil)

	result := c.handleCommand(context.Background(), &pb.AgentCommand{
		CommandId: "cmd-42",
		Body:      &pb.AgentCommand_RestartAdapters{RestartAdapters: &pb.RestartAdapters{}},
	})

	if result.CommandId != "cmd-42" {
		t.Errorf("result command id = %q, want cmd-42", result.CommandId)
	}
	restart, ok := result.Body.(*pb.AgentCommandResult_RestartAdapters)
	if !ok {
		t.Fatalf("result body = %T, want *AgentCommandResult_RestartAdapters", result.Body)
	}
	if len(restart.RestartAdapters.Outcomes) != 1 {
		t.Fatalf("got %d outcomes, want 1 (the stub adapter)", len(restart.RestartAdapters.Outcomes))
	}
	outcome := restart.RestartAdapters.Outcomes[0]
	if outcome.Kind != "stub" {
		t.Errorf("outcome kind = %q, want stub", outcome.Kind)
	}
	if outcome.Ok {
		t.Error("stub adapter reported Ok=true; it has no daemon to restart")
	}
	if outcome.Error == "" {
		t.Error("failing outcome carries no error text")
	}
}

// The stub adapter has no geo-data concept (it implements Restart, not
// GeoDataUpdater), so this proves the dispatch path threads an
// UpdateGeoData command through to the registry and back onto the wire
// correctly even when the answer is "nothing on this node has geo data" --
// an empty outcomes list, not an error and not a crash.
func TestHandleCommand_UpdateGeoData_NoCapableAdapterIsEmptyNotError(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{PanelURL: "panel.example:8443", CAFingerprint: "dead", StateDir: dir, NodeID: 7}
	c := NewClient(cfg, MustRegistry(stub.New(filepath.Join(dir, "services"))), SystemClock{}, tls.Certificate{}, nil)

	result := c.handleCommand(context.Background(), &pb.AgentCommand{
		CommandId: "cmd-geo-1",
		Body: &pb.AgentCommand_UpdateGeoData{UpdateGeoData: &pb.UpdateGeoData{
			GeoipUrl: "https://example.invalid/geoip.dat", GeoipSha256Url: "https://example.invalid/geoip.dat.sha256sum",
			GeositeUrl: "https://example.invalid/geosite.dat", GeositeSha256Url: "https://example.invalid/geosite.dat.sha256sum",
		}},
	})

	if result.CommandId != "cmd-geo-1" {
		t.Errorf("result command id = %q, want cmd-geo-1", result.CommandId)
	}
	geo, ok := result.Body.(*pb.AgentCommandResult_UpdateGeoData)
	if !ok {
		t.Fatalf("result body = %T, want *AgentCommandResult_UpdateGeoData", result.Body)
	}
	if len(geo.UpdateGeoData.Outcomes) != 0 {
		t.Errorf("got %d outcomes for a registry with no geo-capable adapter, want 0", len(geo.UpdateGeoData.Outcomes))
	}
}

// An unrecognised command body must not crash the agent or drop the
// command silently -- it echoes the id with no body set, which the panel
// side treats as a failure (see the SendCommand contract) rather than as
// success with nothing to report.
func TestHandleCommand_UnknownBody_StillEchoesCommandID(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{PanelURL: "panel.example:8443", CAFingerprint: "dead", StateDir: dir, NodeID: 7}
	c := NewClient(cfg, MustRegistry(stub.New(filepath.Join(dir, "services"))), SystemClock{}, tls.Certificate{}, nil)

	result := c.handleCommand(context.Background(), &pb.AgentCommand{CommandId: "cmd-99"})
	if result.CommandId != "cmd-99" {
		t.Errorf("result command id = %q, want cmd-99", result.CommandId)
	}
	if result.Body != nil {
		t.Errorf("body = %v, want nil for an unrecognised command", result.Body)
	}
}

// The receive goroutine parks on the incoming channel whenever the session
// loop is not reading it, and the loop stops reading the moment it returns.
// A session that ends for any reason other than cancellation or a recv error
// — here, a failed heartbeat send — must still release that goroutine, or the
// agent leaks one goroutine and one retained stream per reconnect.
func TestSessionReleasesReceiveGoroutineOnSendFailure(t *testing.T) {
	cfg := &Config{
		PanelURL: "panel.example:8443", CAFingerprint: "dead",
		StateDir: t.TempDir(), NodeID: 1,
	}
	clk := NewFakeClock(time.Unix(1_700_000_000, 0).UTC())
	c := NewClient(cfg, MustRegistry(stub.New(t.TempDir())), clk, tls.Certificate{}, nil)

	before := runtime.NumGoroutine()

	// Hello succeeds; every later send (the heartbeat) fails, which is the
	// exit path that leaks.
	stream := &chattyStream{okSends: 1}
	client := &fakeControlClient{} // GetDesiredSnapshot always errors

	done := make(chan error, 1)
	go func() {
		done <- c.runSession(context.Background(), client, func(ctx context.Context) (pb.Control_StreamClient, error) {
			stream.bind(ctx)
			return stream, nil
		})
	}()

	// The heartbeat timer is registered inside runSession, so keep nudging
	// the clock until it exists and fires.
	var err error
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case err = <-done:
		default:
		}
		if err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("runSession never returned: its receive goroutine is parked on the incoming " +
				"channel with nothing to release it, so the session (and its stream) leak")
		}
		clk.Advance(HeartbeatInterval)
		time.Sleep(time.Millisecond)
	}

	if err == nil || !strings.Contains(err.Error(), "simulated send failure") {
		t.Fatalf("runSession err = %v, want the heartbeat send failure", err)
	}

	// Belt and braces: the goroutine count must settle back, which catches a
	// session that returns promptly while still stranding its goroutine.
	settleDeadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(settleDeadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("goroutines %d -> %d: the session left one behind", before, after)
	}
}

// --- test doubles ---

type fakeControlClient struct {
	snapshot *pb.SnapshotResponse
}

var _ pb.ControlClient = (*fakeControlClient)(nil)

func (f *fakeControlClient) Stream(context.Context, ...grpc.CallOption) (pb.Control_StreamClient, error) {
	return nil, errors.New("fakeControlClient does not open streams")
}

func (f *fakeControlClient) GetDesiredSnapshot(
	context.Context, *pb.SnapshotRequest, ...grpc.CallOption,
) (*pb.SnapshotResponse, error) {
	if f.snapshot == nil {
		return nil, errors.New("no snapshot configured")
	}
	return f.snapshot, nil
}

// fakeStream supplies the ClientStream methods the generated bidi interface
// requires but the agent never calls.
type fakeStream struct{}

func (fakeStream) Header() (metadata.MD, error) { return nil, nil }
func (fakeStream) Trailer() metadata.MD         { return nil }
func (fakeStream) CloseSend() error             { return nil }
func (fakeStream) Context() context.Context     { return context.Background() }
func (fakeStream) SendMsg(any) error            { return nil }
func (fakeStream) RecvMsg(any) error            { return nil }

// recordingStream captures what the agent sends and never receives anything.
type recordingStream struct {
	fakeStream
	mu   sync.Mutex
	sent []*pb.AgentMessage
}

var _ pb.Control_StreamClient = (*recordingStream)(nil)

func (s *recordingStream) Send(m *pb.AgentMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, m)
	return nil
}

func (s *recordingStream) Recv() (*pb.PanelMessage, error) {
	select {} // never returns; reconcileOnce does not receive
}

func (s *recordingStream) lastApplyReport() *pb.ApplyReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.sent) - 1; i >= 0; i-- {
		if p, ok := s.sent[i].Payload.(*pb.AgentMessage_ApplyReport); ok {
			return p.ApplyReport
		}
	}
	return nil
}

// chattyStream always has a message ready, so the session's receive goroutine
// is reliably parked on the incoming channel by the time the session ends.
// Sends succeed okSends times and fail afterwards.
type chattyStream struct {
	fakeStream
	mu      sync.Mutex
	ctx     context.Context
	okSends int
	sends   int
}

var _ pb.Control_StreamClient = (*chattyStream)(nil)

func (s *chattyStream) bind(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ctx = ctx
}

func (s *chattyStream) Send(*pb.AgentMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sends++
	if s.sends > s.okSends {
		return errors.New("simulated send failure")
	}
	return nil
}

// Recv mirrors a real gRPC stream: it unblocks as soon as the stream's
// context is cancelled, and otherwise always has traffic.
func (s *chattyStream) Recv() (*pb.PanelMessage, error) {
	s.mu.Lock()
	ctx := s.ctx
	s.mu.Unlock()
	if ctx != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}
	// An empty payload: the session loop ignores it, which keeps this test
	// about goroutine lifetime rather than message handling.
	return &pb.PanelMessage{}, nil
}

// When the stream dies while the session loop is busy reconciling rather than
// parked in its select, the real failure and the closed incoming channel
// become ready together. A bare select picks between them at random, so the
// actual cause is lost roughly half the time — leaving an operator with an
// unexplained reconnect loop instead of "certificate expired".
func TestSessionReportsTheRealStreamFailure(t *testing.T) {
	const runs = 300
	lost := 0

	for i := 0; i < runs; i++ {
		cfg := &Config{
			PanelURL: "panel.example:8443", CAFingerprint: "dead",
			StateDir: t.TempDir(), NodeID: 1,
		}
		c := NewClient(cfg, MustRegistry(stub.New(t.TempDir())),
			NewFakeClock(time.Unix(1_700_000_000, 0).UTC()), tls.Certificate{}, nil)

		err := c.runSession(context.Background(), &fakeControlClient{},
			func(context.Context) (pb.Control_StreamClient, error) {
				return &busyStream{}, nil
			})
		if err == nil || !strings.Contains(err.Error(), "certificate expired") {
			lost++
		}
	}

	if lost > 0 {
		t.Errorf("the stream's real failure was replaced by a generic message in %d/%d runs; "+
			"an operator loses the reason nodes are reconnecting", lost, runs)
	}
}

// busyStream delivers one FetchNow, which sends the loop into reconcileOnce so
// it is NOT parked in select, then fails Recv. Both recvErr and the closed
// incoming channel are therefore ready when the loop returns to its select.
type busyStream struct {
	fakeStream
	mu sync.Mutex
	n  int
}

var _ pb.Control_StreamClient = (*busyStream)(nil)

func (s *busyStream) Send(*pb.AgentMessage) error { return nil }

func (s *busyStream) Recv() (*pb.PanelMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	if s.n == 1 {
		return &pb.PanelMessage{Payload: &pb.PanelMessage_FetchNow{FetchNow: &pb.FetchNow{}}}, nil
	}
	return nil, errors.New("certificate expired")
}
