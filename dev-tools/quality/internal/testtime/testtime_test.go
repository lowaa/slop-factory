package testtime_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/dev-tools/quality/internal/gosrc"
	"github.com/lowaa/slop-factory/dev-tools/quality/internal/testmod"
	"github.com/lowaa/slop-factory/dev-tools/quality/internal/testtime"
)

const stream = `{"Time":"2026-01-01T00:00:00Z","Action":"start","Package":"example.com/scratch/a"}
{"Time":"2026-01-01T00:00:01Z","Action":"run","Package":"example.com/scratch/a","Test":"TestFast"}
not json: a line some tool wrote
{"Time":"2026-01-01T00:00:01.5Z","Action":"pass","Package":"example.com/scratch/a","Test":"TestFast","Elapsed":0.004}
{"Time":"2026-01-01T00:00:02Z","Action":"pass","Package":"example.com/scratch/a","Test":"TestSlow/sub","Elapsed":3.5}
{"Time":"2026-01-01T00:00:02Z","Action":"fail","Package":"example.com/scratch/a","Test":"TestSlow","Elapsed":3.6}
{"Time":"2026-01-01T00:00:03Z","Action":"skip","Package":"example.com/scratch/a","Test":"TestSkipped","Elapsed":0}
{"Time":"2026-01-01T00:00:12.25Z","Action":"pass","Package":"example.com/scratch","Test":"TestRoot","Elapsed":1}
{"Time":"2026-01-01T00:00:12.25Z","Action":"pass","Package":"example.com/other","Test":"TestElsewhere","Elapsed":2}
{"Time":"2026-01-01T00:00:12.5Z","Action":"pass","Package":"example.com/scratch/a","Elapsed":12}
`

func writeStream(t *testing.T, s string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "test.json")
	require.NoError(t, os.WriteFile(p, []byte(s), 0o600))
	return p
}

func metric(t *testing.T, s string) *testtime.Metric {
	t.Helper()
	mod, err := gosrc.LoadModule(testmod.Write(t, nil))
	require.NoError(t, err)
	return &testtime.Metric{Module: mod, TestJSON: writeStream(t, s)}
}

func TestMeasure(t *testing.T) {
	t.Parallel()
	s, err := metric(t, stream).Measure()
	require.NoError(t, err)
	assert.InDelta(t, 12.5, s.WallSeconds, 1e-9)
	// Top-level tests that ran to a result, slowest first; subtests are in
	// their parent's time.
	assert.Equal(t, []testtime.Test{
		{Package: "./a", Test: "TestSlow", Seconds: 3.6},
		{Package: "example.com/other", Test: "TestElsewhere", Seconds: 2},
		{Package: ".", Test: "TestRoot", Seconds: 1},
		{Package: "./a", Test: "TestFast", Seconds: 0},
	}, s.Slowest)
}

func TestMeasureKeepsTheSlowestN(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	for i := 0; i < testtime.SlowestN+5; i++ {
		b.WriteString(`{"Action":"pass","Package":"example.com/scratch","Test":"Test` + string(rune('A'+i)) + `","Elapsed":1}` + "\n")
	}
	s, err := metric(t, b.String()).Measure()
	require.NoError(t, err)
	assert.Len(t, s.Slowest, testtime.SlowestN)
	assert.Len(t, s.All, testtime.SlowestN+5)
}

func TestCompareOnlyWarns(t *testing.T) {
	t.Parallel()
	m := &testtime.Metric{}
	base := &testtime.Snapshot{WallSeconds: 10}

	res := m.Compare(base, &testtime.Snapshot{WallSeconds: 15})
	assert.Empty(t, res.Warnings, "exactly 50% over is not over")
	res = m.Compare(base, &testtime.Snapshot{WallSeconds: 15.01})
	assert.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0], "15.01s")
	assert.Empty(t, res.Regressions)
	assert.Empty(t, m.Compare(&testtime.Snapshot{}, &testtime.Snapshot{WallSeconds: 99}).Warnings)
	// Tripled, but by less than WarnMinSeconds: noise.
	assert.Empty(t, m.Compare(&testtime.Snapshot{WallSeconds: 0.4}, &testtime.Snapshot{WallSeconds: 1.2}).Warnings)
}

func TestPruneKeepsTheBaseline(t *testing.T) {
	t.Parallel()
	base := &testtime.Snapshot{WallSeconds: 10}
	assert.Same(t, base, (&testtime.Metric{}).Prune(base, &testtime.Snapshot{WallSeconds: 1}))
}

func TestEncodeLeavesOutTheFullList(t *testing.T) {
	t.Parallel()
	m := &testtime.Metric{}
	s := &testtime.Snapshot{WallSeconds: 2, Slowest: []testtime.Test{{Package: ".", Test: "TestA", Seconds: 1}}}
	s.All = s.Slowest
	data, err := m.Encode(s)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "all_tests")
	got, err := m.Decode(data)
	require.NoError(t, err)
	assert.Equal(t, &testtime.Snapshot{WallSeconds: 2, Slowest: s.Slowest}, got)
}

func TestPrintFailures(t *testing.T) {
	t.Parallel()
	events, err := testtime.ReadEvents(strings.NewReader(`{"Action":"output","Package":"p","Test":"TestOK","Output":"fine\n"}
{"Action":"pass","Package":"p","Test":"TestOK"}
{"Action":"output","Package":"p","Test":"TestBad","Output":"    want 1, got 2\n"}
{"Action":"fail","Package":"p","Test":"TestBad"}
{"Action":"output","Package":"p","Output":"FAIL\tp\n"}
{"Action":"fail","Package":"p"}
{"Action":"build-output","ImportPath":"q","Output":"q/q.go:1:1: syntax error\n"}
`))
	require.NoError(t, err)
	var out strings.Builder
	testtime.PrintFailures(events, &out)
	assert.Equal(t, "    want 1, got 2\nFAIL\tp\nq/q.go:1:1: syntax error\n", out.String())
}
