package ticket_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// Conversations are per attempt: a person's comment on the ticket while
// attempts run reaches every live attempt's agent, one addressed to an attempt
// reaches only its agent, answers and decisions are an attempt's own, and no
// agent is shown a sibling's.

type delivery struct {
	Attempt int
	Body    string
}

// deliveries are the ticket comments told to agentID, oldest first, delivered
// or not.
func (f *fixture) deliveries(t *testing.T, agentID uuid.UUID, ticketID int64) []delivery {
	t.Helper()
	rows, err := f.database.Query(f.ctx, `
		SELECT COALESCE(attempt_number, 0), body FROM agent_messages
		 WHERE agent_id = $1 AND ticket_id = $2 AND kind = 'ticket_comment' ORDER BY id`, agentID, ticketID)
	require.NoError(t, err)
	defer rows.Close()
	var got []delivery
	for rows.Next() {
		var d delivery
		require.NoError(t, rows.Scan(&d.Attempt, &d.Body))
		got = append(got, d)
	}
	require.NoError(t, rows.Err())
	return got
}

func (f *fixture) personComments(t *testing.T, ticketID int64, body string) {
	t.Helper()
	_, err := f.svc.AddComment(f.ctx, ticketID, ticket.UserAuthor(f.human), body)
	require.NoError(t, err)
}

func (f *fixture) personCommentsOn(t *testing.T, ticketID int64, attempt int, body string) {
	t.Helper()
	_, err := f.svc.PostComment(f.ctx, &ticket.HumanComment{
		TicketID: ticketID, UserID: f.human, Body: body,
		Attempt: &ticket.AttemptRef{TicketID: ticketID, Number: attempt},
	})
	require.NoError(t, err)
}

func (f *fixture) detailFor(t *testing.T, agentID uuid.UUID, ticketID int64) *ticket.Detail {
	t.Helper()
	d, err := f.svc.DetailFor(f.ctx, ticketID, agentID)
	require.NoError(t, err)
	return d
}

func commentBodies(d *ticket.Detail) []string {
	out := make([]string, 0, len(d.Comments))
	for _, c := range d.Comments {
		out = append(out, c.Body)
	}
	return out
}

func TestFanOut_ATicketCommentReachesEveryLiveAttemptsHolderAndNoFinishedOne(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, a1, a2 := f.running(t)
	finished := f.agent(t, "finished")
	f.addAttempt(t, id, false)
	require.Equal(t, 3, f.claimOnly(t, finished, id).Attempt.Number)
	f.release(t, finished)
	f.setAttemptStatus(t, id, 3, "done")

	f.personComments(t, id, "Use the flat shape.")

	assert.Equal(t, []delivery{{1, "Use the flat shape."}}, f.deliveries(t, a1, id))
	assert.Equal(t, []delivery{{2, "Use the flat shape."}}, f.deliveries(t, a2, id), "each copy keeps its own attempt")
	assert.Empty(t, f.deliveries(t, finished, id), "a finished attempt's agent is told nothing")
	assert.Nil(t, f.commentAttempt(t, id, "Use the flat shape."), "it is the ticket's comment, not an attempt's")
}

func TestFanOut_ACommentMadeForTheOwnerByAConsoleReachesEveryLiveAttempt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, a1, a2 := f.running(t)
	console := f.agent(t, "console")
	f.with(ticket.WithOwnerResolver(f.owners(console)))

	_, err := f.svc.AgentCommentOn(f.ctx, console, id, nil, "Both of you: keep the old name.")
	require.NoError(t, err)

	for attempt, agentID := range map[int]uuid.UUID{1: a1, 2: a2} {
		got := f.deliveries(t, agentID, id)
		require.Len(t, got, 1, "attempt %d", attempt)
		assert.Equal(t, attempt, got[0].Attempt)
		assert.Contains(t, got[0].Body, "keep the old name")
	}
}

