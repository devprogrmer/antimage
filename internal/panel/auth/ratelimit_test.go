package auth

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

func newLimiter(t *testing.T) (*Limiter, *fakeClock) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	clk := &fakeClock{now: time.Unix(1_700_000_000, 0).UTC()}
	return NewLimiter(s, clk.Now), clk
}

func TestCleanSubjectIsAllowed(t *testing.T) {
	lim, _ := newLimiter(t)
	wait, err := lim.Check(context.Background(), "alice", "10.0.0.1")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if wait != 0 {
		t.Errorf("retryAfter = %v, want 0 for a clean subject", wait)
	}
}

func TestAccountLocksAfterFiveFailures(t *testing.T) {
	lim, _ := newLimiter(t)
	ctx := context.Background()
	for i := 0; i < AccountFailureLimit; i++ {
		if err := lim.RecordFailure(ctx, "alice", "10.0.0.1"); err != nil {
			t.Fatalf("RecordFailure %d: %v", i, err)
		}
	}
	wait, err := lim.Check(ctx, "alice", "10.0.0.9")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if wait <= 0 {
		t.Fatal("account not limited after 5 failures")
	}
}

func TestBackoffDoublesAndIsCapped(t *testing.T) {
	lim, _ := newLimiter(t)
	ctx := context.Background()
	var last time.Duration
	for i := 0; i < 20; i++ {
		if err := lim.RecordFailure(ctx, "bob", "10.0.0.1"); err != nil {
			t.Fatalf("RecordFailure: %v", err)
		}
		wait, err := lim.Check(ctx, "bob", "10.0.0.1")
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		if wait > MaxBackoff {
			t.Fatalf("backoff %v exceeded cap %v", wait, MaxBackoff)
		}
		if wait < last {
			t.Fatalf("backoff decreased: %v after %v", wait, last)
		}
		last = wait
	}
	if last != MaxBackoff {
		t.Errorf("final backoff = %v, want the %v ceiling", last, MaxBackoff)
	}
}

func TestFailuresOutsideWindowAreIgnored(t *testing.T) {
	lim, clk := newLimiter(t)
	ctx := context.Background()
	for i := 0; i < AccountFailureLimit; i++ {
		_ = lim.RecordFailure(ctx, "carol", "10.0.0.1")
	}
	clk.advance(Window + time.Minute)
	wait, err := lim.Check(ctx, "carol", "10.0.0.1")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if wait != 0 {
		t.Errorf("retryAfter = %v after the window elapsed, want 0", wait)
	}
}

func TestIPLimitCatchesSpreadAcrossAccounts(t *testing.T) {
	lim, _ := newLimiter(t)
	ctx := context.Background()
	// Under the per-account limit for each name, but over the IP limit overall.
	for i := 0; i < IPFailureLimit; i++ {
		user := string(rune('a' + i%26))
		if err := lim.RecordFailure(ctx, user, "10.0.0.7"); err != nil {
			t.Fatalf("RecordFailure: %v", err)
		}
	}
	wait, err := lim.Check(ctx, "brand-new-user", "10.0.0.7")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if wait <= 0 {
		t.Fatal("IP not limited — credential stuffing across many usernames would pass")
	}
}

func TestResetClearsAfterSuccessfulLogin(t *testing.T) {
	lim, _ := newLimiter(t)
	ctx := context.Background()
	for i := 0; i < AccountFailureLimit; i++ {
		_ = lim.RecordFailure(ctx, "dave", "10.0.0.1")
	}
	if err := lim.Reset(ctx, "dave", "10.0.0.1"); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	wait, err := lim.Check(ctx, "dave", "10.0.0.1")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if wait != 0 {
		t.Errorf("retryAfter = %v after reset, want 0", wait)
	}
}
