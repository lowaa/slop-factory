package discussionlabel_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionlabel"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func TestMain(m *testing.M) { dbtest.Main(m, schema.Migrations) }

var epoch = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

type fakeClassifier struct {
	result *discussionlabel.Classification
	err    error
	calls  atomic.Int32
}

func (f *fakeClassifier) Classify(context.Context, *discussionlabel.Question) (*discussionlabel.Classification, error) {
	f.calls.Add(1)
	return f.result, f.err
}

type fakeCheck struct {
	name string
	ok   bool
	err  error
}

func (c fakeCheck) Name() string { return c.name }
func (c fakeCheck) Run(context.Context, *steward.Subject) (steward.Evidence, error) {
	return steward.Evidence{Check: c.name, OK: c.ok, Detail: "detail of " + c.name}, c.err
}

// engine registers every check the discussion-answer categories name, passing
// except for the ones in failing (false) or erroring.
func engine(failing, erroring string) *steward.Engine {
	var checks []steward.Check
	for _, name := range []string{steward.CheckCIGreenOnHead, steward.CheckHeadUnchanged, steward.CheckDiffWithinScope,
		steward.CheckTicketRevUnchanged, steward.CheckNoHumanOnlyMarker} {
		c := fakeCheck{name: name, ok: name != failing}
		if name == erroring {
			c.err = errors.New("unreadable")
		}
		checks = append(checks, c)
	}
	checks = append(checks, steward.AnswerFromTemplate())
	return steward.NewEngine(slog.DiscardHandler, checks...)
}

type settings struct{ cfg steward.Config }

func (s settings) StewardConfig(context.Context) (*steward.Config, error) { c := s.cfg; return &c, nil }

type rig struct {
	ctx      context.Context
	store    *discussionlabel.Store
	tickets  *ticket.Service
	database *db.DB
	now      time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	return newRigIn(t, dbtest.WithDB(t))
}

// newRigIn is a rig in a tenant of its own in database.
func newRigIn(t *testing.T, database *db.DB) *rig {
	t.Helper()
	tenant := dbtest.NewTenant(t, database)
	return &rig{
		ctx: db.WithTenant(context.Background(), tenant), store: discussionlabel.NewStore(database), database: database, now: epoch,
		tickets: ticket.NewService(ticket.NewPostgresStore(database), slog.DiscardHandler),
	}
}

func (r *rig) ticketID(t *testing.T) int64 {
	t.Helper()
	created, err := r.tickets.Create(r.ctx, &ticket.CreateParams{Title: "a ticket", Status: ticket.StatusOpen})
	require.NoError(t, err)
	return created.ID
}

func (r *rig) service(c discussionlabel.Classifier, e *steward.Engine, level steward.Level, dailyCap int) *discussionlabel.Service {
	return discussionlabel.NewService(slog.DiscardHandler, c, e,
		settings{steward.Config{Level: level, Mode: level.Mode(), DailyCap: dailyCap}}, r.store, func() time.Time { return r.now })
}

func question(ticketID int64, round int) *discussionlabel.Question {
	return &discussionlabel.Question{Target: discussionlabel.Target{TicketID: ticketID, Attempt: 1, Round: round, Revision: 2, HeadSHA: "abc123"}, Question: "merge?", Handoff: "CI is green"}
}

func routine(category string) *discussionlabel.Classification {
	return &discussionlabel.Classification{Label: discussionlabel.LabelRoutine, Category: category, Reason: "ci green", Reasoning: "because"}
}

func TestClassifierOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		result     *discussionlabel.Classification
		err        error
		failing    string
		erroring   string
		level      steward.Level
		wantLabel  discussionlabel.Label
		wantCat    string
		wantReason string
	}{
		{name: "routine and every check passes", result: routine("merge_continue"), level: steward.LevelShadow, wantLabel: discussionlabel.LabelRoutine, wantCat: "merge_continue", wantReason: "ci green"},
		{name: "routine still routine when the level applies", result: routine("infra_wait"), level: steward.LevelRoutine, wantLabel: discussionlabel.LabelRoutine, wantCat: "infra_wait", wantReason: "ci green"},
		{name: "a failed check makes it yours", result: routine("merge_continue"), failing: steward.CheckCIGreenOnHead, level: steward.LevelShadow,
			wantLabel: discussionlabel.LabelYours, wantReason: "checks failed: ci_green_on_head: detail of ci_green_on_head"},
		{name: "an unreadable check makes it yours", result: routine("gate_confirmed"), erroring: steward.CheckHeadUnchanged, level: steward.LevelShadow,
			wantLabel: discussionlabel.LabelYours, wantReason: "checks failed: head_unchanged: the fact could not be read"},
		{name: "unknown category is yours", result: routine("not_a_category"), level: steward.LevelShadow,
			wantLabel: discussionlabel.LabelYours, wantReason: `no answer template applies to "not_a_category" on this discussion`},
		{name: "a category without a discussion template is yours", result: routine("priority"), level: steward.LevelShadow,
			wantLabel: discussionlabel.LabelYours, wantReason: `no answer template applies to "priority" on this discussion`},
		{name: "classifier says yours", result: &discussionlabel.Classification{Label: discussionlabel.LabelYours, Reason: "a direction call"}, level: steward.LevelShadow,
			wantLabel: discussionlabel.LabelYours, wantReason: "a direction call"},
		{name: "classifier error is yours", err: errors.New("boom"), level: steward.LevelShadow,
			wantLabel: discussionlabel.LabelYours, wantReason: "the classification could not be read"},
		{name: "no classification is yours", level: steward.LevelShadow,
			wantLabel: discussionlabel.LabelYours, wantReason: "the classification could not be read"},
		{name: "a label that is not routine or yours is yours", result: &discussionlabel.Classification{Label: "maybe", Category: "merge_continue"}, level: steward.LevelShadow,
			wantLabel: discussionlabel.LabelYours, wantReason: `the classifier gave "maybe", which is not a label`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			id := r.ticketID(t)
			svc := r.service(&fakeClassifier{result: tc.result, err: tc.err}, engine(tc.failing, tc.erroring), tc.level, 10)
			rec, err := svc.Label(r.ctx, question(id, 1))
			require.NoError(t, err)
			assert.Equal(t, tc.wantLabel, rec.Label)
			assert.Equal(t, tc.wantCat, rec.Category)
			assert.Equal(t, tc.wantReason, rec.Reason)
			assert.NotNil(t, rec.AssessedAt)
			if tc.wantLabel == discussionlabel.LabelRoutine {
				assert.NotEmpty(t, rec.Evidence)
				for _, ev := range rec.Evidence {
					assert.True(t, ev.OK, ev.Check)
				}
			}
		})
	}
}

func TestLevelOffRunsNothing(t *testing.T) {
	r := newRig(t)
	c := &fakeClassifier{result: routine("merge_continue")}
	id := r.ticketID(t)
	_, err := r.service(c, engine("", ""), steward.LevelOff, 10).Label(r.ctx, question(id, 1))
	assert.ErrorIs(t, err, discussionlabel.ErrOff)
	assert.Zero(t, c.calls.Load())
	_, err = r.store.Get(r.ctx, id, 1, 1)
	assert.ErrorIs(t, err, discussionlabel.ErrNotFound, "no label is claimed while off")
}

func TestARoundIsClassifiedOnce(t *testing.T) {
	r := newRig(t)
	id := r.ticketID(t)
	c := &fakeClassifier{result: routine("infra_wait")}
	svc := r.service(c, engine("", ""), steward.LevelShadow, 10)
	first, err := svc.Label(r.ctx, question(id, 1))
	require.NoError(t, err)
	again, err := svc.Label(r.ctx, question(id, 1))
	require.NoError(t, err)
	assert.Equal(t, first.ID, again.ID)
	assert.EqualValues(t, 1, c.calls.Load(), "a repeat reads the label and runs nothing")

	_, err = svc.Label(r.ctx, question(id, 2))
	require.NoError(t, err)
	other := question(id, 1)
	other.Attempt = 2
	got, err := svc.Label(r.ctx, other)
	require.NoError(t, err)
	assert.NotEqual(t, first.ID, got.ID, "another attempt's round 1 is its own label")
	assert.EqualValues(t, 3, c.calls.Load())

	latest, err := r.store.Latest(r.ctx, id, 1)
	require.NoError(t, err)
	assert.Equal(t, 2, latest.Round)
	_, err = r.store.Get(r.ctx, id, 3, 1)
	assert.ErrorIs(t, err, discussionlabel.ErrNotFound)
}

