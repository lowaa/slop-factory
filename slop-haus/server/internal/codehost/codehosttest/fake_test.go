package codehosttest_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
)

func TestFake_AnswersWhatItWasGivenAndRecordsTheCalls(t *testing.T) {
	ctx := context.Background()
	tenant := uuid.New()
	ref := codehost.ChangeRef{Host: "git.example.test", Owner: "a", Repo: "b", Number: 4}
	f := &codehosttest.Fake{
		Domain:     "git.example.test",
		Opened:     ref,
		ChecksOf:   map[codehost.ChangeRef]*codehost.Checks{ref: codehost.NewChecks("abc", "main", nil)},
		MergeErr:   codehost.ErrHeadMoved,
		MergedWith: codehost.Merged{SHA: "m"},
	}

	got, err := f.OpenChange(ctx, tenant, &codehost.OpenRequest{Head: "h", Base: "main", Title: "t", Draft: true})
	require.NoError(t, err)
	assert.Equal(t, ref, got)
	assert.Equal(t, []codehosttest.OpenCall{{Tenant: tenant, Request: codehost.OpenRequest{Head: "h", Base: "main", Title: "t", Draft: true}}}, f.Opens())

	checks, err := f.Checks(ctx, tenant, ref)
	require.NoError(t, err)
	assert.Equal(t, codehost.CheckNone, checks.State)
	_, err = f.Checks(ctx, tenant, codehost.ChangeRef{Number: 99})
	assert.ErrorIs(t, err, codehost.ErrChangeNotFound)
	assert.Len(t, f.Checked(), 2)

	_, err = f.Merge(ctx, tenant, &codehost.MergeRequest{Change: ref, HeadSHA: "abc", Subject: "s"})
	assert.ErrorIs(t, err, codehost.ErrHeadMoved)
	require.Len(t, f.Merges(), 1)
	assert.Equal(t, "abc", f.Merges()[0].Merge.HeadSHA, "what the caller asked to merge on is recorded")
	assert.Equal(t, tenant, f.Merges()[0].Tenant)

	f.MergeErr = nil
	merged, err := f.Merge(ctx, tenant, &codehost.MergeRequest{Change: ref, HeadSHA: "abc"})
	require.NoError(t, err)
	assert.Equal(t, "m", merged.SHA)

	f.BranchUpdated = codehost.BranchUpdate{SHA: "u"}
	updated, err := f.UpdateBranch(ctx, tenant, &codehost.UpdateBranchRequest{Change: ref, HeadSHA: "abc"})
	require.NoError(t, err)
	assert.Equal(t, "u", updated.SHA)
	assert.Equal(t, "abc", f.BranchUpdates()[0].Update.HeadSHA)
	f.BranchUpdateErr = codehost.ErrMergeConflict
	_, err = f.UpdateBranch(ctx, tenant, &codehost.UpdateBranchRequest{Change: ref, HeadSHA: "abc"})
	assert.ErrorIs(t, err, codehost.ErrMergeConflict)

	require.NoError(t, f.MarkReady(ctx, tenant, ref))
	assert.Equal(t, ref, f.Readied()[0].Change)

	require.NoError(t, f.CloseChange(ctx, tenant, ref))
	f.CloseErr = codehost.ErrAlreadyMerged
	assert.ErrorIs(t, f.CloseChange(ctx, tenant, ref), codehost.ErrAlreadyMerged)
	require.Len(t, f.Closed(), 2)
	assert.Equal(t, ref, f.Closed()[0].Change)
	assert.Equal(t, tenant, f.Closed()[0].Tenant)
}

func TestFake_RetargetRetitleAndDeleteAreRecorded(t *testing.T) {
	f := &codehosttest.Fake{}
	ctx := context.Background()
	tenant := uuid.New()
	ref := codehost.ChangeRef{Host: "h", Owner: "a", Repo: "b", Number: 3}
	repo := codehost.Repo{Owner: "a", Name: "b"}

	require.NoError(t, f.RetargetChange(ctx, tenant, ref, "main"))
	f.RetargetErr = codehost.ErrRejected
	assert.ErrorIs(t, f.RetargetChange(ctx, tenant, ref, "x"), codehost.ErrRejected)
	assert.Equal(t, []codehosttest.RetargetCall{{tenant, ref, "main"}, {tenant, ref, "x"}}, f.Retargeted())

	require.NoError(t, f.RetitleChange(ctx, tenant, ref, "t"))
	f.RetitleErr = codehost.ErrRejected
	assert.ErrorIs(t, f.RetitleChange(ctx, tenant, ref, "u"), codehost.ErrRejected)
	assert.Equal(t, "t", f.Retitled()[0].Title)
	assert.Len(t, f.Retitled(), 2)

	key := codehosttest.BranchKey{Repo: repo, Branch: "br"}
	f.BranchAt = map[codehosttest.BranchKey]string{key: "sha"}
	require.NoError(t, f.DeleteBranch(ctx, tenant, repo, "br"))
	assert.NotContains(t, f.BranchAt, key)
	f.DeleteErr = codehost.ErrRejected
	assert.ErrorIs(t, f.DeleteBranch(ctx, tenant, repo, "br"), codehost.ErrRejected)
	assert.Equal(t, []codehosttest.BranchKey{key, key}, f.BranchDeletes())
}

func TestNewLinkHost_NamesAChangeByItsLink(t *testing.T) {
	h := codehosttest.NewLinkHost("git.example.test")
	ref, ok := h.ParseChangeURL("https://git.example.test/Acme/Widgets/pull/12/files")
	require.True(t, ok)
	assert.Equal(t, codehost.ChangeRef{Host: "git.example.test", Owner: "acme", Repo: "widgets", Number: 12}, ref)
	assert.Equal(t, "https://git.example.test/acme/widgets/pull/12", h.ChangeURL(ref))
	repo, ok := h.ParseRemote("git.example.test/acme/widgets")
	require.True(t, ok)
	assert.Equal(t, codehost.Repo{Owner: "acme", Name: "widgets"}, repo)

	for _, raw := range []string{"https://other.test/a/b/pull/1", "https://git.example.test/a/b/issues/1", "http://git.example.test/a/b/pull/1", "https://git.example.test/a/b/pull/0"} {
		_, ok := h.ParseChangeURL(raw)
		assert.False(t, ok, raw)
	}
}

func TestFake_TreeOfAnswersFromTreeAt(t *testing.T) {
	repo := codehost.Repo{Owner: "acme", Name: "widgets"}
	f := &codehosttest.Fake{TreeAt: map[codehosttest.RefKey]string{{Repo: repo, Ref: "c1"}: "t1"}}

	got, err := f.TreeOf(context.Background(), uuid.New(), repo, "c1")
	require.NoError(t, err)
	assert.Equal(t, "t1", got)

	_, err = f.TreeOf(context.Background(), uuid.New(), repo, "c2")
	require.ErrorIs(t, err, codehost.ErrChangeNotFound)

	f.TreeErr = codehost.ErrUnreachable
	_, err = f.TreeOf(context.Background(), uuid.New(), repo, "c1")
	require.ErrorIs(t, err, codehost.ErrUnreachable)
}
