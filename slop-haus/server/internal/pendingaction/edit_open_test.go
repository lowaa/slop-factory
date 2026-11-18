package pendingaction_test

import (
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

func TestApprovingAnEditThatOpensChangesTheTicketAndOpensItInOneApproval(t *testing.T) {
	d := newDecider(t)
	target := d.editTarget(t, ticket.StatusPreparing, "a/old/")
	p := d.mustProposeEdit(t, target.ID, &pendingaction.EditPayload{
		BaseRevision: target.Revision, Title: ptr("new title"), Body: ptr("new body"),
		Paths: []string{"a/new/"}, Priority: ptr(model.PriorityHigh), Open: true,
	})

	settled, err := d.svc.Approve(d.ctx, d.approver, p.ID)
	require.NoError(t, err)
	assert.Equal(t, pendingaction.StateApproved, settled.State)

	got, err := d.tickets.Get(d.ctx, target.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusOpen, got.Status)
	assert.Equal(t, "new title", got.Title)
	assert.Equal(t, "new body", got.Body)
	assert.Equal(t, model.PriorityHigh, got.Priority)
	require.Len(t, got.Paths, 1)
	assert.Equal(t, "a/new/", got.Paths[0].Path)
	change, who := d.lastChange(t, target.ID)
	assert.Equal(t, "proposal:"+p.ID.String(), change.Cause)
	assert.Equal(t, "preparing", change.From)
	assert.Equal(t, "open", change.To)
	require.NotNil(t, who)
	assert.Equal(t, d.approver.ID, *who)
}

func TestAnEditThatOpensIsAllOrNothing(t *testing.T) {
	d := newDecider(t)
	// The move to open is refused, after the edit has been written.
	d.svc = pendingaction.NewService(pendingaction.NewPostgresStore(d.database), pendingaction.Config{}, slog.DiscardHandler,
		pendingaction.WithApproval(failingWrites{d.tickets}, d.grants))
	d.svc.SetNow(d.clock.Now)
	target := d.editTarget(t, ticket.StatusPreparing, "a/")
	p := d.mustProposeEdit(t, target.ID, &pendingaction.EditPayload{
		BaseRevision: target.Revision, Title: ptr("new title"), Priority: ptr(model.PriorityUrgent), Open: true,
	})

	settled, err := d.svc.Approve(d.ctx, d.approver, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrRefused)
	assert.Equal(t, pendingaction.StateFailed, settled.State)
	got, err := d.tickets.Get(d.ctx, target.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusPreparing, got.Status)
	assert.Equal(t, "old title", got.Title, "the edit was rolled back with the failed move")
	assert.Equal(t, model.PriorityNormal, got.Priority)
	revisions, err := d.tickets.ListRevisions(d.ctx, target.ID)
	require.NoError(t, err)
	assert.Empty(t, revisions)
}

func TestAStaleEditThatOpensAppliesNothingAndLeavesTheTicketInPreparing(t *testing.T) {
	d := newDecider(t)
	target := d.editTarget(t, ticket.StatusPreparing, "a/")
	p := d.mustProposeEdit(t, target.ID, &pendingaction.EditPayload{
		BaseRevision: target.Revision, Priority: ptr(model.PriorityHigh), Open: true,
	})
	_, err := d.tickets.Edit(d.ctx, target.ID, d.approver.ID, &ticket.EditParams{Body: ptr("someone else's"), BaseRevision: &target.Revision})
	require.NoError(t, err)

	settled, err := d.svc.Approve(d.ctx, d.approver, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrStale)
	assert.Equal(t, pendingaction.StateStale, settled.State)
	got, err := d.tickets.Get(d.ctx, target.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusPreparing, got.Status)
	assert.Equal(t, model.PriorityNormal, got.Priority)
}

func TestAnEditThatOpensIsRefusedWhenProposedOnATicketNotInPreparing(t *testing.T) {
	for _, status := range []ticket.Status{ticket.StatusOpen, ticket.StatusParked, ticket.StatusDone} {
		d := newDecider(t)
		target := d.editTarget(t, status, "a/")
		_, err := d.proposeEdit(t, target.ID, &pendingaction.EditPayload{
			BaseRevision: target.Revision, Priority: ptr(model.PriorityHigh), Open: true,
		})
		require.ErrorIs(t, err, pendingaction.ErrInvalid, string(status))
		assert.Zero(t, d.count(t), string(status))
	}
}

func TestAnEditThatOpensIsRefusedWhenTheTicketLeftPreparingBeforeApproval(t *testing.T) {
	d := newDecider(t)
	target := d.editTarget(t, ticket.StatusPreparing, "a/")
	p := d.mustProposeEdit(t, target.ID, &pendingaction.EditPayload{
		BaseRevision: target.Revision, Priority: ptr(model.PriorityHigh), Open: true,
	})
	d.exec(t, `UPDATE tickets SET status = 'open' WHERE id = $1`, target.ID)

	settled, err := d.svc.Approve(d.ctx, d.approver, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrStale)
	assert.Equal(t, pendingaction.StateStale, settled.State)
	got, err := d.tickets.Get(d.ctx, target.ID)
	require.NoError(t, err)
	assert.Equal(t, model.PriorityNormal, got.Priority)
}

func TestAReadyWaitsForAPendingEditOnTheSameTicket(t *testing.T) {
	d := newDecider(t)
	target := d.editTarget(t, ticket.StatusPreparing, "a/")
	edit := d.mustProposeEdit(t, target.ID, &pendingaction.EditPayload{BaseRevision: target.Revision, Body: ptr("new body")})
	ready := d.propose(t, &pendingaction.ProposeParams{TicketID: target.ID, Kind: pendingaction.KindReady})

	got, err := d.svc.Approve(d.ctx, d.approver, ready.ID)
	require.ErrorIs(t, err, pendingaction.ErrBlocked)
	assert.Nil(t, got)
	assert.Contains(t, err.Error(), "decide the pending edit for this ticket first")
	assert.Equal(t, pendingaction.StatePending, d.stateOf(t, ready).State, "still waiting, not settled")
	assert.Equal(t, ticket.StatusPreparing, d.ticketStatus(t, target.ID))

	_, err = d.svc.Reject(d.ctx, d.approver, edit.ID, "no")
	require.NoError(t, err)
	_, err = d.svc.Approve(d.ctx, d.approver, ready.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusOpen, d.ticketStatus(t, target.ID))
}

func TestAReadyIsReleasedByAnEditBeingWithdrawnExpiredOrApproved(t *testing.T) {
	t.Run("withdrawn", func(t *testing.T) {
		d := newDecider(t)
		target := d.editTarget(t, ticket.StatusPreparing, "a/")
		edit := d.mustProposeEdit(t, target.ID, &pendingaction.EditPayload{BaseRevision: target.Revision, Body: ptr("x")})
		ready := d.propose(t, &pendingaction.ProposeParams{TicketID: target.ID, Kind: pendingaction.KindReady})
		_, err := d.svc.Withdraw(d.ctx, d.console, edit.ID)
		require.NoError(t, err)
		_, err = d.svc.Approve(d.ctx, d.approver, ready.ID)
		require.NoError(t, err)
	})
	t.Run("approved, with a text change: the ready is then stale, not applied", func(t *testing.T) {
		d := newDecider(t)
		target := d.editTarget(t, ticket.StatusPreparing, "a/")
		edit := d.mustProposeEdit(t, target.ID, &pendingaction.EditPayload{BaseRevision: target.Revision, Body: ptr("x")})
		ready := d.propose(t, &pendingaction.ProposeParams{TicketID: target.ID, Kind: pendingaction.KindReady})
		_, err := d.svc.Approve(d.ctx, d.approver, edit.ID)
		require.NoError(t, err)
		settled, err := d.svc.Approve(d.ctx, d.approver, ready.ID)
		require.ErrorIs(t, err, pendingaction.ErrStale)
		assert.Equal(t, pendingaction.StateStale, settled.State)
		assert.Equal(t, ticket.StatusPreparing, d.ticketStatus(t, target.ID))
	})
	t.Run("approved, priority only: the ready applies", func(t *testing.T) {
		d := newDecider(t)
		target := d.editTarget(t, ticket.StatusPreparing, "a/")
		edit := d.mustProposeEdit(t, target.ID, &pendingaction.EditPayload{BaseRevision: target.Revision, Priority: ptr(model.PriorityHigh)})
		ready := d.propose(t, &pendingaction.ProposeParams{TicketID: target.ID, Kind: pendingaction.KindReady})
		_, err := d.svc.Approve(d.ctx, d.approver, edit.ID)
		require.NoError(t, err)
		_, err = d.svc.Approve(d.ctx, d.approver, ready.ID)
		require.NoError(t, err)
		assert.Equal(t, ticket.StatusOpen, d.ticketStatus(t, target.ID))
	})
	t.Run("expired", func(t *testing.T) {
		d := newDecider(t)
		target := d.editTarget(t, ticket.StatusPreparing, "a/")
		d.mustProposeEdit(t, target.ID, &pendingaction.EditPayload{BaseRevision: target.Revision, Body: ptr("x")})
		// The ready is made later, so it outlives the edit.
		d.clock.Advance(pendingaction.DefaultTTL - 1)
		ready := d.propose(t, &pendingaction.ProposeParams{TicketID: target.ID, Kind: pendingaction.KindReady})
		d.clock.Advance(2)
		_, err := d.svc.Approve(d.ctx, d.approver, ready.ID)
		require.NoError(t, err)
	})
}

func TestAnEditOnAnotherTicketDoesNotHoldAReady(t *testing.T) {
	d := newDecider(t)
	other := d.editTarget(t, ticket.StatusPreparing, "a/")
	d.mustProposeEdit(t, other.ID, &pendingaction.EditPayload{BaseRevision: other.Revision, Body: ptr("x")})
	ready := d.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})
	_, err := d.svc.Approve(d.ctx, d.approver, ready.ID)
	require.NoError(t, err)
}

