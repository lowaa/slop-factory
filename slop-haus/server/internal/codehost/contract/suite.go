// Package contract is one suite of cases for codehost.Changes, run against
// the fake (codehosttest) and, when given a scratch repository, against the
// real GitHub adapter. The fake stands in for GitHub and CI in tests of the
// layers above it, so what it answers must be what GitHub answers; the cases
// are the ones where an over-generous fake once let a bug through.
//
// The scratch repository must have no CI: the cases assert only what a
// repository without workflows shows, so the same expectations hold for the
// fake with a CI that reports nothing.
package contract

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
)

// Subject is one implementation under test, on a repository with a Base
// branch that has at least one commit.
type Subject struct {
	Changes codehost.Changes
	// Host is the name changes of this host carry in their references.
	Host   string
	Tenant uuid.UUID
	Repo   codehost.Repo
	Base   string
	// Commit adds a commit with content of its own to branch and returns the
	// branch's new head.
	Commit func(t *testing.T, branch string) string
}

// Run runs every case on a fresh Subject from newSubject.
func Run(t *testing.T, newSubject func(t *testing.T) *Subject) {
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSubject(t)
			c.run(t, &run{Subject: s, ctx: context.Background()})
		})
	}
}

type run struct {
	*Subject
	ctx context.Context
}

// branch makes a branch off Base with one commit of its own, removed (with
// any change opened from it) when the test ends.
func (r *run) branch(t *testing.T) (name, sha string) {
	t.Helper()
	name = "contract-" + uuid.NewString()
	base, err := r.Changes.BranchHead(r.ctx, r.Tenant, r.Repo, r.Base)
	require.NoError(t, err)
	require.NoError(t, r.Changes.CreateBranch(r.ctx, r.Tenant, r.Repo, name, base.SHA))
	t.Cleanup(func() { _ = r.Changes.DeleteBranch(context.Background(), r.Tenant, r.Repo, name) })
	return name, r.Commit(t, name)
}

// open opens a change from branch, closed when the test ends.
func (r *run) open(t *testing.T, branch string, draft bool) codehost.ChangeRef {
	t.Helper()
	ref, err := r.Changes.OpenChange(r.ctx, r.Tenant, &codehost.OpenRequest{Repo: r.Repo, Head: branch, Base: r.Base, Title: "contract " + branch, Draft: draft})
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Changes.CloseChange(context.Background(), r.Tenant, ref) })
	return ref
}

type testCase struct {
	name string
	run  func(t *testing.T, r *run)
}

