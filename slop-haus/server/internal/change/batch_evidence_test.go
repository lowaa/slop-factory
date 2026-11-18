package change_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

const (
	batchBase   = "b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0"
	prefixOne   = "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"
	prefixTwo   = "a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2a2"
	mergeOne    = "e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1e1"
	elsewhere   = "f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0"
	batchBranch = "merge-queue-batch-1"
	headOne     = "9999999999999999999999999999999999999999"
	otherTicket = 987654
)

// treesFake answers the tree of each commit it was given and decides nothing.
type treesFake map[string]string

func (t treesFake) TreeOf(_ context.Context, _ uuid.UUID, _ codehost.Repo, sha string) (string, error) {
	tree, ok := t[sha]
	if !ok {
		return "", fmt.Errorf("%w: %s", codehost.ErrChangeNotFound, sha)
	}
	return tree, nil
}

// batchCase is the change as the second member of a green batch of two,
// the first having merged: main is at that member's merge commit.
type batchCase struct {
	f  *fixture
	h  *holder
	ev *change.BatchEvidence
}

func newBatchCase(t *testing.T) *batchCase {
	t.Helper()
	f := newFixture(t)
	f.trees = treesFake{"mergecommit": "tree-two", prefixTwo: "tree-two"}
	f.svc = f.serviceWith(f.host)
	h := f.holdOpenLocked()
	// The member's own run is red: the batch's run is what counts.
	f.setChecks(headA, codehost.Check{Name: "ci/build", State: codehost.CheckFailure})
	f.host.BranchAt = map[codehosttest.BranchKey]string{
		{Repo: appRepo, Branch: batchBranch}: prefixTwo,
		{Repo: appRepo, Branch: "main"}:      mergeOne,
	}
	f.host.OnBranch = map[codehosttest.BranchCommit]bool{
		{Repo: appRepo, Branch: batchBranch, SHA: headA}:       true,
		{Repo: appRepo, Branch: f.branchOf(h), SHA: batchBase}: true,
	}
	return &batchCase{f: f, h: h, ev: &change.BatchEvidence{
		ID: 1, Branch: batchBranch, BaseSHA: batchBase,
		Members: []change.BatchMember{
			{TicketID: otherTicket, HeadSHA: headOne, PrefixSHA: prefixOne, MergeSHA: mergeOne},
			{TicketID: h.ticket.ID, HeadSHA: headA, PrefixSHA: prefixTwo},
		},
		Checks: codehost.NewChecks(prefixTwo, "main", []codehost.Check{{Name: "ci/build", State: codehost.CheckSuccess}}),
	}}
}

func (c *batchCase) merge() (*change.Merged, error) {
	return c.f.svc.Merge(c.f.ctx, c.f.tenant, c.h.agent, &change.MergeInput{Batch: c.ev})
}

func (c *batchCase) requireRefused(reason string) {
	c.f.t.Helper()
	_, err := c.merge()
	require.ErrorIs(c.f.t, err, change.ErrBatchEvidence)
	var br *change.BatchRefusal
	require.ErrorAs(c.f.t, err, &br)
	assert.Equal(c.f.t, reason, br.Reason)
	assert.Empty(c.f.t, c.f.host.Merges(), "the host is never asked to merge")
	lines := c.f.log.audited(c.f.t, audit.ChangeMergeRefused)
	require.NotEmpty(c.f.t, lines)
	assert.Equal(c.f.t, "batch_evidence", lines[len(lines)-1]["reason"])
	assert.Equal(c.f.t, reason, lines[len(lines)-1]["batch_reason"])
}

func TestBatchMerge_AMemberOfAGreenBatchMergesOnTheBatchsChecks(t *testing.T) {
	c := newBatchCase(t)

	merged, err := c.merge()

	require.NoError(t, err)
	assert.Equal(t, "mergecommit", merged.MergeSHA)
	require.NotNil(t, merged.BatchTree)
	assert.True(t, merged.BatchTree.Matches)
	require.Len(t, c.f.host.Merges(), 1)
	assert.Equal(t, headA, c.f.host.Merges()[0].Merge.HeadSHA, "the member's own head merges, pinned")
	line := c.f.log.audited(t, audit.ChangeMerged)[0]
	assert.EqualValues(t, 1, line["batch_id"])
	assert.EqualValues(t, 2, line["batch_position"])
}

