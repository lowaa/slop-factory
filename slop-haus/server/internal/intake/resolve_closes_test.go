package intake_test

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/intake"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func closing(s *intake.Spec) { s.ResolveCloses = true }

// closingFixture is a fixture whose tenant a's member is the owner.

func newOwnedFixture(t *testing.T) *fixture {
	t.Helper()
	// The member exists only once the fixture has made the tenant, so the
	// owner check consults a map filled in afterwards.
	o := owners{}
	f := newFixture(t, withOwners(o))
	o[f.a.member] = true
	return f
}

func (f *fixture) closingSource() *source {
	f.t.Helper()
	return f.source(f.a, spec(trusted, closing, func(sp *intake.Spec) { sp.DefaultStatus = ticket.StatusOpen }))
}

func TestResolve_CancelsAnUnclaimedTicketWhenTheSourceCloses(t *testing.T) {
	f := newOwnedFixture(t)
	s := f.closingSource()
	f.send(s.post(event("open", "k", nil)))
	id := f.ticketFor(s, "k").ID

	rec := f.send(s.post(event("resolve", "k", map[string]any{"body": "all clear"})))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	got := f.ticketFor(s, "k")
	assert.Equal(t, ticket.StatusCancelled, got.Status, "cancelled, not done")
	events := f.external(s, id)
	require.Len(t, events, 2)
	assert.Equal(t, "resolve", events[1].Event)
	changes, err := f.tickets.ListStatusChanges(s.tenant.ctx, id)
	require.NoError(t, err)
	last := changes[len(changes)-1]
	require.NotNil(t, last.Cause)
	assert.Equal(t, "closed by intake: desk reported it resolved", last.Cause.Text)
	assert.Contains(t, f.deliveries(f.a)[0].Detail, "cancelled")
}

func TestResolve_OnlyCommentsWhenTheSourceDoesNotClose(t *testing.T) {
	f := newOwnedFixture(t)
	s := f.source(f.a, spec(trusted, func(sp *intake.Spec) { sp.DefaultStatus = ticket.StatusOpen }))
	f.send(s.post(event("open", "k", nil)))
	f.send(s.post(event("resolve", "k", map[string]any{"body": "all clear"})))
	got := f.ticketFor(s, "k")
	assert.Equal(t, ticket.StatusOpen, got.Status)
	events := f.external(s, got.ID)
	require.Len(t, events, 2)
	assert.Equal(t, "resolve", events[1].Event)
	assert.Equal(t, "all clear", events[1].Body)
}

func TestResolveCloses_IsAcceptedOnCreateAndListedOverHTTP(t *testing.T) {
	f := newOwnedFixture(t)
	mux := f.boardMux(f.a.who())

	created := decodeCredential(t, callBoard(t, mux, http.MethodPost, "/api/intake/sources",
		createBody(map[string]any{"trust": "trusted", "resolve_closes": true})))
	assert.True(t, created.Source.ResolveCloses)

	rec := callBoard(t, mux, http.MethodPost, "/api/intake/sources",
		createBody(map[string]any{"name": "other", "trust": "untrusted", "resolve_closes": true}))
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())

	listed := decodeList(t, callBoard(t, mux, http.MethodGet, "/api/intake/sources", "")).Body.Sources
	require.Len(t, listed, 1)
	assert.True(t, listed[0].ResolveCloses)

	other := f.newTenant()
	rec = callBoard(t, f.boardMux(other.who()), http.MethodPost, "/api/intake/sources",
		createBody(map[string]any{"trust": "trusted", "resolve_closes": true}))
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
}

