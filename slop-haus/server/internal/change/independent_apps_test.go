package change_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

// The ticket holds repo/app/server/: workspace paths are the repo's path
// followed by the repo-relative ones.
var (
	otherApp  = modified("other/server/main.go", ownPatch)
	sharedRul = scope.SharedPathRule{Path: "other/migrations/", Resolve: "migration"}
)

type appsRepo struct {
	apps    []string
	enforce bool
	shared  []scope.SharedPathRule
}

func (f *fixture) behindInApps(r appsRepo, drift ...codehost.FileDiff) {
	f.t.Helper()
	f.behindBy(nil, nil, drift...)
	pl := placedIn(hostName + "/acme/app")
	pl.Repos[0].Path = "repo"
	pl.Repos[0].IndependentApps = r.apps
	pl.Repos[0].SkipRetestAcrossApps = r.enforce
	pl.Repos[0].Shared = r.shared
	f.ws.set(pl)
}

type driftRow struct {
	Decision, Reason, Head, Tip string
	Dirs                        []string
	Enforced                    bool
	CIHead                      *string
	CIPassed                    *bool
}

func (f *fixture) driftRows() []driftRow {
	f.t.Helper()
	rows, err := f.database.Query(f.ctx, `SELECT decision, reason, drift_dirs, head_sha, base_tip_sha, enforced, ci_head_sha, ci_passed FROM merge_drift_decisions ORDER BY id`)
	require.NoError(f.t, err)
	defer rows.Close()
	var out []driftRow
	for rows.Next() {
		var r driftRow
		require.NoError(f.t, rows.Scan(&r.Decision, &r.Reason, &r.Dirs, &r.Head, &r.Tip, &r.Enforced, &r.CIHead, &r.CIPassed))
		out = append(out, r)
	}
	require.NoError(f.t, rows.Err())
	return out
}

var allApps = []string{"app/", "other/", "third/"}

