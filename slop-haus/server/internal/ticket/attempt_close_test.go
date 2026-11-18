package ticket_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/authn/authntest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit/audittest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// twoHeld is a ticket whose attempts 1 (promoted) and 2 are each held by an agent.
func (f *fixture) twoHeld(t *testing.T) (tk *ticket.Ticket, first, second uuid.UUID) {
	t.Helper()
	tk = f.ticket(t, "Two held", "a/")
	require.Equal(t, 2, f.addAttempt(t, tk.ID, false))
	first, second = f.agent(t, "first"), f.agent(t, "second")
	require.NotNil(t, f.claim(t, first))
	require.NotNil(t, f.claim(t, second))
	require.Equal(t, 2, f.leasedAttempt(t, second))
	return tk, first, second
}

func (f *fixture) ticketStatus(t *testing.T, id int64) string {
	t.Helper()
	var status string
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT status FROM tickets WHERE id = $1`, id).Scan(&status))
	return status
}

func (f *fixture) closeAttempt(t *testing.T, id int64, n int) *ticket.AttemptClosed {
	t.Helper()
	closed, err := f.svc.CloseAttempt(f.ctx, f.human, id, n)
	require.NoError(t, err)
	return closed
}

func TestCloseAttempt_QueuedIsClosedAtOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.ticket(t, "Queued", "a/")
	f.addAttempt(t, tk.ID, false)

	closed := f.closeAttempt(t, tk.ID, 2)
	assert.Equal(t, "queued", closed.From)
	assert.False(t, closed.LeaseRevoked)
	assert.Equal(t, "closed", f.attemptRows(t, tk.ID)[2].Status)
	assert.Equal(t, "open", f.ticketStatus(t, tk.ID))

	var from, to, kind, cause string
	var actor uuid.UUID
	require.NoError(t, f.database.QueryRow(f.ctx, `
		SELECT from_status, to_status, actor_kind, actor_id, cause FROM attempt_status_changes
		 WHERE ticket_id = $1 AND attempt_number = 2 ORDER BY id DESC LIMIT 1`, tk.ID).Scan(&from, &to, &kind, &actor, &cause))
	assert.Equal(t, []string{"queued", "closed", "user", "closed by person"}, []string{from, to, kind, cause})
	assert.Equal(t, f.human, actor)
}

func TestCloseAttempt_InProgressEndsTheLeaseAndTellsTheAgent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk, first, second := f.twoHeld(t)
	// An unpromoted attempt cannot take the lock through the service, so the
	// row is written as one held from before.
	_, err := f.database.Exec(f.ctx, `
		INSERT INTO locks (name, holder_agent_id, holder_lease_id)
		SELECT 'integration', $1, l.id FROM leases l WHERE l.agent_id = $1 AND `+ticket.ActiveLeaseClause, second)
	require.NoError(t, err)
	require.Equal(t, 1, f.lockCount(t))

	closed := f.closeAttempt(t, tk.ID, 2)
	assert.Equal(t, "in_progress", closed.From)
	assert.True(t, closed.LeaseRevoked)
	assert.Zero(t, f.lockCount(t), "the integration lock goes with the lease")
	_, err = f.svc.LeaseFor(f.ctx, second)
	assert.ErrorIs(t, err, ticket.ErrNoLease)

	err = f.svc.Heartbeat(f.ctx, second)
	var revoked *ticket.LeaseRevokedError
	require.ErrorAs(t, err, &revoked)
	assert.ErrorIs(t, err, ticket.ErrLeaseRevoked)
	assert.Equal(t, 2, revoked.Attempt)
	assert.Contains(t, err.Error(), "attempt 2 of ticket")
	assert.Contains(t, err.Error(), "was closed by a human")
	assert.NotContains(t, err.Error(), "moved to")
	assert.ErrorIs(t, f.svc.Heartbeat(f.ctx, second), ticket.ErrNoLease, "told once")

	require.NoError(t, f.svc.Heartbeat(f.ctx, first), "the sibling's lease is untouched")
	assert.Equal(t, "in_progress", f.attemptRows(t, tk.ID)[1].Status)
	assert.Equal(t, "in_progress", f.ticketStatus(t, tk.ID))
}

func TestCloseAttempt_DiscussionAndReviewAreClosed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk, _, second := f.twoHeld(t)
	f.discuss(t, second)
	assert.Equal(t, "discussion", f.attemptRows(t, tk.ID)[2].Status)
	assert.Equal(t, "discussion", f.closeAttempt(t, tk.ID, 2).From)
	assert.Equal(t, "closed", f.attemptRows(t, tk.ID)[2].Status)

	other := f.ticket(t, "In review", "b/")
	f.addAttempt(t, other.ID, false)
	_, err := f.database.Exec(f.ctx, `UPDATE attempts SET status = 'review' WHERE ticket_id = $1 AND number = 2`, other.ID)
	require.NoError(t, err)
	assert.Equal(t, "review", f.closeAttempt(t, other.ID, 2).From)
	assert.Equal(t, "closed", f.attemptRows(t, other.ID)[2].Status)
}

func TestCloseAttempt_Refusals(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.ticket(t, "Refused", "a/")
	f.addAttempt(t, tk.ID, false)
	f.addAttempt(t, tk.ID, false)

	_, err := f.svc.CloseAttempt(f.ctx, f.human, tk.ID, 1)
	assert.ErrorIs(t, err, ticket.ErrClosePromoted)
	assert.Equal(t, "queued", f.attemptRows(t, tk.ID)[1].Status, "the promoted attempt is untouched")

	f.closeAttempt(t, tk.ID, 2)
	_, err = f.svc.CloseAttempt(f.ctx, f.human, tk.ID, 2)
	assert.ErrorIs(t, err, ticket.ErrAttemptClosed)

	_, err = f.database.Exec(f.ctx, `UPDATE attempts SET status = 'done' WHERE ticket_id = $1 AND number = 3`, tk.ID)
	require.NoError(t, err)
	_, err = f.svc.CloseAttempt(f.ctx, f.human, tk.ID, 3)
	assert.ErrorIs(t, err, ticket.ErrAttemptDone)

	_, err = f.svc.CloseAttempt(f.ctx, f.human, tk.ID, 9)
	assert.ErrorIs(t, err, ticket.ErrNoSuchAttempt)
	_, err = f.svc.CloseAttempt(f.ctx, f.human, 999999, 2)
	assert.ErrorIs(t, err, ticket.ErrNotFound)
}

func TestCloseAttempt_AnotherTenantsTicketIsNotFound(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.ticket(t, "Theirs", "a/")
	f.addAttempt(t, tk.ID, false)

	other := newFixtureOn(t, f.database)
	_, err := other.svc.CloseAttempt(other.ctx, other.human, tk.ID, 2)
	assert.ErrorIs(t, err, ticket.ErrNotFound)
	assert.Equal(t, "queued", f.attemptRows(t, tk.ID)[2].Status)
}

func TestCloseAttempt_LeavesThePromotedAttemptsStatusAlone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk, first, second := f.twoHeld(t)
	f.discuss(t, first)
	require.Equal(t, "discussion", f.ticketStatus(t, tk.ID))
	_ = second

	f.closeAttempt(t, tk.ID, 2)
	assert.Equal(t, "discussion", f.attemptRows(t, tk.ID)[1].Status)
	assert.Equal(t, "discussion", f.ticketStatus(t, tk.ID))
	assert.Zero(t, countOf(t, f, `SELECT count(*) FROM attempt_status_changes WHERE ticket_id = $1 AND attempt_number = 1 AND to_status = 'closed'`, tk.ID))
}

func countOf(t *testing.T, f *fixture, sql string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, f.database.QueryRow(f.ctx, sql, args...).Scan(&n))
	return n
}

// A close arriving while a claim is mid-transaction waits on the claim lock,
// so the claim's lease on the attempt is granted first and then ended by the
// close: never a closed attempt with a live lease. The claim is held inside
// its transaction until the close is seen waiting on the lock; without the
// lock the close would run past it, never be seen waiting, and fail the test.
func TestCloseAttempt_ACloseRacingAClaimEndsTheLeaseTheClaimGranted(t *testing.T) {
	f := newFixture(t)
	tk := f.ticket(t, "Raced", "a/")
	f.addAttempt(t, tk.ID, false)
	require.NotNil(t, f.claim(t, f.agent(t, "holds attempt 1")))
	racer := f.agent(t, "racer")

	inside, proceed := make(chan struct{}), make(chan struct{})
	var once, unblock sync.Once
	release := func() { unblock.Do(func() { close(proceed) }) }
	defer release()
	defer ticket.SetClaimBarrier(func() {
		once.Do(func() { close(inside) })
		<-proceed
	})()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		result, err := f.svc.Claim(f.ctx, racer, &tk.ID)
		assert.NoError(t, err)
		assert.NotNil(t, result.Ticket)
	}()
	<-inside
	wg.Add(1)
	var closed *ticket.AttemptClosed
	go func() {
		defer wg.Done()
		var err error
		closed, err = f.svc.CloseAttempt(f.ctx, f.human, tk.ID, 2)
		assert.NoError(t, err)
	}()
	f.awaitLockWaiter(t)
	release()
	wg.Wait()

	require.NotNil(t, closed)
	assert.True(t, closed.LeaseRevoked, "the claim's lease was seen by the close")
	assert.Equal(t, "closed", f.attemptRows(t, tk.ID)[2].Status)
	assert.Zero(t, countOf(t, f, `SELECT count(*) FROM leases l WHERE l.ticket_id = $1 AND l.attempt_number = 2 AND `+ticket.ActiveLeaseClause, tk.ID),
		"no live lease on the closed attempt")
	var revoked *ticket.LeaseRevokedError
	require.ErrorAs(t, f.svc.Heartbeat(f.ctx, racer), &revoked)
}

func TestCloseAttempt_ClearsWhatIsOwedToAnAgentAndSetsClosedAt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.ticket(t, "Owed", "a/")
	f.addAttempt(t, tk.ID, false)
	owed := f.agent(t, "owed")
	_, err := f.database.Exec(f.ctx,
		`UPDATE attempts SET resume_agent_id = $2, affinity_until = now() + interval '1 hour' WHERE ticket_id = $1 AND number = 2`, tk.ID, owed)
	require.NoError(t, err)

	f.closeAttempt(t, tk.ID, 2)
	assert.Equal(t, 1, countOf(t, f, `SELECT count(*) FROM attempts WHERE ticket_id = $1 AND number = 2
		AND resume_agent_id IS NULL AND affinity_until IS NULL AND closed_at IS NOT NULL`, tk.ID))
}

func TestCloseAttempt_TheAgentIsStillToldAfterTheNoticeWasDelivered(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk, _, second := f.twoHeld(t)
	f.closeAttempt(t, tk.ID, 2)
	require.ErrorIs(t, f.svc.Heartbeat(f.ctx, second), ticket.ErrLeaseRevoked)

	recent, err := f.svc.RecentRevocation(f.ctx, second)
	require.NoError(t, err)
	require.NotNil(t, recent)
	assert.Equal(t, 2, recent.Attempt)
	assert.Contains(t, recent.Describe(), "attempt 2")
	assert.Contains(t, recent.Describe(), "was closed by a human")
	assert.Equal(t, tk.ID, recent.TicketID)
}

func TestCloseAttempt_IsAudited(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var logged bytes.Buffer
	counter := audittest.NewCounter()
	svc := ticket.NewService(newStore(f.database), slog.DiscardHandler,
		ticket.WithEvents(audit.New(slog.NewJSONHandler(&logged, nil), counter)))
	tk := f.ticket(t, "Audited", "a/")
	f.addAttempt(t, tk.ID, false)

	_, err := svc.CloseAttempt(f.ctx, f.human, tk.ID, 1)
	require.Error(t, err)
	assert.Equal(t, 0, counter.Count(audit.AttemptClosed), "a refused close is not recorded")
	_, err = svc.CloseAttempt(f.ctx, f.human, tk.ID, 2)
	require.NoError(t, err)
	assert.Equal(t, 1, counter.Count(audit.AttemptClosed))
	assert.Contains(t, logged.String(), `"from_status":"queued"`)
}

type closerFake struct {
	calls []string
	err   error
}

func (c *closerFake) CloseAttemptChange(_ context.Context, tenant uuid.UUID, ticketID int64, attempt int) error {
	c.calls = append(c.calls, fmt.Sprintf("%s/%d/%d", tenant, ticketID, attempt))
	return c.err
}

func (f *fixture) closeMux(enabled bool, closer ticket.AttemptChangeCloser) http.Handler {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(authn.UserMiddleware(api, func(_ context.Context, _ string) (*authn.User, error) {
		return &authn.User{ID: f.human, TenantID: f.tenant}, nil
	}, authntest.Guard(), slog.New(slog.DiscardHandler)))
	ticket.RegisterRoutes(api, ticket.NewHandler(f.svc, slog.DiscardHandler,
		ticket.WithAttemptsAdd(enabled), ticket.WithAttemptChangeCloser(closer)))
	return mux
}

func TestPostTicketAttemptClose_Handler(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tk := f.ticket(t, "Handled", "a/")
	f.addAttempt(t, tk.ID, false)
	f.addAttempt(t, tk.ID, false)
	path := func(n int) string { return fmt.Sprintf("/api/tickets/%d/attempts/%d/close", tk.ID, n) }

	off := &closerFake{}
	rec := send(t, f.closeMux(false, off), http.MethodPost, path(2), ``)
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Equal(t, ticket.FeatureDisabledCode, errorCode(t, rec.Body.Bytes()))
	assert.Equal(t, "queued", f.attemptRows(t, tk.ID)[2].Status, "off closes nothing")
	assert.Empty(t, off.calls)

	closer := &closerFake{err: assert.AnError}
	mux := f.closeMux(true, closer)
	rec = send(t, mux, http.MethodPost, path(2), ``)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := decodeTicket(t, rec.Body.Bytes())
	require.Len(t, got.Attempts, 3)
	assert.Equal(t, "closed", got.Attempts[1].Status)
	assert.Equal(t, []string{fmt.Sprintf("%s/%d/2", f.tenant, tk.ID)}, closer.calls,
		"the change is closed, and a host failure leaves the attempt closed")

	assert.Equal(t, http.StatusConflict, send(t, mux, http.MethodPost, path(2), ``).Code, "already closed")
	assert.Equal(t, http.StatusUnprocessableEntity, send(t, mux, http.MethodPost, path(1), ``).Code, "promoted")
	missing := send(t, mux, http.MethodPost, path(9), ``)
	assert.Equal(t, http.StatusNotFound, missing.Code, "unknown attempt")
	assert.Empty(t, errorCode(t, missing.Body.Bytes()), "a missing attempt is not a switched-off feature")
	assert.Len(t, closer.calls, 1, "a refused close does not touch the host")
}
