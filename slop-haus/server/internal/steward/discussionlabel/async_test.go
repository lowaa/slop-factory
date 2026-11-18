package discussionlabel_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionlabel"
)

type oneTenant struct{ id uuid.UUID }

func (o oneTenant) Tenants(context.Context) ([]uuid.UUID, error) { return []uuid.UUID{o.id}, nil }

// asyncService has no classifier: the steward agent completes rounds through Submit.
func (r *rig) asyncService(e *steward.Engine, level steward.Level, dailyCap int) *discussionlabel.Service {
	return r.service(nil, e, level, dailyCap)
}

func yoursClaim() *discussionlabel.Classification {
	return &discussionlabel.Classification{Label: discussionlabel.LabelYours, Reason: "a direction call"}
}

func TestAsync_QueueClaimsPendingThenARoutineClaimWithPassingChecksIsRoutine(t *testing.T) {
	r := newRig(t)
	id := r.ticketID(t)
	svc := r.asyncService(engine("", ""), steward.LevelShadow, 10)

	queued, err := svc.Queue(r.ctx, question(id, 1))
	require.NoError(t, err)
	assert.Equal(t, discussionlabel.LabelPending, queued.Label)

	done, err := svc.Submit(r.ctx, question(id, 1), routine("merge_continue"))
	require.NoError(t, err)
	assert.Equal(t, queued.ID, done.ID)
	assert.Equal(t, discussionlabel.LabelRoutine, done.Label)
	assert.Equal(t, "merge_continue", done.Category)
}

func TestAsync_ARoutineClaimWhoseCheckFailsIsYoursWithTheFailedCheck(t *testing.T) {
	r := newRig(t)
	id := r.ticketID(t)
	svc := r.asyncService(engine(steward.CheckCIGreenOnHead, ""), steward.LevelShadow, 10)
	_, err := svc.Queue(r.ctx, question(id, 1))
	require.NoError(t, err)

	done, err := svc.Submit(r.ctx, question(id, 1), routine("merge_continue"))

	require.NoError(t, err)
	assert.Equal(t, discussionlabel.LabelYours, done.Label)
	assert.Equal(t, "checks failed: ci_green_on_head: detail of ci_green_on_head", done.Reason)
}

func TestAsync_AnUnreadableCheckIsYoursToo(t *testing.T) {
	r := newRig(t)
	id := r.ticketID(t)
	svc := r.asyncService(engine("", steward.CheckHeadUnchanged), steward.LevelShadow, 10)
	_, err := svc.Queue(r.ctx, question(id, 1))
	require.NoError(t, err)

	done, err := svc.Submit(r.ctx, question(id, 1), routine("gate_confirmed"))

	require.NoError(t, err)
	assert.Equal(t, discussionlabel.LabelYours, done.Label)
}

