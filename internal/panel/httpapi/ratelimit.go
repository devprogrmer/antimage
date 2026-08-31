package httpapi

import (
	"net/http"
	"sync"
	"time"
)

// rateLimiter implements a simple token bucket rate limiter per actor
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[int64]*bucket // keyed by admin ID
	rate    int               // requests per window
	window  time.Duration
}

type bucket struct {
	tokens    int
	lastReset time.Time
}

func newRateLimiter(rate int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		buckets: make(map[int64]*bucket),
		rate:    rate,
		window:  window,
	}
}

// allow checks if the request is allowed and consumes a token
func (rl *rateLimiter) allow(adminID int64) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, exists := rl.buckets[adminID]

	if !exists {
		rl.buckets[adminID] = &bucket{
			tokens:    rl.rate - 1,
			lastReset: now,
		}
		return true
	}

	// Reset bucket if window has passed
	if now.Sub(b.lastReset) >= rl.window {
		b.tokens = rl.rate
		b.lastReset = now
	}

	if b.tokens > 0 {
		b.tokens--
		return true
	}

	return false
}

// cleanup removes stale buckets (called periodically)
func (rl *rateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	for id, b := range rl.buckets {
		if now.Sub(b.lastReset) > rl.window*2 {
			delete(rl.buckets, id)
		}
	}
}

// rateLimitMiddleware applies rate limiting per authenticated admin
func (d Deps) rateLimitMiddleware(limiter *rateLimiter) func(http.Handler) http.Handler {
	// Start cleanup goroutine
	go func() {
		ticker := time.NewTicker(limiter.window)
		defer ticker.Stop()
		for range ticker.C {
			limiter.cleanup()
		}
	}()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor := ActorFrom(r.Context())
			if actor == nil {
				// Not authenticated yet, let authMiddleware handle it
				next.ServeHTTP(w, r)
				return
			}

			if !limiter.allow(actor.AdminID) {
				WriteError(w, http.StatusTooManyRequests, "rate_limit", "too many requests")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
