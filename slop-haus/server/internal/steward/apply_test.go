package steward_test

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// approver is the board's approval, as far as the applier sees it: it
// records what it was asked and answers what the test says.
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

type stewardSettings struct{ cfg steward.Config }

func (s *stewardSettings) StewardConfig(context.Context) (*steward.Config, error) {
	c := s.cfg
	return &c, nil
}

type allowAll struct{}

func (allowAll) IsSteward(context.Context, uuid.UUID) (bool, error) { return true, nil }

// pendingProposals reads proposals from the real service.
type pendingProposals struct{ r *rig }

func (p pendingProposals) Proposal(ctx context.Context, id uuid.UUID) (*steward.ProposalFacts, error) {
	a, err := p.r.pending.Get(ctx, id)
	if err != nil {
		return nil, steward.ErrNotYours
	}
	facts := &steward.ProposalFacts{ID: a.ID, AgentID: a.AgentID, TicketID: a.TicketID, Kind: string(a.Kind),
		Pending: a.State == pendingaction.StatePending, Revision: a.Basis.Revision, Decision: string(a.Payload.Decision), Body: a.Payload.Body}
	if a.Payload.Edit != nil {
		facts.Paths, facts.EditedFields = a.Payload.Edit.Paths, a.Payload.Edit.Fields()
	}
	return facts, nil
}

type applyRig struct {
	*rig
	facts    *facts
	settings *stewardSettings
	approver *approver
	svc      *steward.Service
	applier  *steward.Applier
	now      time.Time
}

func newApplyRig(t *testing.T) *applyRig {
	t.Helper()
	r := newRig(t, dbtest.WithDB(t))
	ar := &applyRig{rig: r, facts: goodFacts(), approver: &approver{}, now: epoch,
		settings: &stewardSettings{cfg: steward.Config{
			Level: steward.LevelRoutine, Mode: steward.ModeLive, DailyCap: 50, VetoMinutes: 10,
			Tiers: steward.TierOverrides{"ticket_ready": steward.TierVeto},
		}}}
	engine := engineOver(ar.facts)
	proposals := pendingProposals{r}
	now := func() time.Time { return ar.now }
	ar.applier = steward.NewApplier(slog.NewTextHandler(io.Discard, nil), r.store, proposals, ar.settings, engine, ar.approver,
		tenantstate.Active(r.database.Global()), now)
	ar.svc = steward.NewService(slog.NewTextHandler(io.Discard, nil), allowAll{}, proposals, ar.settings, r.store, engine, now).WithApplier(ar.applier)
	return ar
}

// assessOpenTicket proposes and assesses "open a preparing ticket".
func (ar *applyRig) assessOpenTicket(t *testing.T) (*steward.Record, *pendingaction.Action) {
	t.Helper()
	a, _ := ar.proposal(t)
	ar.facts.revision = a.Basis.Revision
	rec, err := ar.svc.Assess(ar.ctx, ar.console, &steward.Request{ProposalID: a.ID, Category: "ticket_ready", Reason: "text is final"})
	require.NoError(t, err)
	return rec, a
}

func (ar *applyRig) record(t *testing.T, a *pendingaction.Action) *steward.Record {
	t.Helper()
	rec, err := getByAction(ar.rig, a.ID)
	require.NoError(t, err)
	return rec
}

func TestAutoTierIsAppliedAtOnceThroughTheApprover(t *testing.T) {
	ar := newApplyRig(t)
	ar.settings.cfg.Tiers = steward.TierOverrides{"ticket_ready": steward.TierAuto}

	_, a := ar.assessOpenTicket(t)

	assert.Equal(t, []uuid.UUID{a.ID}, ar.approver.asked)
	got := ar.record(t, a)
	require.NotNil(t, got.AppliedAt)
	assert.Equal(t, epoch, got.AppliedAt.UTC())
	assert.Nil(t, got.VoidedAt)
}

