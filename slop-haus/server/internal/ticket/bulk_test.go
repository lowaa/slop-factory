package ticket_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// Bulk moves against real Postgres: the same rules as a single move, and
// all or nothing.

func (f *fixture) moveMany(t *testing.T, status ticket.Status, ids ...int64) []ticket.Ticket {
	t.Helper()
	moved, err := f.svc.MoveMany(f.ctx, ids, status)
	require.NoError(t, err)
	return moved
}

func refusalsOf(t *testing.T, err error) []ticket.Refusal {
	t.Helper()
	var refused *ticket.BatchRefusedError
	require.ErrorAs(t, err, &refused)
	return refused.Refusals
}

func TestBulkMove_MovesEveryTicketAndReturnsThemInOrder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b := f.preparing(t, "First"), f.ticket(t, "Second")

	moved := f.moveMany(t, ticket.StatusParked, b.ID, a.ID)

	require.Len(t, moved, 2)
	assert.Equal(t, []int64{b.ID, a.ID}, []int64{moved[0].ID, moved[1].ID})
	for _, m := range moved {
		assert.Equal(t, ticket.StatusParked, m.Status)
		assert.Equal(t, ticket.StatusParked, f.status(t, m.ID), "stored, not just echoed")
	}
}

// One refusal and nothing moves -- not even the tickets named before it,
// whose writes the transaction has already made. Every refusal is named,
// not just the first.
//
// Verified: with MoveTickets committing despite the refusals, the open
// tickets are left parked and this fails.
func TestBulkMove_OneRefusalMovesNone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder")
	first := f.ticket(t, "Parkable before", "a/")
	held := f.ticket(t, "Held", "b/")
	_, err := f.svc.Claim(f.ctx, a, &held.ID)
	require.NoError(t, err)
	require.Equal(t, ticket.StatusInProgress, f.status(t, held.ID))
	last := f.ticket(t, "Parkable after", "c/")
	before := f.get(t, first.ID).UpdatedAt

	_, err = f.svc.MoveMany(f.ctx, []int64{first.ID, held.ID, last.ID, 999}, ticket.StatusParked)

	refusals := refusalsOf(t, err)
	require.Len(t, refusals, 2)
	assert.Equal(t, held.ID, refusals[0].TicketID)
	assert.Equal(t, ticket.StatusInProgress, refusals[0].From)
	assert.ErrorIs(t, refusals[0].Err, ticket.ErrNotParkable)
	assert.Equal(t, int64(999), refusals[1].TicketID)
	assert.ErrorIs(t, refusals[1].Err, ticket.ErrNotFound)
	assert.ErrorIs(t, err, ticket.ErrNotParkable, "the batch error matches each refusal's sentinel")
	assert.ErrorIs(t, err, ticket.ErrNotFound)

	assert.Equal(t, ticket.StatusOpen, f.status(t, first.ID), "moved before the refusal, and rolled back")
	assert.Equal(t, before, f.get(t, first.ID).UpdatedAt)
	assert.Equal(t, ticket.StatusInProgress, f.status(t, held.ID))
	assert.Equal(t, ticket.StatusOpen, f.status(t, last.ID))
	lease, err := f.svc.LeaseFor(f.ctx, a)
	require.NoError(t, err, "the refused batch took nothing from the agent")
	assert.Equal(t, held.ID, lease.TicketID)
}

// Each rule a single move enforces, a batch enforces too -- it is the same
// code.
func TestBulkMove_RefusesWhatASingleMoveRefuses(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	drafting := f.preparing(t, "Drafting")
	parked := f.parked(t, "Parked")

	_, err := f.svc.MoveMany(f.ctx, []int64{drafting.ID, parked.ID}, ticket.StatusOpen)

	refusals := refusalsOf(t, err)
	require.Len(t, refusals, 2)
	assert.ErrorIs(t, refusals[0].Err, ticket.ErrLeavePreparing)
	assert.ErrorIs(t, refusals[1].Err, ticket.ErrLeaveParked)
	assert.Equal(t, ticket.StatusPreparing, f.status(t, drafting.ID))
	assert.Equal(t, ticket.StatusParked, f.status(t, parked.ID))
}

