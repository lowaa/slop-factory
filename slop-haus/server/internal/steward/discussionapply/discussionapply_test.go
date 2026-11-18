package discussionapply_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionanswer"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionapply"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionlabel"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func TestMain(m *testing.M) { dbtest.Main(m, schema.Migrations) }

var epoch = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

const (
	revision = 2
	head     = "abc123"
)

type fakeCheck struct {
	name string
	ok   bool
}

func (c fakeCheck) Name() string { return c.name }
func (c fakeCheck) Run(context.Context, *steward.Subject) (steward.Evidence, error) {
	return steward.Evidence{Check: c.name, OK: c.ok, Detail: "detail of " + c.name}, nil
}

func engine(failing string) *steward.Engine {
	var checks []steward.Check
	for _, name := range []string{steward.CheckCIGreenOnHead, steward.CheckHeadUnchanged, steward.CheckDiffWithinScope,
		steward.CheckTicketRevUnchanged, steward.CheckNoHumanOnlyMarker} {
		checks = append(checks, fakeCheck{name, name != failing})
	}
	return steward.NewEngine(slog.DiscardHandler, append(checks, steward.AnswerFromTemplate())...)
}

type settings struct {
	mu  sync.Mutex
	cfg steward.Config
}

func (s *settings) StewardConfig(context.Context) (*steward.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.cfg
	return &c, nil
}

func (s *settings) set(level steward.Level, tiers steward.TierOverrides) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = steward.Config{Level: level, Mode: level.Mode(), DailyCap: 100, VetoMinutes: 30, Tiers: tiers}
}

type discussions struct{}

func (discussions) Open(context.Context, int64, int) (*discussionanswer.Open, error) {
	return &discussionanswer.Open{Attempt: 1, Round: 1, Revision: revision, HeadSHA: head}, nil
}

type poster struct {
	mu     sync.Mutex
	bodies []string
	// loseNext makes the next post land and still fail, as a lost reply.
	loseNext bool
}

func (p *poster) Post(_ context.Context, _ int64, _ int, body, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bodies = append(p.bodies, body)
	if p.loseNext {
		p.loseNext = false
		return errors.New("reply lost")
	}
	return nil
}

func (p *poster) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.bodies)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type rig struct {
	ctx        context.Context
	tenant     uuid.UUID
	tickets    *ticket.Service
	labels     *discussionlabel.Store
	decisions  *discussionanswer.Store
	settings   *settings
	poster     *poster
	clock      *clock
	svc        *discussionapply.Service
	schedules  *discussionapply.Store
	answers    *discussionanswer.Service
	failingChk string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	database := dbtest.WithDB(t)
	tenant := dbtest.NewTenant(t, database)
	r := &rig{
		ctx:       db.WithTenant(context.Background(), tenant),
		tenant:    tenant,
		tickets:   ticket.NewService(ticket.NewPostgresStore(database), slog.DiscardHandler),
		labels:    discussionlabel.NewStore(database),
		decisions: discussionanswer.NewStore(database),
		settings:  &settings{},
		poster:    &poster{},
		clock:     &clock{t: epoch},
	}
	r.settings.set(steward.LevelRoutine, nil)
	r.answers = discussionanswer.NewService(slog.DiscardHandler, &discussionanswer.Deps{
		Labels: r.labels, Discussions: discussions{}, Assessor: r.assessor(), Settings: r.settings,
		Decisions: r.decisions, Poster: r.poster, Now: r.clock.now,
	})
	r.schedules = discussionapply.NewStore(database)
	r.svc = discussionapply.NewService(slog.DiscardHandler, &discussionapply.Deps{
		Settings: r.settings, Schedules: r.schedules, Answers: r.answers,
		Decisions: r.decisions, Labels: r.labels, Now: r.clock.now,
	})
	return r
}

type lateAssessor struct{ r *rig }

