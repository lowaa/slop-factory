package ticket_test

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/approvaltest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

type fixture struct {
	svc      *ticket.Service
	agents   *agent.Service
	database *db.DB
	// promoteHandlers are the handlers promoteMux made.
	promoteHandlers []*ticket.Handler
	ctx             context.Context
	// approvals hears every approval svc commits.
	approvals *approvaltest.Recorder
	// human is a real global_users row: ticket_comments.author_user_id and
	// tickets.created_by_user_id both carry a genuine foreign key to that
	// table, so a comment or ticket attributed to "the human" needs a real
	// row, not just a made-up id that happens to satisfy RLS.
	human uuid.UUID
	// tenant is ctx's, for tests that reach the store through the HTTP
	// routes, whose auth middleware sets the tenant itself.
	tenant uuid.UUID
	// baseOpts are the options svc was built with; see with.
	baseOpts []ticket.ServiceOption
}

// with replaces svc with one built from the fixture's options and opts, for a
// test that needs a resolver whose answers depend on agents it made.
func (f *fixture) with(opts ...ticket.ServiceOption) {
	f.svc = ticket.NewService(newStore(f.database), slog.DiscardHandler, append(slices.Clone(f.baseOpts), opts...)...)
	f.agents = agent.NewService(agent.NewPostgresStore(f.database), f.svc, nil, slog.DiscardHandler)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureOn(t, dbtest.WithDB(t))
}

func newFixtureOn(t *testing.T, database *db.DB) *fixture {
	t.Helper()
	// A real global_tenants row: agent.Service.Create writes a credential
	// referencing it by foreign key.
	tenantID := dbtest.NewTenant(t, database)
	ctx := db.WithTenant(context.Background(), tenantID)
	approvals := &approvaltest.Recorder{}
	baseOpts := []ticket.ServiceOption{
		ticket.WithApprovalHook(approvals),
		ticket.WithCodeHosts(codehosttest.Registry(codehosttest.NewLinkHost(prHost)))}
	svc := ticket.NewService(newStore(database), slog.DiscardHandler, baseOpts...)
	return &fixture{
		svc: svc, approvals: approvals, baseOpts: baseOpts,
		// The ticket service is the LeaseReleaser: disabling an agent hands
		// back whatever it held, and that release is the ticket package's
		// to perform.
		agents:   agent.NewService(agent.NewPostgresStore(database), svc, nil, slog.DiscardHandler),
		database: database,
		ctx:      ctx,
		human:    dbtest.NewUser(t, database),
		tenant:   tenantID,
	}
}

func (f *fixture) agent(t *testing.T, name string) uuid.UUID {
	t.Helper()
	created, err := f.agents.Create(f.ctx, name)
	require.NoError(t, err)
	return created.ID
}

func (f *fixture) ticket(t *testing.T, title string, paths ...string) *ticket.Ticket {
	t.Helper()
	scopes := make([]model.Path, 0, len(paths))
	for _, p := range paths {
		scopes = append(scopes, model.Path{Path: p})
	}
	created, err := f.svc.Create(f.ctx, &ticket.CreateParams{Title: title, Paths: scopes})
	require.NoError(t, err)
	return created
}

// expire forces a lease past its deadline without waiting out the TTL.
func (f *fixture) expire(t *testing.T, agentID uuid.UUID) {
	t.Helper()
	_, err := f.database.Exec(f.ctx,
		`UPDATE leases SET expires_at = now() - interval '1 minute'
		  WHERE agent_id = $1 AND released_at IS NULL`, agentID)
	require.NoError(t, err)
}

func TestClaim_GrantsAndMarksInProgress(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	created := f.ticket(t, "Add leases", "server/internal/ticket/")

	result, err := f.svc.Claim(f.ctx, a, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Ticket)
	assert.Nil(t, result.Blocked)
	assert.Equal(t, created.ID, result.Ticket.ID)
	assert.Equal(t, ticket.StatusInProgress, result.Ticket.Status)
}

func TestClaim_EmptyQueueIsNotAnError(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")

	result, err := f.svc.Claim(f.ctx, a, nil)
	require.NoError(t, err)
	assert.Nil(t, result.Ticket)
	assert.Nil(t, result.Blocked, "an empty queue is not a deadlock")
}

