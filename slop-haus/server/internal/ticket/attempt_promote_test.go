package ticket_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

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

func (f *fixture) promote(t *testing.T, id int64, n int) *ticket.AttemptPromoted {
	t.Helper()
	promoted, err := f.svc.PromoteAttempt(f.ctx, f.human, id, n)
	require.NoError(t, err)
	return promoted
}

// promotedOf is the number of the ticket's promoted attempt; the partial
// unique index lets at most one row match.
func (f *fixture) promotedOf(t *testing.T, id int64) int {
	t.Helper()
	var n int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT number FROM attempts WHERE ticket_id = $1 AND promoted_at IS NOT NULL`, id).Scan(&n))
	return n
}

// promoteTwo is a ticket whose attempt 1 (promoted) is running and whose
// shadow, attempt 2, is queued.
func (f *fixture) promoteTwo(t *testing.T) (id int64, a1 uuid.UUID) {
	t.Helper()
	tk := f.ticket(t, "Two", "a/")
	f.pinBase(t, tk.ID)
	a1 = f.agent(t, fmt.Sprintf("first-%s", uuid.NewString()))
	require.NotNil(t, f.claim(t, a1))
	require.Equal(t, 2, f.addAttempt(t, tk.ID, true))
	return tk.ID, a1
}

func TestPromoteAttempt_FromQueuedMovesThePromotionAndTheScope(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, a1 := f.promoteTwo(t)

	got := f.promote(t, id, 2)

	assert.Equal(t, &ticket.AttemptPromoted{Number: 2, From: "queued", Demoted: 1}, got)
	assert.Equal(t, 2, f.promotedOf(t, id))
	rows := f.attemptRows(t, id)
	assert.Equal(t, attemptRow{Status: "queued", Promoted: true, Shadow: false}, rows[2])
	assert.Equal(t, attemptRow{Status: "in_progress", Promoted: false, Shadow: true}, rows[1], "the demoted running attempt stops holding scope")
	assert.Equal(t, 1, f.leasedAttempt(t, a1), "its lease is untouched")
	assert.Equal(t, ticket.StatusInProgress, f.status(t, id))
	var by uuid.UUID
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT promoted_by_user_id FROM attempts WHERE ticket_id = $1 AND number = 2`, id).Scan(&by))
	assert.Equal(t, f.human, by)
}

func TestPromoteAttempt_FromInProgressChangesNothingAboutTheLease(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, a1 := f.promoteTwo(t)
	a2 := f.agent(t, "second-"+uuid.NewString())
	require.NotNil(t, f.claim(t, a2))
	require.Equal(t, 2, f.leasedAttempt(t, a2))

	got := f.promote(t, id, 2)

	assert.Equal(t, "in_progress", got.From)
	assert.False(t, got.Approved)
	assert.Equal(t, 2, f.leasedAttempt(t, a2))
	assert.Equal(t, 1, f.leasedAttempt(t, a1))
	rows := f.attemptRows(t, id)
	assert.True(t, rows[1].Shadow, "the demoted attempt holds no scope")
	assert.False(t, rows[2].Shadow, "the promoted attempt holds the ticket's")
}

func TestPromoteAttempt_FromDiscussionLeavesTheDiscussion(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, _ := f.promoteTwo(t)
	a2 := f.agent(t, "second-"+uuid.NewString())
	require.NotNil(t, f.claim(t, a2))
	f.discuss(t, a2)

	got := f.promote(t, id, 2)

	assert.Equal(t, "discussion", got.From)
	assert.Equal(t, "discussion", f.attemptRows(t, id)[2].Status)
	assert.Equal(t, ticket.StatusDiscussion, f.status(t, id))
}

