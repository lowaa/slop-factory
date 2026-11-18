package ratelimit

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// sweepEvery is how many Allow calls pass between idle sweeps. Sweeping on
// a call count rather than a timer keeps the limiter free of goroutines to
// close, and costs nothing while nobody is calling.
const sweepEvery = 1024

// TokenBucketConfig configures a TokenBucket.
type TokenBucketConfig struct {
	// PerSecond is the refill rate of each key's bucket.
	PerSecond float64
	// Burst is the bucket's capacity: how many attempts a quiet key may
	// make at once.
	Burst int
	// Idle is how long a key may go unseen before its bucket is dropped,
	// so a stream of distinct keys can't grow memory without bound. A
	// dropped bucket comes back full, so Idle should be at least the time
	// to refill (Burst / PerSecond) for eviction to forget nothing.
	Idle time.Duration
	// Now is for tests; nil means time.Now.
	Now func() time.Time
}

type bucket struct {
	tokens float64
	seen   time.Time
}

// TokenBucket gives each key its own token bucket. It suits steady-rate
// control with bursts allowed; Limiter's sliding window suits "N attempts
// per window" limits.
type TokenBucket struct {
	perSecond float64
	burst     float64
	idle      time.Duration
	now       func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
	calls   int
}

// NewTokenBucket panics on a rate, burst or idle that is not positive, for
// the reason New does: each would quietly switch limiting off or lock
// everyone out.
func NewTokenBucket(cfg *TokenBucketConfig) *TokenBucket {
	if cfg.PerSecond <= 0 || cfg.Burst <= 0 || cfg.Idle <= 0 {
		panic(fmt.Sprintf("ratelimit: rate, burst and idle must be positive, got %g, %d, %s", cfg.PerSecond, cfg.Burst, cfg.Idle))
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &TokenBucket{
		perSecond: cfg.PerSecond,
		burst:     float64(cfg.Burst),
		idle:      cfg.Idle,
		now:       now,
		buckets:   map[string]*bucket{},
	}
}

// Allow takes a token from key's bucket, reporting whether there was one.
func (t *TokenBucket) Allow(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.calls++
	if t.calls%sweepEvery == 0 {
		for k, b := range t.buckets {
			if now.Sub(b.seen) > t.idle {
				delete(t.buckets, k)
			}
		}
	}
	b, ok := t.buckets[key]
	if !ok {
		b = &bucket{tokens: t.burst, seen: now}
		t.buckets[key] = b
	}
	if elapsed := now.Sub(b.seen); elapsed > 0 {
		b.tokens = min(t.burst, b.tokens+elapsed.Seconds()*t.perSecond)
	}
	b.seen = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Middleware limits requests by key(r), answering 429 over the limit.
func (t *TokenBucket) Middleware(key func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !t.Allow(key(r)) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
