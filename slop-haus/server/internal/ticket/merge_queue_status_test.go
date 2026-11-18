package ticket_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// land has the author claim the next ticket and finish it into the landing
// queue, as ticket_finish does in merge-queue mode.
func (f *fixture) land(t *testing.T, author uuid.UUID, id int64) {
	t.Helper()
	require.NotNil(t, f.claim(t, author))
	_, err := f.svc.Land(f.ctx, author, &ticket.LandParams{Handoff: "h", Within: func(q db.Querier) error {
		return landing.EnqueueIn(f.ctx, q, &landing.LandingRow{TicketID: id, AttemptNumber: 1, ChangeURL: reviewPR, HeadSHA: "abc"})
	}})
	require.NoError(t, err)
}

func (f *fixture) landingStates(t *testing.T, id int64) (states []string) {
	t.Helper()
	rows, err := f.database.Query(f.ctx, `SELECT queue_state FROM landings WHERE ticket_id = $1`, id)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		states = append(states, s)
	}
	require.NoError(t, rows.Err())
	return states
}

func TestMergeQueue_EachStayInTheQueueIsItsOwnStatusChange(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	author := f.agent(t, "author")
	created := f.ticket(t, "Lands", "server/")
	f.land(t, author, created.ID)

	assert.Equal(t, "in_merge_queue", f.ticketStatus(t, created.ID))
	assert.Equal(t, map[int]string{1: "in_merge_queue"}, f.attemptStatuses(t, created.ID))
	assert.Equal(t, []string{"queued"}, f.landingStates(t, created.ID))

	server := f.serverAgent(t)
	claim, err := f.serverClaimLanding(server, created.ID)
	require.NoError(t, err)
	require.NotNil(t, claim.Ticket)
	assert.Equal(t, "in_merge_queue", f.ticketStatus(t, created.ID), "the server holding the head is not work in progress")
	assert.Equal(t, map[int]string{1: "in_merge_queue"}, f.attemptStatuses(t, created.ID))

	require.NoError(t, f.store().ServerHandBack(f.ctx, server, created.ID, "main moved", time.Hour))
	require.NoError(t, landing.NewLandingStore(f.database).HandBack(f.ctx, created.ID, landing.MergeRefused, false))
	assert.Equal(t, "open", f.ticketStatus(t, created.ID))
	assert.Equal(t, map[int]string{1: "queued"}, f.attemptStatuses(t, created.ID))

	// Fixed and finished again: back in the queue, a second stay.
	f.expireAffinity(t, created.ID)
	f.land(t, author, created.ID)
	assert.Equal(t, "in_merge_queue", f.ticketStatus(t, created.ID))
	assert.Equal(t, []string{
		">open", "open>in_progress", "in_progress>in_merge_queue", "in_merge_queue>open", "open>in_progress", "in_progress>in_merge_queue",
	}, f.ticketHistory(t, created.ID))
	assert.Equal(t, []string{
		"1:>queued", "1:queued>in_progress", "1:in_progress>in_merge_queue", "1:in_merge_queue>queued", "1:queued>in_progress", "1:in_progress>in_merge_queue",
	}, f.attemptHistory(t, created.ID))

	// Merged: the server closes the ticket it holds.
	_, err = f.serverClaimLanding(server, created.ID)
	require.NoError(t, err)
	_, err = f.svc.Done(f.ctx, server, &ticket.DoneParams{Summary: "merged"})
	require.NoError(t, err)
	assert.Equal(t, "done", f.ticketStatus(t, created.ID))
}

func TestMergeQueue_AWorkerNeverTakesATicketInTheQueueAndTheOldFlowDoesNotEither(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	author := f.agent(t, "author")
	created := f.ticket(t, "Lands", "server/")
	f.land(t, author, created.ID)
	f.expireAffinity(t, created.ID)

	assert.Nil(t, f.claim(t, f.agent(t, "worker")))
	_, err := f.serverClaim(f.serverAgent(t), created.ID)
	assert.ErrorIs(t, err, ticket.ErrServerClaimNotQueued, "the approved-ticket flow claims queued attempts only")
}