func TestBatchMerge_TheFirstMemberNeedsMainAtTheBatchBase(t *testing.T) {
	c := newBatchCase(t)
	c.ev.Members = []change.BatchMember{{TicketID: c.h.ticket.ID, HeadSHA: headA, PrefixSHA: prefixTwo}}
	c.f.host.BranchAt[codehosttest.BranchKey{Repo: appRepo, Branch: "main"}] = batchBase

	_, err := c.merge()
	require.NoError(t, err)
}

func TestBatchMerge_NilEvidenceIsTheOwnChecksPath(t *testing.T) {
	c := newBatchCase(t)

	_, err := c.f.merge(c.h, "")

	require.ErrorIs(t, err, change.ErrChecksNotPassed, "the member's own red run still refuses without evidence")
}

func TestBatchMerge_Contained(t *testing.T) {
	t.Run("a head pushed after the batch was built", func(t *testing.T) {
		c := newBatchCase(t)
		c.ev.Members[1].HeadSHA = headB
		c.requireRefused("head_changed")
	})
	t.Run("a batch branch that lacks the head", func(t *testing.T) {
		c := newBatchCase(t)
		c.f.host.OnBranch[codehosttest.BranchCommit{Repo: appRepo, Branch: batchBranch, SHA: headA}] = false
		c.requireRefused("not_contained")
	})
	t.Run("a head that lacks the batch base", func(t *testing.T) {
		c := newBatchCase(t)
		c.f.host.OnBranch[codehosttest.BranchCommit{Repo: appRepo, Branch: c.f.branchOf(c.h), SHA: batchBase}] = false
		c.requireRefused("base_not_in_head")
	})
	t.Run("a containment the host cannot answer", func(t *testing.T) {
		c := newBatchCase(t)
		c.f.host.ContainsErr = fmt.Errorf("host down")
		_, err := c.merge()
		require.Error(t, err)
		assert.NotErrorIs(t, err, change.ErrBatchEvidence)
		assert.Empty(t, c.f.host.Merges())
	})
}

func TestBatchMerge_Ordered(t *testing.T) {
	t.Run("a predecessor that has not merged", func(t *testing.T) {
		c := newBatchCase(t)
		c.ev.Members[0].MergeSHA = ""
		c.requireRefused("out_of_order")
	})
	t.Run("a dropped predecessor voids the evidence", func(t *testing.T) {
		c := newBatchCase(t)
		c.ev.Members[0].MergeSHA = ""
		c.ev.Members[0].Dropped = true
		c.requireRefused("predecessor_dropped")
	})
	t.Run("a dropped predecessor that merged before it was dropped still voids it", func(t *testing.T) {
		c := newBatchCase(t)
		c.ev.Members[0].Dropped = true
		c.requireRefused("predecessor_dropped")
	})
}

