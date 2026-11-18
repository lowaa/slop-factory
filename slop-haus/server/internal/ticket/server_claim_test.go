package ticket_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func (f *fixture) store() *ticket.PostgresStore { return newStore(f.database) }

func (f *fixture) serverAgent(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := f.store().EnsureServerAgent(f.ctx)
	require.NoError(t, err)
	return id
}

func (f *fixture) serverClaim(server uuid.UUID, id int64) (*ticket.ClaimResult, error) {
	return f.store().ServerClaim(f.ctx, server, id, 1, ticket.DefaultServerLeaseTTL)
}

func (f *fixture) serverClaimLanding(server uuid.UUID, id int64) (*ticket.ClaimResult, error) {
	return f.store().ServerClaimLanding(f.ctx, server, id, 1, ticket.DefaultServerLeaseTTL)
}

// approvedTicket is a gated ticket its author built, put in review and a
// person approved, so it waits in the queue with the author owed first claim.
func (f *fixture) approvedTicket(t *testing.T, author uuid.UUID, paths ...string) *ticket.Ticket {
	t.Helper()
	gated := f.gated(t, "Gated", paths...)
	f.claim(t, author)
	f.review(t, author)
	f.approve(t, gated.ID)
	return gated
}

func (f *fixture) attemptState(t *testing.T, id int64) (status string, owed *uuid.UUID, window *time.Time) {
	t.Helper()
	require.NoError(t, f.database.QueryRow(f.ctx,
		`SELECT status, resume_agent_id, affinity_until FROM attempts WHERE ticket_id = $1 AND number = 1`, id).
		Scan(&status, &owed, &window))
	return status, owed, window
}

func TestEnsureServerAgent_IsIdempotentAndNeverOfferedQueueWork(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	first := f.serverAgent(t)
	assert.Equal(t, first, f.serverAgent(t))

	f.approvedTicket(t, f.agent(t, "author"), "server/")
	_, err := f.svc.Claim(f.ctx, first, nil)
	assert.ErrorIs(t, err, ticket.ErrConsoleUnnamedClaim)
}

func TestEnsureServerAgent_RefusesATakenNameAndADisabledAgent(t *testing.T) {
	t.Parallel()
	t.Run("taken by a worker", func(t *testing.T) {
		f := newFixture(t)
		f.agent(t, ticket.ServerAgentName)
		_, err := f.store().EnsureServerAgent(f.ctx)
		assert.ErrorIs(t, err, ticket.ErrServerAgentTaken)
	})
	t.Run("disabled", func(t *testing.T) {
		f := newFixture(t)
		id := f.serverAgent(t)
		_, err := f.agents.SetDisabled(f.ctx, id, true)
		require.NoError(t, err)
		_, err = f.store().EnsureServerAgent(f.ctx)
		assert.ErrorIs(t, err, ticket.ErrServerAgentDisabled)
		_, err = f.serverClaim(id, 1)
		assert.ErrorIs(t, err, ticket.ErrServerAgentDisabled)
	})
}

func TestServerOperations_RefuseAnyOtherAgent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	worker := f.agent(t, "worker")
	gated := f.approvedTicket(t, f.agent(t, "author"), "server/")
	f.expireAffinity(t, gated.ID)
	_, err := f.serverClaim(worker, gated.ID)
	assert.ErrorIs(t, err, ticket.ErrNotServerAgent)

	require.NotNil(t, f.claim(t, worker))
	assert.ErrorIs(t, f.store().ServerHandBack(f.ctx, worker, gated.ID, "x", time.Minute), ticket.ErrNotServerAgent)
}

func TestServerClaim_BypassesTheAuthorsAffinityWindow(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	author := f.agent(t, "author")
	server := f.serverAgent(t)
	gated := f.approvedTicket(t, author, "server/")

	_, owed, window := f.attemptState(t, gated.ID)
	require.NotNil(t, window, "the author holds first claim")
	require.Equal(t, &author, owed)

	// An ordinary agent is held off by the window; the server is not.
	other := f.agent(t, "other")
	assert.Nil(t, f.claim(t, other))

	got, err := f.serverClaim(server, gated.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Ticket)
	assert.Equal(t, gated.ID, got.Ticket.ID)
	assert.Equal(t, 1, got.Attempt.Number)
	assert.Equal(t, ticket.StatusInProgress, got.Ticket.Status)

	lease, err := f.store().LeaseFor(f.ctx, server)
	require.NoError(t, err)
	assert.Equal(t, gated.ID, lease.TicketID)
}

