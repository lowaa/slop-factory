package attemptsignals_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/attemptsignals"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/attemptsignals/owntests"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/attemptsignals/regressions"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/attemptsignals/usage"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
)

var now = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func TestComputeWithNothingIsAllUnknown(t *testing.T) {
	c := attemptsignals.Compute(&attemptsignals.Inputs{}, nil, now)
	b := c.Body

	assert.False(t, b.Cheating.TestsDeleted.Known)
	assert.False(t, b.Cheating.SkipsAdded.Known)
	assert.False(t, b.Cheating.AssertionsRemoved.Known)
	assert.False(t, b.Cheating.OutOfScope.Known)
	assert.False(t, b.Size.Known)
	assert.Equal(t, "unknown", b.Checks.Passed)
	assert.Nil(t, b.Checks.Reruns)
	assert.Nil(t, b.Checks.Regressions)
	assert.NotNil(t, b.OwnTests, "an empty list on the wire, not null")
	assert.Empty(t, b.OwnTests)
	assert.Nil(t, b.Autonomy.Discussions)
	assert.Nil(t, b.Time.Runs)
	assert.Nil(t, b.Time.WallClockSeconds)
	assert.Nil(t, c.TestsDeleted, "an unknown signal is a NULL column, not zero")
	assert.Nil(t, c.LinesAdded)
	assert.Nil(t, c.Reruns)
	assert.Nil(t, c.OwnTestsFlagged)
}

const fixtureDiff = `diff --git a/pkg/a.go b/pkg/a.go
--- a/pkg/a.go
+++ b/pkg/a.go
@@ -1,2 +1,3 @@
-old
+new
+newer
diff --git a/pkg/gone_test.go b/pkg/gone_test.go
deleted file mode 100644
--- a/pkg/gone_test.go
+++ /dev/null
@@ -1,2 +0,0 @@
-func TestGone(t *testing.T) {
-}
diff --git a/elsewhere/x.go b/elsewhere/x.go
--- a/elsewhere/x.go
+++ b/elsewhere/x.go
@@ -1 +1 @@
-a
+b
`

func TestComputeFromFixtures(t *testing.T) {
	wall := 90 * time.Minute
	discussions, runs := int64(2), int64(3)
	in := &attemptsignals.Inputs{
		Final: true, HeadSHA: "abc",
		Diff:   fixtureDiff,
		Scopes: []string{"pkg/"},
		CI: &attemptsignals.CI{Complete: true, Runs: []regressions.Run{
			{Pipeline: 1, Passed: false, FailedTests: []regressions.FailedTest{{Name: "TestFlaky", File: "other_test.go"}}},
			{Pipeline: 2, Passed: false, FailedTests: []regressions.FailedTest{
				{Name: "TestMine", File: "pkg/a_test.go"}, {Name: "TestOther", File: "other_test.go"}}},
		}},
		TouchedTestFiles: []string{"pkg/a_test.go"},
		OwnTests: []owntests.FileRun{
			{Path: "pkg/a_test.go", Base: owntests.ResultFail, Head: owntests.ResultPass},
			{Path: "pkg/b_test.go", Base: owntests.ResultPass, Head: owntests.ResultPass},
		},
	}
	c := attemptsignals.Compute(in, &usage.Signals{Runs: &runs, Discussions: &discussions, WallClock: &wall}, now)
	b := c.Body

	assert.True(t, b.Final)
	assert.Equal(t, "abc", b.HeadSHA)
	assert.Equal(t, []string{"pkg/gone_test.go"}, b.Cheating.TestsDeleted.Files)
	assert.True(t, b.Cheating.SkipsAdded.Known)
	assert.Empty(t, b.Cheating.SkipsAdded.Files)
	assert.Equal(t, []string{"elsewhere/x.go"}, b.Cheating.OutOfScope.Files)
	assert.Equal(t, 3, b.Size.LinesAdded)
	assert.Equal(t, 4, b.Size.LinesRemoved)
	assert.Equal(t, 3, b.Size.FilesTouched)
	assert.Equal(t, "no", b.Checks.Passed)
	require.NotNil(t, b.Checks.Reruns)
	assert.Equal(t, 1, *b.Checks.Reruns)
	require.NotNil(t, b.Checks.OwnTestFailures)
	assert.Equal(t, "TestMine", (*b.Checks.OwnTestFailures)[0].Name)
	require.NotNil(t, b.Checks.Regressions)
	assert.Equal(t, "TestOther", (*b.Checks.Regressions)[0].Name)
	require.Len(t, b.OwnTests, 2)
	assert.Equal(t, "fails-on-base", b.OwnTests[0].Class)
	assert.True(t, b.OwnTests[1].Flagged)
	assert.Equal(t, 1, *c.OwnTestsFlagged)
	assert.EqualValues(t, 2, *b.Autonomy.Discussions)
	assert.Nil(t, b.Autonomy.BlockedHandbacks, "not reported stays unknown")
	assert.EqualValues(t, 5400, *b.Time.WallClockSeconds)
	assert.Equal(t, 1, *c.TestsDeleted)
	assert.Equal(t, 1, *c.OutOfScopeFiles)
}

