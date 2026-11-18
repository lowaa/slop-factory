package owntests

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name    string
		base    Result
		head    Result
		class   Class
		flagged bool
	}{
		{"fails on base", ResultFail, ResultPass, ClassFailsOnBase, false},
		{"compile-fails on base", ResultCompileFail, ResultPass, ClassFailsOnBase, false},
		{"new test passing on base", ResultPass, ResultPass, ClassPassesOnBase, true},
		{"did not run on base", ResultDidNotRun, ResultPass, ClassDoesNotRun, false},
		{"did not run on head", ResultFail, ResultDidNotRun, ClassDoesNotRun, false},
		{"unknown base", ResultUnknown, ResultPass, ClassUnknown, false},
		{"unrecognised base", Result("bogus"), ResultPass, ClassUnknown, false},
		{"empty head", ResultFail, Result(""), ClassUnknown, false},
		{"compile-fails on head", ResultFail, ResultCompileFail, ClassUnknown, false},
		{"unknown head", ResultFail, ResultUnknown, ClassUnknown, false},
		{"fails on both sides", ResultFail, ResultFail, ClassUnknown, false},
		{"passes base, fails head", ResultPass, ResultFail, ClassUnknown, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify([]FileRun{{Path: "a_test.go", Base: tc.base, Head: tc.head}})
			assert.Equal(t, []Classification{{Path: "a_test.go", Class: tc.class, Flagged: tc.flagged}}, got)
		})
	}
}

func TestClassifyKeepsOrderAndPaths(t *testing.T) {
	got := Classify([]FileRun{
		{Path: "b_test.go", Base: ResultPass, Head: ResultPass},
		{Path: "a_test.go", Base: ResultFail, Head: ResultPass},
	})
	assert.Equal(t, "b_test.go", got[0].Path)
	assert.True(t, got[0].Flagged)
	assert.Equal(t, "a_test.go", got[1].Path)
	assert.False(t, got[1].Flagged)
}

func TestClassifyEmpty(t *testing.T) {
	assert.Empty(t, Classify(nil))
}

// The values are what a producer of results will write, so they are pinned.
func TestWireValues(t *testing.T) {
	assert.Equal(t, []Result{"fail", "compile-fail", "pass", "did-not-run", "unknown"},
		[]Result{ResultFail, ResultCompileFail, ResultPass, ResultDidNotRun, ResultUnknown})
	assert.Equal(t, []Class{"fails-on-base", "passes-on-base", "does-not-run", "unknown"},
		[]Class{ClassFailsOnBase, ClassPassesOnBase, ClassDoesNotRun, ClassUnknown})
}
