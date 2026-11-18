package ticket_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// The PR review lifecycle against real Postgres: an agent hands its PR to a
// human, the human approves it or asks for changes, and the ticket comes
// back -- first to its author -- to be merged or reworked.

const reviewPR = "https://github.example/acme/repo/pull/12"

func (f *fixture) gated(t *testing.T, title string, paths ...string) *ticket.Ticket {
	t.Helper()
	scopes := make([]model.Path, 0, len(paths))
	for _, p := range paths {
		scopes = append(scopes, model.Path{Path: p})
	}
	created, err := f.svc.Create(f.ctx, &ticket.CreateParams{Title: title, Paths: scopes, RequiresApproval: true})
	require.NoError(t, err)
	require.True(t, created.RequiresApproval)
	return created
}

func (f *fixture) review(t *testing.T, agentID uuid.UUID) *ticket.Ticket {
	t.Helper()
	reviewed, err := f.svc.Review(f.ctx, agentID, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: reviewPR, Handoff: "PR open on ticket-1-x."})
	require.NoError(t, err)
	return reviewed
}

func (f *fixture) approve(t *testing.T, ticketID int64) *ticket.Ticket {
	t.Helper()
	approved, err := f.svc.Approve(f.ctx, ticketID, f.human)
	require.NoError(t, err)
	return approved
}

func (f *fixture) requestChanges(t *testing.T, ticketID int64) *ticket.Ticket {
	t.Helper()
	sent, err := f.svc.RequestChanges(f.ctx, ticketID, f.human, "Split the handler in two.")
	require.NoError(t, err)
	return sent
}

func TestReview_HandsTheTicketToAHumanWithThePRAndTheHandoff(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	created := f.gated(t, "Gated", "server/")
	f.claim(t, a)
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, a))

	reviewed := f.review(t, a)

	assert.Equal(t, ticket.StatusReview, reviewed.Status)
	require.NotNil(t, reviewed.PRURL)
	assert.Equal(t, reviewPR, *reviewed.PRURL)
	require.NotNil(t, reviewed.ResumeAgentID)
	assert.Equal(t, a, *reviewed.ResumeAgentID, "the author is owed the ticket back")
	assert.Nil(t, reviewed.ClosedAt)
	_, err := f.svc.LeaseFor(f.ctx, a)
	assert.ErrorIs(t, err, ticket.ErrNoLease, "the lease goes back with the ticket")
	assert.Zero(t, f.lockCount(t), "and the integration lock with it")

	handoffs, err := f.svc.ListAgentContexts(f.ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, handoffs, 1)
	assert.Equal(t, "PR open on ticket-1-x.", handoffs[0].Body)
	assert.Equal(t, a, handoffs[0].AgentID)
}

// All of it or none of it: a refused review leaves the agent holding its
// ticket, with nothing half-written.
func TestReview_RefusesWithoutAValidPROrAHandoff(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	created := f.gated(t, "Gated", "server/")
	f.claim(t, a)

	for name, tc := range map[string]struct {
		params ticket.ReviewParams
		want   error
	}{
		"no PR":      {ticket.ReviewParams{Reason: "reviewed by a person", Handoff: "h"}, ticket.ErrPRURLRequired},
		"http PR":    {ticket.ReviewParams{Reason: "reviewed by a person", PRURL: "http://github.example/pull/1", Handoff: "h"}, ticket.ErrInvalidPRURL},
		"no handoff": {ticket.ReviewParams{Reason: "reviewed by a person", PRURL: reviewPR, Handoff: "  "}, ticket.ErrEmptyHandoff},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.svc.Review(f.ctx, a, &tc.params)
			assert.ErrorIs(t, err, tc.want)
		})
	}

	held, err := f.svc.LeaseFor(f.ctx, a)
	require.NoError(t, err)
	assert.Equal(t, created.ID, held.TicketID)
	handoffs, err := f.svc.ListAgentContexts(f.ctx, created.ID)
	require.NoError(t, err)
	assert.Empty(t, handoffs)
}