func TestApproveAllSkipsAReadyWaitingForAnEditAndReportsWhy(t *testing.T) {
	d := newDecider(t)
	waiting := d.editTarget(t, ticket.StatusPreparing, "a/")
	d.mustProposeEdit(t, waiting.ID, &pendingaction.EditPayload{BaseRevision: waiting.Revision, Body: ptr("x")})
	blocked := d.propose(t, &pendingaction.ProposeParams{TicketID: waiting.ID, Kind: pendingaction.KindReady})
	free := d.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})

	results, err := d.svc.ApproveAll(d.ctx, d.approver, pendingaction.KindReady, nil)
	require.NoError(t, err)
	byID := map[uuid.UUID]pendingaction.Result{}
	for _, r := range results {
		byID[r.ID] = r
	}
	require.Len(t, byID, 2)
	assert.Equal(t, pendingaction.StatePending, byID[blocked.ID].State)
	require.ErrorIs(t, byID[blocked.ID].Err, pendingaction.ErrBlocked)
	assert.Equal(t, pendingaction.StateApproved, byID[free.ID].State)
	assert.NoError(t, byID[free.ID].Err)
	assert.Equal(t, ticket.StatusPreparing, d.ticketStatus(t, waiting.ID))
	assert.Equal(t, ticket.StatusOpen, d.ticketStatus(t, *free.TicketID))
}

func TestTheActionLineOfAnEditThatOpensSaysSo(t *testing.T) {
	a := pendingaction.Action{TicketID: ptr(int64(7)), Kind: pendingaction.KindEdit,
		Payload: pendingaction.Payload{Edit: &pendingaction.EditPayload{Open: true}}}
	assert.Contains(t, pendingaction.ActionLine(&a, ticket.StatusPreparing), "move it out of preparing")
	a.Payload.Edit.Open = false
	assert.Contains(t, pendingaction.ActionLine(&a, ticket.StatusPreparing), "status does not change")
}
