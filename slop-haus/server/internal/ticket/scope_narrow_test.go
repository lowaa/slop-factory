package ticket_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/wakeup"
)

func (f *fixture) paths(t *testing.T, id int64) []string {
	t.Helper()
	got, err := f.svc.Get(f.ctx, id)
	require.NoError(t, err)
	out := make([]string, 0, len(got.Paths))
	for _, p := range got.Paths {
		out = append(out, p.Path)
	}
	return out
}

func (f *fixture) claimOnly(t *testing.T, agentID uuid.UUID, id int64) *ticket.ClaimResult {
	t.Helper()
	result, err := f.svc.Claim(f.ctx, agentID, &id)
	require.NoError(t, err)
	return result
}

func TestNarrowScope_FreesPathsForAConflictingOpenTicket(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	mine := f.ticket(t, "Wide", "frontend/src/", "docs/")
	wants := f.ticket(t, "Wants the frontend", "frontend/src/app.tsx")
	f.claimOnly(t, a, mine.ID)

	blocked := f.claimOnly(t, b, wants.ID)
	require.Nil(t, blocked.Ticket)
	require.NotNil(t, blocked.Blocked)
	require.NotNil(t, mustBlockage(t, f))

	before := f.boardRevision(t)
	result, err := f.svc.NarrowScope(f.ctx, a, []string{"frontend/src/"}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"docs/"}, result.Remaining)
	assert.Greater(t, f.boardRevision(t), before, "the board stream must see the change")
	assert.Nil(t, mustBlockage(t, f), "the blocked-queue banner clears")

	granted := f.claimOnly(t, b, wants.ID)
	require.NotNil(t, granted.Ticket, "the freed path is claimable")
	assert.Equal(t, wants.ID, granted.Ticket.ID)
}

func mustBlockage(t *testing.T, f *fixture) *ticket.QueueBlockage {
	t.Helper()
	// An idle agent is what makes a blockage reportable.
	got, err := f.svc.QueueBlockage(f.ctx)
	require.NoError(t, err)
	return got
}

func TestNarrowScope_ReplacesADirectoryWithNarrowerPaths(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	mine := f.ticket(t, "Settings", "frontend/src/")
	elsewhere := f.ticket(t, "Elsewhere in src", "frontend/src/api/client.ts")
	settings := f.ticket(t, "More settings", "frontend/src/components/settings/panel.tsx")
	f.claimOnly(t, a, mine.ID)

	result, err := f.svc.NarrowScope(f.ctx, a, []string{"frontend/src/"}, []string{"frontend/src/components/settings/"})
	require.NoError(t, err)
	assert.Equal(t, []string{"frontend/src/components/settings/"}, result.Remaining)
	assert.Equal(t, []string{"frontend/src/components/settings/"}, f.paths(t, mine.ID))

	assert.NotNil(t, f.claimOnly(t, b, elsewhere.ID).Ticket, "outside the kept path is free")
	_, err = f.svc.Release(f.ctx, b, "done with it", nil)
	require.NoError(t, err)
	assert.Nil(t, f.claimOnly(t, b, settings.ID).Ticket, "inside the kept path is still held")

	comments, err := f.svc.ListComments(f.ctx, mine.ID)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	require.NotNil(t, comments[0].AuthorAgentID)
	assert.Equal(t, a, *comments[0].AuthorAgentID)
	assert.Contains(t, comments[0].Body, `"frontend/src/"`)
	assert.Contains(t, comments[0].Body, `"frontend/src/components/settings/"`)
}