func TestClaim_RefusesAnAgentThatAlreadyHolds(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	f.ticket(t, "First", "a/")
	f.ticket(t, "Second", "b/")

	_, err := f.svc.Claim(f.ctx, a, nil)
	require.NoError(t, err)

	_, err = f.svc.Claim(f.ctx, a, nil)
	require.ErrorIs(t, err, ticket.ErrAlreadyHolding,
		"one lease per agent is what makes heartbeat unambiguous")
}

func TestClaim_SkipsConflictingScopesAndTakesAFreeOne(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a1, a2 := f.agent(t, "builder-1"), f.agent(t, "builder-2")

	held := f.ticket(t, "Holds core", "server/internal/ticket/")
	f.ticket(t, "Also core", "server/internal/ticket/lease.go")
	free := f.ticket(t, "Frontend", "frontend/src/")

	first, err := f.svc.Claim(f.ctx, a1, nil)
	require.NoError(t, err)
	require.Equal(t, held.ID, first.Ticket.ID)

	second, err := f.svc.Claim(f.ctx, a2, nil)
	require.NoError(t, err)
	require.NotNil(t, second.Ticket)
	assert.Equal(t, free.ID, second.Ticket.ID,
		"the conflicting candidate should be skipped, not block the whole queue")
}

func TestClaim_ReportsWhoBlockedItWhenEverythingConflicts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a1, a2 := f.agent(t, "builder-1"), f.agent(t, "builder-2")

	held := f.ticket(t, "Holds core", "server/internal/ticket/")
	contended := f.ticket(t, "Also core", "server/internal/ticket/lease.go")

	_, err := f.svc.Claim(f.ctx, a1, nil)
	require.NoError(t, err)

	result, err := f.svc.Claim(f.ctx, a2, nil)
	require.NoError(t, err)
	require.Nil(t, result.Ticket)
	require.NotNil(t, result.Blocked, "a deadlocked queue must say why, not look empty")
	assert.Equal(t, contended.ID, result.Blocked.TicketID)
	assert.Equal(t, held.ID, result.Blocked.ConflictingTicketID)
	assert.Equal(t, "server/internal/ticket/", result.Blocked.ConflictingPath)
	assert.Equal(t, a1, result.Blocked.HolderAgentID)
}

func TestClaim_UnscopedTicketConflictsWithNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a1, a2 := f.agent(t, "builder-1"), f.agent(t, "builder-2")

	f.ticket(t, "Scoped", "server/")
	unscoped := f.ticket(t, "No declared scope")

	_, err := f.svc.Claim(f.ctx, a1, nil)
	require.NoError(t, err)

	result, err := f.svc.Claim(f.ctx, a2, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Ticket)
	assert.Equal(t, unscoped.ID, result.Ticket.ID)
}

func TestClaim_OrdersByPriorityThenAge(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	first, err := f.svc.Create(f.ctx, &ticket.CreateParams{Title: "normal, oldest"})
	require.NoError(t, err)
	_, err = f.svc.Create(f.ctx, &ticket.CreateParams{Title: "normal, newer"})
	require.NoError(t, err)
	urgent, err := f.svc.Create(f.ctx, &ticket.CreateParams{
		Title: "urgent, newest", Priority: model.PriorityUrgent})
	require.NoError(t, err)

	got := []int64{}
	for _, name := range []string{"a", "b", "c"} {
		result, err := f.svc.Claim(f.ctx, f.agent(t, name), nil)
		require.NoError(t, err)
		require.NotNil(t, result.Ticket)
		got = append(got, result.Ticket.ID)
	}
	assert.Equal(t, []int64{urgent.ID, first.ID}, got[:2],
		"urgent first, then oldest-normal before newest-normal")
}

func TestClaim_SkipsTicketsWithUnsatisfiedDependencies(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")

	blocker := f.ticket(t, "Must go first")
	dependent := f.ticket(t, "Waits on the other")
	f.blockedBy(t, dependent.ID, blocker.ID)

	result, err := f.svc.Claim(f.ctx, a, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Ticket)
	assert.Equal(t, blocker.ID, result.Ticket.ID, "the dependent must not be handed out first")
}

