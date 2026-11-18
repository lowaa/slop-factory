package assist_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/assist"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func TestMain(m *testing.M) { dbtest.Main(m, schema.Migrations) }

var epoch = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// facts is every reader the merge_continue checks use; it decides nothing.
type facts struct {
	revision int
	head     string
}

func (f *facts) ChecksPassed(context.Context, int64, string) (bool, string, error) {
	return true, "ci-run-1", nil
}
func (f *facts) ChangedPaths(context.Context, int64) ([]string, error) {
	return []string{"a/x.go"}, nil
}
func (f *facts) Revision(context.Context, int64) (int, error)       { return f.revision, nil }
func (f *facts) Paths(context.Context, int64) ([]string, error)     { return []string{"a/"}, nil }
func (f *facts) Status(context.Context, int64) (string, error)      { return "preparing", nil }
func (f *facts) CurrentHead(context.Context, int64) (string, error) { return f.head, nil }
func (f *facts) HumanOnly(context.Context, int64) (bool, string, error) {
	return false, "", nil
}

// proposals reports the real pending action as the kind the test names.
type proposals struct {
	agent   uuid.UUID
	ticket  int64
	pending bool
	rev     int
	body    string
}

func (p *proposals) Proposal(_ context.Context, id uuid.UUID) (*steward.ProposalFacts, error) {
	return &steward.ProposalFacts{ID: id, AgentID: p.agent, TicketID: &p.ticket, Kind: steward.KindDiscussionAnswer,
		Pending: p.pending, Revision: p.rev, Body: p.body}, nil
}

type settings struct{ level steward.Level }

func (s *settings) StewardConfig(context.Context) (*steward.Config, error) {
	return &steward.Config{Level: s.level, Mode: s.level.Mode(), DailyCap: 10, VetoMinutes: 10}, nil
}

type allowAll struct{}

func (allowAll) IsSteward(context.Context, uuid.UUID) (bool, error) { return true, nil }

type denyAll struct{}

func (denyAll) IsSteward(context.Context, uuid.UUID) (bool, error) { return false, nil }

type approver struct {
	mu    sync.Mutex
	asked []uuid.UUID
	err   error
}

func (a *approver) ApproveForSteward(_ context.Context, id uuid.UUID) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asked = append(a.asked, id)
	return a.err
}

func (a *approver) calls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.asked)
}

type liveGrants struct{}

func (liveGrants) HasFullAccessGrant(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return true, nil
}

type rig struct {
	ctx      context.Context
	store    *assist.Store
	facts    *facts
	settings *settings
	approver *approver
	props    *proposals
	svc      *assist.Service
	steward  uuid.UUID
	action   *pendingaction.Action
	owner    uuid.UUID
}

func newRig(t *testing.T) *rig {
	t.Helper()
	database := dbtest.WithDB(t)
	tenant := dbtest.NewTenant(t, database)
	ctx := db.WithTenant(context.Background(), tenant)
	tickets := ticket.NewService(ticket.NewPostgresStore(database), slog.DiscardHandler)
	agents := agent.NewService(agent.NewPostgresStore(database), tickets, nil, slog.DiscardHandler)
	pending := pendingaction.NewService(pendingaction.NewPostgresStore(database), pendingaction.Config{}, slog.DiscardHandler,
		pendingaction.WithApproval(tickets, liveGrants{}))
	console, err := agents.CreateKind(ctx, "console", agent.KindConsole)
	require.NoError(t, err)
	created, err := tickets.Create(ctx, &ticket.CreateParams{Title: "a ticket", Status: ticket.StatusOpen})
	require.NoError(t, err)
	_, err = database.Exec(ctx, `UPDATE tickets SET status = 'preparing' WHERE id = $1`, created.ID)
	require.NoError(t, err)
	a, err := pending.Propose(ctx, &pendingaction.ProposeParams{AgentID: console.ID, TicketID: created.ID, Kind: pendingaction.KindReady})
	require.NoError(t, err)

	r := &rig{ctx: ctx, store: assist.NewStore(database), facts: &facts{revision: 4, head: "abc123"},
		settings: &settings{level: steward.LevelAssist}, approver: &approver{}, steward: console.ID, action: a,
		props: &proposals{agent: console.ID, ticket: created.ID, pending: true, rev: 4, body: answer}, owner: uuid.New()}
	engine := steward.NewEngine(slog.NewTextHandler(io.Discard, nil), steward.CIGreenOnHead(r.facts), steward.HeadUnchanged(r.facts),
		steward.TicketRevUnchanged(r.facts), steward.DiffWithinScope(r.facts, r.facts), steward.NoHumanOnlyMarker(r.facts), steward.AnswerFromTemplate())
	r.svc = assist.NewService(slog.NewTextHandler(io.Discard, nil), allowAll{}, r.props, r.settings, r.store, engine, r.approver,
		func() time.Time { return epoch })
	return r
}

const answer = "Carry on and merge: CI is green on abc123 and the change is within the ticket's scope."

