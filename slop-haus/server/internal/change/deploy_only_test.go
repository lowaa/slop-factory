package change_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

const (
	tipSHA       = "cccccccccccccccccccccccccccccccccccccccc"
	mergeBaseSHA = "dddddddddddddddddddddddddddddddddddddddd"

	tagBump  = "@@ -5,3 +5,3 @@ images:\n   - name: api\n-    newTag: 20261011T1\n+    newTag: 20261011T2\n   - name: web"
	ownFile  = "app/server/thing.go"
	ownPatch = "@@ -1 +1 @@\n-a\n+b"
)

var tagRule = scope.DeployOnlyRule{Path: "deploy/*/kustomization.yaml", Lines: `\s*newTag: \S+`}

// behindBy makes the change's head lag the base's tip by the drift files,
// with the repo's deploy-only rules as the manifest has them and the
// change's own files as given (one source file when none are).
func (f *fixture) behindBy(rules []scope.DeployOnlyRule, own []codehost.FileDiff, drift ...codehost.FileDiff) {
	f.t.Helper()
	pl := placedIn(hostName + "/acme/app")
	pl.Repos[0].DeployOnly = rules
	f.ws.set(pl)
	if own == nil {
		own = []codehost.FileDiff{modified(ownFile, ownPatch)}
	}
	f.host.CompareOf[f.host.Opened].ContainsTip = false
	f.host.DiffOf = map[string]*codehost.Diff{headA: {Base: "main", BaseSHA: tipSHA, MergeBaseSHA: mergeBaseSHA, Files: own}}
	f.host.CommitsOf = map[codehosttest.CommitRange]*codehost.Diff{
		{From: mergeBaseSHA, To: tipSHA}: {BaseSHA: mergeBaseSHA, MergeBaseSHA: mergeBaseSHA, Files: drift},
	}
}

func (f *fixture) holdOpenLocked() *holder {
	f.t.Helper()
	h := f.hold(false)
	f.open(h, false)
	f.lock(h)
	return h
}

// The case this exists for: CI committed image tags to the base after the
// change's checks ran, and nothing else moved.
func TestMerge_BehindOnlyByDeployOnlyLinesMergesAsItIs(t *testing.T) {
	f := newFixture(t)
	h := f.holdOpenLocked()
	f.behindBy([]scope.DeployOnlyRule{tagRule}, nil,
		modified("deploy/test/kustomization.yaml", tagBump), modified("deploy/prod/kustomization.yaml", tagBump))

	merged, err := f.merge(h, headA)

	require.NoError(t, err)
	assert.Equal(t, "mergecommit", merged.MergeSHA)
	require.Len(t, f.host.Merges(), 1)
	assert.Equal(t, headA, f.host.Merges()[0].Merge.HeadSHA, "the tested head merges, without the base taken in")
	assert.Equal(t, []codehosttest.CommitRange{{From: mergeBaseSHA, To: tipSHA}}, f.host.CommitCompares(),
		"the drift read is the base's own history since the head's merge base")
	line := f.log.audited(t, audit.ChangeMerged)[0]
	assert.Equal(t, true, line["behind_deploy_only"])
	assert.EqualValues(t, 2, line["deploy_only_files"])
	assert.Equal(t, tipSHA, line["deploy_only_tip_sha"])
}

func TestMerge_AHeadThatContainsTheTipIsNotRecordedAsBehind(t *testing.T) {
	f := newFixture(t)
	h := f.holdOpenLocked()

	_, err := f.merge(h, headA)

	require.NoError(t, err)
	assert.Equal(t, false, f.log.audited(t, audit.ChangeMerged)[0]["behind_deploy_only"])
	assert.Empty(t, f.host.CommitCompares())
}

// Package moves are why renames are accepted: a change that moves non-deploy
// files, or changes their mode, is not blocked by deploy drift.
func TestMerge_ARenameOrModeChangeOfNonDeployFilesMergesPastDrift(t *testing.T) {
	f := newFixture(t)
	h := f.holdOpenLocked()
	f.behindBy([]scope.DeployOnlyRule{tagRule}, []codehost.FileDiff{
		{Path: "app/new/b.go", PreviousPath: "app/old/b.go", Status: codehost.DiffRenamed},
		{Path: "app/copy.go", PreviousPath: "app/old/c.go", Status: codehost.DiffCopied},
		{Path: "app/run.sh", Status: codehost.DiffModeChanged},
	}, modified("deploy/test/kustomization.yaml", tagBump))

	_, err := f.merge(h, headA)

	require.NoError(t, err)
	require.Len(t, f.host.Merges(), 1)
}

