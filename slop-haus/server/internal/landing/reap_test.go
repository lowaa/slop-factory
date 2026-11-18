package landing_test

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/servermerge/runner"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

type changesWrap struct {
	landing.Changes
	f *fixture
}

func (c *changesWrap) Checks(ctx context.Context, tenant, agent uuid.UUID, sha string) (*change.Report, error) {
	if c.f.onChecks != nil {
		c.f.onChecks(ctx)
	}
	return c.Changes.Checks(ctx, tenant, agent, sha)
}

type connectedTools struct{}

func (connectedTools) Connected(context.Context) (bool, error) { return true, nil }

// reaper is the servermerge reaper job's runner, as a replica runs it beside
// the landing runner.
func (f *fixture) reaper() *runner.Runner {
	f.t.Helper()
	r, err := runner.New(&runner.Options{
		Tickets: ticket.NewServerAgent(f.tickets), Changes: f.changes, Tools: connectedTools{}, Outcomes: f.outcomes,
		Reaper: runner.NewPostgresReaper(f.database), Logger: slog.DiscardHandler,
	})
	require.NoError(f.t, err)
	return r
}

func (f *fixture) expireServerLease() {
	f.t.Helper()
	_, err := f.database.Exec(f.ctx, `UPDATE leases SET expires_at = now() - interval '1 minute'
		WHERE released_at IS NULL AND agent_id = (SELECT id FROM agents WHERE name = $1)`, ticket.ServerAgentName)
	require.NoError(f.t, err)
}

// diedWaitingForChecks is a landing whose process died holding the integration
// lock while it waited for CI, its lease run out.
func (f *fixture) diedWaitingForChecks(c *finished) {
	f.t.Helper()
	f.setChecks(c.ref, headA, true, pend)
	var once sync.Once
	atChecks := make(chan struct{})
	f.onChecks = func(ctx context.Context) {
		once.Do(func() { close(atChecks); <-ctx.Done() })
	}
	f.diedHoldingLease = true
	r := f.runner(landing.Config{})
	r.Kick(f.tenant)
	<-atChecks
	require.NotNil(f.t, f.lockHolder())
	r.Close()
	f.diedHoldingLease = false
	f.onChecks = nil
	f.expireServerLease()
}

// The servermerge reaper must not bounce a landing: its row is the landing
// runner's, which reconciles from the code host. Without the reaper's
// exclusion this fails with a merge_refused "lease expired" hand-back.
func TestReap_ALandingWhoseLeaseLapsedIsNotHandedBack(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.diedWaitingForChecks(c)

	require.NoError(t, f.reaper().Reap(context.Background()))

	stale, err := runner.NewPostgresReaper(f.database).Stale(context.Background())
	require.NoError(t, err)
	assert.Empty(t, stale, "the landing's lease is not offered to the reaper")
	assert.Empty(t, f.outcomesNow(), "the landing's own runner records the outcome")
	row := f.row(c.ticketID)
	require.NotNil(t, row)
	assert.Equal(t, landing.Landing, row.State)
	assert.NotNil(t, f.lockHolder())
}

// A landing whose process died is picked up by the next sweep: its lease is
// reclaimed, the host is read, and it merges. Without the reclaim the server
// agent's own lock-pinned lease keeps the claim refused forever.
func TestLand_ALandingWhoseProcessDiedIsReclaimedAndMerged(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.diedWaitingForChecks(c)
	require.NoError(t, f.reaper().Reap(context.Background()))
	f.setChecks(c.ref, headA, true, green)

	f.land(f.runner(landing.Config{}))

	f.assertMerged(c)
	require.Len(t, f.host.Merges(), 1)
}

func TestLand_ALandingWhoseProcessDiedAfterTheMergeIsClosedNotMergedAgain(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.diedWaitingForChecks(c)
	require.NoError(t, f.reaper().Reap(context.Background()))
	f.markMergedOnHost(c)

	f.land(f.runner(landing.Config{}))

	assert.Equal(t, landing.AlreadyMerged, f.outcomeOf(c.ticketID).Outcome)
	assert.Empty(t, f.host.Merges())
	assert.Nil(t, f.row(c.ticketID))
	assert.Nil(t, f.lockHolder())
}