func TestIndependentApps_DecisionPerDrift(t *testing.T) {
	for name, tc := range map[string]struct {
		repo   appsRepo
		drift  []codehost.FileDiff
		accept bool
		dirs   []string
		why    string
	}{
		"another listed app": {repo: appsRepo{apps: allApps}, drift: []codehost.FileDiff{otherApp, modified("third/x.go", ownPatch)},
			accept: true, dirs: []string{"other/", "third/"}},
		"the ticket's own app, outside its path": {repo: appsRepo{apps: allApps}, drift: []codehost.FileDiff{modified("app/web/x.go", ownPatch)},
			dirs: []string{"app/"}, why: "app/web/x.go is in this ticket's own app app/"},
		"the ticket's own app after another app": {repo: appsRepo{apps: allApps}, drift: []codehost.FileDiff{otherApp, modified("app/web/x.go", ownPatch)},
			why: "app/web/x.go is in this ticket's own app app/"},
		"a library": {repo: appsRepo{apps: allApps}, drift: []codehost.FileDiff{otherApp, modified("go-db/db.go", ownPatch)}, dirs: []string{"other/"},
			why: "go-db/db.go is not in an independent app"},
		"a root file": {repo: appsRepo{apps: allApps}, drift: []codehost.FileDiff{modified("go.work", ownPatch)}, why: "go.work is not in an independent app"},
		"a CI directory": {repo: appsRepo{apps: allApps}, drift: []codehost.FileDiff{modified(".woodpecker/ci.yaml", ownPatch)},
			why: ".woodpecker/ci.yaml is not in an independent app"},
		"the ticket's path": {repo: appsRepo{apps: allApps}, drift: []codehost.FileDiff{otherApp, modified("app/server/x.go", ownPatch)},
			why: "app/server/x.go is under the ticket's path repo/app/server/"},
		"a shared path": {repo: appsRepo{apps: allApps, shared: []scope.SharedPathRule{sharedRul}}, drift: []codehost.FileDiff{modified("other/migrations/0001.sql", ownPatch)},
			why: "other/migrations/0001.sql is a shared path"},
		"no list": {repo: appsRepo{}, drift: []codehost.FileDiff{otherApp}, why: "declares no independent apps"},
		"a rename": {repo: appsRepo{apps: allApps}, drift: []codehost.FileDiff{{Path: "other/b.go", Status: codehost.DiffOther}},
			why: "other/b.go was renamed, copied or changed in mode"},
		"a real rename": {repo: appsRepo{apps: allApps}, drift: []codehost.FileDiff{{Path: "other/b.go", PreviousPath: "other/a.go", Status: codehost.DiffRenamed}},
			why: "other/b.go was renamed, copied or changed in mode"},
		"a mode change": {repo: appsRepo{apps: allApps}, drift: []codehost.FileDiff{{Path: "other/run.sh", Status: codehost.DiffModeChanged}},
			why: "other/run.sh was renamed, copied or changed in mode"},
		"a prefix that is not a directory of the list": {repo: appsRepo{apps: allApps}, drift: []codehost.FileDiff{modified("others/x.go", ownPatch)},
			why: "others/x.go is not in an independent app"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			h := f.holdOpenLocked()
			f.behindInApps(tc.repo, tc.drift...)

			_, err := f.merge(h, headA)

			require.ErrorIs(t, err, change.ErrBehindBase, "shadow mode: the gate refuses whatever the rule says")
			assert.Empty(t, f.host.Merges())
			rows := f.driftRows()
			require.Len(t, rows, 1)
			assert.Equal(t, headA, rows[0].Head)
			assert.Equal(t, tipSHA, rows[0].Tip)
			assert.False(t, rows[0].Enforced)
			line := f.log.audited(t, audit.ChangeMergeRefused)[0]
			assert.Equal(t, "behind_base", line["reason"])
			assert.Equal(t, tipSHA, line["independent_apps_tip_sha"])
			if tc.accept {
				assert.Equal(t, "would_accept", rows[0].Decision)
				assert.Equal(t, "would_accept", line["independent_apps"])
			} else {
				assert.Equal(t, "would_refuse", rows[0].Decision)
				assert.Equal(t, "would_refuse", line["independent_apps"])
				assert.Contains(t, rows[0].Reason, tc.why)
				assert.Contains(t, line["independent_apps_reason"], tc.why)
			}
			if tc.dirs != nil {
				assert.Equal(t, tc.dirs, rows[0].Dirs)
			}
		})
	}
}

func TestIndependentApps_ADeployOnlyAcceptanceNeedsNoDecision(t *testing.T) {
	f := newFixture(t)
	h := f.holdOpenLocked()
	f.behindBy([]scope.DeployOnlyRule{tagRule}, nil, modified("deploy/test/kustomization.yaml", tagBump))

	_, err := f.merge(h, headA)

	require.NoError(t, err)
	assert.Empty(t, f.driftRows())
}

func TestIndependentApps_AHeadOnTheTipNeedsNoDecision(t *testing.T) {
	f := newFixture(t)
	h := f.holdOpenLocked()

	_, err := f.merge(h, headA)

	require.NoError(t, err)
	assert.Empty(t, f.driftRows())
}

func TestIndependentApps_DeployDriftAlongsideAnotherAppIsStillAcceptable(t *testing.T) {
	f := newFixture(t)
	h := f.holdOpenLocked()
	f.behindInApps(appsRepo{apps: allApps}, otherApp, modified("deploy/test/kustomization.yaml", tagBump))
	pl := placedIn(hostName + "/acme/app")
	pl.Repos[0].Path, pl.Repos[0].IndependentApps = "repo", allApps
	pl.Repos[0].DeployOnly = []scope.DeployOnlyRule{tagRule}
	f.ws.set(pl)

	_, err := f.merge(h, headA)

	require.ErrorIs(t, err, change.ErrBehindBase)
	rows := f.driftRows()
	require.Len(t, rows, 1)
	assert.Equal(t, "would_accept", rows[0].Decision)
	assert.Equal(t, []string{"other/"}, rows[0].Dirs)
}