func (a lateAssessor) Assess(ctx context.Context, p *steward.Proposal, l steward.Level, o steward.TierOverrides) *steward.Assessment {
	return engine(a.r.failingChk).Assess(ctx, p, l, o)
}

func (r *rig) assessor() discussionanswer.Assessor { return lateAssessor{r} }

// label stores a completed label of the given outcome for round 1 of a fresh ticket.
func (r *rig) label(t *testing.T, o *discussionlabel.Outcome) (*discussionanswer.Round, *discussionlabel.Record) {
	t.Helper()
	created, err := r.tickets.Create(r.ctx, &ticket.CreateParams{Title: "a ticket", Status: ticket.StatusOpen})
	require.NoError(t, err)
	target := &discussionlabel.Target{TicketID: created.ID, Attempt: 1, Round: 1, Revision: revision, HeadSHA: head}
	claimed, ok, err := r.labels.Claim(r.ctx, target, epoch, epoch.Add(-24*time.Hour), 100)
	require.NoError(t, err)
	require.True(t, ok)
	rec, err := r.labels.Complete(r.ctx, claimed.ID, o)
	require.NoError(t, err)
	return &discussionanswer.Round{TicketID: created.ID, Attempt: 1, Round: 1}, rec
}

func routine(category string) *discussionlabel.Outcome {
	return &discussionlabel.Outcome{Label: discussionlabel.LabelRoutine, Category: category, Reason: "ci green",
		Evidence: []steward.Evidence{{Check: "ci_green_on_head", OK: true}}}
}

func yours() *discussionlabel.Outcome {
	return &discussionlabel.Outcome{Label: discussionlabel.LabelYours, Reason: "a direction call"}
}

// TestLevelByTier asserts the feature follows EffectiveTier and nothing else:
// the expected behaviour of each cell is derived from it, and below level 3 it
// is pinned to human.
func TestLevelByTier(t *testing.T) {
	cat, _ := steward.Lookup("merge_continue")
	for level := steward.LevelOff; level <= steward.MaxLevel; level++ {
		for _, override := range []steward.Tier{"", steward.TierHuman, steward.TierVeto, steward.TierAuto} {
			t.Run(string(rune('0'+level))+"/"+string(override), func(t *testing.T) {
				var tiers steward.TierOverrides
				if override != "" {
					tiers = steward.TierOverrides{"merge_continue": override}
				}
				want := steward.EffectiveTier(cat, level, tiers)
				if level < steward.LevelRoutine {
					require.Equal(t, steward.TierHuman, want, "EffectiveTier must apply nothing below level 3")
				}

				r := newRig(t)
				r.settings.set(level, tiers)
				round, rec := r.label(t, routine("merge_continue"))
				r.svc.Consider(r.ctx, rec)

				badge, err := r.svc.Badge(r.ctx, rec)
				require.NoError(t, err)
				switch want {
				case steward.TierAuto:
					assert.Equal(t, 1, r.poster.count())
					assert.Equal(t, discussionapply.StateApplied, badge.State)
				case steward.TierVeto:
					assert.Zero(t, r.poster.count())
					assert.Equal(t, discussionapply.StateScheduled, badge.State)
					assert.True(t, badge.CanVeto)
					assert.True(t, epoch.Add(30*time.Minute).Equal(*badge.At))
				default:
					assert.Zero(t, r.poster.count())
					assert.Equal(t, discussionapply.StateNone, badge.State)
				}
				_ = round
			})
		}
	}
}

func TestYoursIsLeftToOwner(t *testing.T) {
	r := newRig(t)
	r.settings.set(steward.LevelFull, nil)
	_, rec := r.label(t, yours())
	r.svc.Consider(r.ctx, rec)
	assert.Zero(t, r.poster.count())
	b, err := r.svc.Badge(r.ctx, rec)
	require.NoError(t, err)
	assert.Equal(t, discussionapply.StateNone, b.State)
}

