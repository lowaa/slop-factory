package state_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/state"
)

func (f *fixture) appendEvent(t testing.TB, typ string) int64 {
	t.Helper()
	seq, err := f.Events.Append(context.Background(), &state.NewEvent{Type: state.EventType(typ)})
	require.NoError(t, err)
	return seq
}

func seqs(evs []state.Event) []int64 {
	out := make([]int64, len(evs))
	for i, e := range evs {
		out[i] = e.Seq
	}
	return out
}

func TestAppendAndReadAfter(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	seq, err := f.Events.Append(ctx, &state.NewEvent{
		Type: "agent.state", Agent: "a1", Ticket: "42", Payload: json.RawMessage(`{"to":"working"}`),
	})
	require.NoError(t, err)
	f.clock.Advance(time.Second)
	second := f.appendEvent(t, "ticket.updated")
	assert.Greater(t, second, seq)

	evs, err := f.Events.ReadAfter(ctx, 0, 0)
	require.NoError(t, err)
	require.Len(t, evs, 2)
	assert.Equal(t, state.Event{
		Seq: seq, At: t0, Type: "agent.state", Agent: "a1", Ticket: "42", Payload: json.RawMessage(`{"to":"working"}`),
	}, evs[0])
	assert.Equal(t, json.RawMessage(`{}`), evs[1].Payload, "no payload is an empty object")
	assert.Equal(t, t0.Add(time.Second), evs[1].At)

	evs, err = f.Events.ReadAfter(ctx, seq, 0)
	require.NoError(t, err)
	assert.Equal(t, []int64{second}, seqs(evs))

	_, err = f.Events.Append(ctx, &state.NewEvent{Type: "bad", Payload: json.RawMessage(`{not json`)})
	assert.ErrorContains(t, err, "CHECK")
}

func TestReadAfterPagesInOrder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	var all []int64
	for i := range 7 {
		all = append(all, f.appendEvent(t, fmt.Sprint("e", i)))
	}
	var got []int64
	for cursor := int64(0); ; {
		page, err := f.Events.ReadAfter(ctx, cursor, 3)
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		assert.LessOrEqual(t, len(page), 3)
		got = append(got, seqs(page)...)
		cursor = page[len(page)-1].Seq
	}
	assert.Equal(t, all, got)
}

func TestSeqIsNeverReusedAfterPruning(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.appendEvent(t, "a")
	last := f.appendEvent(t, "b")
	f.clock.Advance(time.Hour)
	_, err := f.Pruner.Prune(ctx, &state.PruneCutoffs{Events: f.clock.Now()})
	require.NoError(t, err)

	// A follower holding cursor `last` must not miss the next event because
	// it was given an old number.
	next := f.appendEvent(t, "c")
	assert.Greater(t, next, last)
	evs, err := f.Events.ReadAfter(ctx, last, 0)
	require.NoError(t, err)
	assert.Equal(t, []int64{next}, seqs(evs))
}

func TestFollowReturnsAtOnceWhenEventsAreWaiting(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	first := f.appendEvent(t, "a")
	second := f.appendEvent(t, "b")

	evs, err := f.Events.Follow(context.Background(), 0, 0)
	require.NoError(t, err)
	assert.Equal(t, []int64{first, second}, seqs(evs))
	evs, err = f.Events.Follow(context.Background(), first, 1)
	require.NoError(t, err)
	assert.Equal(t, []int64{second}, seqs(evs))
}

// TestFollowWakesForAnAppendInTheWaitWindow appends at the worst moment:
// after the follower found nothing and before it waits. A follower that
// took its wake-up channel after reading would sleep through it, and this
// test would hang.
func TestFollowWakesForAnAppendInTheWaitWindow(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var seq int64
	var once sync.Once
	state.SetBeforeWait(f.Events, func() {
		once.Do(func() { seq = f.appendEvent(t, "racer") })
	})

	evs, err := f.Events.Follow(t.Context(), 0, 0)
	require.NoError(t, err)
	assert.Equal(t, []int64{seq}, seqs(evs))
}

