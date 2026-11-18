package ticket_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// Preparing against real Postgres: a status only a person moves a ticket
// out of, which agents may put their own held ticket into, and which takes
// a ticket away from the agent working it when a person puts it there.

func (f *fixture) preparing(t *testing.T, title string, paths ...string) *ticket.Ticket {
	t.Helper()
	scopes := make([]model.Path, 0, len(paths))
	for _, p := range paths {
		scopes = append(scopes, model.Path{Path: p})
	}
	created, err := f.svc.Create(f.ctx, &ticket.CreateParams{Title: title, Paths: scopes, Status: ticket.StatusPreparing})
	require.NoError(t, err)
	require.Equal(t, ticket.StatusPreparing, created.Status)
	return created
}

func (f *fixture) setStatus(t *testing.T, id int64, status ticket.Status) (*ticket.Ticket, error) {
	t.Helper()
	return f.svc.Update(f.ctx, id, &ticket.UpdateParams{Status: &status})
}

func (f *fixture) lockCount(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM locks`).Scan(&n))
	return n
}

func TestCreate_StartingStatusDependsOnWhoFilesIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "filer")

	cases := []struct {
		name   string
		params ticket.CreateParams
		want   ticket.Status
	}{
		// Otherwise the follow-ups one agent spins off are built by others
		// without a person ever having seen them.
		{"an agent's defaults to preparing", ticket.CreateParams{CreatedByAgentID: &a}, ticket.StatusPreparing},
		{"an agent asked to file it open", ticket.CreateParams{CreatedByAgentID: &a, Status: ticket.StatusOpen}, ticket.StatusOpen},
		{"a person's defaults to open", ticket.CreateParams{CreatedByUserID: &f.human}, ticket.StatusOpen},
		{"a person filing it preparing", ticket.CreateParams{CreatedByUserID: &f.human, Status: ticket.StatusPreparing}, ticket.StatusPreparing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := tc.params
			params.Title = tc.name
			created, err := f.svc.Create(f.ctx, &params)
			require.NoError(t, err)
			assert.Equal(t, tc.want, created.Status)

			stored, err := f.svc.Get(f.ctx, created.ID)
			require.NoError(t, err)
			assert.Equal(t, tc.want, stored.Status, "the status is stored, not just echoed")
		})
	}
}

// A ticket starts preparing or open; anything else is a state the claim
// lifecycle reaches, and filing straight into it would skip that.
func TestCreate_RefusesAStartingStatusPastOpen(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	for _, status := range []ticket.Status{ticket.StatusInProgress, ticket.StatusBlocked, ticket.StatusDiscussion, ticket.StatusDone, "nonsense"} {
		_, err := f.svc.Create(f.ctx, &ticket.CreateParams{Title: "x", Status: status})
		assert.ErrorIs(t, err, ticket.ErrInvalidEnum, "status %q", status)
	}
}

// The claim query and the Stop hook's probe both go through
// candidateTickets, which only offers open tickets. These pin that a
// Preparing ticket is never offered -- not as the next ticket, not when
// asked for by id, and not ahead of lower-priority open work.
func TestPreparing_IsNeverClaimable(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	drafting := f.preparing(t, "Still being shaped", "server/")
	urgent := model.PriorityUrgent
	_, err := f.svc.Update(f.ctx, drafting.ID, &ticket.UpdateParams{Priority: &urgent})
	require.NoError(t, err)

	peeked, err := f.svc.Peek(f.ctx, a, nil)
	require.NoError(t, err)
	assert.Nil(t, peeked.Ticket, "the Stop hook must not send an agent after it")

	directed, err := f.svc.Claim(f.ctx, a, &drafting.ID)
	require.NoError(t, err)
	assert.Nil(t, directed.Ticket, "not even when asked for by id")

	queued := f.ticket(t, "Low-priority open work", "web/")
	result, err := f.svc.Claim(f.ctx, a, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Ticket)
	assert.Equal(t, queued.ID, result.Ticket.ID, "open work is taken; the urgent draft is not")
	assert.Equal(t, ticket.StatusPreparing, f.status(t, drafting.ID))
}

// Preparing is not terminal, so work waiting on it stays waiting.
func TestPreparing_DependentsStayBlocked(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	drafting := f.preparing(t, "Being shaped")
	waiting := f.ticket(t, "Needs the draft done first")
	f.blockedBy(t, waiting.ID, drafting.ID)

	result, err := f.svc.Claim(f.ctx, a, nil)
	require.NoError(t, err)
	assert.Nil(t, result.Ticket)
}

func TestMarkReady_PutsATicketWithNothingPendingInTheQueue(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	drafting := f.preparing(t, "Shaped now", "server/")

	ready, err := f.svc.MarkReady(f.ctx, drafting.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusOpen, ready.Status)

	result, err := f.svc.Claim(f.ctx, a, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Ticket)
	assert.Equal(t, drafting.ID, result.Ticket.ID)
}

// The same rule a human's answer to a discussion uses: a ticket marked
// ready over unfinished work shows as blocked, not as Open with nothing
// able to claim it -- and comes back on its own when that work is done.
func TestMarkReady_LandsBlockedWhileADependencyIsUnfinished(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	dep := f.ticket(t, "Dependency", "web/")
	drafting := f.preparing(t, "Waits on the dependency", "server/")
	f.blockedBy(t, drafting.ID, dep.ID)

	ready, err := f.svc.MarkReady(f.ctx, drafting.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusBlocked, ready.Status)

	_, err = f.svc.Claim(f.ctx, a, &dep.ID)
	require.NoError(t, err)
	_, err = f.svc.Done(f.ctx, a, &ticket.DoneParams{Summary: "done"})
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusOpen, f.status(t, drafting.ID))
}

func TestMarkReady_RefusesATicketThatIsNotPreparing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	queued := f.ticket(t, "Already open")

	_, err := f.svc.MarkReady(f.ctx, queued.ID)
	assert.ErrorIs(t, err, ticket.ErrNotPreparing)
	assert.Equal(t, ticket.StatusOpen, f.status(t, queued.ID))

	_, err = f.svc.MarkReady(f.ctx, 9999)
	assert.ErrorIs(t, err, ticket.ErrNotFound)
}

// Leaving Preparing for the queue goes through Ready, which is what applies
// the open-or-blocked rule; the free-form edit may only discard it.
func TestUpdate_APreparingTicketEntersTheQueueOnlyThroughReady(t *testing.T) {
	t.Parallel()
	for _, next := range []ticket.Status{ticket.StatusOpen, ticket.StatusBlocked, ticket.StatusInProgress, ticket.StatusDiscussion, ticket.StatusReview} {
		t.Run("refuses "+string(next), func(t *testing.T) {
			f := newFixture(t)
			drafting := f.preparing(t, "Draft")

			_, err := f.setStatus(t, drafting.ID, next)
			assert.ErrorIs(t, err, ticket.ErrLeavePreparing)
			assert.Equal(t, ticket.StatusPreparing, f.status(t, drafting.ID))
		})
	}
	for _, next := range []ticket.Status{ticket.StatusCancelled, ticket.StatusDone, ticket.StatusPreparing} {
		t.Run("allows "+string(next), func(t *testing.T) {
			f := newFixture(t)
			drafting := f.preparing(t, "Draft")

			updated, err := f.setStatus(t, drafting.ID, next)
			require.NoError(t, err)
			assert.Equal(t, next, updated.Status)
		})
	}
	t.Run("a missing ticket is still not found", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.setStatus(t, 9999, ticket.StatusPreparing)
		assert.ErrorIs(t, err, ticket.ErrNotFound)
	})
}

// Preparing carries no first claim: a person reshaping a ticket owes its
// earlier asker nothing.
func TestUpdate_MovingToPreparingDropsFirstClaim(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	asker := f.agent(t, "asker")
	created := f.ticket(t, "Asked about", "server/")
	f.claim(t, asker)
	f.discuss(t, asker)
	f.humanReply(t, created.ID)

	moved, err := f.setStatus(t, created.ID, ticket.StatusPreparing)
	require.NoError(t, err)
	assert.Nil(t, moved.ResumeAgentID)
	assert.Nil(t, moved.AffinityUntil)

	_, err = f.svc.MarkReady(f.ctx, created.ID)
	require.NoError(t, err)
	other := f.agent(t, "other")
	result, err := f.svc.Claim(f.ctx, other, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Ticket, "any agent may take it once ready")
	assert.Equal(t, created.ID, result.Ticket.ID)
}

// A person's comment answers a discussion; on a Preparing ticket it is just
// a note to self while shaping, and must not put the ticket in the queue.
func TestPreparing_AHumanCommentLeavesItPreparing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	drafting := f.preparing(t, "Draft")

	f.humanReply(t, drafting.ID)
	assert.Equal(t, ticket.StatusPreparing, f.status(t, drafting.ID))
}

// Every status a person moves a held ticket to, other than in_progress,
// takes it away from the agent: the lease and its lock go in the same
// transaction, and the agent's next heartbeat -- every tool call makes
// one -- says why.
var humanMovesThatRevoke = []ticket.Status{
	ticket.StatusPreparing, ticket.StatusOpen, ticket.StatusBlocked,
	ticket.StatusDiscussion, ticket.StatusReview, ticket.StatusDone, ticket.StatusCancelled,
}

func TestHumanMove_RevokesTheLeaseAndTellsTheAgentOnce(t *testing.T) {
	t.Parallel()
	for _, to := range humanMovesThatRevoke {
		t.Run(string(to), func(t *testing.T) {
			f := newFixture(t)
			a := f.agent(t, "merger")
			held := f.ticket(t, "Being worked", "server/")
			f.claim(t, a)
			require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, a))
			if to == ticket.StatusBlocked {
				// Blocked with nothing unfinished to wait on is reopened in
				// the same write; give it something to wait on.
				f.blockedBy(t, held.ID, f.ticket(t, "Prerequisite", "docs/").ID)
			}

			moved, err := f.setStatus(t, held.ID, to)
			require.NoError(t, err)
			assert.Equal(t, to, moved.Status)

			_, err = f.svc.LeaseFor(f.ctx, a)
			assert.ErrorIs(t, err, ticket.ErrNoLease, "the agent no longer holds it")
			assert.Zero(t, f.lockCount(t), "its integration lock goes with the lease")
			other := f.agent(t, "other")
			otherWork := f.ticket(t, "Other work", "web/")
			_, err = f.svc.Claim(f.ctx, other, &otherWork.ID)
			require.NoError(t, err)
			require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, other), "the lock is free again")

			err = f.svc.Heartbeat(f.ctx, a)
			var revoked *ticket.LeaseRevokedError
			require.ErrorAs(t, err, &revoked)
			assert.ErrorIs(t, err, ticket.ErrLeaseRevoked)
			assert.NotErrorIs(t, err, ticket.ErrNoLease,
				"a revocation is an instruction to stop, not a lapsed lease to shrug off")
			assert.Equal(t, held.ID, revoked.TicketID)
			assert.Equal(t, to, revoked.Status)
			assert.Contains(t, err.Error(), "stop working on it")

			assert.ErrorIs(t, f.svc.Heartbeat(f.ctx, a), ticket.ErrNoLease,
				"told once; after that the agent is simply holding nothing")

			_, err = f.svc.Done(f.ctx, a, &ticket.DoneParams{Summary: "finished anyway"})
			assert.ErrorIs(t, err, ticket.ErrNoLease, "and cannot finish the withdrawn ticket")
			assert.Equal(t, to, f.status(t, held.ID))
		})
	}
}

// The case that made every move revoke: a person puts a held ticket back
// in the queue. With the first agent's lease left live, the next claim of
// it collided with that lease; now the second agent gets it cleanly, and
// the first is told and has no lease to act with.
func TestHumanMove_ToOpenHandsTheTicketToTheNextAgentAlone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	first, second := f.agent(t, "first"), f.agent(t, "second")
	// No paths: the first agent's scope would otherwise keep the second
	// off the ticket by path overlap, hiding the double hold behind a
	// different guard.
	held := f.ticket(t, "Requeued")
	f.claim(t, first)

	_, err := f.setStatus(t, held.ID, ticket.StatusOpen)
	require.NoError(t, err)
	reclaimed := f.claim(t, second)
	require.NotNil(t, reclaimed)
	require.Equal(t, held.ID, reclaimed.ID)

	var revoked *ticket.LeaseRevokedError
	require.ErrorAs(t, f.svc.Heartbeat(f.ctx, first), &revoked, "the first agent is told, not kept on")
	assert.Equal(t, held.ID, revoked.TicketID)
	assert.Equal(t, ticket.StatusOpen, revoked.Status)
	assert.Contains(t, revoked.Error(), fmt.Sprintf("ticket #%d was moved to Open by a human -- stop working on it", held.ID))

	_, err = f.svc.Done(f.ctx, first, &ticket.DoneParams{Summary: "finished anyway"})
	assert.ErrorIs(t, err, ticket.ErrNoLease)
	_, err = f.svc.LeaseFor(f.ctx, first)
	assert.ErrorIs(t, err, ticket.ErrNoLease)

	lease, err := f.svc.LeaseFor(f.ctx, second)
	require.NoError(t, err)
	assert.Equal(t, held.ID, lease.TicketID, "the second agent is the only holder")
	assert.NoError(t, f.svc.Heartbeat(f.ctx, second))
	assert.Equal(t, ticket.StatusInProgress, f.status(t, held.ID))
}

// Blocked with nothing unfinished to wait on is reopened in the same
// write; the notice says where the ticket actually is.
func TestHumanMove_ToBlockedThatReopensIsReportedAsOpen(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	held := f.ticket(t, "No prerequisites", "server/")
	f.claim(t, a)

	moved, err := f.setStatus(t, held.ID, ticket.StatusBlocked)
	require.NoError(t, err)
	require.Equal(t, ticket.StatusOpen, moved.Status)

	var revoked *ticket.LeaseRevokedError
	require.ErrorAs(t, f.svc.Heartbeat(f.ctx, a), &revoked)
	assert.Equal(t, ticket.StatusOpen, revoked.Status)
	assert.Contains(t, revoked.Error(), "was moved to Open by a human")
}

func TestHumanMove_TheRevocationMessageNamesWhatHappened(t *testing.T) {
	t.Parallel()
	for status, want := range map[ticket.Status]string{
		ticket.StatusPreparing:  "#4 was moved to Preparing by a human -- stop working on it",
		ticket.StatusOpen:       "#4 was moved to Open by a human -- stop working on it",
		ticket.StatusBlocked:    "#4 was moved to Blocked by a human -- stop working on it",
		ticket.StatusDiscussion: "#4 was moved to Discussion by a human -- stop working on it",
		ticket.StatusDone:       "#4 was moved to Done by a human -- stop working on it",
		ticket.StatusCancelled:  "#4 was cancelled by a human -- stop working on it",
	} {
		assert.Contains(t, (&ticket.LeaseRevokedError{TicketID: 4, Status: status}).Error(), want)
	}
}

// Only a person's revocation is news. A lease that lapsed, or one the agent
// ended itself, leaves the heartbeat saying what it always has.
func TestRevocation_IsNotReportedForOtherWaysALeaseEnds(t *testing.T) {
	t.Parallel()
	t.Run("expiry", func(t *testing.T) {
		f := newFixture(t)
		a := f.agent(t, "builder-1")
		f.ticket(t, "Work", "server/")
		f.claim(t, a)
		f.expire(t, a)

		assert.ErrorIs(t, f.svc.Heartbeat(f.ctx, a), ticket.ErrNoLease)
	})
	t.Run("the agent's own prepare", func(t *testing.T) {
		f := newFixture(t)
		a := f.agent(t, "builder-1")
		f.ticket(t, "Work", "server/")
		f.claim(t, a)
		_, err := f.svc.Prepare(f.ctx, a, "The spec contradicts itself.")
		require.NoError(t, err)

		assert.ErrorIs(t, f.svc.Heartbeat(f.ctx, a), ticket.ErrNoLease)
	})
	t.Run("a human edit that leaves the status alone", func(t *testing.T) {
		f := newFixture(t)
		a := f.agent(t, "builder-1")
		held := f.ticket(t, "Work", "server/")
		f.claim(t, a)
		require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, a))
		priority := model.PriorityHigh
		_, err := f.svc.Update(f.ctx, held.ID, &ticket.UpdateParams{Priority: &priority})
		require.NoError(t, err)
		title := "Work, retitled"
		_, err = f.svc.Edit(f.ctx, held.ID, f.human, &ticket.EditParams{Title: &title})
		require.NoError(t, err)

		assert.NoError(t, f.svc.Heartbeat(f.ctx, a))
		assert.Equal(t, 1, f.lockCount(t), "and it keeps its lock")
	})
	t.Run("a human edit that leaves it in progress", func(t *testing.T) {
		f := newFixture(t)
		a := f.agent(t, "builder-1")
		held := f.ticket(t, "Work", "server/")
		f.claim(t, a)
		priority, status := model.PriorityHigh, ticket.StatusInProgress
		_, err := f.svc.Update(f.ctx, held.ID, &ticket.UpdateParams{Priority: &priority, Status: &status})
		require.NoError(t, err)

		assert.NoError(t, f.svc.Heartbeat(f.ctx, a), "only a move out of in_progress takes the lease")
	})
}

// A notice is about the agent's most recent lease. One the agent never
// heard about, because it went on to claim and finish other work first, is
// history by then.
func TestRevocation_ANoticeForAnOlderLeaseIsNotDelivered(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	first := f.ticket(t, "Taken away", "server/")
	f.claim(t, a)
	_, err := f.setStatus(t, first.ID, ticket.StatusPreparing)
	require.NoError(t, err)

	f.ticket(t, "Next", "web/")
	f.claim(t, a)
	_, err = f.svc.Release(f.ctx, a, "not today", nil)
	require.NoError(t, err)

	assert.ErrorIs(t, f.svc.Heartbeat(f.ctx, a), ticket.ErrNoLease)
}

// An agent silent for longer than a lease lasts would have lost the ticket
// to expiry anyway; its next session should not open on old news.
func TestRevocation_AStaleNoticeIsNotDelivered(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	held := f.ticket(t, "Taken away", "server/")
	f.claim(t, a)
	_, err := f.setStatus(t, held.ID, ticket.StatusPreparing)
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx,
		`UPDATE leases SET released_at = now() - $2::interval WHERE agent_id = $1`,
		a, (ticket.DefaultLeaseTTL + 1).String())
	require.NoError(t, err)

	assert.ErrorIs(t, f.svc.Heartbeat(f.ctx, a), ticket.ErrNoLease)
}

func TestPrepare_SendsTheHeldTicketBackWithTheReason(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "merger")
	held := f.ticket(t, "Turns out underspecified", "server/")
	f.claim(t, a)
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, a))

	prepared, err := f.svc.Prepare(f.ctx, a, "The body asks for two incompatible APIs.")
	require.NoError(t, err)
	assert.Equal(t, held.ID, prepared.ID)
	assert.Equal(t, ticket.StatusPreparing, prepared.Status)
	assert.Nil(t, prepared.ResumeAgentID, "no first claim, unlike a discussion")

	_, err = f.svc.LeaseFor(f.ctx, a)
	assert.ErrorIs(t, err, ticket.ErrNoLease)
	assert.Zero(t, f.lockCount(t))

	comments, err := f.svc.ListComments(f.ctx, held.ID)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, "The body asks for two incompatible APIs.", comments[0].Body)
	require.NotNil(t, comments[0].AuthorAgentID)
	assert.Equal(t, a, *comments[0].AuthorAgentID)

	result, err := f.svc.Claim(f.ctx, a, nil)
	require.NoError(t, err)
	assert.Nil(t, result.Ticket, "out of the queue until a person opens it")
}

func TestPrepare_RequiresAReason(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	held := f.ticket(t, "Work", "server/")
	f.claim(t, a)

	_, err := f.svc.Prepare(f.ctx, a, "  \n")
	assert.ErrorIs(t, err, ticket.ErrEmptyReason)
	assert.Equal(t, ticket.StatusInProgress, f.status(t, held.ID))
}

// An agent may only send back the ticket it holds -- it has no way to name
// another. One that holds nothing moves nothing, and one that holds a
// different ticket moves only its own.
func TestPrepare_OnlyEverMovesTheCallersOwnTicket(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	holder, idle, other := f.agent(t, "holder"), f.agent(t, "idle"), f.agent(t, "other")
	held := f.ticket(t, "Held by holder", "server/")
	queued := f.ticket(t, "Queued", "docs/")
	_, err := f.svc.Claim(f.ctx, holder, &held.ID)
	require.NoError(t, err)

	_, err = f.svc.Prepare(f.ctx, idle, "I think this one is wrong.")
	assert.ErrorIs(t, err, ticket.ErrNoLease)
	assert.Equal(t, ticket.StatusInProgress, f.status(t, held.ID))
	assert.Equal(t, ticket.StatusOpen, f.status(t, queued.ID))

	mine := f.ticket(t, "Other's own", "web/")
	_, err = f.svc.Claim(f.ctx, other, &mine.ID)
	require.NoError(t, err)
	prepared, err := f.svc.Prepare(f.ctx, other, "Needs reshaping.")
	require.NoError(t, err)
	assert.Equal(t, mine.ID, prepared.ID)
	assert.Equal(t, ticket.StatusInProgress, f.status(t, held.ID), "the holder's ticket is untouched")
	assert.Equal(t, ticket.StatusOpen, f.status(t, queued.ID), "queued work is untouched")

	_, err = f.svc.LeaseFor(f.ctx, holder)
	assert.NoError(t, err, "and so is the holder's lease")
}

// Only a person moves a ticket out of Preparing. Every operation an agent
// can reach -- through its MCP tools or its REST claim routes -- either
// needs a lease it cannot get on a Preparing ticket, or leaves the status
// alone.
func TestPreparing_NoAgentOperationTakesATicketOut(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	drafting := f.preparing(t, "Draft", "server/")
	blockedOn := drafting.ID

	_, err := f.svc.Claim(f.ctx, a, &drafting.ID)
	require.NoError(t, err)
	_, err = f.svc.AddComment(f.ctx, drafting.ID, ticket.AgentAuthor(a), "Looks ready to me.")
	require.NoError(t, err)
	_, err = f.svc.Release(f.ctx, a, "releasing", nil)
	assert.ErrorIs(t, err, ticket.ErrNoLease)
	_, err = f.svc.Done(f.ctx, a, &ticket.DoneParams{Summary: "done"})
	assert.ErrorIs(t, err, ticket.ErrNoLease)
	_, err = f.svc.Discuss(f.ctx, a, "q", "h")
	assert.ErrorIs(t, err, ticket.ErrNoLease)
	_, err = f.svc.Prepare(f.ctx, a, "r")
	assert.ErrorIs(t, err, ticket.ErrNoLease)
	_, err = f.svc.ExtendScope(f.ctx, a, []model.Path{{Path: "web/"}})
	assert.ErrorIs(t, err, ticket.ErrNoLease)

	// Releasing some other ticket blocked on the draft writes an edge, not
	// the draft's status.
	f.ticket(t, "Other", "web/")
	f.claim(t, a)
	_, err = f.svc.Release(f.ctx, a, "needs the draft", &blockedOn)
	require.NoError(t, err)

	assert.Equal(t, ticket.StatusPreparing, f.status(t, drafting.ID))
}
