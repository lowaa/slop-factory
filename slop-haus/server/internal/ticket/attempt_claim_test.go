package ticket_test

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func (f *fixture) addAttempt(t *testing.T, ticketID int64, shadow bool) int {
	t.Helper()
	n, err := ticket.AddAttempt(f.ctx, f.database, ticketID, shadow)
	require.NoError(t, err)
	return n
}

// leasedAttempt is the attempt number the agent's live lease is on.
func (f *fixture) leasedAttempt(t *testing.T, agentID uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, f.database.QueryRow(f.ctx,
		`SELECT attempt_number FROM leases l WHERE l.agent_id = $1 AND `+ticket.ActiveLeaseClause, agentID).Scan(&n))
	return n
}

func (f *fixture) peek(t *testing.T, agentID uuid.UUID) *ticket.ClaimResult {
	t.Helper()
	result, err := f.svc.Peek(f.ctx, agentID, nil)
	require.NoError(t, err)
	return result
}

func (f *fixture) startedRevision(t *testing.T, ticketID int64, number int) *int {
	t.Helper()
	var rev *int
	require.NoError(t, f.database.QueryRow(f.ctx,
		`SELECT started_revision FROM attempts WHERE ticket_id = $1 AND number = $2`, ticketID, number).Scan(&rev))
	return rev
}

func TestClaim_TwoAgentsHoldTwoAttemptsOfOneTicket(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a1, a2, a3 := f.agent(t, "first"), f.agent(t, "second"), f.agent(t, "third")
	tk := f.ticket(t, "Two attempts", "a/")
	require.Equal(t, 2, f.addAttempt(t, tk.ID, false))

	require.NotNil(t, f.claim(t, a1))
	assert.Equal(t, 1, f.leasedAttempt(t, a1), "the lowest queued number goes first")
	// Attempt 1 is in progress and attempt 2 queued: still offered, to a probe and a claim alike.
	require.NotNil(t, f.peek(t, a2).Ticket, "the ticket is still offered while a sibling is queued")
	require.NotNil(t, f.claim(t, a2))
	assert.Equal(t, 2, f.leasedAttempt(t, a2))

	assert.Nil(t, f.peek(t, a3).Ticket, "every attempt is leased")
	assert.Nil(t, f.claim(t, a3), "so a third agent gets nothing")
	assert.Nil(t, f.peek(t, a3).Blocked)
}

// The claim lock serialises claims, so the second sees the first's lease and
// takes the next attempt; without it both read attempt 1 as free. The first
// claim is held inside its transaction until the second is seen waiting on the
// advisory lock in pg_locks: if the lock were gone the second would run past
// it, never be seen waiting, and fail this test.
func TestClaim_ConcurrentClaimsOfOneTicketTakeDifferentAttempts(t *testing.T) {
	f := newFixture(t)
	agents := []uuid.UUID{f.agent(t, "racer-1"), f.agent(t, "racer-2")}
	tk := f.ticket(t, "Raced", "a/")
	f.addAttempt(t, tk.ID, false)

	inside, proceed := make(chan struct{}), make(chan struct{})
	var once, unblock sync.Once
	release := func() { unblock.Do(func() { close(proceed) }) }
	defer release()
	defer ticket.SetClaimBarrier(func() {
		once.Do(func() { close(inside) })
		<-proceed
	})()

	var wg sync.WaitGroup
	claim := func(a uuid.UUID) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := f.svc.Claim(f.ctx, a, &tk.ID)
			assert.NoError(t, err)
			assert.NotNil(t, result.Ticket)
		}()
	}
	claim(agents[0])
	<-inside
	claim(agents[1])
	require.Eventually(t, func() bool {
		var waiting int
		require.NoError(t, f.database.QueryRow(f.ctx,
			`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`).Scan(&waiting))
		return waiting > 0
	}, 30*time.Second, 10*time.Millisecond, "the second claim waits on the claim lock")
	release()
	wg.Wait()

	assert.ElementsMatch(t, []int{1, 2}, []int{f.leasedAttempt(t, agents[0]), f.leasedAttempt(t, agents[1])})
}

func TestClaim_ARoutedAttemptIsOfferedOnlyToItsVariant(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	v1 := f.namedVariantVersion(t, "fast", 1)
	tk := f.ticket(t, "Per attempt routing", "a/")
	f.routeTicket(t, tk.ID, v1) // attempt 1
	f.addAttempt(t, tk.ID, false)
	plain := f.agent(t, "in-none")
	fast := f.agentOnVariant(t, "in-fast", v1)

	require.NotNil(t, f.claim(t, plain))
	assert.Equal(t, 2, f.leasedAttempt(t, plain), "the lowest attempt routed to it, not the lowest")
	require.NotNil(t, f.claim(t, fast))
	assert.Equal(t, 1, f.leasedAttempt(t, fast))
}

func TestClaim_AnAttemptsAffinityHoldsOnlyThatAttempt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	owner, other, third := f.agent(t, "owner"), f.agent(t, "other"), f.agent(t, "third")
	tk := f.ticket(t, "Affinity", "a/")
	two := f.addAttempt(t, tk.ID, false)
	_, err := f.database.Exec(f.ctx,
		`UPDATE attempts SET resume_agent_id = $3, affinity_until = now() + interval '1 hour' WHERE ticket_id = $1 AND number = $2`,
		tk.ID, two, owner)
	require.NoError(t, err)

	require.NotNil(t, f.claim(t, other))
	assert.Equal(t, 1, f.leasedAttempt(t, other), "attempt 1 is not held")
	assert.Nil(t, f.peek(t, third).Ticket, "attempt 2 is held for its owner")
	assert.Nil(t, f.claim(t, third))
	require.NotNil(t, f.claim(t, owner))
	assert.Equal(t, 2, f.leasedAttempt(t, owner))
}