// What the base renamed between two files of one rule is still a deploy.
func TestMerge_ABaseRenameWithinOneRuleIsDrift(t *testing.T) {
	f := newFixture(t)
	h := f.holdOpenLocked()
	f.behindBy([]scope.DeployOnlyRule{{Path: "deploy/*/kustomization.yaml"}}, nil,
		codehost.FileDiff{Path: "deploy/new/kustomization.yaml", PreviousPath: "deploy/old/kustomization.yaml", Status: codehost.DiffRenamed})

	_, err := f.merge(h, headA)

	require.NoError(t, err)
	require.Len(t, f.host.Merges(), 1)
	assert.EqualValues(t, 1, f.log.audited(t, audit.ChangeMerged)[0]["deploy_only_files"], "the rename was counted as drift")
}

// A rule with no line pattern takes any change to its files.
func TestMerge_ARuleWithoutLinesTakesAnyChangeToItsFiles(t *testing.T) {
	f := newFixture(t)
	h := f.holdOpenLocked()
	f.behindBy([]scope.DeployOnlyRule{{Path: "deploy/pins.yaml"}}, nil,
		modified("deploy/pins.yaml", "@@ -1,2 +1,2 @@\n-templateRev: a\n+templateRev: b\n note"))

	_, err := f.merge(h, headA)

	require.NoError(t, err)
	require.Len(t, f.host.Merges(), 1)
	assert.Equal(t, headA, f.host.Merges()[0].Merge.HeadSHA)
}

// A deploy that adds or removes a file its rule names is still a deploy.
func TestMerge_DeployOnlyFilesMayBeAddedOrRemoved(t *testing.T) {
	f := newFixture(t)
	h := f.holdOpenLocked()
	f.behindBy([]scope.DeployOnlyRule{tagRule}, nil,
		codehost.FileDiff{Path: "deploy/new/kustomization.yaml", Status: codehost.DiffAdded, Patch: "@@ -0,0 +1 @@\n+  newTag: 1"},
		codehost.FileDiff{Path: "deploy/old/kustomization.yaml", Status: codehost.DiffRemoved, Patch: "@@ -1 +0,0 @@\n-  newTag: 1"})

	_, err := f.merge(h, headA)

	require.NoError(t, err)
	require.Len(t, f.host.Merges(), 1)
	assert.EqualValues(t, 2, f.log.audited(t, audit.ChangeMerged)[0]["deploy_only_files"])
}

// The rules are read again with the rest of the gate just before the host
// is asked to merge: an owner narrowing them meanwhile stops the merge.
func TestMerge_RulesNarrowedWhileTheMergeIsCheckedStopIt(t *testing.T) {
	f := newFixture(t)
	h := f.holdOpenLocked()
	f.behindBy([]scope.DeployOnlyRule{tagRule}, nil, modified("deploy/test/kustomization.yaml", tagBump))
	f.host.AfterChecks = func() { f.ws.set(placedIn(hostName + "/acme/app")) }

	_, err := f.merge(h, headA)

	require.ErrorIs(t, err, change.ErrBehindBase)
	assert.Contains(t, err.Error(), "deploy-only files changed while the merge was being checked")
	assert.Empty(t, f.host.Merges())
	line := f.log.audited(t, audit.ChangeMergeRefused)[0]
	assert.Equal(t, "behind_base", line["reason"])
	assert.Equal(t, "before_merge", line["stage"])
}

