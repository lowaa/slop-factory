package landing_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// The batch tests run on a fake of the batch's host port; this one is the real
// adapter over the code host fake, which is what production runs.
func TestBatchBuilder_BuildsTestsAndEndsABatchThroughTheRealHostAdapter(t *testing.T) {
	f := newFixture(t)
	repo := f.ref(1).InRepo()
	main := codehosttest.BranchKey{Repo: repo, Branch: "main"}
	f.host.CI = codehosttest.NewCI()
	f.host.BranchAt = map[codehosttest.BranchKey]string{main: baseOld}
	f.host.BranchMerged = codehost.BranchMerge{SHA: "cccccccccccccccccccccccccccccccccccccccc"}
	f.host.BranchUpdated = codehost.BranchUpdate{SHA: headA, UpToDate: true}
	var changes []*finished
	for range 2 {
		c := f.finishedChange(model.PriorityNormal)
		changes = append(changes, c)
		f.host.BranchAt[codehosttest.BranchKey{Repo: repo, Branch: ticket.BranchName(c.ticketID, c.attempt)}] = headA
	}
	store := landing.NewBatchStore(f.database)
	builder, err := landing.NewBatchBuilder(f.runner(landing.Config{}), &landing.BatchOptions{Store: store, Host: f.hostWrap.PostgresHost, Members: &membersFake{}})
	require.NoError(t, err)
	cands, err := f.queue.Candidates(f.ctx)
	require.NoError(t, err)

	built, err := builder.Build(f.ctx, cands)

	require.NoError(t, err)
	require.NotNil(t, built)
	t.Cleanup(built.Close)
	branch := built.Batch.Branch
	batchKey := codehosttest.BranchKey{Repo: repo, Branch: branch}
	creates := f.host.BranchCreates()
	require.Len(t, creates, 1)
	assert.Equal(t, batchKey, creates[0].BranchKey)
	assert.Equal(t, baseOld, creates[0].SHA, "the batch starts at main's tip")
	merges := f.host.BranchMerges()
	require.Len(t, merges, 2, "each member's branch is merged into the batch's")
	for i, m := range merges {
		assert.Equal(t, branch, m.Into)
		assert.Equal(t, ticket.BranchName(changes[i].ticketID, changes[i].attempt), m.From)
	}
	opens := f.host.Opens()
	require.Len(t, opens, 3, "the members' own changes, then the batch's")
	assert.Equal(t, branch, opens[2].Request.Head, "a change is opened for the batch branch so that CI runs on it")
	assert.True(t, opens[2].Request.Draft)

	f.host.BranchAt[batchKey] = f.host.BranchMerged.SHA
	checks, err := f.hostWrap.BranchChecks(f.ctx, built.Members[0].Row, branch)
	require.NoError(t, err)
	assert.Equal(t, f.host.BranchMerged.SHA, checks.HeadSHA, "the batch branch's checks are read in the members' repository")

	upd, err := f.hostWrap.UpdateMember(f.ctx, built.Members[0].Row, headA)
	require.NoError(t, err)
	assert.True(t, upd.UpToDate)
	updates := f.host.BranchUpdates()
	require.Len(t, updates, 3, "the builder's two, then this one")
	assert.Equal(t, changes[0].ref, updates[2].Change, "the member's own change is brought up to date through the host")
	assert.Equal(t, headA, updates[2].Update.HeadSHA)

	require.NoError(t, builder.Void(f.ctx, built, "test"))
	assert.Len(t, f.host.Closed(), 1, "the batch's change is closed")
	require.Len(t, f.host.BranchDeletes(), 1)
	assert.Equal(t, batchKey, f.host.BranchDeletes()[0])
}
