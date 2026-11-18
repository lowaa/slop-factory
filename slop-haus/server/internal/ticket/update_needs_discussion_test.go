package ticket_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// needs_discussion written through the ticket's own edit, against real
// Postgres: the same effect the comment route has (comment_needs_discussion_test.go).

func (f *fixture) updateFlagging(t *testing.T, ticketID int64, params *ticket.UpdateParams) *ticket.Ticket {
	t.Helper()
	updated, err := f.svc.Update(f.ctx, ticketID, params)
	require.NoError(t, err)
	return updated
}

func flagged(needs bool) *ticket.UpdateParams {
	return &ticket.UpdateParams{NeedsDiscussion: &needs}
}

// requireWithdrawn asserts agentID was taken off ticketID and told so, as
// the status the ticket landed on.
func (f *fixture) requireWithdrawn(t *testing.T, agentID uuid.UUID, ticketID int64, landed ticket.Status) {
	t.Helper()
	_, err := f.svc.LeaseFor(f.ctx, agentID)
	assert.ErrorIs(t, err, ticket.ErrNoLease, "the lease is ended")
	var revoked *ticket.LeaseRevokedError
	require.ErrorAs(t, f.svc.Heartbeat(f.ctx, agentID), &revoked, "the agent is told, not kept on")
	assert.Equal(t, ticketID, revoked.TicketID)
	assert.Equal(t, landed, revoked.Status)
}

// An agent building the ticket never reads the flag again, so setting it on
// the edit takes the ticket back, as the comment route does.
//
// Verified: with Update writing the flag as a plain field again, the agent
// keeps its lease and the ticket stays in progress.
func TestUpdateSettingNeedsDiscussion_WithdrawsTheTicketFromTheAgentWorkingIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder")
	held := f.ticket(t, "Being built", "server/")
	f.claim(t, a)

	updated := f.updateFlagging(t, held.ID, flagged(true))

	assert.Equal(t, ticket.StatusOpen, updated.Status)
	assert.True(t, updated.NeedsDiscussion)
	f.requireWithdrawn(t, a, held.ID, ticket.StatusOpen)
	_, err := f.svc.Done(f.ctx, a, &ticket.DoneParams{Summary: "finished anyway"})
	assert.ErrorIs(t, err, ticket.ErrNoLease, "and cannot finish it")

	again := f.claim(t, a)
	require.NotNil(t, again)
	assert.Equal(t, held.ID, again.ID)
	assert.True(t, again.NeedsDiscussion, "the next claim plans instead of builds")
}

// The edit's flag write and the withdrawal are one transaction under the
// claim lock. A claim is held inside its transaction, under that lock,
// while the edit is sent: the edit must wait -- writing nothing meanwhile --
// and then take back the ticket the claim handed out. An edit that wrote the
// flag outside the lock would land in the window and leave the claiming
// agent building a ticket flagged to be planned.
//
// Verified both ways it can break: with Update writing the flag as a plain
// field, the edit finishes inside the claim; writing it that way and then
// withdrawing in a second transaction, the flag is seen written while the
// claim is still open.
func TestUpdateSettingNeedsDiscussion_NoClaimSlipsBetweenTheFlagAndTheWithdrawal(t *testing.T) {
	f := newFixture(t)
	a := f.agent(t, "claiming")
	open := f.ticket(t, "Loose ticket", "server/")

	inClaim, leaveClaim := make(chan struct{}), make(chan struct{})
	restore := ticket.SetClaimBarrier(func() {
		close(inClaim)
		<-leaveClaim
	})
	defer restore()

	claimed := make(chan error, 1)
	go func() {
		_, err := f.svc.Claim(f.ctx, a, &open.ID)
		claimed <- err
	}()
	<-inClaim

	updated := make(chan error, 1)
	go func() {
		_, err := f.svc.Update(f.ctx, open.ID, flagged(true))
		updated <- err
	}()

	// Observed states, not a sleep standing in for one: the edit is either
	// queued on the claim lock (correct) or already back (the race).
	var early error
	finished := false
	deadline := time.Now().Add(10 * time.Second)
	for !finished && f.lockWaiters(t) == 0 {
		select {
		case early = <-updated:
			finished = true
		case <-time.After(5 * time.Millisecond):
		}
		require.True(t, time.Now().Before(deadline), "the edit neither waited nor finished")
	}
	flaggedMidClaim := f.get(t, open.ID).NeedsDiscussion
	close(leaveClaim)
	require.NoError(t, <-claimed)
	require.False(t, finished, "the edit finished inside the claim: %v", early)
	assert.False(t, flaggedMidClaim, "the flag was written while the claim held the lock")

	require.NoError(t, <-updated)
	got := f.get(t, open.ID)
	assert.Equal(t, ticket.StatusOpen, got.Status)
	assert.True(t, got.NeedsDiscussion)
	f.requireWithdrawn(t, a, open.ID, ticket.StatusOpen)
}

// Requeued by the rule every requeue uses: blocked while a dependency added
// during the work is unfinished.
func TestUpdateSettingNeedsDiscussion_AWithdrawnTicketWithAPendingDependencyIsBlocked(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder")
	held := f.ticket(t, "Being built", "server/")
	f.claim(t, a)
	f.blockedBy(t, held.ID, f.ticket(t, "Prerequisite", "docs/").ID)

	updated := f.updateFlagging(t, held.ID, flagged(true))

	assert.Equal(t, ticket.StatusBlocked, updated.Status)
	f.requireWithdrawn(t, a, held.ID, ticket.StatusBlocked)
}

