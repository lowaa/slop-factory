package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixture is a project directory with packages and gremlins reports.
type fixture struct {
	t       *testing.T
	project string
	out     bytes.Buffer
	err     bytes.Buffer
	results []string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return &fixture{t: t, project: t.TempDir()}
}

// pkg makes the package's directory and a report of killed and lived mutants.
func (f *fixture) pkg(name string, killed, lived int) {
	f.t.Helper()
	require.NoError(f.t, os.MkdirAll(filepath.Join(f.project, name), 0o750))
	var muts []string
	for i := range killed {
		muts = append(muts, fmt.Sprintf(`{"type":"ARITHMETIC_BASE","status":"KILLED","line":%d,"column":1}`, i+1))
	}
	for i := range lived {
		muts = append(muts, fmt.Sprintf(`{"type":"CONDITIONALS_NEGATION","status":"LIVED","line":%d,"column":3}`, 100+i))
	}
	report := fmt.Sprintf(`{"files":[{"file_name":"x.go","mutations":[%s]}]}`, strings.Join(muts, ","))
	file := filepath.Join(f.t.TempDir(), "r.json")
	require.NoError(f.t, os.WriteFile(file, []byte(report), 0o600))
	f.results = append(f.results, "--result", name+"="+file)
}

func (f *fixture) baseline(content string) {
	f.t.Helper()
	dir := filepath.Join(f.project, "mutation_baseline")
	require.NoError(f.t, os.MkdirAll(dir, 0o750))
	require.NoError(f.t, os.WriteFile(filepath.Join(dir, "score.txt"), []byte(content), 0o600))
}

func (f *fixture) readBaseline() string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.project, "mutation_baseline", "score.txt"))
	require.NoError(f.t, err)
	return string(data)
}

func (f *fixture) run(mode string, extra ...string) int {
	f.t.Helper()
	f.out.Reset()
	f.err.Reset()
	args := append([]string{"summarize", "--project", f.project, "--mode", mode}, extra...)
	args = append(args, f.results...)
	return (&App{Out: &f.out, Err: &f.err}).Run(args)
}

func TestCheckPassesAtTheBaselineAndPrintsTheSummary(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.pkg("./a", 9, 1)
	f.baseline("./a 90.0\n")
	assert.Equal(t, exitOK, f.run("check"))
	assert.Regexp(t, `\./a\s+9\s+1\s+0\s+0\s+90\.0%`, f.out.String())
	assert.Contains(t, f.out.String(), "matches the baseline")
}

func TestCheckFailsOnARegressionNamingThePackage(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.pkg("./a", 8, 2)
	f.baseline("./a 90.0\n")
	assert.Equal(t, exitRegression, f.run("check"))
	assert.Contains(t, f.out.String(), "regressed: ./a: 80.0%")
}

func TestCheckHintsAtPruneAfterAnImprovement(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.pkg("./a", 10, 0)
	f.baseline("./a 90.0\n")
	assert.Equal(t, exitOK, f.run("check", "--prune-cmd", "run it"))
	assert.Contains(t, f.out.String(), "lock the improvement in: run it")
	assert.Equal(t, "./a 90.0\n", f.readBaseline(), "check never writes")
}

func TestCheckWithoutABaselineIsAnError(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.pkg("./a", 9, 1)
	assert.Equal(t, exitError, f.run("check"))
	assert.Contains(t, f.err.String(), "create it with --rebaseline")
}

func TestPruneTightensAndStillFailsOnARegression(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.pkg("./up", 10, 0)
	f.pkg("./down", 5, 5)
	f.baseline("./down 90.0\n./up 80.0\n")
	assert.Equal(t, exitRegression, f.run("prune"))
	got := f.readBaseline()
	assert.Contains(t, got, "./up 100.0\n")
	assert.Contains(t, got, "./down 90.0\n")
}

func TestRebaselineWritesCurrentValuesAndKeepsPackagesNotRun(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.pkg("./a", 5, 5)
	require.NoError(t, os.MkdirAll(filepath.Join(f.project, "other"), 0o750))
	f.baseline("./a 90.0\n./other 70.0\n")
	assert.Equal(t, exitOK, f.run("rebaseline"))
	got := f.readBaseline()
	assert.Contains(t, got, "./a 50.0\n")
	assert.Contains(t, got, "./other 70.0\n")
}