// Each way the drift can be more than deploys keeps today's refusal, says
// why, and asks the host to merge nothing.
func TestMerge_BehindByMoreThanDeployOnlyFilesIsRefused(t *testing.T) {
	rules := []scope.DeployOnlyRule{tagRule}
	tagFile := modified("deploy/test/kustomization.yaml", tagBump)
	for name, tc := range map[string]struct {
		rules []scope.DeployOnlyRule
		own   []codehost.FileDiff
		drift []codehost.FileDiff
		setup func(f *fixture)
		why   string
	}{
		"a repo with no rules": {drift: []codehost.FileDiff{tagFile}},
		"a line its rule does not allow": {rules: rules, drift: []codehost.FileDiff{
			modified("deploy/test/kustomization.yaml", "@@ -5,2 +5,2 @@\n-    newTag: 1\n+    newTag: 2\n-replicas: 1\n+replicas: 9"),
		}, why: `deploy/test/kustomization.yaml changes a line its rule does not allow: "replicas: 1"`},
		"a line that only contains what the rule allows": {rules: rules, drift: []codehost.FileDiff{
			modified("deploy/test/kustomization.yaml", "@@ -5 +5 @@\n-    newTag: 1\n+    newTag: 2 # and image: evil"),
		}, why: "changes a line its rule does not allow"},
		"a file no rule names": {rules: rules, drift: []codehost.FileDiff{tagFile, modified("app/server/main.go", ownPatch)},
			why: "app/server/main.go is not a deploy-only file"},
		"a pattern does not cross a slash": {rules: rules, drift: []codehost.FileDiff{modified("deploy/a/b/kustomization.yaml", tagBump)},
			why: "deploy/a/b/kustomization.yaml is not a deploy-only file"},
		"the change edits a deploy-only file": {rules: rules, own: []codehost.FileDiff{modified(ownFile, ownPatch), tagFile},
			drift: []codehost.FileDiff{tagFile}, why: "deploy/test/kustomization.yaml is deploy-only, and this change edits it too"},
		"a base rename of a non-deploy file": {rules: rules, drift: []codehost.FileDiff{
			{Path: "app/b.go", PreviousPath: "app/a.go", Status: codehost.DiffRenamed},
		}, why: "app/b.go was renamed from app/a.go, which is not a deploy-only file of the same rule"},
		"a base rename onto a deploy-only path": {rules: rules, drift: []codehost.FileDiff{
			{Path: "deploy/test/kustomization.yaml", PreviousPath: "app/a.go", Status: codehost.DiffRenamed},
		}, why: "was renamed from app/a.go"},
		"a base rename away from a deploy-only path": {rules: rules, drift: []codehost.FileDiff{
			{Path: "app/a.go", PreviousPath: "deploy/test/kustomization.yaml", Status: codehost.DiffRenamed},
		}, why: "app/a.go was renamed from deploy/test/kustomization.yaml, which is not a deploy-only file of the same rule"},
		"a base rename onto a path whose rule takes any line": {rules: []scope.DeployOnlyRule{{Path: "deploy/pins.yaml"}}, drift: []codehost.FileDiff{
			{Path: "deploy/pins.yaml", PreviousPath: "x.yaml", Status: codehost.DiffRenamed},
		}, why: "deploy/pins.yaml was renamed from x.yaml"},
		"a base copy of a deploy-only file": {rules: rules, drift: []codehost.FileDiff{
			{Path: "deploy/test/kustomization.yaml", PreviousPath: "deploy/dev/kustomization.yaml", Status: codehost.DiffCopied},
		}, why: "was copied"},
		"a base rename whose content change the host gave no patch for": {rules: rules, drift: []codehost.FileDiff{
			{Path: "deploy/new/kustomization.yaml", PreviousPath: "deploy/old/kustomization.yaml", Status: codehost.DiffRenamed},
		}, why: "its patch cannot be checked against its rule"},
		"the change renames a file from a source the host did not name": {rules: rules, own: []codehost.FileDiff{
			{Path: "app/b.go", Status: codehost.DiffRenamed},
		}, drift: []codehost.FileDiff{tagFile}, why: "from a file the host did not name"},
		"a base mode change": {rules: rules, drift: []codehost.FileDiff{
			{Path: "deploy/test/kustomization.yaml", Status: codehost.DiffModeChanged},
		}, why: "was copied, changed in mode"},
		"the change edits the file a base rename left": {rules: rules,
			own:   []codehost.FileDiff{modified("deploy/old/kustomization.yaml", ownPatch)},
			drift: []codehost.FileDiff{{Path: "deploy/new/kustomization.yaml", PreviousPath: "deploy/old/kustomization.yaml", Status: codehost.DiffRenamed}},
			why:   "deploy/old/kustomization.yaml is deploy-only, and this change edits it too"},
		"the change renames a file away from a deploy-only path": {rules: rules, own: []codehost.FileDiff{
			{Path: "app/server/kustomization.yaml", PreviousPath: "deploy/test/kustomization.yaml", Status: codehost.DiffRenamed},
		}, drift: []codehost.FileDiff{tagFile}, why: "this change renamed deploy/test/kustomization.yaml, a deploy-only file"},
		"the change renames a file onto a deploy-only path": {rules: rules, own: []codehost.FileDiff{
			{Path: "deploy/test/kustomization.yaml", PreviousPath: "app/a.yaml", Status: codehost.DiffRenamed},
		}, drift: []codehost.FileDiff{tagFile}, why: "this change renamed deploy/test/kustomization.yaml, a deploy-only file"},
		"the change copies a file from a deploy-only path": {rules: rules, own: []codehost.FileDiff{
			{Path: "app/copy.yaml", PreviousPath: "deploy/test/kustomization.yaml", Status: codehost.DiffCopied},
		}, drift: []codehost.FileDiff{tagFile}, why: "this change copied deploy/test/kustomization.yaml, a deploy-only file"},
		"the change changes the mode of a deploy-only file": {rules: rules, own: []codehost.FileDiff{
			{Path: "deploy/test/kustomization.yaml", Status: codehost.DiffModeChanged},
		}, drift: []codehost.FileDiff{tagFile}, why: "this change changed the mode of deploy/test/kustomization.yaml"},
		"the change has a file in a status nobody can check": {rules: rules, own: []codehost.FileDiff{
			{Path: "app/x.go", Status: codehost.DiffOther},
		}, drift: []codehost.FileDiff{tagFile}, why: "cannot be checked: app/x.go"},
		"the change renames a file onto a path the base renamed away": {rules: rules,
			own:   []codehost.FileDiff{{Path: "app/b.go", PreviousPath: "deploy/dev/kustomization.yaml", Status: codehost.DiffRenamed}},
			drift: []codehost.FileDiff{tagFile}, why: "this change renamed deploy/dev/kustomization.yaml"},
		"a patch the host did not give": {rules: rules, drift: []codehost.FileDiff{
			{Path: "deploy/test/kustomization.yaml", Status: codehost.DiffModified},
		}, why: "its patch cannot be checked against its rule"},
		"a drift too large to read whole": {rules: rules, setup: func(f *fixture) { f.host.CommitsErr = codehost.ErrDiffUnsupported },
			why: "could not be read"},
		"the change's own files cannot be read": {rules: rules, drift: []codehost.FileDiff{tagFile},
			setup: func(f *fixture) { f.host.DiffErr = codehost.ErrDiffUnsupported }, why: "the change's own files could not be read"},
		"a drift that is not from the merge base": {rules: rules, setup: func(f *fixture) {
			f.host.CommitsOf[codehosttest.CommitRange{From: mergeBaseSHA, To: tipSHA}].MergeBaseSHA = baseOld
		}, drift: []codehost.FileDiff{tagFile}, why: "could not be compared"},
		"a base that no longer lags": {rules: rules, drift: []codehost.FileDiff{tagFile}, setup: func(f *fixture) {
			f.host.DiffOf[headA].MergeBaseSHA = tipSHA
		}, why: "the base changed while it was compared"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			h := f.holdOpenLocked()
			f.behindBy(tc.rules, tc.own, tc.drift...)
			if tc.setup != nil {
				tc.setup(f)
			}

			_, err := f.merge(h, headA)

			require.ErrorIs(t, err, change.ErrBehindBase)
			assert.Contains(t, err.Error(), "merge origin/main into your branch", "the remedy is still to take the base in")
			assert.Contains(t, err.Error(), tc.why)
			assert.Empty(t, f.host.Merges())
			line := f.log.audited(t, audit.ChangeMergeRefused)[0]
			assert.Equal(t, "behind_base", line["reason"])
			if tc.rules != nil {
				assert.Contains(t, line["deploy_only"], tc.why)
			}
		})
	}
}

