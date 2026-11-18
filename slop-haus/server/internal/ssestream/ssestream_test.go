package ssestream_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2/sse"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kclock "github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ssestream"
)

var defaults = ssestream.Options{
	PollInterval: time.Second,
	MaxLifetime:  5 * time.Minute,
	PerUser:      2,
	Total:        3,
	BusyRetry:    30 * time.Second,
}

func TestSlots_CapPerUserAndInTotal(t *testing.T) {
	t.Parallel()
	slots := ssestream.NewSlots(1, 2)
	a, b, c := uuid.New(), uuid.New(), uuid.New()

	releaseA, ok := slots.Acquire(a)
	require.True(t, ok)
	_, ok = slots.Acquire(a)
	assert.False(t, ok, "second slot for the same user")

	_, ok = slots.Acquire(b)
	require.True(t, ok)
	_, ok = slots.Acquire(c)
	assert.False(t, ok, "over the total")

	releaseA()
	_, ok = slots.Acquire(c)
	assert.True(t, ok, "a released slot is reusable")
}

func TestSlots_ReleaseIsIdempotent(t *testing.T) {
	t.Parallel()
	slots := ssestream.NewSlots(1, 1)
	user := uuid.New()
	release, ok := slots.Acquire(user)
	require.True(t, ok)
	release()
	release()

	_, ok = slots.Acquire(user)
	require.True(t, ok)
	_, ok = slots.Acquire(uuid.New())
	assert.False(t, ok, "a double release must not free a second slot")
}

func TestOptionsWithDefaults(t *testing.T) {
	t.Parallel()
	got := ssestream.Options{PerUser: 9}.WithDefaults(defaults)
	assert.Equal(t, 9, got.PerUser)
	assert.Equal(t, defaults.Total, got.Total)
	assert.Equal(t, defaults.PollInterval, got.PollInterval)
	assert.Equal(t, sse.WriteTimeout/2, got.KeepAlive)
	assert.NotNil(t, got.Clock)
}

// A stream lives as long as the access token that opened it, so a session
// that has ended stops getting events once its token would have expired.
func TestStreamDeadline_IsTheTokensExpiry(t *testing.T) {
	t.Parallel()
	s := ssestream.New(ssestream.Options{MaxLifetime: 5 * time.Minute}.WithDefaults(defaults), "test")
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	cases := map[string]struct {
		issued time.Time
		want   time.Time
	}{
		"a fresh token":              {issued: now, want: now.Add(5 * time.Minute)},
		"a token half way through":   {issued: now.Add(-2 * time.Minute), want: now.Add(3 * time.Minute)},
		"a token about to expire":    {issued: now.Add(-5*time.Minute + time.Second), want: now.Add(time.Second)},
		"no issue time on the token": {want: now.Add(5 * time.Minute)},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, c.want, s.Deadline(&authn.User{IssuedAt: c.issued}, now))
		})
	}
}

// recorder is an sse.Sender that keeps what it was sent.
type recorder struct {
	mu       sync.Mutex
	messages []sse.Message
	comments []string
	failOn   string
}

func (r *recorder) sender() sse.Sender {
	return sse.Sender(func(m sse.Message) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.failOn != "" && m.Comment == r.failOn {
			return errors.New("write failed")
		}
		r.messages = append(r.messages, m)
		if m.Comment != "" {
			r.comments = append(r.comments, m.Comment)
		}
		return nil
	})
}

func (r *recorder) keepalives() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.comments {
		if c == "keepalive" {
			n++
		}
	}
	return n
}

func TestOpen_RefusesWithARetryWhenFull(t *testing.T) {
	t.Parallel()
	s := ssestream.New(ssestream.Options{PerUser: 1, BusyRetry: time.Minute}.WithDefaults(defaults), "test")
	user := &authn.User{ID: uuid.New()}
	rec := &recorder{}

	_, release, ok := s.Open(context.Background(), user, rec.sender())
	require.True(t, ok)
	_, _, ok = s.Open(context.Background(), user, rec.sender())
	require.False(t, ok)
	require.Len(t, rec.messages, 1)
	assert.Equal(t, 60000, rec.messages[0].Retry)
	assert.Equal(t, "too many test streams open", rec.messages[0].Comment)

	release()
	_, release, ok = s.Open(context.Background(), user, rec.sender())
	assert.True(t, ok, "release gave the slot back")
	release()
}

