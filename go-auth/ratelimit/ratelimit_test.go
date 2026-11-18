package ratelimit_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/go-auth/ratelimit"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }
func newLimiter(c *clock, limit, maxKeys int) *ratelimit.Limiter {
	return ratelimit.New(&ratelimit.Config{Limit: limit, Window: time.Minute, MaxKeys: maxKeys, Now: c.now})
}

func TestAllowsUpToTheLimitPerKey(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	l := newLimiter(c, 3, 100)

	for i := range 3 {
		assert.True(t, l.Allow("a"), "attempt %d", i+1)
	}
	assert.False(t, l.Allow("a"), "the fourth attempt in the window is refused")
	assert.True(t, l.Allow("b"), "another key has its own allowance")
}

func TestTheWindowSlides(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	l := newLimiter(c, 2, 100)

	assert.True(t, l.Allow("a"))
	c.advance(40 * time.Second)
	assert.True(t, l.Allow("a"))
	assert.False(t, l.Allow("a"))

	c.advance(21 * time.Second)
	assert.True(t, l.Allow("a"), "the first attempt has aged out")
	assert.False(t, l.Allow("a"), "the second has not")
}

// A caller who keeps hammering must not keep pushing their own window
// forward: only the attempts that were allowed count.
func TestRefusedAttemptsAreNotCounted(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	l := newLimiter(c, 1, 100)

	assert.True(t, l.Allow("a"))
	for range 10 {
		c.advance(5 * time.Second)
		assert.False(t, l.Allow("a"))
	}
	c.advance(11 * time.Second)
	assert.True(t, l.Allow("a"))
}

func TestAFloodOfKeysSendsNewKeysToOneOverflowBucket(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	l := newLimiter(c, 2, 10)

	for i := range 10 {
		assert.True(t, l.Allow(fmt.Sprintf("k%d", i)))
	}
	assert.True(t, l.Allow("new1"), "a newcomer is let in through the overflow bucket")
	assert.True(t, l.Allow("new2"), "...which every newcomer shares")
	assert.False(t, l.Allow("new3"), "...up to one key's limit")
	assert.False(t, l.Allow("new1"), "a newcomer isn't given a key of its own while the table is full")
	assert.True(t, l.Allow("k0"), "a key already live keeps its own allowance")

	c.advance(time.Minute + time.Second)
	assert.True(t, l.Allow("new3"), "the flood's keys are swept once they age out")
	assert.True(t, l.Allow("new3"))
	assert.False(t, l.Allow("new3"), "...and a newcomer has its own allowance again, not the overflow's")
}

func TestAFullTableIsLoggedOncePerWindow(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	var logs bytes.Buffer
	l := ratelimit.New(&ratelimit.Config{Limit: 5, Window: time.Minute, MaxKeys: 1, Now: c.now,
		Name: "login", Logger: slog.NewTextHandler(&logs, nil)})

	assert.True(t, l.Allow("a"))
	assert.Empty(t, logs.String())
	l.Allow("b")
	l.Allow("c")
	assert.Equal(t, 1, strings.Count(logs.String(), "key table full"))
	assert.Contains(t, logs.String(), "limiter=login")

	c.advance(30 * time.Second)
	l.Allow("d")
	assert.Equal(t, 1, strings.Count(logs.String(), "key table full"), "not again within the window")

	c.advance(31 * time.Second)
	l.Allow("a") // a is live again, and fills the table
	l.Allow("e")
	assert.Equal(t, 2, strings.Count(logs.String(), "key table full"), "again once a window has passed")
}

// The overflow bucket's attempts age out on their own schedule, while the
// table stays full.
func TestTheOverflowBucketSlidesWhileTheTableStaysFull(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	l := newLimiter(c, 1, 1)

	assert.True(t, l.Allow("a"))
	c.advance(30 * time.Second)
	assert.True(t, l.Allow("new1"), "through the overflow")
	assert.False(t, l.Allow("new2"), "the overflow is used up")

	c.advance(31 * time.Second) // a's attempt ages out and is swept
	assert.True(t, l.Allow("b"), "b takes the one slot")
	assert.False(t, l.Allow("new2"), "the overflow's attempt is still in its window")
	c.advance(30 * time.Second)
	assert.True(t, l.Allow("new2"), "...and then it isn't")
}