func TestIndependentApps_TheSameHeadAndTipIsRecordedOnce(t *testing.T) {
	f := newFixture(t)
	h := f.holdOpenLocked()
	f.behindInApps(appsRepo{apps: allApps}, otherApp)

	_, _ = f.merge(h, headA)
	_, _ = f.merge(h, headA)

	assert.Len(t, f.driftRows(), 1)
	assert.Len(t, f.log.audited(t, audit.ChangeMergeRefused), 2, "both refusals are still audited")
}

func TestIndependentApps_UnreadableDriftIsAWouldRefuse(t *testing.T) {
	f := newFixture(t)
	h := f.holdOpenLocked()
	f.behindInApps(appsRepo{apps: allApps}, otherApp)
	f.host.CommitsErr = codehost.ErrDiffUnsupported

	_, err := f.merge(h, headA)

	require.ErrorIs(t, err, change.ErrBehindBase)
	rows := f.driftRows()
	require.Len(t, rows, 1)
	assert.Equal(t, "would_refuse", rows[0].Decision)
	assert.Contains(t, rows[0].Reason, "could not be read")
}

func TestIndependentApps_AnApprovalByDiffOnlyIsAWouldRefuse(t *testing.T) {
	f := newFixture(t)
	h := f.reviewedAtA(diffOf(baseOld, modified("a.go", hunkOne)))
	f.headBDiff(diffOf(baseNew, modified("a.go", hunkOne)))
	pl := placedIn(hostName + "/acme/app")
	pl.Repos[0].IndependentApps = allApps
	f.ws.set(pl)
	f.host.CompareOf[f.host.Opened].ContainsTip = false

	_, err := f.merge(h, "")

	require.ErrorIs(t, err, change.ErrBehindBase)
	rows := f.driftRows()
	require.Len(t, rows, 1)
	assert.Equal(t, "would_refuse", rows[0].Decision)
	assert.Contains(t, rows[0].Reason, "only by its diff")
}

// The outcome is what the checks said on the head that took the base in.
func TestIndependentApps_LaterChecksOnAnotherHeadAreLinkedToTheDecision(t *testing.T) {
	for name, tc := range map[string]struct {
		state  codehost.CheckState
		linked bool
		passed bool
	}{
		"passed":  {codehost.CheckSuccess, true, true},
		"failed":  {codehost.CheckFailure, true, false},
		"pending": {codehost.CheckPending, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			h := f.holdOpenLocked()
			f.behindInApps(appsRepo{apps: allApps}, otherApp)
			_, _ = f.merge(h, headA)

			// The checks on the head that is still the decided one say nothing.
			_, err := f.svc.Checks(f.ctx, f.tenant, h.agent, "")
			require.NoError(t, err)
			require.Nil(t, f.driftRows()[0].CIPassed)

			f.setChecks(headB, codehost.Check{Name: "ci/build", State: tc.state})
			_, err = f.svc.Checks(f.ctx, f.tenant, h.agent, "")
			require.NoError(t, err)

			row := f.driftRows()[0]
			if !tc.linked {
				assert.Nil(t, row.CIPassed)
				return
			}
			require.NotNil(t, row.CIPassed)
			assert.Equal(t, tc.passed, *row.CIPassed)
			assert.Equal(t, headB, *row.CIHead)

			// The first conclusion stands.
			f.setChecks("cccccccccccccccccccccccccccccccccccccccc", codehost.Check{Name: "ci/build", State: codehost.CheckSuccess})
			_, err = f.svc.Checks(f.ctx, f.tenant, h.agent, "")
			require.NoError(t, err)
			assert.Equal(t, headB, *f.driftRows()[0].CIHead)
		})
	}
}

