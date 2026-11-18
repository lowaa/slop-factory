package wakeup_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/wakeup"
)

type fakeSlots struct {
	mu  sync.Mutex
	ids []uuid.UUID
}

func (f *fakeSlots) SlotIDs(context.Context, uuid.UUID) ([]uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uuid.UUID(nil), f.ids...), nil
}

func (f *fakeSlots) set(ids ...uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids = ids
}

func (r *rig) supervisorWaiter(slots wakeup.Slots) *wakeup.Waiter {
	o := r.opts
	o.Clock = r.clock
	return wakeup.New(wakeup.Deps{
		Tickets: r.state, Messages: r.state, Policy: r.state,
		LockStates: r.state, Polls: wakeup.NewPostgresStore(r.db), Slots: slots,
	}, &o, slog.DiscardHandler)
}

func (r *rig) supervisorPoll(t *testing.T, w *wakeup.Waiter, req *wakeup.WorkerPollRequest) *wakeup.WorkerPoll {
	t.Helper()
	got, err := w.SupervisorPoll(r.ctx(context.Background()), r.agent, req)
	require.NoError(t, err)
	return got
}

func TestSupervisorPoll_OneCallCoversEverySlot(t *testing.T) {
	r := newRig(t, nil)
	a, b := uuid.New(), uuid.New()
	r.state.peek = claimable(38)

	got := r.supervisorPoll(t, r.supervisorWaiter(&fakeSlots{ids: []uuid.UUID{a, b}}), &wakeup.WorkerPollRequest{Wait: time.Minute})

	assert.Equal(t, []*wakeup.Wake{
		{Reason: wakeup.ReasonClaimable, TicketID: 38, SlotID: a},
		{Reason: wakeup.ReasonClaimable, TicketID: 38, SlotID: b},
	}, got.Events, "each slot is told, by name")
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	assert.Equal(t, []uuid.UUID{a, b}, r.state.leaseFor, "each slot's own state is what is read, not the supervisor's")
	assert.Equal(t, []uuid.UUID{a, b}, r.state.peeked)
}

// Slots come and go while a poll is held: one added after the poll began is
// heard at the next look.
func TestSupervisorPoll_ASlotAddedWhileHeldIsHeard(t *testing.T) {
	r := newRig(t, nil)
	slots := &fakeSlots{}
	w := r.supervisorWaiter(slots)
	done := make(chan *wakeup.WorkerPoll, 1)
	go func() { done <- r.supervisorPoll(t, w, &wakeup.WorkerPollRequest{Wait: time.Minute}) }()
	r.tick(t)

	late := uuid.New()
	r.state.set(func(s *state) { s.peek = claimable(7) })
	slots.set(late)

	// The look the first tick started may already see the new slot and end
	// the poll, so further ticks are offered only until the poll answers.
	var got *wakeup.WorkerPoll
	for got == nil {
		select {
		case got = <-done:
		case r.clock.ticks <- time.Now():
		case <-time.After(5 * time.Second):
			t.Fatal("the poll never heard the added slot")
		}
	}
	require.Len(t, got.Events, 1)
	assert.Equal(t, late, got.Events[0].SlotID)
}

func TestSupervisorPoll_EachSlotWatchesItsOwnLease(t *testing.T) {
	r := newRig(t, nil)
	a, b, stranger := uuid.New(), uuid.New(), uuid.New()
	r.state.set(func(s *state) {
		s.leaseEndsFor = map[int64]*ticket.LeaseEnd{5: {TicketID: 11, Kind: ticket.LeaseFinished, Exit: ticket.ExitDone}}
	})
	req := &wakeup.WorkerPollRequest{SlotLeases: map[uuid.UUID]int64{a: 5, stranger: 99}}

	got := r.supervisorPoll(t, r.supervisorWaiter(&fakeSlots{ids: []uuid.UUID{a, b}}), req)

	require.Len(t, got.Events, 1, "slot b has nothing, and the stranger's lease is not asked after")
	assert.Equal(t, a, got.Events[0].SlotID)
	assert.Equal(t, int64(5), got.Events[0].LeaseID)
	assert.Equal(t, wakeup.ReasonFinished, got.Events[0].Reason)
	assert.Equal(t, []int64{5}, r.leaseIDsAsked(), "only a named slot's lease is looked up")
}

func TestSupervisorPoll_IsRecordedAsTheSupervisorAndNeedsNoSlots(t *testing.T) {
	r := newRig(t, nil)
	r.state.peek = claimable(38)

	for name, slots := range map[string]wakeup.Slots{"no slots": &fakeSlots{}, "no slot source": nil} {
		t.Run(name, func(t *testing.T) {
			w := r.supervisorWaiter(slots)
			done := make(chan *wakeup.WorkerPoll, 1)
			go func() { done <- r.supervisorPoll(t, w, &wakeup.WorkerPollRequest{Wait: time.Minute}) }()
			r.clock.deadline <- time.Now()
			assert.Empty(t, (<-done).Events)
		})
	}
	var polled int
	require.NoError(t, r.db.QueryRow(r.ctx(context.Background()), `SELECT count(*) FROM worker_polls WHERE agent_id = $1`, r.agent).Scan(&polled))
	assert.Equal(t, 1, polled, "the supervisor's own poll is what is recorded")
}

func (r *rig) leaseIDsAsked() []int64 {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	return append([]int64(nil), r.state.leaseIDsAsked...)
}