var cases = []testCase{
	{"a bare branch has no checks", func(t *testing.T, r *run) {
		branch, sha := r.branch(t)

		checks, err := r.Changes.BranchChecks(r.ctx, r.Tenant, r.Repo, branch)
		require.NoError(t, err)
		assert.Equal(t, sha, checks.HeadSHA, "checks are for the commit the branch points at")
		assert.Empty(t, checks.Checks)
		assert.False(t, checks.Passed(), "no checks is not a pass")
	}},
	{"checks of a branch the host does not have are not found", func(t *testing.T, r *run) {
		_, err := r.Changes.BranchChecks(r.ctx, r.Tenant, r.Repo, "contract-missing-"+uuid.NewString())
		assert.ErrorIs(t, err, codehost.ErrChangeNotFound)
	}},
	{"a draft change reports on its head commit and base, not a merge ref", func(t *testing.T, r *run) {
		branch, sha := r.branch(t)
		ref := r.open(t, branch, true)

		checks, err := r.Changes.Checks(r.ctx, r.Tenant, ref)
		require.NoError(t, err)
		assert.Equal(t, sha, checks.HeadSHA)
		assert.Equal(t, r.Base, checks.Base)
		assert.False(t, checks.Passed())

		state, err := r.Changes.ChangeState(r.ctx, r.Tenant, ref)
		require.NoError(t, err)
		assert.False(t, state.Merged)
		assert.Equal(t, sha, state.HeadSHA)
	}},
	{"the head of a change follows the branch", func(t *testing.T, r *run) {
		branch, _ := r.branch(t)
		ref := r.open(t, branch, false)
		moved := r.Commit(t, branch)

		checks, err := r.Changes.Checks(r.ctx, r.Tenant, ref)
		require.NoError(t, err)
		assert.Equal(t, moved, checks.HeadSHA)
	}},
	{"a change that was never opened is not found", func(t *testing.T, r *run) {
		ref := codehost.ChangeRef{Host: r.Host, Owner: strings.ToLower(r.Repo.Owner), Repo: strings.ToLower(r.Repo.Name), Number: 999999999}
		_, err := r.Changes.Checks(r.ctx, r.Tenant, ref)
		assert.ErrorIs(t, err, codehost.ErrChangeNotFound)
	}},
	{"a change needs commits beyond its base", func(t *testing.T, r *run) {
		name := "contract-" + uuid.NewString()
		base, err := r.Changes.BranchHead(r.ctx, r.Tenant, r.Repo, r.Base)
		require.NoError(t, err)
		require.NoError(t, r.Changes.CreateBranch(r.ctx, r.Tenant, r.Repo, name, base.SHA))
		t.Cleanup(func() { _ = r.Changes.DeleteBranch(context.Background(), r.Tenant, r.Repo, name) })

		_, err = r.Changes.OpenChange(r.ctx, r.Tenant, &codehost.OpenRequest{Repo: r.Repo, Head: name, Base: r.Base, Title: "empty"})
		assert.ErrorIs(t, err, codehost.ErrRejected)
	}},
	{"a branch has one open change onto a base at a time", func(t *testing.T, r *run) {
		branch, _ := r.branch(t)
		ref := r.open(t, branch, false)

		_, err := r.Changes.OpenChange(r.ctx, r.Tenant, &codehost.OpenRequest{Repo: r.Repo, Head: branch, Base: r.Base, Title: "again"})
		assert.ErrorIs(t, err, codehost.ErrRejected)

		require.NoError(t, r.Changes.CloseChange(r.ctx, r.Tenant, ref))
		again := r.open(t, branch, false)
		assert.NotEqual(t, ref, again, "a closed change is not reopened by opening another")
	}},
	{"closing a change is idempotent and leaves it readable", func(t *testing.T, r *run) {
		branch, sha := r.branch(t)
		ref := r.open(t, branch, false)

		require.NoError(t, r.Changes.CloseChange(r.ctx, r.Tenant, ref))
		require.NoError(t, r.Changes.CloseChange(r.ctx, r.Tenant, ref))
		checks, err := r.Changes.Checks(r.ctx, r.Tenant, ref)
		require.NoError(t, err)
		assert.Equal(t, sha, checks.HeadSHA)
		assert.False(t, checks.Passed(), "closing never makes checks pass")
		state, err := r.Changes.ChangeState(r.ctx, r.Tenant, ref)
		require.NoError(t, err)
		assert.False(t, state.Merged)
	}},
	{"a merge on a head that is not the change's merges nothing", func(t *testing.T, r *run) {
		branch, sha := r.branch(t)
		ref := r.open(t, branch, false)
		r.Commit(t, branch)

		_, err := r.Changes.Merge(r.ctx, r.Tenant, &codehost.MergeRequest{Change: ref, HeadSHA: sha})
		assert.ErrorIs(t, err, codehost.ErrHeadMoved)
		state, err := r.Changes.ChangeState(r.ctx, r.Tenant, ref)
		require.NoError(t, err)
		assert.False(t, state.Merged)
	}},
	{"creating a branch is idempotent at its commit and refuses another", func(t *testing.T, r *run) {
		branch, sha := r.branch(t)
		base, err := r.Changes.BranchHead(r.ctx, r.Tenant, r.Repo, r.Base)
		require.NoError(t, err)

		require.NoError(t, r.Changes.CreateBranch(r.ctx, r.Tenant, r.Repo, branch, sha))
		err = r.Changes.CreateBranch(r.ctx, r.Tenant, r.Repo, branch, base.SHA)
		var conflict *codehost.BranchConflictError
		require.ErrorAs(t, err, &conflict)
		assert.Equal(t, sha, conflict.Have)
	}},
	{"deleting a branch is idempotent", func(t *testing.T, r *run) {
		branch, _ := r.branch(t)

		require.NoError(t, r.Changes.DeleteBranch(r.ctx, r.Tenant, r.Repo, branch))
		require.NoError(t, r.Changes.DeleteBranch(r.ctx, r.Tenant, r.Repo, branch))
		_, err := r.Changes.BranchHead(r.ctx, r.Tenant, r.Repo, branch)
		assert.ErrorIs(t, err, codehost.ErrChangeNotFound)
	}},
}