func TestReview_RequiresAHeldTicket(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	f.gated(t, "Gated", "server/")

	_, err := f.svc.Review(f.ctx, a, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: reviewPR, Handoff: "h"})
	assert.ErrorIs(t, err, ticket.ErrNoLease)
}

// Waiting on a person: nobody can claim it until they answer, and tickets
// depending on it stay blocked -- its change is not merged.
func TestReview_IsNotClaimableAndNotTerminal(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	gated := f.gated(t, "Gated", "server/")
	f.claim(t, a)
	dependent := f.ticket(t, "Builds on it", "web/")
	f.blockedBy(t, dependent.ID, gated.ID)

	f.review(t, a)

	assert.Nil(t, f.claim(t, a), "neither the gated ticket nor its dependent is claimable")
	assert.Nil(t, f.claim(t, b))

	f.approve(t, gated.ID)
	f.claim(t, a)
	_, err := f.svc.Done(f.ctx, a, &ticket.DoneParams{PRURL: reviewPR})
	require.NoError(t, err)
	claimed := f.claim(t, b)
	require.NotNil(t, claimed, "merged, its dependent is free")
	assert.Equal(t, dependent.ID, claimed.ID)
}

// An unmerged PR's paths stay reserved: another agent building on them would
// be building on code that is about to change.
func TestReview_KeepsTheTicketsScopesHeld(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	gated := f.gated(t, "Gated", "server/")
	f.claim(t, a)
	f.review(t, a)
	overlapping := f.ticket(t, "Touches the same package", "server/internal/")

	for _, agentID := range []uuid.UUID{a, b} {
		result, err := f.svc.Claim(f.ctx, agentID, nil)
		require.NoError(t, err)
		assert.Nil(t, result.Ticket, "not even the author may build on its own unmerged PR's paths")
		require.NotNil(t, result.Blocked)
		assert.Equal(t, overlapping.ID, result.Blocked.TicketID)
		assert.Equal(t, gated.ID, result.Blocked.ConflictingTicketID)
		assert.Equal(t, a, result.Blocked.HolderAgentID)

		peeked, err := f.svc.Peek(f.ctx, agentID, nil)
		require.NoError(t, err)
		assert.Nil(t, peeked.Ticket, "Peek must agree with Claim")
	}

	other := f.ticket(t, "Elsewhere", "docs/")
	_, err := f.svc.Claim(f.ctx, b, &other.ID)
	require.NoError(t, err)
	extended, err := f.svc.ExtendScope(f.ctx, b, []model.Path{{Path: "server/main.go"}})
	require.NoError(t, err)
	assert.False(t, extended.Granted, "nor extend into them")

	f.approve(t, gated.ID)
	f.claim(t, a)
	_, err = f.svc.Done(f.ctx, a, &ticket.DoneParams{PRURL: reviewPR})
	require.NoError(t, err)
	_, err = f.svc.Release(f.ctx, b, "retry", nil)
	require.NoError(t, err)
	result, err := f.svc.Claim(f.ctx, b, &overlapping.ID)
	require.NoError(t, err)
	assert.NotNil(t, result.Ticket, "merged, the paths are free")
}

// review -> approve -> claim -> done.
func TestReview_ApprovedGoesBackToTheAuthorToMerge(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	author, other := f.agent(t, "author"), f.agent(t, "other")
	gated := f.gated(t, "Gated", "server/")
	f.claim(t, author)
	f.review(t, author)

	approved := f.approve(t, gated.ID)

	assert.Equal(t, ticket.StatusOpen, approved.Status)
	assert.Equal(t, model.ReviewApproved, approved.ReviewOutcome())
	require.NotNil(t, approved.ApprovedByUserID)
	assert.Equal(t, f.human, *approved.ApprovedByUserID)
	assert.NotNil(t, approved.AffinityUntil)
	assert.Nil(t, f.claim(t, other), "the author has first claim")

	claimed := f.claim(t, author)
	require.NotNil(t, claimed)
	assert.Equal(t, gated.ID, claimed.ID)
	assert.Equal(t, model.ReviewApproved, claimed.ReviewOutcome(), "the claim carries the outcome to the agent")

	done, err := f.svc.Done(f.ctx, author, &ticket.DoneParams{Summary: "merged", PRURL: reviewPR})
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusDone, done.Status)
}