func TestPromoteAttempt_AwaitingChoiceIsApprovedAndGoesBackToItsAgent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, a1 := f.promoteTwo(t)
	a2 := f.agent(t, "second-"+uuid.NewString())
	require.NotNil(t, f.claim(t, a2))
	f.review(t, a2)
	require.Equal(t, "review", f.attemptRows(t, id)[2].Status)
	_, err := f.database.Exec(f.ctx, `UPDATE tickets SET needs_discussion = true WHERE id = $1`, id)
	require.NoError(t, err)

	got := f.promote(t, id, 2)

	assert.True(t, got.Approved)
	var status string
	var resume *uuid.UUID
	var approved bool
	require.NoError(t, f.database.QueryRow(f.ctx,
		`SELECT status, resume_agent_id, approved_at IS NOT NULL FROM attempts WHERE ticket_id = $1 AND number = 2`, id).Scan(&status, &resume, &approved))
	assert.Equal(t, "queued", status)
	require.NotNil(t, resume)
	assert.Equal(t, a2, *resume, "back to its own agent by resume affinity")
	assert.True(t, approved)
	assert.Equal(t, "in_progress", f.attemptRows(t, id)[1].Status, "the demoted sibling keeps running")
	assert.Equal(t, 1, f.leasedAttempt(t, a1))
	assert.True(t, f.get(t, id).NeedsDiscussion, "the ticket's discussion flag is not the promotion's to clear")

	resumed := f.claim(t, a2)
	require.NotNil(t, resumed)
	assert.Equal(t, 2, f.leasedAttempt(t, a2))
	// The worker-facing text is worked out from this at each ticket_next and
	// ticket_get: a shadow that became promoted catches up from origin/main.
	branch := f.svc.EnsureAttemptBranch(f.ctx, f.get(t, id), 2)
	assert.False(t, branch.Unpromoted)
	assert.True(t, f.svc.EnsureAttemptBranch(f.ctx, f.get(t, id), 1).Unpromoted, "and the demoted one no longer does")
}

// awaitingChoice leaves attempt 2 in review, held by a2.
func (f *fixture) awaitingChoice(t *testing.T) (id int64, a2 uuid.UUID) {
	t.Helper()
	id, _ = f.promoteTwo(t)
	a2 = f.agent(t, "second-"+uuid.NewString())
	require.NotNil(t, f.claim(t, a2))
	f.review(t, a2)
	return id, a2
}

func (f *fixture) affinityUntil(t *testing.T, id int64, n int) *time.Time {
	t.Helper()
	var until *time.Time
	require.NoError(t, f.database.QueryRow(f.ctx,
		`SELECT affinity_until FROM attempts WHERE ticket_id = $1 AND number = $2`, id, n).Scan(&until))
	return until
}

func TestPromoteAttempt_BusyAgentIsNotReservedFor(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, a2 := f.awaitingChoice(t)
	f.ticket(t, "Other", "b/")
	require.NotNil(t, f.claim(t, a2), "a2 moves on to another ticket")
	other := f.agent(t, "other-"+uuid.NewString())

	got := f.promote(t, id, 2)

	assert.Equal(t, "skipped:busy", got.Affinity)
	assert.Nil(t, f.affinityUntil(t, id, 2))
	claimed := f.claim(t, other)
	require.NotNil(t, claimed, "any slot can take the attempt at once")
	assert.Equal(t, id, claimed.ID)
	assert.Equal(t, 2, f.leasedAttempt(t, other))
}

func TestPromoteAttempt_IdleAgentIsReservedFor(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, _ := f.awaitingChoice(t)

	got := f.promote(t, id, 2)

	assert.Equal(t, "reserved", got.Affinity)
	assert.NotNil(t, f.affinityUntil(t, id, 2))
}