func TestMergeQueue_ALapsedServerLeaseLeavesTheLandingWhereItIs(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	author := f.agent(t, "author")
	created := f.ticket(t, "Lands", "server/")
	f.land(t, author, created.ID)
	server := f.serverAgent(t)
	_, err := f.serverClaimLanding(server, created.ID)
	require.NoError(t, err)

	f.expire(t, server)
	f.expireAffinity(t, created.ID)
	assert.Nil(t, f.claim(t, f.agent(t, "worker")), "this claim runs the expiry sweep")

	assert.Equal(t, "in_merge_queue", f.ticketStatus(t, created.ID))
	assert.Equal(t, map[int]string{1: "in_merge_queue"}, f.attemptStatuses(t, created.ID))
	assert.Equal(t, []string{"queued"}, f.landingStates(t, created.ID))
}

func TestMergeQueue_ASiblingRunningDoesNotHideTheLanding(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	author := f.agent(t, "author")
	created := f.ticket(t, "Lands", "server/")
	f.land(t, author, created.ID)
	_, err := f.database.Exec(f.ctx, `INSERT INTO attempts (ticket_id, number, status, shadow) VALUES ($1, 2, 'in_progress', true)`, created.ID)
	require.NoError(t, err)

	_, err = f.serverClaimLanding(f.serverAgent(t), created.ID)
	require.NoError(t, err)
	assert.Equal(t, "in_merge_queue", f.ticketStatus(t, created.ID))
}

func TestMergeQueue_ARequeuedBatchMemberLeavesTheQueueWithoutALease(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticket(t, "Lands", "server/")
	f.land(t, f.agent(t, "author"), created.ID)
	server := f.serverAgent(t)
	ref := &ticket.AttemptRef{TicketID: created.ID, Number: 1}

	require.NoError(t, f.store().ServerRequeueMember(f.ctx, server, ref))
	assert.Equal(t, "open", f.ticketStatus(t, created.ID))
	assert.Equal(t, map[int]string{1: "queued"}, f.attemptStatuses(t, created.ID))

	require.NoError(t, f.store().ServerRequeueMember(f.ctx, server, ref), "a second call has nothing to move")
	assert.Equal(t, map[int]string{1: "queued"}, f.attemptStatuses(t, created.ID))

	assert.ErrorIs(t, f.store().ServerRequeueMember(f.ctx, f.agent(t, "other"), ref), ticket.ErrNotServerAgent)
}

func TestMergeQueue_APersonMovingTheTicketOutTakesItOutOfTheQueue(t *testing.T) {
	t.Parallel()
	for _, to := range []ticket.Status{ticket.StatusCancelled, ticket.StatusPreparing, ticket.StatusDone, ticket.StatusOpen, ticket.StatusBlocked} {
		t.Run(string(to), func(t *testing.T) {
			f := newFixture(t)
			created := f.ticket(t, "Lands", "server/")
			f.land(t, f.agent(t, "author"), created.ID)
			server := f.serverAgent(t)
			_, err := f.serverClaimLanding(server, created.ID)
			require.NoError(t, err)

			_, err = f.svc.MoveMany(f.ctx, []int64{created.ID}, to)
			require.NoError(t, err)

			assert.Empty(t, f.landingStates(t, created.ID), "the server must not land what a person took back")
			assert.NotEqual(t, "in_merge_queue", f.ticketStatus(t, created.ID))
			assert.NotEqual(t, "in_merge_queue", f.attemptStatuses(t, created.ID)[1])
			assert.ErrorIs(t, f.store().ServerHandBack(f.ctx, server, created.ID, "x", time.Minute), ticket.ErrNoLease, "the server's lease was taken")
		})
	}
}