func TestFailedCheckLeavesItToOwner(t *testing.T) {
	r := newRig(t)
	r.failingChk = steward.CheckHeadUnchanged
	_, rec := r.label(t, routine("merge_continue"))
	r.svc.Consider(r.ctx, rec)
	assert.Zero(t, r.poster.count())
	b, err := r.svc.Badge(r.ctx, rec)
	require.NoError(t, err)
	assert.Equal(t, discussionapply.StateNone, b.State)
	assert.Contains(t, b.Reason, "no longer pass")
}

func TestAutoAppliedHasNoUndo(t *testing.T) {
	r := newRig(t)
	_, rec := r.label(t, routine("merge_continue"))
	r.svc.Consider(r.ctx, rec)
	b, err := r.svc.Badge(r.ctx, rec)
	require.NoError(t, err)
	assert.Equal(t, discussionapply.StateApplied, b.State)
	assert.False(t, b.CanUndo)
	for _, id := range []string{"merge_continue", "infra_wait", "gate_confirmed"} {
		assert.False(t, discussionapply.CanUndo(id), id)
	}
	assert.True(t, discussionapply.CanUndo("priority"))
}

func TestVetoWindow(t *testing.T) {
	r := newRig(t)
	r.settings.set(steward.LevelRoutine, steward.TierOverrides{"merge_continue": steward.TierVeto})
	_, rec := r.label(t, routine("merge_continue"))
	r.svc.Consider(r.ctx, rec)

	r.clock.advance(29 * time.Minute)
	require.NoError(t, r.svc.ApplyDue(r.ctx))
	assert.Zero(t, r.poster.count(), "inside the window nothing is posted")

	r.clock.advance(time.Minute)
	require.NoError(t, r.svc.ApplyDue(r.ctx))
	assert.Equal(t, 1, r.poster.count())
	require.NoError(t, r.svc.ApplyDue(r.ctx))
	assert.Equal(t, 1, r.poster.count(), "a second sweep posts nothing more")

	b, err := r.svc.Badge(r.ctx, rec)
	require.NoError(t, err)
	assert.Equal(t, discussionapply.StateApplied, b.State)
	assert.False(t, b.CanUndo)
}

// Consider never ran for these labels (a failed write, a cancelled request):
// the sweep schedules them, and the veto window starts when it does.
func TestSweepSchedulesLabelsConsiderMissed(t *testing.T) {
	r := newRig(t)
	r.settings.set(steward.LevelRoutine, steward.TierOverrides{"merge_continue": steward.TierVeto})
	round, rec := r.label(t, routine("merge_continue"))

	require.NoError(t, r.svc.ApplyDue(r.ctx))
	sch, err := r.schedules.Get(r.ctx, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, steward.TierVeto, sch.Tier)
	assert.Zero(t, r.poster.count(), "the window is still open")

	r.clock.advance(30 * time.Minute)
	require.NoError(t, r.svc.ApplyDue(r.ctx))
	assert.Equal(t, 1, r.poster.count())
	_, err = r.svc.Veto(r.ctx, round, "owner")
	assert.ErrorIs(t, err, discussionapply.ErrWindowClosed)
}

func TestSweepPostsAnAutoLabelConsiderMissed(t *testing.T) {
	r := newRig(t)
	r.settings.set(steward.LevelRoutine, steward.TierOverrides{"merge_continue": steward.TierAuto})
	r.label(t, routine("merge_continue"))

	require.NoError(t, r.svc.ApplyDue(r.ctx))
	assert.Equal(t, 1, r.poster.count(), "posted by the pass that scheduled it")
	require.NoError(t, r.svc.ApplyDue(r.ctx))
	assert.Equal(t, 1, r.poster.count())
}

