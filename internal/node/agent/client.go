package agent

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/amyrm/antimage/internal/node/adapter"
	"github.com/amyrm/antimage/internal/node/enforcement"
	"github.com/amyrm/antimage/internal/node/sysinfo"
	pb "github.com/amyrm/antimage/internal/shared/proto/antimage/v1"
	"github.com/amyrm/antimage/internal/shared/version"
)

// ErrHashMismatch means the snapshot's bytes do not hash to the value the
// panel reported.
var ErrHashMismatch = errors.New("snapshot hash mismatch")

const (
	HeartbeatInterval    = 30 * time.Second
	ReconcileInterval    = 5 * time.Minute
	MaxReconnectBackoff  = 60 * time.Second
	baseReconnectBackoff = time.Second
)

// verifySnapshot re-hashes the document the panel sent. The panel is trusted,
// but a truncated response or an encoding bug is not, and applying a partial
// document would converge a node to the wrong state silently.
func verifySnapshot(document []byte, claimed string) error {
	sum := sha256.Sum256(document)
	got := hex.EncodeToString(sum[:])
	if got != claimed {
		return fmt.Errorf("%w: computed %s, panel reported %s", ErrHashMismatch, got, claimed)
	}
	return nil
}

// reconnectBackoff doubles with jitter and caps, so a panel restart is not
// thundering-herded by hundreds of agents reconnecting in lockstep.
func reconnectBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := baseReconnectBackoff
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= MaxReconnectBackoff {
			return MaxReconnectBackoff
		}
	}
	return d
}

// jitter spreads reconnects and reconcile timers.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	spread := int64(d) / 4
	if spread <= 0 {
		return d
	}
	delta := time.Duration(rand.Int63n(spread)) //nolint:gosec // spread, not secrecy
	return d - delta/2
}

// Client holds the control stream and drives reconciliation.
type Client struct {
	cfg      *Config
	ad       adapter.Adapter
	clk      Clock
	rec      *Reconciler
	cert     tls.Certificate
	caDER    []byte
	enforcer *enforcement.Enforcer
}

func NewClient(cfg *Config, ad adapter.Adapter, clk Clock, cert tls.Certificate, caDER []byte) *Client {
	return &Client{
		cfg: cfg, ad: ad, clk: clk, cert: cert, caDER: caDER,
		rec:      NewReconciler(ad, clk, ReconcileOptions{MaxRetries: 3, RetryBase: 2 * time.Second}),
		enforcer: enforcement.New(),
	}
}

// Run dials the panel and reconnects forever with capped, jittered backoff.
func (c *Client) Run(ctx context.Context) error {
	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := c.session(ctx)
		if err == nil || errors.Is(err, context.Canceled) {
			return err
		}
		attempt++
		slog.WarnContext(ctx, "control stream ended; reconnecting",
			"attempt", attempt, "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.clk.After(jitter(reconnectBackoff(attempt))):
		}
	}
}

func (c *Client) dial() (*grpc.ClientConn, error) {
	pool := x509.NewCertPool()
	caCert, err := x509.ParseCertificate(c.caDER)
	if err != nil {
		return nil, fmt.Errorf("parse panel CA: %w", err)
	}
	pool.AddCert(caCert)

	target, err := c.cfg.GRPCTarget()
	if err != nil {
		return nil, err
	}

	creds := credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{c.cert},
		RootCAs:      pool,
		// ServerName is left to gRPC, which derives it from the target's
		// authority. That is only correct because GRPCTarget produced a real
		// host:port: with the raw "https://..." target it would have been
		// derived from a string that is not a hostname at all.
		MinVersion: tls.VersionTLS13,
	})
	return grpc.NewClient(target, grpc.WithTransportCredentials(creds))
}

// session runs one connection: Hello, then heartbeats, reconcile timer, and
// panel messages until the stream dies.
func (c *Client) session(ctx context.Context) error {
	conn, err := c.dial()
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer func() { _ = conn.Close() }()

	client := pb.NewControlClient(conn)
	return c.runSession(ctx, client, func(sessionCtx context.Context) (pb.Control_StreamClient, error) {
		return client.Stream(sessionCtx)
	})
}