func TestVetoTierWaitsForItsDeadlineThenIsApplied(t *testing.T) {
	ar := newApplyRig(t)
	_, a := ar.assessOpenTicket(t)

	got := ar.record(t, a)
	require.NotNil(t, got.VetoDeadline)
	assert.Equal(t, epoch.Add(10*time.Minute), got.VetoDeadline.UTC())
	assert.Zero(t, ar.approver.calls(), "nothing is applied inside the window")

	ar.now = epoch.Add(10*time.Minute - time.Second)
	require.NoError(t, ar.applier.SweepOnce(ar.ctx))
	assert.Zero(t, ar.approver.calls(), "not before the deadline")

	ar.now = epoch.Add(10 * time.Minute)
	require.NoError(t, ar.applier.SweepOnce(ar.ctx))
	assert.Equal(t, []uuid.UUID{a.ID}, ar.approver.asked)
	require.NotNil(t, ar.record(t, a).AppliedAt)

	require.NoError(t, ar.applier.SweepOnce(ar.ctx))
	assert.Equal(t, 1, ar.approver.calls(), "applied once")
}

func TestAVetoedOrUndoneDecisionIsNotApplied(t *testing.T) {
	ar := newApplyRig(t)
	_, vetoed := ar.assessOpenTicket(t)
	_, undone := ar.assessOpenTicket(t)
	ar.exec(t, `UPDATE steward_assessments SET vetoed_at = now() WHERE pending_action_id = $1`, vetoed.ID)
	ar.exec(t, `UPDATE steward_assessments SET undone_at = now() WHERE pending_action_id = $1`, undone.ID)

	ar.now = epoch.Add(time.Hour)
	require.NoError(t, ar.applier.SweepOnce(ar.ctx))

	assert.Zero(t, ar.approver.calls())
}

func TestAChangedRevisionVoidsTheDecisionAndLeavesTheProposal(t *testing.T) {
	ar := newApplyRig(t)
	_, a := ar.assessOpenTicket(t)
	ar.facts.revision++

	ar.now = epoch.Add(time.Hour)
	require.NoError(t, ar.applier.SweepOnce(ar.ctx))

	assert.Zero(t, ar.approver.calls())
	got := ar.record(t, a)
	assert.Nil(t, got.AppliedAt)
	require.NotNil(t, got.VoidedAt)
	assert.Equal(t, steward.VoidChecks, *got.VoidKind)
	assert.Contains(t, got.VoidReason, "ticket_rev_unchanged")
	now, err := ar.pending.Get(ar.ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, pendingaction.StatePending, now.State)
}

func TestAChangedHeadVoidsTheDecision(t *testing.T) {
	// No category on a ready proposal checks a head, so one is added for this.
	defer steward.AddCategoryForTest(steward.Category{ID: "head_x", Class: steward.ClassHousekeeping,
		Kinds: []string{steward.KindReady}, Checks: []string{steward.CheckHeadUnchanged}, Evidence: "x"})()
	ar := newApplyRig(t)
	ar.facts.head = "oldhead"
	a, ticketID := ar.proposal(t)
	rev := a.Basis.Revision
	rec, err := ar.store.Create(ar.ctx, &steward.NewRecord{TicketID: &ticketID, PendingActionID: a.ID, AgentID: ar.console,
		Mode: steward.ModeLive, TicketRevision: &rev, HeadSHA: "oldhead", CreatedAt: epoch,
		Assessment: &steward.Assessment{Category: "head_x", Tier: steward.TierVeto, Verdict: steward.VerdictWouldVeto}})
	require.NoError(t, err)
	require.NoError(t, ar.store.SetVetoDeadline(ar.ctx, rec.ID, epoch))

	ar.facts.head = "newhead"
	require.NoError(t, ar.applier.ApplyDue(ar.ctx))

	assert.Zero(t, ar.approver.calls())
	got := ar.record(t, a)
	require.NotNil(t, got.VoidedAt)
	assert.Contains(t, got.VoidReason, "head_unchanged")
}

