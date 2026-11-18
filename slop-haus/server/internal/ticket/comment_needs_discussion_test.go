package ticket_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// A human's comment that also sets or clears needs_discussion, in one
// transaction under the claim lock, against real Postgres.

func (f *fixture) commentFlagging(t *testing.T, ticketID int64, needs bool) *ticket.HumanCommentResult {
	t.Helper()
	result, err := f.svc.CommentSettingNeedsDiscussion(f.ctx, ticketID, f.human, "Plan it first.", needs)
	require.NoError(t, err)
	require.NotNil(t, result.Ticket, "the ticket comes back with the write")
	return result
}

func (f *fixture) lastComment(t *testing.T, ticketID int64) ticket.Comment {
	t.Helper()
	comments, err := f.svc.ListComments(f.ctx, ticketID)
	require.NoError(t, err)
	require.NotEmpty(t, comments)
	return comments[len(comments)-1]
}

// The race the combined write exists for. The comment's transaction is held
// between its insert and the flag write while an agent claims the ticket --
// as a ticket_wait wake-up would. The claim must wait on the claim lock
// rather than take the ticket without the flag; once the write commits, the
// claim it was waiting on sees the flag.
//
// Verified: with the claim lock taken out of InTransition, the claim
// finishes inside the window and is handed the ticket unflagged.
func TestCommentSettingNeedsDiscussion_NoClaimSeesTheCommentWithoutTheFlag(t *testing.T) {
	f := newFixture(t)
	a := f.agent(t, "waiting")
	open := f.ticket(t, "Loose ticket", "server/")

	inWindow, leaveWindow := make(chan struct{}), make(chan struct{})
	restore := ticket.SetCommentBarrier(func() {
		close(inWindow)
		<-leaveWindow
	})
	defer restore()

	commented := make(chan error, 1)
	go func() {
		_, err := f.svc.CommentSettingNeedsDiscussion(f.ctx, open.ID, f.human, "Plan it first.", true)
		commented <- err
	}()
	<-inWindow

	type claimed struct {
		result *ticket.ClaimResult
		err    error
	}
	claimDone := make(chan claimed, 1)
	go func() {
		result, err := f.svc.Claim(f.ctx, a, &open.ID)
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
	require.NoError(t, <-commented)
	require.Nil(t, early, "a claim finished between the comment and the flag")

	got := <-claimDone
	require.NoError(t, got.err)
	require.NotNil(t, got.result.Ticket)
	assert.True(t, got.result.Ticket.NeedsDiscussion, "the claim that waited sees the flag")
}

// Answering a discussion with the flag set: back in the queue flagged, the
// asker keeping first claim, and its claim telling it to plan and ask.
func TestCommentSettingNeedsDiscussion_OnADiscussionRequeuesItFlagged(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	asker, other := f.agent(t, "asker"), f.agent(t, "other")
	created := f.ticket(t, "Loose ticket", "server/")
	f.claim(t, asker)
	require.False(t, f.discuss(t, asker).NeedsDiscussion)

	result := f.commentFlagging(t, created.ID, true)

	assert.Equal(t, ticket.StatusOpen, result.Ticket.Status)
	assert.True(t, result.Ticket.NeedsDiscussion)
	require.NotNil(t, result.Ticket.ResumeAgentID)
	assert.Equal(t, asker, *result.Ticket.ResumeAgentID)
	require.NotNil(t, result.Ticket.AffinityUntil)
	assert.Equal(t, "Plan it first.", f.lastComment(t, created.ID).Body)

	assert.Nil(t, f.claim(t, other), "the asker keeps first claim")
	resumed := f.claim(t, asker)
	require.NotNil(t, resumed)
	assert.Equal(t, created.ID, resumed.ID)
	assert.True(t, resumed.NeedsDiscussion, "the asker is told to plan and ask, not build")
}

// A discussion answered with the dependency it picked up still unfinished
// goes back blocked, flag and all -- the same rule as a plain answer.
func TestCommentSettingNeedsDiscussion_OnADiscussionWithAPendingDependencyIsBlocked(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	asker := f.agent(t, "asker")
	created := f.ticket(t, "Loose ticket", "server/")
	f.claim(t, asker)
	f.discuss(t, asker)
	f.blockedBy(t, created.ID, f.ticket(t, "Prerequisite", "docs/").ID)

	result := f.commentFlagging(t, created.ID, true)

	assert.Equal(t, ticket.StatusBlocked, result.Ticket.Status)
	assert.True(t, result.Ticket.NeedsDiscussion)
}

// An agent building the ticket never reads the flag again, so setting it
// takes the ticket back: the lease ends, the agent is told to stop, and the
// next claim -- that same agent's included -- plans instead of builds.
func TestCommentSettingNeedsDiscussion_WithdrawsTheTicketFromTheAgentWorkingIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder")
	held := f.ticket(t, "Being built", "server/")
	f.claim(t, a)

	result := f.commentFlagging(t, held.ID, true)

	assert.Equal(t, ticket.StatusOpen, result.Ticket.Status)
	assert.True(t, result.Ticket.NeedsDiscussion)
	assert.Equal(t, "Plan it first.", f.lastComment(t, held.ID).Body)
	_, err := f.svc.LeaseFor(f.ctx, a)
	assert.ErrorIs(t, err, ticket.ErrNoLease, "the lease is ended")

	var revoked *ticket.LeaseRevokedError
	require.ErrorAs(t, f.svc.Heartbeat(f.ctx, a), &revoked, "the agent is told, not kept on")
	assert.Equal(t, held.ID, revoked.TicketID)
	assert.Contains(t, revoked.Error(), "stop working on it")
	_, err = f.svc.Done(f.ctx, a, &ticket.DoneParams{Summary: "finished anyway"})
	assert.ErrorIs(t, err, ticket.ErrNoLease, "and cannot finish it")

	again := f.claim(t, a)
	require.NotNil(t, again)
	assert.Equal(t, held.ID, again.ID)
	assert.True(t, again.NeedsDiscussion)
}