// A live landing's lease is not taken: only an expired one is reclaimed.
func TestLand_ALiveLandingsLeaseIsNotReclaimed(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, true, pend)
	var once sync.Once
	atChecks := make(chan struct{})
	release := make(chan struct{})
	f.onChecks = func(context.Context) { once.Do(func() { close(atChecks); <-release }) }
	live := f.runner(landing.Config{})
	live.Kick(f.tenant)
	<-atChecks

	server, err := f.store.EnsureServerAgent(f.ctx)
	require.NoError(t, err)
	expiry := func() time.Time {
		var at time.Time
		require.NoError(t, f.database.QueryRow(f.ctx,
			`SELECT expires_at FROM leases WHERE released_at IS NULL AND agent_id = $1`, server).Scan(&at))
		return at
	}
	before := expiry()
	_, err = f.store.ServerClaimLanding(f.ctx, server, c.ticketID, 1, ticket.DefaultServerLeaseTTL)
	assert.ErrorIs(t, err, ticket.ErrAlreadyHolding)
	assert.True(t, before.Equal(expiry()), "a live lease must not be renewed by a claim")
	close(release)
}

// The exclusion is for landings only: the same lapsed lease on a ticket with
// no landing row is a dead server-merge attempt, handed back as before.
func TestReap_ALapsedLeaseWithNoLandingRowIsStillHandedBack(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.diedWaitingForChecks(c)
	require.NoError(t, f.queue.Remove(f.ctx, c.ticketID, landing.PhaseMerged))

	stale, err := runner.NewPostgresReaper(f.database).Stale(context.Background())
	require.NoError(t, err)
	require.Len(t, stale, 1)
	require.NoError(t, f.reaper().Reap(context.Background()))

	assert.Equal(t, landing.MergeRefused, f.outcomeOf(c.ticketID).Outcome)
	assert.Nil(t, f.lockHolder())
}

// A graceful shutdown ends the server's claim, so the next sweep (here or on
// another replica) lands the ticket without waiting out the lease's TTL.
func TestClose_ReleasesTheServersClaimAndTheNextSweepLandsIt(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, true, pend)
	var once sync.Once
	atChecks := make(chan struct{})
	f.onChecks = func(ctx context.Context) {
		once.Do(func() { close(atChecks); <-ctx.Done() })
	}
	r := f.runner(landing.Config{})
	r.Kick(f.tenant)
	<-atChecks
	require.NotNil(t, f.lockHolder())

	r.Close()

	assert.Nil(t, f.lockHolder(), "the integration lock is freed")
	var held int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM leases WHERE released_at IS NULL AND agent_id = (SELECT id FROM agents WHERE name = $1)`,
		ticket.ServerAgentName).Scan(&held))
	assert.Zero(t, held, "no lease runs to its TTL")
	row := f.row(c.ticketID)
	require.NotNil(t, row)
	assert.Equal(t, landing.Landing, row.State, "the ticket stays queued, not handed back")

	f.onChecks = nil
	f.setChecks(c.ref, headA, true, green)
	f.land(f.runner(landing.Config{}))
	f.assertMerged(c)
}

// The server agent is one per tenant, so a replica closing must end only the
// claims it abandoned, not another replica's live landing.
func TestClose_LeavesAnotherReplicasLiveClaimAlone(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, true, pend)
	var once sync.Once
	atChecks := make(chan struct{})
	f.onChecks = func(ctx context.Context) {
		once.Do(func() { close(atChecks); <-ctx.Done() })
	}
	owner := f.runner(landing.Config{})
	owner.Kick(f.tenant)
	<-atChecks
	other := f.runner(landing.Config{})
	other.Sweep(f.ctx)
	other.Wait()

	other.Close()

	require.NotNil(t, f.lockHolder(), "the owner's integration lock is still held")
	owner.Close()
	assert.Nil(t, f.lockHolder())
}