func TestResolve_ClaimedOrTouchedTicketsOnlyGetTheComment(t *testing.T) {
	humanComment := func(f *fixture, id int64) {
		_, err := f.tickets.AddComment(f.a.ctx, id, ticket.Author{UserID: &f.a.member}, "looking")
		require.NoError(t, err)
	}
	for name, touch := range map[string]func(f *fixture, id int64){
		"claimed by an agent": func(f *fixture, id int64) {
			agents := agent.NewService(agent.NewPostgresStore(f.database), f.tickets, nil, slog.DiscardHandler)
			a, err := agents.Create(f.a.ctx, "builder")
			require.NoError(t, err)
			res, err := f.tickets.Claim(f.a.ctx, a.ID, nil)
			require.NoError(t, err)
			require.NotNil(t, res.Ticket)
			require.Equal(t, id, res.Ticket.ID)
		},
		"released after a claim": func(f *fixture, id int64) {
			agents := agent.NewService(agent.NewPostgresStore(f.database), f.tickets, nil, slog.DiscardHandler)
			a, err := agents.Create(f.a.ctx, "builder")
			require.NoError(t, err)
			_, err = f.tickets.Claim(f.a.ctx, a.ID, nil)
			require.NoError(t, err)
			_, err = f.database.Exec(f.a.ctx, `UPDATE leases SET released_at = now() WHERE ticket_id = $1`, id)
			require.NoError(t, err)
			_, err = f.database.Exec(f.a.ctx, `UPDATE tickets SET status = 'open' WHERE id = $1`, id)
			require.NoError(t, err)
		},
		"human comment": humanComment,
		"a person moved it": func(f *fixture, id int64) {
			ctx := ticket.WithUser(f.a.ctx, f.a.member)
			for _, to := range []ticket.Status{ticket.StatusParked, ticket.StatusPreparing} {
				_, err := f.tickets.Update(ctx, id, &ticket.UpdateParams{Status: &to})
				require.NoError(t, err)
			}
		},
		"another attempt added": func(f *fixture, id int64) {
			_, err := f.tickets.AddAttempt(f.a.ctx, f.a.member, id, nil)
			require.NoError(t, err)
		},
		"in review": func(f *fixture, id int64) {
			f.setStatus(f.a, id, ticket.StatusInProgress)
			f.setStatus(f.a, id, ticket.StatusReview)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newOwnedFixture(t)
			s := f.closingSource()
			f.send(s.post(event("open", "k", nil)))
			id := f.ticketFor(s, "k").ID
			touch(f, id)
			before := f.ticketFor(s, "k").Status

			rec := f.send(s.post(event("resolve", "k", nil)))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			assert.Equal(t, before, f.ticketFor(s, "k").Status)
			events := f.external(s, id)
			assert.Equal(t, "resolve", events[len(events)-1].Event, "the resolve is still recorded")
		})
	}
}

// blockedOn waits until a backend is waiting on a lock while running a query
// like pattern, so a test can act while a delivery is held up and not guess
// how long to sleep.
func (f *fixture) blockedOn(pattern string) {
	f.t.Helper()
	require.Eventually(f.t, func() bool {
		var n int
		require.NoError(f.t, f.database.Global().QueryRow(context.Background(), `
			SELECT count(*) FROM pg_stat_activity
			 WHERE wait_event_type = 'Lock' AND query ILIKE $1`, pattern).Scan(&n))
		return n > 0
	}, 30*time.Second, 10*time.Millisecond)
}

// resolveInBackground sends the resolve and returns a func that waits for it.
func (f *fixture) resolveInBackground(s *source) (wait func() int) {
	done := make(chan int, 1)
	go func() { done <- f.send(s.post(event("resolve", "k", nil))).Code }()
	return func() int { return <-done }
}

// Claiming and an intake resolve are decided one after the other under the
// claim lock: a resolve that arrives while the lock is held waits, and then
// sees whatever the holder did.
func TestResolve_WaitsForTheClaimLockAndSeesTheClaim(t *testing.T) {
	f := newOwnedFixture(t)
	s := f.closingSource()
	f.send(s.post(event("open", "k", nil)))
	id := f.ticketFor(s, "k").ID
	agents := agent.NewService(agent.NewPostgresStore(f.database), f.tickets, nil, slog.DiscardHandler)
	a, err := agents.Create(f.a.ctx, "builder")
	require.NoError(t, err)

	var wait func() int
	require.NoError(t, f.database.InTx(f.a.ctx, func(q db.Querier) error {
		require.NoError(t, db.TakeAdvisoryLock(f.a.ctx, q, db.ClaimLockKey))
		wait = f.resolveInBackground(s)
		f.blockedOn("%pg_advisory_xact_lock%")
		_, err := q.Exec(f.a.ctx, `INSERT INTO leases (ticket_id, attempt_number, agent_id, expires_at) VALUES ($1, 1, $2, now() + interval '1 hour')`, id, a.ID)
		return err
	}))
	require.Equal(t, http.StatusOK, wait())

	assert.Equal(t, ticket.StatusOpen, f.ticketFor(s, "k").Status, "claimed meanwhile, so not cancelled")
	assert.Equal(t, "resolve", f.external(s, id)[1].Event)
}

