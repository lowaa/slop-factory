package ticket_test

import (
	"encoding/json"
	"fmt"
	clockfake "github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/jobs"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// clock is the time cooldowns are timed by: set to the past, it makes a
// cooldown that has already ended by the database's clock, with no waiting.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t.UTC().Truncate(time.Microsecond)
}

type cooldownFixture struct {
	*fixture
	clock *clock
	sweep *ticket.CooldownSweeper
}

// The shipped defaults, so a change to them is a change these tests see.
var testPolicy = ticket.DefaultCooldownPolicy()

func newCooldownFixture(t *testing.T, policy ticket.CooldownPolicy) *cooldownFixture {
	t.Helper()
	f := newFixture(t)
	c := &clock{}
	c.set(time.Now())
	store := newStore(f.database)
	f.svc = ticket.NewService(store, slog.DiscardHandler, ticket.WithCooldownPolicy(policy), ticket.WithClock(c.now))
	return &cooldownFixture{fixture: f, clock: c, sweep: ticket.NewCooldownSweeper(store, 0, slog.DiscardHandler)}
}

// held has agent claim a fresh ticket.
func (f *cooldownFixture) held(t *testing.T, agent uuid.UUID) *ticket.Ticket {
	t.Helper()
	f.clock.set(time.Now())
	made := f.ticket(t, "Gated on a deploy", "server/")
	claimed, err := f.svc.Claim(f.ctx, agent, &made.ID)
	require.NoError(t, err)
	require.NotNil(t, claimed.Ticket)
	return made
}

func (f *cooldownFixture) releaseAt(t *testing.T, agent uuid.UUID, at time.Time, p ticket.ReleaseParams) *ticket.ReleaseOutcome {
	t.Helper()
	f.clock.set(at)
	out, err := f.svc.ReleaseWith(f.ctx, agent, p)
	require.NoError(t, err)
	return out
}

func (f *cooldownFixture) sweepOnce(t *testing.T) {
	t.Helper()
	require.NoError(t, f.sweep.SweepOnce(f.ctx))
}

func (f *cooldownFixture) claimable(t *testing.T, agent uuid.UUID) bool {
	t.Helper()
	peeked, err := f.svc.Peek(f.ctx, agent, nil)
	require.NoError(t, err)
	claimed, err := f.svc.Claim(f.ctx, agent, nil)
	require.NoError(t, err)
	require.Equal(t, peeked.Ticket != nil, claimed.Ticket != nil, "Peek and Claim agree")
	return claimed.Ticket != nil
}

func (f *cooldownFixture) streak(t *testing.T, id int64) (releases int, blockedUntil *time.Time) {
	t.Helper()
	require.NoError(t, f.database.QueryRow(f.ctx,
		`SELECT cooldown_releases, blocked_until FROM tickets WHERE id = $1`, id).Scan(&releases, &blockedUntil))
	return releases, blockedUntil
}

func (f *cooldownFixture) lastComment(t *testing.T, id int64) string {
	t.Helper()
	comments := f.comments(t, id)
	require.NotEmpty(t, comments)
	return comments[len(comments)-1].Body
}

func TestCooldown_ABareReleaseIsHeldOutUntilTheCooldownEnds(t *testing.T) {
	t.Parallel()
	f := newCooldownFixture(t, testPolicy)
	a := f.agent(t, "builder-1")
	made := f.held(t, a)

	out := f.releaseAt(t, a, time.Now(), ticket.ReleaseParams{Reason: "gate not met"})

	assert.Equal(t, ticket.StatusBlocked, out.Ticket.Status)
	require.NotNil(t, out.RetryAt)
	assert.Equal(t, f.clock.now().Add(testPolicy.Min), *out.RetryAt, "a bare release gets the minimum")
	assert.False(t, f.claimable(t, a), "the same agent cannot re-claim it")
	f.sweepOnce(t)
	assert.Equal(t, ticket.StatusBlocked, f.status(t, made.ID), "a sweep before the end leaves it blocked")
	assert.Contains(t, f.lastComment(t, made.ID), "gate not met")
	assert.Contains(t, f.lastComment(t, made.ID), "Cooling down: not claimable again until")
}