func TestRebaselineWorksWithoutAnExistingBaseline(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.pkg("./a", 5, 5)
	assert.Equal(t, exitOK, f.run("rebaseline"))
	assert.Contains(t, f.readBaseline(), "./a 50.0\n")
}

func TestPruneDropsAStalePackage(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.pkg("./a", 9, 1)
	f.baseline("./a 90.0\n./removed 85.0\n")
	assert.Equal(t, exitOK, f.run("prune"))
	assert.NotContains(t, f.readBaseline(), "./removed")
}

func TestReportWritesFilesAndNeverFails(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.pkg("./a", 5, 5)
	f.baseline("./a 90.0\n")
	dir := filepath.Join(t.TempDir(), "report")
	assert.Equal(t, exitOK, f.run("report", "--report-dir", dir))
	assert.Contains(t, f.out.String(), "regressed (report only)")
	summary, err := os.ReadFile(filepath.Join(dir, "summary.txt"))
	require.NoError(t, err)
	assert.Contains(t, string(summary), "./a")
	survivors, err := os.ReadFile(filepath.Join(dir, "survivors.txt"))
	require.NoError(t, err)
	assert.Contains(t, string(survivors), "./a/x.go:100:3 CONDITIONALS_NEGATION")
	assert.FileExists(t, filepath.Join(dir, "gremlins", "a.json"))
}

func TestReportWithoutABaselineSaysSo(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.pkg("./a", 5, 5)
	assert.Equal(t, exitOK, f.run("report", "--report-dir", t.TempDir()))
	assert.Contains(t, f.out.String(), "nothing to compare")
}

func TestAnyModeWritesTheReportWhenGivenADirectory(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.pkg("./a", 9, 1)
	f.baseline("./a 90.0\n")
	dir := filepath.Join(t.TempDir(), "report")
	assert.Equal(t, exitOK, f.run("check", "--report-dir", dir))
	assert.FileExists(t, filepath.Join(dir, "summary.txt"))
}

func TestASubdirectorysMutantsAreNotThePackages(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.pkg("./a", 9, 1)
	data, err := os.ReadFile(strings.TrimPrefix(f.results[1], "./a="))
	require.NoError(t, err)
	file := filepath.Join(t.TempDir(), "r.json")
	nested := strings.Replace(string(data), `"files":[`, `"files":[{"file_name":"sub/y.go","mutations":[{"type":"X","status":"LIVED","line":1,"column":1}]},`, 1)
	require.NoError(t, os.WriteFile(file, []byte(nested), 0o600))
	f.results = []string{"--result", "./a=" + file}
	f.baseline("./a 90.0\n")
	assert.Equal(t, exitOK, f.run("check"))
	assert.Regexp(t, `\./a\s+9\s+1\s+0\s+0\s+90\.0%`, f.out.String())
}

func TestUsageErrors(t *testing.T) {
	t.Parallel()
	for name, args := range map[string][]string{
		"no subcommand":   {},
		"no project":      {"summarize", "--result", "./a=x"},
		"no result":       {"summarize", "--project", "p"},
		"bad mode":        {"summarize", "--project", "p", "--mode", "x", "--result", "./a=x"},
		"report no dir":   {"summarize", "--project", "p", "--mode", "report", "--result", "./a=x"},
		"result no equal": {"summarize", "--project", t.TempDir(), "--result", "nope"},
		"missing file":    {"summarize", "--project", t.TempDir(), "--result", "./a=/nonexistent"},
	} {
		var out, errb bytes.Buffer
		assert.Equal(t, exitError, (&App{Out: &out, Err: &errb}).Run(args), name)
	}
}

func TestReportNamesDifferForPackagesThatOnlyDifferInSeparators(t *testing.T) {
	t.Parallel()
	assert.NotEqual(t, reportName("./a/b"), reportName("./a-b"))
	assert.Equal(t, "root.json", reportName("."))
}

func TestAPackageThatLostAllItsCoverageRegresses(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.pkg("./a", 0, 0)
	f.baseline("./a 98.4\n")
	assert.Equal(t, exitRegression, f.run("check"))
	assert.Contains(t, f.out.String(), "no mutant is covered")
	assert.Equal(t, exitOK, f.run("rebaseline"))
	assert.NotContains(t, f.readBaseline(), "./a 98.4")
}