func TestServerClaim_TheLeaseTakesTheLockAndCanHeartbeat(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	server := f.serverAgent(t)
	gated := f.approvedTicket(t, f.agent(t, "author"), "server/")
	_, err := f.store().ServerClaim(f.ctx, server, gated.ID, 1, time.Minute)
	require.NoError(t, err)

	lease, err := f.store().LeaseFor(f.ctx, server)
	require.NoError(t, err)
	assert.Less(t, time.Until(lease.ExpiresAt), 2*time.Minute, "the lease is for the TTL asked")

	require.NoError(t, f.store().AcquireLock(f.ctx, server, ticket.IntegrationLockName))
	gate, err := f.svc.ChangeGateFor(f.ctx, server)
	require.NoError(t, err)
	assert.True(t, gate.HoldsLock)
	assert.True(t, gate.Promoted)
	assert.NotNil(t, gate.Review.ApprovedAt)

	require.NoError(t, f.store().Heartbeat(f.ctx, server, time.Hour))
	lease, err = f.store().LeaseFor(f.ctx, server)
	require.NoError(t, err)
	assert.Greater(t, time.Until(lease.ExpiresAt), 30*time.Minute)
}

func TestServerClaim_Refusals(t *testing.T) {
	t.Parallel()
	t.Run("not approved", func(t *testing.T) {
		f := newFixture(t)
		a := f.agent(t, "author")
		gated := f.gated(t, "Gated", "server/")
		f.claim(t, a)
		f.review(t, a)
		// In review, awaiting a person.
		_, err := f.serverClaim(f.serverAgent(t), gated.ID)
		assert.ErrorIs(t, err, ticket.ErrServerClaimNotQueued)

		// Changes requested: queued again, but not approved.
		f.requestChanges(t, gated.ID)
		_, err = f.serverClaim(f.serverAgent(t), gated.ID)
		assert.ErrorIs(t, err, ticket.ErrServerClaimNotApproved)
	})
	t.Run("held", func(t *testing.T) {
		f := newFixture(t)
		author := f.agent(t, "author")
		gated := f.approvedTicket(t, author, "server/")
		f.expireAffinity(t, gated.ID)
		require.NotNil(t, f.claim(t, f.agent(t, "worker")))
		_, err := f.serverClaim(f.serverAgent(t), gated.ID)
		assert.ErrorIs(t, err, ticket.ErrServerClaimHeld)
	})
	t.Run("not promoted", func(t *testing.T) {
		f := newFixture(t)
		author := f.agent(t, "author")
		gated := f.approvedTicket(t, author, "server/")
		_, err := f.database.Exec(f.ctx, `UPDATE attempts SET promoted_at = NULL WHERE ticket_id = $1 AND number = 1`, gated.ID)
		require.NoError(t, err)
		_, err = f.serverClaim(f.serverAgent(t), gated.ID)
		assert.ErrorIs(t, err, ticket.ErrServerClaimNotPromoted)
	})
	t.Run("no such attempt", func(t *testing.T) {
		f := newFixture(t)
		gated := f.approvedTicket(t, f.agent(t, "author"), "server/")
		_, err := f.store().ServerClaim(f.ctx, f.serverAgent(t), gated.ID, 9, time.Hour)
		assert.ErrorIs(t, err, ticket.ErrNoSuchAttempt)
	})
	t.Run("server already holds a ticket", func(t *testing.T) {
		f := newFixture(t)
		server := f.serverAgent(t)
		first := f.approvedTicket(t, f.agent(t, "author-1"), "a/")
		second := f.approvedTicket(t, f.agent(t, "author-2"), "b/")
		_, err := f.serverClaim(server, first.ID)
		require.NoError(t, err)
		_, err = f.serverClaim(server, second.ID)
		assert.ErrorIs(t, err, ticket.ErrAlreadyHolding)
	})
	t.Run("delayed by a cooldown", func(t *testing.T) {
		f := newFixture(t)
		server := f.serverAgent(t)
		gated := f.approvedTicket(t, f.agent(t, "author"), "server/")
		_, err := f.database.Exec(f.ctx, `UPDATE tickets SET blocked_until = now() + interval '1 hour' WHERE id = $1`, gated.ID)
		require.NoError(t, err)
		_, err = f.serverClaim(server, gated.ID)
		assert.ErrorIs(t, err, ticket.ErrServerClaimNotQueued)
		_, err = f.store().LeaseFor(f.ctx, server)
		assert.Error(t, err)
	})
	t.Run("scope held by another ticket", func(t *testing.T) {
		f := newFixture(t)
		gated := f.approvedTicket(t, f.agent(t, "author"), "server/")
		f.ticket(t, "Rival", "server/")
		require.NotNil(t, f.claim(t, f.agent(t, "rival")))
		got, err := f.serverClaim(f.serverAgent(t), gated.ID)
		require.NoError(t, err)
		assert.Nil(t, got.Ticket)
		require.NotNil(t, got.Blocked)
		assert.Equal(t, gated.ID, got.Blocked.TicketID)
	})
}

