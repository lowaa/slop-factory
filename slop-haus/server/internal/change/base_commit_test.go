package change_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
)

func (f *fixture) baseCommitOf(h *holder) *string {
	f.t.Helper()
	got, err := f.tickets.Get(f.ctx, h.ticket.ID)
	require.NoError(f.t, err)
	return got.BaseCommit
}

func TestOpen_RecordsTheMergeBaseOnTheTicketAtItsFirstOpen(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.host.DiffOf = map[string]*codehost.Diff{headA: {Base: "main", BaseSHA: baseNew, MergeBaseSHA: baseOld}}

	f.open(h, true)

	require.NotNil(t, f.baseCommitOf(h))
	assert.Equal(t, baseOld, *f.baseCommitOf(h), "the merge base, not the base's tip")
}

func TestOpen_LaterOpensNeverMoveTheBase(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.host.DiffOf = map[string]*codehost.Diff{headA: {Base: "main", BaseSHA: baseOld, MergeBaseSHA: baseOld}}
	f.open(h, true)

	f.host.DiffOf[headA] = &codehost.Diff{Base: "main", BaseSHA: baseNew, MergeBaseSHA: baseNew}
	f.open(h, false)
	f.recordUnderOldName(h)
	f.host.Opened = codehost.ChangeRef{Host: hostName, Owner: "acme", Repo: "app", Number: 8}
	f.setChecks(headA, codehost.Check{Name: "ci/build", State: codehost.CheckSuccess})
	_, err := f.svc.Open(f.ctx, f.tenant, h.agent, &change.OpenInput{Branch: f.branchOf(h), Title: "Again"})
	require.NoError(t, err, "a superseding change")

	assert.Equal(t, baseOld, *f.baseCommitOf(h))
}

func TestOpen_SucceedsWithoutABaseWhenTheHostCannotBeRead(t *testing.T) {
	for name, set := range map[string]func(f *fixture){
		"diff fails":  func(f *fixture) { f.host.DiffErr = errors.New("host down") },
		"checks fail": func(f *fixture) { f.host.ChecksErr = errors.New("host down") },
		"wrong target": func(f *fixture) {
			f.host.DiffOf = map[string]*codehost.Diff{headA: {Base: "other", MergeBaseSHA: baseOld}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			h := f.hold(false)
			f.host.DiffOf = map[string]*codehost.Diff{headA: {Base: "main", BaseSHA: baseOld, MergeBaseSHA: baseOld}}
			set(f)

			opened := f.open(h, true)

			assert.Equal(t, "https://git.example/acme/app/pull/7", opened.URL)
			assert.Nil(t, f.baseCommitOf(h))
		})
	}
}

func TestOpen_TheBaseSurvivesTheWorkerMergingMainAndTheMerge(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.host.DiffOf = map[string]*codehost.Diff{headA: {Base: "main", BaseSHA: baseOld, MergeBaseSHA: baseOld}}
	f.open(h, false)

	// Main is merged into the branch: the merge base is now main's tip.
	f.setChecks(headB, codehost.Check{Name: "ci/build", State: codehost.CheckSuccess})
	f.host.DiffOf[headB] = &codehost.Diff{Base: "main", BaseSHA: baseNew, MergeBaseSHA: baseNew}
	f.lock(h)
	_, err := f.merge(h, headB)
	require.NoError(t, err)

	assert.Equal(t, baseOld, *f.baseCommitOf(h))
	assert.Len(t, f.host.Diffed(), 1, "the merge base is read once, at the first open, and not again at merge")
}
