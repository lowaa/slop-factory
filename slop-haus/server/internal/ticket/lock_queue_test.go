package ticket_test

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// merger is an agent holding a ticket of its own, so it has a lease to
// queue under.
type merger struct {
	id     uuid.UUID
	ticket *ticket.Ticket
}

func (f *fixture) mergers(t *testing.T, names ...string) []merger {
	t.Helper()
	out := make([]merger, 0, len(names))
	for _, name := range names {
		a := f.agent(t, name)
		created := f.ticket(t, "Work of "+name, name+"/")
		_, err := f.svc.Claim(f.ctx, a, &created.ID)
		require.NoError(t, err)
		out = append(out, merger{id: a, ticket: created})
	}
	return out
}

func (f *fixture) lockHeldBy(t *testing.T) (uuid.UUID, bool) {
	t.Helper()
	var holder uuid.UUID
	rows, err := f.database.Query(f.ctx, `SELECT holder_agent_id FROM locks`)
	require.NoError(t, err)
	defer rows.Close()
	if !rows.Next() {
		return uuid.Nil, false
	}
	require.NoError(t, rows.Scan(&holder))
	return holder, true
}

func (f *fixture) refusal(t *testing.T, err error) *ticket.LockHeldError {
	t.Helper()
	require.ErrorIs(t, err, ticket.ErrLockHeld)
	var held *ticket.LockHeldError
	require.ErrorAs(t, err, &held)
	return held
}

func queuedAgents(held *ticket.LockHeldError) []string {
	names := make([]string, 0, len(held.Ahead))
	for _, e := range held.Ahead {
		names = append(names, e.AgentName)
	}
	return names
}

func TestLockQueue_ARefusedAgentJoinsAndIsToldWhereItStands(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder", "second", "third")
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))

	second := f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[1].id))
	assert.Equal(t, 1, second.Position)
	assert.Empty(t, second.Ahead)
	require.NotNil(t, second.Holder)
	assert.Equal(t, "holder", second.Holder.AgentName)

	third := f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[2].id))
	assert.Equal(t, 2, third.Position)
	require.Len(t, third.Ahead, 1)
	assert.Equal(t, "second", third.Ahead[0].AgentName)
	assert.Equal(t, m[1].ticket.ID, third.Ahead[0].TicketID)
	assert.False(t, third.Ahead[0].QueuedAt.IsZero())
	assert.Contains(t, third.Error(), "number 2 in the queue")
	assert.Contains(t, third.Error(), `agent "second" on ticket #`)

	// Asking again keeps the place rather than going to the back.
	again := f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[1].id))
	assert.Equal(t, 1, again.Position)
}

func TestLockQueue_ReleaseGoesToTheHeadNotToWhoeverAsksFirst(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder", "first", "second", "third")
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	for _, w := range m[1:] {
		f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, w.id))
	}
	require.NoError(t, f.svc.ReleaseIntegrationLock(f.ctx, m[0].id))

	// The one that retried last tries first: the order is the queue's.
	late := f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[3].id))
	assert.Equal(t, 3, late.Position)
	assert.Nil(t, late.Holder, "nobody holds it, it is reserved for the head")
	assert.Contains(t, late.Error(), "reserved")
	assert.Equal(t, []string{"first", "second"}, queuedAgents(late))
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[1].id))
	holder, held := f.lockHeldBy(t)
	require.True(t, held)
	assert.Equal(t, m[1].id, holder)

	// The next release goes to the next in line, whatever the order of asking.
	require.NoError(t, f.svc.ReleaseIntegrationLock(f.ctx, m[1].id))
	f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[3].id))
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[2].id))
	require.NoError(t, f.svc.ReleaseIntegrationLock(f.ctx, m[2].id))
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[3].id))
}

