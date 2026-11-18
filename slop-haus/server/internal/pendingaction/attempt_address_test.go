package pendingaction_test

import (
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func intp(n int) *int { return &n }

// twoAttempts is a ticket with both its attempts in discussion.
func (d *decider) twoAttempts(t *testing.T) int64 {
	t.Helper()
	return d.twoAttemptsIn(t, ticket.StatusDiscussion)
}

// twoAttemptsIn is a ticket with both its attempts in status.
func (d *decider) twoAttemptsIn(t *testing.T, status ticket.Status) int64 {
	t.Helper()
	id := d.ticketIn(t, status)
	d.exec(t, `UPDATE attempts SET status = $2 WHERE ticket_id = $1 AND number = 1`, id, string(status))
	d.exec(t, `INSERT INTO attempts (ticket_id, number, status) VALUES ($1, 2, $2)`, id, string(status))
	return id
}

// attemptStatus is attempt n's status.
func (d *decider) attemptStatus(t *testing.T, id int64, n int) string {
	t.Helper()
	var status string
	require.NoError(t, d.database.QueryRow(d.ctx, `SELECT status FROM attempts WHERE ticket_id = $1 AND number = $2`, id, n).Scan(&status))
	return status
}

func (d *decider) answer(attempt *int, id int64) *pendingaction.ProposeParams {
	return &pendingaction.ProposeParams{
		AgentID: d.console, TicketID: id, Kind: pendingaction.KindDiscussionAnswer,
		Payload: pendingaction.Payload{Body: "go"}, Attempt: attempt,
	}
}

func TestProposing_WithOneLiveAttemptTheAttemptDefaults(t *testing.T) {
	d := newDecider(t)
	id := d.ticketIn(t, ticket.StatusDiscussion)

	unnamed := d.propose(t, d.answer(nil, id))
	require.NotNil(t, unnamed.Attempt)
	assert.Equal(t, 1, *unnamed.Attempt)

	named := d.propose(t, d.answer(intp(1), id))
	require.NotNil(t, named.Attempt)
	assert.Equal(t, 1, *named.Attempt)
}

func TestProposing_WithSeveralLiveAttemptsOneMustBeNamed(t *testing.T) {
	d := newDecider(t)
	id := d.twoAttempts(t)

	_, err := d.svc.Propose(d.ctx, d.answer(nil, id))
	require.ErrorIs(t, err, ticket.ErrAttemptAmbiguous)
	assert.Contains(t, err.Error(), "attempt 2")

	inReview := d.twoAttemptsIn(t, ticket.StatusReview)
	_, err = d.svc.Propose(d.ctx, &pendingaction.ProposeParams{
		AgentID: d.console, TicketID: inReview, Kind: pendingaction.KindReviewDecision,
		Payload: pendingaction.Payload{Decision: pendingaction.DecisionApprove},
	})
	require.ErrorIs(t, err, ticket.ErrAttemptAmbiguous)
	assert.Zero(t, d.count(t), "nothing was proposed")

	got := d.propose(t, d.answer(intp(2), id))
	require.NotNil(t, got.Attempt)
	assert.Equal(t, 2, *got.Attempt)

	_, err = d.svc.Propose(d.ctx, d.answer(intp(3), id))
	require.ErrorIs(t, err, ticket.ErrNoSuchAttempt)
	d.exec(t, `UPDATE attempts SET status = 'closed' WHERE ticket_id = $1 AND number = 2`, id)
	_, err = d.svc.Propose(d.ctx, d.answer(intp(2), id))
	require.ErrorIs(t, err, ticket.ErrAttemptNotLive)
}

func TestProposing_AKindAboutTheTicketTakesNoAttempt(t *testing.T) {
	d := newDecider(t)
	id := d.ticketIn(t, ticket.StatusPreparing)
	_, err := d.svc.Propose(d.ctx, &pendingaction.ProposeParams{
		AgentID: d.console, TicketID: id, Kind: pendingaction.KindReady, Attempt: intp(1),
	})
	require.ErrorIs(t, err, pendingaction.ErrInvalid)
	assert.Zero(t, d.count(t))
}

func TestApproving_AnAnswerRequeuesTheAttemptItIsAboutAlone(t *testing.T) {
	d := newDecider(t)
	id := d.twoAttempts(t)

	sibling := d.propose(t, d.answer(intp(2), id))
	approved, err := d.svc.Approve(d.ctx, d.approver, sibling.ID)
	require.NoError(t, err)
	assert.Equal(t, pendingaction.StateApproved, approved.State)

	assert.Equal(t, "queued", d.attemptStatus(t, id, 2))
	assert.Equal(t, "discussion", d.attemptStatus(t, id, 1), "attempt 1 is still waiting for its own answer")
	assert.Equal(t, ticket.StatusDiscussion, d.ticketStatus(t, id), "the ticket shows what still waits on a person")
	var attempt *int
	require.NoError(t, d.database.QueryRow(d.ctx, `SELECT attempt_number FROM ticket_comments WHERE ticket_id = $1`, id).Scan(&attempt))
	require.NotNil(t, attempt)
	assert.Equal(t, 2, *attempt, "the answer is addressed to the attempt it was for")
}

func TestApproving_ADecisionAppliesToTheAttemptItIsAboutAlone(t *testing.T) {
	d := newDecider(t)
	id := d.twoAttemptsIn(t, ticket.StatusReview)

	p := d.propose(t, &pendingaction.ProposeParams{
		TicketID: id, Kind: pendingaction.KindReviewDecision, Attempt: intp(2),
		Payload: pendingaction.Payload{Decision: pendingaction.DecisionApprove},
	})
	_, err := d.svc.Approve(d.ctx, d.approver, p.ID)
	require.NoError(t, err)

	assert.Equal(t, "queued", d.attemptStatus(t, id, 2))
	assert.Equal(t, "review", d.attemptStatus(t, id, 1), "attempt 1's review is untouched")
	var approved []bool
	rows, err := d.database.Query(d.ctx, `SELECT approved_at IS NOT NULL FROM attempts WHERE ticket_id = $1 ORDER BY number`, id)
	require.NoError(t, err)
	approved, err = pgx.CollectRows(rows, pgx.RowTo[bool])
	require.NoError(t, err)
	assert.Equal(t, []bool{false, true}, approved)
	var attempt *int
	require.NoError(t, d.database.QueryRow(d.ctx, `SELECT attempt_number FROM ticket_comments WHERE ticket_id = $1`, id).Scan(&attempt))
	require.NotNil(t, attempt, "the approval's comment is the attempt's, not every attempt's")
	assert.Equal(t, 2, *attempt)
}

func TestProposing_AboutAnAttemptNotWaitingOnAPersonIsRefused(t *testing.T) {
	d := newDecider(t)
	id := d.twoAttempts(t)
	d.exec(t, `UPDATE attempts SET status = 'queued' WHERE ticket_id = $1 AND number = 2`, id)

	_, err := d.svc.Propose(d.ctx, d.answer(intp(2), id))
	require.ErrorIs(t, err, pendingaction.ErrInvalid, "the ticket shows attempt 1's discussion, which is not attempt 2's")
	assert.Zero(t, d.count(t))
	d.propose(t, d.answer(intp(1), id))
}

func TestApproving_AProposalForAnAttemptClosedSinceIsStale(t *testing.T) {
	d := newDecider(t)
	id := d.twoAttempts(t)
	p := d.propose(t, d.answer(intp(2), id))
	d.exec(t, `UPDATE attempts SET status = 'closed' WHERE ticket_id = $1 AND number = 2`, id)

	settled, err := d.svc.Approve(d.ctx, d.approver, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrStale)
	assert.Equal(t, pendingaction.StateStale, settled.State)
	assert.Empty(t, d.comments(t, id), "no comment was posted")
}

func TestProposing_ForOneAttemptDoesNotWithdrawASiblingsProposal(t *testing.T) {
	d := newDecider(t)
	id := d.twoAttempts(t)

	first := d.propose(t, d.answer(intp(1), id))
	second := d.propose(t, d.answer(intp(2), id))
	assert.Equal(t, pendingaction.StatePending, d.stateOf(t, first).State, "attempt 1's proposal stands")
	assert.Equal(t, pendingaction.StatePending, d.stateOf(t, second).State)

	again := d.propose(t, d.answer(intp(2), id))
	assert.Equal(t, pendingaction.StateWithdrawn, d.stateOf(t, second).State, "a newer proposal for the same attempt supersedes it")
	assert.Equal(t, pendingaction.StatePending, d.stateOf(t, first).State)
	assert.Equal(t, pendingaction.StatePending, d.stateOf(t, again).State)
}

func TestProposing_AboutTheTicketIsJudgedOnTheTicketsStatus(t *testing.T) {
	d := newDecider(t)
	id := d.twoAttempts(t)
	d.exec(t, `UPDATE attempts SET status = 'in_progress' WHERE ticket_id = $1 AND number = 1`, id)

	_, err := d.svc.Propose(d.ctx, &pendingaction.ProposeParams{
		AgentID: d.console, TicketID: id, Kind: pendingaction.KindStatus,
		Payload: pendingaction.Payload{Status: ticket.StatusParked, Body: "park it"},
	})

	require.ErrorIs(t, err, pendingaction.ErrInvalid, "the ticket shows discussion, which cannot be parked")
	assert.Zero(t, d.count(t))
}

func TestApproving_RequestingChangesAppliesToTheAttemptItIsAboutAlone(t *testing.T) {
	d := newDecider(t)
	id := d.twoAttemptsIn(t, ticket.StatusReview)

	p := d.propose(t, &pendingaction.ProposeParams{
		TicketID: id, Kind: pendingaction.KindReviewDecision, Attempt: intp(2),
		Payload: pendingaction.Payload{Decision: pendingaction.DecisionRequestChanges, Body: "rename it"},
	})
	_, err := d.svc.Approve(d.ctx, d.approver, p.ID)
	require.NoError(t, err)

	assert.Equal(t, "queued", d.attemptStatus(t, id, 2))
	assert.Equal(t, "review", d.attemptStatus(t, id, 1), "attempt 1's review is untouched")
	var attempt *int
	require.NoError(t, d.database.QueryRow(d.ctx, `SELECT attempt_number FROM ticket_comments WHERE ticket_id = $1`, id).Scan(&attempt))
	require.NotNil(t, attempt)
	assert.Equal(t, 2, *attempt)
}

func TestProposing_AboutAnAttemptIsRefusedOnlyWhenThatAttemptIsHeld(t *testing.T) {
	d := newDecider(t)
	id := d.twoAttempts(t)
	holder := d.newAgent(t, "holder", false)
	d.exec(t, `INSERT INTO leases (ticket_id, attempt_number, agent_id, expires_at) VALUES ($1, 1, $2, now() + interval '1 hour')`, id, holder)

	got := d.propose(t, d.answer(intp(2), id))
	require.NotNil(t, got.Attempt, "attempt 1's lease is not attempt 2's")

	_, err := d.svc.Propose(d.ctx, d.answer(intp(1), id))
	require.ErrorIs(t, err, pendingaction.ErrTicketHeld)
}
