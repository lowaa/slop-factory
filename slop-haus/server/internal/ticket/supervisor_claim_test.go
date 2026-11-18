package ticket_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// A supervisor creates workers and holds no ticket itself: its slots take the
// queue's work. Handed one, a credential issued to it would be a worker the
// supervisor's cap does not count.
func TestClaim_ASupervisorIsNeverHandedWork(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	queued := f.ticket(t, "queued", "a/")
	supervisor := uuid.New()
	f.run(t, `INSERT INTO agents (id, name, kind, slot_cap) VALUES ($1, 'supervisor', 'supervisor', 2)`, supervisor)

	for name, only := range map[string]*int64{"from the queue": nil, "a named ticket": &queued.ID} {
		t.Run(name, func(t *testing.T) {
			result, err := f.svc.Claim(f.ctx, supervisor, only)
			require.NoError(t, err)
			assert.Nil(t, result.Ticket)

			held, err := f.svc.LeaseFor(f.ctx, supervisor)
			assert.ErrorIs(t, err, ticket.ErrNoLease, "no lease: %v", held)
		})
	}
	result, err := f.svc.Claim(f.ctx, f.agent(t, "worker"), nil)
	require.NoError(t, err)
	require.NotNil(t, result.Ticket, "the ticket was claimable all along")
}
