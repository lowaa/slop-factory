package steward_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/agreement"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

type configSettings struct{ cfg steward.Config }

func (f configSettings) StewardConfig(context.Context) (*steward.Config, error) {
	c := f.cfg
	return &c, nil
}

// liveAssess stores a live-mode assessment created at the given time.
func (r *rig) liveAssess(t *testing.T, category string, tier steward.Tier, verdict steward.Verdict, at time.Time) (*steward.Record, *pendingaction.Action) {
	t.Helper()
	a, ticketID := r.proposal(t)
	rec, err := r.store.Create(r.ctx, &steward.NewRecord{
		TicketID: &ticketID, PendingActionID: a.ID, AgentID: r.console, Mode: steward.ModeLive, CreatedAt: at,
		Assessment: &steward.Assessment{Category: category, Tier: tier, Verdict: verdict, Reason: "because"},
	})
	require.NoError(t, err)
	return rec, a
}

func TestDigestCountsOutcomesAndAgreementPerCategory(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	now := time.Now().UTC().Truncate(time.Second)
	recent := now.Add(-2 * time.Hour)
	user := dbtest.NewUser(t, r.database)

	applied, _ := r.liveAssess(t, "a", steward.TierAuto, steward.VerdictWouldAuto, recent)
	require.NoError(t, r.store.Claim(r.ctx, applied.ID, recent, now.Add(-24*time.Hour), 10))

	undone, _ := r.liveAssess(t, "a", steward.TierAuto, steward.VerdictWouldAuto, recent)
	require.NoError(t, r.store.Claim(r.ctx, undone.ID, recent, now.Add(-24*time.Hour), 10))
	require.NoError(t, r.store.MarkUndone(r.ctx, undone.ID, recent))

	vetoed, _ := r.liveAssess(t, "b", steward.TierVeto, steward.VerdictWouldVeto, recent)
	require.NoError(t, r.store.SetVetoDeadline(r.ctx, vetoed.ID, now.Add(time.Hour)))
	require.NoError(t, r.store.Veto(r.ctx, vetoed.ID, user, recent))

	r.liveAssess(t, "b", steward.TierHuman, steward.VerdictNeedsHuman, recent)

	approved, ap := r.assess(t, "a", steward.VerdictWouldAuto, recent)
	_ = approved
	_, err := r.store.RecordDecision(r.ctx, ap.ID, steward.DecisionApproved, recent)
	require.NoError(t, err)
	_, rp := r.assess(t, "a", steward.VerdictWouldAuto, recent)
	_, err = r.store.RecordDecision(r.ctx, rp.ID, steward.DecisionRejected, recent)
	require.NoError(t, err)

	// Older than a day: in the week, not in the day.
	r.assess(t, "a", steward.VerdictWouldAuto, now.Add(-3*24*time.Hour))

	d := steward.NewDigester(r.store, configSettings{steward.Config{}}, agreement.ForDigest, func() time.Time { return now })
	day, err := d.Digest(r.ctx, "24h")
	require.NoError(t, err)
	require.Len(t, day.Categories, 2)
	a, b := day.Categories[0], day.Categories[1]
	assert.Equal(t, steward.CategoryDigest{Category: "a", Assessed: 4, Applied: 1, Undone: 1, Unclassified: 4, Agreed: 1, Disagreed: 2}, a)
	assert.Equal(t, steward.CategoryDigest{Category: "b", Assessed: 2, Vetoed: 1, NeedsHuman: 1, Unclassified: 2, Disagreed: 1}, b)
	rate, ok := a.AgreementRate()
	assert.True(t, ok)
	assert.InDelta(t, 1.0/3, rate, 0.001, "an undo counts against the steward as well as a rejection")
	rate, ok = b.AgreementRate()
	assert.True(t, ok, "a veto is a decision, though the needs_human one is not")
	assert.Zero(t, rate)
	assert.Equal(t, 6, day.Total.Assessed)
	assert.Len(t, day.Decisions, 6)
	require.NotNil(t, day.Decisions[0].TicketID, "each decision links its ticket")

	week, err := d.Digest(r.ctx, "7d")
	require.NoError(t, err)
	assert.Equal(t, 7, week.Total.Assessed)

	_, err = d.Digest(r.ctx, "1y")
	assert.ErrorIs(t, err, steward.ErrUnknownWindow)
}