func TestFanOut_ACommentAddressedToOneAttemptReachesOnlyItsHolder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, a1, a2 := f.running(t)

	f.personCommentsOn(t, id, 1, "Just you, attempt one.")

	assert.Equal(t, []delivery{{1, "Just you, attempt one."}}, f.deliveries(t, a1, id))
	assert.Empty(t, f.deliveries(t, a2, id))

	console := f.agent(t, "console")
	f.with(ticket.WithOwnerResolver(f.owners(console)))
	_, err := f.svc.AgentCommentOn(f.ctx, console, id, intp(2), "Just attempt two.")
	require.NoError(t, err)
	assert.Len(t, f.deliveries(t, a1, id), 1, "unchanged")
	require.Len(t, f.deliveries(t, a2, id), 1)
	assert.Equal(t, 2, f.deliveries(t, a2, id)[0].Attempt)
}

func TestFanOut_TheCapAndTheCutApplyToEachHolder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, a1, a2 := f.running(t)
	for i := range 20 {
		f.personComments(t, id, fmt.Sprintf("filler %d", i))
	}
	_, err := f.database.Exec(f.ctx, `UPDATE agent_messages SET delivered_at = now() WHERE agent_id = $1`, a2)
	require.NoError(t, err)

	f.personComments(t, id, strings.Repeat("x", 4100))

	require.Len(t, f.deliveries(t, a1, id), 20, "a1 is at its cap: it gets no 21st")
	got := f.deliveries(t, a2, id)
	require.Len(t, got, 21, "a2 had delivered its backlog, and gets its copy")
	last := got[len(got)-1].Body
	assert.True(t, strings.HasSuffix(last, "[comment cut short -- read the rest on the ticket]"))
	assert.Less(t, len(last), 4100, "the cut applies per copy")
}

func TestFanOut_WithOneAttemptDeliversAsItAlwaysDid(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "only")
	made := f.ticket(t, "One attempt", "a/")
	f.personComments(t, made.ID, "Before work: the spec conversation.")
	require.NotNil(t, f.claim(t, a))

	f.personComments(t, made.ID, "While it runs.")

	assert.Equal(t, []delivery{{1, "While it runs."}}, f.deliveries(t, a, made.ID), "nothing before the lease")
}

func TestDiscussions_EachAttemptAsksAndIsAnsweredOnItsOwn(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, a1, a2 := f.running(t)
	_, err := f.svc.Discuss(f.ctx, a1, "Attempt one asks: which shape?", "handoff one")
	require.NoError(t, err)
	_, err = f.svc.Discuss(f.ctx, a2, "Attempt two asks: which name?", "handoff two")
	require.NoError(t, err)
	require.Equal(t, map[int]string{1: "discussion", 2: "discussion"}, f.attemptStatuses(t, id))
	require.Equal(t, ticket.StatusDiscussion, f.status(t, id))

	t.Run("a comment naming no attempt answers none", func(t *testing.T) {
		f.personComments(t, id, "A word for everyone.")
		assert.Equal(t, map[int]string{1: "discussion", 2: "discussion"}, f.attemptStatuses(t, id))
	})

	f.personCommentsOn(t, id, 1, "Attempt one: the flat shape.")

	assert.Equal(t, map[int]string{1: "queued", 2: "discussion"}, f.attemptStatuses(t, id), "only attempt 1 is requeued")
	assert.Equal(t, ticket.StatusDiscussion, f.status(t, id), "the ticket still shows what waits on a person")
	f.closeAffinity(t, id)
	require.Equal(t, 1, f.claimOnly(t, a1, id).Attempt.Number, "attempt 1 is claimable again")
	assert.Equal(t, map[int]string{1: "in_progress", 2: "discussion"}, f.attemptStatuses(t, id))

	t.Run("attempt 2's agent sees neither attempt 1's question nor its answer nor its handoff", func(t *testing.T) {
		d := f.detailFor(t, a2, id)
		bodies := strings.Join(commentBodies(d), "\n")
		assert.Contains(t, bodies, "A word for everyone.", "the ticket's own, given to every attempt")
		assert.Contains(t, bodies, "Attempt two asks: which name?")
		assert.NotContains(t, bodies, "Attempt one asks")
		assert.NotContains(t, bodies, "Attempt one: the flat shape.")
		require.NotNil(t, d.Handoff)
		assert.Equal(t, "handoff two", d.Handoff.Body)
	})
	t.Run("attempt 1's agent sees its own and not attempt 2's", func(t *testing.T) {
		d := f.detailFor(t, a1, id)
		bodies := strings.Join(commentBodies(d), "\n")
		assert.Contains(t, bodies, "Attempt one asks")
		assert.Contains(t, bodies, "Attempt one: the flat shape.")
		assert.NotContains(t, bodies, "Attempt two asks")
		require.NotNil(t, d.Handoff)
		assert.Equal(t, "handoff one", d.Handoff.Body)
	})
	t.Run("a person on the board sees everything", func(t *testing.T) {
		d, err := f.svc.Detail(f.ctx, id)
		require.NoError(t, err)
		bodies := strings.Join(commentBodies(d), "\n")
		assert.Contains(t, bodies, "Attempt one asks")
		assert.Contains(t, bodies, "Attempt two asks")
	})
}

