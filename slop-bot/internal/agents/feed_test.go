package agents_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

// Tests of how the scheduler keeps agents in step with their tickets when
// the ticket feed or the backend fails it: the feed is at-least-once, and
// an event lost would leave an agent behind its ticket until the next.

func (f *fixture) feedCursor() string {
	f.t.Helper()
	c, _, err := f.state.Cursors.Get(context.Background(), agents.CursorKey)
	require.NoError(f.t, err)
	return c
}

// A sync that fails (a busy database, a remote backend away) holds the
// feed cursor back, so a restart replays the event, and is tried again on
// the clock: a cancel lost to it would leave the agent running.
func TestAFailedSyncHoldsTheCursorAndIsTriedAgain(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	_, a := f.working("t")
	f.racing.failGet(a.Ticket, errBackendAway)
	f.setStatus(a.Ticket, tickets.StatusCancelled)
	// Recorded once the failed pass has decided the cursor and the retry.
	f.awaitExcept(agents.EventSyncFailed, func(e *state.Event) bool { return e.Ticket == a.Ticket }, gone(a.ID))
	held := f.feedCursor()

	f.racing.healGet(a.Ticket)
	f.clock.Advance(agents.SyncRetryMax)
	f.await(agents.EventFinished, of(a.ID))
	// The scheduler has finished its pass once Stop returns.
	require.NoError(t, f.mgr.Stop(f.ctx()))
	assert.NotEqual(t, held, f.feedCursor(), "saved past the cancel only once its sync got through")
}

// A ticket the backend no longer has can't be synced, and never will be:
// it isn't retried, so it can't hold the feed cursor for good.
func TestAMissingTicketDoesntHoldTheCursor(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	// Waiting between turns, so the drain at the end has none to wait for.
	_, a := f.waitingAgent("t", nil)
	_, b := f.waitingAgent("other", nil)
	f.mgr.WaitStarts() // the spawns' own reads of the tickets are done
	f.racing.failGet(a.Ticket, tickets.ErrNotFound)
	_, err := f.tickets.Comment(context.Background(), ident.UserPrincipal(), a.Ticket, "an event for the missing ticket")
	require.NoError(t, err)
	held := f.feedCursor()
	// The feed is in order: b's cancel is synced after a's event.
	f.setStatus(b.Ticket, tickets.StatusCancelled)
	f.awaitExcept(agents.EventFinished, of(b.Agent), ofType("", agents.EventSyncFailed))
	require.NoError(t, f.mgr.Stop(f.ctx()))
	assert.NotEqual(t, held, f.feedCursor(), "saved past both events")
}

// A feed event lost to the backend can't leave an agent behind its ticket
// for ever: a periodic pass syncs every live agent with its ticket.
func TestAPeriodicPassCatchesALostTicketEvent(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	_, a := f.working("t")
	f.racing.lose(a.Ticket)
	f.setStatus(a.Ticket, tickets.StatusCancelled)
	f.clock.Advance(agents.ReconcileEvery)
	ended := f.await(agents.EventFinished, of(a.ID))
	assert.Equal(t, a.Ticket, ended.Ticket)
}