func TestAnEditWiderThanItsCategoryIsVoided(t *testing.T) {
	ar := newApplyRig(t)
	created, err := ar.tickets.Create(ar.ctx, &ticket.CreateParams{Title: "t", Body: "b", Priority: model.PriorityNormal, Status: ticket.StatusPreparing})
	require.NoError(t, err)
	body := "rewritten"
	high := model.PriorityHigh
	a, err := ar.pending.Propose(ar.ctx, &pendingaction.ProposeParams{AgentID: ar.console, TicketID: created.ID, Kind: pendingaction.KindEdit,
		Payload: pendingaction.Payload{Edit: &pendingaction.EditPayload{BaseRevision: created.Revision, Body: &body, Priority: &high}}})
	require.NoError(t, err)
	ar.facts.revision = a.Basis.Revision
	rev := a.Basis.Revision
	// Assessed as a priority change while carrying a body: stored as if it had passed.
	rec, err := ar.store.Create(ar.ctx, &steward.NewRecord{TicketID: &created.ID, PendingActionID: a.ID, AgentID: ar.console,
		Mode: steward.ModeLive, TicketRevision: &rev, CreatedAt: epoch,
		Assessment: &steward.Assessment{Category: "priority", Tier: steward.TierVeto, Verdict: steward.VerdictWouldVeto}})
	require.NoError(t, err)
	require.NoError(t, ar.store.SetVetoDeadline(ar.ctx, rec.ID, epoch))

	require.NoError(t, ar.applier.ApplyDue(ar.ctx))

	assert.Zero(t, ar.approver.calls())
	assert.Contains(t, ar.record(t, a).VoidReason, "edit_fields_allowed")
}

func TestAnEditOnAProtectedPathIsVoidedAtTheRecheck(t *testing.T) {
	ar := newApplyRig(t)
	created, err := ar.tickets.Create(ar.ctx, &ticket.CreateParams{Title: "t", Body: "b", Priority: model.PriorityNormal, Status: ticket.StatusPreparing})
	require.NoError(t, err)
	high := model.PriorityHigh
	a, err := ar.pending.Propose(ar.ctx, &pendingaction.ProposeParams{AgentID: ar.console, TicketID: created.ID, Kind: pendingaction.KindEdit,
		Payload: pendingaction.Payload{Edit: &pendingaction.EditPayload{BaseRevision: created.Revision, Priority: &high}}})
	require.NoError(t, err)
	ar.facts.revision = a.Basis.Revision
	rev := a.Basis.Revision
	// Assessed when the path was not yet protected; the owner protects it before the window ends.
	rec, err := ar.store.Create(ar.ctx, &steward.NewRecord{TicketID: &created.ID, PendingActionID: a.ID, AgentID: ar.console,
		Mode: steward.ModeLive, TicketRevision: &rev, CreatedAt: epoch,
		Assessment: &steward.Assessment{Category: "bookkeeping", Tier: steward.TierVeto, Verdict: steward.VerdictWouldVeto}})
	require.NoError(t, err)
	require.NoError(t, ar.store.SetVetoDeadline(ar.ctx, rec.ID, epoch))
	ar.facts.protected = map[string]string{"server/auth/": "auth"}

	require.NoError(t, ar.applier.ApplyDue(ar.ctx))

	assert.Zero(t, ar.approver.calls())
	assert.Contains(t, ar.record(t, a).VoidReason, "no_protected_path")
}

func TestAnAssessmentTheServerStoppedBeforeSchedulingIsPickedUp(t *testing.T) {
	ar := newApplyRig(t)
	_, veto := ar.assessOpenTicket(t)
	ar.settings.cfg.Tiers = steward.TierOverrides{"ticket_ready": steward.TierAuto}
	_, auto := ar.assessOpenTicket(t)
	// The server stopped before either was scheduled.
	ar.exec(t, `UPDATE steward_assessments SET veto_deadline = NULL, applied_at = NULL`)
	ar.approver.asked = nil

	ar.now = epoch.Add(30 * time.Second)
	require.NoError(t, ar.applier.SweepOnce(ar.ctx))
	assert.Zero(t, ar.approver.calls(), "a fresh assessment is left to its own request")

	ar.now = epoch.Add(2 * time.Minute)
	require.NoError(t, ar.applier.SweepOnce(ar.ctx))
	assert.Equal(t, []uuid.UUID{auto.ID}, ar.approver.asked, "Auto is applied")
	got := ar.record(t, veto)
	require.NotNil(t, got.VetoDeadline, "Veto gets its window from when it was made")
	assert.Equal(t, epoch.Add(10*time.Minute), got.VetoDeadline.UTC())
	assert.Nil(t, got.AppliedAt)

	ar.now = epoch.Add(11 * time.Minute)
	require.NoError(t, ar.applier.SweepOnce(ar.ctx))
	assert.Equal(t, []uuid.UUID{auto.ID, veto.ID}, ar.approver.asked)
}