func TestDiscussions_AnAnswerToTheSiblingRequeuesTheSiblingAlone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, a1, a2 := f.running(t)
	_, err := f.svc.Discuss(f.ctx, a1, "Attempt one asks.", "handoff one")
	require.NoError(t, err)
	_, err = f.svc.Discuss(f.ctx, a2, "Attempt two asks.", "handoff two")
	require.NoError(t, err)

	f.personCommentsOn(t, id, 2, "Attempt two: the flat name.")

	assert.Equal(t, map[int]string{1: "discussion", 2: "queued"}, f.attemptStatuses(t, id), "the promoted attempt still waits")
	assert.Equal(t, ticket.StatusDiscussion, f.status(t, id))
}

func TestVisibility_AnAgentWithNoAttemptOfItsOwnSeesTheTicketsOwnAlone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, _, _ := f.running(t)
	f.personComments(t, id, "Guidance for all.")
	f.personCommentsOn(t, id, 1, "Private to one.")
	outsider := f.agent(t, "outsider")

	bodies := commentBodies(f.detailFor(t, outsider, id))

	assert.Equal(t, []string{"Guidance for all."}, bodies)
}

func TestVisibility_AWorkerOfASingleAttemptTicketSeesItAll(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "only")
	made := f.ticket(t, "One attempt", "a/")
	f.personComments(t, made.ID, "spec talk")
	require.NotNil(t, f.claim(t, a))
	f.personComments(t, made.ID, "while it runs")
	other := f.agent(t, "reader")

	assert.Equal(t, []string{"spec talk", "while it runs"}, commentBodies(f.detailFor(t, a, made.ID)))
	assert.Equal(t, []string{"spec talk", "while it runs"}, commentBodies(f.detailFor(t, other, made.ID)),
		"with no sibling there is nothing to keep apart")
}

func TestVisibility_ADiscussionAgentStillSeesItsOwnConversationWhileItWaits(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, a1, a2 := f.running(t)
	_, err := f.svc.Discuss(f.ctx, a1, "Attempt one asks.", "handoff one")
	require.NoError(t, err)
	_, err = f.svc.Discuss(f.ctx, a2, "Attempt two asks.", "handoff two")
	require.NoError(t, err)

	// Neither holds a lease while it waits; each is owed its attempt back.
	assert.Equal(t, []string{"Attempt one asks."}, commentBodies(f.detailFor(t, a1, id)))
	assert.Equal(t, []string{"Attempt two asks."}, commentBodies(f.detailFor(t, a2, id)))
}

func TestReview_ADecisionOnOneAttemptAppliesToThatAttemptOnly(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, a1, a2 := f.running(t)
	f.review(t, a1)
	f.review(t, a2)
	require.Equal(t, map[int]string{1: "review", 2: "review"}, f.attemptStatuses(t, id))
	ref2 := &ticket.AttemptRef{TicketID: id, Number: 2}

	_, err := f.svc.RequestChangesAttempt(f.ctx, id, ref2, f.human, "Split attempt two.")
	require.NoError(t, err)

	assert.Equal(t, map[int]string{1: "review", 2: "queued"}, f.attemptStatuses(t, id), "attempt 1 is still in review")
	assert.Equal(t, ticket.StatusReview, f.status(t, id))
	require.NotNil(t, f.commentAttempt(t, id, "Split attempt two."))
	assert.Equal(t, 2, *f.commentAttempt(t, id, "Split attempt two."), "the request is attempt 2's comment")
	assert.Contains(t, commentBodies(f.detailFor(t, a2, id)), "Split attempt two.", "attempt 2's agent reads it on resuming")
	assert.NotContains(t, commentBodies(f.detailFor(t, a1, id)), "Split attempt two.")

	t.Run("each agent is told its own attempt's outcome", func(t *testing.T) {
		f.closeAffinity(t, id)
		_, err := f.svc.ApproveAttempt(f.ctx, id, &ticket.AttemptRef{TicketID: id, Number: 1}, f.human)
		require.NoError(t, err)
		assert.Equal(t, model.ReviewApproved, f.detailFor(t, a1, id).Ticket.ReviewOutcome())
		assert.Equal(t, model.ReviewChangesRequested, f.detailFor(t, a2, id).Ticket.ReviewOutcome(),
			"attempt 2 is not read as approved because attempt 1 is")
	})
	t.Run("the other's decision is a refusal where it is not in review", func(t *testing.T) {
		_, err := f.svc.ApproveAttempt(f.ctx, id, ref2, f.human)
		require.ErrorIs(t, err, ticket.ErrNotInReview, "attempt 2 was sent back, not left in review")
	})
}