// Once the author's first claim lapses, anyone may merge the approved PR.
func TestReview_ApprovedIsAnyonesAfterAffinity(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	author, other := f.agent(t, "author"), f.agent(t, "other")
	gated := f.gated(t, "Gated", "server/")
	f.claim(t, author)
	f.review(t, author)
	f.approve(t, gated.ID)
	f.closeAffinity(t, gated.ID)

	claimed := f.claim(t, other)
	require.NotNil(t, claimed)
	assert.Equal(t, gated.ID, claimed.ID)
}

// review -> request changes -> claim -> review: every cycle needs a fresh
// approval.
func TestReview_ChangesRequestedComesBackForAnotherReview(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	author := f.agent(t, "author")
	gated := f.gated(t, "Gated", "server/")
	f.claim(t, author)
	f.review(t, author)
	f.approve(t, gated.ID)
	f.claim(t, author)
	// Merging main in needed conflict resolution: back for another look.
	f.review(t, author)

	sent := f.requestChanges(t, gated.ID)

	assert.Equal(t, ticket.StatusOpen, sent.Status)
	assert.Equal(t, model.ReviewChangesRequested, sent.ReviewOutcome())
	assert.Nil(t, sent.ApprovedAt, "the earlier approval does not survive")
	assert.Nil(t, sent.ApprovedByUserID)
	comments, err := f.svc.ListComments(f.ctx, gated.ID)
	require.NoError(t, err)
	require.NotEmpty(t, comments)
	last := comments[len(comments)-1]
	assert.Equal(t, "Split the handler in two.", last.Body)
	require.NotNil(t, last.AuthorUserID)
	assert.Equal(t, f.human, *last.AuthorUserID)

	claimed := f.claim(t, author)
	require.NotNil(t, claimed)
	_, err = f.svc.Done(f.ctx, author, &ticket.DoneParams{PRURL: reviewPR})
	assert.ErrorIs(t, err, ticket.ErrNotApproved, "changes requested is not an approval")

	again := f.review(t, author)
	assert.Equal(t, ticket.StatusReview, again.Status)
	assert.Equal(t, model.ReviewNone, again.ReviewOutcome(), "a new cycle starts with no outcome")
}

// Going back into review clears an approval: it was of the change as it was.
func TestReview_ReenteringReviewClearsTheApproval(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	author := f.agent(t, "author")
	gated := f.gated(t, "Gated", "server/")
	f.claim(t, author)
	f.review(t, author)
	f.approve(t, gated.ID)
	f.claim(t, author)

	again := f.review(t, author)

	assert.Nil(t, again.ApprovedAt)
	assert.Nil(t, again.ApprovedByUserID)
}

func TestReview_ApproveAndRequestChangesNeedATicketInReview(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	queued := f.gated(t, "Not in review")

	_, err := f.svc.Approve(f.ctx, queued.ID, f.human)
	assert.ErrorIs(t, err, ticket.ErrNotInReview)
	_, err = f.svc.RequestChanges(f.ctx, queued.ID, f.human, "why")
	assert.ErrorIs(t, err, ticket.ErrNotInReview)
	comments, err := f.svc.ListComments(f.ctx, queued.ID)
	require.NoError(t, err)
	assert.Empty(t, comments, "a refused request leaves no comment behind")
	assert.Equal(t, ticket.StatusOpen, f.status(t, queued.ID))

	_, err = f.svc.Approve(f.ctx, 9999, f.human)
	assert.ErrorIs(t, err, ticket.ErrNotFound)
}

