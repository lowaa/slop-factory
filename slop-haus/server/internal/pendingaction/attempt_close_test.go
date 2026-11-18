package pendingaction_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
)

// Closing an attempt does not void its pending proposals: approval finds the
// attempt no longer live and settles them as stale.
func TestApproving_AProposalOnAnAttemptClosedByThePersonEndpointIsStale(t *testing.T) {
	d := newDecider(t)
	id := d.twoAttempts(t)
	p := d.propose(t, d.answer(intp(2), id))

	_, err := d.tickets.CloseAttempt(d.ctx, d.approver.ID, id, 2)
	require.NoError(t, err)
	assert.Equal(t, pendingaction.StatePending, d.stateOf(t, p).State, "closing does not touch the proposal")

	settled, err := d.svc.Approve(d.ctx, d.approver, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrStale)
	assert.Equal(t, pendingaction.StateStale, settled.State)
	assert.Empty(t, d.comments(t, id), "no comment was posted")
}
