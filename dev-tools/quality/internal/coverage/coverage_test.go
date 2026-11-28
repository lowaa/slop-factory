package coverage_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/dev-tools/quality/internal/coverage"
	"github.com/lowaa/slop-factory/dev-tools/quality/internal/gosrc"
	"github.com/lowaa/slop-factory/dev-tools/quality/internal/testmod"
)

// profile writes a cover profile of "<file> <start>,<end> <stmts> <count>"
// lines, the file module-relative.
func profile(t *testing.T, mode string, lines ...string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("mode: " + mode + "\n")
	for _, l := range lines {
		b.WriteString(testmod.Path + "/" + l + "\n")
	}
	p := filepath.Join(t.TempDir(), "cover.out")
	require.NoError(t, os.WriteFile(p, []byte(b.String()), 0o600))
	return p
}

func scratchModule(t *testing.T) *gosrc.Module {
	t.Helper()
	m, err := gosrc.LoadModule(testmod.Write(t, map[string]string{
		"a/a.go": "package a\n",
		"b/b.go": "package b\n\nfunc B() {}\n",
		// Only declarations: nothing a profile could measure.
		"c/c.go":      "package c\n\ntype T struct{}\n",
		"a/zz_gen.go": testmod.Generated + "package a\n",
	}))
	require.NoError(t, err)
	return m
}

func TestMeasureMergesProfilesExcludingGeneratedAndForeignFiles(t *testing.T) {
	t.Parallel()
	mod := scratchModule(t)
	// One run covers a's first block, another its second, in another mode:
	// merged, a is fully covered.
	first := profile(t, "atomic",
		"a/a.go:1.1,2.2 3 4",
		"a/a.go:3.1,4.2 1 0",
		"b/b.go:1.1,2.2 1 0",
		"b/b.go:3.1,4.2 3 2",
		// Neither of these may count: generated, and another module's.
		"a/zz_gen.go:1.1,2.2 100 0",
	)
	second := profile(t, "set",
		"a/a.go:1.1,2.2 3 0",
		"a/a.go:3.1,4.2 1 1",
	)
	foreign := filepath.Join(t.TempDir(), "foreign.out")
	require.NoError(t, os.WriteFile(foreign, []byte("mode: set\nexample.com/other/o.go:1.1,2.2 50 0\n"), 0o600))

	m := &coverage.Metric{Module: mod, Profiles: []string{first, second, foreign}}
	s, err := m.Measure()
	require.NoError(t, err)
	assert.Equal(t, map[string]float64{"./a": 100, "./b": 75}, s.Packages)
	assert.InDelta(t, 87.5, s.Total, 1e-9) // 7 of 8
	assert.Empty(t, s.Unmeasured)
}

func TestMeasureNamesPackagesNoProfileReached(t *testing.T) {
	t.Parallel()
	m := &coverage.Metric{Module: scratchModule(t), Profiles: []string{profile(t, "set", "a/a.go:1.1,2.2 1 1")}}
	s, err := m.Measure()
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"./b": true}, s.Unmeasured, "c has only declarations, so its absence is no gap")
}

func TestANewPackageInNoProfileRegresses(t *testing.T) {
	t.Parallel()
	m := &coverage.Metric{}
	base := snap(90, map[string]float64{"./a": 90})
	cur := snap(90, map[string]float64{"./a": 90})
	cur.Unmeasured = map[string]bool{"./c": true}
	res := m.Compare(base, cur)
	assert.Equal(t, []string{"./c: a new package in no cover profile: a profile of only some packages' tests?"}, res.Regressions)
}

func TestMetricNamesItsBaseline(t *testing.T) {
	t.Parallel()
	m := &coverage.Metric{}
	assert.Equal(t, "coverage", m.Name())
	assert.Equal(t, "coverage.txt", m.File())
	assert.True(t, m.Gated())
	s := snap(50, map[string]float64{"./a": 50})
	report, err := m.Report(s)
	require.NoError(t, err)
	encoded, err := m.Encode(s)
	require.NoError(t, err)
	assert.Equal(t, encoded, report)
}

func TestMeasureRoundsToOneDecimal(t *testing.T) {
	t.Parallel()
	m := &coverage.Metric{Module: scratchModule(t), Profiles: []string{profile(t, "set",
		"a/a.go:1.1,2.2 1 1",
		"a/a.go:3.1,4.2 2 0",
	)}}
	s, err := m.Measure()
	require.NoError(t, err)
	assert.InDelta(t, 33.3, s.Packages["./a"], 1e-9)
}