func TestReview_RequestChangesNeedsAComment(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	gated := f.gated(t, "Gated", "server/")
	f.claim(t, a)
	f.review(t, a)

	_, err := f.svc.RequestChanges(f.ctx, gated.ID, f.human, " ")

	assert.ErrorIs(t, err, ticket.ErrEmptyComment)
	assert.Equal(t, ticket.StatusReview, f.status(t, gated.ID))
}

// Back to the queue means open, or blocked while a dependency picked up
// since is unfinished -- never an unconditional open.
func TestReview_ApprovedWithAnUnfinishedDependencyIsBlocked(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	gated := f.gated(t, "Gated", "server/")
	f.claim(t, a)
	f.review(t, a)
	f.blockedBy(t, gated.ID, f.ticket(t, "Prerequisite", "docs/").ID)

	approved := f.approve(t, gated.ID)

	assert.Equal(t, ticket.StatusBlocked, approved.Status)
	assert.Equal(t, model.ReviewApproved, approved.ReviewOutcome())
}

// The server is the only enforcement there is: a requires_approval ticket
// with no approval cannot be closed, and the refusal leaves it held.
func TestDone_RefusesAnUnapprovedGatedTicket(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	gated := f.gated(t, "Gated", "server/")
	f.claim(t, a)

	_, err := f.svc.Done(f.ctx, a, &ticket.DoneParams{Summary: "merged it anyway", PRURL: reviewPR})

	assert.ErrorIs(t, err, ticket.ErrNotApproved)
	assert.Contains(t, err.Error(), "ticket_finish", "the refusal says what to do instead")
	held, err := f.svc.LeaseFor(f.ctx, a)
	require.NoError(t, err)
	assert.Equal(t, gated.ID, held.TicketID, "still held, to put into review")
	assert.Equal(t, ticket.StatusInProgress, f.status(t, gated.ID))
	comments, err := f.svc.ListComments(f.ctx, gated.ID)
	require.NoError(t, err)
	assert.Empty(t, comments, "the summary is not recorded for a refused close")
}

func TestDone_AnUngatedTicketNeedsNoApproval(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	f.ticket(t, "Ungated", "server/")
	f.claim(t, a)

	done, err := f.svc.Done(f.ctx, a, &ticket.DoneParams{PRURL: reviewPR})

	require.NoError(t, err)
	assert.Equal(t, ticket.StatusDone, done.Status)
}

// A person can turn the gate on after filing, and off again.
func TestUpdate_TogglesRequiresApproval(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticket(t, "Filed ungated")
	assert.False(t, created.RequiresApproval, "off unless asked for")

	on := true
	updated, err := f.svc.Update(f.ctx, created.ID, &ticket.UpdateParams{RequiresApproval: &on})
	require.NoError(t, err)
	assert.True(t, updated.RequiresApproval)

	off := false
	updated, err = f.svc.Update(f.ctx, created.ID, &ticket.UpdateParams{RequiresApproval: &off})
	require.NoError(t, err)
	assert.False(t, updated.RequiresApproval)
}