func TestNeedsDiscussion_GatesEveryAttempt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, a1, a2 := f.running(t)
	_, err := f.database.Exec(f.ctx, `UPDATE tickets SET needs_discussion = true WHERE id = $1`, id)
	require.NoError(t, err)

	for attempt, agentID := range map[int]uuid.UUID{1: a1, 2: a2} {
		_, err := f.svc.Done(f.ctx, agentID, &ticket.DoneParams{Summary: "built it anyway", PRURL: reviewPR})
		require.ErrorIs(t, err, ticket.ErrNeedsDiscussion, "done, attempt %d", attempt)
		_, err = f.svc.Review(f.ctx, agentID, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: reviewPR, Handoff: "PR open"})
		require.ErrorIs(t, err, ticket.ErrNeedsDiscussion, "review, attempt %d", attempt)
	}
	assert.Equal(t, map[int]string{1: "in_progress", 2: "in_progress"}, f.attemptStatuses(t, id), "both stay held")
}

func TestVisibility_AnAttemptOwedToNobodyIsNoOutsidersToSee(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, a1, _ := f.running(t)
	_, err := f.svc.Discuss(f.ctx, a1, "Attempt one asks.", "handoff one")
	require.NoError(t, err)
	f.personCommentsOn(t, id, 2, "Private to two.")
	outsider := f.agent(t, "outsider")

	// Attempt 1 is owed to a1; attempt 2 is owed to no one, which is not the
	// outsider.
	d := f.detailFor(t, outsider, id)
	assert.Empty(t, commentBodies(d))
	assert.Nil(t, d.Handoff)
}

func TestVisibility_AnAgentWithNoAttemptIsNotShownThePromotedOnesReview(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, a1, _ := f.running(t)
	f.review(t, a1)
	_, err := f.svc.ApproveAttempt(f.ctx, id, &ticket.AttemptRef{TicketID: id, Number: 1}, f.human)
	require.NoError(t, err)
	outsider := f.agent(t, "outsider")

	seen := f.detailFor(t, outsider, id).Ticket

	assert.Nil(t, seen.PRURL)
	assert.Equal(t, model.ReviewNone, seen.ReviewOutcome())
}

func TestFanOut_ALiveSiblingOfAFinishedPromotedAttemptIsToldTheTicketsComment(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, a1, a2 := f.running(t)
	f.release(t, a1)
	f.setAttemptStatus(t, id, 1, "done")

	f.personComments(t, id, "Keep going on the second.")

	assert.Equal(t, []delivery{{2, "Keep going on the second."}}, f.deliveries(t, a2, id))
	assert.Equal(t, []string{"Keep going on the second."}, commentBodies(f.detailFor(t, a2, id)))
}

func TestDetailFor_NamesTheAttemptTheReaderHolds(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, a1, a2 := f.running(t)
	stranger := f.agent(t, "stranger")

	require.NotNil(t, f.detailFor(t, a1, id).Held)
	assert.Equal(t, 1, f.detailFor(t, a1, id).Held.Number)
	assert.Equal(t, 2, f.detailFor(t, a2, id).Held.Number, "each holder is told its own attempt, not a sibling's")
	assert.Nil(t, f.detailFor(t, stranger, id).Held, "a reader with no lease holds nothing")
}