func TestDigestExplainsAnEmptyAgreementRecord(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	now := time.Now().UTC().Truncate(time.Second)
	recent := now.Add(-time.Hour)

	// The steward's own "needs_human" label is outside every category.
	r.assess(t, "needs_human", steward.VerdictNeedsHuman, recent)
	r.assess(t, "needs_human", steward.VerdictNeedsHuman, recent)
	// A listed category the engine sent to the owner, and a retired one.
	r.assess(t, "ticket_ready", steward.VerdictNeedsHuman, recent)
	r.assess(t, "open_ticket", steward.VerdictWouldVeto, recent)
	// Claims the owner has not decided, and one decided.
	r.assess(t, "priority", steward.VerdictWouldAuto, recent)
	r.liveAssess(t, "priority", steward.TierVeto, steward.VerdictWouldVeto, recent)
	_, decided := r.assess(t, "priority", steward.VerdictWouldAuto, recent)
	_, err := r.store.RecordDecision(r.ctx, decided.ID, steward.DecisionApproved, recent)
	require.NoError(t, err)

	d := steward.NewDigester(r.store, configSettings{}, agreement.ForDigest, func() time.Time { return now })
	got, err := d.Digest(r.ctx, "24h")
	require.NoError(t, err)

	assert.Equal(t, 7, got.Total.Assessed)
	assert.Equal(t, 3, got.Total.NeedsHuman)
	assert.Equal(t, 2, got.Total.Unclassified, "needs_human is not a category; open_ticket is retired, not unclassified")
	assert.Equal(t, 3, got.Total.Awaiting, "the retired veto verdict, and the undecided priority claims (shadow and live)")
	assert.Equal(t, 1, got.Total.Agreed)
	assert.Zero(t, got.Total.Disagreed)
}

func TestDigestIsTenantScoped(t *testing.T) {
	database := dbtest.WithDB(t)
	mine, other := newRig(t, database), newRig(t, database)
	now := time.Now().UTC()
	other.assess(t, "a", steward.VerdictWouldAuto, now.Add(-time.Hour))
	d := steward.NewDigester(mine.store, configSettings{steward.Config{}}, agreement.ForDigest, func() time.Time { return now })
	got, err := d.Digest(mine.ctx, "24h")
	require.NoError(t, err)
	assert.Zero(t, got.Total.Assessed)
}

// applyNow claims the assessment and approves its proposal, as an application
// that completed.
func (r *rig) applyNow(t *testing.T, rec *steward.Record, at time.Time) {
	t.Helper()
	require.NoError(t, r.store.Claim(r.ctx, rec.ID, at, at.Add(-24*time.Hour), 100))
	_, err := r.database.Exec(r.ctx, `UPDATE pending_actions SET status = 'approved', decided_at = now() WHERE id = $1`, rec.PendingActionID)
	require.NoError(t, err)
}