// Out of review and into the queue only through Approve or Request changes,
// which record the outcome the next claim is told. Leaving for somewhere
// outside the queue is fine -- a PR the person merged themselves is done.
func TestUpdate_ATicketInReviewReturnsToTheQueueOnlyThroughAReview(t *testing.T) {
	t.Parallel()
	for _, next := range []ticket.Status{ticket.StatusOpen, ticket.StatusBlocked, ticket.StatusInProgress} {
		t.Run("refuses "+string(next), func(t *testing.T) {
			f := newFixture(t)
			a := f.agent(t, "builder-1")
			gated := f.gated(t, "Gated", "server/")
			f.claim(t, a)
			f.review(t, a)

			_, err := f.setStatus(t, gated.ID, next)
			assert.ErrorIs(t, err, ticket.ErrLeaveReview)
			assert.Equal(t, ticket.StatusReview, f.status(t, gated.ID))
		})
	}
	for _, next := range []ticket.Status{ticket.StatusDone, ticket.StatusCancelled, ticket.StatusPreparing, ticket.StatusDiscussion} {
		t.Run("allows "+string(next), func(t *testing.T) {
			f := newFixture(t)
			a := f.agent(t, "builder-1")
			gated := f.gated(t, "Gated", "server/")
			f.claim(t, a)
			f.review(t, a)

			updated, err := f.setStatus(t, gated.ID, next)
			require.NoError(t, err)
			assert.Equal(t, next, updated.Status)
		})
	}
}

// A person moving an approved ticket back into review by hand starts a new
// cycle, as ticket_finish does.
func TestUpdate_MovingIntoReviewClearsTheOutcome(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	gated := f.gated(t, "Gated", "server/")
	f.claim(t, a)
	f.review(t, a)
	f.approve(t, gated.ID)

	moved, err := f.setStatus(t, gated.ID, ticket.StatusReview)

	require.NoError(t, err)
	assert.Equal(t, model.ReviewNone, moved.ReviewOutcome())
	assert.Nil(t, moved.ApprovedByUserID)
}

// An agent asking a human to read a change it was free to merge must say why:
// the refusal leaves it holding the ticket with nothing written.
func TestReview_AnUngatedTicketNeedsAReason(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	created := f.ticket(t, "Ungated", "server/")
	f.claim(t, a)

	for _, reason := range []string{"", "   "} {
		_, err := f.svc.Review(f.ctx, a, &ticket.ReviewParams{PRURL: reviewPR, Handoff: "h", Reason: reason})
		require.ErrorIs(t, err, ticket.ErrReviewNeedsReason)
	}

	held, err := f.svc.LeaseFor(f.ctx, a)
	require.NoError(t, err)
	assert.Equal(t, created.ID, held.TicketID, "the agent still holds the ticket")
	handoffs, err := f.svc.ListAgentContexts(f.ctx, created.ID)
	require.NoError(t, err)
	assert.Empty(t, handoffs)
}

func TestReview_AnUngatedTicketWithAReasonGoesToReviewAndShowsTheReason(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	created := f.ticket(t, "Ungated", "server/")
	f.claim(t, a)

	reviewed, err := f.svc.Review(f.ctx, a, &ticket.ReviewParams{PRURL: reviewPR, Handoff: "h", Reason: "  touches the auth path "})
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusReview, reviewed.Status)

	var body string
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT body FROM ticket_comments WHERE ticket_id = $1 ORDER BY id DESC LIMIT 1`, created.ID).Scan(&body))
	assert.Equal(t, "Review requested though not required: touches the auth path", body)
}

func TestReview_AGatedTicketNeedsNoReasonAndPostsNone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	created := f.gated(t, "Gated", "server/")
	f.claim(t, a)

	reviewed, err := f.svc.Review(f.ctx, a, &ticket.ReviewParams{PRURL: reviewPR, Handoff: "h", Reason: "unneeded"})
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusReview, reviewed.Status)

	var n int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM ticket_comments WHERE ticket_id = $1`, created.ID).Scan(&n))
	assert.Zero(t, n)
}

// A shadow cannot merge, so review is its only way out and needs no reason.
func TestReview_AnUnpromotedAttemptNeedsNoReason(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, _ := f.promoteTwo(t)
	shadow := f.agent(t, "shadow")
	claimed := f.claim(t, shadow)
	require.Equal(t, id, claimed.ID)

	reviewed, err := f.svc.Review(f.ctx, shadow, &ticket.ReviewParams{PRURL: reviewPR, Handoff: "h"})
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusReview, reviewed.Status)
}
