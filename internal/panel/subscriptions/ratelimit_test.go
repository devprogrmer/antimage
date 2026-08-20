package subscriptions

import (
	"sync"
	"testing"
	"time"
)

func TestSlidingWindowLimiter_BasicLimit(t *testing.T) {
	rl := NewSlidingWindowLimiter(10, time.Minute)
	defer rl.(*slidingWindowLimiter).Stop()

	token := "test-token-123"

	// First 10 requests should be allowed.
	for i := 0; i < 10; i++ {
		if !rl.Allow(token) {
			t.Errorf("request %d should be allowed", i+1)
		}
	}

	// 11th request should be denied.
	if rl.Allow(token) {
		t.Error("11th request should be denied")
	}

	// 12th request should also be denied.
	if rl.Allow(token) {
		t.Error("12th request should be denied")
	}
}

func TestSlidingWindowLimiter_WindowSlides(t *testing.T) {
	// Use a short window for testing.
	rl := NewSlidingWindowLimiter(3, 100*time.Millisecond)
	defer rl.(*slidingWindowLimiter).Stop()

	token := "test-token-456"

	// Use up the limit.
	for i := 0; i < 3; i++ {
		if !rl.Allow(token) {
			t.Errorf("request %d should be allowed", i+1)
		}
	}

	// 4th request denied.
	if rl.Allow(token) {
		t.Error("4th request should be denied")
	}

	// Wait for window to slide.
	time.Sleep(150 * time.Millisecond)

	// Should be allowed again after window expires.
	if !rl.Allow(token) {
		t.Error("request after window expiry should be allowed")
	}
}

func TestSlidingWindowLimiter_PerTokenIsolation(t *testing.T) {
	rl := NewSlidingWindowLimiter(5, time.Minute)
	defer rl.(*slidingWindowLimiter).Stop()

	token1 := "token-1"
	token2 := "token-2"

	// Use up token1's limit.
	for i := 0; i < 5; i++ {
		if !rl.Allow(token1) {
			t.Errorf("token1 request %d should be allowed", i+1)
		}
	}

	// Token1 exhausted.
	if rl.Allow(token1) {
		t.Error("token1 should be rate limited")
	}

	// Token2 should still have full quota.
	for i := 0; i < 5; i++ {
		if !rl.Allow(token2) {
			t.Errorf("token2 request %d should be allowed", i+1)
		}
	}

	// Token2 exhausted.
	if rl.Allow(token2) {
		t.Error("token2 should be rate limited")
	}
}

func TestSlidingWindowLimiter_ConcurrentAccess(t *testing.T) {
	rl := NewSlidingWindowLimiter(100, time.Minute)
	defer rl.(*slidingWindowLimiter).Stop()

	var wg sync.WaitGroup
	tokens := []string{"token-a", "token-b", "token-c"}

	// Concurrent requests across multiple tokens.
	for _, token := range tokens {
		token := token // capture
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rl.Allow(token)
			}()
		}
	}

	wg.Wait()
	// Test passes if no data races occur.
}

func TestSlidingWindowLimiter_MemoryCleanup(t *testing.T) {
	rl := NewSlidingWindowLimiter(10, 50*time.Millisecond)
	defer rl.(*slidingWindowLimiter).Stop()

	swl := rl.(*slidingWindowLimiter)

	// Generate activity for multiple tokens.
	for i := 0; i < 100; i++ {
		token := "temp-token-" + string(rune(i))
		rl.Allow(token)
	}

	// Verify tokens are tracked.
	swl.mu.RLock()
	initialCount := len(swl.buckets)
	swl.mu.RUnlock()

	if initialCount == 0 {
		t.Fatal("expected tokens to be tracked")
	}

	// Wait for cleanup to run (window expires + cleanup interval).
	time.Sleep(200 * time.Millisecond)
	swl.pruneOldTokens() // Force cleanup.

	// Verify old tokens are removed.
	swl.mu.RLock()
	finalCount := len(swl.buckets)
	swl.mu.RUnlock()

	if finalCount >= initialCount {
		t.Errorf("expected cleanup to reduce token count: %d -> %d", initialCount, finalCount)
	}
}

func TestSlidingWindowLimiter_ZeroLimit(t *testing.T) {
	rl := NewSlidingWindowLimiter(0, time.Minute)
	defer rl.(*slidingWindowLimiter).Stop()

	token := "test-token"

	// With zero limit, all requests should be denied.
	if rl.Allow(token) {
		t.Error("request should be denied with zero limit")
	}
}

func TestSlidingWindowLimiter_SingleRequest(t *testing.T) {
	rl := NewSlidingWindowLimiter(1, time.Minute)
	defer rl.(*slidingWindowLimiter).Stop()

	token := "test-token"

	// First request allowed.
	if !rl.Allow(token) {
		t.Error("first request should be allowed")
	}

	// Second request denied.
	if rl.Allow(token) {
		t.Error("second request should be denied")
	}
}