func TestCooldown_TheSweepReopensATicketOnceItsCooldownHasEnded(t *testing.T) {
	t.Parallel()
	f := newCooldownFixture(t, testPolicy)
	a := f.agent(t, "builder-1")
	made := f.held(t, a)

	f.releaseAt(t, a, time.Now(), ticket.ReleaseParams{Reason: "gate not met"})
	// The five minutes are over by the database's clock. Written after the
	// release: a ticket whose cooldown has already ended when it is written
	// lands open at once, as the status is worked out from the row.
	_, err := f.database.Exec(f.ctx, `UPDATE tickets SET blocked_until = now() - interval '1 hour' WHERE id = $1`, made.ID)
	require.NoError(t, err)

	assert.False(t, f.claimable(t, a), "still blocked until the sweep runs: nothing else re-judges it")
	f.sweepOnce(t)
	assert.Equal(t, ticket.StatusOpen, f.status(t, made.ID))
	assert.True(t, f.claimable(t, a))
}

func TestCooldown_TheSweepLeavesAnArchivedTenantsTicketsAlone(t *testing.T) {
	t.Parallel()
	f := newCooldownFixture(t, testPolicy)
	a := f.agent(t, "builder-1")
	made := f.held(t, a)
	f.releaseAt(t, a, time.Now(), ticket.ReleaseParams{Reason: "gate not met"})
	_, err := f.database.Exec(f.ctx, `UPDATE tickets SET blocked_until = now() - interval '1 hour' WHERE id = $1`, made.ID)
	require.NoError(t, err)

	dbtest.ArchiveTenant(t, f.database, f.tenant)
	f.sweepOnce(t)
	assert.Equal(t, ticket.StatusBlocked, f.status(t, made.ID), "nothing is reopened for an archived tenant")

	dbtest.UnarchiveTenant(t, f.database, f.tenant)
	f.sweepOnce(t)
	assert.Equal(t, ticket.StatusOpen, f.status(t, made.ID))
}

func TestCooldown_RunSweepsAgainOnEachTick(t *testing.T) {
	t.Parallel()
	f := newCooldownFixture(t, testPolicy)
	a := f.agent(t, "builder-1")
	made := f.held(t, a)
	// Live for an hour: the first pass finds nothing due.
	f.releaseAt(t, a, time.Now(), ticket.ReleaseParams{Reason: "gate not met", RetryAfter: time.Hour})

	sweeper := ticket.NewCooldownSweeper(newStore(f.database), time.Minute, slog.DiscardHandler)
	clk := clockfake.NewFake(time.Unix(1_000_000, 0))
	passes := make(chan error, 4)
	sched := jobs.New(&jobs.Options{Clock: clk, AfterRun: func(_ string, err error) { passes <- err }})
	_, err := sched.Start(f.ctx, sweeper.Job())
	require.NoError(t, err)
	defer sched.Close()
	require.NoError(t, <-passes)
	require.Equal(t, ticket.StatusBlocked, f.status(t, made.ID), "the pass at startup left it")

	_, err = f.database.Exec(f.ctx, `UPDATE tickets SET blocked_until = now() - interval '1 minute' WHERE id = $1`, made.ID)
	require.NoError(t, err)
	clk.BlockUntil(1)
	clk.Advance(time.Minute)
	require.NoError(t, <-passes)
	require.Equal(t, ticket.StatusOpen, f.status(t, made.ID), "the next tick reopened the ticket")
}

func TestCooldown_ZeroIntervalHasNoJob(t *testing.T) {
	t.Parallel()
	f := newCooldownFixture(t, testPolicy)
	assert.Nil(t, f.sweep.Job())
}

func TestCooldown_RetryAfterIsClampedToThePolicy(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		asked time.Duration
		want  time.Duration
	}{
		{"below the minimum", time.Second, testPolicy.Min},
		{"within", 30 * time.Minute, 30 * time.Minute},
		{"above the maximum", 100 * time.Hour, testPolicy.Max},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCooldownFixture(t, testPolicy)
			a := f.agent(t, "builder-1")
			f.held(t, a)

			out := f.releaseAt(t, a, time.Now(), ticket.ReleaseParams{Reason: "waiting", RetryAfter: tc.asked})

			require.NotNil(t, out.RetryAt)
			assert.Equal(t, f.clock.now().Add(tc.want), *out.RetryAt)
		})
	}
}

