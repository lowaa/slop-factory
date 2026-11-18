package change_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

const mergedURL = "https://git.example/acme/app/pull/7"

// readyToMerge is an agent holding the integration lock with an open,
// passing change on a ticket needing no approval.
func (f *fixture) readyToMerge() *holder {
	f.t.Helper()
	h := f.hold(false)
	f.open(h, false)
	f.lock(h)
	return h
}

func (f *fixture) lockHeld() bool {
	f.t.Helper()
	state, err := f.tickets.IntegrationLockState(f.ctx)
	require.NoError(f.t, err)
	return state.Holder != nil
}

func TestMerge_ClosesTheTicketAndFreesTheLock(t *testing.T) {
	f := newFixture(t)
	h := f.readyToMerge()

	merged, err := f.merge(h, headA)

	require.NoError(t, err)
	assert.True(t, merged.Closed)
	done, err := f.tickets.Get(f.ctx, h.ticket.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusDone, done.Status)
	require.NotNil(t, done.PRURL)
	assert.Equal(t, mergedURL, *done.PRURL)
	assert.False(t, f.lockHeld(), "the lock goes with the lease")
	_, err = f.tickets.LeaseFor(f.ctx, h.agent)
	require.ErrorIs(t, err, ticket.ErrNoLease)
}

func TestMerge_ALateTicketDoneAndUnlockAreNoOps(t *testing.T) {
	f := newFixture(t)
	h := f.readyToMerge()
	_, err := f.merge(h, headA)
	require.NoError(t, err)
	before, err := f.tickets.ListComments(f.ctx, h.ticket.ID)
	require.NoError(t, err)

	_, err = f.tickets.Done(f.ctx, h.agent, &ticket.DoneParams{PRURL: mergedURL, Summary: "done"})
	require.NoError(t, err, "same link: older briefings still call it")
	require.NoError(t, f.tickets.ReleaseIntegrationLock(f.ctx, h.agent))

	after, err := f.tickets.ListComments(f.ctx, h.ticket.ID)
	require.NoError(t, err)
	assert.Len(t, after, len(before), "a no-op posts nothing")
	_, err = f.tickets.Done(f.ctx, h.agent, &ticket.DoneParams{PRURL: "https://git.example/acme/app/pull/8"})
	require.ErrorIs(t, err, ticket.ErrNoLease, "another change is not the one that closed it")
}

func TestReleaseIntegrationLock_NotHeldStillFailsForAnAgentThatWasNotMerged(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)

	err := f.tickets.ReleaseIntegrationLock(f.ctx, h.agent)

	require.ErrorIs(t, err, ticket.ErrNoSuchLock)
}

func TestMerge_TheWebhookAfterIsANoOp(t *testing.T) {
	f := newFixture(t)
	h := f.readyToMerge()
	_, err := f.merge(h, headA)
	require.NoError(t, err)
	before, err := f.tickets.ListComments(f.ctx, h.ticket.ID)
	require.NoError(t, err)

	_, err = f.tickets.CloseMergedPR(f.ctx, f.host.Opened, f.creator)

	require.ErrorIs(t, err, ticket.ErrNotMergeable, "the webhook ignores it")
	after, err := f.tickets.ListComments(f.ctx, h.ticket.ID)
	require.NoError(t, err)
	assert.Len(t, after, len(before))
}

func TestMerge_TheWebhookBeforeLeavesTheCloseToTheMerge(t *testing.T) {
	f := newFixture(t)
	h := f.readyToMerge()

	_, err := f.tickets.CloseMergedPR(f.ctx, f.host.Opened, f.creator)
	require.ErrorIs(t, err, ticket.ErrNotMergeable, "a ticket an agent holds is left alone")
	merged, err := f.merge(h, headA)

	require.NoError(t, err)
	assert.True(t, merged.Closed)
	done, err := f.tickets.Get(f.ctx, h.ticket.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusDone, done.Status)
}

// failingDone is the ticket side with a close that cannot be made.
type failingDone struct{ *ticket.Service }

func (failingDone) Done(context.Context, uuid.UUID, *ticket.DoneParams) (*ticket.Ticket, error) {
	return nil, errors.New("database went away")
}

func TestMerge_AFailedCloseIsStillAMerge(t *testing.T) {
	f := newFixture(t)
	h := f.readyToMerge()
	svc := f.serviceOver(f.host, failingDone{f.tickets})

	merged, err := svc.Merge(f.ctx, f.tenant, h.agent, &change.MergeInput{ExpectedSHA: headA})

	require.NoError(t, err)
	assert.False(t, merged.Closed)
	assert.Equal(t, "mergecommit", merged.MergeSHA)
	assert.True(t, f.lockHeld(), "nothing was closed")
	_, err = f.tickets.Done(f.ctx, h.agent, &ticket.DoneParams{PRURL: mergedURL})
	require.NoError(t, err, "ticket_finish completes it")
	assert.False(t, f.lockHeld())
}

// mergeLeavingTheTicketHeld merges as change_merge does, with the close
// that follows it failing, for tests about what a merged change leaves
// behind for the agent that still holds the ticket.
func (f *fixture) mergeLeavingTheTicketHeld(h *holder) {
	f.t.Helper()
	svc := f.serviceOver(f.host, failingDone{f.tickets})
	_, err := svc.Merge(f.ctx, f.tenant, h.agent, &change.MergeInput{})
	require.NoError(f.t, err)
}

func TestMerge_AMergeIsNotMadeTwiceWhileTheTicketIsStillHeld(t *testing.T) {
	f := newFixture(t)
	h := f.readyToMerge()
	f.mergeLeavingTheTicketHeld(h)

	_, err := f.merge(h, "")

	require.ErrorIs(t, err, change.ErrAlreadyMerged)
	assert.Len(t, f.host.Merges(), 1)
}

// Handing a merged change over for review would park the ticket where only a
// person can act, and a person cannot approve a merge that already happened:
// the agent is told to release it for a person to close.
func TestReview_AMergedChangeIsNotHandedOverForReview(t *testing.T) {
	f := newFixture(t)
	h := f.readyToMerge()
	f.mergeLeavingTheTicketHeld(h)

	_, err := f.svc.Review(f.ctx, f.tenant, h.agent, &ticket.ReviewParams{Reason: "reviewed by a person", Handoff: "done"})

	require.ErrorIs(t, err, change.ErrAlreadyMerged)
	assert.True(t, f.lockHeld(), "the agent still holds the ticket: nothing was handed over")
	assert.Contains(t, err.Error(), "PR already merged outside slop-haus; a person must close the ticket ('Close as merged'). Release with this reason.")
}
