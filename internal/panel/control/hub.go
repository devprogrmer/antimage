// Package control hosts the gRPC control plane.
//
// It owns ALL stream state (spec section 3). HTTP handlers never touch a
// stream: they bump a revision in the store and call Hub.Notify, so an admin
// action and a node reconnect converge through one code path.
package control

import (
	"context"
	"errors"
	"sync"
	"time"

	pb "github.com/amyrm/antimage/internal/shared/proto/antimage/v1"
)

// Hub tracks which nodes currently hold a control stream and fans messages
// out to them: revision bumps (fire-and-forget) and on-demand commands
// (request/response, correlated by command id).
//
// The hub is deliberately not durable. A bump or command that misses a
// disconnected node is not lost in the sense that matters: desired state
// lives in the database and the agent re-reconciles on reconnect, and an
// on-demand command that could not be delivered is reported to its caller
// as such rather than silently retried later -- "restart this now" has no
// honest meaning for a node that is not there to receive it "later".
type Hub struct {
	mu    sync.RWMutex
	conns map[int64]chan int64
	cmds  map[int64]chan *pb.AgentCommand

	// pending correlates an in-flight AgentCommand to whichever caller is
	// waiting on SendCommand. Keyed by command_id rather than node id,
	// because a node can only be mid-command once at a time from THIS hub's
	// perspective, but the id space is the command's own, not the node's --
	// using it keeps DeliverResult from needing to know which node a result
	// came from.
	pendingMu sync.Mutex
	pending   map[string]chan *pb.AgentCommandResult
}

func NewHub() *Hub {
	return &Hub{
		conns:   make(map[int64]chan int64),
		cmds:    make(map[int64]chan *pb.AgentCommand),
		pending: make(map[string]chan *pb.AgentCommandResult),
	}
}

// Register attaches a stream for nodeID and returns its bump channel, its
// command channel, and a release function. A second Register for the same
// node supersedes the first, closing both of its channels, which is what
// happens when an agent reconnects before the panel notices the old stream
// died.
func (h *Hub) Register(nodeID int64) (bumps <-chan int64, cmds <-chan *pb.AgentCommand, release func()) {
	bumpCh := make(chan int64, 1)
	cmdCh := make(chan *pb.AgentCommand, 1)

	h.mu.Lock()
	if existing, ok := h.conns[nodeID]; ok {
		close(existing)
	}
	if existing, ok := h.cmds[nodeID]; ok {
		close(existing)
	}
	h.conns[nodeID] = bumpCh
	h.cmds[nodeID] = cmdCh
	h.mu.Unlock()

	var once sync.Once
	rel := func() {
		once.Do(func() {
			h.mu.Lock()
			if current, ok := h.conns[nodeID]; ok && current == bumpCh {
				delete(h.conns, nodeID)
				close(bumpCh)
			}
			if current, ok := h.cmds[nodeID]; ok && current == cmdCh {
				delete(h.cmds, nodeID)
				close(cmdCh)
			}
			h.mu.Unlock()
		})
	}
	return bumpCh, cmdCh, rel
}

// Notify delivers a revision bump. It reports whether a stream was connected.
//
// It never blocks: if the agent's buffer is full it drops the bump, because
// the agent will fetch the latest snapshot anyway and a stalled node must
// never stall an admin request.
//
// The lock is held across the send, not just the map lookup. The send itself
// cannot block (select has a default case), so this costs nothing, and it is
// what stops a concurrent Register or release from closing this exact
// channel between the lookup and the send — without it, that interleaving
// makes Notify send on a closed channel and panic.
func (h *Hub) Notify(nodeID, revision int64) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	ch, ok := h.conns[nodeID]
	if !ok {
		return false
	}
	select {
	case ch <- revision:
	default:
	}
	return true
}

// ErrCommandNotDelivered means the target node was not connected. Distinct
// from a timeout: a timeout means the node had the command and has not yet
// answered; this means it never got the command at all.
var ErrCommandNotDelivered = errors.New("node not connected")

// ErrCommandTimeout means the command reached a connected node but no
// result arrived within the deadline. The command may still complete on the
// node; the caller has simply stopped waiting for it.
var ErrCommandTimeout = errors.New("no result before deadline")

// SendCommand delivers cmd to nodeID and waits up to timeout for its result.
//
// Unlike Notify, this DOES wait -- a revision bump has nothing to wait for
// (the agent will fetch the document on its own schedule regardless), but an
// operator who clicked "restart" is asking a yes/or/no question that the
// HTTP response has to answer honestly, and the answer is not known until
// the agent replies.
func (h *Hub) SendCommand(ctx context.Context, nodeID int64, cmd *pb.AgentCommand, timeout time.Duration) (*pb.AgentCommandResult, error) {
	resultCh := make(chan *pb.AgentCommandResult, 1)

	h.pendingMu.Lock()
	h.pending[cmd.CommandId] = resultCh
	h.pendingMu.Unlock()
	defer func() {
		h.pendingMu.Lock()
		delete(h.pending, cmd.CommandId)
		h.pendingMu.Unlock()
	}()

	h.mu.RLock()
	ch, ok := h.cmds[nodeID]
	if ok {
		select {
		case ch <- cmd:
		default:
			// The node's command buffer (capacity 1) is already full with an
			// undelivered command. Reported the same as "not connected": from
			// the caller's perspective nothing new was delivered either way.
			ok = false
		}
	}
	h.mu.RUnlock()
	if !ok {
		return nil, ErrCommandNotDelivered
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-resultCh:
		return result, nil
	case <-timer.C:
		return nil, ErrCommandTimeout
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// DeliverResult wakes whichever SendCommand call is waiting on this result's
// command id, if any. Called from the Stream handler when an
// AgentCommandResult arrives; a result for a command id nobody is waiting on
// (the caller already timed out, or this hub restarted) is dropped, which is
// correct -- there is nothing left to deliver it to.
func (h *Hub) DeliverResult(result *pb.AgentCommandResult) {
	h.pendingMu.Lock()
	ch, ok := h.pending[result.CommandId]
	h.pendingMu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- result:
	default:
	}
}

func (h *Hub) Online(nodeID int64) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.conns[nodeID]
	return ok
}

func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns)
}
