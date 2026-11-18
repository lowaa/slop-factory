package change_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

const prefixed = "[DO NOT MERGE] Add the thing"

var (
	ref7 = codehost.ChangeRef{Host: hostName, Owner: "acme", Repo: "app", Number: 7}
	ref8 = codehost.ChangeRef{Host: hostName, Owner: "acme", Repo: "app", Number: 8}
)

// withTitle is what the host says the change's title is now.
func (f *fixture) withTitle(ref codehost.ChangeRef, title string) {
	f.t.Helper()
	f.host.ChecksOf[ref].Title = title
}

// promotedPair is a ticket whose attempt 1 (promoted) has change #7 against
// the base branch and whose shadow, attempt 2, has change #8 against the
// ticket's own base branch, as the host has them.
func (f *fixture) promotedPair() (first, second *holder) {
	f.t.Helper()
	first = f.hold(false)
	second = f.shadowOf(first)
	f.openAs(first, 7, true)
	f.openAs(second, 8, true)
	f.withTitle(ref7, "Add the thing")
	f.withTitle(ref8, prefixed)
	return first, second
}

func (f *fixture) promoteAttempt(h *holder, n int) *ticket.AttemptPromoted {
	f.t.Helper()
	promoted, err := f.tickets.PromoteAttempt(f.ctx, f.approver, h.ticket.ID, n)
	require.NoError(f.t, err)
	return promoted
}

func TestOpen_AnUnpromotedAttemptOfAChosenTicketIsMarkedDoNotMerge(t *testing.T) {
	f := newFixture(t)
	first := f.hold(false)
	second := f.shadowOf(first)

	f.openAs(first, 7, true)
	f.openAs(second, 8, true)

	opens := f.host.Opens()
	require.Len(t, opens, 2)
	assert.Equal(t, "Add the thing", opens[0].Request.Title, "the promoted attempt's title is the agent's")
	assert.Equal(t, "main", opens[0].Request.Base)
	assert.Equal(t, prefixed, opens[1].Request.Title)
	assert.Equal(t, ticket.UnpromotedBase(first.ticket.ID), opens[1].Request.Base)
}

func TestOpen_ThePrefixIsNeverDoubled(t *testing.T) {
	f := newFixture(t)
	first := f.hold(false)
	second := f.shadowOf(first)

	_, err := f.svc.Open(f.ctx, f.tenant, second.agent, &change.OpenInput{Branch: f.branchOf(second), Title: prefixed, Draft: true})
	require.NoError(t, err)

	assert.Equal(t, prefixed, f.host.Opens()[0].Request.Title)
}

func TestOpen_AnAttemptOfATicketWithNoChoiceIsNotMarked(t *testing.T) {
	f := newFixture(t)
	first := f.hold(false)
	second := f.shadowOf(first)
	_, err := f.database.Exec(f.ctx, `UPDATE attempts SET promoted_at = NULL, promoted_by_user_id = NULL WHERE ticket_id = $1`, first.ticket.ID)
	require.NoError(t, err)

	f.openAs(second, 8, true)

	assert.Equal(t, "Add the thing", f.host.Opens()[0].Request.Title, "nothing is promoted yet, so nothing says do not merge")
}

func TestApplyPromotion_ADemotedChangeGoesBackToDraft(t *testing.T) {
	f := newFixture(t)
	first, _ := f.promotedPair()

	f.promoteAttempt(first, 2)
	require.NoError(t, f.svc.ApplyPromotion(f.ctx, f.tenant, first.ticket.ID))

	assert.Equal(t, []codehosttest.OperationCall{{Tenant: f.tenant, Change: ref7}}, f.host.Drafted(), "only the change the ticket chose against")
}

func TestApplyPromotion_RetargetsAndRetitlesBothWays(t *testing.T) {
	f := newFixture(t)
	first, _ := f.promotedPair()
	base := ticket.UnpromotedBase(first.ticket.ID)

	f.promoteAttempt(first, 2)
	require.NoError(t, f.svc.ApplyPromotion(f.ctx, f.tenant, first.ticket.ID))

	assert.ElementsMatch(t, []codehosttest.RetargetCall{
		{Tenant: f.tenant, Change: ref8, Base: "main"},
		{Tenant: f.tenant, Change: ref7, Base: base},
	}, f.host.Retargeted())
	assert.ElementsMatch(t, []codehosttest.RetitleCall{
		{Tenant: f.tenant, Change: ref8, Title: "Add the thing"},
		{Tenant: f.tenant, Change: ref7, Title: prefixed},
	}, f.host.Retitled())
	assert.Equal(t, "main", f.currentOf(first, 2).Base, "the record follows the host, or the merge gate would refuse the promoted change")
	assert.Equal(t, base, f.currentOf(first, 1).Base)
	assert.Equal(t, 2, f.promotedNumber(first))
}

// A request that finishes late applies the promotion as it is now, not the
// one it made.
func TestApplyPromotion_WorksFromTheCurrentChoiceNotTheRequestsOwn(t *testing.T) {
	f := newFixture(t)
	first, _ := f.promotedPair()
	f.promoteAttempt(first, 2)
	f.promoteAttempt(first, 1)

	require.NoError(t, f.svc.ApplyPromotion(f.ctx, f.tenant, first.ticket.ID))

	assert.Equal(t, "main", f.currentOf(first, 1).Base)
	assert.Equal(t, ticket.UnpromotedBase(first.ticket.ID), f.currentOf(first, 2).Base)
	assert.Contains(t, f.host.Retargeted(), codehosttest.RetargetCall{Tenant: f.tenant, Change: ref7, Base: "main"})
}

