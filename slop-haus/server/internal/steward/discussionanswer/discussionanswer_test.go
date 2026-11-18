package discussionanswer_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionanswer"
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

// engine registers every check the discussion-answer categories name, passing
// except for the one named failing.
func engine(failing string) *steward.Engine {
	var checks []steward.Check
	for _, name := range []string{steward.CheckCIGreenOnHead, steward.CheckHeadUnchanged, steward.CheckDiffWithinScope,
		steward.CheckTicketRevUnchanged, steward.CheckNoHumanOnlyMarker} {
		checks = append(checks, fakeCheck{name, name != failing})
	}
	return steward.NewEngine(slog.DiscardHandler, append(checks, steward.AnswerFromTemplate())...)
}

type settings struct{ cfg steward.Config }

func (s settings) StewardConfig(context.Context) (*steward.Config, error) { c := s.cfg; return &c, nil }

type fakeDiscussions struct {
	mu   sync.Mutex
	open *discussionanswer.Open
}

func (f *fakeDiscussions) Open(context.Context, int64, int) (*discussionanswer.Open, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.open == nil {
		return nil, discussionanswer.ErrNoOpen
	}
	o := *f.open
	return &o, nil
}

type post struct {
	ticketID int64
	attempt  int
	body     string
	key      string
}

type fakePoster struct {
	mu    sync.Mutex
	posts []post
	err   error
	// lost makes a post land and still fail, as a reply lost on the way back.
	lost bool
}

func (f *fakePoster) Post(_ context.Context, ticketID int64, attempt int, body, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil && !f.lost {
		return f.err
	}
	f.posts = append(f.posts, post{ticketID, attempt, body, key})
	return f.err
}

type fakeLabeller struct{ calls atomic.Int32 }

func (f *fakeLabeller) AssessNow(context.Context, *discussionlabel.Question) (*discussionlabel.Record, error) {
	f.calls.Add(1)
	return &discussionlabel.Record{Label: discussionlabel.LabelYours}, nil
}

type rig struct {
	ctx         context.Context
	database    *db.DB
	tenant      uuid.UUID
	labelStore  *discussionlabel.Store
	decisions   *discussionanswer.Store
	tickets     *ticket.Service
	discussions *fakeDiscussions
	poster      *fakePoster
	labeller    *fakeLabeller
}

func newRig(t *testing.T) *rig {
	t.Helper()
	database := dbtest.WithDB(t)
	tenant := dbtest.NewTenant(t, database)
	return &rig{
		ctx:         db.WithTenant(context.Background(), tenant),
		database:    database,
		tenant:      tenant,
		labelStore:  discussionlabel.NewStore(database),
		decisions:   discussionanswer.NewStore(database),
		tickets:     ticket.NewService(ticket.NewPostgresStore(database), slog.DiscardHandler),
		discussions: &fakeDiscussions{open: &discussionanswer.Open{Attempt: 1, Round: 1, Revision: revision, HeadSHA: head}},
		poster:      &fakePoster{},
		labeller:    &fakeLabeller{},
	}
}

func (r *rig) service(failing string, level steward.Level) *discussionanswer.Service {
	return discussionanswer.NewService(slog.DiscardHandler, &discussionanswer.Deps{
		Labels: r.labelStore, Labeller: r.labeller, Discussions: r.discussions, Assessor: engine(failing),
		Settings:  settings{steward.Config{Level: level, Mode: level.Mode(), DailyCap: 100}},
		Decisions: r.decisions, Poster: r.poster, Now: func() time.Time { return epoch },
	})
}

// label stores a completed label for round 1 of a fresh ticket.
func (r *rig) label(t *testing.T, o *discussionlabel.Outcome) (*discussionanswer.Round, *discussionlabel.Record) {
	t.Helper()
	created, err := r.tickets.Create(r.ctx, &ticket.CreateParams{Title: "a ticket", Status: ticket.StatusOpen})
	require.NoError(t, err)
	target := &discussionlabel.Target{TicketID: created.ID, Attempt: 1, Round: 1, Revision: revision, HeadSHA: head}
	claimed, ok, err := r.labelStore.Claim(r.ctx, target, epoch, epoch.Add(-24*time.Hour), 100)
	require.NoError(t, err)
	require.True(t, ok)
	rec, err := r.labelStore.Complete(r.ctx, claimed.ID, o)
	require.NoError(t, err)
	return &discussionanswer.Round{TicketID: created.ID, Attempt: 1, Round: 1}, rec
}

