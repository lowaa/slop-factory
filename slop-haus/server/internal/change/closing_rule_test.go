package change_test

import (
	"errors"
	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/jobs"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// ruleFixture is a ticket whose promoted attempt (1, change #7) and unpromoted
// sibling (2, change #8) both have changes, with the closing rule wired to the
// ticket transitions as the server does.
type ruleFixture struct {
	*fixture
	promoted, sibling *holder
	base              string
}

func newRuleFixture(t *testing.T) *ruleFixture {
	t.Helper()
	f := newFixture(t)
	f.settled = f.svc.TicketsSettled
	r := &ruleFixture{fixture: f, promoted: f.hold(false)}
	r.sibling = f.addSibling(r.promoted)
	r.base = ticket.UnpromotedBase(r.promoted.ticket.ID)
	f.host.BranchAt = map[codehosttest.BranchKey]string{{Repo: appRepo, Branch: r.base}: baseOld}
	f.openAs(r.promoted, 7, false)
	f.openAs(r.sibling, 8, true)
	return r
}

// finishSibling is the sibling agent's ticket_finish.
func (r *ruleFixture) finishSibling(h *holder) {
	r.t.Helper()
	_, err := r.svc.Review(r.ctx, r.tenant, h.agent, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: pr2, Handoff: "done"})
	require.NoError(r.t, err)
}

// finishTicket is the promoted agent's ticket_finish, after its merge.
func (r *ruleFixture) finishTicket() {
	r.t.Helper()
	_, err := r.tickets.Done(r.ctx, r.promoted.agent, &ticket.DoneParams{Summary: "merged", PRURL: pr1})
	require.NoError(r.t, err)
}

func (r *ruleFixture) closedNumbers() []int64 {
	var out []int64
	for _, c := range r.host.Closed() {
		out = append(out, c.Change.Number)
	}
	return out
}

func (r *ruleFixture) deleted() bool { return len(r.host.BranchDeletes()) > 0 }

func (r *ruleFixture) closedReason(attempt int) string {
	r.t.Helper()
	return r.currentOf(r.promoted, attempt).ClosedReason
}

func TestClosingRule_PromotedMergesThenSiblingFinishes(t *testing.T) {
	r := newRuleFixture(t)
	r.finishTicket()
	assert.Empty(t, r.closedNumbers(), "the sibling is still running: it keeps its change")
	assert.False(t, r.deleted())

	r.finishSibling(r.sibling)

	assert.Equal(t, []int64{8}, r.closedNumbers())
	assert.Equal(t, "closing_rule", r.closedReason(2))
	assert.Equal(t, []codehosttest.BranchKey{{Repo: appRepo, Branch: r.base}}, r.host.BranchDeletes())
	require.Len(t, r.log.audited(t, audit.ChangeClosedByRule), 1)
	require.Len(t, r.log.audited(t, audit.BaseBranchDeleted), 1)
}

func TestClosingRule_SiblingFinishesThenPromotedMerges(t *testing.T) {
	r := newRuleFixture(t)
	r.finishSibling(r.sibling)
	assert.Empty(t, r.closedNumbers(), "the ticket is not done: the experiment may still be promoted")

	r.finishTicket()

	assert.Equal(t, []int64{8}, r.closedNumbers())
	assert.True(t, r.deleted())
}

func TestClosingRule_SiblingClosedByAPersonBeforeAndAfterTheMerge(t *testing.T) {
	t.Run("before", func(t *testing.T) {
		r := newRuleFixture(t)
		_, err := r.tickets.CloseAttempt(r.ctx, r.approver, r.promoted.ticket.ID, 2)
		require.NoError(t, err)
		assert.Empty(t, r.closedNumbers(), "the person's close is the attempt service's to carry out, not the rule's")

		r.finishTicket()

		assert.Equal(t, []int64{8}, r.closedNumbers())
		assert.True(t, r.deleted())
	})
	t.Run("after", func(t *testing.T) {
		r := newRuleFixture(t)
		r.finishTicket()
		_, err := r.tickets.CloseAttempt(r.ctx, r.approver, r.promoted.ticket.ID, 2)
		require.NoError(t, err)

		assert.Equal(t, []int64{8}, r.closedNumbers())
		assert.True(t, r.deleted())
	})
}

func TestClosingRule_ARunningSiblingKeepsItsChange(t *testing.T) {
	r := newRuleFixture(t)
	r.finishTicket()

	_, err := r.svc.ApplyClosingRule(r.ctx, r.tenant, 0)
	require.NoError(t, err)

	assert.Empty(t, r.closedNumbers())
	assert.Empty(t, r.currentOf(r.promoted, 2).ClosedReason)
	assert.False(t, r.deleted(), "the sibling's catch-up merges the base branch")
}

func TestClosingRule_ThePromotedChangeIsNeverClosed(t *testing.T) {
	r := newRuleFixture(t)
	r.finishSibling(r.sibling)
	r.finishTicket()

	assert.NotContains(t, r.closedNumbers(), int64(7))
	assert.Nil(t, r.currentOf(r.promoted, 1).ClosedAt)
}

