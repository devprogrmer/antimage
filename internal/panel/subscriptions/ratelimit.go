package subscriptions

import (
	"sync"
	"time"
)

// RateLimiter enforces per-token request limits using a sliding window algorithm.
type RateLimiter interface {
	// Allow returns true if the request is allowed, false if rate limit exceeded.
	Allow(token string) bool
}

// slidingWindowLimiter tracks request timestamps per token and enforces a limit
// over a sliding time window.
type slidingWindowLimiter struct {
	limit  int
	window time.Duration

	mu      sync.RWMutex
	buckets map[string][]time.Time

	// Cleanup goroutine management
	stopCleanup chan struct{}
	cleanupOnce sync.Once
}

// NewSlidingWindowLimiter creates a rate limiter with the given limit and window.
// Example: NewSlidingWindowLimiter(10, time.Minute) = 10 requests per minute.
func NewSlidingWindowLimiter(limit int, window time.Duration) RateLimiter {
	rl := &slidingWindowLimiter{
		limit:       limit,
		window:      window,
		buckets:     make(map[string][]time.Time),
		stopCleanup: make(chan struct{}),
	}

	// Start background cleanup goroutine to prevent memory leaks.
	go rl.cleanup()

	return rl
}

// Allow checks if a request for the given token should be allowed.
func (rl *slidingWindowLimiter) Allow(token string) bool {
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Get existing timestamps for this token.
	timestamps := rl.buckets[token]

	// Remove timestamps outside the current window.
	cutoff := now.Add(-rl.window)
	valid := timestamps[:0]
	for _, ts := range timestamps {
		if ts.After(cutoff) {
			valid = append(valid, ts)
		}
	}

	// Check if we're at the limit.
	if len(valid) >= rl.limit {
		rl.buckets[token] = valid
		return false
	}

	// Allow the request and record timestamp.
	valid = append(valid, now)
	rl.buckets[token] = valid
	return true
}

// cleanup periodically removes old tokens to prevent unbounded memory growth.
func (rl *slidingWindowLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			rl.pruneOldTokens()
		case <-rl.stopCleanup:
			return
		}
	}
}

// pruneOldTokens removes tokens that haven't been seen in 2x the window duration.
func (rl *slidingWindowLimiter) pruneOldTokens() {
	now := time.Now()
	cutoff := now.Add(-2 * rl.window)

	rl.mu.Lock()
	defer rl.mu.Unlock()

	for token, timestamps := range rl.buckets {
		// If no recent activity, remove the token entirely.
		if len(timestamps) == 0 || timestamps[len(timestamps)-1].Before(cutoff) {
			delete(rl.buckets, token)
		}
	}
}

// Stop halts the cleanup goroutine (for testing).
func (rl *slidingWindowLimiter) Stop() {
	rl.cleanupOnce.Do(func() {
		close(rl.stopCleanup)
	})
}