func TestMeasureFailsOnAFileItCannotRead(t *testing.T) {
	t.Parallel()
	mod := scratchModule(t)
	testmod.WriteFiles(t, mod.Dir, map[string]string{"a/bad.go": "not go at all"})
	_, err := (&coverage.Metric{Module: mod, Profiles: []string{profile(t, "set", "a/bad.go:1.1,2.2 1 1")}}).Measure()
	assert.Error(t, err)
}

func TestMeasureFailsOnASourceTreeItCannotWalk(t *testing.T) {
	t.Parallel()
	mod := scratchModule(t)
	locked := filepath.Join(mod.Dir, "locked")
	require.NoError(t, os.Mkdir(locked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) }) // #nosec G302 -- so t.TempDir can remove it
	_, err := (&coverage.Metric{Module: mod, Profiles: []string{profile(t, "set", "a/a.go:1.1,2.2 1 1")}}).Measure()
	assert.ErrorContains(t, err, "permission denied")
}

func TestMeasureWithoutInputFails(t *testing.T) {
	t.Parallel()
	_, err := (&coverage.Metric{Module: scratchModule(t)}).Measure()
	assert.ErrorContains(t, err, "no cover profile")
}

// fakeConverter writes the profile it was given and records the call.
type fakeConverter struct {
	profile string
	err     error
	gotDirs []string
}

func (f *fakeConverter) TextFormat(coverDir, outFile string) error {
	f.gotDirs = append(f.gotDirs, coverDir)
	if f.err != nil {
		return f.err
	}
	return os.WriteFile(outFile, []byte(f.profile), 0o600)
}

func TestMeasureConvertsCoverDirs(t *testing.T) {
	t.Parallel()
	conv := &fakeConverter{profile: "mode: set\n" + testmod.Path + "/b/b.go:1.1,2.2 2 1\n"}
	m := &coverage.Metric{
		Module: scratchModule(t), CoverDirs: []string{"/cov/e2e"}, Converter: conv, TempDir: t.TempDir(),
		Profiles: []string{profile(t, "set", "b/b.go:3.1,4.2 2 0")},
	}
	s, err := m.Measure()
	require.NoError(t, err)
	assert.Equal(t, []string{"/cov/e2e"}, conv.gotDirs)
	assert.Equal(t, map[string]float64{"./b": 50}, s.Packages)
}

func TestMeasureReportsAConversionFailure(t *testing.T) {
	t.Parallel()
	m := &coverage.Metric{
		Module: scratchModule(t), CoverDirs: []string{"/cov"}, TempDir: t.TempDir(),
		Converter: &fakeConverter{err: errors.New("boom")},
	}
	_, err := m.Measure()
	assert.ErrorContains(t, err, "boom")
}

// The real `go tool covdata`, on a GOCOVERDIR from a binary built with
// -cover: what e2e coverage will feed in.
func TestGoConverterReadsARealCoverDir(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("builds a binary")
	}
	dir := testmod.Write(t, map[string]string{
		"main.go": "package main\n\nimport \"os\"\n\nfunc main() {\n\tif len(os.Args) > 5 {\n\t\tprintln(\"many\")\n\t}\n}\n",
	})
	bin := filepath.Join(t.TempDir(), "bin")
	build := exec.Command("go", "build", "-cover", "-o", bin, ".")
	build.Dir = dir
	build.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
	out, err := build.CombinedOutput()
	require.NoError(t, err, string(out))

	coverDir := t.TempDir()
	run := exec.Command(bin)
	run.Env = append(os.Environ(), "GOCOVERDIR="+coverDir)
	out, err = run.CombinedOutput()
	require.NoError(t, err, string(out))

	mod, err := gosrc.LoadModule(dir)
	require.NoError(t, err)
	m := &coverage.Metric{Module: mod, CoverDirs: []string{coverDir}, Converter: &coverage.GoConverter{}, TempDir: t.TempDir()}
	s, err := m.Measure()
	require.NoError(t, err)
	// Two statements, the if and the println; the println didn't run.
	assert.Equal(t, map[string]float64{".": 50}, s.Packages)
}

func TestGoConverterReportsTheToolsError(t *testing.T) {
	t.Parallel()
	err := (&coverage.GoConverter{}).TextFormat(filepath.Join(t.TempDir(), "missing"), filepath.Join(t.TempDir(), "out"))
	assert.ErrorContains(t, err, "covdata")
}

