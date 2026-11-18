package change_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
)

func (f *fixture) heldPaths(h *holder) []string {
	f.t.Helper()
	got, err := f.tickets.Get(f.ctx, h.ticket.ID)
	require.NoError(f.t, err)
	var out []string
	for _, p := range got.Paths {
		out = append(out, p.Path)
	}
	return out
}

func TestNarrowScopeToChange_KeepsTheFilesTheChangeTouches(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.host.DiffOf = map[string]*codehost.Diff{headA: {Base: "main", BaseSHA: baseOld, MergeBaseSHA: baseOld,
		Files: []codehost.FileDiff{modified("app/server/a.go", hunkOne)}}}
	f.open(h, true)

	res, err := f.svc.NarrowScopeToChange(f.ctx, f.tenant, h.agent)

	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, []string{"repo/app/server/a.go"}, f.heldPaths(h))
}

func TestNarrowScopeToChange_AnUnreadableChangeLeavesTheScopeAlone(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.host.DiffOf = map[string]*codehost.Diff{headA: {Base: "main", BaseSHA: baseOld, MergeBaseSHA: baseOld,
		Files: []codehost.FileDiff{modified("app/server/a.go", hunkOne)}}}
	f.open(h, true)
	f.host.DiffErr = errors.New("host down")

	_, err := f.svc.NarrowScopeToChange(f.ctx, f.tenant, h.agent)

	require.Error(t, err)
	assert.Equal(t, []string{"repo/app/server/"}, f.heldPaths(h))
}

func TestNarrowScopeToChange_NoChangeNarrowsNothing(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)

	res, err := f.svc.NarrowScopeToChange(f.ctx, f.tenant, h.agent)

	require.NoError(t, err)
	assert.Nil(t, res)
	assert.Equal(t, []string{"repo/app/server/"}, f.heldPaths(h))
}

func TestNarrowScopeToChange_ADiffForAnotherBaseLeavesTheScopeAlone(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.host.DiffOf = map[string]*codehost.Diff{headA: {Base: "main", BaseSHA: baseOld, MergeBaseSHA: baseOld,
		Files: []codehost.FileDiff{modified("app/server/a.go", hunkOne)}}}
	f.open(h, true)
	f.host.DiffOf[headA] = &codehost.Diff{Base: "other", Files: []codehost.FileDiff{modified("app/server/a.go", hunkOne)}}

	_, err := f.svc.NarrowScopeToChange(f.ctx, f.tenant, h.agent)

	require.Error(t, err)
	assert.Equal(t, []string{"repo/app/server/"}, f.heldPaths(h))
}

func TestNarrowScopeToChange_ARenameKeepsItsSourceButACopyDoesNot(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	renamed := codehost.FileDiff{Path: "app/server/new.go", PreviousPath: "app/server/old.go", Status: codehost.DiffRenamed}
	copied := codehost.FileDiff{Path: "app/server/copy.go", PreviousPath: "app/server/src.go", Status: codehost.DiffCopied}
	f.host.DiffOf = map[string]*codehost.Diff{headA: {Base: "main", BaseSHA: baseOld, MergeBaseSHA: baseOld,
		Files: []codehost.FileDiff{renamed, copied}}}
	f.open(h, true)

	_, err := f.svc.NarrowScopeToChange(f.ctx, f.tenant, h.agent)

	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"repo/app/server/new.go", "repo/app/server/old.go", "repo/app/server/copy.go"}, f.heldPaths(h))
}
