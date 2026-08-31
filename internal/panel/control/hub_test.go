package control

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	pb "github.com/amyrm/antimage/internal/shared/proto/antimage/v1"
)

func TestNotifyReachesRegisteredNode(t *testing.T) {
	h := NewHub()
	ch, _, release := h.Register(7)
	defer release()

	if !h.Notify(7, 3) {
		t.Fatal("Notify returned false for a connected node")
	}
	select {
	case rev := <-ch:
		if rev != 3 {
			t.Errorf("revision = %d, want 3", rev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the revision bump")
	}
}

func TestNotifyToDisconnectedNodeIsFalse(t *testing.T) {
	h := NewHub()
	if h.Notify(99, 1) {
		t.Error("Notify returned true for a node that is not connected")
	}
	// This is not an error: the node reconciles on reconnect because state
	// lives in the database, not in the hub.
}

func TestReleaseRemovesTheNode(t *testing.T) {
	h := NewHub()
	_, _, release := h.Register(7)
	if !h.Online(7) {
		t.Fatal("node not reported online after Register")
	}
	release()
	if h.Online(7) {
		t.Error("node still online after release")
	}
	if h.Count() != 0 {
		t.Errorf("Count = %d, want 0", h.Count())
	}
}

func TestReconnectSupersedesTheOldStream(t *testing.T) {
	h := NewHub()
	first, _, releaseFirst := h.Register(7)
	second, _, releaseSecond := h.Register(7)
	defer releaseSecond()

	h.Notify(7, 5)
	select {
	case <-second:
	case <-time.After(time.Second):
		t.Fatal("the newest stream did not receive the bump")
	}
	select {
	case _, open := <-first:
		if open {
			t.Error("the superseded stream received a bump")
		}
	default:
	}
	releaseFirst() // must not panic or remove the live registration
	if !h.Online(7) {
		t.Error("releasing a superseded stream removed the live one")
	}
}

func TestNotifyNeverBlocks(t *testing.T) {
	h := NewHub()
	_, _, release := h.Register(7)
	defer release()
	// The agent is not reading. Notify must drop rather than block, because
	// a stalled agent must never stall an admin's HTTP request.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			h.Notify(7, int64(i))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Notify blocked on a slow consumer")
	}
}

// TestSendCommand_DeliversAndWaitsForResult is the round trip the restart
// fix depends on: a command placed on the node's command channel, and the
// caller blocked on SendCommand woken by DeliverResult with the exact
// result -- simulating what control_service.go's Stream handler does when
// an AgentCommandResult arrives.
func TestSendCommand_DeliversAndWaitsForResult(t *testing.T) {
	h := NewHub()
	_, cmds, release := h.Register(7)
	defer release()

	cmd := &pb.AgentCommand{
		CommandId: "cmd-1",
		Body: &pb.AgentCommand_RestartAdapters{
			RestartAdapters: &pb.RestartAdapters{},
		},
	}

	resultCh := make(chan *pb.AgentCommandResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := h.SendCommand(context.Background(), 7, cmd, time.Second)
		resultCh <- result
		errCh <- err
	}()

	select {
	case received := <-cmds:
		if received.CommandId != "cmd-1" {
			t.Fatalf("agent received command id %q, want cmd-1", received.CommandId)
		}
	case <-time.After(time.Second):
		t.Fatal("command never reached the agent's channel")
	}

	h.DeliverResult(&pb.AgentCommandResult{
		CommandId: "cmd-1",
		Body: &pb.AgentCommandResult_RestartAdapters{
			RestartAdapters: &pb.RestartAdaptersResult{
				Outcomes: []*pb.AdapterRestartOutcome{{Kind: "xray", Ok: true}},
			},
		},
	})

	if err := <-errCh; err != nil {
		t.Fatalf("SendCommand: %v", err)
	}
	result := <-resultCh
	if result.CommandId != "cmd-1" {
		t.Errorf("result command id = %q, want cmd-1", result.CommandId)
	}
}

// TestSendCommand_NotDeliveredWhenOffline proves the caller learns
// immediately that nothing was listening, rather than waiting out the full
// timeout for a node that was never going to answer.
func TestSendCommand_NotDeliveredWhenOffline(t *testing.T) {
	h := NewHub()
	start := time.Now()
	_, err := h.SendCommand(context.Background(), 99, &pb.AgentCommand{CommandId: "x"}, 5*time.Second)
	if !errors.Is(err, ErrCommandNotDelivered) {
		t.Errorf("err = %v, want ErrCommandNotDelivered", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("SendCommand took %v for an offline node; should return immediately", elapsed)
	}
}

// TestSendCommand_TimesOutWithoutAResult proves a connected-but-silent agent
// (one that received the command but crashed, hung, or is simply slow)
// produces a distinct, bounded-time error rather than blocking the HTTP
// request forever.
func TestSendCommand_TimesOutWithoutAResult(t *testing.T) {
	h := NewHub()
	_, cmds, release := h.Register(7)
	defer release()

	go func() {
		<-cmds // agent "receives" it and never replies
	}()

	_, err := h.SendCommand(context.Background(), 7, &pb.AgentCommand{CommandId: "x"}, 50*time.Millisecond)
	if !errors.Is(err, ErrCommandTimeout) {
		t.Errorf("err = %v, want ErrCommandTimeout", err)
	}
}

// A result for a command id nobody is waiting on (the caller already timed
// out, or SendCommand was never called for it) must be silently dropped,
// not panic and not block the caller of DeliverResult -- which, in
// production, is the Stream handler's own goroutine.
func TestDeliverResult_UnknownCommandIDIsDroppedSafely(t *testing.T) {
	h := NewHub()
	h.DeliverResult(&pb.AgentCommandResult{CommandId: "nobody-is-waiting"})
}

func TestHubIsRaceFree(t *testing.T) {
	h := NewHub()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ch, _, release := h.Register(int64(i % 5))
			h.Notify(int64(i%5), int64(i))
			select {
			case <-ch:
			default:
			}
			release()
		}(i)
	}
	wg.Wait()
}
