package pendingaction_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func TestApproveForStewardAppliesAsTheOwnerThroughTheNormalPathAndSaysSo(t *testing.T) {
	f := newAutoFixture(t)
	console := f.consoleFor(t, f.a)
	// The owner's auto-approve setting is off: the steward's say is its own.
	pending := f.proposeReady(t, console)
	require.Equal(t, pendingaction.StatePending, pending.State)

	got, err := f.svc.ApproveForSteward(f.ctx, pending.ID)

	require.NoError(t, err)
	assert.Equal(t, pendingaction.StateApproved, got.State)
	assert.Equal(t, pendingaction.OutcomeStewardApproved, got.Outcome)
	assert.True(t, got.AutoApproved(), "it never counts as a person's own approval")
	require.NotNil(t, got.DecidedByUserID)
	assert.Equal(t, f.a.ID, *got.DecidedByUserID)
	assert.Equal(t, ticket.StatusOpen, f.ticketStatus(t, *got.TicketID))

	var cause string
	require.NoError(t, f.database.QueryRow(f.ctx,
		`SELECT cause FROM ticket_status_changes WHERE ticket_id = $1 ORDER BY id DESC LIMIT 1`, got.TicketID).Scan(&cause))
	assert.Equal(t, pendingaction.StewardCauseFor(got.ID), cause)
	assert.False(t, f.decisions.seen(got.ID), "the owner never decided it, so it is not an agreement to count")
}

func TestApproveForStewardKeepsTheNormalApprovalChecks(t *testing.T) {
	f := newAutoFixture(t)
	console := f.consoleFor(t, f.a)
	pending := f.proposeReady(t, console)
	f.exec(t, `UPDATE tickets SET status = 'open' WHERE id = $1`, *pending.TicketID)

	got, err := f.svc.ApproveForSteward(f.ctx, pending.ID)

	assert.ErrorIs(t, err, pendingaction.ErrStale)
	assert.Equal(t, pendingaction.StateStale, got.State)

	_, err = f.svc.ApproveForSteward(f.ctx, pending.ID)
	assert.ErrorIs(t, err, pendingaction.ErrNotPending, "applies once")
}

func TestApproveForStewardNeedsATenantOwner(t *testing.T) {
	f := newAutoFixture(t)
	pending := f.proposeReady(t, f.consoleFor(t, f.a))
	f.owner.err = errors.New("the tenant has no owner")

	_, err := f.svc.ApproveForSteward(f.ctx, pending.ID)

	assert.Error(t, err)
	assert.Equal(t, ticket.StatusPreparing, f.ticketStatus(t, *pending.TicketID))

	bare := f.serviceFor(pendingaction.Config{})
	_, err = bare.ApproveForSteward(f.ctx, pending.ID)
	assert.ErrorIs(t, err, pendingaction.ErrNotConfigured, "without an owner source nothing is approved")

	_, err = f.svc.ApproveForSteward(f.ctx, uuid.New())
	assert.Error(t, err)
}

func TestAClickedApprovalIsStillRecordedAsTheOwnersDecision(t *testing.T) {
	f := newAutoFixture(t)
	pending := f.proposeReady(t, f.consoleFor(t, f.a))

	_, err := f.svc.Approve(f.ctx, f.a, pending.ID)

	require.NoError(t, err)
	assert.True(t, f.decisions.seen(pending.ID))
}
