package ticket_test

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// parkedBenchmark is a parked ticket with an explicit base and a snapshot,
// edited after parking: what a replay must not be shown.
func (r *pinRig) parkedBenchmark(t *testing.T) *ticket.Ticket {
	t.Helper()
	r.knownRefs()
	tk, err := r.createWithRef(t, "v1.2.0", func(p *ticket.CreateParams) {
		p.Status = ticket.StatusPreparing
		p.Title, p.Body = "Benchmark title", "Benchmark body"
	})
	require.NoError(t, err)
	_, err = r.svc.Update(r.ctx, tk.ID, &ticket.UpdateParams{Status: ptr(ticket.StatusParked)})
	require.NoError(t, err)
	_, err = r.store().SnapshotParked(r.ctx, tk.ID)
	require.NoError(t, err)
	r.edit(t, tk.ID, "Hindsight title", "Hindsight body")
	return tk
}

func TestReplayParked_SeesTheViewAsParkedFromTheBaseAndLeavesTheTicketParked(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	tk := r.parkedBenchmark(t)

	added := r.addReplay(t, tk.ID)
	assert.True(t, added.Replay)
	assert.True(t, added.Shadow)
	assert.Equal(t, ticket.StatusParked, r.status(t, tk.ID), "a replay never changes the ticket's status")

	replayer := r.agent(t, "replayer")
	res := r.claimResult(t, replayer)

	assert.Equal(t, "Benchmark title", res.Ticket.Title)
	assert.Equal(t, "Benchmark body", res.Ticket.Body)
	assert.Equal(t, commitX, r.host.BranchAt[r.branchKey(ticket.BranchName(tk.ID, added.Number))], "at the old base")
	assert.Equal(t, commitX, r.host.BranchAt[r.branchKey(ticket.UnpromotedBase(tk.ID))])
	assert.Equal(t, ticket.StatusParked, r.status(t, tk.ID))
	assert.Equal(t, added.Number, r.leasedAttempt(t, replayer), "the parked ticket's own queued attempt is never handed out")
	assert.Equal(t, ticket.ReplayNote, ticket.GuidanceFor(res.Ticket, res.Branch).DoneThroughOther)
}

func TestReplayParked_RefusedWithoutAnExplicitBase(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	tk := r.parkedBenchmark(t)
	_, err := r.database.Exec(r.ctx, `UPDATE tickets SET base_commit = NULL WHERE id = $1`, tk.ID)
	require.NoError(t, err)

	_, err = r.svc.AddReplays(r.ctx, r.human, tk.ID, []*uuid.UUID{nil})

	require.ErrorIs(t, err, ticket.ErrNoBase)
	assert.Len(t, r.attemptRows(t, tk.ID), 1, "only the ticket's own queued attempt")
}

func TestReplayParked_RefusedWithoutASnapshot(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	tk := r.parkedBenchmark(t)
	_, err := r.database.Exec(r.ctx, `DELETE FROM ticket_snapshots WHERE ticket_id = $1`, tk.ID)
	require.NoError(t, err)

	_, err = r.svc.AddReplays(r.ctx, r.human, tk.ID, []*uuid.UUID{nil})

	require.ErrorIs(t, err, ticket.ErrNoSnapshot)
}

func TestReplayParked_ManyClosedReplaysThenANewOneClaims(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	tk := r.parkedBenchmark(t)
	for i := 0; i < ticket.MaxLiveAttempts+2; i++ {
		r.addReplay(t, tk.ID)
		agent := r.agent(t, fmt.Sprintf("replayer-%d", i))
		require.NotNil(t, r.claimResult(t, agent))
		_, err := r.database.Exec(r.ctx, `UPDATE attempts SET status = 'closed' WHERE ticket_id = $1 AND status <> 'closed'`, tk.ID)
		require.NoError(t, err)
	}

	added := r.addReplay(t, tk.ID)
	res := r.claimResult(t, r.agent(t, "latest"))

	assert.Equal(t, "Benchmark title", res.Ticket.Title)
	assert.Equal(t, commitX, r.host.BranchAt[r.branchKey(ticket.BranchName(tk.ID, added.Number))])
	assert.Equal(t, ticket.StatusParked, r.status(t, tk.ID))
}

func TestReplayParked_UnparkingStillWorksAfterReplays(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	tk := r.parkedBenchmark(t)
	r.addReplay(t, tk.ID)

	got, err := r.svc.Update(r.ctx, tk.ID, &ticket.UpdateParams{Status: ptr(ticket.StatusPreparing)})

	require.NoError(t, err)
	assert.Equal(t, ticket.StatusPreparing, got.Status)
}
