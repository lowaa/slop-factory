package ticket_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// The discussion lifecycle against real Postgres: an agent stops to ask,
// a human answers in the comments, and the ticket comes back -- first to
// the agent that asked.

func (f *fixture) claim(t *testing.T, agentID uuid.UUID) *ticket.Ticket {
	t.Helper()
	result, err := f.svc.Claim(f.ctx, agentID, nil)
	require.NoError(t, err)
	return result.Ticket
}

func (f *fixture) discuss(t *testing.T, agentID uuid.UUID) *ticket.Ticket {
	t.Helper()
	discussed, err := f.svc.Discuss(f.ctx, agentID, "Which API shape?", "Handler half-done on branch t-1.")
	require.NoError(t, err)
	return discussed
}

func (f *fixture) humanReply(t *testing.T, ticketID int64) {
	t.Helper()
	_, err := f.svc.AddComment(f.ctx, ticketID, ticket.UserAuthor(f.human), "Go with the flat shape.")
	require.NoError(t, err)
}

// humanReplyTo is a person's answer addressed to one attempt, which on a
// ticket with several live attempts is the only way to answer one's discussion.
func (f *fixture) humanReplyTo(t *testing.T, ticketID int64, attempt int) {
	t.Helper()
	_, err := f.svc.PostComment(f.ctx, &ticket.HumanComment{
		TicketID: ticketID, UserID: f.human, Body: "Go with the flat shape.",
		Attempt: &ticket.AttemptRef{TicketID: ticketID, Number: attempt},
	})
	require.NoError(t, err)
}

// closeAffinity ends the asking agent's first-claim window without waiting
// it out.
func (f *fixture) closeAffinity(t *testing.T, ticketID int64) {
	t.Helper()
	_, err := f.database.Exec(f.ctx,
		`UPDATE attempts SET affinity_until = now() - interval '1 minute' WHERE ticket_id = $1 AND number = 1`, ticketID)
	require.NoError(t, err)
}

// closeAffinityOf is closeAffinity for one attempt.
func (f *fixture) closeAffinityOf(t *testing.T, ticketID int64, attempt int) {
	t.Helper()
	_, err := f.database.Exec(f.ctx,
		`UPDATE attempts SET affinity_until = now() - interval '1 minute' WHERE ticket_id = $1 AND number = $2`, ticketID, attempt)
	require.NoError(t, err)
}

func TestDiscuss_HandsTheTicketBackWithTheQuestionAndTheHandoff(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	created := f.ticket(t, "Loose ticket", "server/")
	f.claim(t, a)

	discussed := f.discuss(t, a)

	assert.Equal(t, ticket.StatusDiscussion, discussed.Status)
	require.NotNil(t, discussed.ResumeAgentID)
	assert.Equal(t, a, *discussed.ResumeAgentID)
	_, err := f.svc.LeaseFor(f.ctx, a)
	assert.ErrorIs(t, err, ticket.ErrNoLease, "the lease goes back with the ticket")

	comments, err := f.svc.ListComments(f.ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, "Which API shape?", comments[0].Body)
	require.NotNil(t, comments[0].AuthorAgentID)
	assert.Equal(t, a, *comments[0].AuthorAgentID, "the question is the agent's, not the human's")

	handoffs, err := f.svc.ListAgentContexts(f.ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, handoffs, 1)
	assert.Equal(t, "Handler half-done on branch t-1.", handoffs[0].Body)
	assert.Equal(t, a, handoffs[0].AgentID)
}

// All of it or none of it: a refused discussion leaves the agent holding
// its ticket, with nothing half-written.
func TestDiscuss_RefusesWithoutAQuestionOrAHandoff(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	created := f.ticket(t, "Loose ticket", "server/")
	f.claim(t, a)

	_, err := f.svc.Discuss(f.ctx, a, " ", "handoff")
	assert.ErrorIs(t, err, ticket.ErrEmptyQuestion)
	_, err = f.svc.Discuss(f.ctx, a, "question?", "")
	assert.ErrorIs(t, err, ticket.ErrEmptyHandoff)

	held, err := f.svc.LeaseFor(f.ctx, a)
	require.NoError(t, err)
	assert.Equal(t, created.ID, held.TicketID)
	comments, err := f.svc.ListComments(f.ctx, created.ID)
	require.NoError(t, err)
	assert.Empty(t, comments)
}

func TestDiscuss_RequiresAHeldTicket(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	f.ticket(t, "Loose ticket", "server/")

	_, err := f.svc.Discuss(f.ctx, a, "question?", "handoff")

	assert.ErrorIs(t, err, ticket.ErrNoLease)
}

// Waiting on a person, so nobody can pick it up -- not the asker, not
// anyone else -- until the person answers.
func TestDiscussion_TicketIsNotClaimableUntilAHumanAnswers(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	f.ticket(t, "Loose ticket", "server/")
	f.claim(t, a)
	f.discuss(t, a)

	assert.Nil(t, f.claim(t, a))
	assert.Nil(t, f.claim(t, b))
}

// An agent adding to its own question is still waiting on the human.
func TestDiscussion_AnAgentsCommentDoesNotAnswerIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	created := f.ticket(t, "Loose ticket", "server/")
	f.claim(t, a)
	f.discuss(t, a)

	_, err := f.svc.AddComment(f.ctx, created.ID, ticket.AgentAuthor(a), "Also: which error codes?")
	require.NoError(t, err)

	got, err := f.svc.Get(f.ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusDiscussion, got.Status)
}