// A holder that releases and takes the lock straight back goes to the back:
// back-to-back merges by one agent are what starved the others.
func TestLockQueue_AHolderThatRelocksGoesToTheBack(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder", "waiting")
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[1].id))
	require.NoError(t, f.svc.ReleaseIntegrationLock(f.ctx, m[0].id))

	relock := f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	assert.Equal(t, 2, relock.Position)
	assert.Equal(t, []string{"waiting"}, queuedAgents(relock))
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[1].id))
}

// With nobody waiting there is nobody to be fair to.
func TestLockQueue_AHolderThatRelocksWithNoQueueTakesItAgain(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder")
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	require.NoError(t, f.svc.ReleaseIntegrationLock(f.ctx, m[0].id))
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
}

// holdClaimLock holds the claim lock until the returned release is called,
// so the operations a test then starts all park on it and are admitted in
// the order they arrived -- which is how a test makes a particular
// interleaving happen instead of hoping for it.
func (f *fixture) holdClaimLock(t *testing.T) (release func()) {
	t.Helper()
	held, leave := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- f.database.InTx(f.ctx, func(q db.Querier) error {
			if err := db.TakeAdvisoryLock(f.ctx, q, db.ClaimLockKey); err != nil {
				return err
			}
			close(held)
			<-leave
			return nil
		})
	}()
	select {
	case <-held:
	case err := <-done:
		t.Fatalf("could not take the claim lock: %v", err)
	}
	var once sync.Once
	release = func() {
		once.Do(func() { close(leave) })
		require.NoError(t, <-done)
	}
	t.Cleanup(func() { once.Do(func() { close(leave) }) })
	return release
}

// lockCalls starts the agents' lock calls one after another, each parked
// on the claim lock before the next starts, so that they are decided in
// this order once the claim lock is let go.
func (f *fixture) lockCalls(t *testing.T, release func(), agents ...uuid.UUID) []error {
	t.Helper()
	results := make([]chan error, len(agents))
	for i, id := range agents {
		results[i] = make(chan error, 1)
		go func() { results[i] <- f.svc.AcquireIntegrationLock(f.ctx, id) }()
		f.awaitLockWaiters(t, i+1, results[i])
	}
	release()
	errs := make([]error, len(agents))
	for i := range results {
		errs[i] = <-results[i]
	}
	return errs
}

// The one that would win by arriving first is the one that is not first in
// line: the claim lock admits it first, and the queue must still refuse it.
func TestLockQueue_TwoAgentsRacingForOneReleaseDoNotBothGetIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder", "head", "behind")
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[1].id))
	f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[2].id))
	require.NoError(t, f.svc.ReleaseIntegrationLock(f.ctx, m[0].id))

	errs := f.lockCalls(t, f.holdClaimLock(t), m[2].id, m[1].id)

	require.ErrorIs(t, errs[0], ticket.ErrLockHeld, "behind was decided first and must not take it")
	require.NoError(t, errs[1], "the head wins the release")
	holder, held := f.lockHeldBy(t)
	require.True(t, held)
	assert.Equal(t, m[1].id, holder)
}

// With no queue the claim lock alone decides: the first admitted wins, the
// second is refused and queued behind it.
func TestLockQueue_TwoAgentsRacingForAFreeLockDoNotBothGetIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "one", "two")

	errs := f.lockCalls(t, f.holdClaimLock(t), m[0].id, m[1].id)

	require.NoError(t, errs[0])
	held := f.refusal(t, errs[1])
	assert.Equal(t, 1, held.Position)
	require.NotNil(t, held.Holder)
	assert.Equal(t, "one", held.Holder.AgentName)
}

func TestLockQueue_AnAgentWhoseLeaseExpiresLeavesTheQueue(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder", "gone", "waiting")
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[1].id))
	f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[2].id))
	f.expire(t, m[1].id)
	require.NoError(t, f.svc.ReleaseIntegrationLock(f.ctx, m[0].id))

	// Not stuck behind a ghost at the head: the next in line is first.
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[2].id))
}