// Adding a comment does not take the claim lock; the resolve waits out a
// comment in flight instead of cancelling past it.
func TestResolve_WaitsForACommentInFlight(t *testing.T) {
	f := newOwnedFixture(t)
	s := f.closingSource()
	f.send(s.post(event("open", "k", nil)))
	id := f.ticketFor(s, "k").ID

	var wait func() int
	require.NoError(t, f.database.InTx(f.a.ctx, func(q db.Querier) error {
		_, err := q.Exec(f.a.ctx, `INSERT INTO ticket_comments (ticket_id, author_user_id, body) VALUES ($1, $2, 'on it')`, id, f.a.member)
		require.NoError(t, err)
		wait = f.resolveInBackground(s)
		f.blockedOn("%FOR UPDATE%")
		return nil
	}))
	require.Equal(t, http.StatusOK, wait())

	assert.Equal(t, ticket.StatusOpen, f.ticketFor(s, "k").Status, "commented meanwhile, so not cancelled")
}

func TestResolve_ThenOpenFollowsTheReopenPolicy(t *testing.T) {
	f := newOwnedFixture(t)
	s := f.source(f.a, spec(trusted, closing, func(sp *intake.Spec) {
		sp.DefaultStatus = ticket.StatusOpen
		sp.ReopenPolicy = intake.ReopenExisting
	}))
	f.send(s.post(event("open", "k", nil)))
	first := f.ticketFor(s, "k").ID
	f.send(s.post(event("resolve", "k", nil)))
	require.Equal(t, ticket.StatusCancelled, f.ticketFor(s, "k").Status)

	// A different body: an identical one inside the dedupe window is the same delivery.
	f.send(s.post(event("open", "k", map[string]any{"body": "it is back"})))
	got := f.ticketFor(s, "k")
	assert.Equal(t, first, got.ID)
	assert.Equal(t, ticket.StatusOpen, got.Status)
}

func TestCreate_ResolveClosesNeedsTheOwnerAndATrustedSource(t *testing.T) {
	f := newOwnedFixture(t)

	_, err := f.svc.Create(f.a.ctx, f.a.who(), spec(closing))
	require.ErrorIs(t, err, intake.ErrInvalidSource, "untrusted")

	nonOwner := f.newTenant()
	_, err = f.svc.Create(nonOwner.ctx, nonOwner.who(), spec(trusted, closing))
	require.ErrorIs(t, err, intake.ErrNotOwner)

	created, err := f.svc.Create(f.a.ctx, f.a.who(), spec(trusted, closing))
	require.NoError(t, err)
	assert.True(t, created.Source.ResolveCloses)

	plain, err := f.svc.Create(f.a.ctx, f.a.who(), spec(trusted, func(sp *intake.Spec) { sp.Name = "plain" }))
	require.NoError(t, err)
	assert.False(t, plain.Source.ResolveCloses, "off by default")
}

func TestCreate_ResolveClosesWithNoOwnerCheckIsRefused(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Create(f.a.ctx, f.a.who(), spec(trusted, closing))
	require.ErrorIs(t, err, intake.ErrNotOwner)
}

func TestDatabase_UntrustedSourceCannotResolveClose(t *testing.T) {
	f := newFixture(t)
	s := f.source(f.a, spec())
	_, err := f.database.Exec(f.a.ctx, `UPDATE intake_sources SET resolve_closes = true WHERE id = $1`, s.id)
	require.Error(t, err)
}
