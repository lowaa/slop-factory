package change_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func (f *fixture) openInput(h *holder) *change.OpenInput {
	return &change.OpenInput{Branch: f.branchOf(h), Title: "T", Draft: true}
}

func TestShadowBase_OpenRefusesAHeadThatTookInMainAndOpensNothing(t *testing.T) {
	f := newFixture(t)
	first := f.hold(false)
	second := f.shadowOf(first)
	f.host.TookInMain = map[string]bool{f.branchOf(second): true}

	_, err := f.svc.Open(f.ctx, f.tenant, second.agent, f.openInput(second))

	require.ErrorIs(t, err, change.ErrTookInMain)
	assert.Contains(t, err.Error(), "git switch -c "+f.branchOf(second)+"-fix "+baseOld)
	assert.Contains(t, err.Error(), "git push --force-with-lease origin HEAD:"+f.branchOf(second))
	assert.Contains(t, err.Error(), ticket.UnpromotedBase(first.ticket.ID))
	assert.Empty(t, f.host.Opens())
	assert.Empty(t, f.host.BranchCreates())
	calls := f.host.TakesCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, "main", calls[0].Branch)
	assert.Equal(t, baseOld, calls[0].Since, "main is compared against the ticket's pinned base commit")
}

func TestShadowBase_OpenAcceptsAHeadBuiltOnTheBaseIncludingOneThatMergedTheBaseBranch(t *testing.T) {
	f := newFixture(t)
	first := f.hold(false)
	second := f.shadowOf(first)
	// The host's answer for a head that merged the ticket's base branch is the
	// same as for one built on the base commit: nothing of main past it.
	f.host.TookInMain = map[string]bool{f.branchOf(second): false}

	opened := f.openAs(second, 8, true)

	assert.False(t, opened.Existing)
	assert.Len(t, f.host.Opens(), 1)
}

func TestShadowBase_ThePromotedAttemptIsNotChecked(t *testing.T) {
	f := newFixture(t)
	first := f.hold(false)
	f.host.TookInMain = map[string]bool{f.branchOf(first): true}

	f.openAs(first, 7, false)

	assert.Empty(t, f.host.TakesCalls())
	assert.Len(t, f.host.Opens(), 1)
}

func TestShadowBase_AHostThatCannotAnswerIsARetryableRefusal(t *testing.T) {
	f := newFixture(t)
	first := f.hold(false)
	second := f.shadowOf(first)
	f.host.TakesErr = codehost.ErrUnreachable

	_, err := f.svc.Open(f.ctx, f.tenant, second.agent, f.openInput(second))

	require.ErrorIs(t, err, change.ErrBaseUnverified)
	require.ErrorIs(t, err, codehost.ErrUnreachable)
	assert.Contains(t, err.Error(), "call again")
	assert.Empty(t, f.host.Opens())

	f.host.TakesErr = nil
	f.openAs(second, 8, true)
	assert.Len(t, f.host.Opens(), 1, "the same call succeeds once the host answers")
}

func TestShadowBase_ReopeningAnExistingDraftIsCheckedToo(t *testing.T) {
	f := newFixture(t)
	first := f.hold(false)
	second := f.shadowOf(first)
	f.openAs(second, 8, true)
	f.host.TookInMain = map[string]bool{f.branchOf(second): true}

	_, err := f.svc.Open(f.ctx, f.tenant, second.agent, f.openInput(second))

	require.ErrorIs(t, err, change.ErrTookInMain)
}

func (f *fixture) reviewShadow(h *holder) error {
	_, err := f.svc.Review(f.ctx, f.tenant, h.agent, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: pr2, Handoff: "ready"})
	return err
}

func TestShadowBase_ReviewRefusesAPushThatTookInMainAfterOpenAtTheReviewedCommit(t *testing.T) {
	f := newFixture(t)
	first := f.hold(true)
	second := f.shadowOf(first)
	f.openAs(second, 8, true)
	base := ticket.UnpromotedBase(first.ticket.ID)
	f.host.DiffOf = map[string]*codehost.Diff{headA: {Base: base, BaseSHA: baseOld, MergeBaseSHA: baseOld, Files: []codehost.FileDiff{modified("a.go", hunkOne)}}}
	// Clean when opened; the push after it took main in. The check names the
	// commit the human is pointed at, not the branch.
	f.host.TookInMain = map[string]bool{headA: true}

	err := f.reviewShadow(second)

	require.ErrorIs(t, err, change.ErrTookInMain)
	assert.Empty(t, f.currentOf(first, 2).ReviewedHead, "nothing is recorded as handed over")
	calls := f.host.TakesCalls()
	require.NotEmpty(t, calls)
	assert.Equal(t, headA, calls[len(calls)-1].Head)
}

func TestShadowBase_ReviewIsRetryableWhenTheHostCannotAnswerAndPassesWhenClean(t *testing.T) {
	f := newFixture(t)
	first := f.hold(true)
	second := f.shadowOf(first)
	f.openAs(second, 8, true)
	base := ticket.UnpromotedBase(first.ticket.ID)
	f.host.DiffOf = map[string]*codehost.Diff{headA: {Base: base, BaseSHA: baseOld, MergeBaseSHA: baseOld, Files: []codehost.FileDiff{modified("a.go", hunkOne)}}}
	f.host.TakesErr = codehost.ErrUnreachable

	require.ErrorIs(t, f.reviewShadow(second), change.ErrBaseUnverified)

	f.host.TakesErr = nil
	require.NoError(t, f.reviewShadow(second))
	calls := f.host.TakesCalls()
	last := calls[len(calls)-1]
	assert.Equal(t, "main", last.Branch)
	assert.Equal(t, baseOld, last.Since)
	assert.Equal(t, headA, last.Head)
}
