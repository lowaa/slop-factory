package complexity_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/dev-tools/quality/internal/complexity"
	"github.com/lowaa/slop-factory/dev-tools/quality/internal/gosrc"
	"github.com/lowaa/slop-factory/dev-tools/quality/internal/testmod"
)

const source = `package p

func Simple() {}

// Branchy: cyclomatic 1 + if + for + if = 4; cognitive if 1, for 1, the
// nested if 2 = 4.
func Branchy(a int) int {
	if a > 0 {
		return 1
	}
	for i := 0; i < a; i++ {
		if i == 2 {
			return i
		}
	}
	return 0
}

//gocognit:ignore
func Rec(n int) int {
	if n == 0 {
		return 0
	}
	return Rec(n - 1)
}

type T struct{}

func (t *T) M() {}

func (t T) V() {}

type G[K any] struct{}

func (g *G[K]) X() {}
`

// complexFn is a function far over every threshold, which must not count
// wherever it's put below.
var complexFn = func() string {
	var b strings.Builder
	b.WriteString("package p\n\nfunc Huge(a int) int {\n")
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&b, "\tif a == %d {\n\t\treturn %d\n\t}\n", i, i)
	}
	b.WriteString("\treturn 0\n}\n")
	return b.String()
}()

func measure(t *testing.T, files map[string]string) *complexity.Snapshot {
	t.Helper()
	mod, err := gosrc.LoadModule(testmod.Write(t, files))
	require.NoError(t, err)
	s, err := (&complexity.Metric{Module: mod}).Measure()
	require.NoError(t, err)
	return s
}

func byName(s *complexity.Snapshot) map[string]complexity.Function {
	out := map[string]complexity.Function{}
	for _, f := range s.Functions {
		out[f.File+" "+f.Function] = f
	}
	return out
}

func TestMeasuresEachFunction(t *testing.T) {
	t.Parallel()
	s := measure(t, map[string]string{"p/p.go": source})
	fns := byName(s)
	assert.Equal(t, complexity.Function{File: "p/p.go", Function: "Branchy", Lines: 11, Cyclomatic: 4, Cognitive: 4}, fns["p/p.go Branchy"])
	assert.Equal(t, complexity.Function{File: "p/p.go", Function: "Simple", Lines: 1, Cyclomatic: 1, Cognitive: 0}, fns["p/p.go Simple"])
	// Recursion scores a cognitive point; gocognit:ignore doesn't hide it
	// from the distribution.
	assert.Equal(t, 2, fns["p/p.go Rec"].Cognitive)
	assert.Contains(t, fns, "p/p.go (*T).M")
	assert.Contains(t, fns, "p/p.go (T).V")
	assert.Contains(t, fns, "p/p.go (*G).X")
	assert.Equal(t, 6, s.Count)
}

func TestMeasureSkipsWhatIsntTheModulesOwnCode(t *testing.T) {
	t.Parallel()
	s := measure(t, map[string]string{
		"p/p.go":             "package p\n\nfunc Simple() {}\n",
		"p/p_test.go":        complexFn,
		"p/zz_gen.go":        testmod.Generated + complexFn,
		"p/testdata/x.go":    complexFn,
		"vendor/v/v.go":      complexFn,
		".hidden/h.go":       complexFn,
		"_skip/s.go":         complexFn,
		"nested/go.mod":      "module example.com/nested\n",
		"nested/n.go":        complexFn,
		"nested/deeper/d.go": complexFn,
	})
	assert.Equal(t, 1, s.Count, s.Functions)
	assert.Equal(t, 1, s.Cyclomatic.Max)
}

