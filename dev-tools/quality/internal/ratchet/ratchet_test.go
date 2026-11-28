package ratchet_test

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/dev-tools/quality/internal/ratchet"
)

// fakeMetric is a measure of one number, lower is better. It decides
// nothing the test doesn't give it: Compare and Prune return the result
// and value the test set, and record what they were called with.
type fakeMetric struct {
	gated      bool
	current    int
	measureErr error
	result     *ratchet.Result
	pruned     int

	comparedBase, comparedCur int
	compared                  bool
}

func (f *fakeMetric) Name() string { return "fake" }
func (f *fakeMetric) File() string { return "fake.txt" }
func (f *fakeMetric) Gated() bool  { return f.gated }
func (f *fakeMetric) Measure() (int, error) {
	return f.current, f.measureErr
}

func (f *fakeMetric) Compare(base, cur int) *ratchet.Result {
	f.comparedBase, f.comparedCur, f.compared = base, cur, true
	if f.result == nil {
		return &ratchet.Result{}
	}
	return f.result
}
func (f *fakeMetric) Prune(_, _ int) int { return f.pruned }
func (f *fakeMetric) Encode(s int) ([]byte, error) {
	return []byte(strconv.Itoa(s) + "\n"), nil
}

func (f *fakeMetric) Decode(data []byte) (int, error) {
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

func (f *fakeMetric) Report(cur int) ([]byte, error) {
	return []byte("report " + strconv.Itoa(cur) + "\n"), nil
}

type env struct {
	runner *ratchet.Runner
	out    *strings.Builder
}

func newEnv(t *testing.T, mode ratchet.Mode, baseline string) *env {
	t.Helper()
	dir := t.TempDir()
	if baseline != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "fake.txt"), []byte(baseline), 0o600))
	}
	out := &strings.Builder{}
	return &env{
		runner: &ratchet.Runner{
			Mode: mode, BaselineDir: dir, ReportDir: filepath.Join(t.TempDir(), "reports"),
			PruneCmd: "proj/quality.sh --prune", Out: out,
		},
		out: out,
	}
}

func (e *env) baseline(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.runner.BaselineDir, "fake.txt"))
	require.NoError(t, err)
	return string(data)
}

func TestCheckFailsOnARegression(t *testing.T) {
	t.Parallel()
	e := newEnv(t, ratchet.Check, "5\n")
	m := &fakeMetric{gated: true, current: 6, result: &ratchet.Result{Regressions: []string{"x: 6, baseline 5"}}}
	failed, err := ratchet.Run(e.runner, m)
	require.NoError(t, err)
	assert.True(t, failed)
	assert.Equal(t, [2]int{5, 6}, [2]int{m.comparedBase, m.comparedCur})
	assert.Contains(t, e.out.String(), "fake regressed: x: 6, baseline 5")
	assert.Equal(t, "5\n", e.baseline(t), "a check never writes")
}

func TestCheckPassesAnImprovementWithAPruneHint(t *testing.T) {
	t.Parallel()
	e := newEnv(t, ratchet.Check, "5\n")
	m := &fakeMetric{gated: true, current: 4, result: &ratchet.Result{Improvements: []string{"x: 4, baseline 5"}}}
	failed, err := ratchet.Run(e.runner, m)
	require.NoError(t, err)
	assert.False(t, failed)
	assert.Contains(t, e.out.String(), "fake improved: x: 4, baseline 5")
	assert.Contains(t, e.out.String(), "proj/quality.sh --prune")
}

func TestWarningsNeverFail(t *testing.T) {
	t.Parallel()
	e := newEnv(t, ratchet.Check, "5\n")
	m := &fakeMetric{current: 9, result: &ratchet.Result{Warnings: []string{"slow"}}}
	failed, err := ratchet.Run(e.runner, m)
	require.NoError(t, err)
	assert.False(t, failed)
	assert.Contains(t, e.out.String(), "fake warning: slow")
}