func snap(total float64, pkgs map[string]float64) *coverage.Snapshot {
	return &coverage.Snapshot{Total: total, Packages: pkgs}
}

func TestCompare(t *testing.T) {
	t.Parallel()
	m := &coverage.Metric{}
	base := snap(80, map[string]float64{"./a": 90, "./b": 70})
	cases := []struct {
		name         string
		cur          *coverage.Snapshot
		regressions  []string
		improvements int
	}{
		{name: "same", cur: snap(80, map[string]float64{"./a": 90, "./b": 70})},
		{name: "within the tolerance", cur: snap(79.9, map[string]float64{"./a": 89.9, "./b": 70})},
		{
			name:        "a package below the tolerance",
			cur:         snap(80, map[string]float64{"./a": 89.8, "./b": 70}),
			regressions: []string{"./a: 89.8%, baseline 90.0% (tolerance 0.1)"},
		},
		{
			name:        "the total below the tolerance",
			cur:         snap(79.8, map[string]float64{"./a": 90, "./b": 70}),
			regressions: []string{"total: 79.8%, baseline 80.0% (tolerance 0.1)"},
		},
		{name: "improved", cur: snap(81, map[string]float64{"./a": 91, "./b": 70}), improvements: 2},
		{
			name:        "a new package under the floor",
			cur:         snap(80, map[string]float64{"./a": 90, "./b": 70, "./c": 79.9}),
			regressions: []string{"./c: 79.9%, a new package, below the 80% floor"},
		},
		{name: "a new package at the floor", cur: snap(80, map[string]float64{"./a": 90, "./b": 70, "./c": 80}), improvements: 1},
		{name: "a package gone", cur: snap(80, map[string]float64{"./a": 90}), improvements: 1},
		{
			name:         "a package in no profile",
			cur:          &coverage.Snapshot{Total: 90, Packages: map[string]float64{"./a": 90}, Unmeasured: map[string]bool{"./b": true}},
			regressions:  []string{"./b: in the source but in no cover profile: a profile of only some packages' tests?"},
			improvements: 1, // the total
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res := m.Compare(base, tc.cur)
			assert.Equal(t, tc.regressions, res.Regressions)
			assert.Len(t, res.Improvements, tc.improvements, res.Improvements)
		})
	}
}

func TestPruneOnlyTightens(t *testing.T) {
	t.Parallel()
	m := &coverage.Metric{}
	base := snap(80, map[string]float64{"./up": 70, "./down": 90, "./gone": 50})
	cur := snap(79.9, map[string]float64{"./up": 75, "./down": 85, "./new": 95, "./weak": 10})
	got := m.Prune(base, cur)
	assert.Equal(t, snap(80, map[string]float64{"./up": 75, "./down": 90, "./new": 95}), got)
}

func TestPruneKeepsAnUnmeasuredPackage(t *testing.T) {
	t.Parallel()
	base := snap(50, map[string]float64{"./a": 90, "./b": 10})
	cur := &coverage.Snapshot{Total: 90, Packages: map[string]float64{"./a": 90}, Unmeasured: map[string]bool{"./b": true, "./c": true}}
	got := (&coverage.Metric{}).Prune(base, cur)
	assert.Equal(t, snap(50, map[string]float64{"./a": 90, "./b": 10}), got)
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	t.Parallel()
	m := &coverage.Metric{}
	s := snap(66.7, map[string]float64{"./b": 50, ".": 100, "./a": 33.3})
	data, err := m.Encode(s)
	require.NoError(t, err)
	body := strings.Join(nonComments(string(data)), "\n")
	assert.Equal(t, ". 100.0\n./a 33.3\n./b 50.0\ntotal 66.7", body)
	got, err := m.Decode(data)
	require.NoError(t, err)
	assert.Equal(t, s, got)
}

func nonComments(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

func TestDecodeRejectsMalformedBaselines(t *testing.T) {
	t.Parallel()
	m := &coverage.Metric{}
	for name, data := range map[string]string{
		"no total":        "./a 50\n",
		"too many fields": "./a 50 x\ntotal 1\n",
		"not a number":    "./a fifty\ntotal 1\n",
	} {
		_, err := m.Decode([]byte(data))
		assert.Error(t, err, name)
	}
}
