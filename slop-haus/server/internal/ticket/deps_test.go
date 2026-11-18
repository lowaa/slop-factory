package ticket_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// Cycle rejection is the reason AddDependency exists as a Service method
// rather than a straight insert. Nothing un-blocks a ticket by hand, so a
// ticket that depends transitively on itself can never become claimable
// and has no route back through the API -- the edge has to be refused at
// the point of creation, not cleaned up afterwards.
func TestAddDependency_RejectsCycles(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc, ctx := f.svc, f.ctx

	a, err := svc.Create(ctx, &ticket.CreateParams{Title: "A"})
	require.NoError(t, err)
	b, err := svc.Create(ctx, &ticket.CreateParams{Title: "B"})
	require.NoError(t, err)
	c, err := svc.Create(ctx, &ticket.CreateParams{Title: "C"})
	require.NoError(t, err)

	t.Run("self dependency", func(t *testing.T) {
		_, err := svc.AddDependency(ctx, ticket.UserAuthor(f.human), a.ID, a.ID)
		require.ErrorIs(t, err, ticket.ErrDependencyCycle)
	})

	t.Run("direct cycle", func(t *testing.T) {
		f.blockedBy(t, a.ID, b.ID)
		_, err := svc.AddDependency(ctx, ticket.UserAuthor(f.human), b.ID, a.ID)
		require.ErrorIs(t, err, ticket.ErrDependencyCycle)
	})

	t.Run("transitive cycle", func(t *testing.T) {
		// a -> b already exists; add b -> c, then c -> a closes the loop.
		f.blockedBy(t, b.ID, c.ID)
		_, err := svc.AddDependency(ctx, ticket.UserAuthor(f.human), c.ID, a.ID)
		require.ErrorIs(t, err, ticket.ErrDependencyCycle)
	})

	t.Run("long acyclic chain is accepted", func(t *testing.T) {
		d, err := svc.Create(ctx, &ticket.CreateParams{Title: "D"})
		require.NoError(t, err)
		// c -> d extends the existing a -> b -> c chain without closing it.
		f.blockedBy(t, c.ID, d.ID)

		got, err := svc.Get(ctx, c.ID)
		require.NoError(t, err)
		assert.Equal(t, []int64{d.ID}, got.DependsOn)
	})
}

func TestAddDependency_IsIdempotent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc, ctx := f.svc, f.ctx

	a, err := svc.Create(ctx, &ticket.CreateParams{Title: "A"})
	require.NoError(t, err)
	b, err := svc.Create(ctx, &ticket.CreateParams{Title: "B"})
	require.NoError(t, err)

	f.blockedBy(t, a.ID, b.ID)
	change, err := svc.AddDependency(ctx, ticket.UserAuthor(f.human), a.ID, b.ID)
	require.NoError(t, err, "re-adding the same edge should not error")
	assert.False(t, change.Changed, "re-adding an existing edge changes nothing")

	got, err := svc.Get(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, []int64{b.ID}, got.DependsOn)
}

func TestRemoveDependency(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc, ctx := f.svc, f.ctx

	a, err := svc.Create(ctx, &ticket.CreateParams{Title: "A"})
	require.NoError(t, err)
	b, err := svc.Create(ctx, &ticket.CreateParams{Title: "B"})
	require.NoError(t, err)

	f.blockedBy(t, a.ID, b.ID)
	_, err = svc.RemoveDependency(ctx, ticket.UserAuthor(f.human), a.ID, b.ID)
	require.NoError(t, err)

	got, err := svc.Get(ctx, a.ID)
	require.NoError(t, err)
	assert.Empty(t, got.DependsOn)
}

// A page listing a ticket's blockers names each one, so the titles come
// back with the ticket from both the single read and the list -- two
// separate queries in the store -- in the same order as the ids.
func TestBlockers_CarryTheirTitles(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc, ctx := f.svc, f.ctx

	work, err := svc.Create(ctx, &ticket.CreateParams{Title: "Build the UI"})
	require.NoError(t, err)
	api, err := svc.Create(ctx, &ticket.CreateParams{Title: "Build the API"})
	require.NoError(t, err)
	schema, err := svc.Create(ctx, &ticket.CreateParams{Title: "Design the schema"})
	require.NoError(t, err)
	f.blockedBy(t, work.ID, schema.ID)
	f.blockedBy(t, work.ID, api.ID)

	want := []model.TicketRef{
		{ID: api.ID, Title: "Build the API", Status: ticket.StatusOpen},
		{ID: schema.ID, Title: "Design the schema", Status: ticket.StatusOpen},
	}

	got, err := svc.Get(ctx, work.ID)
	require.NoError(t, err)
	assert.Equal(t, want, got.Blockers)
	assert.Equal(t, []int64{api.ID, schema.ID}, got.DependsOn)

	all, err := svc.List(ctx, nil)
	require.NoError(t, err)
	blockersByID := map[int64][]model.TicketRef{}
	for _, listed := range all {
		blockersByID[listed.ID] = listed.Blockers
	}
	require.Contains(t, blockersByID, work.ID)
	assert.Equal(t, want, blockersByID[work.ID])
	assert.Empty(t, blockersByID[api.ID])
}

func TestAddDependency_MissingTicket(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc, ctx := f.svc, f.ctx

	a, err := svc.Create(ctx, &ticket.CreateParams{Title: "A"})
	require.NoError(t, err)

	_, err = svc.AddDependency(ctx, ticket.UserAuthor(f.human), a.ID, 9999)
	require.ErrorIs(t, err, ticket.ErrNotFound)
}

// Cancelled counts as satisfied alongside done. If the claim query and the
// blocked predicate ever disagree on this, cancelling a ticket silently
// strands every dependent on work nobody will ever do.
func TestStatus_IsTerminal(t *testing.T) {
	t.Parallel()
	assert.True(t, ticket.StatusDone.IsTerminal())
	assert.True(t, ticket.StatusCancelled.IsTerminal())
	assert.False(t, ticket.StatusOpen.IsTerminal())
	assert.False(t, ticket.StatusInProgress.IsTerminal())
	assert.False(t, ticket.StatusBlocked.IsTerminal())
	assert.False(t, ticket.StatusPreparing.IsTerminal())
	// Parked is "not now", not "won't do": terminal, it would release
	// everything blocked on it.
	assert.False(t, ticket.StatusParked.IsTerminal())
}