func TestNarrowScope_OnlyTheTicketTheCallerHolds(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	mine := f.ticket(t, "Mine", "a/", "b/")
	f.claimOnly(t, a, mine.ID)

	_, err := f.svc.NarrowScope(f.ctx, b, []string{"b/"}, nil)
	require.ErrorIs(t, err, ticket.ErrNoLease, "an agent holding nothing")

	theirs := f.ticket(t, "Theirs", "c/", "d/")
	f.claimOnly(t, b, theirs.ID)
	_, err = f.svc.NarrowScope(f.ctx, b, []string{"b/"}, nil)
	require.ErrorIs(t, err, ticket.ErrNarrowUnknownPath, "another ticket's path is not the caller's to give up")
	assert.ElementsMatch(t, []string{"a/", "b/"}, f.paths(t, mine.ID))
	assert.ElementsMatch(t, []string{"c/", "d/"}, f.paths(t, theirs.ID))

	f.expire(t, a)
	_, err = f.svc.NarrowScope(f.ctx, a, []string{"b/"}, nil)
	require.ErrorIs(t, err, ticket.ErrNoLease, "a lapsed lease holds nothing")
}

func TestNarrowScope_NeverAddsAPath(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	mine := f.ticket(t, "Mine", "app/", "docs/")
	f.claimOnly(t, a, mine.ID)

	cases := map[string]struct {
		remove, keep []string
		want         error
	}{
		"keep outside the removed":        {[]string{"app/"}, []string{"other/"}, ticket.ErrNarrowAdds},
		"keep is the removed itself":      {[]string{"app/"}, []string{"app/"}, ticket.ErrNarrowAdds},
		"keep a sibling by string prefix": {[]string{"app/"}, []string{"app2/x/"}, ticket.ErrNarrowAdds},
		"keep inside a path not removed":  {[]string{"app/"}, []string{"docs/guide/"}, ticket.ErrNarrowAdds},
		"keep inside a removed file":      {[]string{"docs/"}, []string{"docs"}, ticket.ErrNarrowAdds},
		"remove a path never held":        {[]string{"nope/"}, nil, ticket.ErrNarrowUnknownPath},
		"remove part of a held dir":       {[]string{"app/main.go"}, nil, ticket.ErrNarrowUnknownPath},
		"nothing to remove":               {nil, nil, scope.ErrInvalidPath},
		"keeps overlapping each other":    {[]string{"app/"}, []string{"app/a/", "app/a/b.go"}, scope.ErrInvalidPath},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := f.svc.NarrowScope(f.ctx, a, tc.remove, tc.keep)
			require.ErrorIs(t, err, tc.want)
			assert.ElementsMatch(t, []string{"app/", "docs/"}, f.paths(t, mine.ID), "a refusal changes nothing")
			comments, err := f.svc.ListComments(f.ctx, mine.ID)
			require.NoError(t, err)
			assert.Empty(t, comments, "and says nothing")
		})
	}
}

func TestNarrowScope_NeverEmptiesTheScope(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	mine := f.ticket(t, "Mine", "a/", "b/")
	f.claimOnly(t, a, mine.ID)

	_, err := f.svc.NarrowScope(f.ctx, a, []string{"a/", "b/"}, nil)
	require.ErrorIs(t, err, ticket.ErrNarrowEmpty)
	assert.ElementsMatch(t, []string{"a/", "b/"}, f.paths(t, mine.ID))

	_, err = f.svc.NarrowScope(f.ctx, a, []string{"a/", "b/"}, []string{"b/x/"})
	require.NoError(t, err, "replacing a path with one inside it leaves a scope")
	assert.Equal(t, []string{"b/x/"}, f.paths(t, mine.ID))
}

func TestNarrowScope_PersistsAcrossARelease(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	mine := f.ticket(t, "Mine", "app/", "docs/")
	f.claimOnly(t, a, mine.ID)

	_, err := f.svc.NarrowScope(f.ctx, a, []string{"app/"}, []string{"app/one/"})
	require.NoError(t, err)
	_, err = f.svc.Release(f.ctx, a, "handing back", nil)
	require.NoError(t, err)

	got, err := f.svc.Get(f.ctx, mine.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []model.Path{
		{Path: "docs/", Source: model.PathSourceFiled},
		{Path: "app/one/", Source: model.PathSourceExtended},
	}, got.Paths)

	other := f.ticket(t, "Rest of app", "app/two/")
	f.claimOnly(t, b, other.ID)
	assert.NotNil(t, f.claimOnly(t, a, mine.ID).Ticket, "the narrowed ticket no longer collides with app/two/")
}

