package wakeup_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/wakeup"
)

func (r *rig) archive(t *testing.T)   { dbtest.ArchiveTenant(t, r.db, r.tenant) }
func (r *rig) unarchive(t *testing.T) { dbtest.UnarchiveTenant(t, r.db, r.tenant) }

// A worker poll for an archived tenant is answered at once as disabled and
// not recorded as a poll (which supervisor detection reads); unarchived it
// is recorded and hears the work.
func TestWorkerPoll_AnArchivedTenantIsToldToStopAndLeavesNoTrace(t *testing.T) {
	r := newRig(t, nil)
	r.state.peek = claimable(38)
	w := r.workerWaiter(nil)
	polls := func() (n int) {
		require.NoError(t, r.db.QueryRow(r.ctx(context.Background()), `SELECT count(*) FROM worker_polls`).Scan(&n))
		return n
	}

	r.archive(t)
	got := r.poll(t, w, &wakeup.WorkerPollRequest{})
	assert.Equal(t, []wakeup.Reason{wakeup.ReasonDisabled}, reasons(got))
	assert.Zero(t, polls())
	assert.Zero(t, r.state.read(func(s *state) int { return s.peeks }))

	r.unarchive(t)
	got = r.poll(t, w, &wakeup.WorkerPollRequest{})
	assert.Equal(t, []wakeup.Reason{wakeup.ReasonClaimable}, reasons(got))
	assert.Equal(t, 1, polls())
}