// runSession drives one control stream to completion.
//
// It opens the stream itself, through openStream, because the stream and the
// receive goroutine must share a context that is cancelled the instant this
// function returns — for ANY reason, not just cancellation of the agent's own
// context or a recv error. The receive goroutine parks on `incoming <- msg`
// whenever the loop below is not reading, and the loop stops reading the
// moment it returns. Every non-recv exit path (a failed heartbeat send, an
// upgrade demand from the panel) would therefore strand that goroutine on the
// send forever, holding the stream with it: one leaked goroutine and one
// retained stream per reconnect, for the life of the process.
func (c *Client) runSession(
	ctx context.Context,
	client pb.ControlClient,
	openStream func(context.Context) (pb.Control_StreamClient, error),
) error {
	sessionCtx, cancel := context.WithCancel(ctx)
	// Covers the exits below that happen before the receive goroutine exists.
	defer cancel()

	stream, err := openStream(sessionCtx)
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}

	desc := c.ad.Descriptor()
	if err := stream.Send(&pb.AgentMessage{Payload: &pb.AgentMessage_Hello{Hello: &pb.Hello{
		NodeId: c.cfg.NodeID, AgentVersion: version.Version,
		ProtocolVersion: version.Protocol,
		Adapters: []*pb.AdapterDescriptor{{
			Kind: string(desc.Kind), Version: desc.Version,
			HotUserAdd: desc.Caps.HotUserAdd, SelfAccounting: desc.Caps.SelfAccounting,
			RequiresPki: desc.Caps.RequiresPKI, ServiceSchema: desc.Caps.ServiceSchema,
		}},
	}}}); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}

	incoming := make(chan *pb.PanelMessage)
	recvErr := make(chan error, 1)
	recvDone := make(chan struct{})
	go func() {
		defer close(recvDone)
		defer close(incoming)
		for {
			msg, err := stream.Recv()
			if err != nil {
				recvErr <- err
				return
			}
			select {
			case incoming <- msg:
			case <-sessionCtx.Done():
				return
			}
		}
	}()
	// Wait for the goroutine rather than merely signalling it, so "the
	// session has ended" and "the session's goroutine has ended" cannot drift
	// apart. Cancel first and wait second: defers run last-in-first-out, so
	// this runs ahead of the cancel above, and waiting on a goroutine that
	// has not been told to stop would deadlock against the very park this
	// exists to prevent. Cancelling twice is harmless. The cancellation
	// unblocks both the channel send above and stream.Recv, which is bound to
	// the same context.
	defer func() {
		cancel()
		<-recvDone
	}()

	heartbeat := c.clk.After(HeartbeatInterval)
	reconcile := c.clk.After(jitter(ReconcileInterval))

	// Start accounting loop if the adapter supports it (SP3).
	if _, ok := c.ad.(adapter.UsageReporter); ok {
		go c.AccountingLoop(sessionCtx, stream, 30*time.Second)
	}

	// Start enforcement stats loop.
	go c.EnforcementStatsLoop(sessionCtx, stream, 60*time.Second)

	// Start Xray enforcement loop if adapter is Xray (SP7).
	c.startXrayEnforcementIfSupported(sessionCtx)

	for {
		select {
		case <-sessionCtx.Done():
			return sessionCtx.Err()

		case err := <-recvErr:
			return err

		case msg, ok := <-incoming:
			if !ok {
				// The receive goroutine sends the real failure to recvErr
				// before its deferred close of incoming, so whenever the
				// stream died from a Recv error that error is already
				// buffered here. Both channels are then ready at once, and a
				// bare select would pick between them at random — losing the
				// actual cause roughly half the time, precisely when the loop
				// was busy reconciling rather than parked in the select. To an
				// operator that is the difference between "certificate
				// expired" and an unexplained reconnect loop.
				select {
				case err := <-recvErr:
					return err
				default:
					return errors.New("stream closed")
				}
			}
			switch msg.Payload.(type) {
			case *pb.PanelMessage_RevisionBump, *pb.PanelMessage_FetchNow:
				if err := c.reconcileOnce(sessionCtx, client, stream); err != nil {
					slog.ErrorContext(sessionCtx, "reconcile failed", "error", err)
				}
			case *pb.PanelMessage_UpgradeRequired:
				return errors.New("panel requires an agent upgrade")
			}

		case <-heartbeat:
			if err := c.sendHeartbeat(sessionCtx, stream); err != nil {
				return err
			}
			heartbeat = c.clk.After(HeartbeatInterval)

		case <-reconcile:
			if err := c.reconcileOnce(sessionCtx, client, stream); err != nil {
				slog.ErrorContext(sessionCtx, "scheduled reconcile failed", "error", err)
			}
			reconcile = c.clk.After(jitter(ReconcileInterval))
		}
	}
}