func TestBatchMerge_Undisturbed(t *testing.T) {
	t.Run("an unrelated commit landed on main", func(t *testing.T) {
		c := newBatchCase(t)
		c.f.host.BranchAt[codehosttest.BranchKey{Repo: appRepo, Branch: "main"}] = elsewhere
		c.f.host.CommitsOf = map[codehosttest.CommitRange]*codehost.Diff{
			{From: mergeOne, To: elsewhere}: {BaseSHA: mergeOne, MergeBaseSHA: mergeOne, Files: []codehost.FileDiff{modified("lib/x.go", ownPatch)}},
		}
		c.f.host.DiffOf = map[string]*codehost.Diff{headA: {Base: "main", Files: []codehost.FileDiff{modified(ownFile, ownPatch)}}}
		c.requireRefused("main_disturbed")
	})
	t.Run("main moved and the drift cannot be read", func(t *testing.T) {
		c := newBatchCase(t)
		c.f.host.BranchAt[codehosttest.BranchKey{Repo: appRepo, Branch: "main"}] = elsewhere
		c.requireRefused("main_disturbed")
	})
	t.Run("main is not at the batch base for the first member", func(t *testing.T) {
		c := newBatchCase(t)
		c.ev.Members = c.ev.Members[1:]
		c.requireRefused("main_disturbed")
	})
	t.Run("deploy-only drift between members is allowed", func(t *testing.T) {
		c := newBatchCase(t)
		pl := placedIn(hostName + "/acme/app")
		pl.Repos[0].DeployOnly = []scope.DeployOnlyRule{tagRule}
		c.f.ws.set(pl)
		c.f.host.BranchAt[codehosttest.BranchKey{Repo: appRepo, Branch: "main"}] = elsewhere
		c.f.host.CommitsOf = map[codehosttest.CommitRange]*codehost.Diff{
			{From: mergeOne, To: elsewhere}: {BaseSHA: mergeOne, MergeBaseSHA: mergeOne, Files: []codehost.FileDiff{modified("deploy/test/kustomization.yaml", tagBump)}},
		}
		c.f.host.DiffOf = map[string]*codehost.Diff{headA: {Base: "main", Files: []codehost.FileDiff{modified(ownFile, ownPatch)}}}

		c.f.trees.(treesFake)["mergecommit"] = "tree-two-and-the-deploy-files"
		c.f.host.CommitsOf[codehosttest.CommitRange{From: elsewhere, To: "mergecommit"}] = &codehost.Diff{BaseSHA: elsewhere, MergeBaseSHA: elsewhere, Files: []codehost.FileDiff{modified(ownFile, ownPatch)}}

		merged, err := c.merge()

		require.NoError(t, err)
		assert.Equal(t, true, c.f.log.audited(t, audit.ChangeMerged)[0]["behind_deploy_only"])
		assert.True(t, merged.BatchTree.Matches, "the merge holds files the batch did not test, by the drift the gate allowed")
	})
	t.Run("a merge adding more than the member's files to a drifted tip is reported", func(t *testing.T) {
		c := newBatchCase(t)
		pl := placedIn(hostName + "/acme/app")
		pl.Repos[0].DeployOnly = []scope.DeployOnlyRule{tagRule}
		c.f.ws.set(pl)
		c.f.host.BranchAt[codehosttest.BranchKey{Repo: appRepo, Branch: "main"}] = elsewhere
		c.f.host.CommitsOf = map[codehosttest.CommitRange]*codehost.Diff{
			{From: mergeOne, To: elsewhere}:      {BaseSHA: mergeOne, MergeBaseSHA: mergeOne, Files: []codehost.FileDiff{modified("deploy/test/kustomization.yaml", tagBump)}},
			{From: elsewhere, To: "mergecommit"}: {BaseSHA: elsewhere, MergeBaseSHA: elsewhere, Files: []codehost.FileDiff{modified(ownFile, ownPatch), modified("lib/late.go", ownPatch)}},
		}
		c.f.host.DiffOf = map[string]*codehost.Diff{headA: {Base: "main", Files: []codehost.FileDiff{modified(ownFile, ownPatch)}}}

		merged, err := c.merge()

		require.NoError(t, err, "the host's merge stands")
		assert.False(t, merged.BatchTree.Matches)
		assert.Contains(t, merged.BatchTree.Why, "lib/late.go")
	})
	t.Run("drift in another independent app is allowed only when enforced", func(t *testing.T) {
		for enforce, wantErr := range map[bool]bool{true: false, false: true} {
			c := newBatchCase(t)
			pl := placedIn(hostName + "/acme/app")
			pl.Repos[0].Path = "repo"
			pl.Repos[0].IndependentApps = allApps
			pl.Repos[0].SkipRetestAcrossApps = enforce
			c.f.ws.set(pl)
			c.f.host.BranchAt[codehosttest.BranchKey{Repo: appRepo, Branch: "main"}] = elsewhere
			c.f.host.CommitsOf = map[codehosttest.CommitRange]*codehost.Diff{
				{From: mergeOne, To: elsewhere}: {BaseSHA: mergeOne, MergeBaseSHA: mergeOne, Files: []codehost.FileDiff{otherApp}},
			}
			c.f.host.DiffOf = map[string]*codehost.Diff{headA: {Base: "main", Files: []codehost.FileDiff{modified(ownFile, ownPatch)}}}

			_, err := c.merge()

			if wantErr {
				require.ErrorIs(t, err, change.ErrBatchEvidence, "enforce=%v", enforce)
			} else {
				require.NoError(t, err, "enforce=%v", enforce)
				assert.Equal(t, true, c.f.log.audited(t, audit.ChangeMerged)[0]["behind_independent_apps"])
			}
		}
	})
	t.Run("deploy-only drift in a file the member edits is not", func(t *testing.T) {
		c := newBatchCase(t)
		pl := placedIn(hostName + "/acme/app")
		pl.Repos[0].DeployOnly = []scope.DeployOnlyRule{tagRule}
		c.f.ws.set(pl)
		c.f.host.BranchAt[codehosttest.BranchKey{Repo: appRepo, Branch: "main"}] = elsewhere
		c.f.host.CommitsOf = map[codehosttest.CommitRange]*codehost.Diff{
			{From: mergeOne, To: elsewhere}: {BaseSHA: mergeOne, MergeBaseSHA: mergeOne, Files: []codehost.FileDiff{modified("deploy/test/kustomization.yaml", tagBump)}},
		}
		c.f.host.DiffOf = map[string]*codehost.Diff{headA: {Base: "main", Files: []codehost.FileDiff{modified("deploy/test/kustomization.yaml", tagBump)}}}
		c.requireRefused("main_disturbed")
	})
}