func TestAsync_ARepeatSubmitKeepsTheFirstLabel(t *testing.T) {
	r := newRig(t)
	id := r.ticketID(t)
	svc := r.asyncService(engine("", ""), steward.LevelShadow, 10)
	_, err := svc.Queue(r.ctx, question(id, 1))
	require.NoError(t, err)
	first, err := svc.Submit(r.ctx, question(id, 1), yoursClaim())
	require.NoError(t, err)

	again, err := svc.Submit(r.ctx, question(id, 1), routine("merge_continue"))

	require.NoError(t, err)
	assert.Equal(t, first.ID, again.ID)
	assert.Equal(t, discussionlabel.LabelYours, again.Label, "a second claim does not replace the label")
	var n int
	require.NoError(t, r.database.QueryRow(r.ctx, `SELECT count(*) FROM steward_discussion_labels`).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestAsync_ARoundThatMovedIsRefusedAndStaysPending(t *testing.T) {
	r := newRig(t)
	id := r.ticketID(t)
	svc := r.asyncService(engine("", ""), steward.LevelShadow, 10)
	_, err := svc.Queue(r.ctx, question(id, 1))
	require.NoError(t, err)

	for name, mutate := range map[string]func(*discussionlabel.Question){
		"revision": func(q *discussionlabel.Question) { q.Revision++ },
		"head":     func(q *discussionlabel.Question) { q.HeadSHA = "def456" },
	} {
		q := question(id, 1)
		mutate(q)
		_, err := svc.Submit(r.ctx, q, routine("merge_continue"))
		assert.ErrorIs(t, err, discussionlabel.ErrStale, name)
	}
	rec, err := r.store.Get(r.ctx, id, 1, 1)
	require.NoError(t, err)
	assert.Equal(t, discussionlabel.LabelPending, rec.Label)
}

func TestAsync_SubmitWithoutAQueuedRoundIsNotFound(t *testing.T) {
	r := newRig(t)
	id := r.ticketID(t)

	_, err := r.asyncService(engine("", ""), steward.LevelShadow, 10).Submit(r.ctx, question(id, 1), yoursClaim())

	assert.ErrorIs(t, err, discussionlabel.ErrNotFound)
}

func TestAsync_AtTheCapTheRoundIsNotAssessed(t *testing.T) {
	r := newRig(t)
	id := r.ticketID(t)
	svc := r.asyncService(engine("", ""), steward.LevelShadow, 1)

	first, err := svc.Queue(r.ctx, question(id, 1))
	require.NoError(t, err)
	capped, err := svc.Queue(r.ctx, question(id, 2))
	require.NoError(t, err)

	assert.Equal(t, discussionlabel.LabelPending, first.Label)
	assert.Equal(t, discussionlabel.LabelNotAssessed, capped.Label)
	assert.Equal(t, discussionlabel.CapReason, capped.Reason)
	_, err = svc.Submit(r.ctx, question(id, 2), routine("merge_continue"))
	require.NoError(t, err)
	got, err := r.store.Get(r.ctx, id, 1, 2)
	require.NoError(t, err)
	assert.Equal(t, discussionlabel.LabelNotAssessed, got.Label, "a capped round is not labelled by a late claim")
}

func TestAsync_WhenOffTheRoundIsNotAssessedWithTheReason(t *testing.T) {
	r := newRig(t)
	id := r.ticketID(t)

	rec, err := r.asyncService(engine("", ""), steward.LevelOff, 10).Queue(r.ctx, question(id, 1))

	require.NoError(t, err)
	assert.Equal(t, discussionlabel.LabelNotAssessed, rec.Label)
	assert.Equal(t, discussionlabel.OffReason, rec.Reason)
}

func TestAsync_APendingRoundTimesOutAndAssessNowQueuesItAgain(t *testing.T) {
	r := newRig(t)
	id := r.ticketID(t)
	tenant, err := db.TenantFrom(r.ctx)
	require.NoError(t, err)
	svc := r.asyncService(engine("", ""), steward.LevelShadow, 10).WithRoster(oneTenant{tenant})
	_, err = svc.Queue(r.ctx, question(id, 1))
	require.NoError(t, err)

	r.now = r.now.Add(discussionlabel.LabelTimeout - time.Minute)
	require.NoError(t, svc.ExpireOnce(r.ctx))
	still, err := r.store.Get(r.ctx, id, 1, 1)
	require.NoError(t, err)
	assert.Equal(t, discussionlabel.LabelPending, still.Label, "within the bound")

	r.now = r.now.Add(2 * time.Minute)
	require.NoError(t, svc.ExpireOnce(r.ctx))
	expired, err := r.store.Get(r.ctx, id, 1, 1)
	require.NoError(t, err)
	assert.Equal(t, discussionlabel.LabelNotAssessed, expired.Label)
	assert.Equal(t, "steward did not label it in time", expired.Reason)

	requeued, err := svc.AssessNow(r.ctx, question(id, 1))
	require.NoError(t, err)
	assert.Equal(t, discussionlabel.LabelPending, requeued.Label)
	done, err := svc.Submit(r.ctx, question(id, 1), yoursClaim())
	require.NoError(t, err)
	assert.Equal(t, discussionlabel.LabelYours, done.Label)
}

func TestAsync_TheObserverIsToldOnceWhenASubmitCompletesTheLabel(t *testing.T) {
	r := newRig(t)
	id := r.ticketID(t)
	obs := &recordingObserver{}
	svc := r.asyncService(engine("", ""), steward.LevelShadow, 10).WithObserver(obs)
	_, err := svc.Queue(r.ctx, question(id, 1))
	require.NoError(t, err)

	stale := question(id, 1)
	stale.Revision++
	_, err = svc.Submit(r.ctx, stale, routine("merge_continue"))
	require.ErrorIs(t, err, discussionlabel.ErrStale)
	assert.Empty(t, obs.got, "a refused claim tells no one")

	_, err = svc.Submit(r.ctx, question(id, 1), routine("merge_continue"))
	require.NoError(t, err)
	_, err = svc.Submit(r.ctx, question(id, 1), routine("merge_continue"))
	require.NoError(t, err)

	require.Len(t, obs.got, 1, "a repeat tells no one again")
	assert.Equal(t, discussionlabel.LabelRoutine, obs.got[0].Label)
}

func TestAsync_ARunClaimedForOlderFactsCannotCompleteTheRequeuedLabel(t *testing.T) {
	r := newRig(t)
	id := r.ticketID(t)
	tenant, err := db.TenantFrom(r.ctx)
	require.NoError(t, err)
	svc := r.asyncService(engine("", ""), steward.LevelShadow, 10).WithRoster(oneTenant{tenant})
	old := question(id, 1)
	queued, err := svc.Queue(r.ctx, old)
	require.NoError(t, err)
	r.now = r.now.Add(discussionlabel.LabelTimeout + time.Minute)
	require.NoError(t, svc.ExpireOnce(r.ctx))
	newer := question(id, 1)
	newer.Revision++
	_, err = svc.AssessNow(r.ctx, newer)
	require.NoError(t, err)

	_, err = r.store.CompleteAgainst(r.ctx, queued.ID, &old.Target, &discussionlabel.Outcome{Label: discussionlabel.LabelYours, Reason: "old"})

	assert.ErrorIs(t, err, discussionlabel.ErrNotPending)
	got, err := r.store.Get(r.ctx, id, 1, 1)
	require.NoError(t, err)
	assert.Equal(t, discussionlabel.LabelPending, got.Label)
	assert.Equal(t, newer.Revision, *got.TicketRevision)
}

func TestAsync_TheTimeoutSweepReachesEveryTenantAndLeavesLabelledRoundsAlone(t *testing.T) {
	r := newRig(t)
	other := newRigIn(t, r.database)
	mine, theirs := r.ticketID(t), other.ticketID(t)
	tenantOf := func(rg *rig) uuid.UUID {
		id, err := db.TenantFrom(rg.ctx)
		require.NoError(t, err)
		return id
	}
	for _, c := range []struct {
		rg *rig
		id int64
	}{{r, mine}, {other, theirs}} {
		svc := c.rg.asyncService(engine("", ""), steward.LevelShadow, 10)
		_, err := svc.Queue(c.rg.ctx, question(c.id, 1))
		require.NoError(t, err)
		_, err = svc.Queue(c.rg.ctx, question(c.id, 2))
		require.NoError(t, err)
		_, err = svc.Submit(c.rg.ctx, question(c.id, 2), yoursClaim())
		require.NoError(t, err)
		c.rg.now = c.rg.now.Add(discussionlabel.LabelTimeout + time.Minute)
	}
	sweeper := r.asyncService(engine("", ""), steward.LevelShadow, 10).WithRoster(rosterOf{tenantOf(r), tenantOf(other)})

	require.NoError(t, sweeper.ExpireOnce(context.Background()))

	for _, c := range []struct {
		rg *rig
		id int64
	}{{r, mine}, {other, theirs}} {
		pending, err := c.rg.store.Get(c.rg.ctx, c.id, 1, 1)
		require.NoError(t, err)
		assert.Equal(t, discussionlabel.LabelNotAssessed, pending.Label)
		done, err := c.rg.store.Get(c.rg.ctx, c.id, 1, 2)
		require.NoError(t, err)
		assert.Equal(t, discussionlabel.LabelYours, done.Label, "a labelled round is never expired")
	}
}

type rosterOf []uuid.UUID

func (r rosterOf) Tenants(context.Context) ([]uuid.UUID, error) { return r, nil }
