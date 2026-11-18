package conductor_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/conductor"
)

// pollInBackground starts a poll that waits, and returns where its answer
// will arrive.
func (f *fixture) pollInBackground(svc *conductor.Service, h *host) <-chan polled {
	out := make(chan polled, 1)
	go func() {
		res, err := svc.Poll(f.ctx, h.proof, &conductor.PollRequest{HostID: h.id, Wait: time.Hour})
		out <- polled{res, err}
	}()
	return out
}

// lookouts reports each time a poll has finished reading the queue. A poll
// that has been ticked reads the queue again before it parks, so a test that
// changes the world straight after clock.tick races that read: a change made
// before it is taken for work found, and the poll returns without the next
// tick the test then waits to give it.
type lookouts struct {
	conductor.Store
	looks chan struct{}
	seen  int
	ticks int
}

func (l *lookouts) ReadWork(ctx context.Context, conductorID, hostID uuid.UUID, limit int) ([]conductor.WorkItem, error) {
	defer func() { l.looks <- struct{}{} }()
	return l.Store.ReadWork(ctx, conductorID, hostID, limit)
}

// newWatchedService is a service whose polls can be waited on with
// tickAndLook.
func (f *fixture) newWatchedService(clock *manualClock) (*conductor.Service, *lookouts) {
	l := &lookouts{Store: conductor.NewPostgresStore(f.database), looks: make(chan struct{}, 64)}
	return f.newService(&conductor.Options{Clock: clock, Store: l}), l
}

// forget drops the reads so far, for a test that polled before the poll it
// goes on to tick.
func (l *lookouts) forget() {
	for len(l.looks) > 0 {
		<-l.looks
	}
	l.seen, l.ticks = 0, 0
}

// tickAndLook makes one polling interval pass and returns once the poll has
// read the queue for it, so what the test does next is seen by the poll's
// next read, not this one.
func (l *lookouts) tickAndLook(t *testing.T, clock *manualClock) {
	t.Helper()
	clock.tick(t)
	l.ticks++
	for l.seen < l.ticks+1 { // the read before the first tick, and one per tick
		select {
		case <-l.looks:
			l.seen++
		case <-time.After(10 * time.Second):
			t.Fatal("the poll did not read the queue")
		}
	}
}

type polled struct {
	res *conductor.PollResult
	err error
}

func awaitPoll(t *testing.T, ch <-chan polled) polled {
	t.Helper()
	select {
	case p := <-ch:
		return p
	case <-time.After(10 * time.Second):
		t.Fatal("the poll did not return")
		return polled{}
	}
}

func TestAPollHeldOpenReturnsWhenWorkArrives(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	clock := newManualClock()
	svc, looks := f.newWatchedService(clock)
	t.Cleanup(svc.Close)
	h := f.host(t, owned)
	answer := f.pollInBackground(svc, h)

	// The poll has looked and found nothing: it takes the tick only from its wait.
	looks.tickAndLook(t, clock)
	select {
	case <-answer:
		t.Fatal("the poll returned with nothing queued")
	default:
	}
	f.start(t, owned)
	clock.tick(t)

	got := awaitPoll(t, answer)
	require.NoError(t, got.err)
	assert.Equal(t, conductor.PollWork, got.res.Reason)
	assert.Equal(t, []conductor.WorkKind{conductor.WorkStart}, kinds(got.res.Items))
}

// The two-replica rule: nothing a wait needs is in the process that serves
// it. The message goes in through one api process and reaches a host polling
// another.
func TestAMessagePostedThroughOneReplicaReachesAHostPollingAnother(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	clock := newManualClock()
	hostSide, looks := f.newWatchedService(clock)
	t.Cleanup(hostSide.Close)
	ownerSide := f.newService(nil)
	t.Cleanup(ownerSide.Close)
	h := f.host(t, owned)
	first, err := hostSide.Poll(f.ctx, h.proof, &conductor.PollRequest{HostID: h.id})
	require.NoError(t, err)
	acked := first.Items[len(first.Items)-1].Seq
	looks.forget()
	answer := make(chan polled, 1)
	go func() {
		res, err := hostSide.Poll(f.ctx, h.proof, &conductor.PollRequest{HostID: h.id, Ack: acked, Wait: time.Hour})
		answer <- polled{res, err}
	}()
	looks.tickAndLook(t, clock)

	_, err = ownerSide.Send(f.ctx, owned, c.ID, &conductor.SendRequest{Text: "through the other replica"})
	require.NoError(t, err)
	clock.tick(t)

	got := awaitPoll(t, answer)
	require.NoError(t, got.err)
	require.Len(t, got.res.Items, 1)
	assert.Equal(t, "through the other replica", got.res.Items[0].Text)
}

func TestAPollWithNothingToDoTimesOut(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	clock := newManualClock()
	svc := f.newService(&conductor.Options{Clock: clock})
	t.Cleanup(svc.Close)
	answer := f.pollInBackground(svc, f.host(t, owned))
	clock.tick(t)

	clock.deadline <- time.Now()

	got := awaitPoll(t, answer)
	require.NoError(t, got.err)
	assert.Equal(t, conductor.PollTimeout, got.res.Reason)
	assert.Empty(t, got.res.Items)
}

func TestShuttingDownAnswersAWaitingPollWithRestart(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	clock := newManualClock()
	svc := f.newService(&conductor.Options{Clock: clock})
	h := f.host(t, owned)
	answer := f.pollInBackground(svc, h)
	clock.tick(t)

	svc.Close()

	got := awaitPoll(t, answer)
	require.NoError(t, got.err)
	assert.Equal(t, conductor.PollRestart, got.res.Reason)
	later, err := svc.Poll(f.ctx, h.proof, &conductor.PollRequest{HostID: h.id, Wait: time.Hour})
	require.NoError(t, err)
	assert.Equal(t, conductor.PollRestart, later.Reason, "and so is every poll after it, at once")
}

func TestAPollWhoseCallerGivesUpEndsWithItsError(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	clock := newManualClock()
	svc := f.newService(&conductor.Options{Clock: clock})
	t.Cleanup(svc.Close)
	h := f.host(t, owned)
	ctx, cancel := context.WithCancel(f.ctx)
	answer := make(chan polled, 1)
	go func() {
		res, err := svc.Poll(ctx, h.proof, &conductor.PollRequest{HostID: h.id, Wait: time.Hour})
		answer <- polled{res, err}
	}()
	clock.tick(t)

	cancel()

	got := awaitPoll(t, answer)
	assert.ErrorIs(t, got.err, context.Canceled)
}

func TestAPollWithNoWaitLooksOnce(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	clock := newManualClock() // never ticks: waiting at all would hang
	svc := f.newService(&conductor.Options{Clock: clock})
	t.Cleanup(svc.Close)

	h := f.host(t, owned)

	res, err := svc.Poll(f.ctx, h.proof, &conductor.PollRequest{HostID: h.id})

	require.NoError(t, err)
	assert.Equal(t, conductor.PollTimeout, res.Reason)
}