func TestBatchMerge_Green(t *testing.T) {
	for name, state := range map[string]codehost.CheckState{"red": codehost.CheckFailure, "pending": codehost.CheckPending} {
		t.Run(name, func(t *testing.T) {
			c := newBatchCase(t)
			c.ev.Checks = codehost.NewChecks(prefixTwo, "main", []codehost.Check{{Name: "ci/build", State: state}})
			c.requireRefused("checks_not_green")
		})
	}
	t.Run("none", func(t *testing.T) {
		c := newBatchCase(t)
		c.ev.Checks = codehost.NewChecks(prefixTwo, "main", nil)
		c.requireRefused("checks_not_green")
	})
	t.Run("checks read for another head", func(t *testing.T) {
		c := newBatchCase(t)
		c.ev.Checks = codehost.NewChecks(prefixOne, "main", []codehost.Check{{Name: "ci/build", State: codehost.CheckSuccess}})
		c.requireRefused("checks_other_head")
	})
	t.Run("a batch branch that moved after the checks", func(t *testing.T) {
		c := newBatchCase(t)
		c.f.host.BranchAt[codehosttest.BranchKey{Repo: appRepo, Branch: batchBranch}] = elsewhere
		c.requireRefused("batch_branch_moved")
	})
}

func TestBatchMerge_OneMemberOneBatchRun(t *testing.T) {
	t.Run("evidence of a batch the ticket was not in", func(t *testing.T) {
		c := newBatchCase(t)
		c.ev.Members[1].TicketID = otherTicket + 1
		c.requireRefused("not_member")
	})
	t.Run("a member the runner dropped presenting its own batch", func(t *testing.T) {
		c := newBatchCase(t)
		c.ev.Members[1].Dropped = true
		c.requireRefused("not_member")
	})
	t.Run("evidence that names nothing", func(t *testing.T) {
		c := newBatchCase(t)
		c.ev.Checks = nil
		c.requireRefused("invalid_evidence")
	})
}

func TestBatchMerge_RefusedWithoutATreeReader(t *testing.T) {
	c := newBatchCase(t)
	c.f.trees = nil
	c.f.svc = c.f.serviceWith(c.f.host)
	c.requireRefused("no_tree_reader")
}

func TestBatchMerge_AMergeWithAnotherTreeIsReported(t *testing.T) {
	c := newBatchCase(t)
	c.f.trees.(treesFake)["mergecommit"] = "tree-other"

	merged, err := c.merge()

	require.NoError(t, err, "the host's merge stands")
	require.NotNil(t, merged.BatchTree)
	assert.False(t, merged.BatchTree.Matches)
	assert.Contains(t, merged.BatchTree.Why, "tree-other")
	failed := c.f.log.audited(t, audit.ChangeOperationFailed)
	require.NotEmpty(t, failed)
	assert.Equal(t, "batch_tree", failed[len(failed)-1]["operation"])
}

