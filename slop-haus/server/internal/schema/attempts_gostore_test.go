package schema_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// The ticket package writes the moved fields on the attempt only, and the
// attempt shows the ticket's status and times. Driven through the real service
// and store rather than SQL written for the test, so a write path that misses
// the attempt is one the test reaches.
func TestWritesThroughTheTicketStoreReachAttemptOne(t *testing.T) {
	database := dbtest.WithDB(t)
	ctx := db.WithTenant(context.Background(), uuid.New())
	svc := ticket.NewService(ticket.NewPostgresStore(database), slog.DiscardHandler)
	agent, user := uuid.New(), dbtest.NewUser(t, database)
	_, err := database.Exec(ctx, `INSERT INTO agents (id, name) VALUES ($1, 'worker')`, agent)
	require.NoError(t, err)

	attemptMatchesTicket := func(id int64, wantStatus string) {
		t.Helper()
		var status string
		var same bool
		require.NoError(t, database.QueryRow(ctx, `
			SELECT a.status, (a.updated_at, a.closed_at) IS NOT DISTINCT FROM (t.updated_at, t.closed_at)
			  FROM attempts a JOIN tickets t ON t.tenant_id = a.tenant_id AND t.id = a.ticket_id
			 WHERE t.id = $1 AND a.number = 1`, id).Scan(&status, &same))
		assert.Equal(t, wantStatus, status)
		assert.True(t, same, "attempt 1 holds the ticket's times")
	}
	write := func(id int64, w *ticket.TicketWrite) {
		t.Helper()
		require.NoError(t, svc.InTransition(ctx, func(tx ticket.TransitionTx) error { return tx.WriteTicket(ctx, id, w) }))
	}

	created, err := svc.Create(ctx, &ticket.CreateParams{Title: "one", RequiresApproval: true})
	require.NoError(t, err)
	attemptMatchesTicket(created.ID, "queued")

	claim, err := svc.Claim(ctx, agent, nil)
	require.NoError(t, err)
	require.NotNil(t, claim.Ticket)
	attemptMatchesTicket(created.ID, "in_progress")
	var leaseAttempt int
	require.NoError(t, database.QueryRow(ctx, `SELECT attempt_number FROM leases WHERE ticket_id = $1`, created.ID).Scan(&leaseAttempt))
	assert.Equal(t, 1, leaseAttempt, "the lease names the promoted attempt")

	pr := "https://github.example/pull/12"
	write(created.ID, &ticket.TicketWrite{Status: ticket.To(ticket.StatusReview), PRURL: &pr, Review: ticket.ReviewStarts()})
	attemptMatchesTicket(created.ID, "review")
	write(created.ID, &ticket.TicketWrite{Review: ticket.ReviewRequestsChanges()})
	attemptMatchesTicket(created.ID, "review")
	write(created.ID, &ticket.TicketWrite{Review: ticket.ReviewStarts()})
	write(created.ID, &ticket.TicketWrite{Status: ticket.To(ticket.StatusDone), Closing: ticket.Closes(), Review: ticket.ReviewApproves(user)})
	attemptMatchesTicket(created.ID, "done")
	var approved bool
	require.NoError(t, database.QueryRow(ctx, `
		SELECT approved_at IS NOT NULL AND approved_by_user_id = $2 AND pr_url IS NOT NULL AND closed_at IS NOT NULL
		  FROM attempts WHERE ticket_id = $1`, created.ID, user).Scan(&approved))
	assert.True(t, approved, "the review outcome, link and close reached the attempt")

	// A discussion: the agent owed the answer is the attempt's resume agent.
	agent = uuid.New()
	_, err = database.Exec(ctx, `INSERT INTO agents (id, name) VALUES ($1, 'second')`, agent)
	require.NoError(t, err)
	discussed, err := svc.Create(ctx, &ticket.CreateParams{Title: "two"})
	require.NoError(t, err)
	_, err = svc.Claim(ctx, agent, nil)
	require.NoError(t, err)
	_, err = svc.Discuss(ctx, agent, "which way?", "handoff")
	require.NoError(t, err)
	attemptMatchesTicket(discussed.ID, "discussion")
	var resume *uuid.UUID
	require.NoError(t, database.QueryRow(ctx, `SELECT resume_agent_id FROM attempts WHERE ticket_id = $1`, discussed.ID).Scan(&resume))
	require.NotNil(t, resume)
	assert.Equal(t, agent, *resume)
	var handoffAttempt int
	require.NoError(t, database.QueryRow(ctx, `SELECT attempt_number FROM agent_contexts WHERE ticket_id = $1`, discussed.ID).Scan(&handoffAttempt))
	assert.Equal(t, 1, handoffAttempt)

	// Cancelled is closed.
	cancelled, err := svc.Create(ctx, &ticket.CreateParams{Title: "three"})
	require.NoError(t, err)
	_, err = svc.MoveMany(ctx, []int64{cancelled.ID}, ticket.StatusCancelled)
	require.NoError(t, err)
	attemptMatchesTicket(cancelled.ID, "closed")
}