func TestMergeQueue_NoPersonMovesATicketIntoTheQueue(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	open := f.ticket(t, "Open", "a/")
	_, err := f.svc.MoveMany(f.ctx, []int64{open.ID}, ticket.StatusInMergeQueue)
	require.ErrorIs(t, err, ticket.ErrEnterMergeQueue)

	f.land(t, f.agent(t, "author"), f.ticket(t, "Lands", "b/").ID)
	var queued int64
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT id FROM tickets WHERE status = 'in_merge_queue'`).Scan(&queued))
	_, err = f.svc.MoveMany(f.ctx, []int64{queued}, ticket.StatusInProgress)
	assert.ErrorIs(t, err, ticket.ErrLeaveMergeQueue)
}

func TestMergeQueue_TheQueueCountsAgainstTheLiveAttemptCap(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.ticket(t, "Capped", "a/")
	for i := 0; i < ticket.MaxLiveAttempts-1; i++ {
		f.add(t, tk.ID)
	}
	f.setAttemptStatus(t, tk.ID, 2, "in_merge_queue")
	for n := 3; n <= ticket.MaxLiveAttempts; n++ {
		f.setAttemptStatus(t, tk.ID, n, "done")
	}
	f.add(t, tk.ID)
	f.add(t, tk.ID)
	f.add(t, tk.ID)
	_, err := f.svc.AddAttempt(f.ctx, f.human, tk.ID, nil)
	assert.ErrorIs(t, err, ticket.ErrAttemptCap)
}

func TestRepairMergeQueue(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T) (*fixture, int64, uuid.UUID) {
		f := newFixture(t)
		created := f.ticket(t, "Lands", "server/")
		f.land(t, f.agent(t, "author"), created.ID)
		return f, created.ID, f.serverAgent(t)
	}

	t.Run("a landing row whose attempt was handed back is taken into the queue again", func(t *testing.T) {
		f, id, server := setup(t)
		_, err := f.serverClaimLanding(server, id)
		require.NoError(t, err)
		require.NoError(t, f.store().ServerHandBack(f.ctx, server, id, "x", time.Hour))
		require.Equal(t, "open", f.ticketStatus(t, id), "the crash left the row behind")

		repaired, err := f.store().RepairMergeQueue(f.ctx, time.Hour)
		require.NoError(t, err)
		assert.Empty(t, repaired, "a write that may still be in flight is left alone")

		repaired, err = f.store().RepairMergeQueue(f.ctx, 0)
		require.NoError(t, err)
		assert.Equal(t, []int64{id}, repaired)
		assert.Equal(t, "in_merge_queue", f.ticketStatus(t, id))
		assert.Equal(t, map[int]string{1: "in_merge_queue"}, f.attemptStatuses(t, id))
	})

	t.Run("a landing row of an attempt a worker holds is dropped", func(t *testing.T) {
		f, id, _ := setup(t)
		f.setAttemptStatus(t, id, 1, "in_progress")

		repaired, err := f.store().RepairMergeQueue(f.ctx, 0)
		require.NoError(t, err)
		assert.Equal(t, []int64{id}, repaired)
		assert.Empty(t, f.landingStates(t, id))
	})

	t.Run("an attempt in the queue with no row goes back to its author", func(t *testing.T) {
		f, id, _ := setup(t)
		_, err := f.database.Exec(f.ctx, `DELETE FROM landings WHERE ticket_id = $1`, id)
		require.NoError(t, err)

		repaired, err := f.store().RepairMergeQueue(f.ctx, 0)
		require.NoError(t, err)
		assert.Equal(t, []int64{id}, repaired)
		assert.Equal(t, "open", f.ticketStatus(t, id))
		assert.Equal(t, map[int]string{1: "queued"}, f.attemptStatuses(t, id))
	})

	t.Run("a consistent queue is left alone, the server's lease on the head included", func(t *testing.T) {
		f, id, server := setup(t)
		_, err := f.serverClaimLanding(server, id)
		require.NoError(t, err)

		repaired, err := f.store().RepairMergeQueue(f.ctx, 0)
		require.NoError(t, err)
		assert.Empty(t, repaired)
		assert.Equal(t, "in_merge_queue", f.ticketStatus(t, id))
	})
}

func TestServerClaimLanding_TakesAQueuedLandingIntoTheQueue(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticket(t, "Lands", "server/")
	f.land(t, f.agent(t, "author"), created.ID)
	f.setAttemptStatus(t, created.ID, 1, "queued") // as the release before the status enqueued it

	_, err := f.serverClaim(f.serverAgent(t), created.ID)
	assert.ErrorIs(t, err, ticket.ErrServerClaimNotQueued, "the approved-ticket flow leaves a landing row to the landing queue")

	_, err = f.serverClaimLanding(f.serverAgent(t), created.ID)
	require.NoError(t, err)
	assert.Equal(t, map[int]string{1: "in_merge_queue"}, f.attemptStatuses(t, created.ID))
}
