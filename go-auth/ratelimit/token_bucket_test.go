package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func newBucket(perSecond float64, burst int, idle time.Duration) (*TokenBucket, *fakeClock) {
	c := &fakeClock{t: time.Unix(1_000_000, 0)}
	return NewTokenBucket(&TokenBucketConfig{PerSecond: perSecond, Burst: burst, Idle: idle, Now: c.Now}), c
}

func TestTokenBucket_BurstThenDeny(t *testing.T) {
	tb, _ := newBucket(1, 2, time.Minute)
	assert.True(t, tb.Allow("k"))
	assert.True(t, tb.Allow("k"))
	assert.False(t, tb.Allow("k"), "burst spent and no time has passed")
}

func TestTokenBucket_KeysAreIndependent(t *testing.T) {
	tb, _ := newBucket(1, 1, time.Minute)
	require.True(t, tb.Allow("a"))
	assert.False(t, tb.Allow("a"))
	assert.True(t, tb.Allow("b"))
}

func TestTokenBucket_RefillsAtRateAndCapsAtBurst(t *testing.T) {
	tb, c := newBucket(2, 3, time.Hour)
	for range 3 {
		require.True(t, tb.Allow("k"))
	}
	require.False(t, tb.Allow("k"))

	c.Advance(750 * time.Millisecond) // 1.5 tokens at 2/s
	assert.True(t, tb.Allow("k"))
	assert.False(t, tb.Allow("k"), "only 0.5 token left")

	c.Advance(250 * time.Millisecond) // the kept 0.5 plus 0.5 more
	assert.True(t, tb.Allow("k"), "a fractional remainder carries over")
	assert.False(t, tb.Allow("k"))

	c.Advance(time.Minute)
	for range 3 {
		assert.True(t, tb.Allow("k"))
	}
	assert.False(t, tb.Allow("k"), "refill is capped at the burst")
}

func TestTokenBucket_EvictsIdleKeysOnSweep(t *testing.T) {
	tb, c := newBucket(1, 1, time.Minute)
	require.True(t, tb.Allow("stale"))

	c.Advance(time.Hour)
	for i := 0; i < sweepEvery-2; i++ {
		tb.Allow("filler")
	}
	_, present := tb.buckets["stale"]
	assert.True(t, present, "no sweep before the sweepEvery-th call")

	tb.Allow("filler")
	_, present = tb.buckets["stale"]
	assert.False(t, present, "idle bucket evicted by the sweep")
	_, present = tb.buckets["filler"]
	assert.True(t, present, "a key just seen survives the sweep")
}

func TestTokenBucket_KeepsKeysWithinIdle(t *testing.T) {
	tb, c := newBucket(1, 1, time.Hour)
	require.True(t, tb.Allow("k"))
	c.Advance(time.Minute)
	for i := 0; i < sweepEvery-1; i++ {
		tb.Allow("filler")
	}
	_, present := tb.buckets["k"]
	assert.True(t, present, "not idle long enough to evict")
}

func TestTokenBucket_EvictedKeyStartsFull(t *testing.T) {
	tb, c := newBucket(0.001, 1, time.Minute)
	require.True(t, tb.Allow("k"))
	require.False(t, tb.Allow("k"))
	c.Advance(2 * time.Minute) // past idle, yet far too short to refill a token
	for i := 0; i < sweepEvery; i++ {
		tb.Allow("filler" + strconv.Itoa(i))
	}
	assert.True(t, tb.Allow("k"))
}

func TestNewTokenBucket_PanicsOnNonPositive(t *testing.T) {
	assert.Panics(t, func() { NewTokenBucket(&TokenBucketConfig{PerSecond: 0, Burst: 1, Idle: time.Second}) })
	assert.Panics(t, func() { NewTokenBucket(&TokenBucketConfig{PerSecond: 1, Burst: 0, Idle: time.Second}) })
	assert.Panics(t, func() { NewTokenBucket(&TokenBucketConfig{PerSecond: 1, Burst: 1}) })
}

func TestTokenBucket_Middleware(t *testing.T) {
	tb, _ := newBucket(1, 1, time.Minute)
	calls := 0
	mw := tb.Middleware(func(r *http.Request) string { return r.Header.Get("X-Caller") },
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	do := func(caller string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Caller", caller)
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		return rec
	}
	assert.Equal(t, http.StatusOK, do("a").Code)
	rec := do("a")
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "1", rec.Header().Get("Retry-After"))
	assert.Equal(t, http.StatusOK, do("b").Code)
	assert.Equal(t, 2, calls, "the limited request never reaches the handler")
}