func TestClosingRule_ChangeOpenRacingTheMergeClosesWhatItOpened(t *testing.T) {
	f := newFixture(t)
	f.settled = f.svc.TicketsSettled
	promoted := f.hold(false)
	sibling := f.addSibling(promoted)
	f.host.BranchAt = map[codehosttest.BranchKey]string{}
	// Between change_open reading its gate and recording the change, the ticket
	// is merged and the attempt finishes.
	f.host.BeforeBranch = func() {
		_, err := f.database.Exec(f.ctx, `UPDATE attempts SET status = 'review' WHERE ticket_id = $1 AND number = 2`, promoted.ticket.ID)
		require.NoError(t, err)
		_, err = f.database.Exec(f.ctx, `UPDATE tickets SET status = 'done' WHERE id = $1`, promoted.ticket.ID)
		require.NoError(t, err)
	}

	f.openAs(sibling, 8, true)

	assert.Equal(t, []int64{8}, closedNumbersOf(f.host))
	assert.Equal(t, "closing_rule", f.currentOf(promoted, 2).ClosedReason)
}

func closedNumbersOf(h *codehosttest.Fake) []int64 {
	var out []int64
	for _, c := range h.Closed() {
		out = append(out, c.Change.Number)
	}
	return out
}

func TestClosingRule_ReconcilerClosesWhatAFailedHostCallLeft(t *testing.T) {
	r := newRuleFixture(t)
	r.host.CloseErr = errors.New("host down")
	r.finishSibling(r.sibling)
	r.finishTicket()
	require.Empty(t, r.closedReason(2), "the close failed: left for the reconciler")
	require.Len(t, r.log.audited(t, audit.ChangeOperationFailed), 2, "one for the sibling's finishing, one for the ticket's")
	assert.False(t, r.deleted(), "a change that is still open keeps the base branch")

	r.host.CloseErr = nil
	rec := change.NewReconciler(r.svc, r.store, 0, slog.DiscardHandler)
	require.NoError(t, rec.PassOnce(r.ctx))

	assert.Equal(t, "closing_rule", r.closedReason(2))
	assert.True(t, r.deleted())
}

func TestClosingRule_ConcurrentApplicationsRecordOnce(t *testing.T) {
	r := newRuleFixture(t)
	r.finishSibling(r.sibling)
	r.finishTicket()
	_, err := r.database.Exec(r.ctx, `UPDATE ticket_changes SET closed_at = NULL, closed_reason = NULL, base_deleted_at = NULL WHERE attempt_number = 2`)
	require.NoError(t, err)

	before := len(r.log.audited(t, audit.ChangeClosedByRule))
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			_, err := r.svc.ApplyClosingRule(r.ctx, r.tenant, 0)
			assert.NoError(t, err)
		})
	}
	wg.Wait()

	assert.Equal(t, "closing_rule", r.closedReason(2))
	assert.Len(t, r.log.audited(t, audit.ChangeClosedByRule), before+1, "the race writes one record")
}

func TestClosingRule_BaseBranchOutlivesEveryOpenChangeTargetingIt(t *testing.T) {
	r := newRuleFixture(t)
	third := r.addThird()
	r.finishTicket()
	r.finishSibling(r.sibling)
	assert.Equal(t, []int64{8}, r.closedNumbers())
	assert.False(t, r.deleted(), "the third attempt's change still targets it")

	_, err := r.database.Exec(r.ctx, `UPDATE attempts SET status = 'review' WHERE ticket_id = $1 AND number = $2`, third.ticket.ID, third.attempt)
	require.NoError(t, err)
	assert.False(t, r.deleted())
	_, err = r.svc.ApplyClosingRule(r.ctx, r.tenant, third.ticket.ID)
	require.NoError(t, err)

	assert.ElementsMatch(t, []int64{8, 9}, r.closedNumbers())
	assert.Len(t, r.host.BranchDeletes(), 1)
}

func TestClosingRule_ATenantWithNoExperimentsIsLeftAlone(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.openAs(h, 7, false)
	_, err := f.database.Exec(f.ctx, `UPDATE tickets SET status = 'done' WHERE id = $1`, h.ticket.ID)
	require.NoError(t, err)

	rec := change.NewReconciler(f.svc, f.store, 0, slog.DiscardHandler)
	require.NoError(t, rec.PassOnce(f.ctx))

	assert.Empty(t, f.host.Closed())
	assert.Empty(t, f.host.BranchDeletes())
}

func TestClosingRule_ATenantMismatchIsRefused(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.ApplyClosingRule(f.ctx, uuid.New(), 0)
	require.Error(t, err)
}