func routineOutcome() *discussionlabel.Outcome {
	return &discussionlabel.Outcome{Label: discussionlabel.LabelRoutine, Category: "merge_continue", Reason: "ci green",
		Evidence: []steward.Evidence{{Check: "ci_green_on_head", OK: true}}}
}

func yoursOutcome() *discussionlabel.Outcome {
	return &discussionlabel.Outcome{Label: discussionlabel.LabelYours, Reason: "a direction call"}
}

func wantBody(id int64) string {
	b, _ := steward.RenderAnswer("merge_continue", &steward.Subject{TicketID: id, HeadSHA: head})
	return b
}

func TestDraft(t *testing.T) {
	r := newRig(t)
	round, rec := r.label(t, routineOutcome())
	svc := r.service("", steward.LevelShadow)

	d, err := svc.Draft(r.ctx, round)
	require.NoError(t, err)
	assert.Equal(t, rec.ID, d.Label.ID)
	assert.Equal(t, "merge_continue", d.Category)
	assert.Equal(t, wantBody(round.TicketID), d.Body)
	assert.Equal(t, rec.Evidence, d.Evidence)
	assert.Nil(t, d.Decision)

	yoursRound, _ := r.label(t, yoursOutcome())
	d, err = svc.Draft(r.ctx, yoursRound)
	require.NoError(t, err)
	assert.Empty(t, d.Body)

	_, err = svc.Draft(r.ctx, &discussionanswer.Round{TicketID: round.TicketID, Attempt: 1, Round: 9})
	assert.ErrorIs(t, err, discussionlabel.ErrNotFound)
}

func TestApprovePostsTemplateOnce(t *testing.T) {
	r := newRig(t)
	round, _ := r.label(t, routineOutcome())
	svc := r.service("", steward.LevelShadow)

	dec, err := svc.Approve(r.ctx, round, "owner")
	require.NoError(t, err)
	assert.Equal(t, discussionanswer.ActionApprove, dec.Action)
	assert.Equal(t, wantBody(round.TicketID), dec.FinalBody)
	assert.Equal(t, []post{{round.TicketID, 1, wantBody(round.TicketID), dec.ID.String()}}, r.poster.posts)

	_, err = svc.Approve(r.ctx, round, "owner")
	assert.ErrorIs(t, err, discussionanswer.ErrAlreadyDecided)
	_, err = svc.Edit(r.ctx, round, "other words", "owner")
	assert.ErrorIs(t, err, discussionanswer.ErrAlreadyDecided)
	assert.Len(t, r.poster.posts, 1)

	d, err := svc.Draft(r.ctx, round)
	require.NoError(t, err)
	require.NotNil(t, d.Decision)
	assert.Equal(t, "owner", d.Decision.Actor)
}

func TestConcurrentApprovePostsOnce(t *testing.T) {
	r := newRig(t)
	round, rec := r.label(t, routineOutcome())
	svc := r.service("", steward.LevelShadow)

	// Leave the claim's post owed, so every concurrent caller below finds it
	// and posts: the interleaving is forced rather than left to scheduling.
	r.poster.err = errors.New("post failed")
	_, err := svc.Approve(r.ctx, round, "owner")
	require.Error(t, err)
	claim, err := r.decisions.ForLabel(r.ctx, rec.ID)
	require.NoError(t, err)
	r.poster.err = nil
	r.poster.posts = nil

	// A caller that loses the claim race may finish the winner's owed post, so
	// several can succeed and post; the ticket service dedupes on the key. What
	// must hold is one decision, so one key, and no caller decides differently.
	var wg sync.WaitGroup
	var mu sync.Mutex
	ids := map[uuid.UUID]bool{}
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dec, err := svc.Approve(r.ctx, round, "owner")
			if err != nil {
				assert.ErrorIs(t, err, discussionanswer.ErrAlreadyDecided)
				return
			}
			mu.Lock()
			ids[dec.ID] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
	assert.Equal(t, map[uuid.UUID]bool{claim.ID: true}, ids)
	r.poster.mu.Lock()
	defer r.poster.mu.Unlock()
	require.NotEmpty(t, r.poster.posts)
	for _, p := range r.poster.posts {
		assert.Equal(t, claim.ID.String(), p.key)
	}
}