func TestPromoteAttempt_RepromotionMovesItAndTheIndexHolds(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, _ := f.promoteTwo(t)

	f.promote(t, id, 2)
	back := f.promote(t, id, 1)

	assert.Equal(t, 2, back.Demoted)
	assert.Equal(t, 1, f.promotedOf(t, id))
	rows := f.attemptRows(t, id)
	assert.False(t, rows[2].Promoted)
	assert.True(t, rows[2].Shadow)
	assert.False(t, rows[1].Shadow)
	var promoted int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM attempts WHERE ticket_id = $1 AND promoted_at IS NOT NULL`, id).Scan(&promoted))
	assert.Equal(t, 1, promoted)
}

// Repeating a promotion is success that writes nothing, so it can be used to
// carry the changes to the code host again after a failure there.
func TestPromoteAttempt_AlreadyPromotedIsASilentSuccess(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var logged bytes.Buffer
	counter := audittest.NewCounter()
	svc := ticket.NewService(newStore(f.database), slog.DiscardHandler,
		ticket.WithEvents(audit.New(slog.NewJSONHandler(&logged, nil), counter)))
	id, _ := f.promoteTwo(t)
	before := f.attemptRows(t, id)

	got, err := svc.PromoteAttempt(f.ctx, f.human, id, 1)

	require.NoError(t, err)
	assert.Equal(t, &ticket.AttemptPromoted{Number: 1, From: "in_progress", Already: true}, got)
	assert.Equal(t, before, f.attemptRows(t, id))
	assert.Zero(t, counter.Count(audit.AttemptPromoted))
}

func TestPromoteAttempt_Refusals(t *testing.T) {
	t.Parallel()
	t.Run("unknown attempt and ticket", func(t *testing.T) {
		f := newFixture(t)
		id, _ := f.promoteTwo(t)
		_, err := f.svc.PromoteAttempt(f.ctx, f.human, id, 9)
		assert.ErrorIs(t, err, ticket.ErrNoSuchAttempt)
		_, err = f.svc.PromoteAttempt(f.ctx, f.human, 999999, 1)
		assert.ErrorIs(t, err, ticket.ErrNotFound)
	})
	t.Run("another tenant's ticket", func(t *testing.T) {
		f := newFixture(t)
		id, _ := f.promoteTwo(t)
		other := newFixtureOn(t, f.database)
		_, err := other.svc.PromoteAttempt(other.ctx, other.human, id, 2)
		assert.ErrorIs(t, err, ticket.ErrNotFound)
		assert.Equal(t, 1, f.promotedOf(t, id))
	})
	t.Run("closed and done attempts", func(t *testing.T) {
		f := newFixture(t)
		id, _ := f.promoteTwo(t)
		require.Equal(t, 3, f.addAttempt(t, id, true))
		f.closeAttempt(t, id, 2)
		_, err := f.svc.PromoteAttempt(f.ctx, f.human, id, 2)
		assert.ErrorIs(t, err, ticket.ErrAttemptClosed)
		f.run(t, `UPDATE attempts SET status = 'done' WHERE ticket_id = $1 AND number = 3`, id)
		_, err = f.svc.PromoteAttempt(f.ctx, f.human, id, 3)
		assert.ErrorIs(t, err, ticket.ErrAttemptDone)
		assert.Equal(t, 1, f.promotedOf(t, id))
	})
	t.Run("done and cancelled tickets", func(t *testing.T) {
		for _, status := range []ticket.Status{ticket.StatusDone, ticket.StatusCancelled} {
			f := newFixture(t)
			id, _ := f.promoteTwo(t)
			f.forceStatus(t, id, status)
			_, err := f.svc.PromoteAttempt(f.ctx, f.human, id, 2)
			assert.ErrorIs(t, err, ticket.ErrPromoteTicketFinished, string(status))
			assert.Equal(t, 1, f.promotedOf(t, id))
		}
	})
	t.Run("the promoted attempt holds the integration lock", func(t *testing.T) {
		f := newFixture(t)
		id, a1 := f.promoteTwo(t)
		require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, a1))
		_, err := f.svc.PromoteAttempt(f.ctx, f.human, id, 2)
		assert.ErrorIs(t, err, ticket.ErrPromoteMerging)
		assert.Equal(t, 1, f.promotedOf(t, id))
		assert.False(t, f.attemptRows(t, id)[2].Promoted)
	})
	t.Run("a scope another ticket holds", func(t *testing.T) {
		f := newFixture(t)
		id, a1 := f.promoteTwo(t)
		a3 := f.agent(t, "rival-"+uuid.NewString())
		a2 := f.agent(t, "second-"+uuid.NewString())
		require.NotNil(t, f.claim(t, a2))
		require.Equal(t, 2, f.leasedAttempt(t, a2))
		// Attempt 1 gone, nothing holds "a/" until the rival takes it.
		f.release(t, a1)
		rival := f.ticket(t, "Rival", "a/")
		claimed, err := f.svc.Claim(f.ctx, a3, &rival.ID)
		require.NoError(t, err)
		require.NotNil(t, claimed.Ticket, "the shadow holds no scope, so the rival may take it")

		_, err = f.svc.PromoteAttempt(f.ctx, f.human, id, 2)
		assert.ErrorIs(t, err, ticket.ErrScopeConflict)
		assert.Equal(t, 1, f.promotedOf(t, id), "a refused promotion moves nothing")
		assert.True(t, f.attemptRows(t, id)[2].Shadow)
	})
}

// A claim queued behind the promotion sees the finished promotion, never half
// of it; and a lock request queued behind it by the attempt it demoted is
// refused as any unpromoted attempt's is.
func TestPromoteAttempt_RacingTheDemotedAttemptsLockRequestLeavesOneOutcome(t *testing.T) {
	f := newFixture(t)
	id, a1 := f.promoteTwo(t)

	inside, proceed := make(chan struct{}), make(chan struct{})
	var once, unblock sync.Once
	release := func() { unblock.Do(func() { close(proceed) }) }
	defer release()
	defer ticket.SetPromoteBarrier(func() {
		once.Do(func() { close(inside) })
		<-proceed
	})()

	promoteDone := make(chan error, 1)
	go func() {
		_, err := f.svc.PromoteAttempt(f.ctx, f.human, id, 2)
		promoteDone <- err
	}()
	<-inside
	lockDone := make(chan error, 1)
	go func() { lockDone <- f.svc.AcquireIntegrationLock(f.ctx, a1) }()
	f.awaitLockWaiter(t)
	release()

	require.NoError(t, <-promoteDone)
	assert.ErrorIs(t, <-lockDone, ticket.ErrAttemptAwaitsChoice)
	assert.Zero(t, f.lockCount(t))
	assert.Equal(t, 2, f.promotedOf(t, id))
}

func TestPromoteAttempt_RacingAClaimOfThePromotedAttemptLeavesOneOutcome(t *testing.T) {
	f := newFixture(t)
	id, _ := f.promoteTwo(t)
	a2 := f.agent(t, "second-"+uuid.NewString())

	inside, proceed := make(chan struct{}), make(chan struct{})
	var once, unblock sync.Once
	release := func() { unblock.Do(func() { close(proceed) }) }
	defer release()
	defer ticket.SetPromoteBarrier(func() {
		once.Do(func() { close(inside) })
		<-proceed
	})()
	promoteDone := make(chan error, 1)
	go func() {
		_, err := f.svc.PromoteAttempt(f.ctx, f.human, id, 2)
		promoteDone <- err
	}()
	<-inside
	claimed := make(chan *ticket.ClaimResult, 1)
	go func() {
		result, err := f.svc.Claim(f.ctx, a2, nil)
		assert.NoError(t, err)
		claimed <- result
	}()
	f.awaitLockWaiter(t)
	release()

	require.NoError(t, <-promoteDone)
	result := <-claimed
	require.NotNil(t, result.Ticket)
	assert.Equal(t, 2, f.leasedAttempt(t, a2))
	assert.Equal(t, 2, f.promotedOf(t, id))
	assert.Equal(t, attemptRow{Status: "in_progress", Promoted: true, Shadow: false}, f.attemptRows(t, id)[2])
	assert.False(t, result.Unpromoted, "the claim was made on the promoted attempt")
}

func TestPromoteAttempt_IsAudited(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var logged bytes.Buffer
	counter := audittest.NewCounter()
	svc := ticket.NewService(newStore(f.database), slog.DiscardHandler,
		ticket.WithEvents(audit.New(slog.NewJSONHandler(&logged, nil), counter)))
	id, _ := f.promoteTwo(t)

	_, err := svc.PromoteAttempt(f.ctx, f.human, id, 2)
	require.NoError(t, err)

	assert.Equal(t, 1, counter.Count(audit.AttemptPromoted))
	var event map[string]any
	require.NoError(t, json.Unmarshal(logged.Bytes(), &event))
	assert.Equal(t, f.human.String(), event["user_id"])
	assert.EqualValues(t, 2, event["attempt"])
	assert.EqualValues(t, 1, event["demoted_attempt"])
	assert.Equal(t, "queued", event["from_status"])

	_, err = svc.PromoteAttempt(f.ctx, f.human, id, 9)
	require.Error(t, err)
	assert.Equal(t, 1, counter.Count(audit.AttemptPromoted), "a refused promotion is not recorded as one")
}

type promotionsFake struct {
	mu       sync.Mutex
	checkErr error
	applyErr error
	checked  []string
	applied  int
	// promotedAt reads the ticket's promoted attempt when ApplyPromotion is
	// called, and seen collects the answers.
	promotedAt func() int
	seen       []int
	// applyStarted is closed when ApplyPromotion is called, and the call then
	// waits for applyBlock to close; both nil leaves it immediate.
	applyStarted chan struct{}
	applyBlock   chan struct{}
}

func (p *promotionsFake) CheckPromotable(_ context.Context, tenant uuid.UUID, ticketID int64, attempt int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.checked = append(p.checked, fmt.Sprintf("%s/%d/%d", tenant, ticketID, attempt))
	return p.checkErr
}

func (p *promotionsFake) ApplyPromotion(_ context.Context, _ uuid.UUID, _ int64) error {
	if p.applyStarted != nil {
		close(p.applyStarted)
		<-p.applyBlock
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.applied++
	if p.promotedAt != nil {
		p.seen = append(p.seen, p.promotedAt())
	}
	return p.applyErr
}

func (f *fixture) promoteMux(enabled bool, p ticket.AttemptPromotionChanges) http.Handler {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(authn.UserMiddleware(api, func(_ context.Context, _ string) (*authn.User, error) {
		return &authn.User{ID: f.human, TenantID: f.tenant}, nil
	}, authntest.Guard(), slog.New(slog.DiscardHandler)))
	h := ticket.NewHandler(f.svc, slog.DiscardHandler, ticket.WithAttemptPromotion(enabled, p))
	f.promoteHandlers = append(f.promoteHandlers, h)
	ticket.RegisterRoutes(api, h)
	return mux
}

// waitCarries waits for the promotions the muxes made have reached the fake
// host, which happens after their responses.
func (f *fixture) waitCarries() {
	for _, h := range f.promoteHandlers {
		h.WaitForCarries()
	}
}

func TestPostTicketAttemptPromote_Handler(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, _ := f.promoteTwo(t)
	path := func(n int) string { return fmt.Sprintf("/api/tickets/%d/attempts/%d/promote", id, n) }

	off := &promotionsFake{}
	rec := send(t, f.promoteMux(false, off), http.MethodPost, path(2), ``)
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Equal(t, ticket.FeatureDisabledCode, errorCode(t, rec.Body.Bytes()))
	assert.Equal(t, 1, f.promotedOf(t, id), "off promotes nothing")
	assert.Empty(t, off.checked)

	blocked := &promotionsFake{checkErr: fmt.Errorf("%w: took in main", ticket.ErrPromotionBlocked)}
	assert.Equal(t, http.StatusConflict, send(t, f.promoteMux(true, blocked), http.MethodPost, path(2), ``).Code)
	unverified := &promotionsFake{checkErr: fmt.Errorf("%w: host down", ticket.ErrPromotionUnverified)}
	assert.Equal(t, http.StatusServiceUnavailable, send(t, f.promoteMux(true, unverified), http.MethodPost, path(2), ``).Code)
	assert.Equal(t, 1, f.promotedOf(t, id), "a refused check promotes nothing")
	assert.Equal(t, []string{fmt.Sprintf("%s/%d/2", f.tenant, id)}, blocked.checked, "the check is of the requested tenant, ticket and attempt")
	assert.Zero(t, blocked.applied)

	// The host failing after the commit leaves the promotion made.
	changes := &promotionsFake{applyErr: assert.AnError, promotedAt: func() int { return f.promotedOf(t, id) }}
	mux := f.promoteMux(true, changes)
	rec = send(t, mux, http.MethodPost, path(2), ``)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := decodeTicket(t, rec.Body.Bytes())
	require.Len(t, got.Attempts, 2)
	assert.Equal(t, 2, f.promotedOf(t, id))
	f.waitCarries()
	assert.Equal(t, 1, changes.applied)
	assert.Equal(t, []int{2}, changes.seen, "the host is called after the commit")

	assert.Equal(t, http.StatusNotFound, send(t, mux, http.MethodPost, path(9), ``).Code, "unknown attempt")
	f.waitCarries()
	assert.Equal(t, 1, changes.applied, "a refused promotion does not touch the host")
	assert.Equal(t, http.StatusOK, send(t, mux, http.MethodPost, path(2), ``).Code, "already promoted is success")
	f.waitCarries()
	assert.Equal(t, 2, changes.applied, "and carries the changes to the host again")
}

func TestPostTicketAttemptPromote_AnswersWhileTheHostIsStillBeingCarriedTo(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, _ := f.promoteTwo(t)
	release := make(chan struct{})
	started := make(chan struct{})
	changes := &promotionsFake{applyBlock: release, applyStarted: started}
	mux := f.promoteMux(true, changes)

	rec := send(t, mux, http.MethodPost, fmt.Sprintf("/api/tickets/%d/attempts/2/promote", id), ``)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	<-started
	assert.Equal(t, 2, f.promotedOf(t, id), "the response came while the host call is still blocked")

	close(release)
	f.waitCarries()
	assert.Equal(t, 1, changes.applied)
}