// A batch's cross-ticket effects: leases on the moved tickets revoked, and
// tickets waiting on them unblocked.
func TestBulkMove_RevokesLeasesAndUnblocksWhatWaitedOnThem(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder")
	held := f.ticket(t, "Held", "server/")
	f.claim(t, a)
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, a))
	dep := f.ticket(t, "Dependency", "docs/")
	waiting := f.ticket(t, "Waiting", "web/")
	f.blockedBy(t, waiting.ID, dep.ID)
	require.Equal(t, ticket.StatusBlocked, f.status(t, waiting.ID))

	f.moveMany(t, ticket.StatusCancelled, held.ID, dep.ID)

	assert.Equal(t, ticket.StatusOpen, f.status(t, waiting.ID), "its only dependency is cancelled")
	assert.NotNil(t, f.get(t, held.ID).ClosedAt)
	assert.Zero(t, f.lockCount(t), "the integration lock went with the lease")
	var revoked *ticket.LeaseRevokedError
	require.ErrorAs(t, f.svc.Heartbeat(f.ctx, a), &revoked)
	assert.Equal(t, held.ID, revoked.TicketID)
	assert.Equal(t, ticket.StatusCancelled, revoked.Status)
}

// The outcome depends on which tickets a batch names, not the order it
// names them in: rules look only at a ticket's own status, and cross-ticket
// effects happen once, after every move.
func TestBulkMove_OrderDoesNotMatter(t *testing.T) {
	t.Parallel()
	type outcome struct {
		statuses map[string]ticket.Status
		revoked  ticket.Status
	}
	run := func(t *testing.T, reverse bool) outcome {
		f := newFixture(t)
		a := f.agent(t, "builder")
		held := f.ticket(t, "Held", "server/")
		f.claim(t, a)
		prerequisite := f.ticket(t, "Prerequisite", "docs/")
		dependent := f.ticket(t, "Dependent", "web/")
		f.blockedBy(t, dependent.ID, prerequisite.ID)
		ids := []int64{held.ID, prerequisite.ID, dependent.ID}
		if reverse {
			ids = []int64{dependent.ID, prerequisite.ID, held.ID}
		}

		f.moveMany(t, ticket.StatusBlocked, ids...)

		var revoked *ticket.LeaseRevokedError
		require.ErrorAs(t, f.svc.Heartbeat(f.ctx, a), &revoked)
		return outcome{
			statuses: map[string]ticket.Status{
				"held":         f.status(t, held.ID),
				"prerequisite": f.status(t, prerequisite.ID),
				"dependent":    f.status(t, dependent.ID),
			},
			revoked: revoked.Status,
		}
	}

	forward, backward := run(t, false), run(t, true)

	assert.Equal(t, forward, backward)
	// Blocked with nothing unfinished to wait on lands on open; the
	// dependent's prerequisite is open, so unfinished.
	assert.Equal(t, map[string]ticket.Status{
		"held": ticket.StatusOpen, "prerequisite": ticket.StatusOpen, "dependent": ticket.StatusBlocked,
	}, forward.statuses)
	assert.Equal(t, ticket.StatusOpen, forward.revoked, "the agent is told where its ticket landed")
}

// A claim waiting on the batch sees all of it or none of it: the batch
// holds the claim lock from its first write to its commit.
//
// Verified: with moveTickets not taking the claim lock, the claim finishes
// inside the window and is handed a ticket the batch is parking.
func TestBulkMove_NoClaimSeesAHalfAppliedBatch(t *testing.T) {
	f := newFixture(t)
	a := f.agent(t, "waiting")
	first, second := f.ticket(t, "First"), f.ticket(t, "Second")

	inWindow, leaveWindow := make(chan struct{}), make(chan struct{})
	restore := ticket.SetMoveBarrier(func() {
		close(inWindow)
		<-leaveWindow
	})
	defer restore()

	moved := make(chan error, 1)
	go func() {
		_, err := f.svc.MoveMany(f.ctx, []int64{first.ID, second.ID}, ticket.StatusParked)
		moved <- err
	}()
	<-inWindow

	type claimed struct {
		result *ticket.ClaimResult
		err    error
	}
	claimDone := make(chan claimed, 1)
	go func() {
		result, err := f.svc.Claim(f.ctx, a, &second.ID)
		claimDone <- claimed{result, err}
	}()

	// Observed states, not a sleep standing in for one: the claim is
	// either queued on a lock (correct) or already back (the race).
	var early *claimed
	deadline := time.Now().Add(10 * time.Second)
	for early == nil && f.lockWaiters(t) == 0 {
		select {
		case got := <-claimDone:
			early = &got
		case <-time.After(5 * time.Millisecond):
		}
		require.True(t, time.Now().Before(deadline), "the claim neither waited nor finished")
	}
	close(leaveWindow)
	movedErr := <-moved
	require.Nil(t, early, "a claim finished in the middle of the batch")
	require.NoError(t, movedErr)

	got := <-claimDone
	require.NoError(t, got.err)
	assert.Nil(t, got.result.Ticket, "the claim that waited sees the whole batch: both parked")
	assert.Equal(t, ticket.StatusParked, f.status(t, second.ID))
}