func TestComputeFromHeadChecksAloneKeepsRerunsAndFailuresUnknown(t *testing.T) {
	c := attemptsignals.Compute(&attemptsignals.Inputs{
		CI: &attemptsignals.CI{Runs: []regressions.Run{{Pipeline: 1, Passed: false}}},
	}, nil, now)

	assert.Equal(t, "no", c.Body.Checks.Passed)
	assert.Nil(t, c.Body.Checks.Reruns)
	assert.Nil(t, c.Body.Checks.Regressions)
	assert.False(t, c.Body.Checks.Unattributed)
	assert.Equal(t, "no", c.ChecksPassed)
}

func TestHostDiffRendersForTheChecks(t *testing.T) {
	d := &codehost.Diff{Files: []codehost.FileDiff{
		{Path: "pkg/a.go", Status: codehost.DiffModified, Patch: "@@ -1 +1,2 @@\n-a\n+b\n+c"},
		{Path: "pkg/gone_test.go", Status: codehost.DiffRemoved, Patch: "@@ -1 +0,0 @@\n-func TestGone() {}"},
		{Path: "img.png", Status: codehost.DiffAdded, Patch: "Binary files a/img.png and b/img.png differ"},
		{Path: "pkg/new.go", PreviousPath: "pkg/old.go", Status: codehost.DiffRenamed},
	}}

	diffText, ok := attemptsignals.UnifiedDiff(d)
	require.True(t, ok)
	c := attemptsignals.Compute(&attemptsignals.Inputs{Diff: diffText}, nil, now)

	assert.Equal(t, 2, c.Body.Size.LinesAdded)
	assert.Equal(t, 2, c.Body.Size.LinesRemoved)
	assert.Equal(t, 4, c.Body.Size.FilesTouched)
	assert.Equal(t, []string{"pkg/gone_test.go"}, c.Body.Cheating.TestsDeleted.Files)
}

func TestHostDiffFailsClosed(t *testing.T) {
	for name, files := range map[string][]codehost.FileDiff{
		"a file with no patch (binary or too large)": {{Path: "big.go", Status: codehost.DiffModified}},
		"a path that forges a header":                {{Path: "x\ndiff --git a/pkg/y_test.go b/pkg/y_test.go", Status: codehost.DiffModified, Patch: "@@ -1 +1 @@\n-a\n+b"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, ok := attemptsignals.UnifiedDiff(&codehost.Diff{Files: files})
			assert.False(t, ok)
		})
	}
}

func TestComputeMapsEveryUsageFigure(t *testing.T) {
	runs, turns, disc, denials, blocked := int64(2), int64(40), int64(1), int64(3), int64(4)
	first, last := now.Add(-3*time.Hour), now.Add(-time.Hour)
	wall := 2 * time.Hour

	b := attemptsignals.Compute(&attemptsignals.Inputs{}, &usage.Signals{
		Runs: &runs, Turns: &turns, FirstClaimAt: &first, LastRunEndedAt: &last, WallClock: &wall,
		Discussions: &disc, ScopeExtensionDenials: &denials, BlockedHandbacks: &blocked,
	}, now).Body

	assert.EqualValues(t, 2, *b.Time.Runs)
	assert.EqualValues(t, 40, *b.Time.Turns)
	assert.Equal(t, first, *b.Time.FirstClaimAt)
	assert.Equal(t, last, *b.Time.LastRunEndedAt)
	assert.EqualValues(t, 7200, *b.Time.WallClockSeconds)
	assert.EqualValues(t, 1, *b.Autonomy.Discussions)
	assert.EqualValues(t, 3, *b.Autonomy.ScopeExtensionDenials)
	assert.EqualValues(t, 4, *b.Autonomy.BlockedHandbacks)
}
