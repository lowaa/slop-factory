package discussionlabel_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionlabel"
)

// pendingProbe reads the round's label while the classifier runs.
type pendingProbe struct {
	r      *rig
	id     int64
	result *discussionlabel.Classification
	seen   discussionlabel.Label
}

func (p *pendingProbe) Classify(ctx context.Context, _ *discussionlabel.Question) (*discussionlabel.Classification, error) {
	rec, err := p.r.store.Get(ctx, p.id, 1, 1)
	if err == nil {
		p.seen = rec.Label
	}
	return p.result, nil
}

func TestQueue_TheLabelIsPendingWhileClassifyingThenRoutineOrYours(t *testing.T) {
	for name, tc := range map[string]struct {
		result *discussionlabel.Classification
		want   discussionlabel.Label
	}{
		"routine": {routine("merge_continue"), discussionlabel.LabelRoutine},
		"yours":   {&discussionlabel.Classification{Label: discussionlabel.LabelYours, Reason: "a direction call"}, discussionlabel.LabelYours},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			id := r.ticketID(t)
			probe := &pendingProbe{r: r, id: id, result: tc.result}

			rec, err := r.service(probe, engine("", ""), steward.LevelShadow, 10).Queue(r.ctx, question(id, 1))

			require.NoError(t, err)
			assert.Equal(t, discussionlabel.LabelPending, probe.seen)
			assert.Equal(t, tc.want, rec.Label)
		})
	}
}

func TestQueue_RepeatsClassifyOnce(t *testing.T) {
	r := newRig(t)
	id := r.ticketID(t)
	c := &fakeClassifier{result: routine("infra_wait")}
	svc := r.service(c, engine("", ""), steward.LevelShadow, 10)

	first, err := svc.Queue(r.ctx, question(id, 1))
	require.NoError(t, err)
	second, err := svc.Queue(r.ctx, question(id, 1))
	require.NoError(t, err)

	assert.Equal(t, first.ID, second.ID)
	assert.EqualValues(t, 1, c.calls.Load())
}

func TestQueue_AtTheCapTheLabelIsNotAssessedAndNothingRuns(t *testing.T) {
	r := newRig(t)
	id := r.ticketID(t)
	c := &fakeClassifier{result: routine("merge_continue")}
	svc := r.service(c, engine("", ""), steward.LevelShadow, 1)

	first, err := svc.Queue(r.ctx, question(id, 1))
	require.NoError(t, err)
	capped, err := svc.Queue(r.ctx, question(id, 2))
	require.NoError(t, err)

	assert.Equal(t, discussionlabel.LabelRoutine, first.Label)
	assert.Equal(t, discussionlabel.LabelNotAssessed, capped.Label)
	assert.Equal(t, discussionlabel.CapReason, capped.Reason)
	assert.EqualValues(t, 1, c.calls.Load())
}

func TestQueue_WhenOffTheLabelIsNotAssessedNeverRoutine(t *testing.T) {
	r := newRig(t)
	id := r.ticketID(t)
	c := &fakeClassifier{result: routine("merge_continue")}
	svc := r.service(c, engine("", ""), steward.LevelOff, 10)

	rec, err := svc.Queue(r.ctx, question(id, 1))
	require.NoError(t, err)
	again, err := svc.Queue(r.ctx, question(id, 1))
	require.NoError(t, err)

	assert.Equal(t, discussionlabel.LabelNotAssessed, rec.Label)
	assert.Equal(t, discussionlabel.OffReason, rec.Reason)
	assert.Equal(t, rec.ID, again.ID, "a repeat finds the same row")
	assert.Zero(t, c.calls.Load())
}

func TestQueue_ASkipNeverReplacesAnEarlierLabel(t *testing.T) {
	r := newRig(t)
	id := r.ticketID(t)
	first, err := r.service(&fakeClassifier{result: routine("merge_continue")}, engine("", ""), steward.LevelShadow, 10).Queue(r.ctx, question(id, 1))
	require.NoError(t, err)

	again, err := r.service(nil, engine("", ""), steward.LevelOff, 10).Queue(r.ctx, question(id, 1))

	require.NoError(t, err)
	assert.Equal(t, first.ID, again.ID)
	assert.Equal(t, discussionlabel.LabelRoutine, again.Label)
}