// Requeued from under an agent by the same rule as every other requeue:
// blocked while a dependency added during the work is unfinished.
func TestCommentSettingNeedsDiscussion_AWithdrawnTicketWithAPendingDependencyIsBlocked(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder")
	held := f.ticket(t, "Being built", "server/")
	f.claim(t, a)
	f.blockedBy(t, held.ID, f.ticket(t, "Prerequisite", "docs/").ID)

	result := f.commentFlagging(t, held.ID, true)

	assert.Equal(t, ticket.StatusBlocked, result.Ticket.Status)
	var revoked *ticket.LeaseRevokedError
	require.ErrorAs(t, f.svc.Heartbeat(f.ctx, a), &revoked)
	assert.Equal(t, ticket.StatusBlocked, revoked.Status)
}

func TestCommentSettingNeedsDiscussion_OnAnOpenTicketLandsBoth(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	open := f.ticket(t, "Loose ticket", "server/")

	result := f.commentFlagging(t, open.ID, true)

	assert.Equal(t, ticket.StatusOpen, result.Ticket.Status)
	assert.True(t, result.Ticket.NeedsDiscussion)
	assert.True(t, f.get(t, open.ID).NeedsDiscussion)
	c := f.lastComment(t, open.ID)
	assert.Equal(t, result.Comment.ID, c.ID)
	require.NotNil(t, c.AuthorUserID)
	assert.Equal(t, f.human, *c.AuthorUserID)
}

// Clearing is "that's settled, go build": the same write with false.
func TestCommentSettingNeedsDiscussion_ClearsTheFlag(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "asker")
	flagged, err := f.svc.Create(f.ctx, &ticket.CreateParams{
		Title: "Rough idea", NeedsDiscussion: true, Paths: []model.Path{{Path: "server/"}},
	})
	require.NoError(t, err)

	result := f.commentFlagging(t, flagged.ID, false)
	assert.False(t, result.Ticket.NeedsDiscussion)
	assert.False(t, f.get(t, flagged.ID).NeedsDiscussion)

	// And on a discussion: requeued with the flag cleared, so the resumed
	// claim builds.
	f.commentFlagging(t, flagged.ID, true)
	f.claim(t, a)
	f.discuss(t, a)
	f.commentFlagging(t, flagged.ID, true)
	resumed := f.claim(t, a)
	require.NotNil(t, resumed)
	require.True(t, resumed.NeedsDiscussion)
	f.discuss(t, a)
	result = f.commentFlagging(t, flagged.ID, false)
	assert.Equal(t, ticket.StatusOpen, result.Ticket.Status)
	resumed = f.claim(t, a)
	require.NotNil(t, resumed)
	assert.False(t, resumed.NeedsDiscussion)
}