// The flag's effect is decided on the ticket as the edit's status move left
// it: an edit that keeps a held ticket in progress while flagging it still
// takes it back, rather than the move's "in progress keeps its lease"
// letting the flag through unseen.
func TestUpdateSettingNeedsDiscussion_WithAStatusThatKeepsTheLeaseStillWithdraws(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder")
	held := f.ticket(t, "Being built", "server/")
	f.claim(t, a)

	inProgress, needs := ticket.StatusInProgress, true
	updated := f.updateFlagging(t, held.ID, &ticket.UpdateParams{Status: &inProgress, NeedsDiscussion: &needs})

	assert.Equal(t, ticket.StatusOpen, updated.Status)
	assert.True(t, updated.NeedsDiscussion)
	f.requireWithdrawn(t, a, held.ID, ticket.StatusOpen)
}

// A move that takes the ticket out of the queue lands where it was sent,
// flagged: the flag's requeue is only for a ticket still in progress.
func TestUpdateSettingNeedsDiscussion_WithAStatusMoveLandsWhereItWasSent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder")
	held := f.ticket(t, "Being built", "server/")
	f.claim(t, a)

	preparing, needs := ticket.StatusPreparing, true
	updated := f.updateFlagging(t, held.ID, &ticket.UpdateParams{Status: &preparing, NeedsDiscussion: &needs})

	assert.Equal(t, ticket.StatusPreparing, updated.Status)
	assert.True(t, updated.NeedsDiscussion)
	f.requireWithdrawn(t, a, held.ID, ticket.StatusPreparing)
}

// Clearing the flag is not a reason to stop an agent: one planning can
// carry on and ask as it would have.
func TestUpdateSettingNeedsDiscussion_ClearingLeavesTheAgentWorking(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "planner")
	created, err := f.svc.Create(f.ctx, &ticket.CreateParams{
		Title: "Rough idea", NeedsDiscussion: true, Paths: []model.Path{{Path: "server/"}},
	})
	require.NoError(t, err)
	f.claim(t, a)

	updated := f.updateFlagging(t, created.ID, flagged(false))

	assert.Equal(t, ticket.StatusInProgress, updated.Status)
	assert.False(t, updated.NeedsDiscussion)
	assert.NoError(t, f.svc.Heartbeat(f.ctx, a))
}

// Flagging a ticket nobody holds writes the flag and nothing else: it is
// not an answer to a discussion, as a comment would be, and an open ticket
// stays open.
func TestUpdateSettingNeedsDiscussion_OnATicketNobodyHoldsWritesTheFlagOnly(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	asker := f.agent(t, "asker")
	open := f.ticket(t, "Loose ticket", "server/")
	waiting := f.ticket(t, "Waiting on an answer", "docs/")
	_, err := f.svc.Claim(f.ctx, asker, &waiting.ID)
	require.NoError(t, err)
	f.discuss(t, asker)

	updated := f.updateFlagging(t, open.ID, flagged(true))
	assert.Equal(t, ticket.StatusOpen, updated.Status)
	assert.True(t, updated.NeedsDiscussion)

	updated = f.updateFlagging(t, waiting.ID, flagged(true))
	assert.Equal(t, ticket.StatusDiscussion, updated.Status, "an edit is not an answer")
	assert.True(t, updated.NeedsDiscussion)
}

// The other fields of the edit land in the same write, and a ticket that
// does not exist is refused.
func TestUpdateSettingNeedsDiscussion_WritesTheOtherFieldsAndRefusesAMissingTicket(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	open := f.ticket(t, "Loose ticket", "server/")

	high, needs := model.PriorityHigh, true
	updated := f.updateFlagging(t, open.ID, &ticket.UpdateParams{
		Priority: &high, NeedsDiscussion: &needs, Paths: []model.Path{{Path: "web/"}},
	})
	assert.Equal(t, model.PriorityHigh, updated.Priority)
	assert.True(t, updated.NeedsDiscussion)
	require.Len(t, updated.Paths, 1)
	assert.Equal(t, "web/", updated.Paths[0].Path)

	_, err := f.svc.Update(f.ctx, 9999, flagged(true))
	assert.ErrorIs(t, err, ticket.ErrNotFound)
}

// Over the wire: the board's PATCH takes a held ticket back, and the
// response is the ticket as the write left it.
func TestUpdateTicket_NeedsDiscussionWithdrawsOverTheWire(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	a := f.agent(t, "builder")
	held := f.ticket(t, "Being built", "server/")
	f.claim(t, a)

	rec := send(t, mux, http.MethodPatch, fmt.Sprintf("/api/tickets/%d", held.ID), `{"needs_discussion": true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := decodeTicket(t, rec.Body.Bytes())
	assert.Equal(t, ticket.StatusOpen, got.Status)
	assert.True(t, got.NeedsDiscussion)
	f.requireWithdrawn(t, a, held.ID, ticket.StatusOpen)

	rec = send(t, mux, http.MethodPatch, "/api/tickets/9999", `{"needs_discussion": true}`)
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}
