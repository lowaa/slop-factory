package conformance

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

var feedTests = []test{
	{"Feed/OrderedWithCursors", testFeedOrdered},
	{"Feed/HistoryFromTheZeroCursor", testFeedHistory},
	{"Feed/ResumeAfterARestart", testFeedResume},
	{"Feed/ASlowConsumerMissesNothing", testFeedSlowConsumer},
	{"Feed/ConcurrentWritersMissNothing", testFeedConcurrentWriters},
	{"Feed/UnblockedWhenTheLastBlockerIsDone", testFeedUnblocked},
	{"Feed/NoOpsAndRefusalsRecordNothing", testFeedNoOps},
	{"Feed/Lifecycle", testFeedLifecycle},
	{"Feed/StopWhileTheConsumerIsAway", testFeedStopWhileBlocked},
}

// seen is an event without its cursor and time, for comparing.
type seen struct {
	Kind   tickets.EventKind
	Ticket ident.TicketID
	By     ident.Principal
}

func seenOf(evs []tickets.TicketEvent) []seen {
	out := []seen{}
	for _, ev := range evs {
		out = append(out, seen{ev.Kind, ev.Ticket, ev.By})
	}
	return out
}

func start(t *testing.T, sys tickets.TicketingSystem, after tickets.Cursor) <-chan tickets.TicketEvent {
	t.Helper()
	require.NoError(t, sys.Start(t.Context(), after))
	return sys.Events()
}

// next blocks for the next event. A feed that never sends hangs the test
// until go test's own timeout: no wall-clock wait stands in for "nothing
// came".
func next(t *testing.T, ch <-chan tickets.TicketEvent) tickets.TicketEvent {
	t.Helper()
	ev, ok := <-ch
	require.True(t, ok, "the feed closed")
	return ev
}

func take(t *testing.T, ch <-chan tickets.TicketEvent, n int) []tickets.TicketEvent {
	t.Helper()
	out := make([]tickets.TicketEvent, n)
	for i := range out {
		out[i] = next(t, ch)
	}
	return out
}

// marker proves absence without waiting: a comment on a ticket of its own,
// written after the writes under test, so every event they made comes
// before its event.
type marker struct {
	f  *fixture
	id ident.TicketID
}

func newMarker(f *fixture) *marker { return &marker{f: f, id: f.ticket("marker").ID} }

// upTo writes the mark and returns every event before it.
func (m *marker) upTo(ch <-chan tickets.TicketEvent) []tickets.TicketEvent {
	m.f.t.Helper()
	_, err := m.f.sys.Comment(m.f.ctx, user, m.id, "mark")
	require.NoError(m.f.t, err)
	var out []tickets.TicketEvent
	for {
		ev := next(m.f.t, ch)
		if ev.Ticket == m.id && ev.Kind == tickets.EventCommented {
			return out
		}
		out = append(out, ev)
	}
}

func assertOrdered(t *testing.T, evs []tickets.TicketEvent) {
	t.Helper()
	for i := 1; i < len(evs); i++ {
		assert.Less(t, evs[i-1].Cursor, evs[i].Cursor, "cursors increase, as strings")
	}
}

func testFeedOrdered(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	ch := start(t, f.sys, "")
	a := f.agent("builder-1") // created, then one update to in_progress
	work := f.ticket("Work")
	_, err := f.sys.Comment(f.ctx, a, work.ID, "hello")
	require.NoError(t, err)
	_, err = f.sys.Update(f.ctx, user, work.ID, &tickets.TicketChange{Title: ptr("Renamed")})
	require.NoError(t, err)
	_, err = f.sys.Assign(f.ctx, user, work.ID, "aaron")
	require.NoError(t, err)
	_, err = f.sys.AddBlocker(f.ctx, a, a.Ticket, work.ID)
	require.NoError(t, err)
	filed := f.filedBy(a, "follow-up")

	evs := take(t, ch, 8)
	assert.Equal(t, []seen{
		{tickets.EventCreated, a.Ticket, user},
		{tickets.EventUpdated, a.Ticket, user},
		{tickets.EventCreated, work.ID, user},
		{tickets.EventCommented, work.ID, a},
		{tickets.EventUpdated, work.ID, user},
		{tickets.EventAssigned, work.ID, user},
		{tickets.EventBlockersChanged, a.Ticket, a},
		{tickets.EventCreated, filed.ID, a},
	}, seenOf(evs))
	assertOrdered(t, evs)
	for _, ev := range evs {
		assert.NotEmpty(t, ev.Cursor)
		assert.False(t, ev.At.IsZero())
	}
}