func TestAMissingGatedBaselineIsAnError(t *testing.T) {
	t.Parallel()
	for _, mode := range []ratchet.Mode{ratchet.Check, ratchet.Prune} {
		e := newEnv(t, mode, "")
		_, err := ratchet.Run(e.runner, &fakeMetric{gated: true})
		assert.ErrorIs(t, err, ratchet.ErrNoBaseline, mode)
	}
}

func TestAMissingReportOnlyBaselineIsANote(t *testing.T) {
	t.Parallel()
	e := newEnv(t, ratchet.Check, "")
	m := &fakeMetric{}
	failed, err := ratchet.Run(e.runner, m)
	require.NoError(t, err)
	assert.False(t, failed)
	assert.False(t, m.compared)
	assert.Contains(t, e.out.String(), "no baseline")
}

func TestAMalformedBaselineIsAnError(t *testing.T) {
	t.Parallel()
	e := newEnv(t, ratchet.Check, "five\n")
	_, err := ratchet.Run(e.runner, &fakeMetric{gated: true})
	assert.ErrorContains(t, err, "fake.txt")
}

func TestAMeasureErrorIsAnError(t *testing.T) {
	t.Parallel()
	e := newEnv(t, ratchet.Check, "5\n")
	_, err := ratchet.Run(e.runner, &fakeMetric{gated: true, measureErr: errors.New("boom")})
	assert.ErrorContains(t, err, "boom")
}

func TestPruneWritesThePrunedBaselineAndStillFails(t *testing.T) {
	t.Parallel()
	e := newEnv(t, ratchet.Prune, "5\n")
	m := &fakeMetric{gated: true, current: 6, pruned: 3, result: &ratchet.Result{Regressions: []string{"y"}}}
	failed, err := ratchet.Run(e.runner, m)
	require.NoError(t, err)
	assert.True(t, failed)
	assert.Equal(t, "3\n", e.baseline(t))
	assert.NotContains(t, e.out.String(), "--prune", "no hint to run what just ran")
}

func TestRebaselineWritesTheCurrentValueWithoutComparing(t *testing.T) {
	t.Parallel()
	e := newEnv(t, ratchet.Rebaseline, "")
	m := &fakeMetric{gated: true, current: 42}
	failed, err := ratchet.Run(e.runner, m)
	require.NoError(t, err)
	assert.False(t, failed)
	assert.False(t, m.compared)
	assert.Equal(t, "42\n", e.baseline(t))
	info, err := os.Stat(filepath.Join(e.runner.BaselineDir, "fake.txt"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}

func TestReportWritesTheReportAndNeverFails(t *testing.T) {
	t.Parallel()
	e := newEnv(t, ratchet.Report, "5\n")
	m := &fakeMetric{gated: true, current: 6, result: &ratchet.Result{Regressions: []string{"z"}}}
	failed, err := ratchet.Run(e.runner, m)
	require.NoError(t, err)
	assert.False(t, failed)
	data, err := os.ReadFile(filepath.Join(e.runner.ReportDir, "fake.txt"))
	require.NoError(t, err)
	assert.Equal(t, "report 6\n", string(data))
	assert.Contains(t, e.out.String(), "fake regressed (report only): z")
	assert.Equal(t, "5\n", e.baseline(t))

	// Nor does a report fail for want of a baseline.
	e = newEnv(t, ratchet.Report, "")
	_, err = ratchet.Run(e.runner, &fakeMetric{gated: true})
	assert.NoError(t, err)
}

func TestEncodeJSON(t *testing.T) {
	t.Parallel()
	data, err := ratchet.EncodeJSON(map[string]int{"a": 1})
	require.NoError(t, err)
	assert.Equal(t, "{\n  \"a\": 1\n}\n", string(data))
	_, err = ratchet.EncodeJSON(make(chan int))
	assert.Error(t, err)
}

func TestParseMode(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"check", "prune", "rebaseline", "report"} {
		m, err := ratchet.ParseMode(s)
		require.NoError(t, err)
		assert.Equal(t, ratchet.Mode(s), m)
	}
	_, err := ratchet.ParseMode("loosen")
	assert.Error(t, err)
}
