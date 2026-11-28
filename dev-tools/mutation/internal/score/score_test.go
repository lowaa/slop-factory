package score

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/dev-tools/mutation/internal/gremlins"
)

func neverGone(string) bool { return false }

func TestCompareToleratesAFallWithinTheTolerance(t *testing.T) {
	t.Parallel()
	res := Compare(Snapshot{"./a": 90}, Snapshot{"./a": 88.0}, neverGone)
	assert.Empty(t, res.Regressions)
}

func TestCompareFailsPastTheTolerance(t *testing.T) {
	t.Parallel()
	res := Compare(Snapshot{"./a": 90}, Snapshot{"./a": 87.9}, neverGone)
	require.Len(t, res.Regressions, 1)
	assert.Contains(t, res.Regressions[0], "./a")
	assert.Contains(t, res.Regressions[0], "87.9")
	assert.Contains(t, res.Regressions[0], "90.0")
}

func TestCompareReportsAnImprovement(t *testing.T) {
	t.Parallel()
	res := Compare(Snapshot{"./a": 90}, Snapshot{"./a": 95}, neverGone)
	assert.Empty(t, res.Regressions)
	assert.Equal(t, []string{"./a: 90.0% -> 95.0%"}, res.Improvements)
}

func TestCompareHoldsANewPackageToTheFloor(t *testing.T) {
	t.Parallel()
	res := Compare(Snapshot{}, Snapshot{"./low": 79.9, "./ok": 80}, neverGone)
	require.Len(t, res.Regressions, 1)
	assert.Contains(t, res.Regressions[0], "./low")
	assert.Equal(t, []string{"./ok: new at 80.0%"}, res.Improvements)
}

func TestCompareIgnoresABaselinePackageThatWasNotRun(t *testing.T) {
	t.Parallel()
	res := Compare(Snapshot{"./a": 90, "./b": 90}, Snapshot{"./a": 90}, neverGone)
	assert.Empty(t, res.Regressions)
	assert.Empty(t, res.Improvements)
}

func TestCompareFlagsAStaleEntryOnlyWhenTheSourceIsGone(t *testing.T) {
	t.Parallel()
	gone := func(p string) bool { return p == "./b" }
	res := Compare(Snapshot{"./a": 90, "./b": 90}, Snapshot{}, gone)
	assert.Equal(t, []string{"./b: gone, a stale baseline entry"}, res.Improvements)
}

func TestPruneTightensAddsAndDropsButNeverLoosens(t *testing.T) {
	t.Parallel()
	base := Snapshot{"./up": 80, "./down": 90, "./stale": 85, "./notrun": 70}
	cur := Snapshot{"./up": 92, "./down": 85, "./new": 81, "./newlow": 50}
	got := Prune(base, cur, func(p string) bool { return p == "./stale" })
	assert.Equal(t, Snapshot{"./up": 92, "./down": 90, "./notrun": 70, "./new": 81}, got)
}

func TestRebaselineTakesCurrentValuesEvenWhenLower(t *testing.T) {
	t.Parallel()
	base := Snapshot{"./a": 90, "./kept": 70, "./stale": 85}
	got := Rebaseline(base, Snapshot{"./a": 60, "./b": 10}, nil, func(p string) bool { return p == "./stale" })
	assert.Equal(t, Snapshot{"./a": 60, "./b": 10, "./kept": 70}, got)
}

func TestEncodeDecodeRoundTripsSorted(t *testing.T) {
	t.Parallel()
	in := Snapshot{"./b": 91.5, "./a": 100}
	data := Encode(in)
	assert.Less(t, bytes.Index(data, []byte("./a ")), bytes.Index(data, []byte("./b ")))
	out, err := Decode(data)
	require.NoError(t, err)
	assert.Equal(t, in, out)
}

func TestDecodeRejectsMalformedLines(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"./a", "./a 1 2", "./a x", "./a 101", "./a -1"} {
		_, err := Decode([]byte(in + "\n"))
		assert.Error(t, err, in)
	}
}

func TestScoresSkipsPackagesWithNothingDecided(t *testing.T) {
	t.Parallel()
	rows := []*Row{
		{Package: "./a", Counts: gremlins.Counts{Killed: 2, Lived: 1}},
		{Package: "./none", Counts: gremlins.Counts{NotCovered: 3}},
	}
	assert.Equal(t, Snapshot{"./a": 66.7}, Scores(rows))
}

func TestWriteSummaryHasAPackageRowAndATotal(t *testing.T) {
	t.Parallel()
	rows := []*Row{
		{Package: "./a", Counts: gremlins.Counts{Killed: 3, Lived: 1, TimedOut: 1, NotCovered: 2}},
		{Package: "./none", Counts: gremlins.Counts{NotCovered: 3}},
	}
	var b bytes.Buffer
	WriteSummary(&b, rows)
	out := b.String()
	assert.Regexp(t, `\./a\s+3\s+1\s+1\s+2\s+80\.0%`, out)
	assert.Regexp(t, `\./none\s+0\s+0\s+0\s+3\s+n/a`, out)
	assert.Regexp(t, `total\s+3\s+1\s+1\s+5\s+80\.0%`, out)
}

func TestWriteSurvivorsOpensInAnEditor(t *testing.T) {
	t.Parallel()
	rows := []*Row{{Package: "./internal/x", Survivors: []gremlins.Survivor{{File: "x.go", Line: 4, Column: 9, Type: "CONDITIONALS_NEGATION"}}}}
	var b bytes.Buffer
	WriteSurvivors(&b, rows)
	assert.Equal(t, "./internal/x/x.go:4:9 CONDITIONALS_NEGATION\n", b.String())
}

func TestCompareAFallOfExactlyTheToleranceIsWithinIt(t *testing.T) {
	t.Parallel()
	for _, c := range [][2]float64{{65.4, 63.4}, {65.9, 63.9}, {64.4, 62.4}} {
		res := Compare(Snapshot{"./a": c[0]}, Snapshot{"./a": c[1]}, neverGone)
		assert.Empty(t, res.Regressions, "%v", c)
	}
}

func TestRebaselineDropsAPackageThatDecidedNothing(t *testing.T) {
	t.Parallel()
	got := Rebaseline(Snapshot{"./a": 98.4, "./b": 70}, Snapshot{}, []string{"./a"}, neverGone)
	assert.Equal(t, Snapshot{"./b": 70}, got)
}

func TestUndecidedNamesRowsWithNoScore(t *testing.T) {
	t.Parallel()
	rows := []*Row{{Package: "./a", Counts: gremlins.Counts{Killed: 1}}, {Package: "./b", Counts: gremlins.Counts{NotCovered: 2}}}
	assert.Equal(t, []string{"./b"}, Undecided(rows))
}