func TestCooldown_ANegativeRetryAfterIsRefused(t *testing.T) {
	t.Parallel()
	f := newCooldownFixture(t, testPolicy)
	a := f.agent(t, "builder-1")
	made := f.held(t, a)

	_, err := f.svc.ReleaseWith(f.ctx, a, ticket.ReleaseParams{Reason: "x", RetryAfter: -time.Minute})

	require.ErrorIs(t, err, ticket.ErrInvalidRetryAfter)
	assert.Equal(t, ticket.StatusInProgress, f.status(t, made.ID), "still held")
}

func TestCooldown_WithoutAPolicyAReleaseRequeuesAtOnce(t *testing.T) {
	t.Parallel()
	f := newCooldownFixture(t, ticket.CooldownPolicy{})
	a := f.agent(t, "builder-1")
	f.held(t, a)

	out := f.releaseAt(t, a, time.Now(), ticket.ReleaseParams{Reason: "x", RetryAfter: time.Hour})

	assert.Equal(t, ticket.StatusOpen, out.Ticket.Status)
	assert.Nil(t, out.RetryAt)
}

func TestCooldown_BlockedOnAloneIsNotCooledAndOpensWithItsBlocker(t *testing.T) {
	t.Parallel()
	f := newCooldownFixture(t, testPolicy)
	a, b := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	dep := f.held(t, b)
	waiting := f.ticket(t, "Waiting", "web/")
	_, err := f.svc.Claim(f.ctx, a, &waiting.ID)
	require.NoError(t, err)

	out := f.releaseAt(t, a, time.Now(), ticket.ReleaseParams{Reason: "needs it", BlockedOn: &dep.ID})

	assert.Nil(t, out.RetryAt)
	_, err = f.svc.Done(f.ctx, b, &ticket.DoneParams{Summary: "done"})
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusOpen, f.status(t, waiting.ID))
}

func TestCooldown_WithBlockedOnTheTicketReopensOnlyWhenBothClear(t *testing.T) {
	t.Parallel()
	f := newCooldownFixture(t, testPolicy)
	a, b := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	dep := f.held(t, b)
	waiting := f.ticket(t, "Waiting", "web/")
	_, err := f.svc.Claim(f.ctx, a, &waiting.ID)
	require.NoError(t, err)
	f.releaseAt(t, a, time.Now(), ticket.ReleaseParams{Reason: "needs it", BlockedOn: &dep.ID, RetryAfter: time.Hour})

	// The blocker finishes inside the cooldown: still held.
	f.clock.set(time.Now())
	_, err = f.svc.Done(f.ctx, b, &ticket.DoneParams{Summary: "done"})
	require.NoError(t, err)
	f.sweepOnce(t)
	assert.Equal(t, ticket.StatusBlocked, f.status(t, waiting.ID), "the cooldown still runs")

	// The cooldown ends: nothing else is left.
	_, err = f.database.Exec(f.ctx, `UPDATE tickets SET blocked_until = now() - interval '1 minute' WHERE id = $1`, waiting.ID)
	require.NoError(t, err)
	f.sweepOnce(t)
	assert.Equal(t, ticket.StatusOpen, f.status(t, waiting.ID))
}

func TestCooldown_AnEndedCooldownDoesNotReopenATicketWithAnUnfinishedBlocker(t *testing.T) {
	t.Parallel()
	f := newCooldownFixture(t, testPolicy)
	a, b := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	dep := f.held(t, b)
	waiting := f.ticket(t, "Waiting", "web/")
	_, err := f.svc.Claim(f.ctx, a, &waiting.ID)
	require.NoError(t, err)
	f.releaseAt(t, a, time.Now().Add(-time.Hour), ticket.ReleaseParams{Reason: "needs it", BlockedOn: &dep.ID, RetryAfter: time.Minute})

	f.sweepOnce(t)

	assert.Equal(t, ticket.StatusBlocked, f.status(t, waiting.ID))
}