// An approval that covers the head only because its diff against the tip is
// the reviewed one needs the tip: lagging it, even by deploys, is refused.
func TestMerge_AnApprovalCoveringTheHeadByItsDiffStillNeedsTheTip(t *testing.T) {
	f := newFixture(t)
	h := f.reviewedAtA(diffOf(baseOld, modified("a.go", hunkOne)))
	f.headBDiff(diffOf(baseNew, modified("a.go", hunkOne)))
	pl := placedIn(hostName + "/acme/app")
	pl.Repos[0].DeployOnly = []scope.DeployOnlyRule{tagRule}
	f.ws.set(pl)
	f.host.CompareOf[f.host.Opened].ContainsTip = false

	_, err := f.merge(h, "")

	require.ErrorIs(t, err, change.ErrBehindBase)
	assert.Contains(t, err.Error(), "the approval covers this head only by its diff")
	assert.Empty(t, f.host.Merges())
	assert.Empty(t, f.host.CommitCompares())
}

// The reviewed head itself is covered by the approval as it is: deploy drift
// after the hand-over does not need another review.
func TestMerge_TheReviewedHeadMayLagTheBaseByDeploys(t *testing.T) {
	f := newFixture(t)
	h := f.hold(true)
	f.open(h, false)
	f.host.DiffOf = map[string]*codehost.Diff{headA: diffOf(mergeBaseSHA, modified(ownFile, ownPatch))}
	f.handOverAndApprove(h)
	f.behindBy([]scope.DeployOnlyRule{tagRule}, nil, modified("deploy/test/kustomization.yaml", tagBump))

	_, err := f.merge(h, headA)

	require.NoError(t, err)
	assert.Equal(t, "exact_head", f.log.audited(t, audit.ChangeMerged)[0]["approval_match"])
}
