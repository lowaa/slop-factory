package landing_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing/guidance"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

const (
	pinnedBase = "0000000000000000000000000000000000000abc"
	mergeOfA   = "cccccccccccccccccccccccccccccccccccccccc"
)

// predecessor lands a change that touches a.go, which is the file every fixture
// change touches, and returns it.
func (f *fixture) predecessor() *finished {
	f.t.Helper()
	f.host.MergedWith = codehost.Merged{SHA: mergeOfA}
	p := f.finishedChange(model.PriorityNormal)
	f.pinBase(p)
	f.land(f.runner(landing.Config{}))
	f.assertMerged(p)
	return p
}

// pinBase gives the ticket the base commit a claim records on a real host, and
// makes the host say the predecessor's merge commit descends from it.
func (f *fixture) pinBase(c *finished) {
	f.t.Helper()
	_, err := f.database.Exec(f.ctx, `UPDATE tickets SET base_commit = $2 WHERE id = $1`, c.ticketID, pinnedBase)
	require.NoError(f.t, err)
	if f.host.CommitsOf == nil {
		f.host.CommitsOf = map[codehosttest.CommitRange]*codehost.Diff{}
	}
	f.host.CommitsOf[codehosttest.CommitRange{From: pinnedBase, To: mergeOfA}] = &codehost.Diff{
		BaseSHA: pinnedBase, MergeBaseSHA: pinnedBase, Files: []codehost.FileDiff{{Path: "a.go"}, {Path: "other.go"}},
	}
}

// requeue is the author finishing again after a handback.
func (f *fixture) requeue(c *finished) {
	f.t.Helper()
	require.NoError(f.t, f.queue.Enqueue(f.ctx, &landing.LandingRow{
		TicketID: c.ticketID, AttemptNumber: 1, ChangeURL: f.hostWrapURL(c.ref), HeadSHA: headA,
	}))
}

func (f *fixture) detailOf(id int64) string {
	f.t.Helper()
	var d string
	require.NoError(f.t, f.database.QueryRow(f.ctx,
		`SELECT detail FROM server_merge_outcomes WHERE ticket_id = $1 ORDER BY id DESC LIMIT 1`, id).Scan(&d))
	return d
}

func (f *fixture) conflicting() *finished {
	f.t.Helper()
	c := f.finishedChange(model.PriorityNormal)
	f.pinBase(c)
	f.setChecks(c.ref, headA, false, green)
	f.host.BranchUpdateErr = codehost.ErrMergeConflict
	return c
}

func TestLand_AConflictNamesTheChangeThatLandedFirst(t *testing.T) {
	f := newFixture(t)
	pred := f.predecessor()
	c := f.conflicting()

	f.land(f.runner(landing.Config{}))

	f.assertHandedBack(c, landing.MergeMainConflict)
	text := f.comments(c.ticketID)
	assert.Contains(t, text, f.hostWrapURL(pred.ref), "the predecessor's change")
	assert.Contains(t, text, fmt.Sprintf("ticket #%d", pred.ticketID))
	assert.Contains(t, text, `conflicting files: "a.go"`, "only the files both touch")
	assert.NotContains(t, text, "other.go")
	assert.Contains(t, text, mergeOfA, "the commit to merge")
	assert.Contains(t, text, "do not just retry")
	assert.Equal(t, fmt.Sprintf("collision cause=merge_conflict predecessors=%d: merging the base into the head conflicted", pred.ticketID), f.detailOf(c.ticketID))
}

// behindThenRed is a change the base has moved past, whose merge with the base
// is clean and red.
func (f *fixture) behindThenRed() *finished {
	f.t.Helper()
	c := f.finishedChange(model.PriorityNormal)
	f.pinBase(c)
	f.setChecks(c.ref, headA, false, green)
	f.host.BranchUpdated = codehost.BranchUpdate{SHA: headB}
	f.hostWrap.onUpdate = func(_ context.Context, _ string) {
		f.host.DiffOf[headB] = f.diff(baseNew)
		f.setChecks(c.ref, headB, true, red)
	}
	f.pipes.passedOnBase = true
	return c
}

func TestLand_RedAfterACleanMergeIsAnInteractionWhenGreenOnBothSides(t *testing.T) {
	f := newFixture(t)
	pred := f.predecessor()
	c := f.behindThenRed()
	f.pipes.passedOnCommit = map[string]bool{headA: true}

	f.land(f.runner(landing.Config{}))

	f.assertHandedBack(c, landing.ChecksRed)
	text := f.comments(c.ticketID)
	assert.Contains(t, text, "green on the base's tip and on your change before the merge")
	assert.Contains(t, text, f.hostWrapURL(pred.ref))
	assert.True(t, strings.HasPrefix(f.detailOf(c.ticketID), fmt.Sprintf("collision cause=interaction predecessors=%d", pred.ticketID)), f.detailOf(c.ticketID))
}

