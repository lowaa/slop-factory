package bench

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/internal/review"
)

func TestParseVariant(t *testing.T) {
	tests := []struct {
		spec     string
		wantName string
		wantArgs []string
	}{
		{"name=--a --b=1", "name", []string{"--a", "--b=1"}},
		{"chunked=", "chunked", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.spec, func(t *testing.T) {
			v, err := ParseVariant(tt.spec)
			require.NoError(t, err)
			assert.Equal(t, tt.wantName, v.Name)
			assert.Equal(t, tt.wantArgs, v.Args)
		})
	}
}

func TestParseVariant_Errors(t *testing.T) {
	tests := []string{
		"no-equals-sign",
		"=--a --b",
	}
	for _, spec := range tests {
		t.Run(spec, func(t *testing.T) {
			_, err := ParseVariant(spec)
			require.Error(t, err)
		})
	}
}

// fakeReviewer scripts a Review or error per call, and records every call
// it saw so tests can assert on call order and arguments.
type fakeReviewer struct {
	mu    sync.Mutex
	calls []fakeReviewerCall
	// script maps a variant's args (joined) to the sequence of results it
	// should return, one per call to that variant.
	script map[string][]fakeReviewerResult
	next   map[string]int
}

type fakeReviewerCall struct {
	dir      string
	args     []string
	caseName string
	variant  string
	run      int
}

type fakeReviewerResult struct {
	review *review.Review
	err    error
}

func (f *fakeReviewer) Review(_ context.Context, spec RunSpec) (*review.Review, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeReviewerCall{dir: spec.Dir, args: spec.Args, caseName: spec.Case, variant: spec.Variant, run: spec.Run})

	key := argsKey(spec.Args)
	results := f.script[key]
	i := f.next[key]
	if f.next == nil {
		f.next = map[string]int{}
	}
	f.next[key] = i + 1
	if i >= len(results) {
		return &review.Review{}, nil
	}
	res := results[i]
	return res.review, res.err
}

func argsKey(args []string) string {
	key := ""
	for _, a := range args {
		key += a + "\x00"
	}
	return key
}

func TestRunner_Run_InterleavesVariantsAndPassesArgs(t *testing.T) {
	c := caseWithBugs(Bug{ID: "b1", Locations: []Location{{File: "a.go", StartLine: 1, EndLine: 1}}})
	v1 := Variant{Name: "v1", Args: []string{"--foo"}}
	v2 := Variant{Name: "v2", Args: []string{"--bar"}}

	fr := &fakeReviewer{script: map[string][]fakeReviewerResult{
		argsKey(v1.Args): {{review: &review.Review{}}, {review: &review.Review{}}},
		argsKey(v2.Args): {{review: &review.Review{}}, {review: &review.Review{}}},
	}}

	r := NewRunner(fr, slog.DiscardHandler)
	report := r.Run(context.Background(), c, "/repo", []Variant{v1, v2}, 2)

	require.Len(t, fr.calls, 4)
	// Call order must be v1, v2, v1, v2 -- runs interleaved, not run back
	// to back.
	assert.Equal(t, []string{"--foo"}, fr.calls[0].args)
	assert.Equal(t, []string{"--bar"}, fr.calls[1].args)
	assert.Equal(t, []string{"--foo"}, fr.calls[2].args)
	assert.Equal(t, []string{"--bar"}, fr.calls[3].args)
	for _, call := range fr.calls {
		assert.Equal(t, "/repo", call.dir)
	}

	require.Len(t, report.Variants, 2)
	assert.Equal(t, v1, report.Variants[0].Variant)
	assert.Equal(t, v2, report.Variants[1].Variant)
	assert.Len(t, report.Variants[0].Runs, 2)
	assert.Len(t, report.Variants[1].Runs, 2)
}

// TestRunner_Run_PassesCaseVariantAndRunNumberToReviewer checks that a
// Reviewer sees which case, variant and run number each call belongs to --
// what lets slop-review tag a benchmark run as Kind bench (report.Bench)
// rather than a plain review when it posts to a server.
func TestRunner_Run_PassesCaseVariantAndRunNumberToReviewer(t *testing.T) {
	c := caseWithBugs(Bug{ID: "b1", Locations: []Location{{File: "a.go", StartLine: 1, EndLine: 1}}})
	c.Name = "my-case"
	v1 := Variant{Name: "v1", Args: []string{"--foo"}}
	v2 := Variant{Name: "v2", Args: []string{"--bar"}}

	fr := &fakeReviewer{script: map[string][]fakeReviewerResult{
		argsKey(v1.Args): {{review: &review.Review{}}, {review: &review.Review{}}},
		argsKey(v2.Args): {{review: &review.Review{}}, {review: &review.Review{}}},
	}}

	r := NewRunner(fr, slog.DiscardHandler)
	r.Run(context.Background(), c, "/repo", []Variant{v1, v2}, 2)

	require.Len(t, fr.calls, 4)
	// Call order is v1, v2, v1, v2 (see TestRunner_Run_InterleavesVariantsAndPassesArgs).
	wantRuns := []int{1, 1, 2, 2}
	wantVariants := []string{"v1", "v2", "v1", "v2"}
	for i, call := range fr.calls {
		assert.Equal(t, "my-case", call.caseName)
		assert.Equal(t, wantVariants[i], call.variant)
		assert.Equal(t, wantRuns[i], call.run)
	}
}