// Cancelled counts as satisfied alongside done. If the claim query accepted
// only 'done' while the blocked predicate accepted both, cancelling a
// ticket would silently strand every dependent forever.
func TestClaim_CancelledDependencyUnblocksItsDependents(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")

	blocker := f.ticket(t, "Will be cancelled")
	dependent := f.ticket(t, "Depends on it")
	f.blockedBy(t, dependent.ID, blocker.ID)

	cancelled := ticket.StatusCancelled
	_, err := f.svc.Update(f.ctx, blocker.ID, &ticket.UpdateParams{Status: &cancelled})
	require.NoError(t, err)

	result, err := f.svc.Claim(f.ctx, a, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Ticket)
	assert.Equal(t, dependent.ID, result.Ticket.ID)
}

func TestClaim_ExpiredLeaseReturnsItsTicketToTheQueue(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a1, a2 := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	created := f.ticket(t, "Abandoned", "server/")

	_, err := f.svc.Claim(f.ctx, a1, nil)
	require.NoError(t, err)
	f.expire(t, a1)

	result, err := f.svc.Claim(f.ctx, a2, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Ticket, "a dead agent's scope must not block a live one")
	assert.Equal(t, created.ID, result.Ticket.ID)
}

// The test the advisory lock exists for: two claims for *different*
// tickets whose scopes overlap, forced to run inside their transactions at
// the same time.
//
// Two things had to be right for this test to mean anything, and neither
// was obvious:
//
// It must force a genuine overlap. A claim completes in about a
// millisecond, so goroutines started together still run one after another,
// each seeing the previous one's committed lease. Hence claimBarrier.
//
// The two claimants must go for *different* tickets. Both agents otherwise
// start at the head of the same queue and race for the same ticket's one
// attempt, where the leases_one_active_per_attempt index rejects the loser --
// so the test passes with the lock removed, proving only that the index works. Directed
// claims (the ticket_id argument) put them on different tickets whose paths
// collide, which is the case no constraint can express and the only thing
// the advisory lock is for.
//
// Verified: with the lock removed this fails, granting two leases over
// conflicting scopes.
func TestClaim_ConcurrentClaimsNeverGrantOverlappingScopes(t *testing.T) {
	f := newFixture(t)
	a1, a2 := f.agent(t, "racer-1"), f.agent(t, "racer-2")

	// Two separate tickets under one directory: at most one may ever be
	// held, and no unique index can say so.
	first := f.ticket(t, "Core, part one", "server/internal/core/one.go")
	second := f.ticket(t, "Core, part two", "server/internal/core/")

	// Release both claims only once both are inside their transactions past
	// the conflict scan. The timeout is what lets this terminate when the
	// advisory lock does its job and the second claim never arrives.
	var (
		mu      sync.Mutex
		waiting int
		arrived = make(chan struct{}, 2)
	)
	restore := ticket.SetClaimBarrier(func() {
		mu.Lock()
		waiting++
		mu.Unlock()
		arrived <- struct{}{}

		deadline := time.After(2 * time.Second)
		for {
			mu.Lock()
			both := waiting >= 2
			mu.Unlock()
			if both {
				return
			}
			select {
			case <-deadline:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	})
	defer restore()

	var (
		wg        sync.WaitGroup
		resultsMu sync.Mutex
		granted   []int64
	)
	claims := []struct {
		id   uuid.UUID
		want *int64
	}{
		{id: a1, want: &first.ID},
		{id: a2, want: &second.ID},
	}
	for _, a := range claims {
		wg.Add(1)
		go func(agentID uuid.UUID, want *int64) {
			defer wg.Done()
			result, err := f.svc.Claim(f.ctx, agentID, want)
			if err != nil {
				return
			}
			if result.Ticket != nil {
				resultsMu.Lock()
				granted = append(granted, result.Ticket.ID)
				resultsMu.Unlock()
			}
		}(a.id, a.want)
	}
	wg.Wait()

	require.Len(t, granted, 1,
		"tickets #%d and #%d overlap on server/internal/core/, so only one may be granted",
		first.ID, second.ID)

	// The invariant stated directly, not inferred from the count: no two
	// active leases may hold conflicting paths.
	rows, err := f.database.Query(f.ctx, `
		SELECT p.path FROM leases l
		  JOIN ticket_paths p ON p.ticket_id = l.ticket_id
		 WHERE l.released_at IS NULL`)
	require.NoError(t, err)
	defer rows.Close()

	activePaths := []string{}
	for rows.Next() {
		var path string
		require.NoError(t, rows.Scan(&path))
		activePaths = append(activePaths, path)
	}
	require.NoError(t, rows.Err())

	for i := range activePaths {
		for j := i + 1; j < len(activePaths); j++ {
			assert.False(t, scope.Conflicts(activePaths[i], activePaths[j]),
				"active leases hold conflicting scopes %q and %q",
				activePaths[i], activePaths[j])
		}
	}
}

// Distinct scopes must all be grantable concurrently -- the lock serializes
// claims, it must not serialize the work.
func TestClaim_ConcurrentClaimsOnDisjointScopesAllSucceed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	scopes := []string{"server/", "frontend/", "docs/", "e2e/"}
	for _, scope := range scopes {
		f.ticket(t, "Work in "+scope, scope)
	}
	agents := make([]uuid.UUID, len(scopes))
	for i := range agents {
		agents[i] = f.agent(t, "racer-"+string(rune('a'+i)))
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		granted []int64
	)
	start := make(chan struct{})
	for _, a := range agents {
		wg.Add(1)
		go func(agentID uuid.UUID) {
			defer wg.Done()
			<-start
			result, err := f.svc.Claim(f.ctx, agentID, nil)
			require.NoError(t, err)
			if result.Ticket != nil {
				mu.Lock()
				granted = append(granted, result.Ticket.ID)
				mu.Unlock()
			}
		}(a)
	}
	close(start)
	wg.Wait()

	assert.Len(t, granted, len(scopes), "disjoint scopes must not block each other")
}

// disable runs the first half of disabling an agent, as agent.Service does,
// and returns when it happened; the release is the caller's to run late.
func (f *fixture) disable(t *testing.T, agentID uuid.UUID) time.Time {
	t.Helper()
	disabled, err := agent.NewPostgresStore(f.database).SetDisabled(f.ctx, agentID, true)
	require.NoError(t, err)
	require.NotNil(t, disabled.DisabledAt)
	return *disabled.DisabledAt
}

func (f *fixture) enable(t *testing.T, agentID uuid.UUID) {
	t.Helper()
	_, err := agent.NewPostgresStore(f.database).SetDisabled(f.ctx, agentID, false)
	require.NoError(t, err)
}

// Disabling is mark-disabled then release, in two packages, so a release can
// arrive after the agent was re-enabled. These run the steps by hand in that
// order.
func TestReleaseLeasesFor_SparesALeaseTakenAfterAReEnable(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "flapper")
	other := f.agent(t, "other")
	tk := f.ticket(t, "Contested", "contested/")

	disabledAt := f.disable(t, a)
	f.enable(t, a)
	_, err := f.svc.Claim(f.ctx, a, &tk.ID)
	require.NoError(t, err)

	require.NoError(t, f.svc.ReleaseLeasesFor(f.ctx, a, "agent disabled", disabledAt))

	assert.Equal(t, ticket.Status("in_progress"), f.status(t, tk.ID))
	res, err := f.svc.Claim(f.ctx, other, &tk.ID)
	require.NoError(t, err)
	assert.Nil(t, res.Ticket, "the ticket is still held, so another agent cannot take it")
	require.NoError(t, f.svc.Heartbeat(f.ctx, a))
}