func TestApproveRefusals(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outcome *discussionlabel.Outcome
		failing string
		level   steward.Level
		move    func(*fakeDiscussions)
		want    error
	}{
		{name: "yours label", outcome: yoursOutcome(), level: steward.LevelShadow, want: discussionanswer.ErrNotRoutine},
		{name: "revision moved", outcome: routineOutcome(), level: steward.LevelShadow, want: discussionanswer.ErrStale,
			move: func(f *fakeDiscussions) { f.open.Revision++ }},
		{name: "head moved", outcome: routineOutcome(), level: steward.LevelShadow, want: discussionanswer.ErrStale,
			move: func(f *fakeDiscussions) { f.open.HeadSHA = "def456" }},
		{name: "round moved on", outcome: routineOutcome(), level: steward.LevelShadow, want: discussionanswer.ErrStale,
			move: func(f *fakeDiscussions) { f.open.Round = 2 }},
		{name: "attempt moved on", outcome: routineOutcome(), level: steward.LevelShadow, want: discussionanswer.ErrStale,
			move: func(f *fakeDiscussions) { f.open.Attempt = 2 }},
		{name: "no open discussion", outcome: routineOutcome(), level: steward.LevelShadow, want: discussionanswer.ErrStale,
			move: func(f *fakeDiscussions) { f.open = nil }},
		{name: "a check now fails", outcome: routineOutcome(), failing: steward.CheckCIGreenOnHead, level: steward.LevelShadow, want: discussionanswer.ErrChecksFailed},
		{name: "steward off", outcome: routineOutcome(), level: steward.LevelOff, want: discussionanswer.ErrOff},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			round, _ := r.label(t, tc.outcome)
			if tc.move != nil {
				tc.move(r.discussions)
			}
			_, err := r.service(tc.failing, tc.level).Approve(r.ctx, round, "owner")
			assert.ErrorIs(t, err, tc.want)
			assert.Empty(t, r.poster.posts)
			// A refused approve claims nothing: the owner can still edit.
			_, err = r.decisions.ForLabel(r.ctx, mustLabel(t, r, round).ID)
			assert.ErrorIs(t, err, discussionanswer.ErrNoDecision)
		})
	}
}

func mustLabel(t *testing.T, r *rig, round *discussionanswer.Round) *discussionlabel.Record {
	t.Helper()
	rec, err := r.labelStore.Get(r.ctx, round.TicketID, round.Attempt, round.Round)
	require.NoError(t, err)
	return rec
}

func TestEditPostsOwnerText(t *testing.T) {
	r := newRig(t)
	round, _ := r.label(t, routineOutcome())
	svc := r.service("", steward.LevelShadow)

	dec, err := svc.Edit(r.ctx, round, "do it differently", "owner")
	require.NoError(t, err)
	assert.Equal(t, discussionanswer.ActionEdit, dec.Action)
	assert.Equal(t, wantBody(round.TicketID), dec.OriginalBody)
	assert.Equal(t, "do it differently", dec.FinalBody)
	assert.Equal(t, []post{{round.TicketID, 1, "do it differently", dec.ID.String()}}, r.poster.posts)
}

func TestEditOfYoursLabelHasNoOriginal(t *testing.T) {
	r := newRig(t)
	round, _ := r.label(t, yoursOutcome())
	dec, err := r.service("", steward.LevelShadow).Edit(r.ctx, round, "my answer", "owner")
	require.NoError(t, err)
	assert.Empty(t, dec.OriginalBody)
	assert.Equal(t, "my answer", r.poster.posts[0].body)
}