func TestConcurrentDeliveriesOfOneRoundRunOnce(t *testing.T) {
	r := newRig(t)
	id := r.ticketID(t)
	c := &fakeClassifier{result: routine("infra_wait")}
	svc := r.service(c, engine("", ""), steward.LevelShadow, 10)
	const n = 6
	recs := make([]*discussionlabel.Record, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			recs[i], errs[i] = svc.Label(r.ctx, question(id, 1))
		}()
	}
	close(start)
	wg.Wait()
	for i := range n {
		require.NoError(t, errs[i])
		assert.Equal(t, recs[0].ID, recs[i].ID)
	}
	assert.EqualValues(t, 1, c.calls.Load())
}

func TestStoredLabelRoundTrips(t *testing.T) {
	r := newRig(t)
	id := r.ticketID(t)
	svc := r.service(&fakeClassifier{result: routine("merge_continue")}, engine("", ""), steward.LevelShadow, 10)
	saved, err := svc.Label(r.ctx, question(id, 1))
	require.NoError(t, err)
	got, err := r.store.Get(r.ctx, id, 1, 1)
	require.NoError(t, err)
	assert.Equal(t, saved.ID, got.ID)
	assert.Equal(t, "because", got.Reasoning)
	assert.Equal(t, 2, *got.TicketRevision)
	assert.Equal(t, "abc123", *got.HeadSHA)
	assert.Len(t, got.Evidence, len(saved.Evidence))
	_, err = r.store.Get(r.ctx, id, 1, 9)
	assert.ErrorIs(t, err, discussionlabel.ErrNotFound)
}

func TestTheCapGivesNotAssessedAndAssessNowCountsToday(t *testing.T) {
	r := newRig(t)
	c := &fakeClassifier{result: routine("infra_wait")}
	svc := r.service(c, engine("", ""), steward.LevelShadow, 1)
	a, b, d := r.ticketID(t), r.ticketID(t), r.ticketID(t)

	first, err := svc.Label(r.ctx, question(a, 1))
	require.NoError(t, err)
	assert.Equal(t, discussionlabel.LabelRoutine, first.Label)

	capped, err := svc.Label(r.ctx, question(b, 1))
	require.NoError(t, err)
	assert.Equal(t, discussionlabel.LabelNotAssessed, capped.Label)
	assert.Equal(t, discussionlabel.CapReason, capped.Reason)
	assert.Nil(t, capped.AssessedAt)
	assert.EqualValues(t, 1, c.calls.Load(), "no classifier run once the cap is spent")

	_, err = svc.AssessNow(r.ctx, question(b, 1))
	assert.ErrorIs(t, err, discussionlabel.ErrDailyCap, "still capped today")
	assert.EqualValues(t, 1, c.calls.Load(), "assess now runs nothing while capped")

	r.now = r.now.Add(25 * time.Hour)
	now, err := svc.AssessNow(r.ctx, question(b, 1))
	require.NoError(t, err)
	assert.Equal(t, capped.ID, now.ID)
	assert.Equal(t, discussionlabel.LabelRoutine, now.Label)
	assert.NotNil(t, now.AssessedAt)

	// The assessed-now run fills the new day's cap.
	over, err := svc.Label(r.ctx, question(d, 1))
	require.NoError(t, err)
	assert.Equal(t, discussionlabel.LabelNotAssessed, over.Label)

	_, err = svc.AssessNow(r.ctx, question(b, 1))
	assert.ErrorIs(t, err, discussionlabel.ErrNotAssessable, "an assessed label is not assessed again")
	assert.EqualValues(t, 2, c.calls.Load(), "the refused calls ran no classifier")
}

