package intake

import (
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lowaa/slop-factory/go-auth/ratelimit"
)

// Limiter decides whether a source may make another request now.
type Limiter interface {
	Allow(source uuid.UUID) bool
}

// RateLimiter is a token bucket per source, in memory: a limit per process
// is a limit per source however many replicas there are only up to that
// count, which is what a flood control needs.
type RateLimiter struct {
	buckets *ratelimit.TokenBucket
}

func NewRateLimiter(perMinute, burst int) *RateLimiter {
	perSecond := float64(perMinute) / 60
	// A bucket idle for its whole refill time is full again, so dropping it
	// forgets nothing; the floor keeps sweeps of fast-refilling limits rare.
	idle := max(time.Minute, time.Duration(float64(burst)/perSecond*float64(time.Second)))
	return &RateLimiter{buckets: ratelimit.NewTokenBucket(&ratelimit.TokenBucketConfig{PerSecond: perSecond, Burst: burst, Idle: idle})}
}

func (l *RateLimiter) Allow(source uuid.UUID) bool {
	return l.buckets.Allow(source.String())
}

// refusalLog coalesces rate-limited refusals into at most one recorded
// delivery per source per interval, carrying the count. Recording each one
// would hand a sender that has hit its limit a way to write to the
// database as fast as it can send.
type refusalLog struct {
	interval time.Duration
	now      func() time.Time
	mu       sync.Mutex
	last     map[uuid.UUID]time.Time
	pending  map[uuid.UUID]int
}

func newRefusalLog(interval time.Duration, now func() time.Time) *refusalLog {
	return &refusalLog{interval: interval, now: now, last: map[uuid.UUID]time.Time{}, pending: map[uuid.UUID]int{}}
}

// refuse counts a refusal, and reports how many to record now: zero while
// the interval since the last record has not passed.
func (r *refusalLog) refuse(source uuid.UUID) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending[source]++
	now := r.now()
	if last, ok := r.last[source]; ok && now.Sub(last) < r.interval {
		return 0
	}
	n := r.pending[source]
	r.pending[source] = 0
	r.last[source] = now
	return n
}