func TestAnApplicationInterruptedBeforeTheApprovalIsVoided(t *testing.T) {
	ar := newApplyRig(t)
	_, a := ar.assessOpenTicket(t)
	// Claimed, then the server stopped: the proposal is still pending.
	require.NoError(t, ar.store.Claim(ar.ctx, ar.record(t, a).ID, epoch, epoch.Add(-time.Hour), 50))

	ar.now = epoch.Add(time.Minute)
	require.NoError(t, ar.applier.SweepOnce(ar.ctx))
	assert.NotNil(t, ar.record(t, a).AppliedAt, "not yet taken to be interrupted")

	ar.now = epoch.Add(10 * time.Minute)
	require.NoError(t, ar.applier.SweepOnce(ar.ctx))
	got := ar.record(t, a)
	assert.Nil(t, got.AppliedAt)
	assert.Equal(t, steward.VoidNotApplicable, *got.VoidKind)
	n, err := ar.store.Applied(ar.ctx, epoch.Add(-time.Hour))
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestAProposalDecidedMeanwhileIsNotApplied(t *testing.T) {
	ar := newApplyRig(t)
	_, a := ar.assessOpenTicket(t)
	owner := &authn.User{ID: dbtest.NewUser(t, ar.database), TenantID: ar.tenant, AuthTime: time.Now()}
	_, err := ar.pending.Reject(ar.ctx, owner, a.ID, "no")
	require.NoError(t, err)

	ar.now = epoch.Add(time.Hour)
	require.NoError(t, ar.applier.SweepOnce(ar.ctx))

	assert.Zero(t, ar.approver.calls())
	got := ar.record(t, a)
	assert.Nil(t, got.AppliedAt)
	assert.NotNil(t, got.HumanDecision, "the owner's decision stands")
}

func TestARefusedApprovalVoidsTheClaim(t *testing.T) {
	ar := newApplyRig(t)
	_, a := ar.assessOpenTicket(t)
	ar.approver.err = steward.ErrApprovalRefused

	ar.now = epoch.Add(time.Hour)
	require.NoError(t, ar.applier.SweepOnce(ar.ctx))

	got := ar.record(t, a)
	assert.Nil(t, got.AppliedAt, "a refused approval is not an application")
	assert.Equal(t, steward.VoidRefused, *got.VoidKind)
	n, err := ar.store.Applied(ar.ctx, epoch.Add(-time.Hour))
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestNothingIsAppliedUnlessTheModeIsLive(t *testing.T) {
	for _, mode := range []steward.Mode{steward.ModeShadow, steward.ModeOff} {
		ar := newApplyRig(t)
		_, a := ar.assessOpenTicket(t)
		ar.settings.cfg.Mode = mode

		ar.now = epoch.Add(time.Hour)
		require.NoError(t, ar.applier.SweepOnce(ar.ctx))

		assert.Zero(t, ar.approver.calls(), mode)
		assert.Nil(t, ar.record(t, a).VoidedAt, "left as it was: %s", mode)
	}
}

func TestShadowAssessmentsAreNeverApplied(t *testing.T) {
	ar := newApplyRig(t)
	ar.settings.cfg.Level, ar.settings.cfg.Mode = steward.LevelShadow, steward.ModeShadow
	ar.settings.cfg.Tiers = steward.TierOverrides{"ticket_ready": steward.TierAuto}
	_, a := ar.assessOpenTicket(t)
	ar.settings.cfg.Level, ar.settings.cfg.Mode = steward.LevelRoutine, steward.ModeLive

	ar.now = epoch.Add(time.Hour)
	require.NoError(t, ar.applier.SweepOnce(ar.ctx))

	assert.Zero(t, ar.approver.calls())
	assert.Nil(t, ar.record(t, a).AppliedAt)
}

func TestNeedsHumanAndNeverAutoCategoriesNeverApply(t *testing.T) {
	defer steward.AddCategoryForTest(steward.Category{ID: "never_x", Class: steward.ClassMechanical, NeverAuto: true,
		Kinds: []string{steward.KindReady}, Checks: []string{steward.CheckTicketPreparing}, Evidence: "x"})()
	ar := newApplyRig(t)
	ar.settings.cfg.Tiers = nil

	// The honest path: the engine says needs_human, live mode or not.
	a, _ := ar.proposal(t)
	ar.facts.revision = a.Basis.Revision
	rec, err := ar.svc.Assess(ar.ctx, ar.console, &steward.Request{ProposalID: a.ID, Category: "never_x", Reason: "x"})
	require.NoError(t, err)
	assert.Equal(t, steward.VerdictNeedsHuman, rec.Verdict)

	// A needs_human verdict with a deadline forced on it.
	ar.exec(t, `UPDATE steward_assessments SET veto_deadline = $2 WHERE id = $1`, rec.ID, epoch)
	// A stored verdict that says it may apply, for a category that never may.
	b, ticketID := ar.proposal(t)
	rev := b.Basis.Revision
	forged, err := ar.store.Create(ar.ctx, &steward.NewRecord{TicketID: &ticketID, PendingActionID: b.ID, AgentID: ar.console,
		Mode: steward.ModeLive, TicketRevision: &rev, CreatedAt: epoch,
		Assessment: &steward.Assessment{Category: "never_x", Tier: steward.TierAuto, Verdict: steward.VerdictWouldAuto}})
	require.NoError(t, err)
	require.NoError(t, ar.store.SetVetoDeadline(ar.ctx, forged.ID, epoch))
	ar.facts.revision = rev

	ar.now = epoch.Add(time.Hour)
	require.NoError(t, ar.applier.SweepOnce(ar.ctx))
	ar.applier.Consider(ar.ctx, forged, &ar.settings.cfg)

	assert.Zero(t, ar.approver.calls())
	assert.Equal(t, steward.VoidNotApplicable, *ar.record(t, b).VoidKind)
}

func TestTheDailyCapStopsApplicationsAndIsRecorded(t *testing.T) {
	ar := newApplyRig(t)
	_, first := ar.assessOpenTicket(t)
	_, second := ar.assessOpenTicket(t)
	ar.settings.cfg.DailyCap = 1

	ar.now = epoch.Add(time.Hour)
	require.NoError(t, ar.applier.SweepOnce(ar.ctx))

	assert.Equal(t, 1, ar.approver.calls())
	applied, capped := 0, 0
	for _, a := range []*pendingaction.Action{first, second} {
		rec := ar.record(t, a)
		if rec.AppliedAt != nil {
			applied++
		}
		if rec.VoidKind != nil && *rec.VoidKind == steward.VoidDailyCap {
			capped++
		}
	}
	assert.Equal(t, 1, applied)
	assert.Equal(t, 1, capped)
	hits, err := ar.store.CapHits(ar.ctx, epoch)
	require.NoError(t, err)
	assert.Equal(t, 1, hits)

	// A day later the cap has room again, but the capped decision stays void.
	ar.now = epoch.Add(26 * time.Hour)
	require.NoError(t, ar.applier.SweepOnce(ar.ctx))
	assert.Equal(t, 1, ar.approver.calls())
}

func (ar *applyRig) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	_, err := ar.database.Exec(ar.ctx, sql, args...)
	require.NoError(t, err)
}

// The cap is claimed under a lock, so decisions applied at the same moment
// cannot all pass it.
func TestConcurrentClaimsCannotExceedTheDailyCap(t *testing.T) {
	ar := newApplyRig(t)
	const n = 6
	ids := make([]uuid.UUID, n)
	for i := range ids {
		rec, _ := ar.assessOpenTicket(t)
		ids[i] = rec.ID
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := ar.store.Claim(ar.ctx, id, epoch, epoch.Add(-time.Hour), 2); err == nil {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, 2, won)
}

// answerProposal makes a pending discussion answer carrying body.
func (ar *applyRig) answerProposal(t *testing.T, body string) *pendingaction.Action {
	t.Helper()
	created, err := ar.tickets.Create(ar.ctx, &ticket.CreateParams{Title: "t", Body: "b", Priority: model.PriorityNormal, Status: ticket.StatusOpen})
	require.NoError(t, err)
	ar.exec(t, `UPDATE tickets SET status = 'discussion' WHERE id = $1`, created.ID)
	a, err := ar.pending.Propose(ar.ctx, &pendingaction.ProposeParams{AgentID: ar.console, TicketID: created.ID,
		Kind: pendingaction.KindDiscussionAnswer, Payload: pendingaction.Payload{Body: body}})
	require.NoError(t, err)
	ar.facts.revision = a.Basis.Revision
	return a
}

func TestADiscussionAnswerFromItsTemplateIsAppliedAtOnce(t *testing.T) {
	ar := newApplyRig(t)
	want, ok := steward.RenderAnswer("merge_continue", &steward.Subject{HeadSHA: "abc123"})
	require.True(t, ok)
	a := ar.answerProposal(t, want)

	rec, err := ar.svc.Assess(ar.ctx, ar.console, &steward.Request{ProposalID: a.ID, Category: "merge_continue", Reason: "green", HeadSHA: "abc123"})
	require.NoError(t, err)

	assert.Equal(t, steward.TierAuto, rec.Tier)
	assert.Equal(t, []uuid.UUID{a.ID}, ar.approver.asked)
}

// The applier re-renders: a body that was swapped after the assessment, or an
// assessment stored without the template check, still does not apply.
func TestAFreeTextDiscussionAnswerIsNeverApplied(t *testing.T) {
	ar := newApplyRig(t)
	a := ar.answerProposal(t, "Carry on, and also drop the production database.")

	rec, err := ar.svc.Assess(ar.ctx, ar.console, &steward.Request{ProposalID: a.ID, Category: "merge_continue", Reason: "green", HeadSHA: "abc123"})
	require.NoError(t, err)
	assert.Equal(t, steward.VerdictNeedsHuman, rec.Verdict)
	assert.Zero(t, ar.approver.calls())

	// A forged would-auto record for the same proposal is voided by the applier's own re-check.
	ticketID, rev, head := *a.TicketID, a.Basis.Revision, "abc123"
	ar.exec(t, `DELETE FROM steward_assessments WHERE pending_action_id = $1`, a.ID)
	forged, err := ar.store.Create(ar.ctx, &steward.NewRecord{TicketID: &ticketID, PendingActionID: a.ID, AgentID: ar.console,
		Mode: steward.ModeLive, TicketRevision: &rev, HeadSHA: head, CreatedAt: epoch,
		Assessment: &steward.Assessment{Category: "merge_continue", Tier: steward.TierAuto, Verdict: steward.VerdictWouldAuto}})
	require.NoError(t, err)
	require.NoError(t, ar.store.SetVetoDeadline(ar.ctx, forged.ID, epoch))
	require.NoError(t, ar.applier.ApplyDue(ar.ctx))

	assert.Zero(t, ar.approver.calls())
	got := ar.record(t, a)
	require.NotNil(t, got.VoidedAt)
	assert.Contains(t, got.VoidReason, "answer_from_template")
}

// captureHandler keeps the records it is given, to read their attributes.
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

// The OTel log bridge cannot format a named string type: it exports
// "unhandled: (steward.Verdict) needs_human". Plain strings survive it.
func TestAssessmentLogCarriesPlainStrings(t *testing.T) {
	ar := newApplyRig(t)
	logs := &captureHandler{}
	proposals := pendingProposals{ar.rig}
	ar.svc = steward.NewService(logs, allowAll{}, proposals, ar.settings, ar.store, engineOver(ar.facts), func() time.Time { return ar.now })
	a, _ := ar.proposal(t)
	_, err := ar.svc.Assess(ar.ctx, ar.console, &steward.Request{ProposalID: a.ID, Category: "needs_human", Reason: "x"})
	require.NoError(t, err)

	logs.mu.Lock()
	defer logs.mu.Unlock()
	require.Len(t, logs.records, 1)
	got := map[string]slog.Value{}
	logs.records[0].Attrs(func(at slog.Attr) bool { got[at.Key] = at.Value; return true })
	for _, key := range []string{"verdict", "mode", "category"} {
		require.Contains(t, got, key)
		assert.Equal(t, slog.KindString, got[key].Kind(), key)
	}
	assert.Equal(t, "needs_human", got["verdict"].String())
}