// The post lands and its reply is lost: the claim stays owed, the next sweep
// finishes it, and an owner's edit owed its post is not the sweep's to retry.
func TestSweepFinishesAnOwedClaim(t *testing.T) {
	r := newRig(t)
	r.settings.set(steward.LevelRoutine, steward.TierOverrides{"merge_continue": steward.TierAuto})
	_, rec := r.label(t, routine("merge_continue"))
	r.poster.loseNext = true
	r.svc.Consider(r.ctx, rec)
	assert.Equal(t, 1, r.poster.count())
	dec, err := r.decisions.ForLabel(r.ctx, rec.ID)
	require.NoError(t, err)
	assert.Nil(t, dec.PostedAt)

	require.NoError(t, r.svc.ApplyDue(r.ctx))

	dec, err = r.decisions.ForLabel(r.ctx, rec.ID)
	require.NoError(t, err)
	assert.NotNil(t, dec.PostedAt)
	assert.Equal(t, 2, r.poster.count(), "the repeat relies on the key to be a no-op")
	require.NoError(t, r.svc.ApplyDue(r.ctx))
	assert.Equal(t, 2, r.poster.count(), "a posted claim is not retried")
}

func TestSweepLeavesUnscheduledLabelsItShouldNotApply(t *testing.T) {
	r := newRig(t)
	r.settings.set(steward.LevelRoutine, steward.TierOverrides{"merge_continue": steward.TierAuto})
	r.label(t, yours())
	r.label(t, routine("merge_continue"))
	r.clock.advance(discussionapply.UnscheduledWindow + time.Minute)

	require.NoError(t, r.svc.ApplyDue(r.ctx))

	assert.Zero(t, r.poster.count(), "a yours label, and a routine one past the window, are left to the owner")
}

func TestOwnerVetoIsDisagreementAndStopsApply(t *testing.T) {
	r := newRig(t)
	r.settings.set(steward.LevelRoutine, steward.TierOverrides{"merge_continue": steward.TierVeto})
	round, rec := r.label(t, routine("merge_continue"))
	r.svc.Consider(r.ctx, rec)

	r.clock.advance(10 * time.Minute)
	dec, err := r.svc.Veto(r.ctx, round, "owner")
	require.NoError(t, err)
	assert.Equal(t, discussionanswer.ActionReject, dec.Action, "a veto is a rejected draft, which scores as disagreement")
	assert.Equal(t, "owner", dec.Actor)

	r.clock.advance(time.Hour)
	require.NoError(t, r.svc.ApplyDue(r.ctx))
	assert.Zero(t, r.poster.count())
	b, err := r.svc.Badge(r.ctx, rec)
	require.NoError(t, err)
	assert.Equal(t, discussionapply.StateVetoed, b.State)

	_, err = r.svc.Veto(r.ctx, round, "owner")
	assert.ErrorIs(t, err, discussionapply.ErrWindowClosed)
}

func TestVetoRefusals(t *testing.T) {
	r := newRig(t)
	r.settings.set(steward.LevelRoutine, steward.TierOverrides{"merge_continue": steward.TierVeto})
	round, rec := r.label(t, routine("merge_continue"))
	_, err := r.svc.Veto(r.ctx, round, "owner")
	assert.ErrorIs(t, err, discussionapply.ErrNotScheduled, "nothing scheduled yet")

	r.svc.Consider(r.ctx, rec)
	r.clock.advance(31 * time.Minute)
	_, err = r.svc.Veto(r.ctx, round, "owner")
	assert.ErrorIs(t, err, discussionapply.ErrWindowClosed, "after the deadline")
	b, err := r.svc.Badge(r.ctx, rec)
	require.NoError(t, err)
	assert.Equal(t, discussionapply.StateScheduled, b.State)
	assert.False(t, b.CanVeto, "the veto closes at the deadline even before the sweep")
}