// A bulk move and a bulk priority change over the same tickets, one under
// the claim lock and one not, meet on the tickets' rows: both lock them in
// id order, so the second waits for the first rather than each holding a
// row the other needs.
//
// Verified: with moveTickets writing in the order it was asked, the move
// holds the higher id while the priority change takes the lower, and
// Postgres aborts one of them as a deadlock.
func TestBulk_AMoveAndAPriorityChangeDoNotDeadlock(t *testing.T) {
	f := newFixture(t)
	low, high := f.ticket(t, "Low id"), f.ticket(t, "High id")

	inWindow, leaveWindow := make(chan struct{}), make(chan struct{})
	restore := ticket.SetMoveBarrier(func() {
		close(inWindow)
		<-leaveWindow
	})
	defer restore()

	moved := make(chan error, 1)
	go func() {
		_, err := f.svc.MoveMany(f.ctx, []int64{high.ID, low.ID}, ticket.StatusParked)
		moved <- err
	}()
	<-inWindow

	prioritised := make(chan error, 1)
	go func() {
		_, err := f.svc.SetPriorityMany(f.ctx, []int64{low.ID, high.ID}, model.PriorityUrgent)
		prioritised <- err
	}()
	deadline := time.Now().Add(10 * time.Second)
	for f.lockWaiters(t) == 0 {
		require.True(t, time.Now().Before(deadline), "the priority change never met the move's row locks")
		time.Sleep(5 * time.Millisecond)
	}
	close(leaveWindow)

	require.NoError(t, <-moved)
	require.NoError(t, <-prioritised)
	for _, id := range []int64{low.ID, high.ID} {
		got := f.get(t, id)
		assert.Equal(t, ticket.StatusParked, got.Status)
		assert.Equal(t, model.PriorityUrgent, got.Priority)
	}
}

func TestBulkReady_LandsEachOpenOrBlocked(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	unfinished := f.ticket(t, "Unfinished dependency")
	free := f.preparing(t, "Nothing pending")
	waiting := f.preparing(t, "Waits on it")
	f.blockedBy(t, waiting.ID, unfinished.ID)
	// One ticket of the batch waiting on another: whichever is written
	// first, the other is still unfinished.
	chained := f.preparing(t, "Waits on the free one")
	f.blockedBy(t, chained.ID, free.ID)

	ready, err := f.svc.MarkReadyMany(f.ctx, []int64{chained.ID, waiting.ID, free.ID})

	require.NoError(t, err)
	require.Len(t, ready, 3)
	assert.Equal(t, ticket.StatusBlocked, ready[0].Status)
	assert.Equal(t, ticket.StatusBlocked, ready[1].Status)
	assert.Equal(t, ticket.StatusOpen, ready[2].Status)
	assert.Equal(t, ticket.StatusBlocked, f.status(t, chained.ID))
}

func TestBulkReady_RefusesATicketThatIsNotPreparingAndMovesNone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	drafting := f.preparing(t, "Drafting")
	queued := f.ticket(t, "Already queued")

	_, err := f.svc.MarkReadyMany(f.ctx, []int64{drafting.ID, queued.ID})

	refusals := refusalsOf(t, err)
	require.Len(t, refusals, 1)
	assert.Equal(t, queued.ID, refusals[0].TicketID)
	assert.Equal(t, ticket.StatusOpen, refusals[0].From)
	assert.ErrorIs(t, refusals[0].Err, ticket.ErrNotPreparing)
	assert.Equal(t, ticket.StatusPreparing, f.status(t, drafting.ID))
}

func TestBulkPriority_SetsEveryTicket(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b := f.ticket(t, "A"), f.preparing(t, "B")

	updated, err := f.svc.SetPriorityMany(f.ctx, []int64{a.ID, b.ID}, model.PriorityUrgent)

	require.NoError(t, err)
	require.Len(t, updated, 2)
	for _, u := range updated {
		assert.Equal(t, model.PriorityUrgent, u.Priority)
		assert.Equal(t, model.PriorityUrgent, f.get(t, u.ID).Priority)
	}
	assert.Equal(t, ticket.StatusPreparing, f.status(t, b.ID), "priority alone moves nothing")
}

