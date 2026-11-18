package landing_test

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit/audittest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

type guardFixture struct {
	*fixture
	guard    *landing.EnqueueGuard
	counter  *audittest.Counter
	ticketID int64
}

func newGuardFixture(t *testing.T) *guardFixture {
	f := newFixture(t)
	counter := audittest.NewCounter()
	g := &guardFixture{fixture: f, counter: counter}
	g.guard = landing.NewEnqueueGuard(f.outcomes, f.tickets, audit.New(slog.DiscardHandler, counter))
	g.ticketID = g.newTicket("Add the thing", ticket.StatusOpen)
	return g
}

func (g *guardFixture) newTicket(title string, status ticket.Status) int64 {
	g.t.Helper()
	created, err := g.tickets.Create(g.ctx, &ticket.CreateParams{
		Title: title, Paths: []model.Path{{Path: "app/server/" + uuid.NewString() + "/"}}, Status: ticket.StatusOpen, CreatedByUserID: &g.creator,
	})
	require.NoError(g.t, err)
	// Straight to the state under test: the guard reads it and no workflow matters here.
	_, err = g.database.Exec(g.ctx, `UPDATE tickets SET status = $1 WHERE id = $2`, string(status), created.ID)
	require.NoError(g.t, err)
	return created.ID
}

func (g *guardFixture) record(o landing.Outcome, head string) {
	g.t.Helper()
	require.NoError(g.t, g.outcomes.Record(g.ctx, &landing.Record{TicketID: g.ticketID, AttemptNumber: 1, ChangeURL: "https://example.test/pr/1", HeadSHA: head, Outcome: o}))
}

func (g *guardFixture) check(head string, flake int64) error {
	return g.guard.Check(g.ctx, g.tenant, uuid.New(), g.ticketID, head, flake)
}

func TestEnqueueGuard_RefusesTheSameHeadAfterRed(t *testing.T) {
	g := newGuardFixture(t)
	require.NoError(t, g.check(headA, 0))
	g.record(landing.ChecksRed, headA)

	err := g.check(headA, 0)
	require.ErrorIs(t, err, landing.ErrRedRequeue)
	assert.Contains(t, err.Error(), headA)
	assert.Contains(t, err.Error(), "red_was_flake")
	assert.Equal(t, 1, g.counter.Count(audit.LandingRequeueRefused))
}

func TestEnqueueGuard_AcceptsANewHead(t *testing.T) {
	g := newGuardFixture(t)
	g.record(landing.ChecksRed, headA)
	require.NoError(t, g.check(headB, 0))
}

func TestEnqueueGuard_OtherOutcomesDoNotTrigger(t *testing.T) {
	g := newGuardFixture(t)
	for _, o := range []landing.Outcome{landing.MergeMainConflict, landing.MainMoved, landing.CodeHostError, landing.ChecksPendingTimeout} {
		g.record(o, headA)
	}
	require.NoError(t, g.check(headA, 0))
}

func TestEnqueueGuard_AnotherTicketsRedDoesNotTrigger(t *testing.T) {
	g := newGuardFixture(t)
	require.NoError(t, g.outcomes.Record(g.ctx, &landing.Record{TicketID: g.newTicket("Other", ticket.StatusOpen), AttemptNumber: 1, ChangeURL: "u", HeadSHA: headA, Outcome: landing.ChecksRed}))
	require.NoError(t, g.check(headA, 0))
}

func TestEnqueueGuard_AFlakeCitationAllowsExactlyOneMoreAttempt(t *testing.T) {
	g := newGuardFixture(t)
	flake := g.newTicket("CI flake: landing TestX", ticket.StatusOpen)
	g.record(landing.ChecksRed, headA)

	require.NoError(t, g.check(headA, flake))

	g.record(landing.ChecksRed, headA)
	err := g.check(headA, flake)
	require.ErrorIs(t, err, landing.ErrRedRequeue)
	assert.Contains(t, err.Error(), "already bought")
}

func TestEnqueueGuard_AnInvalidCitationIsRefused(t *testing.T) {
	g := newGuardFixture(t)
	g.record(landing.ChecksRed, headA)
	notFlake := g.newTicket("Something else", ticket.StatusOpen)
	cancelled := g.newTicket("CI flake: cancelled one", ticket.StatusCancelled)

	for name, flake := range map[string]int64{"missing": 999999999, "wrong title": notFlake, "cancelled": cancelled} {
		t.Run(name, func(t *testing.T) {
			err := g.check(headA, flake)
			require.ErrorIs(t, err, landing.ErrRedRequeue)
			assert.True(t, strings.Contains(err.Error(), "flake"))
		})
	}
}

func TestEnqueueGuard_ADoneFlakeTicketIsAccepted(t *testing.T) {
	g := newGuardFixture(t)
	g.record(landing.ChecksRed, headA)
	flake := g.newTicket("CI flake: fixed since", ticket.StatusDone)
	require.NoError(t, g.check(headA, flake))
}