// The mirror image: a lease held when the agent was disabled is released even
// if the agent was re-enabled before the release ran.
func TestReleaseLeasesFor_ReleasesALeaseHeldAtDisableAfterAReEnable(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "flapper")
	tk := f.ticket(t, "Held", "held/")
	_, err := f.svc.Claim(f.ctx, a, &tk.ID)
	require.NoError(t, err)

	disabledAt := f.disable(t, a)
	f.enable(t, a)

	require.NoError(t, f.svc.ReleaseLeasesFor(f.ctx, a, "agent disabled", disabledAt))

	assert.Equal(t, ticket.Status("open"), f.status(t, tk.ID))
}

func TestReleaseLeasesFor_DisabledAgainReleasesTheLeaseTakenBetween(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "flapper")
	tk := f.ticket(t, "Held", "held/")

	f.disable(t, a)
	f.enable(t, a)
	_, err := f.svc.Claim(f.ctx, a, &tk.ID)
	require.NoError(t, err)
	secondDisable := f.disable(t, a)

	require.NoError(t, f.svc.ReleaseLeasesFor(f.ctx, a, "agent disabled", secondDisable))

	assert.Equal(t, ticket.Status("open"), f.status(t, tk.ID))
}

// A claim that arrives while a disable is still uncommitted must wait for it
// and then be refused. Otherwise it reads "enabled", is granted a lease
// stamped after the disable's disabled_at, and the timestamp-bounded release
// leaves a disabled agent holding work.
func TestClaim_WaitsForAnInFlightDisableAndIsRefused(t *testing.T) {
	f := newFixture(t)
	a := f.agent(t, "racer")
	tk := f.ticket(t, "Work", "work/")

	type claimed struct {
		res *ticket.ClaimResult
		err error
	}
	done := make(chan claimed, 1)
	err := f.database.InTx(f.ctx, func(q db.Querier) error {
		if _, err := q.Exec(f.ctx, `UPDATE agents SET disabled_at = now() WHERE id = $1`, a); err != nil {
			return err
		}
		go func() {
			res, err := f.svc.Claim(f.ctx, a, &tk.ID)
			done <- claimed{res, err}
		}()
		// Commit only once the claim is parked on the disable's row lock,
		// so the interleaving is the one under test and not a lucky one.
		var blocked bool
		require.Eventually(t, func() bool {
			if err := q.QueryRow(f.ctx, `
				SELECT EXISTS (SELECT 1 FROM pg_locks
				                WHERE NOT granted AND locktype IN ('transactionid', 'tuple'))`).Scan(&blocked); err != nil {
				return false
			}
			return blocked
		}, 10*time.Second, 10*time.Millisecond)
		return nil
	})
	require.NoError(t, err)

	got := <-done
	require.NoError(t, got.err)
	assert.Nil(t, got.res.Ticket, "a claim that raced a disable must not be granted work")
	assert.Equal(t, ticket.Status("open"), f.status(t, tk.ID))
}