// Verified: with SetPriority committing despite the missing ticket, A is
// left urgent and this fails.
func TestBulkPriority_OneMissingTicketChangesNone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.ticket(t, "A")

	_, err := f.svc.SetPriorityMany(f.ctx, []int64{a.ID, 404}, model.PriorityUrgent)

	refusals := refusalsOf(t, err)
	require.Len(t, refusals, 1)
	assert.Equal(t, int64(404), refusals[0].TicketID)
	assert.ErrorIs(t, refusals[0].Err, ticket.ErrNotFound)
	assert.Equal(t, model.PriorityNormal, f.get(t, a.ID).Priority)
}

func TestBulk_TheServiceShapesTheBatch(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	capped := ticket.NewService(newStore(f.database), slog.DiscardHandler, ticket.WithBulkMax(2))
	a, b, c := f.ticket(t, "A"), f.ticket(t, "B"), f.ticket(t, "C")

	t.Run("duplicates move once", func(t *testing.T) {
		moved, err := capped.MoveMany(f.ctx, []int64{a.ID, a.ID, b.ID, a.ID}, ticket.StatusParked)
		require.NoError(t, err)
		assert.Equal(t, []int64{a.ID, b.ID}, []int64{moved[0].ID, moved[1].ID})
	})
	t.Run("the cap counts distinct tickets", func(t *testing.T) {
		_, err := capped.SetPriorityMany(f.ctx, []int64{a.ID, b.ID, c.ID}, model.PriorityHigh)
		assert.ErrorIs(t, err, ticket.ErrBatchTooLarge)
		_, err = capped.MarkReadyMany(f.ctx, []int64{a.ID, b.ID, c.ID})
		assert.ErrorIs(t, err, ticket.ErrBatchTooLarge)
		_, err = capped.MoveMany(f.ctx, []int64{a.ID, b.ID, c.ID}, ticket.StatusPreparing)
		assert.ErrorIs(t, err, ticket.ErrBatchTooLarge)
		assert.Equal(t, ticket.StatusOpen, f.status(t, c.ID))
	})
	t.Run("an empty batch", func(t *testing.T) {
		_, err := capped.MoveMany(f.ctx, nil, ticket.StatusParked)
		assert.ErrorIs(t, err, ticket.ErrEmptyBatch)
		_, err = capped.MarkReadyMany(f.ctx, []int64{})
		assert.ErrorIs(t, err, ticket.ErrEmptyBatch)
		_, err = capped.SetPriorityMany(f.ctx, nil, model.PriorityHigh)
		assert.ErrorIs(t, err, ticket.ErrEmptyBatch)
	})
	t.Run("an unknown status or priority", func(t *testing.T) {
		_, err := capped.MoveMany(f.ctx, []int64{c.ID}, "nonsense")
		assert.ErrorIs(t, err, ticket.ErrInvalidEnum)
		_, err = capped.SetPriorityMany(f.ctx, []int64{c.ID}, "whenever")
		assert.ErrorIs(t, err, ticket.ErrInvalidEnum)
	})
}

// Another tenant's ticket is one this tenant does not have: it fails the
// batch as not found, and neither tenant's tickets change.
func TestBulk_AnotherTenantsTicketIsNotFound(t *testing.T) {
	t.Parallel()
	a, b := newTwoTenants(t)
	own := a.ticket(t, "Tenant A's #1")
	b.ticket(t, "Tenant B's #1")
	theirs := b.ticket(t, "Tenant B's #2")
	require.Equal(t, int64(2), theirs.ID)

	_, err := a.svc.MoveMany(a.ctx, []int64{own.ID, theirs.ID}, ticket.StatusParked)
	refusals := refusalsOf(t, err)
	require.Len(t, refusals, 1)
	assert.Equal(t, theirs.ID, refusals[0].TicketID)
	assert.ErrorIs(t, refusals[0].Err, ticket.ErrNotFound)

	_, err = a.svc.SetPriorityMany(a.ctx, []int64{own.ID, theirs.ID}, model.PriorityUrgent)
	assert.ErrorIs(t, err, ticket.ErrNotFound)

	mine, err := a.svc.Get(a.ctx, own.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusOpen, mine.Status)
	assert.Equal(t, model.PriorityNormal, mine.Priority)
	stillTheirs, err := b.svc.Get(b.ctx, theirs.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusOpen, stillTheirs.Status)
	assert.Equal(t, model.PriorityNormal, stillTheirs.Priority)
}
