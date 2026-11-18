package ticket_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func TestLand_QueuesTheTicketForTheServerAndNotForWorkers(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	authorID := f.agent(t, "author")
	created := f.ticket(t, "Ungated", "server/")
	require.NotNil(t, f.claim(t, authorID))

	_, err := f.svc.Land(f.ctx, authorID, &ticket.LandParams{
		Handoff: "PR open",
		Within: func(q db.Querier) error {
			return landing.EnqueueIn(f.ctx, q, &landing.LandingRow{TicketID: created.ID, AttemptNumber: 1, ChangeURL: reviewPR, HeadSHA: "abc"})
		},
	})
	require.NoError(t, err)

	status, owed, _ := f.attemptState(t, created.ID)
	assert.Equal(t, "in_merge_queue", status)
	assert.Equal(t, "in_merge_queue", f.ticketStatus(t, created.ID))
	require.NotNil(t, owed)
	assert.Equal(t, authorID, *owed, "a handback returns the ticket to its author")

	f.expireAffinity(t, created.ID)
	assert.Nil(t, f.claim(t, f.agent(t, "worker")), "a ticket in the landing queue is the server's, not a worker's")

	server := f.serverAgent(t)
	got, err := f.serverClaimLanding(server, created.ID)
	require.NoError(t, err, "a ticket that does not require approval needs none to land")
	assert.NotNil(t, got.Ticket)
	status, _, _ = f.attemptState(t, created.ID)
	assert.Equal(t, "in_merge_queue", status, "the server holding the head does not make it work in progress")
	assert.Equal(t, "in_merge_queue", f.ticketStatus(t, created.ID))
}

func TestLand_ARowThatCannotBeWrittenLeavesTheTicketWithItsAgent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	authorID := f.agent(t, "author")
	created := f.ticket(t, "Ungated", "server/")
	require.NotNil(t, f.claim(t, authorID))

	_, err := f.svc.Land(f.ctx, authorID, &ticket.LandParams{Handoff: "h", Within: func(db.Querier) error { return errors.New("no row") }})
	require.Error(t, err)

	status, _, _ := f.attemptState(t, created.ID)
	assert.Equal(t, "in_progress", status)
}

func TestLand_ARowHandedBackIsTheAuthorsToClaimAgain(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	authorID := f.agent(t, "author")
	created := f.ticket(t, "Ungated", "server/")
	require.NotNil(t, f.claim(t, authorID))
	_, err := f.svc.Land(f.ctx, authorID, &ticket.LandParams{Handoff: "h", Within: func(q db.Querier) error {
		return landing.EnqueueIn(f.ctx, q, &landing.LandingRow{TicketID: created.ID, AttemptNumber: 1, ChangeURL: reviewPR, HeadSHA: "abc"})
	}})
	require.NoError(t, err)

	server := f.serverAgent(t)
	_, err = f.serverClaimLanding(server, created.ID)
	require.NoError(t, err)
	require.NoError(t, f.store().ServerHandBack(f.ctx, server, created.ID, "retry", time.Hour))
	store := landing.NewLandingStore(f.database)
	require.NoError(t, store.HandBack(f.ctx, created.ID, landing.MergeRefused, false))
	assert.Equal(t, "open", f.ticketStatus(t, created.ID))

	assert.NotNil(t, f.claim(t, authorID))
}

func TestServerClaim_AGatedTicketStillNeedsItsApproval(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	gated := f.approvedTicket(t, f.agent(t, "author"), "server/")
	f.expireAffinity(t, gated.ID)
	_, err := f.database.Exec(f.ctx, `UPDATE attempts SET approved_at = NULL, approved_by_user_id = NULL WHERE ticket_id = $1`, gated.ID)
	require.NoError(t, err)

	_, err = f.serverClaim(f.serverAgent(t), gated.ID)
	assert.ErrorIs(t, err, ticket.ErrServerClaimNotApproved)
}

func TestLand_RechecksApprovalAndPromotionInItsOwnTransaction(t *testing.T) {
	t.Parallel()
	t.Run("a gated ticket whose approval is gone", func(t *testing.T) {
		f := newFixture(t)
		authorID := f.agent(t, "author")
		gated := f.gated(t, "Gated", "server/")
		require.NotNil(t, f.claim(t, authorID))

		_, err := f.svc.Land(f.ctx, authorID, &ticket.LandParams{Handoff: "h"})
		assert.ErrorIs(t, err, ticket.ErrNotApproved)
		status, _, _ := f.attemptState(t, gated.ID)
		assert.Equal(t, "in_progress", status)
	})
	t.Run("an attempt that is not promoted", func(t *testing.T) {
		f := newFixture(t)
		authorID := f.agent(t, "author")
		created := f.ticket(t, "Ungated", "server/")
		require.NotNil(t, f.claim(t, authorID))
		_, err := f.database.Exec(f.ctx, `UPDATE attempts SET promoted_at = NULL WHERE ticket_id = $1`, created.ID)
		require.NoError(t, err)

		_, err = f.svc.Land(f.ctx, authorID, &ticket.LandParams{Handoff: "h"})
		assert.ErrorIs(t, err, ticket.ErrLandNotPromoted)
	})
}