func TestBatchMerge_ApprovalStillCoversEachMembersHead(t *testing.T) {
	setup := func(t *testing.T, headDiff *codehost.Diff) *batchCase {
		f := newFixture(t)
		f.trees = treesFake{"mergecommit": "tree-two", prefixTwo: "tree-two"}
		f.svc = f.serviceWith(f.host)
		h := f.reviewedAtA(diffOf(baseOld, modified("a.go", hunkOne)))
		f.headBDiff(headDiff)
		f.host.BranchAt = map[codehosttest.BranchKey]string{
			{Repo: appRepo, Branch: batchBranch}: prefixTwo,
			{Repo: appRepo, Branch: "main"}:      mergeOne,
		}
		f.host.OnBranch = map[codehosttest.BranchCommit]bool{
			{Repo: appRepo, Branch: batchBranch, SHA: headB}:       true,
			{Repo: appRepo, Branch: f.branchOf(h), SHA: batchBase}: true,
		}
		return &batchCase{f: f, h: h, ev: &change.BatchEvidence{
			ID: 1, Branch: batchBranch, BaseSHA: batchBase,
			Members: []change.BatchMember{
				{TicketID: otherTicket, HeadSHA: headOne, PrefixSHA: prefixOne, MergeSHA: mergeOne},
				{TicketID: h.ticket.ID, HeadSHA: headB, PrefixSHA: prefixTwo},
			},
			Checks: codehost.NewChecks(prefixTwo, "main", []codehost.Check{{Name: "ci/build", State: codehost.CheckSuccess}}),
		}}
	}
	t.Run("the same diff on a new head keeps the approval", func(t *testing.T) {
		c := setup(t, diffOf(baseNew, modified("a.go", hunkOne)))
		// The member's diff against the merge base, not the tip, which the
		// earlier members moved.
		c.f.host.DiffOf[headB].MergeBaseSHA = baseOld

		_, err := c.merge()

		require.NoError(t, err)
		assert.Equal(t, "same_diff", c.f.log.audited(t, audit.ChangeMerged)[0]["approval_match"])
	})
	t.Run("a stale approval inside a green batch is refused", func(t *testing.T) {
		c := setup(t, diffOf(baseNew, modified("a.go", hunkOne), modified("b.go", hunkTwo)))
		c.f.host.DiffOf[headB].MergeBaseSHA = baseOld

		_, err := c.merge()

		require.ErrorIs(t, err, change.ErrHeadNotReviewed)
		assert.Empty(t, c.f.host.Merges())
	})
}

func TestBatchMerge_CIPassed(t *testing.T) {
	const url = "https://ci.example/repos/7/pipeline/11/9"
	pending := func(u string) *codehost.Checks {
		return codehost.NewChecks(prefixTwo, "main", []codehost.Check{{Name: "ci/build", State: codehost.CheckPending, TargetURL: u}})
	}
	t.Run("a pending check the CI vouched for is green", func(t *testing.T) {
		c := newBatchCase(t)
		c.ev.Checks = pending(url)
		c.ev.CIPassed = []change.CheckLine{{Name: "ci/build", TargetURL: url}}
		_, err := c.merge()
		require.NoError(t, err)
	})
	t.Run("not when the check was rerun elsewhere", func(t *testing.T) {
		c := newBatchCase(t)
		c.ev.Checks = pending(url + "2")
		c.ev.CIPassed = []change.CheckLine{{Name: "ci/build", TargetURL: url}}
		c.requireRefused("checks_not_green")
	})
	t.Run("not for a failed check", func(t *testing.T) {
		c := newBatchCase(t)
		c.ev.Checks = codehost.NewChecks(prefixTwo, "main", []codehost.Check{{Name: "ci/build", State: codehost.CheckFailure, TargetURL: url}})
		c.ev.CIPassed = []change.CheckLine{{Name: "ci/build", TargetURL: url}}
		c.requireRefused("checks_not_green")
	})
}
