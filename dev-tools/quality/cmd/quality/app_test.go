package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/dev-tools/quality/internal/testmod"
)

type run struct {
	code     int
	out, err string
}

func runApp(args ...string) *run {
	var out, errOut strings.Builder
	code := (&App{Out: &out, Err: &errOut}).Run(args)
	return &run{code: code, out: out.String(), err: errOut.String()}
}

// project is a scratch module with a.go (4 statements' worth of profile)
// and the profiles of a good run and a worse one.
type project struct {
	dir, good, worse, testJSON string
}

func newProject(t *testing.T) *project {
	t.Helper()
	dir := testmod.Write(t, map[string]string{"a/a.go": "package a\n\nfunc F(x int) int {\n\tif x > 0 {\n\t\treturn 1\n\t}\n\treturn 0\n}\n"})
	write := func(name, content string) string {
		p := filepath.Join(t.TempDir(), name)
		require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
		return p
	}
	prefix := "mode: atomic\n" + testmod.Path + "/a/a.go:"
	return &project{
		dir:      dir,
		good:     write("good.out", prefix+"3.20,4.11 1 1\n"+testmod.Path+"/a/a.go:4.11,6.3 1 1\n"),
		worse:    write("worse.out", prefix+"3.20,4.11 1 1\n"+testmod.Path+"/a/a.go:4.11,6.3 1 0\n"),
		testJSON: write("test.json", `{"Time":"2026-01-01T00:00:00Z","Action":"pass","Package":"example.com/scratch/a","Test":"TestF","Elapsed":0.5}`+"\n"),
	}
}

func (p *project) measure(mode, profile string, extra ...string) *run {
	return runApp(append([]string{
		"measure", "--module", p.dir, "--mode", mode, "--coverprofile", profile,
		"--test-json", p.testJSON, "--prune-cmd", "scratch/quality.sh --prune",
	}, extra...)...)
}

func TestRebaselineThenCheck(t *testing.T) {
	t.Parallel()
	p := newProject(t)
	r := p.measure("rebaseline", p.good)
	require.Equal(t, exitOK, r.code, r.err)
	for _, f := range []string{"coverage.txt", "complexity.json", "testtime.json"} {
		assert.FileExists(t, filepath.Join(p.dir, "quality_baseline", f))
	}

	r = p.measure("check", p.good)
	assert.Equal(t, exitOK, r.code, r.out+r.err)
	assert.Contains(t, r.out, "coverage: matches the baseline")

	r = p.measure("check", p.worse)
	assert.Equal(t, exitRegression, r.code, r.out+r.err)
	assert.Contains(t, r.out, "coverage regressed: ./a: 50.0%, baseline 100.0%")
}

func TestPruneHintAndPrune(t *testing.T) {
	t.Parallel()
	p := newProject(t)
	require.Equal(t, exitOK, p.measure("rebaseline", p.worse).code)

	r := p.measure("check", p.good)
	assert.Equal(t, exitOK, r.code, r.err)
	assert.Contains(t, r.out, "lock the improvement in: scratch/quality.sh --prune")

	r = p.measure("prune", p.good)
	require.Equal(t, exitOK, r.code, r.err)
	data, err := os.ReadFile(filepath.Join(p.dir, "quality_baseline", "coverage.txt"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "./a 100.0\n")
}

func TestOnlyRunsTheNamedMeasures(t *testing.T) {
	t.Parallel()
	p := newProject(t)
	r := runApp("measure", "--module", p.dir, "--mode", "rebaseline", "--only", "complexity")
	require.Equal(t, exitOK, r.code, r.err)
	assert.FileExists(t, filepath.Join(p.dir, "quality_baseline", "complexity.json"))
	assert.NoFileExists(t, filepath.Join(p.dir, "quality_baseline", "coverage.txt"))
}

func TestTesttimeIsSkippedWithoutTestOutput(t *testing.T) {
	t.Parallel()
	p := newProject(t)
	r := runApp("measure", "--module", p.dir, "--mode", "rebaseline", "--only", "testtime")
	require.Equal(t, exitOK, r.code, r.err)
	assert.Contains(t, r.out, "testtime: skipped")
}

func TestCheckWithoutABaselineIsAnError(t *testing.T) {
	t.Parallel()
	p := newProject(t)
	r := p.measure("check", p.good)
	assert.Equal(t, exitError, r.code)
	assert.Contains(t, r.err, "--rebaseline")
}

func TestReportWritesEveryReportAndNeverFails(t *testing.T) {
	t.Parallel()
	p := newProject(t)
	require.Equal(t, exitOK, p.measure("rebaseline", p.good).code)
	reports := t.TempDir()
	r := p.measure("report", p.worse, "--report-dir", reports)
	assert.Equal(t, exitOK, r.code, r.err)
	for _, f := range []string{"coverage.txt", "complexity.json", "testtime.json"} {
		assert.FileExists(t, filepath.Join(reports, f))
	}
	assert.Contains(t, r.out, "(report only)")
}

func TestUsageErrors(t *testing.T) {
	t.Parallel()
	p := newProject(t)
	for name, args := range map[string][]string{
		"no command":       nil,
		"unknown command":  {"frobnicate"},
		"no module":        {"measure", "--mode", "check"},
		"unknown mode":     {"measure", "--module", p.dir, "--mode", "loosen"},
		"unknown measure":  {"measure", "--module", p.dir, "--mode", "check", "--only", "coverage,speed"},
		"report needs dir": {"measure", "--module", p.dir, "--mode", "report"},
		"stray argument":   {"measure", "--module", p.dir, "--mode", "check", "extra"},
		"unknown flag":     {"measure", "--module", p.dir, "--frob"},
		"failures no file": {"failures"},
		"failures missing": {"failures", filepath.Join(t.TempDir(), "none.json")},
		"not a module":     {"measure", "--module", t.TempDir(), "--mode", "check"},
	} {
		r := runApp(args...)
		assert.Equal(t, exitError, r.code, name)
		assert.NotEmpty(t, r.err, name)
	}
}

func TestFailuresPrintsFailedTestOutput(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "test.json")
	require.NoError(t, os.WriteFile(p, []byte(`{"Action":"output","Package":"p","Test":"TestBad","Output":"boom\n"}
{"Action":"fail","Package":"p","Test":"TestBad"}
`), 0o600))
	r := runApp("failures", p)
	assert.Equal(t, exitOK, r.code)
	assert.Equal(t, "boom\n", r.err)
}