func TestNarrowScope_ATicketInReviewHoldsOnlyWhatItKept(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	gated := f.gated(t, "Gated", "frontend/src/", "docs/")
	f.claimOnly(t, a, gated.ID)

	_, err := f.svc.NarrowScope(f.ctx, a, []string{"frontend/src/"}, []string{"frontend/src/settings/"})
	require.NoError(t, err)
	f.review(t, a)

	free := f.ticket(t, "Frontend elsewhere", "frontend/src/api/")
	held := f.ticket(t, "Docs", "docs/guide.md")
	kept := f.ticket(t, "Settings", "frontend/src/settings/page.tsx")
	assert.NotNil(t, f.claimOnly(t, b, free.ID).Ticket, "freed while in review")
	_, err = f.svc.Release(f.ctx, b, "next", nil)
	require.NoError(t, err)
	assert.Nil(t, f.claimOnly(t, b, held.ID).Ticket, "a kept filed path stays held")
	assert.Nil(t, f.claimOnly(t, b, kept.ID).Ticket, "a kept narrower path stays held")
}

func TestNarrowScope_TenantsAreIsolated(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	other := db.WithTenant(context.Background(), dbtest.NewTenant(t, f.database))
	otherSvc := ticket.NewService(newStore(f.database), slog.DiscardHandler)

	a := f.agent(t, "builder-1")
	mine := f.ticket(t, "Mine", "app/", "docs/")
	f.claimOnly(t, a, mine.ID)

	_, err := otherSvc.NarrowScope(other, a, []string{"docs/"}, nil)
	require.ErrorIs(t, err, ticket.ErrNoLease, "my agent holds nothing in another tenant")

	theirs, err := otherSvc.Create(other, &ticket.CreateParams{
		Title: "Theirs", Paths: []model.Path{{Path: "app/"}, {Path: "docs/"}},
	})
	require.NoError(t, err)
	assert.NotNil(t, theirs, "the same paths are no conflict across tenants")
	assert.ElementsMatch(t, []string{"app/", "docs/"}, f.paths(t, mine.ID))
}