func TestRejectPostsNothing(t *testing.T) {
	r := newRig(t)
	round, _ := r.label(t, routineOutcome())
	svc := r.service("", steward.LevelShadow)

	dec, err := svc.Reject(r.ctx, round, "not what I want", "owner")
	require.NoError(t, err)
	assert.Equal(t, discussionanswer.ActionReject, dec.Action)
	assert.Equal(t, wantBody(round.TicketID), dec.OriginalBody)
	assert.Equal(t, "not what I want", dec.Reason)
	assert.Empty(t, dec.FinalBody)
	assert.Empty(t, r.poster.posts)
	assert.NotNil(t, dec.PostedAt, "a reject has no post to wait for")

	_, err = svc.Approve(r.ctx, round, "owner")
	assert.ErrorIs(t, err, discussionanswer.ErrAlreadyDecided)
}

func TestEditAndRejectRefusals(t *testing.T) {
	r := newRig(t)
	round, _ := r.label(t, routineOutcome())
	svc := r.service("", steward.LevelShadow)

	_, err := svc.Edit(r.ctx, round, "  ", "owner")
	assert.ErrorIs(t, err, discussionanswer.ErrEmpty)
	_, err = svc.Reject(r.ctx, round, "", "owner")
	assert.ErrorIs(t, err, discussionanswer.ErrEmpty)

	r.discussions.open.Round = 2
	_, err = svc.Edit(r.ctx, round, "text", "owner")
	assert.ErrorIs(t, err, discussionanswer.ErrStale)
	_, err = svc.Reject(r.ctx, round, "why", "owner")
	assert.ErrorIs(t, err, discussionanswer.ErrStale)
	assert.Empty(t, r.poster.posts)
}

func TestNotAssessedHasNoDraft(t *testing.T) {
	r := newRig(t)
	created, err := r.tickets.Create(r.ctx, &ticket.CreateParams{Title: "capped", Status: ticket.StatusOpen})
	require.NoError(t, err)
	_, _, err = r.labelStore.Claim(r.ctx, &discussionlabel.Target{TicketID: created.ID, Attempt: 1, Round: 1, Revision: revision, HeadSHA: head}, epoch, epoch.Add(-time.Hour), 0)
	require.NoError(t, err)
	round := &discussionanswer.Round{TicketID: created.ID, Attempt: 1, Round: 1}
	svc := r.service("", steward.LevelShadow)

	_, err = svc.Edit(r.ctx, round, "text", "owner")
	assert.ErrorIs(t, err, discussionanswer.ErrNoDraft)
	_, err = svc.Reject(r.ctx, round, "why", "owner")
	assert.ErrorIs(t, err, discussionanswer.ErrNoDraft)
	_, err = svc.Approve(r.ctx, round, "owner")
	assert.ErrorIs(t, err, discussionanswer.ErrNotRoutine)

	_, err = svc.AssessNow(r.ctx, round)
	require.NoError(t, err)
	assert.EqualValues(t, 1, r.labeller.calls.Load())
}

func TestFailedPostKeepsTheClaimAndRetriesUnderTheSameKey(t *testing.T) {
	r := newRig(t)
	round, rec := r.label(t, routineOutcome())
	svc := r.service("", steward.LevelShadow)

	r.poster.err = errors.New("host down")
	r.poster.lost = true
	_, err := svc.Approve(r.ctx, round, "owner")
	require.ErrorContains(t, err, "host down")
	claim, err := r.decisions.ForLabel(r.ctx, rec.ID)
	require.NoError(t, err)
	assert.Nil(t, claim.PostedAt, "a post of unknown outcome is not marked posted")

	r.poster.err = nil
	dec, err := svc.Approve(r.ctx, round, "owner")
	require.NoError(t, err)
	assert.NotNil(t, dec.PostedAt)
	assert.Equal(t, claim.ID, dec.ID)
	require.Len(t, r.poster.posts, 2, "the retry posts again, relying on the key to make that a no-op")
	assert.Equal(t, claim.ID.String(), r.poster.posts[0].key)
	assert.Equal(t, r.poster.posts[0].key, r.poster.posts[1].key)

	_, err = svc.Approve(r.ctx, round, "owner")
	assert.ErrorIs(t, err, discussionanswer.ErrAlreadyDecided)
	assert.Len(t, r.poster.posts, 2)
}

