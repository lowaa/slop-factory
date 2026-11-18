package ticket_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// consoles are the consoles a test runs against, keyed by a subtest name.
func (f *fixture) consoles(t *testing.T) map[string]uuid.UUID {
	t.Helper()
	console, err := f.agents.CreateKind(f.ctx, "console-kind", agent.KindConsole)
	require.NoError(t, err)
	return map[string]uuid.UUID{"kind": console.ID}
}

func TestClaim_AConsoleTakesOnlyANamedTicket(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	queued := f.ticket(t, "queued", "a/")

	for name, console := range f.consoles(t) {
		t.Run(name, func(t *testing.T) {
			result, err := f.svc.Claim(f.ctx, console, nil)
			require.ErrorIs(t, err, ticket.ErrConsoleUnnamedClaim)
			assert.Nil(t, result)

			held, err := f.svc.LeaseFor(f.ctx, console)
			assert.ErrorIs(t, err, ticket.ErrNoLease, "refused, so no lease: %v", held)

			result, err = f.svc.Claim(f.ctx, console, &queued.ID)
			require.NoError(t, err)
			require.NotNil(t, result.Ticket)
			assert.Equal(t, queued.ID, result.Ticket.ID)
			require.NoError(t, f.svcRelease(console))
		})
	}
}

func (f *fixture) svcRelease(agentID uuid.UUID) error {
	_, err := f.svc.Release(f.ctx, agentID, "test over", nil)
	return err
}

func TestClaim_AWorkerStillTakesFromTheQueue(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	queued := f.ticket(t, "queued", "a/")

	result, err := f.svc.Claim(f.ctx, f.agent(t, "worker"), nil)
	require.NoError(t, err)
	require.NotNil(t, result.Ticket)
	assert.Equal(t, queued.ID, result.Ticket.ID)
}

// A console waits for nothing, so an idle one is not capacity the queue is
// short of.
func TestQueueBlockage_IgnoresIdleConsoles(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	holder := f.agent(t, "holder")
	held := f.ticket(t, "held", "server/")
	_, err := f.svc.Claim(f.ctx, holder, &held.ID)
	require.NoError(t, err)
	f.ticket(t, "waiting", "server/internal/")
	f.consoles(t)

	got, err := f.svc.QueueBlockage(f.ctx)
	require.NoError(t, err)
	assert.Nil(t, got)

	f.agent(t, "idle-worker")
	got, err = f.svc.QueueBlockage(f.ctx)
	require.NoError(t, err)
	assert.NotNil(t, got, "an idle worker in the same position is stuck, so the check can tell them apart")
}
