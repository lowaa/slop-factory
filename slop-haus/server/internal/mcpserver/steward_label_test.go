package mcpserver_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionanswer"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionlabel"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// fixedRounds is the round as the board reads it: the ticket's own revision
// and a head the test can move.
type fixedRounds struct {
	tickets *ticket.Service
	head    string
}

func (f *fixedRounds) Round(ctx context.Context, ticketID int64, attempt, round int) (*discussionanswer.Open, error) {
	t, err := f.tickets.Get(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	return &discussionanswer.Open{Attempt: attempt, Round: round, Revision: t.Revision, HeadSHA: f.head, Question: "merge?"}, nil
}

// queued is a ticket with a pending label for attempt 1, round 1.
func (r *realRig) queued(t *testing.T) *ticket.Ticket {
	t.Helper()
	tk := r.forceStatus(ticket.StatusDiscussion)
	open, err := r.rounds.Round(r.ctx, tk.ID, 1, 1)
	require.NoError(t, err)
	rec, err := r.labels.Queue(r.ctx, &discussionlabel.Question{
		Target:   discussionlabel.Target{TicketID: tk.ID, Attempt: 1, Round: 1, Revision: open.Revision, HeadSHA: open.HeadSHA},
		Question: open.Question,
	})
	require.NoError(t, err)
	require.Equal(t, discussionlabel.LabelPending, rec.Label)
	return tk
}

func (r *realRig) labelOf(t *testing.T, ticketID int64) *discussionlabel.Record {
	t.Helper()
	rec, err := discussionlabel.NewStore(r.database).Get(r.ctx, ticketID, 1, 1)
	require.NoError(t, err)
	return rec
}

func labelArgs(ticketID int64, label, category string) map[string]any {
	return map[string]any{"ticket_id": ticketID, "attempt": 1, "round": 1, "label": label, "category": category, "reason": "because"}
}

func TestStewardLabel_RefusesAnyoneButTheSteward(t *testing.T) {
	r := newAssistRig(t, steward.LevelShadow)
	tk := r.queued(t)

	for name, s := range map[string]*consoleSession{"console": r.agentSession("console", true), "worker": r.agentSession("worker", false)} {
		res := s.call(t, "steward_label_discussion", labelArgs(tk.ID, "yours", ""))
		assert.True(t, res.IsError, name)
		assert.Contains(t, resultText(t, res), "only the steward", name)
	}
	assert.Equal(t, discussionlabel.LabelPending, r.labelOf(t, tk.ID).Label)
}

func TestStewardLabel_YoursCompletesThePendingRoundAndARepeatKeepsIt(t *testing.T) {
	r := newAssistRig(t, steward.LevelShadow)
	tk := r.queued(t)
	s := r.stewardSession("steward")

	res := s.call(t, "steward_label_discussion", labelArgs(tk.ID, "yours", ""))
	require.False(t, res.IsError, resultText(t, res))
	assert.Equal(t, discussionlabel.LabelYours, r.labelOf(t, tk.ID).Label)

	again := s.call(t, "steward_label_discussion", labelArgs(tk.ID, "routine", "merge_continue"))
	require.False(t, again.IsError, resultText(t, again))
	assert.Equal(t, discussionlabel.LabelYours, r.labelOf(t, tk.ID).Label, "a repeat does not relabel")
}

func TestStewardLabel_ARoutineClaimNeedsTheServersChecks(t *testing.T) {
	r := newAssistRig(t, steward.LevelShadow)
	tk := r.queued(t)

	res := newAssistSteward(r).call(t, "steward_label_discussion", labelArgs(tk.ID, "routine", "merge_continue"))

	require.False(t, res.IsError, resultText(t, res))
	got := r.labelOf(t, tk.ID)
	assert.Equal(t, discussionlabel.LabelYours, got.Label, "the rig has no CI to show green, so the checks cannot pass")
	assert.Contains(t, got.Reason, "checks failed")
}

func newAssistSteward(r *realRig) *consoleSession { return r.stewardSession("steward") }

func TestStewardLabel_AMovedRoundIsRefused(t *testing.T) {
	r := newAssistRig(t, steward.LevelShadow)
	tk := r.queued(t)
	r.rounds.head = "head-2"

	res := r.stewardSession("steward").call(t, "steward_label_discussion", labelArgs(tk.ID, "yours", ""))

	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "moved")
	assert.Equal(t, discussionlabel.LabelPending, r.labelOf(t, tk.ID).Label)
}

func TestStewardLabel_RejectsALabelThatIsNeitherRoutineNorYours(t *testing.T) {
	r := newAssistRig(t, steward.LevelShadow)
	tk := r.queued(t)

	res := r.stewardSession("steward").call(t, "steward_label_discussion", labelArgs(tk.ID, "not_assessed", ""))

	assert.True(t, res.IsError)
	assert.Equal(t, discussionlabel.LabelPending, r.labelOf(t, tk.ID).Label)
}

func TestStewardLabel_ATenantCannotLabelAnotherTenantsRound(t *testing.T) {
	database := newAssistRig(t, steward.LevelShadow).database
	mine := newRealRigWith(t, database, &realRigOptions{steward: &steward.Config{Level: steward.LevelShadow, Mode: steward.ModeShadow, DailyCap: 10}})
	theirs := newRealRigWith(t, database, &realRigOptions{steward: &steward.Config{Level: steward.LevelShadow, Mode: steward.ModeShadow, DailyCap: 10}})
	tk := theirs.queued(t)

	res := mine.stewardSession("steward").call(t, "steward_label_discussion", labelArgs(tk.ID, "yours", ""))

	assert.True(t, res.IsError)
	ctx := db.WithTenant(context.Background(), theirs.tenantID)
	rec, err := discussionlabel.NewStore(database).Get(ctx, tk.ID, 1, 1)
	require.NoError(t, err)
	assert.Equal(t, discussionlabel.LabelPending, rec.Label)
}

func TestStewardLabel_Level1AppliesNothing(t *testing.T) {
	r := newAssistRig(t, steward.LevelShadow)
	tk := r.queued(t)

	res := r.stewardSession("steward").call(t, "steward_label_discussion", labelArgs(tk.ID, "yours", ""))

	require.False(t, res.IsError, resultText(t, res))
	var comments, decisions int
	require.NoError(t, r.database.QueryRow(r.ctx, `SELECT count(*) FROM ticket_comments WHERE ticket_id = $1`, tk.ID).Scan(&comments))
	require.NoError(t, r.database.QueryRow(r.ctx, `SELECT count(*) FROM steward_discussion_decisions`).Scan(&decisions))
	assert.Zero(t, comments)
	assert.Zero(t, decisions)
}