// now() is the transaction's start, so a lease stamped with it by a claim that
// began before a disable and re-enable would look older than the disable and
// be released with the leases the agent held at the time. The claim parks on
// the disable's row lock, which is what lets the disable and re-enable commit
// after the claim's transaction began.
func TestReleaseLeasesFor_SparesALeaseFromAClaimThatBeganBeforeTheDisable(t *testing.T) {
	f := newFixture(t)
	a := f.agent(t, "flapper")
	tk := f.ticket(t, "Contested", "contested/")

	type claimed struct {
		res *ticket.ClaimResult
		err error
	}
	done := make(chan claimed, 1)
	var disabledAt time.Time
	err := f.database.InTx(f.ctx, func(q db.Querier) error {
		if _, err := q.Exec(f.ctx, `UPDATE agents SET disabled_at = now() WHERE id = $1`, a); err != nil {
			return err
		}
		go func() {
			res, err := f.svc.Claim(f.ctx, a, &tk.ID)
			done <- claimed{res, err}
		}()
		var blocked bool
		require.Eventually(t, func() bool {
			if err := q.QueryRow(f.ctx, `
				SELECT EXISTS (SELECT 1 FROM pg_locks
				                WHERE NOT granted AND locktype IN ('transactionid', 'tuple'))`).Scan(&blocked); err != nil {
				return false
			}
			return blocked
		}, 10*time.Second, 10*time.Millisecond)
		// Stamped after the claim's transaction began, as a disable that
		// lands while the claim waits is.
		if err := q.QueryRow(f.ctx,
			`UPDATE agents SET disabled_at = clock_timestamp() WHERE id = $1 RETURNING disabled_at`, a).Scan(&disabledAt); err != nil {
			return err
		}
		_, err := q.Exec(f.ctx, `UPDATE agents SET disabled_at = NULL WHERE id = $1`, a)
		return err
	})
	require.NoError(t, err)
	got := <-done
	require.NoError(t, got.err)
	require.NotNil(t, got.res.Ticket, "the agent was enabled again when the claim ran")

	require.NoError(t, f.svc.ReleaseLeasesFor(f.ctx, a, "agent disabled", disabledAt))

	assert.Equal(t, ticket.Status("in_progress"), f.status(t, tk.ID))
}
