package github_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/githubapp"
)

func TestChangeState_ReadsMergeBaseAndMerger(t *testing.T) {
	f := newFixture(t)
	f.api.on("GET", pullPath, 200, map[string]any{
		"number": 12, "merged": true, "merge_commit_sha": tipSHA,
		"merged_by": map[string]any{"login": "merge-bot"},
		"head":      map[string]string{"sha": headSHA}, "base": map[string]string{"ref": "release/1"},
	})

	got, err := f.ch.ChangeState(context.Background(), f.tenant, changeRef)

	require.NoError(t, err)
	assert.Equal(t, &codehost.ChangeState{Merged: true, Base: "release/1", HeadSHA: headSHA, MergeSHA: tipSHA, MergedBy: "merge-bot"}, got)
	assert.Equal(t, []asked{{f.tenant, codehost.Repo{Owner: "acme", Name: "widgets"}, codehost.ReadChecks}}, f.creds.asked)
}

func TestChangeState_AnOpenChangeIsNotMerged(t *testing.T) {
	f := newFixture(t)
	f.pullIs(false, headSHA)

	got, err := f.ch.ChangeState(context.Background(), f.tenant, changeRef)

	require.NoError(t, err)
	assert.False(t, got.Merged)
}

func TestChangeState_ABodyWithoutABaseOrHeadIsRefused(t *testing.T) {
	f := newFixture(t)
	f.api.on("GET", pullPath, 200, map[string]any{"number": 12, "merged": true})

	_, err := f.ch.ChangeState(context.Background(), f.tenant, changeRef)

	assert.ErrorIs(t, err, githubapp.ErrBadResponse)
}

func TestBranchContains(t *testing.T) {
	tests := map[string]struct {
		answer any
		status int
		want   bool
		errIs  error
	}{
		"branch is ahead of the commit": {answer: map[string]any{"status": "ahead"}, status: 200, want: true},
		"identical":                     {answer: map[string]any{"status": "identical"}, status: 200, want: true},
		"branch is behind the commit":   {answer: map[string]any{"status": "behind"}, status: 200, want: false},
		"diverged":                      {answer: map[string]any{"status": "diverged"}, status: 200, want: false},
		"no status":                     {answer: map[string]any{}, status: 200, errIs: githubapp.ErrBadResponse},
		"unknown commit":                {answer: map[string]any{"message": "Not Found"}, status: 404, errIs: codehost.ErrChangeNotFound},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.api.on("GET", "/repos/acme/widgets/compare/"+tipSHA+"...main", tc.status, tc.answer)

			got, err := f.ch.BranchContains(context.Background(), f.tenant, codehost.Repo{Owner: "acme", Name: "widgets"}, "main", tipSHA)

			if tc.errIs != nil {
				require.ErrorIs(t, err, tc.errIs)
				assert.False(t, got, "an error never reads as contained")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestBranchContains_NeedsACommitSHA(t *testing.T) {
	f := newFixture(t)

	_, err := f.ch.BranchContains(context.Background(), f.tenant, codehost.Repo{Owner: "acme", Name: "widgets"}, "main", "main")

	assert.ErrorIs(t, err, codehost.ErrRejected)
	assert.Zero(t, f.api.total())
}