// The asker is best placed to act on the answer, so it gets first claim;
// anyone else waits out the window.
func TestDiscussion_AHumanAnswerGivesTheAskerFirstClaim(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	created := f.ticket(t, "Loose ticket", "server/")
	f.claim(t, a)
	f.discuss(t, a)

	f.humanReply(t, created.ID)

	got, err := f.svc.Get(f.ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusOpen, got.Status)
	require.NotNil(t, got.AffinityUntil)
	comments, err := f.svc.ListComments(f.ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, comments, 2, "the question, then the answer")
	assert.Equal(t, "Go with the flat shape.", comments[1].Body)
	assert.Nil(t, comments[1].AuthorAgentID, "the answer is the human's")
	require.NotNil(t, comments[1].AuthorUserID)
	assert.Equal(t, f.human, *comments[1].AuthorUserID)

	assert.Nil(t, f.claim(t, b), "another agent waits out the asker's window")
	regained := f.claim(t, a)
	require.NotNil(t, regained)
	assert.Equal(t, created.ID, regained.ID)
	assert.Nil(t, regained.ResumeAgentID, "claiming ends the discussion's hold")
	assert.Nil(t, regained.AffinityUntil)
}

// An asker that has gone quiet must not hold the answer hostage.
func TestDiscussion_AnyAgentMayClaimOnceTheWindowCloses(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	created := f.ticket(t, "Loose ticket", "server/")
	f.claim(t, a)
	f.discuss(t, a)
	f.humanReply(t, created.ID)

	f.closeAffinity(t, created.ID)

	taken := f.claim(t, b)
	require.NotNil(t, taken)
	assert.Equal(t, created.ID, taken.ID)
}

// Nothing is coming back for a disabled agent, so its window does not
// apply.
func TestDiscussion_ADisabledAskerHoldsNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	created := f.ticket(t, "Loose ticket", "server/")
	f.claim(t, a)
	f.discuss(t, a)
	f.humanReply(t, created.ID)

	_, err := f.agents.SetDisabled(f.ctx, a, true)
	require.NoError(t, err)

	taken := f.claim(t, b)
	require.NotNil(t, taken)
	assert.Equal(t, created.ID, taken.ID)
}

// The Stop hook asks Peek whether an agent has work. It must not tell
// another agent to go claim a ticket held for the asker.
func TestDiscussion_PeekRespectsTheAskersWindow(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	created := f.ticket(t, "Loose ticket", "server/")
	f.claim(t, a)
	f.discuss(t, a)
	f.humanReply(t, created.ID)

	forB, err := f.svc.Peek(f.ctx, b, nil)
	require.NoError(t, err)
	assert.Nil(t, forB.Ticket)
	forA, err := f.svc.Peek(f.ctx, a, nil)
	require.NoError(t, err)
	require.NotNil(t, forA.Ticket)
	assert.Equal(t, created.ID, forA.Ticket.ID)
}

// Back to the queue is the same predicate as a release: a ticket that has
// picked up an unfinished dependency comes back blocked, not open.
func TestDiscussion_AnAnsweredTicketWithAPendingDependencyIsBlocked(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	created := f.ticket(t, "Loose ticket", "server/")
	blocker := f.ticket(t, "Prerequisite", "web/")
	f.claim(t, a)
	f.discuss(t, a)
	f.blockedBy(t, created.ID, blocker.ID)

	f.humanReply(t, created.ID)

	got, err := f.svc.Get(f.ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusBlocked, got.Status)
}

// A discussion is not an ending: work blocked on the ticket stays blocked.
func TestDiscussion_DoesNotUnblockTicketsWaitingOnIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	loose := f.ticket(t, "Loose ticket", "server/")
	f.claim(t, a)
	f.discuss(t, a)
	dependent := f.ticket(t, "Needs the loose one", "web/")
	f.blockedBy(t, dependent.ID, loose.ID)

	assert.Nil(t, f.claim(t, b), "the dependent is not claimable while its dependency is in discussion")
}

// The human's comment on a ticket nobody is waiting about changes nothing
// but the thread.
func TestDiscussion_AHumanCommentElsewhereLeavesTheStatusAlone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	created := f.ticket(t, "Ordinary ticket", "server/")
	f.claim(t, a)

	f.humanReply(t, created.ID)

	got, err := f.svc.Get(f.ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusInProgress, got.Status)
	assert.Nil(t, got.AffinityUntil)
}

// The planning pass: a ticket filed as needing discussion is claimed as
// such, and entering discussion is what completes it -- so the claim after
// the answer is a working one.
func TestNeedsDiscussion_IsClearedByTheDiscussionItAskedFor(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	created, err := f.svc.Create(f.ctx, &ticket.CreateParams{
		Title: "Rough idea", NeedsDiscussion: true, Paths: []model.Path{{Path: "server/"}},
	})
	require.NoError(t, err)
	assert.True(t, created.NeedsDiscussion)

	claimed := f.claim(t, a)
	require.NotNil(t, claimed)
	assert.True(t, claimed.NeedsDiscussion, "the claimer is told to plan, not build")

	discussed := f.discuss(t, a)
	assert.False(t, discussed.NeedsDiscussion)
}

// Handoffs accumulate, oldest first: the last is the one to resume from.
func TestDiscussion_HandoffsAccumulateInOrder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	created := f.ticket(t, "Loose ticket", "server/")

	f.claim(t, a)
	_, err := f.svc.Discuss(f.ctx, a, "First question?", "first handoff")
	require.NoError(t, err)
	f.humanReply(t, created.ID)
	f.claim(t, a)
	_, err = f.svc.Discuss(f.ctx, a, "Second question?", "second handoff")
	require.NoError(t, err)

	handoffs, err := f.svc.ListAgentContexts(f.ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, handoffs, 2)
	assert.Equal(t, "first handoff", handoffs[0].Body)
	assert.Equal(t, "second handoff", handoffs[1].Body)
}
