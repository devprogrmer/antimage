// Package control hosts the gRPC control plane.
//
// It owns ALL stream state (spec section 3). HTTP handlers never touch a
// stream: they bump a revision in the store and call Hub.Notify, so an admin
// action and a node reconnect converge through one code path.
package control

import "sync"

// Hub tracks which nodes currently hold a control stream and fans revision
// bumps out to them.
//
// The hub is deliberately not durable. A bump that misses a disconnected node
// is not lost, because desired state lives in the database and the agent
// re-reconciles on reconnect.
type Hub struct {
	mu    sync.RWMutex
	conns map[int64]chan int64
}

func NewHub() *Hub {
	return &Hub{conns: make(map[int64]chan int64)}
}

// Register attaches a stream for nodeID and returns its bump channel plus a
// release function. A second Register for the same node supersedes the first,
// closing its channel, which is what happens when an agent reconnects before
// the panel notices the old stream died.
func (h *Hub) Register(nodeID int64) (<-chan int64, func()) {
	ch := make(chan int64, 1)

	h.mu.Lock()
	if existing, ok := h.conns[nodeID]; ok {
		close(existing)
	}
	h.conns[nodeID] = ch
	h.mu.Unlock()

	var once sync.Once
	release := func() {
		once.Do(func() {
			h.mu.Lock()
			// Only remove if we are still the live registration.
			if current, ok := h.conns[nodeID]; ok && current == ch {
				delete(h.conns, nodeID)
				close(ch)
			}
			h.mu.Unlock()
		})
	}
	return ch, release
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