func TestEditRetryFinishesTheOwedPostOnlyForTheSameText(t *testing.T) {
	r := newRig(t)
	round, _ := r.label(t, routineOutcome())
	svc := r.service("", steward.LevelShadow)
	r.poster.err = errors.New("host down")
	r.poster.lost = true
	_, err := svc.Edit(r.ctx, round, "my words", "owner")
	require.Error(t, err)
	r.poster.err = nil

	_, err = svc.Edit(r.ctx, round, "other words", "owner")
	assert.ErrorIs(t, err, discussionanswer.ErrAlreadyDecided)
	dec, err := svc.Edit(r.ctx, round, "my words", "owner")
	require.NoError(t, err)
	assert.NotNil(t, dec.PostedAt)
	require.Len(t, r.poster.posts, 2)
	assert.Equal(t, r.poster.posts[0].key, r.poster.posts[1].key)
	assert.Equal(t, dec.ID.String(), r.poster.posts[1].key)
}

// Turning the steward off does not strand an answer whose post is owed.
func TestOwedApprovalIsFinishedEvenWhenTheStewardIsOff(t *testing.T) {
	r := newRig(t)
	round, _ := r.label(t, routineOutcome())
	r.poster.err = errors.New("host down")
	r.poster.lost = true
	_, err := r.service("", steward.LevelShadow).Approve(r.ctx, round, "owner")
	require.Error(t, err)
	r.poster.err = nil

	dec, err := r.service("", steward.LevelOff).Approve(r.ctx, round, "owner")

	require.NoError(t, err)
	assert.NotNil(t, dec.PostedAt)
}

// A different decision does not take over a claim whose post is owed.
func TestOwedClaimIsNotTakenOverByAnotherDecision(t *testing.T) {
	r := newRig(t)
	round, _ := r.label(t, routineOutcome())
	svc := r.service("", steward.LevelShadow)
	r.poster.err = errors.New("host down")
	_, err := svc.Approve(r.ctx, round, "owner")
	require.Error(t, err)

	_, err = svc.Edit(r.ctx, round, "my own words", "owner")

	assert.ErrorIs(t, err, discussionanswer.ErrAlreadyDecided)
}

func TestStoreOneDecisionPerLabel(t *testing.T) {
	r := newRig(t)
	_, rec := r.label(t, routineOutcome())

	first, err := r.decisions.Claim(r.ctx, &discussionanswer.Decision{LabelID: rec.ID, Action: discussionanswer.ActionApprove, FinalBody: "x", Actor: "a", CreatedAt: epoch})
	require.NoError(t, err)
	_, err = r.decisions.Claim(r.ctx, &discussionanswer.Decision{LabelID: rec.ID, Action: discussionanswer.ActionReject, Reason: "r", Actor: "b", CreatedAt: epoch})
	assert.ErrorIs(t, err, discussionanswer.ErrAlreadyDecided)

	got, err := r.decisions.ForLabel(r.ctx, rec.ID)
	require.NoError(t, err)
	assert.Equal(t, first.ID, got.ID)

	assert.Nil(t, got.PostedAt)
	require.NoError(t, r.decisions.MarkPosted(r.ctx, first.ID, epoch))
	require.NoError(t, r.decisions.MarkPosted(r.ctx, first.ID, epoch.Add(time.Hour)))
	got, err = r.decisions.ForLabel(r.ctx, rec.ID)
	require.NoError(t, err)
	require.NotNil(t, got.PostedAt)
	assert.True(t, epoch.Equal(*got.PostedAt), "the first time is kept")
}

func TestStoreConstraints(t *testing.T) {
	r := newRig(t)
	_, rec := r.label(t, routineOutcome())

	_, err := r.decisions.Claim(r.ctx, &discussionanswer.Decision{LabelID: rec.ID, Action: "bogus", FinalBody: "x", Actor: "a", CreatedAt: epoch})
	assert.Error(t, err)
	_, err = r.decisions.Claim(r.ctx, &discussionanswer.Decision{LabelID: rec.ID, Action: discussionanswer.ActionReject, Actor: "a", CreatedAt: epoch})
	assert.Error(t, err, "a reject needs a reason")
	_, err = r.decisions.Claim(r.ctx, &discussionanswer.Decision{LabelID: rec.ID, Action: discussionanswer.ActionEdit, Actor: "a", CreatedAt: epoch})
	assert.Error(t, err, "an edit needs a body")
}