func TestApplyPromotion_ARepeatChangesNothingAndNeverDoublesAPrefix(t *testing.T) {
	f := newFixture(t)
	first, _ := f.promotedPair()
	f.promoteAttempt(first, 2)
	require.NoError(t, f.svc.ApplyPromotion(f.ctx, f.tenant, first.ticket.ID))
	// The host now shows what the first pass made it.
	f.withTitle(ref8, "Add the thing")
	f.withTitle(ref7, prefixed)
	retitles := len(f.host.Retitled())

	require.NoError(t, f.svc.ApplyPromotion(f.ctx, f.tenant, first.ticket.ID))

	assert.Len(t, f.host.Retitled(), retitles, "titles already right are not written again")
}

func TestApplyPromotion_APromotedAttemptWithNoChangeAndAMergedOneAreLeftAlone(t *testing.T) {
	f := newFixture(t)
	first := f.hold(false)
	second := f.shadowOf(first)
	f.openAs(first, 7, true)
	f.withTitle(ref7, "Add the thing")
	_, err := f.database.Exec(f.ctx, `UPDATE ticket_changes SET merged_at = now(), merge_sha = 'abc', merged_by_agent_id = $2 WHERE ticket_id = $1`, first.ticket.ID, first.agent)
	require.NoError(t, err)
	_ = second

	require.NoError(t, f.svc.ApplyPromotion(f.ctx, f.tenant, first.ticket.ID))

	assert.Empty(t, f.host.Retargeted(), "attempt 2 has no change and attempt 1's merged")
	assert.Empty(t, f.host.Retitled())
}

func TestApplyPromotion_AHostFailureIsAuditedAndTheOthersAreStillDone(t *testing.T) {
	f := newFixture(t)
	first, _ := f.promotedPair()
	f.promoteAttempt(first, 2)
	f.host.RetargetErr = errors.New("host down")

	err := f.svc.ApplyPromotion(f.ctx, f.tenant, first.ticket.ID)

	require.Error(t, err)
	assert.Len(t, f.host.Retargeted(), 2, "a failure on one does not stop the other")
	failed := f.log.audited(t, audit.ChangeOperationFailed)
	require.Len(t, failed, 2)
	assert.Equal(t, "retarget_change", failed[0]["operation"])
	assert.Contains(t, failed[0]["err"], "host down")
	assert.Equal(t, "main", f.currentOf(first, 1).Base, "unchanged: the record follows the host")
	assert.Equal(t, ticket.UnpromotedBase(first.ticket.ID), f.currentOf(first, 2).Base)
	assert.Empty(t, f.host.Retitled(), "a change not retargeted is not retitled")
	assert.Equal(t, 2, f.promotedNumber(first), "the promotion stands")
}

func (f *fixture) promotedNumber(h *holder) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.database.QueryRow(f.ctx, `SELECT number FROM attempts WHERE ticket_id = $1 AND promoted_at IS NOT NULL`, h.ticket.ID).Scan(&n))
	return n
}

func TestApplyPromotion_ATenantMismatchIsRefused(t *testing.T) {
	f := newFixture(t)
	first, _ := f.promotedPair()
	f.promoteAttempt(first, 2)

	require.Error(t, f.svc.ApplyPromotion(f.ctx, [16]byte{1}, first.ticket.ID))
	assert.Empty(t, f.host.Retargeted())
}

func TestCheckPromotable(t *testing.T) {
	t.Run("a head that took in main is refused", func(t *testing.T) {
		f := newFixture(t)
		first, second := f.promotedPair()
		f.host.TookInMain = map[string]bool{headA: true}

		err := f.svc.CheckPromotable(f.ctx, f.tenant, first.ticket.ID, second.attempt)

		require.ErrorIs(t, err, ticket.ErrPromotionBlocked)
		require.ErrorIs(t, err, change.ErrTookInMain)
		calls := f.host.TakesCalls()
		require.Len(t, calls, 2, "one at change_open, one now")
		assert.Equal(t, headA, calls[len(calls)-1].Head, "the head the host reports now, not the branch name")
	})
	t.Run("a host that cannot say is not a pass", func(t *testing.T) {
		f := newFixture(t)
		first, second := f.promotedPair()
		f.host.TakesErr = errors.New("host down")

		err := f.svc.CheckPromotable(f.ctx, f.tenant, first.ticket.ID, second.attempt)

		require.ErrorIs(t, err, ticket.ErrPromotionUnverified)
	})
	t.Run("a clean head, no change and an already chosen change pass without a host call", func(t *testing.T) {
		f := newFixture(t)
		first, second := f.promotedPair()
		require.NoError(t, f.svc.CheckPromotable(f.ctx, f.tenant, first.ticket.ID, second.attempt))
		calls := len(f.host.TakesCalls())
		require.NoError(t, f.svc.CheckPromotable(f.ctx, f.tenant, first.ticket.ID, first.attempt), "targets the base branch already")
		require.NoError(t, f.svc.CheckPromotable(f.ctx, f.tenant, first.ticket.ID, 9), "no change")
		assert.Len(t, f.host.TakesCalls(), calls)
	})
}

func TestPromoteAttempt_RefusedWhileThePromotedAttemptsChangeIsBeingMerged(t *testing.T) {
	f := newFixture(t)
	first, _ := f.promotedPair()
	claim, err := f.store.BeginMerge(f.ctx, f.currentOf(first, 1).ID)
	require.NoError(t, err)
	require.NotNil(t, claim)

	_, err = f.tickets.PromoteAttempt(f.ctx, f.approver, first.ticket.ID, 2)

	require.ErrorIs(t, err, ticket.ErrPromoteMerging)
	assert.Equal(t, 1, f.promotedNumber(first))
}