// Clearing the flag is not a reason to stop an agent: one planning can
// carry on and ask as it would have.
func TestCommentSettingNeedsDiscussion_ClearingLeavesTheAgentWorking(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "planner")
	flagged, err := f.svc.Create(f.ctx, &ticket.CreateParams{
		Title: "Rough idea", NeedsDiscussion: true, Paths: []model.Path{{Path: "server/"}},
	})
	require.NoError(t, err)
	f.claim(t, a)

	result := f.commentFlagging(t, flagged.ID, false)

	assert.Equal(t, ticket.StatusInProgress, result.Ticket.Status)
	assert.NoError(t, f.svc.Heartbeat(f.ctx, a))
}

// A refused write leaves nothing behind: not the flag without the comment.
func TestCommentSettingNeedsDiscussion_RefusalsWriteNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	open := f.ticket(t, "Loose ticket", "server/")

	_, err := f.svc.CommentSettingNeedsDiscussion(f.ctx, open.ID, f.human, "", true)
	assert.ErrorIs(t, err, ticket.ErrEmptyComment)
	assert.False(t, f.get(t, open.ID).NeedsDiscussion)

	_, err = f.svc.CommentSettingNeedsDiscussion(f.ctx, 9999, f.human, "Plan it first.", true)
	assert.ErrorIs(t, err, ticket.ErrNotFound)
}

// Over the wire: the flag rides on the comment, and the response carries
// the ticket the write left; without it the response is the comment alone,
// as before.
func TestCreateTicketComment_NeedsDiscussionWire(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	asker := f.agent(t, "asker")
	created := f.ticket(t, "Loose ticket", "server/")
	f.claim(t, asker)
	f.discuss(t, asker)
	path := fmt.Sprintf("/api/tickets/%d/comments", created.ID)

	rec := send(t, mux, http.MethodPost, path, `{"body": "Plan it first.", "needs_discussion": true}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var got ticket.CreatedCommentBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "Plan it first.", got.Body)
	require.NotNil(t, got.AuthorUserID)
	assert.Equal(t, f.human.String(), *got.AuthorUserID)
	require.NotNil(t, got.Ticket)
	assert.Equal(t, ticket.StatusOpen, got.Ticket.Status)
	assert.True(t, got.Ticket.NeedsDiscussion)

	rec = send(t, mux, http.MethodPost, path, `{"body": "Settled: go flat.", "needs_discussion": false}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.NotNil(t, got.Ticket)
	assert.False(t, got.Ticket.NeedsDiscussion)

	rec = send(t, mux, http.MethodPost, path, `{"body": "Just a note."}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), `"ticket"`)

	rec = send(t, mux, http.MethodPost, path, `{"body": "", "needs_discussion": true}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
}

// Only a signed-in person: an agent's token is refused, and nothing lands.
func TestCreateTicketComment_AnAgentCannotSetNeedsDiscussion(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	mux, _ := f.rolesMux(t, true)
	open := f.ticket(t, "Loose ticket", "server/")

	rec := sendAs(t, mux, http.MethodPost, fmt.Sprintf("/api/tickets/%d/comments", open.ID), rolesAgentToken,
		`{"body": "Plan it first.", "needs_discussion": true}`)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	assert.False(t, f.get(t, open.ID).NeedsDiscussion)
	comments, err := f.svc.ListComments(f.ctx, open.ID)
	require.NoError(t, err)
	assert.Empty(t, comments)
}

// Another tenant's ticket is not found, and its flag is left alone.
func TestCommentSettingNeedsDiscussion_AnotherTenantsTicketIsNotFound(t *testing.T) {
	t.Parallel()
	a, b := newTwoTenants(t)
	human := dbtest.NewUser(t, a.database)
	theirs := b.ticket(t, "Tenant B's ticket", "server/")

	_, err := a.svc.CommentSettingNeedsDiscussion(a.ctx, theirs.ID, human, "Plan it first.", true)
	assert.ErrorIs(t, err, ticket.ErrNotFound)
	got, err := b.svc.Get(b.ctx, theirs.ID)
	require.NoError(t, err)
	assert.False(t, got.NeedsDiscussion)
}