func TestAutoCannotBeVetoed(t *testing.T) {
	r := newRig(t)
	round, rec := r.label(t, routine("merge_continue"))
	r.svc.Consider(r.ctx, rec)
	_, err := r.svc.Veto(r.ctx, round, "owner")
	assert.ErrorIs(t, err, discussionapply.ErrWindowClosed)
	assert.Equal(t, 1, r.poster.count())
}

func TestLevelLoweredInsideWindowStopsApply(t *testing.T) {
	r := newRig(t)
	r.settings.set(steward.LevelRoutine, steward.TierOverrides{"merge_continue": steward.TierVeto})
	_, rec := r.label(t, routine("merge_continue"))
	r.svc.Consider(r.ctx, rec)

	r.settings.set(steward.LevelAssist, steward.TierOverrides{"merge_continue": steward.TierVeto})
	r.clock.advance(time.Hour)
	require.NoError(t, r.svc.ApplyDue(r.ctx))
	assert.Zero(t, r.poster.count())
	b, err := r.svc.Badge(r.ctx, rec)
	require.NoError(t, err)
	assert.Equal(t, discussionapply.StateNone, b.State)
}

func TestChecksFailingAtDeadlineLeavesItToOwner(t *testing.T) {
	r := newRig(t)
	r.settings.set(steward.LevelRoutine, steward.TierOverrides{"merge_continue": steward.TierVeto})
	_, rec := r.label(t, routine("merge_continue"))
	r.svc.Consider(r.ctx, rec)

	r.failingChk = steward.CheckCIGreenOnHead
	r.clock.advance(time.Hour)
	require.NoError(t, r.svc.ApplyDue(r.ctx))
	assert.Zero(t, r.poster.count())
	b, err := r.svc.Badge(r.ctx, rec)
	require.NoError(t, err)
	assert.Equal(t, discussionapply.StateNone, b.State)
	assert.NotEmpty(t, b.Reason)
}

func TestConsiderTwiceKeepsDeadline(t *testing.T) {
	r := newRig(t)
	r.settings.set(steward.LevelRoutine, steward.TierOverrides{"merge_continue": steward.TierVeto})
	_, rec := r.label(t, routine("merge_continue"))
	r.svc.Consider(r.ctx, rec)
	r.clock.advance(20 * time.Minute)
	r.svc.Consider(r.ctx, rec)
	b, err := r.svc.Badge(r.ctx, rec)
	require.NoError(t, err)
	assert.True(t, epoch.Add(30*time.Minute).Equal(*b.At))
}

func TestAutoScheduleDoesNotBypassNewVetoWindow(t *testing.T) {
	r := newRig(t)
	_, rec := r.label(t, routine("merge_continue"))
	r.settings.set(steward.LevelRoutine, steward.TierOverrides{"merge_continue": steward.TierAuto})
	sch, err := r.schedules.Create(r.ctx, rec.ID, steward.TierAuto, epoch, epoch)
	require.NoError(t, err)
	require.Equal(t, steward.TierAuto, sch.Tier)
	r.settings.set(steward.LevelRoutine, steward.TierOverrides{"merge_continue": steward.TierVeto})
	require.NoError(t, r.svc.ApplyDue(r.ctx))
	assert.Zero(t, r.poster.count())
	b, err := r.svc.Badge(r.ctx, rec)
	require.NoError(t, err)
	assert.Equal(t, discussionapply.StateNone, b.State)
	assert.NotEmpty(t, b.Reason)
}

type roster []uuid.UUID

func (r roster) Tenants(context.Context) ([]uuid.UUID, error) { return r, nil }

func TestSweepOnceVisitsEveryTenant(t *testing.T) {
	r := newRig(t)
	r.settings.set(steward.LevelRoutine, steward.TierOverrides{"merge_continue": steward.TierVeto})
	_, rec := r.label(t, routine("merge_continue"))
	r.svc.Consider(r.ctx, rec)
	r.clock.advance(time.Hour)

	sweeper := discussionapply.NewService(slog.DiscardHandler, &discussionapply.Deps{
		Settings: r.settings, Schedules: r.schedules, Answers: r.answers, Decisions: r.decisions, Labels: r.labels,
		Roster: roster{r.tenant}, Now: r.clock.now,
	})
	require.NoError(t, sweeper.SweepOnce(context.Background()))
	assert.Equal(t, 1, r.poster.count())
}