func testFeedHistory(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	first, second := f.ticket("first"), f.ticket("second")
	ch := start(t, f.sys, "")
	assert.Equal(t, []seen{
		{tickets.EventCreated, first.ID, user},
		{tickets.EventCreated, second.ID, user},
	}, seenOf(take(t, ch, 2)), "a zero cursor starts at the first event")
}

// A restart resumes just after the stored cursor, and an older cursor
// replays: delivery is at least once.
func testFeedResume(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	work := f.ticket("Work")
	for i := range 4 {
		_, err := f.sys.Comment(f.ctx, user, work.ID, fmt.Sprintf("comment %d", i))
		require.NoError(t, err)
	}
	first := take(t, start(t, f.sys, ""), 3)
	require.NoError(t, f.sys.Stop(f.ctx))

	restarted := f.backend.Open(t)
	ch := start(t, restarted, first[2].Cursor)
	rest := take(t, ch, 2)
	assert.Equal(t, []seen{{tickets.EventCommented, work.ID, user}, {tickets.EventCommented, work.ID, user}}, seenOf(rest))
	assertOrdered(t, append(first, rest...))
	_, err := restarted.Comment(f.ctx, user, work.ID, "after the restart")
	require.NoError(t, err)
	live := next(t, ch)
	assert.Equal(t, tickets.EventCommented, live.Kind)
	assert.Greater(t, live.Cursor, rest[1].Cursor)
	require.NoError(t, restarted.Stop(f.ctx))

	replayed := take(t, start(t, f.backend.Open(t), first[0].Cursor), 5)
	assert.Equal(t, append(first[1:], append(rest, live)...), replayed,
		"the same events with the same cursors")
}

// Writers are never held up by a consumer that isn't reading, and nothing
// they write is lost: it is all delivered, in order, once the consumer
// reads. Several times any batch an implementation might hold in memory.
func testFeedSlowConsumer(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	ch := start(t, f.sys, "")
	work := f.ticket("Work")
	const n = 1000
	// The writes complete with nobody reading: if a writer waited on the
	// consumer, this would deadlock.
	for i := range n {
		_, err := f.sys.Comment(f.ctx, user, work.ID, fmt.Sprintf("comment %d", i))
		require.NoError(t, err)
	}
	evs := take(t, ch, n+1)
	assert.Equal(t, tickets.EventCreated, evs[0].Kind)
	for _, ev := range evs[1:] {
		assert.Equal(t, seen{tickets.EventCommented, work.ID, user}, seenOf([]tickets.TicketEvent{ev})[0])
	}
	assertOrdered(t, evs)
	assert.Len(t, f.comments(work.ID), n)
}

func testFeedConcurrentWriters(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	ch := start(t, f.sys, "")
	const writers, each = 4, 50
	work := make([]*tickets.Ticket, writers)
	for i := range work {
		work[i] = f.ticket(fmt.Sprintf("writer %d", i))
	}
	created := take(t, ch, writers)
	assertOrdered(t, created)

	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		wg.Go(func() {
			for j := range each {
				if _, err := f.sys.Comment(f.ctx, user, work[i].ID, fmt.Sprintf("%d", j)); err != nil {
					errs[i] = err
					return
				}
			}
		})
	}
	// Read while they write.
	evs := take(t, ch, writers*each)
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	assertOrdered(t, evs)
	per := map[ident.TicketID]int{}
	for _, ev := range evs {
		per[ev.Ticket]++
	}
	for _, w := range work {
		assert.Equal(t, each, per[w.ID], "every write by %s, once", w.Title)
	}
}