// addThird adds a third attempt with its own change (#9).
func (r *ruleFixture) addThird() *holder {
	r.t.Helper()
	third := r.addSibling(r.promoted)
	r.host.BranchAt[codehosttest.BranchKey{Repo: appRepo, Branch: r.base}] = baseOld
	r.expectChange(codehost.ChangeRef{Host: hostName, Owner: "acme", Repo: "app", Number: 9})
	r.openAs(third, 9, true)
	return third
}

func TestClosingRule_BaseBranchIsKeptWhileAnotherChangeStaysOpen(t *testing.T) {
	r := newRuleFixture(t)
	third := r.addThird()
	r.finishTicket()
	_, err := r.database.Exec(r.ctx, `UPDATE attempts SET status = 'review' WHERE ticket_id = $1 AND number IN (2, $2)`, r.promoted.ticket.ID, third.attempt)
	require.NoError(t, err)
	_, err = r.database.Exec(r.ctx, `UPDATE ticket_changes SET closed_at = now(), closed_reason = 'closing_rule' WHERE attempt_number = 2`)
	require.NoError(t, err)
	r.host.CloseErr = errors.New("host down")

	_, err = r.svc.ApplyClosingRule(r.ctx, r.tenant, 0)

	require.Error(t, err)
	assert.False(t, r.deleted(), "#9 is still open and targets the base branch")
}

func TestClosingRule_ABaseBranchDeleteThatFailsIsRetried(t *testing.T) {
	r := newRuleFixture(t)
	r.host.DeleteErr = errors.New("host down")
	r.finishSibling(r.sibling)
	r.finishTicket()
	require.Equal(t, []int64{8}, r.closedNumbers())

	var deletedAt *time.Time
	require.NoError(t, r.database.QueryRow(r.ctx, `SELECT base_deleted_at FROM ticket_changes WHERE attempt_number = 2`).Scan(&deletedAt))
	assert.Nil(t, deletedAt, "a failed delete is not recorded as done")

	r.host.DeleteErr = nil
	_, err := r.svc.ApplyClosingRule(r.ctx, r.tenant, 0)
	require.NoError(t, err)
	require.NoError(t, r.database.QueryRow(r.ctx, `SELECT base_deleted_at FROM ticket_changes WHERE attempt_number = 2`).Scan(&deletedAt))
	assert.NotNil(t, deletedAt)
	assert.Len(t, r.host.BranchDeletes(), 2, "failed once, then done")

	_, err = r.svc.ApplyClosingRule(r.ctx, r.tenant, 0)
	require.NoError(t, err)
	assert.Len(t, r.host.BranchDeletes(), 2, "once recorded, the host is not asked again")
}

func TestReconciler_JobRepairsOnATick(t *testing.T) {
	r := newRuleFixture(t)
	r.host.CloseErr = errors.New("host down")
	r.finishSibling(r.sibling)
	r.finishTicket()
	require.Empty(t, r.closedReason(2))
	r.host.CloseErr = nil

	rec := change.NewReconciler(r.svc, r.store, time.Minute, slog.DiscardHandler)
	clk := clock.NewFake(time.Unix(1_000_000, 0))
	passes := make(chan error, 4)
	sched := jobs.New(&jobs.Options{Clock: clk, AfterRun: func(_ string, err error) { passes <- err }})
	_, err := sched.Start(r.ctx, rec.Job())
	require.NoError(t, err)
	defer sched.Close()

	require.NoError(t, <-passes)
	assert.Equal(t, "closing_rule", r.closedReason(2), "the startup pass repaired it")
	clk.BlockUntil(1)
	clk.Advance(time.Minute)
	require.NoError(t, <-passes)
}

func TestReconciler_ZeroIntervalHasNoJob(t *testing.T) {
	r := newRuleFixture(t)
	assert.Nil(t, change.NewReconciler(r.svc, r.store, 0, slog.DiscardHandler).Job())
}

func TestClosingRule_AFinishedReplayOfAParkedTicketIsClosed(t *testing.T) {
	r := newRuleFixture(t)
	id := r.promoted.ticket.ID
	_, err := r.database.Exec(r.ctx, `UPDATE tickets SET status = 'parked' WHERE id = $1`, id)
	require.NoError(t, err)

	// Not a replay: a parked ticket's other attempts are left alone.
	_, err = r.database.Exec(r.ctx, `UPDATE attempts SET status = 'review' WHERE ticket_id = $1 AND number = 2`, id)
	require.NoError(t, err)
	_, err = r.svc.ApplyClosingRule(r.ctx, r.tenant, id)
	require.NoError(t, err)
	assert.Empty(t, r.closedNumbers())

	_, err = r.database.Exec(r.ctx, `UPDATE attempts SET replay = true WHERE ticket_id = $1 AND number = 2`, id)
	require.NoError(t, err)
	_, err = r.svc.ApplyClosingRule(r.ctx, r.tenant, id)
	require.NoError(t, err)
	assert.Equal(t, []int64{8}, r.closedNumbers())
	assert.False(t, r.deleted(), "a parked ticket keeps its base branch")
}