func TestCooldown_RepeatedCooldownReleasesEscalateToDiscussion(t *testing.T) {
	t.Parallel()
	f := newCooldownFixture(t, testPolicy)
	a := f.agent(t, "builder-1")
	made := f.held(t, a)
	past := time.Now().Add(-2 * time.Hour)

	for i := 1; i < testPolicy.EscalateAfter; i++ {
		out := f.releaseAt(t, a, past, ticket.ReleaseParams{Reason: "still not deployed"})
		require.False(t, out.Escalated, "release %d", i)
		f.sweepOnce(t)
		claimed, err := f.svc.Claim(f.ctx, a, nil)
		require.NoError(t, err)
		require.NotNil(t, claimed.Ticket, "reclaimed after check %d", i)
	}
	out := f.releaseAt(t, a, past, ticket.ReleaseParams{Reason: "prod still runs 3608e9a0"})

	assert.True(t, out.Escalated)
	assert.Equal(t, testPolicy.EscalateAfter, out.Checks)
	assert.Equal(t, ticket.StatusDiscussion, out.Ticket.Status)
	assert.Nil(t, out.RetryAt)
	f.sweepOnce(t)
	assert.False(t, f.claimable(t, a), "a person has to answer")
	last := f.lastComment(t, made.ID)
	assert.Contains(t, last, "after 3 checks")
	assert.Contains(t, last, "prod still runs 3608e9a0", "quotes the last reason")
}

func TestCooldown_ARunLongerThanTheTotalLimitEscalates(t *testing.T) {
	t.Parallel()
	policy := testPolicy
	policy.MaxTotal = time.Hour
	f := newCooldownFixture(t, policy)
	a := f.agent(t, "builder-1")
	f.held(t, a)

	first := f.releaseAt(t, a, time.Now().Add(-3*time.Hour), ticket.ReleaseParams{Reason: "waiting"})
	require.False(t, first.Escalated)
	f.sweepOnce(t)
	claimed, err := f.svc.Claim(f.ctx, a, nil)
	require.NoError(t, err)
	require.NotNil(t, claimed.Ticket)

	second := f.releaseAt(t, a, time.Now(), ticket.ReleaseParams{Reason: "waiting"})

	assert.True(t, second.Escalated, "two checks, but three hours after the first")
}

func TestCooldown_AnyOtherExitResetsTheCount(t *testing.T) {
	t.Parallel()
	f := newCooldownFixture(t, testPolicy)
	a := f.agent(t, "builder-1")
	made := f.held(t, a)
	past := time.Now().Add(-2 * time.Hour)
	reclaim := func() {
		f.sweepOnce(t)
		claimed, err := f.svc.Claim(f.ctx, a, nil)
		require.NoError(t, err)
		require.NotNil(t, claimed.Ticket)
	}
	f.releaseAt(t, a, past, ticket.ReleaseParams{Reason: "waiting"})
	reclaim()
	f.releaseAt(t, a, past, ticket.ReleaseParams{Reason: "waiting"})
	reclaim()
	releases, _ := f.streak(t, made.ID)
	require.Equal(t, 2, releases)

	_, err := f.svc.Discuss(f.ctx, a, "which way?", "handoff")
	require.NoError(t, err)

	releases, until := f.streak(t, made.ID)
	assert.Zero(t, releases)
	assert.Nil(t, until)
}

func TestCooldown_APersonsMoveClearsTheCooldown(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		to   ticket.Status
	}{
		{"to open", ticket.StatusOpen},
		{"to preparing", ticket.StatusPreparing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCooldownFixture(t, testPolicy)
			a := f.agent(t, "builder-1")
			made := f.held(t, a)
			f.releaseAt(t, a, time.Now(), ticket.ReleaseParams{Reason: "waiting"})
			releases, until := f.streak(t, made.ID)
			require.Equal(t, 1, releases)
			require.NotNil(t, until)

			to := tc.to
			_, err := f.svc.Update(f.ctx, made.ID, &ticket.UpdateParams{Status: &to})
			require.NoError(t, err)

			releases, until = f.streak(t, made.ID)
			assert.Zero(t, releases)
			assert.Nil(t, until)
			if tc.to == ticket.StatusOpen {
				assert.True(t, f.claimable(t, a), "claimable now, not after the cooldown")
			}
		})
	}
}

func TestCooldown_ALongRetryAfterAloneDoesNotTripTheTotalLimit(t *testing.T) {
	t.Parallel()
	policy := testPolicy
	policy.MaxTotal = time.Hour
	f := newCooldownFixture(t, policy)
	a := f.agent(t, "builder-1")
	f.held(t, a)

	out := f.releaseAt(t, a, time.Now(), ticket.ReleaseParams{Reason: "waiting", RetryAfter: 10 * time.Hour})

	assert.False(t, out.Escalated, "the streak has only just begun, however long this wait is")
}