func testFeedUnblocked(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	work, b1, b2 := f.ticket("Work"), f.ticket("b1"), f.ticket("b2")
	held, b3 := f.ticket("Held"), f.ticket("b3")
	f.blockedBy(work.ID, b1.ID)
	f.blockedBy(work.ID, b2.ID)
	f.blockedBy(held.ID, b3.ID)
	m := newMarker(f)
	ch := start(t, f.sys, "")
	m.upTo(ch)

	f.reach(b1.ID, tickets.StatusDone)
	assert.Equal(t, []seen{
		{tickets.EventUpdated, b1.ID, user},
		{tickets.EventUpdated, b1.ID, user},
		{tickets.EventBlockersChanged, work.ID, user},
	}, seenOf(m.upTo(ch)), "one blocker of two done: changed, still blocked")

	f.reach(b2.ID, tickets.StatusDone)
	assert.Equal(t, []seen{
		{tickets.EventUpdated, b2.ID, user},
		{tickets.EventUpdated, b2.ID, user},
		{tickets.EventBlockersChanged, work.ID, user},
		{tickets.EventUnblocked, work.ID, user},
	}, seenOf(m.upTo(ch)), "the last one done: unblocked")

	f.reach(b3.ID, tickets.StatusCancelled)
	assert.Equal(t, []seen{{tickets.EventUpdated, b3.ID, user}}, seenOf(m.upTo(ch)),
		"a cancelled blocker unblocks nothing")
	_, err := f.sys.RemoveBlocker(f.ctx, user, held.ID, b3.ID)
	require.NoError(t, err)
	assert.Equal(t, []seen{
		{tickets.EventBlockersChanged, held.ID, user},
		{tickets.EventUnblocked, held.ID, user},
	}, seenOf(m.upTo(ch)), "a person lifting the last one unblocks it")

	f.reach(b1.ID, tickets.StatusOpen)
	assert.Equal(t, []seen{
		{tickets.EventUpdated, b1.ID, user},
		{tickets.EventBlockersChanged, work.ID, user},
	}, seenOf(m.upTo(ch)), "a reopened blocker blocks again")
}

func testFeedNoOps(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	a := f.agent("builder-1")
	work, blocker := f.ticket("Work"), f.ticket("Blocker")
	f.blockedBy(work.ID, blocker.ID)
	_, err := f.sys.Assign(f.ctx, user, work.ID, "aaron")
	require.NoError(t, err)
	m := newMarker(f)
	ch := start(t, f.sys, "")
	m.upTo(ch)

	_, err = f.sys.Update(f.ctx, user, work.ID, &tickets.TicketChange{Title: ptr("Work"), Status: ptr(tickets.StatusOpen)})
	require.NoError(t, err)
	_, err = f.sys.Update(f.ctx, user, work.ID, &tickets.TicketChange{})
	require.NoError(t, err)
	_, err = f.sys.Assign(f.ctx, user, work.ID, "aaron")
	require.NoError(t, err)
	_, err = f.sys.AddBlocker(f.ctx, user, work.ID, blocker.ID)
	require.NoError(t, err)
	_, err = f.sys.RemoveBlocker(f.ctx, user, blocker.ID, work.ID)
	require.NoError(t, err)
	_, err = f.sys.Update(f.ctx, a, a.Ticket, &tickets.TicketChange{Title: ptr("x"), AutoMerge: ptr(true)})
	require.Error(t, err)
	_, err = f.sys.AddBlocker(f.ctx, user, blocker.ID, work.ID)
	require.ErrorIs(t, err, tickets.ErrCycle)

	assert.Empty(t, m.upTo(ch), "nothing that changed nothing is on the feed")
}

func testFeedLifecycle(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	never := f.backend.Open(t)
	require.NoError(t, never.Stop(f.ctx), "stopping an unstarted system")
	_, ok := <-never.Events()
	assert.False(t, ok, "Events is closed after Stop, started or not")
	require.NoError(t, never.Stop(f.ctx), "Stop again")
	require.ErrorIs(t, never.Start(f.ctx, ""), tickets.ErrStopped)

	require.ErrorIs(t, f.sys.Start(f.ctx, "not a cursor"), tickets.ErrInvalidCursor)
	ch := start(t, f.sys, "")
	assert.Equal(t, ch, f.sys.Events(), "the same channel before and after Start")
	require.ErrorIs(t, f.sys.Start(f.ctx, ""), tickets.ErrAlreadyStarted)
	require.NoError(t, f.sys.Stop(f.ctx))
	_, ok = <-ch
	assert.False(t, ok)
	require.NoError(t, f.sys.Stop(f.ctx))
}

// Stop must not wait on a consumer that has gone: the feed is mid-send,
// with history it hasn't delivered.
func testFeedStopWhileBlocked(t *testing.T, nb NewBackend) {
	f := newFixture(t, nb)
	for range 3 {
		f.ticket("unread")
	}
	ch := start(t, f.sys, "")
	next(t, ch) // the feed is now running, and blocked on the next send
	require.NoError(t, f.sys.Stop(f.ctx))
	for range ch {
		// An implementation may have buffered some before Stop. The loop
		// ends only once Events is closed.
	}
}