func (r *rig) draft(t *testing.T) *assist.Draft {
	t.Helper()
	d, err := r.svc.Draft(r.ctx, r.steward, &assist.Request{ProposalID: r.action.ID, Category: "merge_continue",
		Reason: "green", HeadSHA: "abc123", Body: answer})
	require.NoError(t, err)
	return d
}

func TestADraftIsStoredWithItsEvidenceAndWhatItWasReadAgainst(t *testing.T) {
	r := newRig(t)
	d := r.draft(t)

	got, err := r.store.Get(r.ctx, r.action.ID)
	require.NoError(t, err)
	assert.Equal(t, d.ID, got.ID)
	assert.Equal(t, answer, got.Body)
	assert.Equal(t, 4, got.TicketRevision)
	assert.Equal(t, "abc123", got.HeadSHA)
	assert.Equal(t, []string{"ci_green_on_head", "head_unchanged", "diff_within_scope", "ticket_rev_unchanged", "no_human_only_marker", "answer_from_template"},
		checkNames(got.Evidence))
	for _, ev := range got.Evidence {
		assert.True(t, ev.OK, ev.Check)
	}
	assert.Equal(t, "ci-run-1", got.Evidence[0].Ref)
	assert.Nil(t, got.AppliedAt)

	_, err = r.svc.Draft(r.ctx, r.steward, &assist.Request{ProposalID: r.action.ID, Category: "merge_continue"})
	assert.ErrorIs(t, err, assist.ErrAlreadyDrafted, "one draft per proposal")
}

func TestApplyingADraftApprovesTheProposalOnceAsTheOwner(t *testing.T) {
	r := newRig(t)
	r.draft(t)

	require.NoError(t, r.svc.Apply(r.ctx, r.owner, r.action.ID))

	assert.Equal(t, []uuid.UUID{r.action.ID}, r.approver.asked)
	got, err := r.store.Get(r.ctx, r.action.ID)
	require.NoError(t, err)
	require.NotNil(t, got.AppliedAt)
	assert.Equal(t, r.owner, *got.AppliedBy)

	assert.ErrorIs(t, r.svc.Apply(r.ctx, r.owner, r.action.ID), assist.ErrAlreadyApplied)
	assert.Equal(t, 1, r.approver.calls(), "a second click approves nothing")
}

func TestAMovedRevisionOrHeadRefusesTheApply(t *testing.T) {
	cases := map[string]func(*rig){
		"revision": func(r *rig) { r.facts.revision++ },
		"head":     func(r *rig) { r.facts.head = "def456" },
	}
	for name, move := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.draft(t)
			move(r)

			err := r.svc.Apply(r.ctx, r.owner, r.action.ID)

			require.ErrorIs(t, err, assist.ErrStale)
			assert.Zero(t, r.approver.calls())
			got, err := r.store.Get(r.ctx, r.action.ID)
			require.NoError(t, err)
			assert.Nil(t, got.AppliedAt, "the proposal stays the owner's, and the draft can be tried again")
		})
	}
}

func TestAFailedApprovalReleasesTheDraftForAnotherClick(t *testing.T) {
	r := newRig(t)
	r.draft(t)
	r.approver.err = errors.New("boom")
	require.Error(t, r.svc.Apply(r.ctx, r.owner, r.action.ID))

	r.approver.err = nil
	require.NoError(t, r.svc.Apply(r.ctx, r.owner, r.action.ID))
}

func TestAssistOnlyActsAtLevelTwoAndNeverAppliesByItself(t *testing.T) {
	for _, l := range []steward.Level{steward.LevelOff, steward.LevelShadow, steward.LevelRoutine, steward.LevelFull} {
		r := newRig(t)
		r.settings.level = l
		_, err := r.svc.Draft(r.ctx, r.steward, &assist.Request{ProposalID: r.action.ID, Category: "merge_continue", HeadSHA: "abc123"})
		assert.ErrorIs(t, err, assist.ErrNotAssist, l)
	}

	r := newRig(t)
	r.draft(t)
	r.settings.level = steward.LevelRoutine
	assert.ErrorIs(t, r.svc.Apply(r.ctx, r.owner, r.action.ID), assist.ErrNotAssist)

	// Drafting at level 2 approves nothing: only Apply reaches the approver.
	r = newRig(t)
	r.draft(t)
	assert.Zero(t, r.approver.calls())
}

func TestAWouldSendBackReviewIsNotesOnly(t *testing.T) {
	r := newRig(t)
	r.props = &proposals{agent: r.steward, ticket: r.props.ticket, pending: true, rev: 4}
	engine := steward.NewEngine(slog.NewTextHandler(io.Discard, nil))
	r.svc = assist.NewService(slog.NewTextHandler(io.Discard, nil), allowAll{}, reviewProposals{r.props}, r.settings, r.store, engine,
		r.approver, func() time.Time { return epoch })
	_, err := r.svc.Draft(r.ctx, r.steward, &assist.Request{ProposalID: r.action.ID, Category: "none", Body: "findings", WouldSendBack: true})
	require.NoError(t, err)

	assert.ErrorIs(t, r.svc.Apply(r.ctx, r.owner, r.action.ID), assist.ErrNotApplicable)
	assert.Zero(t, r.approver.calls())
}

