package control

import (
	"sync"
	"testing"
	"time"
)

func TestNotifyReachesRegisteredNode(t *testing.T) {
	h := NewHub()
	ch, release := h.Register(7)
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
	_, release := h.Register(7)
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
	first, releaseFirst := h.Register(7)
	second, releaseSecond := h.Register(7)
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
	_, release := h.Register(7)
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

func TestHubIsRaceFree(t *testing.T) {
	h := NewHub()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ch, release := h.Register(int64(i % 5))
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