func TestClaim_ACooldownKeepsAnInProgressTicketOutForEveryAttempt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a1, a2 := f.agent(t, "first"), f.agent(t, "second")
	tk := f.ticket(t, "Cooling", "a/")
	f.addAttempt(t, tk.ID, false)
	require.NotNil(t, f.claim(t, a1))
	_, err := f.database.Exec(f.ctx, `UPDATE tickets SET blocked_until = now() + interval '1 hour' WHERE id = $1`, tk.ID)
	require.NoError(t, err)
	require.Equal(t, ticket.StatusInProgress, f.get(t, tk.ID).Status, "its status does not show the cooldown")

	assert.Nil(t, f.peek(t, a2).Ticket)
	assert.Nil(t, f.claim(t, a2))

	_, err = f.database.Exec(f.ctx, `UPDATE tickets SET blocked_until = now() - interval '1 minute' WHERE id = $1`, tk.ID)
	require.NoError(t, err)
	require.NotNil(t, f.claim(t, a2), "offered again once it ends")
}

func TestClaim_SiblingAttemptsNeverConflictButOtherTicketsDo(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a1, a2, a3 := f.agent(t, "first"), f.agent(t, "second"), f.agent(t, "third")
	tk := f.ticket(t, "Siblings", "s/")
	other := f.ticket(t, "Neighbour", "s/inner.go")
	f.addAttempt(t, tk.ID, false)

	require.NotNil(t, f.claim(t, a1))
	second, err := f.svc.Claim(f.ctx, a2, &tk.ID)
	require.NoError(t, err)
	require.NotNil(t, second.Ticket, "attempt 2 is not blocked by attempt 1's paths")
	assert.Equal(t, 2, f.leasedAttempt(t, a2))

	blocked, err := f.svc.Claim(f.ctx, a3, &other.ID)
	require.NoError(t, err)
	assert.Nil(t, blocked.Ticket)
	require.NotNil(t, blocked.Blocked, "another ticket still is")
	assert.Equal(t, tk.ID, blocked.Blocked.ConflictingTicketID)
}

func TestClaim_AShadowsLeaseHoldsNoScope(t *testing.T) {
	t.Parallel()
	for _, shadow := range []bool{false, true} {
		name := "a plain attempt holds its scope"
		if shadow {
			name = "a shadow holds none"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			v1 := f.namedVariantVersion(t, "fast", 1)
			holder, rival, owner := f.agent(t, "holder"), f.agent(t, "rival"), f.agent(t, "owner")
			held := f.ticket(t, "Held by the attempt", "s/")
			f.routeTicket(t, held.ID, v1) // attempt 1 is not the holder's to take
			f.addAttempt(t, held.ID, shadow)
			overlapping := f.ticket(t, "Overlapping", "s/x.go")
			edited := f.ticket(t, "Edited", "e/")
			elsewhere := f.ticket(t, "Elsewhere", "o/")

			claimOnly := func(agent uuid.UUID, id int64) *ticket.Ticket {
				result, err := f.svc.Claim(f.ctx, agent, &id)
				require.NoError(t, err)
				return result.Ticket
			}
			require.NotNil(t, claimOnly(holder, held.ID))
			require.Equal(t, 2, f.leasedAttempt(t, holder))
			require.NotNil(t, claimOnly(owner, elsewhere.ID))

			// activeScopes: another ticket's claim over the holder's paths.
			assert.Equal(t, shadow, claimOnly(rival, overlapping.ID) != nil)

			// holdingScope: the holder's own paths edited over another's scope.
			_, err := f.svc.Update(f.ctx, held.ID, &ticket.UpdateParams{Paths: scopes("s/", "o/y.go")})
			if shadow {
				require.NoError(t, err, "a ticket held only by a shadow is not checked")
			} else {
				require.ErrorIs(t, err, ticket.ErrScopeConflict)
			}
			// activeScopes again: an edit over the holder's paths.
			_, err = f.svc.Update(f.ctx, edited.ID, &ticket.UpdateParams{Paths: scopes("e/")})
			require.NoError(t, err)
		})
	}
}

func TestClaim_StartedRevisionIsSetAtTheFirstClaimOnly(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	first, second := f.agent(t, "first"), f.agent(t, "second")
	tk := f.ticket(t, "Starting", "a/")
	_, err := f.database.Exec(f.ctx, `UPDATE tickets SET revision = 7 WHERE id = $1`, tk.ID)
	require.NoError(t, err)
	require.Nil(t, f.startedRevision(t, tk.ID, 1), "never started")

	require.NotNil(t, f.claim(t, first))
	require.NotNil(t, f.startedRevision(t, tk.ID, 1))
	assert.Equal(t, 7, *f.startedRevision(t, tk.ID, 1))

	_, err = f.database.Exec(f.ctx, `UPDATE tickets SET revision = 9 WHERE id = $1`, tk.ID)
	require.NoError(t, err)
	f.expire(t, first)
	require.NotNil(t, f.claim(t, second))
	assert.Equal(t, 7, *f.startedRevision(t, tk.ID, 1), "a re-claim leaves it")
}

func TestClaim_SaysWhetherTheGrantedAttemptIsUnpromoted(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a1, a2 := f.agent(t, "first"), f.agent(t, "second")
	tk := f.ticket(t, "Two attempts", "a/")
	require.Equal(t, 2, f.addAttempt(t, tk.ID, false))

	first, err := f.svc.Claim(f.ctx, a1, nil)
	require.NoError(t, err)
	second, err := f.svc.Claim(f.ctx, a2, nil)
	require.NoError(t, err)

	assert.False(t, first.Unpromoted, "attempt 1 is the promoted one")
	assert.True(t, second.Unpromoted)
}
