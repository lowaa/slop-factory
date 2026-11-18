package pendingaction_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Deciding applies a move as the person who decided, and the views show the
// whole tenant's proposals: the service refuses a caller that is not a
// signed-in person, whatever transport it came by.
func TestDecidingAndViewingRefuseACallerThatIsNotAPerson(t *testing.T) {
	d := newDecider(t)
	id := d.twoAttemptsIn(t, ticket.StatusReview)
	p := d.propose(t, &pendingaction.ProposeParams{
		TicketID: id, Kind: pendingaction.KindReviewDecision, Attempt: intp(2),
		Payload: pendingaction.Payload{Decision: pendingaction.DecisionApprove},
	})

	_, err := d.svc.Approve(d.ctx, nil, p.ID)
	assert.ErrorIs(t, err, authn.ErrNotPerson)
	_, err = d.svc.Reject(d.ctx, nil, p.ID, "no")
	assert.ErrorIs(t, err, authn.ErrNotPerson)
	_, err = d.svc.ApproveAll(d.ctx, nil, pendingaction.KindReviewDecision, []uuid.UUID{p.ID})
	assert.ErrorIs(t, err, authn.ErrNotPerson)
	_, err = d.svc.RejectAll(d.ctx, nil, []uuid.UUID{p.ID}, "no")
	assert.ErrorIs(t, err, authn.ErrNotPerson)
	_, err = d.svc.ViewOf(d.ctx, nil, p.ID)
	assert.ErrorIs(t, err, authn.ErrNotPerson)
	_, err = d.svc.ViewsPage(d.ctx, nil, &pendingaction.ListFilter{})
	assert.ErrorIs(t, err, authn.ErrNotPerson)

	got, err := d.svc.Get(d.ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, pendingaction.StatePending, got.State, "nothing was decided")
}