func TestLockQueue_AnAgentThatReleasesItsTicketLeavesTheQueue(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder", "leaving", "waiting")
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[1].id))
	f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[2].id))
	_, err := f.svc.Release(f.ctx, m[1].id, "giving up", nil)
	require.NoError(t, err)

	state, err := f.svc.IntegrationLockState(f.ctx)
	require.NoError(t, err)
	require.Len(t, state.Queue, 1)
	assert.Equal(t, m[2].id, state.Queue[0].AgentID)
	var rows int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM lock_waiters`).Scan(&rows))
	assert.Equal(t, 1, rows, "the entry is gone, not merely hidden")
}

func TestLockQueue_AnOfferNotTakenUpPassesToTheNextInLine(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder", "asleep", "waiting")
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[1].id))
	f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[2].id))
	require.NoError(t, f.svc.ReleaseIntegrationLock(f.ctx, m[0].id))

	// Within the bound the head keeps its place.
	refused := f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[2].id))
	assert.Equal(t, 2, refused.Position)

	_, err := f.database.Exec(f.ctx, `UPDATE lock_waiters SET granted_at = now() - $1::interval`,
		(ticket.DefaultLockOfferBound + time.Minute).String())
	require.NoError(t, err)
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[2].id))
	holder, held := f.lockHeldBy(t)
	require.True(t, held)
	assert.Equal(t, m[2].id, holder)

	// The one that slept lost its place: asking again is the back of the queue.
	require.NoError(t, f.svc.ReleaseIntegrationLock(f.ctx, m[2].id))
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[1].id), "nobody else is waiting")
}

func TestLockQueue_TheHolderIsStillFreedByItsLeaseEnding(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder", "waiting")
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[1].id))
	_, err := f.svc.Release(f.ctx, m[0].id, "giving up", nil)
	require.NoError(t, err)

	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[1].id))
}

func TestLockQueue_AwaitReservesTheLockWithoutTakingIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder", "waiting", "latecomer")
	require.NoError(t, f.svc.AwaitIntegrationLock(f.ctx, m[0].id), "free: reserved for the caller")
	_, held := f.lockHeldBy(t)
	assert.False(t, held, "reserved, not taken")

	// The reservation is real: nobody else can take it from under the waiter.
	late := f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[2].id))
	assert.Nil(t, late.Holder)
	assert.Equal(t, []string{"holder"}, queuedAgents(late))

	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	refused := f.refusal(t, f.svc.AwaitIntegrationLock(f.ctx, m[1].id))
	assert.Equal(t, 2, refused.Position, "the latecomer queued first")
}

func TestLockQueue_AReservationNobodyAnswersLapses(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "gone", "waiting")
	require.NoError(t, f.svc.AwaitIntegrationLock(f.ctx, m[0].id))
	f.refusal(t, f.svc.AwaitIntegrationLock(f.ctx, m[1].id))
	_, err := f.database.Exec(f.ctx, `UPDATE lock_waiters SET granted_at = now() - interval '1 hour'`)
	require.NoError(t, err)

	require.NoError(t, f.svc.AwaitIntegrationLock(f.ctx, m[1].id))
}

func TestLockQueue_APersonCanReleaseTheLockAndDropAnEntry(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "dead", "dropped", "waiting")
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[1].id))
	f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[2].id))

	require.NoError(t, f.svc.DropNamedLockWaiter(f.ctx, ticket.IntegrationLockName, m[1].id))
	require.ErrorIs(t, f.svc.DropNamedLockWaiter(f.ctx, ticket.IntegrationLockName, m[1].id), ticket.ErrNotQueued)

	holder, err := f.svc.ForceReleaseNamedLock(f.ctx, ticket.IntegrationLockName)
	require.NoError(t, err)
	assert.Equal(t, "dead", holder.AgentName)
	_, err = f.svc.ForceReleaseNamedLock(f.ctx, ticket.IntegrationLockName)
	require.ErrorIs(t, err, ticket.ErrNoSuchLock)

	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[2].id))
	// The dead holder's lease is no longer pinned.
	f.expire(t, m[0].id)
	_, err = f.svc.LeaseFor(f.ctx, m[0].id)
	require.ErrorIs(t, err, ticket.ErrNoLease)
}

func TestLockState_ReportsHolderPRAndQueue(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder", "waiting")
	state, err := f.svc.IntegrationLockState(f.ctx)
	require.NoError(t, err)
	assert.Nil(t, state.Holder)
	assert.Empty(t, state.Queue)

	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[1].id))
	_, err = f.database.Exec(f.ctx, `UPDATE attempts SET pr_url = 'https://github.com/o/r/pull/9' WHERE ticket_id = $1 AND number = 1`, m[0].ticket.ID)
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx, `UPDATE lock_waiters SET queued_at = now() - interval '10 minutes'`)
	require.NoError(t, err)

	state, err = f.svc.IntegrationLockState(f.ctx)
	require.NoError(t, err)
	require.NotNil(t, state.Holder)
	assert.Equal(t, "holder", state.Holder.AgentName)
	assert.Equal(t, m[0].ticket.ID, state.Holder.TicketID)
	assert.Equal(t, "https://github.com/o/r/pull/9", state.Holder.PRURL)
	require.Len(t, state.Queue, 1)
	assert.GreaterOrEqual(t, state.Queue[0].Waited, 10*time.Minute)
	assert.Less(t, state.Queue[0].Waited, 11*time.Minute)
}

func TestLockState_TheHoldersPRIsItsLeasesAttempts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "first")
	f.addAttempt(t, m[0].ticket.ID, false)
	second := f.agent(t, "second")
	_, err := f.svc.Claim(f.ctx, second, &m[0].ticket.ID)
	require.NoError(t, err)
	require.Equal(t, 2, f.leasedAttempt(t, second))
	_, err = f.database.Exec(f.ctx, `
		UPDATE attempts SET pr_url = 'https://github.com/o/r/pull/' || number WHERE ticket_id = $1`, m[0].ticket.ID)
	require.NoError(t, err)
	f.promoteAttempt(t, m[0].ticket.ID, 2)

	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, second))

	state, err := f.svc.IntegrationLockState(f.ctx)
	require.NoError(t, err)
	require.NotNil(t, state.Holder)
	assert.Equal(t, second, state.Holder.AgentID)
	assert.Equal(t, "https://github.com/o/r/pull/2", state.Holder.PRURL, "attempt 2's, not the earlier promoted attempt 1's")
}

func (f *fixture) promoteAttempt(t *testing.T, ticketID int64, number int) {
	t.Helper()
	_, err := f.database.Exec(f.ctx, `UPDATE attempts SET promoted_at = NULL WHERE ticket_id = $1 AND promoted_at IS NOT NULL`, ticketID)
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx, `UPDATE attempts SET promoted_at = now() WHERE ticket_id = $1 AND number = $2`, ticketID, number)
	require.NoError(t, err)
}

func TestLockQueue_AnUnpromotedAttemptIsRefusedTheLockAndDoesNotQueue(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "first")
	for _, shadow := range []bool{false, true} {
		f.addAttempt(t, m[0].ticket.ID, shadow)
	}
	second := f.agent(t, "second")
	_, err := f.svc.Claim(f.ctx, second, &m[0].ticket.ID)
	require.NoError(t, err)
	require.Equal(t, 2, f.leasedAttempt(t, second))

	err = f.svc.AcquireIntegrationLock(f.ctx, second)
	require.ErrorIs(t, err, ticket.ErrAttemptAwaitsChoice)
	assert.Contains(t, err.Error(), "ticket_finish")
	require.ErrorIs(t, f.svc.AwaitIntegrationLock(f.ctx, second), ticket.ErrAttemptAwaitsChoice)

	state, err := f.svc.IntegrationLockState(f.ctx)
	require.NoError(t, err)
	assert.Nil(t, state.Holder)
	assert.Empty(t, state.Queue)
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id), "attempt 1, the promoted one, takes it")
}

// Each tenant's queue, and a person's power over it, stops at the tenant:
// the release and the drop must not reach another tenant's lock or entries.
func TestLockQueue_TenantsHaveQueuesOfTheirOwn(t *testing.T) {
	t.Parallel()
	a, b := newTwoTenants(t)
	waiting := func(f *tenantFixture, name string) uuid.UUID {
		id, err := f.agents.Create(f.ctx, name)
		require.NoError(t, err)
		created := f.ticket(t, name+" work", name+"/")
		_, err = f.svc.Claim(f.ctx, id.ID, &created.ID)
		require.NoError(t, err)
		return id.ID
	}
	aHolder := waiting(a, "a-holder")
	aWaiting := waiting(a, "a-waiting")
	bHolder := waiting(b, "b-holder")
	require.NoError(t, a.svc.AcquireIntegrationLock(a.ctx, aHolder))
	require.ErrorIs(t, a.svc.AcquireIntegrationLock(a.ctx, aWaiting), ticket.ErrLockHeld)
	require.NoError(t, b.svc.AcquireIntegrationLock(b.ctx, bHolder))

	state, err := b.svc.IntegrationLockState(b.ctx)
	require.NoError(t, err)
	assert.Empty(t, state.Queue, "A's waiter is not in B's queue")
	assert.Equal(t, "b-holder", state.Holder.AgentName)

	require.ErrorIs(t, b.svc.DropNamedLockWaiter(b.ctx, ticket.IntegrationLockName, aWaiting), ticket.ErrNotQueued,
		"B cannot drop A's entry, even naming its agent")
	released, err := b.svc.ForceReleaseNamedLock(b.ctx, ticket.IntegrationLockName)
	require.NoError(t, err)
	assert.Equal(t, "b-holder", released.AgentName)
	stateA, err := a.svc.IntegrationLockState(a.ctx)
	require.NoError(t, err)
	require.NotNil(t, stateA.Holder, "A's lock is untouched by B's release")
	assert.Len(t, stateA.Queue, 1)
}

// The offer's clock starts when the lock comes free, not when someone next
// asks, and on the first live entry, not a dead one ahead of it.
func TestLockQueue_TheOfferStartsWhenTheLockComesFree(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder", "gone", "waiting")
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[1].id))
	f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[2].id))
	f.expire(t, m[1].id)
	require.NoError(t, f.svc.ReleaseIntegrationLock(f.ctx, m[0].id))

	state, err := f.svc.IntegrationLockState(f.ctx)
	require.NoError(t, err)
	require.Len(t, state.Queue, 1)
	assert.Equal(t, m[2].id, state.Queue[0].AgentID)
	assert.NotNil(t, state.Queue[0].OfferedAt, "offered at the release, with nobody having asked since")
}

// Disabling an agent ends its leases through a statement of its own, not
// endLease: its queue entry must go, and a lock it held must go to the head
// with the offer started.
func TestLockQueue_DisablingAnAgentLeavesTheQueueAndOffersItsLock(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder", "disabled-waiter", "waiting")
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[1].id))
	f.refusal(t, f.svc.AcquireIntegrationLock(f.ctx, m[2].id))

	_, err := f.agents.SetDisabled(f.ctx, m[1].id, true)
	require.NoError(t, err)
	state, err := f.svc.IntegrationLockState(f.ctx)
	require.NoError(t, err)
	require.Len(t, state.Queue, 1)
	var rows int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM lock_waiters`).Scan(&rows))
	assert.Equal(t, 1, rows, "the dequeue happened with the lease, not merely hidden by the read")

	_, err = f.agents.SetDisabled(f.ctx, m[0].id, true)
	require.NoError(t, err)
	state, err = f.svc.IntegrationLockState(f.ctx)
	require.NoError(t, err)
	assert.Nil(t, state.Holder, "the disabled holder's lock is gone")
	require.Len(t, state.Queue, 1)
	assert.Equal(t, m[2].id, state.Queue[0].AgentID)
	assert.NotNil(t, state.Queue[0].OfferedAt, "offered when it came free, with nobody having asked since")
}