func TestFollowBlocksUntilAnAppend(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	waiting := make(chan struct{})
	var once sync.Once
	state.SetBeforeWait(f.Events, func() { once.Do(func() { close(waiting) }) })

	type result struct {
		evs []state.Event
		err error
	}
	got := make(chan result, 1)
	go func() {
		evs, err := f.Events.Follow(t.Context(), 0, 0)
		got <- result{evs, err}
	}()
	<-waiting
	select {
	case r := <-got:
		t.Fatalf("Follow returned with nothing appended: %v", r)
	default:
	}
	seq := f.appendEvent(t, "wake")
	r := <-got
	require.NoError(t, r.err)
	assert.Equal(t, []int64{seq}, seqs(r.evs))
}

func TestFollowEndsWithItsContextOrClose(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	waiting := make(chan struct{}, 2)
	state.SetBeforeWait(f.Events, func() { waiting <- struct{}{} })

	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 2)
	go func() {
		_, err := f.Events.Follow(ctx, 0, 0)
		errs <- err
	}()
	go func() {
		_, err := f.Events.Follow(context.Background(), 0, 0)
		errs <- err
	}()
	<-waiting
	<-waiting

	cancel()
	assert.ErrorIs(t, <-errs, context.Canceled)
	f.Events.Close()
	assert.ErrorIs(t, <-errs, state.ErrClosed)
	_, err := f.Events.Follow(context.Background(), 0, 0)
	assert.ErrorIs(t, err, state.ErrClosed, "and from then on")
	f.Events.Close() // idempotent
}

// TestFollowersSeeEveryEventInOrderUnderConcurrentAppends is the e2e
// synchronisation contract: several followers, resuming from their own
// cursors in small pages, each see every event once, in seq order, while
// several writers append at once.
func TestFollowersSeeEveryEventInOrderUnderConcurrentAppends(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	const writers, perWriter, followers = 4, 25, 3
	const total = writers * perWriter

	seen := make([][]int64, followers)
	var fwg sync.WaitGroup
	for i := range followers {
		fwg.Go(func() {
			var cursor int64
			for len(seen[i]) < total {
				evs, err := f.Events.Follow(t.Context(), cursor, 7)
				if !assert.NoError(t, err) {
					return
				}
				seen[i] = append(seen[i], seqs(evs)...)
				cursor = evs[len(evs)-1].Seq
			}
		})
	}

	appended := make([][]int64, writers)
	var wwg sync.WaitGroup
	for w := range writers {
		wwg.Go(func() {
			for i := range perWriter {
				seq, err := f.Events.Append(context.Background(), &state.NewEvent{Type: state.EventType(fmt.Sprintf("w%d-%d", w, i))})
				if !assert.NoError(t, err) {
					return
				}
				appended[w] = append(appended[w], seq)
			}
		})
	}
	wwg.Wait()
	fwg.Wait()

	var want []int64
	for _, a := range appended {
		want = append(want, a...)
	}
	assert.Len(t, want, total)
	for i := range followers {
		assert.ElementsMatch(t, want, seen[i], "follower %d", i)
		assert.IsIncreasing(t, seen[i], "follower %d", i)
	}
}

// TestFollowReportsErrClosedWhenCloseLandsBeforeItsRead closes the state
// (followers, then the database) after a follower checked for Close but
// before it read: it must see ErrClosed, not the driver's closed-database
// error.
func TestFollowReportsErrClosedWhenCloseLandsBeforeItsRead(t *testing.T) {
	t.Parallel()
	s, err := state.Open(context.Background(), &state.Options{
		Path: t.TempDir() + "/state.db", Logger: discardLogger(), Clock: newFakeClock(),
	})
	require.NoError(t, err)
	state.SetBeforeRead(s.Events, func() { _ = s.Close() })

	_, err = s.Events.Follow(context.Background(), 0, 0)
	assert.ErrorIs(t, err, state.ErrClosed)
}