func TestDistribution(t *testing.T) {
	t.Parallel()
	// Ten functions of cyclomatic 1..10, and Huge at 31.
	var b strings.Builder
	b.WriteString("package p\n\n")
	for n := 1; n <= 10; n++ {
		fmt.Fprintf(&b, "func F%d(a int) {\n", n)
		for i := 1; i < n; i++ {
			fmt.Fprintf(&b, "\tif a == %d {\n\t\treturn\n\t}\n", i)
		}
		b.WriteString("}\n\n")
	}
	s := measure(t, map[string]string{"p/p.go": b.String(), "p/huge.go": complexFn})

	assert.Equal(t, 11, s.Count)
	// (55 + 31) / 11
	assert.Equal(t, complexity.Distribution{Threshold: 15, Mean: 7.82, P90: 10, Max: 31, OverThreshold: 1}, s.Cyclomatic)
	assert.Equal(t, complexity.Distribution{Threshold: 20, Mean: 6.82, P90: 9, Max: 30, OverThreshold: 1}, s.Cognitive)
	require.Len(t, s.Longest, complexity.LongestN)
	assert.Equal(t, "Huge", s.Longest[0].Function)
	assert.Equal(t, "F10", s.Longest[1].Function)
}

func TestEmptyModule(t *testing.T) {
	t.Parallel()
	s := measure(t, nil)
	assert.Equal(t, 0, s.Count)
	assert.Empty(t, s.Longest)
}

func dist(mean float64, p90, maxV, over int) complexity.Distribution {
	return complexity.Distribution{Threshold: 15, Mean: mean, P90: p90, Max: maxV, OverThreshold: over}
}

func snap(cyc, cog complexity.Distribution) *complexity.Snapshot {
	return &complexity.Snapshot{Cyclomatic: cyc, Cognitive: cog}
}

func TestCompare(t *testing.T) {
	t.Parallel()
	m := &complexity.Metric{}
	base := snap(dist(3, 6, 12, 1), dist(2, 5, 18, 0))
	cases := []struct {
		name         string
		cur          *complexity.Snapshot
		regressions  []string
		improvements int
	}{
		{name: "same", cur: snap(dist(3, 6, 12, 1), dist(2, 5, 18, 0))},
		{name: "a mean rises within the tolerance", cur: snap(dist(3.1, 6, 12, 1), dist(2, 5, 18, 0))},
		{
			name:        "a mean rises past the tolerance",
			cur:         snap(dist(3.11, 6, 12, 1), dist(2, 5, 18, 0)),
			regressions: []string{"cyclomatic mean: 3.11, baseline 3.00"},
		},
		{
			name: "p90, max and the count rise",
			cur:  snap(dist(3, 6, 12, 1), dist(2, 6, 19, 1)),
			regressions: []string{
				"cognitive p90: 6, baseline 5",
				"cognitive max: 19, baseline 18",
				"cognitive functions over 15: 1, baseline 0",
			},
		},
		{name: "everything falls", cur: snap(dist(2, 5, 11, 0), dist(1, 4, 17, 0)), improvements: 7},
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
	m := &complexity.Metric{}
	base := &complexity.Snapshot{Count: 4, Cyclomatic: dist(3, 6, 12, 1), Cognitive: dist(2, 5, 18, 0)}
	cur := &complexity.Snapshot{
		Count: 9, Cyclomatic: dist(2.5, 7, 10, 2), Cognitive: dist(2.5, 4, 19, 0),
		Longest: []complexity.Function{{File: "a.go", Function: "F", Lines: 3}},
	}
	got := m.Prune(base, cur)
	assert.Equal(t, &complexity.Snapshot{
		Count: 9, Cyclomatic: dist(2.5, 6, 10, 1), Cognitive: dist(2, 4, 18, 0),
		Longest: cur.Longest,
	}, got)
}

func TestEncodeLeavesOutTheFullListAndRoundTrips(t *testing.T) {
	t.Parallel()
	m := &complexity.Metric{}
	s := measure(t, map[string]string{"p/p.go": source})
	data, err := m.Encode(s)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "all_functions")
	got, err := m.Decode(data)
	require.NoError(t, err)
	want := *s
	want.Functions = nil
	assert.Equal(t, &want, got)

	report, err := m.Report(s)
	require.NoError(t, err)
	var full map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(report, &full))
	assert.Contains(t, full, "all_functions")
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	t.Parallel()
	_, err := (&complexity.Metric{}).Decode([]byte(`{"functions": 1, "cyclomatc": {}}`))
	assert.Error(t, err)
}
