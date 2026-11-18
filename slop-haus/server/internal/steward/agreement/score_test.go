package agreement_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/agreement"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func TestMain(m *testing.M) { dbtest.Main(m, schema.Migrations) }

var epoch = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

type liveGrants struct{}

func (liveGrants) HasFullAccessGrant(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return true, nil
}

type rig struct {
	database *db.DB
	ctx      context.Context
	tickets  *ticket.Service
	pending  *pendingaction.Service
	store    *steward.Store
	scorer   *agreement.Scorer
	console  uuid.UUID
	approver *authn.User
}

func newRig(t *testing.T, database *db.DB) *rig {
	t.Helper()
	tenant := dbtest.NewTenant(t, database)
	tickets := ticket.NewService(ticket.NewPostgresStore(database), slog.DiscardHandler)
	store := steward.NewStore(database)
	r := &rig{database: database, ctx: db.WithTenant(context.Background(), tenant), tickets: tickets, store: store,
		scorer: agreement.NewScorer(store, database)}
	r.pending = pendingaction.NewService(pendingaction.NewPostgresStore(database), pendingaction.Config{}, slog.DiscardHandler,
		pendingaction.WithApproval(tickets, liveGrants{}),
		pendingaction.WithDecisionRecorder(steward.NewDecisionRecorder(store)))
	agents := agent.NewService(agent.NewPostgresStore(database), tickets, nil, slog.DiscardHandler)
	console, err := agents.CreateKind(r.ctx, "console", agent.KindConsole)
	require.NoError(t, err)
	r.console = console.ID
	r.approver = &authn.User{ID: dbtest.NewUser(t, database), TenantID: tenant, AuthTime: time.Now()}
	return r
}

func (r *rig) assess(t *testing.T, category string, verdict steward.Verdict, at time.Time) *pendingaction.Action {
	t.Helper()
	created, err := r.tickets.Create(r.ctx, &ticket.CreateParams{Title: "a ticket", Status: ticket.StatusOpen})
	require.NoError(t, err)
	_, err = r.database.Exec(r.ctx, `UPDATE tickets SET status = 'preparing' WHERE id = $1`, created.ID)
	require.NoError(t, err)
	a, err := r.pending.Propose(r.ctx, &pendingaction.ProposeParams{AgentID: r.console, TicketID: created.ID, Kind: pendingaction.KindReady})
	require.NoError(t, err)
	_, err = r.store.Create(r.ctx, &steward.NewRecord{TicketID: &created.ID, PendingActionID: a.ID, AgentID: r.console,
		Mode: steward.ModeShadow, CreatedAt: at,
		Assessment: &steward.Assessment{Category: category, Tier: steward.TierVeto, Verdict: verdict, Reason: "steward says " + category}})
	require.NoError(t, err)
	return a
}

func (r *rig) mark(t *testing.T, a *pendingaction.Action, column string) {
	t.Helper()
	_, err := r.database.Exec(r.ctx, `UPDATE steward_assessments SET `+column+` = $2 WHERE pending_action_id = $1`, a.ID, time.Now().Add(time.Hour))
	require.NoError(t, err)
}

func byCategory(scores []agreement.CategoryScore) map[string]agreement.CategoryScore {
	out := map[string]agreement.CategoryScore{}
	for _, s := range scores {
		out[s.Category] = s
	}
	return out
}

func TestScoreCountsDecisionsAgreementAndDisagreements(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	for i := 0; i < 2; i++ {
		_, err := r.pending.Approve(r.ctx, r.approver, r.assess(t, "priority", steward.VerdictWouldVeto, epoch.Add(time.Duration(i)*time.Minute)).ID)
		require.NoError(t, err)
	}
	rejected := r.assess(t, "priority", steward.VerdictWouldVeto, epoch.Add(2*time.Minute))
	_, err := r.pending.Reject(r.ctx, r.approver, rejected.ID, "wrong priority")
	require.NoError(t, err)
	vetoed := r.assess(t, "priority", steward.VerdictWouldVeto, epoch.Add(3*time.Minute))
	r.mark(t, vetoed, "vetoed_at")
	undone := r.assess(t, "priority", steward.VerdictWouldAuto, epoch.Add(4*time.Minute))
	_, err = r.pending.Approve(r.ctx, r.approver, undone.ID)
	require.NoError(t, err)
	r.mark(t, undone, "undone_at")
	r.assess(t, "priority", steward.VerdictWouldVeto, epoch.Add(5*time.Minute)) // pending: not a decision
	r.assess(t, "priority", steward.VerdictNeedsHuman, epoch.Add(6*time.Minute))
	other := r.assess(t, "infra_wait", steward.VerdictWouldAuto, epoch)
	_, err = r.pending.Approve(r.ctx, r.approver, other.ID)
	require.NoError(t, err)

	scores, err := r.scorer.Score(r.ctx, agreement.Window{})
	require.NoError(t, err)
	got := byCategory(scores)
	require.Len(t, scores, 2)

	p := got["priority"]
	assert.Equal(t, 5, p.Decisions)
	assert.Equal(t, 2, p.Agreed)
	assert.InDelta(t, 40.0, p.AgreementPct, 0.001)
	require.Len(t, p.Latest, 3)
	assert.Equal(t, "undone", p.Latest[0].How, "newest first, and an undo beats the approval")
	assert.Equal(t, "vetoed", p.Latest[1].How)
	assert.Equal(t, "rejected", p.Latest[2].How)
	assert.Equal(t, "wrong priority", p.Latest[2].OwnerReason)
	assert.Equal(t, "steward says priority", p.Latest[2].StewardReason)
	assert.Equal(t, rejected.ID, p.Latest[2].PendingActionID)
	assert.NotNil(t, p.Latest[2].TicketID)

	assert.Equal(t, 1, got["infra_wait"].Decisions)
	assert.InDelta(t, 100.0, got["infra_wait"].AgreementPct, 0.001)
	assert.Empty(t, got["infra_wait"].Latest)
}

