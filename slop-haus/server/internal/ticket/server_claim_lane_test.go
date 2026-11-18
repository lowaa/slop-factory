package ticket_test

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// laneFixture has a workspace whose repo declares app-a and app-b independent
// and enforced, with tools/ outside every app.
func laneFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	_, err := manifest.NewPostgresStore(f.database).Put(f.ctx, []manifest.Repo{
		{Path: "mono", Remote: "github.example/acme/repo", IndependentApps: []string{"app-a/", "app-b/"}, BatchWithinApps: true},
	}, f.human)
	require.NoError(t, err)
	return f
}

// queuedLanding puts a ticket with paths in the landing queue.
func (f *fixture) queuedLanding(t *testing.T, name string, paths ...string) int64 {
	t.Helper()
	created := f.ticket(t, name, paths...)
	f.land(t, f.agent(t, "author-"+name), created.ID)
	return created.ID
}

func (f *fixture) setLandingState(t *testing.T, id int64, state string) {
	t.Helper()
	_, err := f.database.Exec(f.ctx, `UPDATE landings SET queue_state = $2 WHERE ticket_id = $1`, id, state)
	require.NoError(t, err)
}

func TestServerClaimLanding_OneActiveEngineInAnApp(t *testing.T) {
	t.Parallel()
	for _, active := range []string{"serial_merging", "batch_testing"} {
		t.Run(active+" holds its app", func(t *testing.T) {
			f := laneFixture(t)
			running := f.queuedLanding(t, "running", "mono/app-a/server/")
			f.setLandingState(t, running, active)
			sameApp := f.queuedLanding(t, "same", "mono/app-a/web/")
			otherApp := f.queuedLanding(t, "other", "mono/app-b/server/")
			server := f.serverAgent(t)

			_, err := f.workspaceStore().ServerClaimLanding(f.ctx, server, sameApp, 1, ticket.DefaultServerLeaseTTL)
			assert.ErrorIs(t, err, ticket.ErrServerClaimLaneHeld, "the same app waits")

			got, err := f.workspaceStore().ServerClaimLanding(f.ctx, server, otherApp, 1, ticket.DefaultServerLeaseTTL)
			require.NoError(t, err)
			assert.NotNil(t, got.Ticket, "a different app proceeds")
		})
	}
}

func TestServerClaimLanding_ASharedPathEntryWaitsForEveryAppAndBlocksEveryApp(t *testing.T) {
	t.Parallel()
	t.Run("waits for any active entry", func(t *testing.T) {
		f := laneFixture(t)
		running := f.queuedLanding(t, "running", "mono/app-b/server/")
		f.setLandingState(t, running, "serial_waiting_checks")
		shared := f.queuedLanding(t, "shared", "mono/tools/")
		_, err := f.workspaceStore().ServerClaimLanding(f.ctx, f.serverAgent(t), shared, 1, ticket.DefaultServerLeaseTTL)
		assert.ErrorIs(t, err, ticket.ErrServerClaimLaneHeld)
	})
	t.Run("blocks every app while active", func(t *testing.T) {
		f := laneFixture(t)
		running := f.queuedLanding(t, "running", "mono/tools/")
		f.setLandingState(t, running, "serial_merging_main")
		for _, p := range []string{"mono/app-a/x/", "mono/app-b/x/"} {
			id := f.queuedLanding(t, p, p)
			_, err := f.workspaceStore().ServerClaimLanding(f.ctx, f.serverAgent(t), id, 1, ticket.DefaultServerLeaseTTL)
			assert.ErrorIs(t, err, ticket.ErrServerClaimLaneHeld, p)
		}
	})
	t.Run("an entry spanning two apps counts as shared", func(t *testing.T) {
		f := laneFixture(t)
		running := f.queuedLanding(t, "running", "mono/app-a/x/", "mono/app-b/x/")
		f.setLandingState(t, running, "batch_merging")
		id := f.queuedLanding(t, "b", "mono/app-b/y/")
		_, err := f.workspaceStore().ServerClaimLanding(f.ctx, f.serverAgent(t), id, 1, ticket.DefaultServerLeaseTTL)
		assert.ErrorIs(t, err, ticket.ErrServerClaimLaneHeld)
	})
}

// The rule is for the move out of queued: a batch's later member, claimed once
// the earlier one's lease ended, is not held by the batch it belongs to.
func TestServerClaimLanding_AnEntryThatLeftQueuedIsNotHeldByItsOwnApp(t *testing.T) {
	t.Parallel()
	f := laneFixture(t)
	first := f.queuedLanding(t, "first", "mono/app-a/server/")
	f.setLandingState(t, first, "batch_merging")
	member := f.queuedLanding(t, "member", "mono/app-a/web/")
	f.setLandingState(t, member, "batch_testing")
	got, err := f.workspaceStore().ServerClaimLanding(f.ctx, f.serverAgent(t), member, 1, ticket.DefaultServerLeaseTTL)
	require.NoError(t, err)
	assert.NotNil(t, got.Ticket)
}

func TestServerClaimLanding_TwoConcurrentClaimsInOneAppAdmitOne(t *testing.T) {
	t.Parallel()
	f := laneFixture(t)
	a := f.queuedLanding(t, "a", "mono/app-a/server/")
	b := f.queuedLanding(t, "b", "mono/app-a/web/")
	server := f.serverAgent(t)
	store := f.workspaceStore()
	var wg sync.WaitGroup
	results := make([]*ticket.ClaimResult, 2)
	errs := make([]error, 2)
	for i, id := range []int64{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = store.ServerClaimLanding(f.ctx, server, id, 1, ticket.DefaultServerLeaseTTL)
		}()
	}
	wg.Wait()
	won := 0
	for i := range results {
		if errs[i] == nil && results[i].Ticket != nil {
			won++
		}
	}
	assert.Equal(t, 1, won, "errors: %v", errs)
}