func TestLand_RedThatWasAlreadyRedOnTheChangesOwnHeadIsItsOwnBugAndNotACollision(t *testing.T) {
	f := newFixture(t)
	f.predecessor()
	c := f.behindThenRed()
	f.pipes.passedOnCommit = map[string]bool{headA: false}

	f.land(f.runner(landing.Config{}))

	f.assertHandedBack(c, landing.ChecksRed)
	text := f.comments(c.ticketID)
	assert.Contains(t, text, "your change's own failure")
	assert.NotContains(t, text, "ticket #")
	assert.NotContains(t, f.detailOf(c.ticketID), guidance.DetailPrefix, "an own bug does not count towards the bound")
}

func TestLand_TheThirdCollisionGoesToDiscussionAndNotTheSecond(t *testing.T) {
	f := newFixture(t)
	pred := f.predecessor()
	c := f.conflicting()
	r := f.runner(landing.Config{})

	for lap := 1; lap <= 2; lap++ {
		f.land(r)
		f.assertHandedBack(c, landing.MergeMainConflict)
		f.requeue(c)
	}
	got, err := f.tickets.Get(f.ctx, c.ticketID)
	require.NoError(t, err)
	assert.NotEqual(t, ticket.StatusDiscussion, got.Status, "two collisions are still a handback")

	f.land(r)

	got, err = f.tickets.Get(f.ctx, c.ticketID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusDiscussion, got.Status)
	assert.Nil(t, f.row(c.ticketID), "no longer in the landing queue")
	assert.Nil(t, f.lockHolder())
	assert.Contains(t, f.comments(c.ticketID), fmt.Sprintf("#%d", pred.ticketID))
	assert.Contains(t, strings.Split(f.comments(c.ticketID), "\n"), "[human-only]", "the steward reads the marker as a line of its own")
	assert.True(t, strings.HasPrefix(f.detailOf(c.ticketID), guidance.BoundPrefix+" cause=merge_conflict"), f.detailOf(c.ticketID))
}

func TestLand_TheCountStartsAgainAfterAPersonLookedAtTheTicket(t *testing.T) {
	f := newFixture(t)
	f.predecessor()
	c := f.conflicting()
	f.bound = 1
	r := f.runner(landing.Config{})

	f.land(r)
	got, err := f.tickets.Get(f.ctx, c.ticketID)
	require.NoError(t, err)
	require.Equal(t, ticket.StatusDiscussion, got.Status)

	n, err := guidance.NewPostgresHistory(f.database).Collisions(f.ctx, c.ticketID)
	require.NoError(t, err)
	assert.Zero(t, n, "the bound's own row starts a new count")
}

func TestLandedSince_ListsOnlyWhatDescendsFromTheBaseOldestFirstWithEachOnesOwnFiles(t *testing.T) {
	f := newFixture(t)
	const (
		mOld = "dddddddddddddddddddddddddddddddddddddddd"
		m1   = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
		m2   = "ffffffffffffffffffffffffffffffffffffffff"
	)
	f.host.CommitsOf = map[codehosttest.CommitRange]*codehost.Diff{
		{From: pinnedBase, To: mOld}: {BaseSHA: pinnedBase, MergeBaseSHA: "before"},
		{From: pinnedBase, To: m2}:   {BaseSHA: pinnedBase, MergeBaseSHA: pinnedBase},
		{From: pinnedBase, To: m1}:   {BaseSHA: pinnedBase, MergeBaseSHA: pinnedBase, Files: []codehost.FileDiff{{Path: "one.go"}}},
		{From: m1, To: m2}:           {BaseSHA: m1, MergeBaseSHA: m1, Files: []codehost.FileDiff{{Path: "two.go"}}},
	}
	var ids []int64
	for _, sha := range []string{mOld, m1, m2} {
		f.host.MergedWith = codehost.Merged{SHA: sha}
		p := f.finishedChange(model.PriorityNormal)
		f.land(f.runner(landing.Config{}))
		f.assertMerged(p)
		ids = append(ids, p.ticketID)
	}
	c := f.finishedChange(model.PriorityNormal)
	f.pinBase(c)

	got, err := f.landed.LandedSince(f.ctx, c.ticketID, 1)

	require.NoError(t, err)
	require.Len(t, got, 2, "the merge before the base is not named")
	assert.Equal(t, []int64{ids[1], ids[2]}, []int64{got[0].TicketID, got[1].TicketID}, "oldest first")
	assert.Equal(t, []string{"one.go"}, got[0].Files)
	assert.Equal(t, []string{"two.go"}, got[1].Files, "each change's files are against the one before it")
}

func TestLandedSince_TheBaseCommitItselfIsNotNamed(t *testing.T) {
	f := newFixture(t)
	f.host.MergedWith = codehost.Merged{SHA: pinnedBase}
	p := f.finishedChange(model.PriorityNormal)
	f.land(f.runner(landing.Config{}))
	f.assertMerged(p)
	c := f.finishedChange(model.PriorityNormal)
	f.pinBase(c)

	got, err := f.landed.LandedSince(f.ctx, c.ticketID, 1)

	require.NoError(t, err)
	assert.Empty(t, got)
}
