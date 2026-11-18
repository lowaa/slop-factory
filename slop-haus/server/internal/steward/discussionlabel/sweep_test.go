package discussionlabel_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionlabel"
)

// queueingRounds queues each round it is given as the real hook does, once
// failures has been spent.
type queueingRounds struct {
	svc      *discussionlabel.Service
	failures int
	calls    int
}

func (q *queueingRounds) QueueRound(ctx context.Context, r discussionlabel.Round) error {
	q.calls++
	if q.failures > 0 {
		q.failures--
		return errors.New("the hook call failed")
	}
	_, err := q.svc.Queue(ctx, question(r.TicketID, r.Round))
	return err
}

// inDiscussion puts the ticket's attempt into an open round, as a route
// that never reached the hook would leave it.
func (r *rig) inDiscussion(t *testing.T, ticketID int64, round int) {
	t.Helper()
	_, err := r.database.Exec(r.ctx, `UPDATE attempts SET status = 'discussion', discussion_round = $2 WHERE ticket_id = $1 AND number = 1`, ticketID, round)
	require.NoError(t, err)
}

func (r *rig) sweeper(t *testing.T, level steward.Level, dailyCap int, failures int) (*discussionlabel.Service, *queueingRounds) {
	t.Helper()
	tenant, err := db.TenantFrom(r.ctx)
	require.NoError(t, err)
	svc := r.asyncService(engine("", ""), level, dailyCap).WithRoster(oneTenant{tenant})
	rounds := &queueingRounds{svc: svc, failures: failures}
	return svc.WithRounds(rounds), rounds
}

func TestSweep_AFailedHookCallIsRepairedAndARepeatMakesNothingNew(t *testing.T) {
	r := newRig(t)
	id := r.ticketID(t)
	r.inDiscussion(t, id, 1)
	svc, rounds := r.sweeper(t, steward.LevelShadow, 10, 1)

	require.Error(t, svc.SweepUnlabelled(r.ctx), "the failed call is reported")
	_, err := r.store.Get(r.ctx, id, 1, 1)
	require.ErrorIs(t, err, discussionlabel.ErrNotFound)

	require.NoError(t, svc.SweepUnlabelled(r.ctx))
	rec, err := r.store.Get(r.ctx, id, 1, 1)
	require.NoError(t, err)
	assert.Equal(t, discussionlabel.LabelPending, rec.Label)

	calls := rounds.calls
	require.NoError(t, svc.SweepUnlabelled(r.ctx))
	assert.Equal(t, calls, rounds.calls, "a labelled round is not queued again")
}

func TestSweep_AnOldRoundZeroIsLabelledAsRoundOne(t *testing.T) {
	r := newRig(t)
	id := r.ticketID(t)
	r.inDiscussion(t, id, 0)
	svc, _ := r.sweeper(t, steward.LevelShadow, 10, 0)

	require.NoError(t, svc.SweepUnlabelled(r.ctx))

	_, err := r.store.Get(r.ctx, id, 1, 1)
	require.NoError(t, err)
}

func TestSweep_TheDailyCapStillApplies(t *testing.T) {
	r := newRig(t)
	first, second := r.ticketID(t), r.ticketID(t)
	r.inDiscussion(t, first, 1)
	r.inDiscussion(t, second, 1)
	svc, _ := r.sweeper(t, steward.LevelShadow, 1, 0)

	require.NoError(t, svc.SweepUnlabelled(r.ctx))

	a, err := r.store.Get(r.ctx, first, 1, 1)
	require.NoError(t, err)
	b, err := r.store.Get(r.ctx, second, 1, 1)
	require.NoError(t, err)
	assert.Equal(t, discussionlabel.LabelPending, a.Label)
	assert.Equal(t, discussionlabel.LabelNotAssessed, b.Label)
	assert.Equal(t, discussionlabel.CapReason, b.Reason)
}

func TestSweep_WhileTheStewardIsOffNothingIsQueued(t *testing.T) {
	r := newRig(t)
	id := r.ticketID(t)
	r.inDiscussion(t, id, 1)
	svc, rounds := r.sweeper(t, steward.LevelOff, 10, 0)

	require.NoError(t, svc.SweepUnlabelled(r.ctx))

	assert.Zero(t, rounds.calls)
}

func TestSweep_ARoundNotInDiscussionIsLeftAlone(t *testing.T) {
	r := newRig(t)
	r.ticketID(t)
	svc, rounds := r.sweeper(t, steward.LevelShadow, 10, 0)

	require.NoError(t, svc.SweepUnlabelled(r.ctx))

	assert.Zero(t, rounds.calls)
}
