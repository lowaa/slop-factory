package ticket_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// A ticket done through its promoted attempt, with attempt 2 a shadow still
// queued: the shape a done ticket was once left in.
func (f *fixture) doneWithQueuedShadow(t *testing.T) int64 {
	t.Helper()
	promoted := f.agent(t, "promoted")
	made := f.ticket(t, "Done with a shadow", "a/")
	require.NotNil(t, f.claim(t, promoted))
	f.addAttempt(t, made.ID, true)
	_, err := f.svc.Done(f.ctx, promoted, &ticket.DoneParams{Summary: "Done."})
	require.NoError(t, err)
	require.Equal(t, ticket.StatusDone, f.status(t, made.ID))
	return made.ID
}

func TestClaim_ADoneTicketsQueuedShadowIsOfferedAndClaimedAndTheTicketStaysDone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id := f.doneWithQueuedShadow(t)
	shadow := f.agent(t, "shadow")

	probe := f.peek(t, shadow)
	require.NotNil(t, probe.Ticket, "a probe offers it too, so the Stop hook and ticket_wait wake the agent")
	assert.Equal(t, id, probe.Ticket.ID)

	claimed, err := f.svc.Claim(f.ctx, shadow, nil)
	require.NoError(t, err)
	require.NotNil(t, claimed.Ticket)
	assert.Equal(t, 2, f.leasedAttempt(t, shadow))
	assert.Equal(t, ticket.StatusInProgress, claimed.Ticket.Status, "the shadow sees its own status")
	assert.True(t, claimed.Ticket.DoneThroughOther, "and is told the ticket is done through another attempt")
	assert.Equal(t, ticket.StatusDone, f.status(t, id))
	assert.Equal(t, map[int]string{1: "done", 2: "in_progress"}, f.attemptStatuses(t, id))
}

// A shadow in discussion, answered, is requeued, and
// has to be claimable again although its ticket is done.
func TestClaim_ADoneTicketsShadowSurvivesDiscussionAndItsAnswer(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id := f.doneWithQueuedShadow(t)
	shadow, other := f.agent(t, "shadow"), f.agent(t, "other")
	require.NotNil(t, f.claim(t, shadow))

	f.discuss(t, shadow)
	assert.Equal(t, ticket.StatusDone, f.status(t, id), "done outranks a shadow's discussion")
	assert.Nil(t, f.peek(t, other).Ticket, "nothing is claimable while it waits on the human")

	f.humanReplyTo(t, id, 2)
	assert.Equal(t, ticket.StatusDone, f.status(t, id))
	assert.Equal(t, map[int]string{1: "done", 2: "queued"}, f.attemptStatuses(t, id))
	f.closeAffinityOf(t, id, 2)
	require.NotNil(t, f.claim(t, other), "the answered shadow is claimable again")
	assert.Equal(t, 2, f.leasedAttempt(t, other))
	assert.Equal(t, ticket.StatusDone, f.status(t, id))
}

func TestClaim_ADoneTicketWithNoLiveShadowIsNotOffered(t *testing.T) {
	t.Parallel()
	for name, shadowStatus := range map[string]string{"closed": "closed", "done": "done", "review": "review", "discussion": "discussion"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			id := f.doneWithQueuedShadow(t)
			f.setAttemptStatus(t, id, 2, shadowStatus)
			agent := f.agent(t, "idle")

			assert.Nil(t, f.peek(t, agent).Ticket)
			assert.Nil(t, f.claim(t, agent))
			directed, err := f.svc.Claim(f.ctx, agent, &id)
			require.NoError(t, err)
			assert.Nil(t, directed.Ticket, "a directed claim is refused too")
		})
	}
}

// The promoted attempt is never what keeps a done ticket claimable, even if
// its status is not done (a ticket forced to done under it).
func TestClaim_ADoneTicketsPromotedAttemptIsNotOffered(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	made := f.ticket(t, "Forced done", "a/")
	f.forceStatus(t, made.ID, ticket.StatusDone)

	assert.Nil(t, f.peek(t, f.agent(t, "idle")).Ticket)
}

// A shadow of a done ticket is not blocked by dependencies, which a done
// ticket has met; a cooldown still holds it as it holds any attempt.
func TestClaim_ADoneTicketsShadowIgnoresBlockersButNotACooldown(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id := f.doneWithQueuedShadow(t)
	agent := f.agent(t, "shadow")
	_, err := f.database.Exec(f.ctx, `UPDATE tickets SET blocked_until = now() + interval '1 hour' WHERE id = $1`, id)
	require.NoError(t, err)
	assert.Nil(t, f.peek(t, agent).Ticket, "the cooldown holds it")

	_, err = f.database.Exec(f.ctx, `UPDATE tickets SET blocked_until = NULL WHERE id = $1`, id)
	require.NoError(t, err)
	dep := f.ticket(t, "Unfinished", "z/")
	_, err = f.database.Exec(f.ctx, `INSERT INTO ticket_deps (tenant_id, ticket_id, depends_on_ticket_id)
		SELECT tenant_id, id, $2 FROM tickets WHERE id = $1`, id, dep.ID)
	require.NoError(t, err)
	offered := f.peek(t, agent).Ticket
	require.NotNil(t, offered)
	assert.Equal(t, id, offered.ID, "blockers do not apply to a done ticket (the blocker itself is open, so it is not what was offered)")
}