func TestNoticesListWaitingAndRecentlyApplied(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	now := time.Now().UTC().Truncate(time.Second)
	cfg := steward.Config{Level: steward.LevelRoutine, Mode: steward.ModeLive, DailyCap: 3, VetoMinutes: 60}
	user := dbtest.NewUser(t, r.database)

	waiting, _ := r.liveAssess(t, "b", steward.TierVeto, steward.VerdictWouldVeto, now.Add(-10*time.Minute))
	require.NoError(t, r.store.SetVetoDeadline(r.ctx, waiting.ID, now.Add(50*time.Minute)))
	// Assessed days ago, yet still waiting: the applier would apply it.
	stale, _ := r.liveAssess(t, "b", steward.TierVeto, steward.VerdictWouldVeto, now.Add(-72*time.Hour))
	require.NoError(t, r.store.SetVetoDeadline(r.ctx, stale.ID, now.Add(time.Hour)))
	unscheduled, _ := r.liveAssess(t, "b", steward.TierVeto, steward.VerdictWouldVeto, now.Add(-time.Minute))

	// Not actionable: vetoed, voided, decided by the owner, needs the owner, shadow.
	vetoed, _ := r.liveAssess(t, "b", steward.TierVeto, steward.VerdictWouldVeto, now.Add(-time.Minute))
	require.NoError(t, r.store.SetVetoDeadline(r.ctx, vetoed.ID, now.Add(time.Hour)))
	require.NoError(t, r.store.Veto(r.ctx, vetoed.ID, user, now))
	voided, _ := r.liveAssess(t, "b", steward.TierVeto, steward.VerdictWouldVeto, now.Add(-time.Minute))
	require.NoError(t, r.store.Void(r.ctx, voided.ID, steward.VoidChecks, "x", now))
	_, decidedAction := r.liveAssess(t, "b", steward.TierVeto, steward.VerdictWouldVeto, now.Add(-time.Minute))
	_, err := r.store.RecordDecision(r.ctx, decidedAction.ID, steward.DecisionApproved, now)
	require.NoError(t, err)
	r.liveAssess(t, "b", steward.TierHuman, steward.VerdictNeedsHuman, now.Add(-time.Minute))
	r.assess(t, "b", steward.VerdictWouldVeto, now.Add(-time.Minute))

	recent, _ := r.liveAssess(t, "a", steward.TierAuto, steward.VerdictWouldAuto, now.Add(-3*time.Hour))
	r.applyNow(t, recent, now.Add(-20*time.Minute))
	old, _ := r.liveAssess(t, "a", steward.TierAuto, steward.VerdictWouldAuto, now.Add(-5*time.Hour))
	r.applyNow(t, old, now.Add(-4*time.Hour))
	undone, _ := r.liveAssess(t, "a", steward.TierAuto, steward.VerdictWouldAuto, now.Add(-time.Hour))
	r.applyNow(t, undone, now.Add(-10*time.Minute))
	require.NoError(t, r.store.MarkUndone(r.ctx, undone.ID, now))
	// Claimed but the approval has not gone through: nothing to undo yet.
	inFlight, _ := r.liveAssess(t, "a", steward.TierAuto, steward.VerdictWouldAuto, now.Add(-time.Hour))
	require.NoError(t, r.store.Claim(r.ctx, inFlight.ID, now.Add(-5*time.Minute), now.Add(-24*time.Hour), 100))

	d := steward.NewDigester(r.store, configSettings{cfg}, agreement.ForDigest, func() time.Time { return now })
	n, err := d.Notices(r.ctx)
	require.NoError(t, err)
	var pendingIDs []uuid.UUID
	for _, p := range n.Pending {
		pendingIDs = append(pendingIDs, p.PendingActionID)
	}
	assert.ElementsMatch(t, []uuid.UUID{waiting.PendingActionID, stale.PendingActionID, unscheduled.PendingActionID}, pendingIDs)
	for _, p := range n.Pending {
		if p.PendingActionID == unscheduled.PendingActionID {
			assert.Nil(t, p.At, "no window opened yet")
		} else {
			require.NotNil(t, p.At)
		}
	}
	require.Len(t, n.Applied, 1, "only an approved application of the last hour, not undone")
	assert.Equal(t, recent.PendingActionID, n.Applied[0].PendingActionID)
	assert.True(t, n.CapReached, "four applied in the day against a cap of three")

	cfg.DailyCap = 5
	n, err = steward.NewDigester(r.store, configSettings{cfg}, agreement.ForDigest, func() time.Time { return now }).Notices(r.ctx)
	require.NoError(t, err)
	assert.False(t, n.CapReached)
}

func TestDigestBoundsItsDecisionListNotItsCounts(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	now := time.Now().UTC().Truncate(time.Second)
	total := steward.MaxDigestDecisions + 2
	// More proposals than the default flood limits allow.
	r.pending = pendingaction.NewService(pendingaction.NewPostgresStore(r.database),
		pendingaction.Config{RatePerMinute: 1000, MaxPendingPerAgent: 1000}, slog.DiscardHandler)
	var newest uuid.UUID
	for i := 0; i < total; i++ {
		// i = 0 is the oldest.
		_, a := r.assess(t, "a", steward.VerdictWouldAuto, now.Add(-time.Duration(total-i)*time.Minute))
		newest = a.ID
	}
	d := steward.NewDigester(r.store, configSettings{}, agreement.ForDigest, func() time.Time { return now })
	got, err := d.Digest(r.ctx, "24h")
	require.NoError(t, err)
	assert.Equal(t, total, got.Total.Assessed, "counts cover every assessment")
	require.Len(t, got.Decisions, steward.MaxDigestDecisions)
	assert.True(t, got.DecisionsCut)
	assert.Equal(t, newest, got.Decisions[0].PendingActionID, "newest first")
	for i := 1; i < len(got.Decisions); i++ {
		assert.True(t, got.Decisions[i].At.Before(got.Decisions[i-1].At))
	}
}