// The real poster, over the real decision store: the owner's approval comes
// out as their own comment, the ticket leaves discussion, and a second call
// posts nothing.
func TestApproveThroughTheRealPosterPostsOnce(t *testing.T) {
	r := newRig(t)
	round, _ := r.label(t, routineOutcome())
	_, err := r.database.Exec(r.ctx, `UPDATE tickets SET status = 'discussion' WHERE id = $1`, round.TicketID)
	require.NoError(t, err)
	owner := &authn.User{ID: dbtest.NewUser(t, r.database), TenantID: r.tenant}
	ctx := authn.NewUserContext(r.ctx, owner)
	svc := discussionanswer.NewService(slog.DiscardHandler, &discussionanswer.Deps{
		Labels: r.labelStore, Discussions: r.discussions, Assessor: engine(""),
		Settings:  settings{steward.Config{Level: steward.LevelShadow, Mode: steward.LevelShadow.Mode(), DailyCap: 100}},
		Decisions: r.decisions, Poster: pendingaction.NewDiscussionPoster(r.tickets), Now: func() time.Time { return epoch },
	})

	_, err = svc.Approve(ctx, round, owner.ID.String())
	require.NoError(t, err)
	_, err = svc.Approve(ctx, round, owner.ID.String())
	assert.ErrorIs(t, err, discussionanswer.ErrAlreadyDecided)

	got, err := r.tickets.Get(ctx, round.TicketID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusOpen, got.Status)
	comments, err := r.tickets.ListComments(ctx, round.TicketID)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	require.NotNil(t, comments[0].AuthorUserID)
	assert.Equal(t, owner.ID, *comments[0].AuthorUserID)
	assert.Contains(t, comments[0].Body, wantBody(round.TicketID))
}

// lostReply posts through the real poster and then reports failure, so the
// answer is on the ticket while the service believes it is not.
type lostReply struct{ inner discussionanswer.Poster }

func (l *lostReply) Post(ctx context.Context, ticketID int64, attempt int, body, key string) error {
	if err := l.inner.Post(ctx, ticketID, attempt, body, key); err != nil {
		return err
	}
	return errors.New("reply lost")
}

// The answer lands, the reply is lost, and the ticket has left discussion
// (so the round is stale): the retry must finish the claim, not post again and
// not be refused.
func TestRetryAfterALandedPostPostsNothingMore(t *testing.T) {
	r := newRig(t)
	round, _ := r.label(t, routineOutcome())
	_, err := r.database.Exec(r.ctx, `UPDATE tickets SET status = 'discussion' WHERE id = $1`, round.TicketID)
	require.NoError(t, err)
	owner := &authn.User{ID: dbtest.NewUser(t, r.database), TenantID: r.tenant}
	ctx := authn.NewUserContext(r.ctx, owner)
	real := pendingaction.NewDiscussionPoster(r.tickets)
	build := func(p discussionanswer.Poster) *discussionanswer.Service {
		return discussionanswer.NewService(slog.DiscardHandler, &discussionanswer.Deps{
			Labels: r.labelStore, Discussions: r.discussions, Assessor: engine(""),
			Settings:  settings{steward.Config{Level: steward.LevelShadow, Mode: steward.LevelShadow.Mode(), DailyCap: 100}},
			Decisions: r.decisions, Poster: p, Now: func() time.Time { return epoch },
		})
	}

	_, err = build(&lostReply{inner: real}).Approve(ctx, round, owner.ID.String())
	require.ErrorContains(t, err, "reply lost")
	r.discussions.open = nil

	dec, err := build(real).Approve(ctx, round, owner.ID.String())
	require.NoError(t, err)
	assert.NotNil(t, dec.PostedAt)
	comments, err := r.tickets.ListComments(ctx, round.TicketID)
	require.NoError(t, err)
	assert.Len(t, comments, 1)
}

func TestAssessNowWithoutAClassifierIsUnavailable(t *testing.T) {
	r := newRig(t)
	round, _ := r.label(t, routineOutcome())
	svc := discussionanswer.NewService(slog.DiscardHandler, &discussionanswer.Deps{Labels: r.labelStore, Discussions: r.discussions})

	_, err := svc.AssessNow(r.ctx, round)

	assert.ErrorIs(t, err, discussionanswer.ErrUnavailable)
}