// Replays (attempts added to a done ticket) are not built: a shadow outliving
// its ticket's done must not open the way to adding one.
func TestAddAttempt_ADoneTicketStillTakesNoNewAttemptEvenWithALiveShadow(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id := f.doneWithQueuedShadow(t)

	_, err := f.svc.AddAttempt(f.ctx, f.human, id, nil)
	require.ErrorIs(t, err, ticket.ErrNoAttemptsOnStatus)
	assert.Len(t, f.attemptRows(t, id), 2)
}

func TestClaim_ADoneTicketStaysDoneWhenItsShadowReviewsAndFinishes(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id := f.doneWithQueuedShadow(t)
	shadow := f.agent(t, "shadow")
	require.NotNil(t, f.claim(t, shadow))
	closedAt := func() *time.Time {
		var at *time.Time
		require.NoError(t, f.database.QueryRow(f.ctx, `SELECT closed_at FROM tickets WHERE id = $1`, id).Scan(&at))
		return at
	}
	before := closedAt()
	require.NotNil(t, before)

	_, err := f.svc.Review(f.ctx, shadow, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: reviewPR, Handoff: "PR open."})
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusDone, f.status(t, id))
	assert.Equal(t, map[int]string{1: "done", 2: "review"}, f.attemptStatuses(t, id))

	assert.Nil(t, f.peek(t, f.agent(t, "idle")).Ticket, "a shadow in review is not claimable")
	assert.Equal(t, before, closedAt(), "the ticket's closing time is the promoted attempt's")
}

// The note belongs to a shadow of a done ticket only: not to a shadow of a
// ticket still running, nor to the promoted attempt.
func TestClaim_OnlyAShadowOfADoneTicketIsToldItIsDoneThroughAnother(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	running := f.ticket(t, "Still running", "r/")
	f.addAttempt(t, running.ID, true)
	first, second := f.agent(t, "first"), f.agent(t, "second")

	promoted := f.claim(t, first)
	require.NotNil(t, promoted)
	assert.False(t, promoted.DoneThroughOther, "the promoted attempt")
	shadow := f.claim(t, second)
	require.NotNil(t, shadow)
	assert.False(t, shadow.DoneThroughOther, "a shadow of a ticket that is not done")

	seen, err := f.svc.DetailFor(f.ctx, running.ID, first)
	require.NoError(t, err)
	assert.False(t, seen.Ticket.DoneThroughOther)
}

func TestClaim_ADoneTicketsShadowCanFinishAndTheTicketStaysDone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id := f.doneWithQueuedShadow(t)
	shadow := f.agent(t, "shadow")
	require.NotNil(t, f.claim(t, shadow))

	detail, err := f.svc.DetailFor(f.ctx, id, shadow)
	require.NoError(t, err)
	assert.True(t, detail.Ticket.DoneThroughOther, "ticket_get reads it too")

	_, err = f.svc.Done(f.ctx, shadow, &ticket.DoneParams{Summary: "Shadow finished."})
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusDone, f.status(t, id))
	assert.Equal(t, map[int]string{1: "done", 2: "done"}, f.attemptStatuses(t, id))
	for _, status := range []string{"done", "closed"} {
		f.setAttemptStatus(t, id, 2, status)
		// A finished shadow is no longer held; read it as its conversation owner would.
		seen, err := newStore(f.database).TicketSeenBy(f.ctx, id, ticket.Conversation{Attempt: attemptNumber(2)})
		require.NoError(t, err)
		assert.False(t, seen.DoneThroughOther, "a %s shadow has nothing left to carry on", status)
	}
}

func attemptNumber(n int) *int { return &n }

// Post-merge review is of the promoted attempt's merged change, so a shadow
// still running does not hold it back.
func TestMarkPostReviewed_DoesNotWaitForARunningShadow(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id := f.doneWithQueuedShadow(t)
	require.NotNil(t, f.claim(t, f.agent(t, "shadow")))
	require.Equal(t, map[int]string{1: "done", 2: "in_progress"}, f.attemptStatuses(t, id))
	require.Contains(t, f.awaitingPostReview(t), id)

	marked, err := f.svc.MarkPostReviewed(f.ctx, id, f.human, "")
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusDone, marked.Status)
	assert.NotContains(t, f.awaitingPostReview(t), id)
	assert.Equal(t, map[int]string{1: "done", 2: "in_progress"}, f.attemptStatuses(t, id))
}
