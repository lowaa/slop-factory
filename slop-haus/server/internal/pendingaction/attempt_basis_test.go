package pendingaction_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// The basis is read from the ticket's attempt, so a proposal goes stale when
// the attempt's PR changes, whatever the ticket's own column says.
func TestTheBasisIsReadFromTheTicketsAttempt(t *testing.T) {
	f := newFixture(t, pendingaction.Config{})
	console := f.newAgent(t, "console", true)
	id := f.ticketIn(t, ticket.StatusReview)
	const attemptPR = "https://github.com/o/r/pull/77"
	f.exec(t, `UPDATE attempts SET pr_url = 'https://github.com/o/r/pull/5' WHERE ticket_id = $1 AND number = 1`, id)
	f.exec(t, `UPDATE attempts SET pr_url = $2, review_started_at = now() WHERE ticket_id = $1 AND number = 1`, id, attemptPR)

	got, err := f.svc.Propose(f.ctx, &pendingaction.ProposeParams{
		AgentID: console, TicketID: id, Kind: pendingaction.KindReviewDecision,
		Payload: pendingaction.Payload{Decision: pendingaction.DecisionApprove},
	})
	require.NoError(t, err)
	require.NotNil(t, got.Basis.PRURL)
	assert.Equal(t, attemptPR, *got.Basis.PRURL)
	assert.NotNil(t, got.Basis.ReviewStartedAt)

	views, err := f.svc.Views(f.ctx, &pendingaction.ListFilter{})
	require.NoError(t, err)
	require.Len(t, views, 1)
	assert.Empty(t, views[0].StaleReason)

	f.exec(t, `UPDATE attempts SET pr_url = 'https://github.com/o/r/pull/78' WHERE ticket_id = $1 AND number = 1`, id)
	views, err = f.svc.Views(f.ctx, &pendingaction.ListFilter{})
	require.NoError(t, err)
	assert.NotEmpty(t, views[0].StaleReason, "the attempt's PR changed")

	filtered, err := f.svc.Views(f.ctx, &pendingaction.ListFilter{Ticket: &ticket.ListFilter{PRLink: "https://github.com/o/r/pull/78"}})
	require.NoError(t, err)
	assert.Len(t, filtered, 1, "the ticket filter reads the attempt's PR too")
}