func TestNoticesAreEmptyUnlessLive(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	now := time.Now().UTC()
	r.liveAssess(t, "b", steward.TierVeto, steward.VerdictWouldVeto, now.Add(-time.Minute))
	n, err := steward.NewDigester(r.store, configSettings{steward.Config{Mode: steward.ModeShadow, DailyCap: 5}}, agreement.ForDigest, func() time.Time { return now }).Notices(r.ctx)
	require.NoError(t, err)
	assert.Empty(t, n.Pending)
	assert.Equal(t, steward.ModeShadow, n.Mode)
}

func TestDigestHandlerServesTheViews(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	now := time.Now().UTC().Truncate(time.Second)
	r.assess(t, "a", steward.VerdictWouldAuto, now.Add(-time.Hour))
	waiting, _ := r.liveAssess(t, "b", steward.TierVeto, steward.VerdictWouldVeto, now.Add(-time.Minute))
	require.NoError(t, r.store.SetVetoDeadline(r.ctx, waiting.ID, now.Add(time.Hour)))
	applied, _ := r.liveAssess(t, "a", steward.TierAuto, steward.VerdictWouldAuto, now.Add(-time.Minute))
	r.applyNow(t, applied, now.Add(-time.Minute))
	d := steward.NewDigester(r.store, configSettings{steward.Config{Level: steward.LevelRoutine, Mode: steward.ModeLive, DailyCap: 1}}, agreement.ForDigest, func() time.Time { return now })
	// Authentication is the route metadata's; the handlers are called directly.
	h := steward.NewDigestHandler(d, slog.NewTextHandler(io.Discard, nil))

	resp, err := h.GetDigest(r.ctx, &steward.StewardDigestRequest{Window: "24h"})
	require.NoError(t, err)
	assert.Equal(t, 3, resp.Body.Total.Assessed)
	assert.Equal(t, 1, resp.Body.Total.Applied)
	assert.Equal(t, 2, resp.Body.Total.Awaiting, "the shadow claim and the live one in its veto window")
	assert.Equal(t, 3, resp.Body.Total.Unclassified)
	require.Len(t, resp.Body.Decisions, 3)
	assert.Nil(t, resp.Body.Total.AgreementRate)

	_, err = h.GetDigest(r.ctx, &steward.StewardDigestRequest{Window: "bogus"})
	var se huma.StatusError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, http.StatusUnprocessableEntity, se.GetStatus())

	notices, err := h.GetNotices(r.ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "live", notices.Body.Mode)
	require.Len(t, notices.Body.Pending, 1)
	assert.Equal(t, waiting.PendingActionID.String(), notices.Body.Pending[0].ProposalID)
	assert.Equal(t, waiting.TicketID, notices.Body.Pending[0].TicketID)
	assert.Equal(t, "b", notices.Body.Pending[0].Category)
	require.NotNil(t, notices.Body.Pending[0].At)
	require.Len(t, notices.Body.Applied, 1)
	assert.Equal(t, applied.PendingActionID.String(), notices.Body.Applied[0].ProposalID)
	assert.True(t, notices.Body.CapReached)
	assert.Equal(t, 1, notices.Body.DailyCap)
}

// discussionRound stores a labelled discussion round, and the owner's decision
// on it when action is set.
func (r *rig) discussionRound(t *testing.T, round int, label, category, action, original, final string, at time.Time) {
	t.Helper()
	created, err := r.tickets.Create(r.ctx, &ticket.CreateParams{Title: "discussed", Status: ticket.StatusOpen})
	require.NoError(t, err)
	var id string
	require.NoError(t, r.database.QueryRow(r.ctx, `
		INSERT INTO steward_discussion_labels (id, ticket_id, attempt, round, label, category, head_sha, assessed_at, created_at)
		VALUES (gen_random_uuid(), $1, 1, $2, $3, $4, 'abc', $5, $5) RETURNING id::text`,
		created.ID, round, label, category, at).Scan(&id))
	if action == "" {
		return
	}
	_, err = r.database.Exec(r.ctx, `
		INSERT INTO steward_discussion_decisions (id, label_id, action, original_body, final_body, reason, actor, created_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, 'because', 'owner', $5)`, id, action, original, final, at)
	require.NoError(t, err)
}