func TestRunner_Run_ErrorBecomesRunResultError(t *testing.T) {
	c := caseWithBugs(Bug{ID: "b1", Locations: []Location{{File: "a.go", StartLine: 1, EndLine: 1}}})
	v := Variant{Name: "v1"}
	wantErr := errors.New("boom")

	fr := &fakeReviewer{script: map[string][]fakeReviewerResult{
		argsKey(v.Args): {{err: wantErr}},
	}}

	r := NewRunner(fr, slog.DiscardHandler)
	report := r.Run(context.Background(), c, "/repo", []Variant{v}, 1)

	require.Len(t, report.Variants, 1)
	require.Len(t, report.Variants[0].Runs, 1)
	run := report.Variants[0].Runs[0]
	assert.Nil(t, run.Score)
	assert.Equal(t, "boom", run.Error)
}

// TestRunner_Run_ScoresACaughtBugOnTheRightVariantAndRun checks that Run
// doesn't just plumb a Reviewer's output through unscored: a finding that
// matches a planted bug must turn into a Score with Caught/Bugs actually
// filled in, and it must land on the specific variant/run that produced it
// -- not just anywhere in the report -- since a runner that scored every
// run against, say, the last result seen would pass a test that only
// checked *some* run somewhere came back caught.
func TestRunner_Run_ScoresACaughtBugOnTheRightVariantAndRun(t *testing.T) {
	c := caseWithBugs(Bug{ID: "b1", Kind: "concurrency", Locations: []Location{{File: "a.go", StartLine: 10, EndLine: 12}}})
	v1 := Variant{Name: "v1", Args: []string{"--v1"}}
	v2 := Variant{Name: "v2", Args: []string{"--v2"}}

	catches := &review.Review{Findings: []review.Finding{
		{File: "a.go", Line: 11, Severity: review.SeverityMajor, Summary: "race", Detail: "d"},
	}}
	misses := &review.Review{Findings: []review.Finding{
		{File: "other.go", Line: 1, Severity: review.SeverityNit, Summary: "unrelated", Detail: "d"},
	}}

	fr := &fakeReviewer{script: map[string][]fakeReviewerResult{
		// v1 catches the bug on its first run only, misses on the second.
		argsKey(v1.Args): {{review: catches}, {review: misses}},
		// v2 never catches it.
		argsKey(v2.Args): {{review: misses}, {review: misses}},
	}}

	r := NewRunner(fr, slog.DiscardHandler)
	report := r.Run(context.Background(), c, "/repo", []Variant{v1, v2}, 2)

	require.Len(t, report.Variants, 2)
	v1Runs := report.Variants[0].Runs
	require.Len(t, v1Runs, 2)

	require.NotNil(t, v1Runs[0].Score, "v1 run 1 should have been scored")
	assert.Equal(t, 1, v1Runs[0].Score.Caught)
	assert.Equal(t, 1, v1Runs[0].Score.Total)
	require.Len(t, v1Runs[0].Score.Bugs, 1)
	assert.Equal(t, "b1", v1Runs[0].Score.Bugs[0].ID)
	assert.True(t, v1Runs[0].Score.Bugs[0].Caught, "v1 run 1's finding was inside the bug's line window")

	require.NotNil(t, v1Runs[1].Score, "v1 run 2 should have been scored too, just not caught")
	assert.Equal(t, 0, v1Runs[1].Score.Caught)
	assert.False(t, v1Runs[1].Score.Bugs[0].Caught)

	v2Runs := report.Variants[1].Runs
	require.Len(t, v2Runs, 2)
	for i, run := range v2Runs {
		require.NotNilf(t, run.Score, "v2 run %d should have been scored", i+1)
		assert.Equalf(t, 0, run.Score.Caught, "v2 run %d never caught the bug", i+1)
	}
}

func TestRunner_Run_ReportHasOneEntryPerVariantWithRunsResults(t *testing.T) {
	c := caseWithBugs(Bug{ID: "b1", Locations: []Location{{File: "a.go", StartLine: 1, EndLine: 1}}})
	v1 := Variant{Name: "v1"}
	v2 := Variant{Name: "v2"}
	v3 := Variant{Name: "v3"}

	fr := &fakeReviewer{}
	r := NewRunner(fr, slog.DiscardHandler)
	report := r.Run(context.Background(), c, "/repo", []Variant{v1, v2, v3}, 3)

	require.Len(t, report.Variants, 3)
	for _, vr := range report.Variants {
		assert.Len(t, vr.Runs, 3)
	}
}