func TestOwnerReasonDoesNotChangeTheCount(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	for _, reason := range []string{"", "a long, careful, persuasive reason"} {
		_, err := r.pending.Reject(r.ctx, r.approver, r.assess(t, "priority", steward.VerdictWouldVeto, epoch).ID, reason)
		require.NoError(t, err)
	}
	scores, err := r.scorer.Score(r.ctx, agreement.Window{})
	require.NoError(t, err)
	require.Len(t, scores, 1)
	assert.Equal(t, 2, scores[0].Decisions)
	assert.Equal(t, 0, scores[0].Agreed)
}

func TestScoreHonoursTheWindowAndTheTenant(t *testing.T) {
	database := dbtest.WithDB(t)
	mine, theirs := newRig(t, database), newRig(t, database)
	_, err := mine.pending.Approve(mine.ctx, mine.approver, mine.assess(t, "priority", steward.VerdictWouldVeto, epoch).ID)
	require.NoError(t, err)
	_, err = theirs.pending.Reject(theirs.ctx, theirs.approver, theirs.assess(t, "priority", steward.VerdictWouldVeto, epoch).ID, "")
	require.NoError(t, err)

	scores, err := mine.scorer.Score(mine.ctx, agreement.Window{})
	require.NoError(t, err)
	require.Len(t, scores, 1)
	assert.Equal(t, 1, scores[0].Decisions)
	assert.Equal(t, 1, scores[0].Agreed)

	scores, err = mine.scorer.Score(mine.ctx, agreement.Window{Since: epoch.Add(time.Hour)})
	require.NoError(t, err)
	assert.Empty(t, scores)
}

func TestScoreRefusesAWindowBeyondTheScanLimit(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	r.assess(t, "priority", steward.VerdictWouldVeto, epoch)
	r.assess(t, "priority", steward.VerdictWouldVeto, epoch)

	_, err := r.scorer.WithScanLimit(1).Score(r.ctx, agreement.Window{})
	assert.ErrorIs(t, err, agreement.ErrWindowTooLarge)
	_, err = r.scorer.WithScanLimit(2).Score(r.ctx, agreement.Window{})
	assert.NoError(t, err, "exactly at the limit is fine")
}

func TestWindowIsHalfOpen(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	for _, at := range []time.Time{epoch, epoch.Add(time.Hour)} {
		_, err := r.pending.Reject(r.ctx, r.approver, r.assess(t, "priority", steward.VerdictWouldVeto, at).ID, "")
		require.NoError(t, err)
	}
	decisions := func(w agreement.Window) int {
		scores, err := r.scorer.Score(r.ctx, w)
		require.NoError(t, err)
		if len(scores) == 0 {
			return 0
		}
		return scores[0].Decisions
	}
	assert.Equal(t, 2, decisions(agreement.Window{Since: epoch}), "Since is inclusive")
	assert.Equal(t, 1, decisions(agreement.Window{Until: epoch.Add(time.Hour)}), "Until is exclusive")
}

func TestLatestDisagreementsAreCappedAndOrderedByWhenTheOwnerWentAgainstThem(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	// The oldest assessment is the one undone last.
	oldest := r.assess(t, "priority", steward.VerdictWouldAuto, epoch)
	r.mark(t, oldest, "undone_at")
	for i := 1; i <= agreement.MaxDisagreements+1; i++ {
		_, err := r.pending.Reject(r.ctx, r.approver, r.assess(t, "priority", steward.VerdictWouldVeto, epoch.Add(time.Duration(i)*time.Minute)).ID, "")
		require.NoError(t, err)
	}
	scores, err := r.scorer.Score(r.ctx, agreement.Window{})
	require.NoError(t, err)
	require.Len(t, scores, 1)
	assert.Equal(t, agreement.MaxDisagreements+2, scores[0].Decisions)
	require.Len(t, scores[0].Latest, agreement.MaxDisagreements)
	assert.Equal(t, oldest.ID, scores[0].Latest[0].PendingActionID, "the undo (an hour from now) is the latest")
}