func (c *Client) reconcileOnce(ctx context.Context, client pb.ControlClient, stream pb.Control_StreamClient) error {
	snap, err := client.GetDesiredSnapshot(ctx, &pb.SnapshotRequest{NodeId: c.cfg.NodeID})
	if err != nil {
		return fmt.Errorf("fetch snapshot: %w", err)
	}
	if err := verifySnapshot(snap.Document, snap.Sha256); err != nil {
		return err
	}

	var desired adapter.Desired
	if err := json.Unmarshal(snap.Document, &desired); err != nil {
		return fmt.Errorf("decode desired document: %w", err)
	}

	// Refuse a document from the future BEFORE converging on part of it.
	//
	// Decoding cannot detect this: fields added by a newer schema are
	// omitempty, so an old agent parses a new document cleanly and silently
	// ignores what it did not recognise. Converging on the remainder would
	// report success for a state the node is not actually in.
	if err := adapter.CheckSchemaVersion(desired.SchemaVersion); err != nil {
		report := &pb.ApplyReport{
			TargetRevision: snap.Revision, Converged: false,
			Error: err.Error(), DocSha256: snap.Sha256,
		}
		if sendErr := stream.Send(&pb.AgentMessage{
			Payload: &pb.AgentMessage_ApplyReport{ApplyReport: report},
		}); sendErr != nil {
			return fmt.Errorf("report unsupported schema: %w", sendErr)
		}
		// Not a transport failure, so the stream stays up: the operator sees a
		// node reporting a clear reason rather than one that keeps dropping.
		return nil
	}

	// Sync enforcement policies before convergence
	c.syncEnforcement(desired)

	run, runErr := c.rec.Converge(ctx, desired)

	report := &pb.ApplyReport{
		TargetRevision: snap.Revision, Converged: run.Converged,
		Deferred: run.Deferred, Error: run.Err, DocSha256: snap.Sha256,
	}
	for _, st := range run.Steps {
		report.Steps = append(report.Steps, &pb.StepResult{
			Seq: int32(st.Seq), Ok: st.OK, Error: st.Err,
			// Disruption.String() is the wire form on purpose: it yields
			// exactly the values node_apply_steps.disruption is CHECKed
			// against ("none" | "reload" | "restart"), so the panel stores
			// what the adapter meant rather than a stringified enum ordinal.
			Kind: st.Kind, Disruption: st.Disruption.String(),
			DurationMs: st.Duration.Milliseconds(),
		})
	}
	if err := stream.Send(&pb.AgentMessage{
		Payload: &pb.AgentMessage_ApplyReport{ApplyReport: report},
	}); err != nil {
		return fmt.Errorf("send apply report: %w", err)
	}
	return runErr
}

func (c *Client) sendHeartbeat(ctx context.Context, stream pb.Control_StreamClient) error {
	health, err := c.ad.Probe(ctx)
	if err != nil {
		health = adapter.Health{OK: false, Detail: err.Error()}
	}
	sample := sysinfo.Sample()
	return stream.Send(&pb.AgentMessage{Payload: &pb.AgentMessage_Heartbeat{
		Heartbeat: &pb.Heartbeat{
			Load1: sample.Load1, MemUsedBytes: sample.MemUsed, UptimeSeconds: sample.UptimeS,
			AdapterHealth: []*pb.AdapterHealth{{
				Kind: string(c.ad.Descriptor().Kind), Ok: health.OK, Detail: health.Detail,
			}},
		},
	}})
}