func TestANonPositiveSettingIsRefused(t *testing.T) {
	for name, cfg := range map[string]ratelimit.Config{
		"zero window":   {Limit: 1, Window: 0, MaxKeys: 1},
		"zero limit":    {Limit: 0, Window: time.Minute, MaxKeys: 1},
		"zero max keys": {Limit: 1, Window: time.Minute, MaxKeys: 0},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Panics(t, func() { ratelimit.New(&cfg) })
		})
	}
}

// Attempts arriving together must not each see room that only one of them
// has.
func TestConcurrentAttemptsShareOneAllowance(t *testing.T) {
	l := ratelimit.New(&ratelimit.Config{Limit: 5, Window: time.Hour, MaxKeys: 10})
	var allowed atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 100 {
		wg.Go(func() {
			<-start
			if l.Allow("a") {
				allowed.Add(1)
			}
		})
	}
	close(start)
	wg.Wait()
	assert.Equal(t, int32(5), allowed.Load())
}

type recordingObserver struct {
	mu         sync.Mutex
	refused    map[string]int
	overflowed map[string]int
}

func (o *recordingObserver) RateLimitRefused(limiter string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.refused[limiter]++
}

func (o *recordingObserver) RateLimitOverflowed(limiter string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.overflowed[limiter]++
}

// The observer sees each refusal and each attempt that went to the
// overflow bucket, allowed or not -- the overflow count is what says the
// table is full, whether or not anyone was refused.
func TestTheObserverIsToldOfRefusalsAndOverflow(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	obs := &recordingObserver{refused: map[string]int{}, overflowed: map[string]int{}}
	l := ratelimit.New(&ratelimit.Config{Name: "login", Limit: 1, Window: time.Minute, MaxKeys: 1, Now: c.now, Observer: obs})

	assert.True(t, l.Allow("a"))
	assert.False(t, l.Allow("a"))
	assert.Equal(t, map[string]int{"login": 1}, obs.refused, "a refusal in a key's own bucket")
	assert.Empty(t, obs.overflowed, "nothing has overflowed while the key had room")

	assert.True(t, l.Allow("b"), "the first newcomer is let in through the overflow bucket")
	assert.False(t, l.Allow("c"), "the second finds it spent")
	assert.Equal(t, map[string]int{"login": 2}, obs.overflowed)
	assert.Equal(t, map[string]int{"login": 2}, obs.refused)
}

// reentrantObserver calls back into its limiter, once per notification
// (a callback's own refusal would otherwise notify it again, forever).
type reentrantObserver struct {
	l      *ratelimit.Limiter
	inside bool
	calls  int
}

func (o *reentrantObserver) callBack() {
	if o.inside {
		return
	}
	o.inside = true
	defer func() { o.inside = false }()
	o.calls++
	o.l.Allow("from-the-observer")
}

func (o *reentrantObserver) RateLimitRefused(string)    { o.callBack() }
func (o *reentrantObserver) RateLimitOverflowed(string) { o.callBack() }

// The observer is told after the limiter's lock is released, so it may be
// slow, or even call back in, without holding up every other caller.
func TestAnObserverMayCallBackIn(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	obs := &reentrantObserver{}
	obs.l = ratelimit.New(&ratelimit.Config{Name: "login", Limit: 1, Window: time.Minute, MaxKeys: 1, Now: c.now, Observer: obs})

	done := make(chan struct{})
	go func() {
		defer close(done)
		obs.l.Allow("a")
		obs.l.Allow("a")
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Allow deadlocked calling its observer")
	}
	assert.Equal(t, 1, obs.calls, "told once, of the refusal")
}