// A narrowing racing a claim: the claim's conflict scan must see the paths
// wholly before or wholly after the change. The narrowing is held inside its
// transaction, after writing and before commit; the claim is started then,
// and the test waits until the database shows it queued on the claim lock
// rather than scanning.
func TestNarrowScope_RacingAClaimNeverGrantsOverlappingScopes(t *testing.T) {
	f := newFixture(t)
	a, b, c := f.agent(t, "narrower"), f.agent(t, "claimer-1"), f.agent(t, "claimer-2")
	holder := f.ticket(t, "Core", "core/")
	freed := f.ticket(t, "Wants other", "core/other/x.go")
	kept := f.ticket(t, "Wants kept", "core/kept/y.go")
	f.claimOnly(t, a, holder.ID)

	inside, proceed := make(chan struct{}), make(chan struct{})
	var release sync.Once
	releaseNarrow := func() { release.Do(func() { close(proceed) }) }
	// A failed assertion must not leave the narrowing parked in its
	// transaction, which would hold the test database open.
	defer releaseNarrow()
	restoreNarrow := ticket.SetNarrowBarrier(func() {
		close(inside)
		<-proceed
	})
	defer restoreNarrow()

	scanned := make(chan struct{}, 4)
	restoreClaim := ticket.SetClaimBarrier(func() { scanned <- struct{}{} })
	defer restoreClaim()

	var wg sync.WaitGroup
	narrowed := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := f.svc.NarrowScope(f.ctx, a, []string{"core/"}, []string{"core/kept/"})
		narrowed <- err
	}()
	<-inside

	type claimed struct {
		result *ticket.ClaimResult
		err    error
	}
	claims := make(chan claimed, 2)
	for agentID, id := range map[uuid.UUID]int64{b: freed.ID, c: kept.ID} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := f.svc.Claim(f.ctx, agentID, &id)
			claims <- claimed{result, err}
		}()
	}

	// Both claims are parked on the advisory lock the narrowing holds.
	require.Eventually(t, func() bool {
		var waiting int
		require.NoError(t, f.database.QueryRow(f.ctx,
			`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`).Scan(&waiting))
		return waiting >= 2
	}, 10*time.Second, 5*time.Millisecond)
	select {
	case <-scanned:
		t.Fatal("a claim scanned scopes while a narrowing was mid-transaction")
	default:
	}

	releaseNarrow()
	require.NoError(t, <-narrowed)
	wg.Wait()
	close(claims)

	var granted []int64
	for c := range claims {
		require.NoError(t, c.err)
		if c.result.Ticket != nil {
			granted = append(granted, c.result.Ticket.ID)
		}
	}
	// Only the claim on the freed path may be granted; the kept path is
	// still the narrowing ticket's.
	assert.Equal(t, []int64{freed.ID}, granted)

	var overlapping int
	require.NoError(t, f.database.QueryRow(f.ctx, `
		SELECT count(*) FROM leases l1
		  JOIN ticket_paths p1 ON p1.ticket_id = l1.ticket_id
		  JOIN leases l2 ON l2.id <> l1.id AND l2.released_at IS NULL
		  JOIN ticket_paths p2 ON p2.ticket_id = l2.ticket_id
		 WHERE l1.released_at IS NULL AND (p1.path LIKE p2.path || '%')`).Scan(&overlapping))
	assert.Zero(t, overlapping)
}

type alwaysOn struct{}

func (alwaysOn) TicketProcessingEnabled(context.Context) (bool, error) { return true, nil }

func TestNarrowScope_TellsAnIdleAgentsPollOfTheFreedPath(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	holder, idle := f.agent(t, "holder"), f.agent(t, "idle")
	mine := f.ticket(t, "Wide", "frontend/src/", "docs/")
	wanted := f.ticket(t, "Needs the frontend", "frontend/src/app.tsx")
	f.claimOnly(t, holder, mine.ID)

	w := wakeup.New(wakeup.Deps{Tickets: f.svc, Policy: alwaysOn{}}, nil, slog.DiscardHandler)
	defer w.Close()

	// A zero wait is one look: the path is still held, so nothing is claimable.
	before, err := w.WorkerPoll(f.ctx, idle, &wakeup.WorkerPollRequest{})
	require.NoError(t, err)
	assert.Empty(t, before.Events)

	_, err = f.svc.NarrowScope(f.ctx, holder, []string{"frontend/src/"}, nil)
	require.NoError(t, err)

	after, err := w.WorkerPoll(f.ctx, idle, &wakeup.WorkerPollRequest{})
	require.NoError(t, err)
	require.Len(t, after.Events, 1)
	assert.Equal(t, wakeup.ReasonClaimable, after.Events[0].Reason)
	assert.Equal(t, wanted.ID, after.Events[0].TicketID)
}

func TestNarrowScope_RemainingListsEachPathOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	mine := f.ticket(t, "Settings", "src/")
	f.claimOnly(t, a, mine.ID)

	// The service dedupes its inputs, so go to the store, which is where the
	// duplicate was produced.
	store := newStore(f.database)
	result, err := store.NarrowScope(f.ctx, a, []string{"src/"}, []string{"src/keep/", "src/keep/"})
	require.NoError(t, err)
	assert.Equal(t, []string{"src/keep/"}, result.Remaining)
	assert.Equal(t, []string{"src/keep/"}, result.Kept)
}