type commentLog struct {
	mu       sync.Mutex
	comments []*ticket.HumanComment
}

func (c *commentLog) PostComment(_ context.Context, hc *ticket.HumanComment) (*ticket.HumanCommentResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.comments = append(c.comments, hc)
	return &ticket.HumanCommentResult{}, nil
}

type owner struct{ id uuid.UUID }

func (o owner) TenantOwner(context.Context, uuid.UUID) (uuid.UUID, error) { return o.id, nil }

// TestSweepPostsThroughTheRealPosterAsTheTenantOwner has no signed-in person
// behind the sweep; the answer must still post, as the tenant's owner.
func TestSweepPostsThroughTheRealPosterAsTheTenantOwner(t *testing.T) {
	r := newRig(t)
	r.settings.set(steward.LevelRoutine, steward.TierOverrides{"merge_continue": steward.TierVeto})
	comments := &commentLog{}
	ownerID := uuid.New()
	r.answers = discussionanswer.NewService(slog.DiscardHandler, &discussionanswer.Deps{
		Labels: r.labels, Discussions: discussions{}, Assessor: r.assessor(), Settings: r.settings, Decisions: r.decisions,
		Poster: pendingaction.NewDiscussionPoster(comments).WithServerOwners(owner{ownerID}), Now: r.clock.now,
	})
	sweeper := discussionapply.NewService(slog.DiscardHandler, &discussionapply.Deps{
		Settings: r.settings, Schedules: r.schedules, Answers: r.answers, Decisions: r.decisions, Labels: r.labels,
		Roster: roster{r.tenant}, Now: r.clock.now,
	})
	_, rec := r.label(t, routine("merge_continue"))
	sweeper.Consider(r.ctx, rec)
	r.clock.advance(time.Hour)

	require.NoError(t, sweeper.SweepOnce(context.Background()))

	require.Len(t, comments.comments, 1)
	assert.Equal(t, ownerID, comments.comments[0].UserID)
}

func TestDraftBadgeIsBadgeInTheAPIShape(t *testing.T) {
	r := newRig(t)
	r.settings.set(steward.LevelRoutine, steward.TierOverrides{"merge_continue": steward.TierVeto})
	_, rec := r.label(t, routine("merge_continue"))
	r.svc.Consider(r.ctx, rec)

	got, err := r.svc.DraftBadge(r.ctx, rec)

	require.NoError(t, err)
	require.NotNil(t, got.At)
	assert.Equal(t, "scheduled", got.State)
	assert.True(t, got.CanVeto)
	assert.False(t, got.CanUndo)
	assert.True(t, got.At.Equal(epoch.Add(30*time.Minute)))
}

func TestJobSweepsDueAnswers(t *testing.T) {
	r := newRig(t)
	r.settings.set(steward.LevelRoutine, steward.TierOverrides{"merge_continue": steward.TierVeto})
	_, rec := r.label(t, routine("merge_continue"))
	r.svc.Consider(r.ctx, rec)
	r.clock.advance(time.Hour)
	sweeper := discussionapply.NewService(slog.DiscardHandler, &discussionapply.Deps{
		Settings: r.settings, Schedules: r.schedules, Answers: r.answers, Decisions: r.decisions, Labels: r.labels,
		Roster: roster{r.tenant}, Now: r.clock.now,
	})

	job := sweeper.Job(discussionapply.DefaultInterval)

	require.NotNil(t, job)
	require.NoError(t, job.Run(context.Background()))
	assert.Equal(t, 1, r.poster.count())
	assert.Nil(t, sweeper.Job(0))
}