func TestDigestHandlerReportsDiscussionAgreement(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	now := time.Now().UTC().Truncate(time.Second)
	at := now.Add(-time.Hour)
	tmpl, ok := steward.RenderAnswer("merge_continue", &steward.Subject{TicketID: 1, HeadSHA: "abc"})
	require.True(t, ok)
	// The template is rendered per ticket; the comparison is on the text the
	// owner posted, so an edit with other words is the disagreement.
	r.discussionRound(t, 1, "routine", "merge_continue", "approve", tmpl, tmpl, at)
	r.discussionRound(t, 1, "routine", "merge_continue", "edit", tmpl, "do it my way", at.Add(time.Minute))
	r.discussionRound(t, 1, "yours", "", "edit", "", "use option B", at)
	r.discussionRound(t, 1, "yours", "", "edit", "", tmpl, at.Add(time.Minute))
	// Outside the window.
	r.discussionRound(t, 1, "routine", "merge_continue", "reject", tmpl, "", now.Add(-48*time.Hour))

	cfg := steward.Config{WarnMinDecisions: 3, WarnMinAgreementPct: 60}
	d := steward.NewDigester(r.store, configSettings{cfg}, agreement.ForDigest, func() time.Time { return now }).
		WithDiscussions(agreement.NewDigestSource(agreement.NewScorer(r.store, r.database)))
	h := steward.NewDigestHandler(d, slog.NewTextHandler(io.Discard, nil))

	resp, err := h.GetDigest(r.ctx, &steward.StewardDigestRequest{Window: "24h"})
	require.NoError(t, err)
	disc := resp.Body.Discussions

	require.Len(t, disc.Labels, 2)
	assert.Equal(t, "merge_continue", disc.Labels[0].Category)
	assert.Equal(t, "yours", disc.Labels[1].Category, "the yours bucket is reported with the categories")
	assert.Equal(t, steward.DiscussionScoreBody{Category: "yours", Decisions: 2, Agreed: 1, Disagreed: 1,
		AgreementRate: ptr(0.5), WarnFewDecisions: true, WarnLowAgreement: true}, disc.Labels[1])
	assert.Equal(t, "all", disc.LabelsTotal.Category)
	assert.Equal(t, "all", disc.AnswersTotal.Category)
	assert.True(t, disc.Labels[0].WarnFewDecisions, "1 decision is under the minimum of 3")
	assert.False(t, disc.Labels[0].WarnLowAgreement, "100% is not under 60%")
	assert.Equal(t, 3, disc.LabelsTotal.Decisions, "an edited routine draft is an answer disagreement only")
	assert.Equal(t, 1, disc.LabelsTotal.Disagreed)

	require.Len(t, disc.Answers, 1)
	assert.Equal(t, 2, disc.Answers[0].Decisions)
	assert.Equal(t, 1, disc.Answers[0].Agreed)
	assert.Equal(t, 2, disc.AnswersTotal.Decisions)
	assert.True(t, disc.AnswersTotal.WarnFewDecisions)

	require.Len(t, disc.AnswerDisagreements, 1)
	assert.Equal(t, "do it my way", disc.AnswerDisagreements[0].FinalBody)
	assert.Equal(t, tmpl, disc.AnswerDisagreements[0].OriginalBody)
	assert.Equal(t, "edit", disc.AnswerDisagreements[0].How)
	require.Len(t, disc.LabelDisagreements, 1)
	assert.Equal(t, "yours", disc.LabelDisagreements[0].Category)
}

func TestDigestHandlerReportsAnEmptyDiscussionWindow(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	now := time.Now().UTC().Truncate(time.Second)
	d := steward.NewDigester(r.store, configSettings{}, agreement.ForDigest, func() time.Time { return now }).
		WithDiscussions(agreement.NewDigestSource(agreement.NewScorer(r.store, r.database)))
	resp, err := steward.NewDigestHandler(d, slog.DiscardHandler).GetDigest(r.ctx, &steward.StewardDigestRequest{Window: "7d"})
	require.NoError(t, err)
	disc := resp.Body.Discussions
	assert.NotNil(t, disc.Labels, "empty lists, not null")
	assert.Empty(t, disc.Labels)
	assert.Empty(t, disc.AnswerDisagreements)
	assert.Nil(t, disc.LabelsTotal.AgreementRate)
	assert.Nil(t, disc.AnswersTotal.AgreementRate)
}

func ptr[T any](v T) *T { return &v }