func TestCooldown_WithoutAnInjectedClockItIsTimedByTheDatabase(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.svc = ticket.NewService(newStore(f.database), slog.DiscardHandler, ticket.WithCooldownPolicy(testPolicy))
	a := f.agent(t, "builder-1")
	made := f.ticket(t, "Gated", "server/")
	_, err := f.svc.Claim(f.ctx, a, &made.ID)
	require.NoError(t, err)

	out, err := f.svc.ReleaseWith(f.ctx, a, ticket.ReleaseParams{Reason: "gate not met"})

	require.NoError(t, err)
	var fromDB time.Duration
	require.NoError(t, f.database.QueryRow(f.ctx,
		`SELECT blocked_until - now() FROM tickets WHERE id = $1`, made.ID).Scan(&fromDB))
	assert.InDelta(t, testPolicy.Min.Seconds(), fromDB.Seconds(), 5, "the minimum, from the database's now()")
	require.NotNil(t, out.RetryAt)
}

func TestCooldown_APersonsMoveToOpenKeepsADependencyBlock(t *testing.T) {
	t.Parallel()
	f := newCooldownFixture(t, testPolicy)
	a, b := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	dep := f.held(t, b)
	waiting := f.ticket(t, "Waiting", "web/")
	_, err := f.svc.Claim(f.ctx, a, &waiting.ID)
	require.NoError(t, err)
	f.releaseAt(t, a, time.Now(), ticket.ReleaseParams{Reason: "needs it", BlockedOn: &dep.ID, RetryAfter: time.Hour})

	to := ticket.StatusOpen
	_, err = f.svc.Update(f.ctx, waiting.ID, &ticket.UpdateParams{Status: &to})
	require.NoError(t, err)

	releases, until := f.streak(t, waiting.ID)
	assert.Zero(t, releases)
	assert.Nil(t, until)
	assert.Equal(t, ticket.StatusBlocked, f.status(t, waiting.ID), "the unfinished blocker still holds it")
}

func TestCooldown_TheCooldownIsOnTheDetailTheListAndTheBoardDelta(t *testing.T) {
	t.Parallel()
	f := newCooldownFixture(t, testPolicy)
	a := f.agent(t, "builder-1")
	plain := f.ticket(t, "No cooldown", "docs/")
	made := f.held(t, a)
	since := f.boardRevision(t)
	f.releaseAt(t, a, time.Now(), ticket.ReleaseParams{Reason: "gate not met"})

	detail := f.detail(t, made.ID)
	require.NotNil(t, detail.BlockedUntil)
	assert.Equal(t, f.clock.now().Add(testPolicy.Min), detail.BlockedUntil.UTC())
	assert.Equal(t, 1, detail.CooldownReleases)
	require.NotNil(t, detail.CooldownSince)

	rec := send(t, f.boardMux(), http.MethodGet, "/api/tickets", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list ticket.ListTicketsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list.Body))
	byID := map[int64]ticket.TicketBody{}
	for _, tb := range list.Body.Tickets {
		byID[tb.ID] = tb
	}
	require.NotNil(t, byID[made.ID].BlockedUntil, "the card list carries it")
	assert.Equal(t, 1, byID[made.ID].CooldownReleases)
	require.NotNil(t, byID[made.ID].CooldownSince)

	delta := f.boardDelta(t, since)
	require.False(t, delta.Reload)
	require.Equal(t, []int64{made.ID}, deltaIDs(delta))
	require.NotNil(t, delta.Tickets[0].BlockedUntil, "a card updates live when a cooldown starts")
	assert.Equal(t, 1, delta.Tickets[0].CooldownReleases)
	require.NotNil(t, delta.Tickets[0].CooldownSince)

	none := f.detail(t, plain.ID)
	assert.Nil(t, none.BlockedUntil)
	assert.Zero(t, none.CooldownReleases)
	assert.Nil(t, none.CooldownSince)
	require.Contains(t, byID, plain.ID)
	assert.Nil(t, byID[plain.ID].BlockedUntil)
	assert.Zero(t, byID[plain.ID].CooldownReleases)
	raw := send(t, f.boardMux(), http.MethodGet, fmt.Sprintf("/api/tickets/%d", plain.ID), "").Body.String()
	assert.NotContains(t, raw, "cooldown_releases", "left out with no streak")
}