func TestIndependentApps_TheFlagLetsAWouldAcceptMerge(t *testing.T) {
	f := newFixture(t)
	h := f.holdOpenLocked()
	f.behindInApps(appsRepo{apps: allApps, enforce: true}, otherApp)

	merged, err := f.merge(h, headA)

	require.NoError(t, err)
	assert.Equal(t, "mergecommit", merged.MergeSHA)
	require.Len(t, f.host.Merges(), 1)
	assert.Equal(t, headA, f.host.Merges()[0].Merge.HeadSHA)
	line := f.log.audited(t, audit.ChangeMerged)[0]
	assert.Equal(t, true, line["behind_independent_apps"])
	assert.Equal(t, "other/", line["independent_apps_dirs"])
	assert.Equal(t, tipSHA, line["independent_apps_tip_sha"])
	rows := f.driftRows()
	require.Len(t, rows, 1)
	assert.True(t, rows[0].Enforced)
}

func TestIndependentApps_TheFlagStillRefusesAWouldRefuse(t *testing.T) {
	f := newFixture(t)
	h := f.holdOpenLocked()
	f.behindInApps(appsRepo{apps: allApps, enforce: true}, otherApp, modified("go-db/db.go", ownPatch))

	_, err := f.merge(h, headA)

	require.ErrorIs(t, err, change.ErrBehindBase)
	assert.Empty(t, f.host.Merges())
}

func TestIndependentApps_TheFlagTurnedOffOrTheListChangedWhileCheckingStopsTheMerge(t *testing.T) {
	for name, edit := range map[string]func(pl *ticket.Placement){
		"flag off":     func(pl *ticket.Placement) { pl.Repos[0].SkipRetestAcrossApps = false },
		"list changed": func(pl *ticket.Placement) { pl.Repos[0].IndependentApps = []string{"app/"} },
		"shared path":  func(pl *ticket.Placement) { pl.Repos[0].Shared = []scope.SharedPathRule{sharedRul} },
		"repo moved":   func(pl *ticket.Placement) { pl.Repos[0].Path = "elsewhere" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			h := f.holdOpenLocked()
			f.behindInApps(appsRepo{apps: allApps, enforce: true}, otherApp)
			f.host.AfterChecks = func() {
				pl := placedIn(hostName + "/acme/app")
				pl.Repos[0].Path, pl.Repos[0].IndependentApps, pl.Repos[0].SkipRetestAcrossApps = "repo", allApps, true
				edit(pl)
				f.ws.set(pl)
			}

			_, err := f.merge(h, headA)

			require.ErrorIs(t, err, change.ErrBehindBase)
			assert.Contains(t, err.Error(), "independent apps changed while the merge was being checked")
			assert.Empty(t, f.host.Merges())
		})
	}
}

func TestIndependentApps_TheFlagNeverAcceptsAnApprovalCoveredOnlyByItsDiff(t *testing.T) {
	f := newFixture(t)
	h := f.reviewedAtA(diffOf(baseOld, modified("a.go", hunkOne)))
	f.headBDiff(diffOf(baseNew, modified("a.go", hunkOne)))
	pl := placedIn(hostName + "/acme/app")
	pl.Repos[0].Path, pl.Repos[0].IndependentApps, pl.Repos[0].SkipRetestAcrossApps = "repo", allApps, true
	f.ws.set(pl)
	f.host.CompareOf[f.host.Opened].ContainsTip = false

	_, err := f.merge(h, "")

	require.ErrorIs(t, err, change.ErrBehindBase)
	assert.Empty(t, f.host.Merges())
}

func TestIndependentApps_ChecksOnAHeadThatStillLagsTheBaseAreNotLinked(t *testing.T) {
	f := newFixture(t)
	h := f.holdOpenLocked()
	f.behindInApps(appsRepo{apps: allApps}, otherApp)
	_, _ = f.merge(h, headA)

	f.setChecks(headB, codehost.Check{Name: "ci/build", State: codehost.CheckSuccess})
	f.host.CompareOf[f.host.Opened].ContainsTip = false
	_, err := f.svc.Checks(f.ctx, f.tenant, h.agent, "")
	require.NoError(t, err)

	assert.Nil(t, f.driftRows()[0].CIPassed)
}