func (f *fixture) expireAffinity(t *testing.T, id int64) {
	t.Helper()
	_, err := f.database.Exec(f.ctx, `UPDATE attempts SET affinity_until = now() - interval '1 minute' WHERE ticket_id = $1`, id)
	require.NoError(t, err)
}

func TestServerHandBack_ReturnsTheTicketToTheAuthorWithGuidanceAndFreesTheLock(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	author := f.agent(t, "author")
	server := f.serverAgent(t)
	gated := f.approvedTicket(t, author, "server/")
	_, err := f.serverClaim(server, gated.ID)
	require.NoError(t, err)
	require.NoError(t, f.store().AcquireLock(f.ctx, server, ticket.IntegrationLockName))

	guidance := "main has moved: merge it, rerun the tests, push, merge"
	require.NoError(t, f.store().ServerHandBack(f.ctx, server, gated.ID, guidance, 20*time.Minute))

	status, owed, window := f.attemptState(t, gated.ID)
	assert.Equal(t, "queued", status)
	assert.Equal(t, &author, owed)
	require.NotNil(t, window)
	assert.InDelta(t, 20*time.Minute, time.Until(*window), float64(time.Minute))
	assert.Equal(t, string(ticket.StatusOpen), f.ticketStatus(t, gated.ID))

	// The server holds nothing and the lock is free.
	_, err = f.store().LeaseFor(f.ctx, server)
	assert.Error(t, err)
	var locks int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM locks`).Scan(&locks))
	assert.Zero(t, locks)

	// Guidance reaches the author when it comes back; others are still held off.
	assert.Nil(t, f.claim(t, f.agent(t, "other")))
	back := f.claim(t, author)
	require.NotNil(t, back)
	assert.Equal(t, gated.ID, back.ID)
	assert.Equal(t, 1, f.commentCount(t, gated.ID, guidance))
	var seen []string
	for _, c := range f.detailFor(t, author, gated.ID).Comments {
		seen = append(seen, c.Body)
	}
	assert.Contains(t, seen, guidance)
}

func TestServerHandBack_Refusals(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	server := f.serverAgent(t)
	gated := f.approvedTicket(t, f.agent(t, "author"), "server/")
	assert.ErrorIs(t, f.store().ServerHandBack(f.ctx, server, gated.ID, "x", time.Minute), ticket.ErrNoLease)
	_, err := f.serverClaim(server, gated.ID)
	require.NoError(t, err)
	assert.ErrorIs(t, f.store().ServerHandBack(f.ctx, server, gated.ID, "", time.Minute), ticket.ErrEmptyComment)
}

func TestServerHandBack_NoWindowForAnAuthorThatIsNotAnEnabledWorker(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	author := f.agent(t, "author")
	server := f.serverAgent(t)
	gated := f.approvedTicket(t, author, "server/")
	_, err := f.serverClaim(server, gated.ID)
	require.NoError(t, err)
	_, err = f.agents.SetDisabled(f.ctx, author, true)
	require.NoError(t, err)

	require.NoError(t, f.store().ServerHandBack(f.ctx, server, gated.ID, "retry", time.Hour))
	_, _, window := f.attemptState(t, gated.ID)
	assert.Nil(t, window)
	require.NotNil(t, f.claim(t, f.agent(t, "other")), "nothing is coming back for a disabled author")
}

func TestServerReleaseLanding_EndsTheLeaseAndLeavesTheTicketQueued(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticket(t, "Lands", "server/")
	f.land(t, f.agent(t, "author"), created.ID)
	server := f.serverAgent(t)
	_, err := f.serverClaimLanding(server, created.ID)
	require.NoError(t, err)
	other := f.ticket(t, "Other", "other/")
	f.land(t, f.agent(t, "author2"), other.ID)
	_, err = f.store().ServerClaimLanding(f.ctx, server, other.ID, 1, ticket.DefaultServerLeaseTTL)
	require.Error(t, err, "one lease at a time, until released")

	require.NoError(t, f.store().ServerReleaseLanding(f.ctx, server, other.ID))
	var held int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM leases WHERE released_at IS NULL AND ticket_id = $1`, created.ID).Scan(&held))
	assert.Equal(t, 1, held, "another ticket's release leaves the lease")

	require.NoError(t, f.store().ServerReleaseLanding(f.ctx, server, created.ID))

	assert.Equal(t, map[int]string{1: "in_merge_queue"}, f.attemptStatuses(t, created.ID))
	assert.Equal(t, "in_merge_queue", f.ticketStatus(t, created.ID))
	_, err = f.store().ServerClaimLanding(f.ctx, server, other.ID, 1, ticket.DefaultServerLeaseTTL)
	require.NoError(t, err, "the claim is free at once")
	require.NoError(t, f.store().ServerReleaseLanding(f.ctx, server, created.ID))
	require.NoError(t, f.store().ServerReleaseLanding(f.ctx, server, created.ID), "nothing held is a no-op")
}