func TestOnlyAReviewCanBeMarkedWouldSendBack(t *testing.T) {
	r := newRig(t)
	_, err := r.svc.Draft(r.ctx, r.steward, &assist.Request{ProposalID: r.action.ID, Category: "merge_continue", HeadSHA: "abc123", WouldSendBack: true})
	assert.Error(t, err)
}

type reviewProposals struct{ *proposals }

func (p reviewProposals) Proposal(ctx context.Context, id uuid.UUID) (*steward.ProposalFacts, error) {
	f, err := p.proposals.Proposal(ctx, id)
	if f != nil {
		f.Kind = steward.KindReviewDecision
	}
	return f, err
}

func checkNames(evidence []steward.Evidence) []string {
	var out []string
	for _, ev := range evidence {
		out = append(out, ev.Check)
	}
	return out
}

func TestAFailingCheckIsKeptAsEvidenceAndBlocksTheApply(t *testing.T) {
	r := newRig(t)
	r.facts.head = "def456"
	d := r.draft(t)

	var failed []string
	for _, ev := range d.Evidence {
		if !ev.OK {
			failed = append(failed, ev.Check)
		}
	}
	assert.Equal(t, []string{"head_unchanged"}, failed)
	assert.ErrorIs(t, r.svc.Apply(r.ctx, r.owner, r.action.ID), assist.ErrStale)
}

func TestAnAnswerIsShownAsTheProposalStatesIt(t *testing.T) {
	r := newRig(t)
	r.props.body = "what approving posts"
	d := r.draft(t)
	assert.Equal(t, "what approving posts", d.Body, "the steward's own wording is not what the click does")
}

func TestOnlyAnEnabledStewardMayDraftItsOwnPendingProposal(t *testing.T) {
	req := func(r *rig) *assist.Request {
		return &assist.Request{ProposalID: r.action.ID, Category: "merge_continue", HeadSHA: "abc123"}
	}
	engine := func(r *rig) *steward.Engine { return steward.NewEngine(slog.NewTextHandler(io.Discard, nil)) }

	t.Run("not a steward", func(t *testing.T) {
		r := newRig(t)
		svc := assist.NewService(slog.DiscardHandler, denyAll{}, r.props, r.settings, r.store, engine(r), r.approver, nil)
		_, err := svc.Draft(r.ctx, r.steward, req(r))
		assert.ErrorIs(t, err, steward.ErrNotSteward)
		_, err = r.store.Get(r.ctx, r.action.ID)
		assert.ErrorIs(t, err, assist.ErrNotFound)
	})
	t.Run("another agent's proposal", func(t *testing.T) {
		r := newRig(t)
		_, err := r.svc.Draft(r.ctx, uuid.New(), req(r))
		assert.ErrorIs(t, err, steward.ErrNotYours)
		_, err = r.store.Get(r.ctx, r.action.ID)
		assert.ErrorIs(t, err, assist.ErrNotFound)
	})
	t.Run("a decided proposal", func(t *testing.T) {
		r := newRig(t)
		r.props.pending = false
		_, err := r.svc.Draft(r.ctx, r.steward, req(r))
		assert.ErrorIs(t, err, steward.ErrNotPending)
		_, err = r.store.Get(r.ctx, r.action.ID)
		assert.ErrorIs(t, err, assist.ErrNotFound)
	})
	t.Run("decided after drafting", func(t *testing.T) {
		r := newRig(t)
		r.draft(t)
		r.props.pending = false
		assert.ErrorIs(t, r.svc.Apply(r.ctx, r.owner, r.action.ID), steward.ErrNotPending)
		assert.Zero(t, r.approver.calls())
		got, err := r.store.Get(r.ctx, r.action.ID)
		require.NoError(t, err)
		assert.Nil(t, got.AppliedAt)
	})
}

func TestConcurrentClaimsHaveOneWinner(t *testing.T) {
	r := newRig(t)
	d := r.draft(t)
	const clickers = 8
	start := make(chan struct{})
	results := make(chan error, clickers)
	for range clickers {
		go func() {
			<-start
			results <- r.store.Claim(r.ctx, d.ID, r.owner, epoch, epoch.Add(-time.Minute))
		}()
	}
	close(start)
	won := 0
	for range clickers {
		if err := <-results; err == nil {
			won++
		} else {
			assert.ErrorIs(t, err, assist.ErrAlreadyApplied)
		}
	}
	assert.Equal(t, 1, won)
}

func TestAnInterruptedClaimIsTakenOverOnceStale(t *testing.T) {
	r := newRig(t)
	d := r.draft(t)
	require.NoError(t, r.store.Claim(r.ctx, d.ID, r.owner, epoch, epoch.Add(-time.Minute)))

	assert.ErrorIs(t, r.store.Claim(r.ctx, d.ID, r.owner, epoch.Add(time.Minute), epoch.Add(-time.Minute)), assist.ErrAlreadyApplied,
		"a recent claim stands")
	require.NoError(t, r.store.Claim(r.ctx, d.ID, r.owner, epoch.Add(time.Hour), epoch.Add(time.Minute)),
		"a claim whose proposal is still pending, long after, was interrupted")
}