func TestConcurrentClaimsCannotExceedTheCap(t *testing.T) {
	r := newRig(t)
	const runs, dailyCap = 6, 2
	since := r.now.Add(-24 * time.Hour)
	labels := make([]*discussionlabel.Record, runs)
	errs := make([]error, runs)
	ids := make([]int64, runs)
	for i := range ids {
		ids[i] = r.ticketID(t)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range runs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			labels[i], _, errs[i] = r.store.Claim(r.ctx, &discussionlabel.Target{TicketID: ids[i], Attempt: 1, Round: 1}, r.now, since, dailyCap)
		}()
	}
	close(start)
	wg.Wait()
	claimed := 0
	for i := range runs {
		require.NoError(t, errs[i])
		if labels[i].Label == discussionlabel.LabelPending {
			claimed++
		} else {
			assert.Equal(t, discussionlabel.LabelNotAssessed, labels[i].Label)
		}
	}
	assert.Equal(t, dailyCap, claimed)
}

func TestAssessNowRerunsAStalePendingRunAgainstTheDiscussionAsItStands(t *testing.T) {
	r := newRig(t)
	id := r.ticketID(t)
	c := &fakeClassifier{result: routine("infra_wait")}
	svc := r.service(c, engine("", ""), steward.LevelShadow, 10)
	stuck, claimed, err := r.store.Claim(r.ctx, &question(id, 1).Target, r.now, r.now.Add(-24*time.Hour), 10)
	require.NoError(t, err)
	require.True(t, claimed)

	_, err = svc.AssessNow(r.ctx, question(id, 1))
	assert.ErrorIs(t, err, discussionlabel.ErrNotAssessable, "a run still inside its time is left alone")
	assert.Zero(t, c.calls.Load())

	r.now = r.now.Add(discussionlabel.PendingTTL + time.Second)
	q := question(id, 1)
	q.Revision, q.HeadSHA = 5, "def456"
	got, err := svc.AssessNow(r.ctx, q)
	require.NoError(t, err)
	assert.Equal(t, stuck.ID, got.ID)
	assert.Equal(t, discussionlabel.LabelRoutine, got.Label)
	assert.Equal(t, 5, *got.TicketRevision)
	assert.Equal(t, "def456", *got.HeadSHA)
}

type recordingObserver struct{ got []*discussionlabel.Record }

func (o *recordingObserver) Consider(_ context.Context, rec *discussionlabel.Record) {
	o.got = append(o.got, rec)
}

func TestObserverIsToldOfEachCompletedLabel(t *testing.T) {
	r := newRig(t)
	id := r.ticketID(t)
	obs := &recordingObserver{}
	svc := r.service(&fakeClassifier{result: routine("merge_continue")}, engine("", ""), steward.LevelShadow, 10).WithObserver(obs)

	rec, err := svc.Label(r.ctx, question(id, 1))
	require.NoError(t, err)
	require.Len(t, obs.got, 1)
	assert.Equal(t, rec.ID, obs.got[0].ID)
	assert.Equal(t, discussionlabel.LabelRoutine, obs.got[0].Label)

	_, err = svc.Label(r.ctx, question(id, 1))
	require.NoError(t, err)
	assert.Len(t, obs.got, 1, "a repeat completes nothing")
}

func TestObserverIsToldWhenAssessNowCompletesALabel(t *testing.T) {
	r := newRig(t)
	id := r.ticketID(t)
	obs := &recordingObserver{}
	_, err := r.service(nil, engine("", ""), steward.LevelOff, 10).Queue(r.ctx, question(id, 1))
	require.NoError(t, err)
	svc := r.service(&fakeClassifier{result: routine("merge_continue")}, engine("", ""), steward.LevelShadow, 10).WithObserver(obs)

	_, err = svc.AssessNow(r.ctx, question(id, 1))
	require.NoError(t, err)
	require.Len(t, obs.got, 1)
	assert.Equal(t, discussionlabel.LabelRoutine, obs.got[0].Label)
}
