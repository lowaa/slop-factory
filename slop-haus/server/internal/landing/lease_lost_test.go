package landing_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// revokeServerLease ends the server agent's lease as a person taking the ticket
// away does: the heartbeat then finds no lease, and the lock the lease held is
// free for the next landing's lease, as the reaper leaves it.
func (f *fixture) revokeServerLease() {
	f.t.Helper()
	_, err := f.database.Exec(f.ctx, `DELETE FROM locks`)
	require.NoError(f.t, err)
	_, err = f.database.Exec(f.ctx, `UPDATE leases SET released_at = now()
		WHERE released_at IS NULL AND agent_id = (SELECT id FROM agents WHERE name = $1)`, ticket.ServerAgentName)
	require.NoError(f.t, err)
}

// loseLeaseAtChecks makes the first read of the change's checks revoke the
// lease and wait for the heartbeat to notice, which is the runner's own signal
// that the lease is gone. The checks are green after, for the landing the sweep
// then starts afresh.
func (f *fixture) loseLeaseAtChecks(c *finished) {
	var once sync.Once
	f.onChecks = func(ctx context.Context) {
		once.Do(func() {
			f.revokeServerLease()
			<-ctx.Done()
			f.setChecks(c.ref, headA, true, green)
		})
	}
}

func TestLand_ALeaseLostMidLandingHandsNothingBackAndTheSweepLandsItAfresh(t *testing.T) {
	f := newFixture(t)
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, true, pend)
	f.loseLeaseAtChecks(c)

	f.land(f.runner(landing.Config{HeartbeatInterval: time.Millisecond}))

	recs := f.outcomesNow()
	require.Len(t, recs, 1, "the run that lost the lease recorded nothing")
	assert.Equal(t, landing.Merged, recs[0].Outcome)
	assert.NotContains(t, f.comments(c.ticketID), "The server", "and told the author nothing")
	assert.Len(t, f.host.Merges(), 1, "the change merged once, on the run that held the lease")
	f.assertMerged(c)
}
