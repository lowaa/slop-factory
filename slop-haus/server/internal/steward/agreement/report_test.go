package agreement_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/agreement"
)

func TestTheReportTextNamesCategoriesDisagreementsAndDiscussionRounds(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	rep := agreement.NewReporter(r.scorer)
	empty, err := rep.Text(r.ctx, time.Time{}, time.Time{})
	require.NoError(t, err)
	assert.Empty(t, empty)

	a := r.assess(t, "merge_continue", steward.VerdictWouldAuto, epoch)
	_, err = r.pending.Reject(r.ctx, r.approver, a.ID, "not yet")
	require.NoError(t, err)
	vetoed := r.assess(t, "merge_continue", steward.VerdictWouldVeto, epoch)
	r.mark(t, vetoed, "vetoed_at")

	ok := r.assess(t, "merge_continue", steward.VerdictWouldAuto, epoch)
	_, err = r.pending.Approve(r.ctx, r.approver, ok.ID)
	require.NoError(t, err)
	r.assess(t, "ticket_ready", steward.VerdictWouldVeto, epoch) // never opened: expired

	answered := r.discussionTicket(t)
	r.decide(t, r.label(t, answered, 1, "routine", "merge_continue", epoch), "edit",
		templateFor(t, "merge_continue", answered, "abc"), "go ahead, differently", "", epoch.Add(time.Minute))

	tid := r.discussionTicket(t)
	r.label(t, tid, 1, "routine", "merge_continue", epoch)
	r.ownerComment(t, tid, "no, hold off", epoch.Add(time.Minute))

	text, err := rep.Text(r.ctx, time.Time{}, time.Time{})
	require.NoError(t, err)
	assert.Contains(t, text, "- merge_continue: 3 decided, owner agreed 1 (33%), disagreed 2")
	assert.Contains(t, text, "you said would_auto; the owner rejected: not yet (you gave: steward says merge_continue)")
	assert.Contains(t, text, "you said would_veto; the owner vetoed")
	assert.Contains(t, text, "- ticket_ready: 1 decided, owner agreed 0 (0%), disagreed 1")
	assert.Contains(t, text, "you said would_veto; the owner expired")
	assert.Contains(t, text, "discussion labels, merge_continue: 1 decided, owner agreed 0 (0%)")
	assert.Contains(t, text, "discussion answers, merge_continue: 1 decided, owner agreed 0 (0%)")
	assert.Contains(t, text, "your draft for #")
	assert.Contains(t, text, "answered elsewhere")
}

func TestTheReportTextFailsWhenDiscussionScoringFails(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	r.label(t, r.discussionTicket(t), 1, "routine", "merge_continue", epoch)
	r.label(t, r.discussionTicket(t), 1, "routine", "merge_continue", epoch)
	_, err := agreement.NewReporter(agreement.NewScorer(r.store, r.database).WithScanLimit(1)).Text(r.ctx, time.Time{}, time.Time{})
	assert.ErrorIs(t, err, agreement.ErrWindowTooLarge)
}
