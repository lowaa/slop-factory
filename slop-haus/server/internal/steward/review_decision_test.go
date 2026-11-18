package steward_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
)

// reviewProposals hands back the review proposal the test set. The decision
// is carried by the real wire layer from the pending action's payload; here
// only what the steward does with it is under test.
type reviewProposals struct{ facts steward.ProposalFacts }

func (p *reviewProposals) Proposal(context.Context, uuid.UUID) (*steward.ProposalFacts, error) {
	f := p.facts
	return &f, nil
}

func newReviewRig(t *testing.T, decision string) (*applyRig, *reviewProposals, *steward.Applier) {
	t.Helper()
	ar := newApplyRig(t)
	rf := goodReview()
	rf.revision = 4
	engine := reviewEngine(rf)
	a, ticketID := ar.proposal(t)
	props := &reviewProposals{facts: steward.ProposalFacts{ID: a.ID, AgentID: a.AgentID, TicketID: &ticketID,
		Kind: steward.KindReviewDecision, Pending: true, Revision: 4, Decision: decision}}
	ar.settings.cfg = steward.Config{Level: steward.LevelFull, Mode: steward.ModeLive, DailyCap: 50, VetoMinutes: 10}
	now := func() time.Time { return ar.now }
	log := slog.NewTextHandler(io.Discard, nil)
	applier := steward.NewApplier(log, ar.store, props, ar.settings, engine, ar.approver, tenantstate.Active(ar.database.Global()), now)
	ar.svc = steward.NewService(log, allowAll{}, props, ar.settings, ar.store, engine, now).WithApplier(applier)
	return ar, props, applier
}

func TestAnApproveReviewReachesItsTierAndIsApplied(t *testing.T) {
	ar, props, _ := newReviewRig(t, steward.ReviewDecisionApprove)

	rec, err := ar.svc.Assess(ar.ctx, ar.console, &steward.Request{ProposalID: props.facts.ID, Category: "review_approve", Reason: "docs only", HeadSHA: "abc123"})
	require.NoError(t, err)

	assert.NotEqual(t, steward.VerdictNeedsHuman, rec.Verdict, "%+v", rec.Evidence)
	assert.Equal(t, []uuid.UUID{props.facts.ID}, ar.approver.asked)
}

func TestARequestChangesReviewStaysWithAHuman(t *testing.T) {
	ar, props, _ := newReviewRig(t, "request_changes")

	rec, err := ar.svc.Assess(ar.ctx, ar.console, &steward.Request{ProposalID: props.facts.ID, Category: "review_approve", Reason: "docs only", HeadSHA: "abc123"})
	require.NoError(t, err)

	assert.Equal(t, steward.VerdictNeedsHuman, rec.Verdict)
	assert.Zero(t, ar.approver.calls())
}

func TestTheApplierRecheckVoidsAReviewWhoseDecisionIsNotApprove(t *testing.T) {
	ar, props, applier := newReviewRig(t, steward.ReviewDecisionApprove)
	ar.settings.cfg.Tiers = steward.TierOverrides{"review_approve": steward.TierVeto}
	rec, err := ar.svc.Assess(ar.ctx, ar.console, &steward.Request{ProposalID: props.facts.ID, Category: "review_approve", Reason: "docs only", HeadSHA: "abc123"})
	require.NoError(t, err)
	require.Zero(t, ar.approver.calls())

	props.facts.Decision = "request_changes"
	ar.now = epoch.Add(time.Hour)
	require.NoError(t, applier.SweepOnce(ar.ctx))

	assert.Zero(t, ar.approver.calls())
	got, err := getByAction(ar.rig, rec.PendingActionID)
	require.NoError(t, err)
	require.NotNil(t, got.VoidedAt)
	assert.Contains(t, got.VoidReason, "request_changes")
}

func TestTheApplierRecheckKeepsAnApproveReviewDecision(t *testing.T) {
	ar, props, applier := newReviewRig(t, steward.ReviewDecisionApprove)
	ar.settings.cfg.Tiers = steward.TierOverrides{"review_approve": steward.TierVeto}
	_, err := ar.svc.Assess(ar.ctx, ar.console, &steward.Request{ProposalID: props.facts.ID, Category: "review_approve", Reason: "docs only", HeadSHA: "abc123"})
	require.NoError(t, err)

	ar.now = epoch.Add(time.Hour)
	require.NoError(t, applier.SweepOnce(ar.ctx))

	assert.Equal(t, []uuid.UUID{props.facts.ID}, ar.approver.asked)
}