func TestOpen_EndsTheContextWithTheTokenAndFreesTheSlot(t *testing.T) {
	t.Parallel()
	fake := kclock.NewFake(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	s := ssestream.New(ssestream.Options{MaxLifetime: time.Minute, PerUser: 1, Clock: fake}.WithDefaults(defaults), "test")
	user := &authn.User{ID: uuid.New(), IssuedAt: fake.Now()}

	ctx, release, ok := s.Open(context.Background(), user, (&recorder{}).sender())
	require.True(t, ok)
	fake.BlockUntil(1)
	fake.Advance(time.Minute)
	<-ctx.Done()

	release()
	_, release, ok = s.Open(context.Background(), user, (&recorder{}).sender())
	assert.True(t, ok)
	release()
}

// loopRig runs Loop on a fake clock, one tick per call to step.
type loopRig struct {
	fake  *kclock.Fake
	rec   *recorder
	ticks chan struct{}
	done  chan struct{}
}

func startLoop(t *testing.T, opts ssestream.Options, closed <-chan struct{}, tick func(context.Context) (bool, bool)) *loopRig {
	t.Helper()
	fake := kclock.NewFake(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	opts.Clock = fake
	s := ssestream.New(opts.WithDefaults(defaults), "test")
	r := &loopRig{fake: fake, rec: &recorder{}, ticks: make(chan struct{}, 100), done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		defer close(r.done)
		s.Loop(ctx, r.rec.sender(), closed, func(ctx context.Context) (bool, bool) {
			// Signalled on entry: the next tick starting proves the previous
			// iteration, keepalive included, has finished.
			r.ticks <- struct{}{}
			return tick(ctx)
		})
	}()
	return r
}

func (r *loopRig) awaitTick(t *testing.T) {
	t.Helper()
	select {
	case <-r.ticks:
	case <-time.After(5 * time.Second):
		t.Fatal("no tick")
	}
}

func (r *loopRig) awaitEnd(t *testing.T) {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not end")
	}
}

func TestLoop_PollsOnTheIntervalAndStopsWhenTold(t *testing.T) {
	t.Parallel()
	n := 0
	r := startLoop(t, ssestream.Options{PollInterval: time.Second, KeepAlive: time.Hour}, nil, func(context.Context) (bool, bool) {
		n++
		return false, n == 3
	})
	r.awaitTick(t) // the first poll is immediate
	r.fake.BlockUntil(1)
	r.fake.Advance(time.Second)
	r.awaitTick(t)
	r.fake.Advance(time.Second)
	r.awaitTick(t)
	r.awaitEnd(t)
	assert.Equal(t, 3, n)
}

func TestLoop_KeepsAQuietStreamAliveButNotOneThatKeepsWriting(t *testing.T) {
	t.Parallel()
	var writing atomic.Bool
	r := startLoop(t, ssestream.Options{PollInterval: time.Second, KeepAlive: 3 * time.Second}, nil, func(context.Context) (bool, bool) {
		return writing.Load(), false
	})
	r.awaitTick(t)
	r.fake.BlockUntil(1)
	for range 3 {
		r.fake.Advance(time.Second)
		r.awaitTick(t)
	}
	r.fake.Advance(time.Second)
	r.awaitTick(t)
	assert.Equal(t, 1, r.rec.keepalives(), "quiet for KeepAlive")

	writing.Store(true)
	for range 3 {
		r.fake.Advance(time.Second)
		r.awaitTick(t)
	}
	r.fake.Advance(time.Second)
	r.awaitTick(t)
	assert.Equal(t, 1, r.rec.keepalives(), "writes defer the keepalive")
}

func TestLoop_EndsOnClosedAndOnAFailedKeepalive(t *testing.T) {
	t.Parallel()
	closed := make(chan struct{})
	r := startLoop(t, ssestream.Options{KeepAlive: time.Hour}, closed, func(context.Context) (bool, bool) { return false, false })
	r.awaitTick(t)
	close(closed)
	r.awaitEnd(t)

	r = startLoop(t, ssestream.Options{PollInterval: time.Second, KeepAlive: time.Second}, nil, func(context.Context) (bool, bool) { return false, false })
	r.rec.mu.Lock()
	r.rec.failOn = "keepalive"
	r.rec.mu.Unlock()
	r.awaitTick(t)
	r.fake.BlockUntil(1)
	r.fake.Advance(time.Second)
	r.awaitEnd(t)
}
