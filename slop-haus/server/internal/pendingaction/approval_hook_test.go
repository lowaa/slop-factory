package pendingaction_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func TestApprovalHook_ApprovingAReviewDecisionFiresOnce(t *testing.T) {
	d := newDecider(t)
	id := d.reviewTicket(t)
	p := d.propose(t, &pendingaction.ProposeParams{TicketID: id, Kind: pendingaction.KindReviewDecision,
		Payload: pendingaction.Payload{Decision: pendingaction.DecisionApprove}})

	_, err := d.svc.Approve(d.ctx, d.approver, p.ID)
	require.NoError(t, err)
	_, err = d.svc.Approve(d.ctx, d.approver, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrNotPending)

	d.tickets.WaitApprovalHooks()
	heard := d.approvals.Approvals()
	require.Len(t, heard, 1)
	assert.Equal(t, d.tenant, heard[0].TenantID)
	assert.Equal(t, id, heard[0].TicketID)
	assert.Equal(t, 1, heard[0].Attempt)
	assert.Equal(t, d.approver.ID, heard[0].ApproverID)
	assert.False(t, heard[0].ApprovedAt.IsZero())
}

func TestApprovalHook_RequestingChangesFiresNothing(t *testing.T) {
	d := newDecider(t)
	id := d.reviewTicket(t)
	p := d.propose(t, &pendingaction.ProposeParams{TicketID: id, Kind: pendingaction.KindReviewDecision,
		Payload: pendingaction.Payload{Decision: pendingaction.DecisionRequestChanges, Body: "add a test"}})

	_, err := d.svc.Approve(d.ctx, d.approver, p.ID)
	require.NoError(t, err)

	d.tickets.WaitApprovalHooks()
	assert.Empty(t, d.approvals.Approvals())
}

func TestApprovalHook_AutoApprovedReviewDecisionFiresOnce(t *testing.T) {
	f := newAutoFixture(t)
	console := f.consoleFor(t, f.a)
	f.turnOn(t, f.a, pendingaction.KindReviewDecision)
	id := f.ticketIn(t, ticket.StatusReview)
	f.exec(t, `UPDATE attempts SET pr_url = 'https://github.com/o/r/pull/1', review_started_at = $2 WHERE ticket_id = $1 AND number = 1`, id, f.clock.Now())

	got, err := f.svc.Propose(f.ctx, &pendingaction.ProposeParams{AgentID: console, TicketID: id, Kind: pendingaction.KindReviewDecision,
		Payload: pendingaction.Payload{Decision: pendingaction.DecisionApprove}})
	require.NoError(t, err)
	require.True(t, got.AutoApproved())

	f.tickets.WaitApprovalHooks()
	heard := f.approvals.Approvals()
	require.Len(t, heard, 1)
	assert.Equal(t, id, heard[0].TicketID)
	assert.Equal(t, f.a.ID, heard[0].ApproverID)
}

func TestApprovalHook_AProposalThatApprovesNoReviewFiresNothing(t *testing.T) {
	f := newAutoFixture(t)
	console := f.consoleFor(t, f.a)
	f.turnOn(t, f.a, pendingaction.KindReady)

	f.proposeReady(t, console)

	f.tickets.WaitApprovalHooks()
	assert.Empty(t, f.approvals.Approvals())
}
