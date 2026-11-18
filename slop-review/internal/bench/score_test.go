package bench

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/internal/review"
)

func caseWithBugs(bugs ...Bug) *Case {
	return &Case{Name: "c", Bugs: bugs}
}

func TestScoreReview_CatchesWithinLineRange(t *testing.T) {
	c := caseWithBugs(Bug{
		ID:        "b1",
		Locations: []Location{{File: "a.go", StartLine: 10, EndLine: 12}},
	})
	r := &review.Review{Findings: []review.Finding{
		{File: "a.go", Line: 11, Summary: "s"},
	}}

	s := ScoreReview(c, r)
	require.Len(t, s.Bugs, 1)
	assert.True(t, s.Bugs[0].Caught)
	assert.Equal(t, 1, s.Caught)
	assert.Equal(t, 0, s.Unmatched)
}

func TestScoreReview_CatchesWithinWindow(t *testing.T) {
	c := caseWithBugs(Bug{
		ID:        "b1",
		Locations: []Location{{File: "a.go", StartLine: 10, EndLine: 12}},
	})

	// lineWindow is 3: line 7 is StartLine-3, line 15 is EndLine+3. Both
	// should still count as catching the bug.
	for _, line := range []int{7, 15} {
		r := &review.Review{Findings: []review.Finding{
			{File: "a.go", Line: line, Summary: "s"},
		}}
		s := ScoreReview(c, r)
		assert.Truef(t, s.Bugs[0].Caught, "line %d should be within window", line)
	}
}

func TestScoreReview_JustOutsideWindowDoesNotCatch(t *testing.T) {
	c := caseWithBugs(Bug{
		ID:        "b1",
		Locations: []Location{{File: "a.go", StartLine: 10, EndLine: 12}},
	})

	// One past the window on either side.
	for _, line := range []int{6, 16} {
		r := &review.Review{Findings: []review.Finding{
			{File: "a.go", Line: line, Summary: "s"},
		}}
		s := ScoreReview(c, r)
		assert.Falsef(t, s.Bugs[0].Caught, "line %d should be outside window", line)
	}
}

func TestScoreReview_WrongFileDoesNotCatch(t *testing.T) {
	c := caseWithBugs(Bug{
		ID:        "b1",
		Locations: []Location{{File: "a.go", StartLine: 10, EndLine: 12}},
	})
	r := &review.Review{Findings: []review.Finding{
		{File: "b.go", Line: 11, Summary: "s"},
	}}

	s := ScoreReview(c, r)
	assert.False(t, s.Bugs[0].Caught)
	assert.Equal(t, 1, s.Unmatched)
}

func TestScoreReview_LineZeroCatchesOnlyWithKeyword(t *testing.T) {
	c := caseWithBugs(Bug{
		ID:        "b1",
		Locations: []Location{{File: "a.go", StartLine: 10, EndLine: 12}},
		Keywords:  []string{"race"},
	})

	t.Run("no keyword", func(t *testing.T) {
		r := &review.Review{Findings: []review.Finding{
			{File: "a.go", Line: 0, Summary: "something unrelated", Detail: "still unrelated"},
		}}
		s := ScoreReview(c, r)
		assert.False(t, s.Bugs[0].Caught)
	})

	t.Run("keyword in summary, case-insensitive", func(t *testing.T) {
		r := &review.Review{Findings: []review.Finding{
			{File: "a.go", Line: 0, Summary: "possible RACE condition here"},
		}}
		s := ScoreReview(c, r)
		assert.True(t, s.Bugs[0].Caught)
	})

	t.Run("keyword in detail, case-insensitive", func(t *testing.T) {
		r := &review.Review{Findings: []review.Finding{
			{File: "a.go", Line: 0, Summary: "no summary hit", Detail: "this looks like a Race to me"},
		}}
		s := ScoreReview(c, r)
		assert.True(t, s.Bugs[0].Caught)
	})
}

func TestScoreReview_MultipleLocationsSecondLocationCatches(t *testing.T) {
	c := caseWithBugs(Bug{
		ID: "b1",
		Locations: []Location{
			{File: "a.go", StartLine: 10, EndLine: 12},
			{File: "b.go", StartLine: 50, EndLine: 52},
		},
	})
	r := &review.Review{Findings: []review.Finding{
		{File: "b.go", Line: 51, Summary: "s"},
	}}

	s := ScoreReview(c, r)
	assert.True(t, s.Bugs[0].Caught)
}

func TestScoreReview_ByCollectsPersonasWithoutDuplicates(t *testing.T) {
	c := caseWithBugs(Bug{
		ID:        "b1",
		Locations: []Location{{File: "a.go", StartLine: 10, EndLine: 12}},
	})
	r := &review.Review{Findings: []review.Finding{
		{File: "a.go", Line: 11, Summary: "s1", Personas: []string{"standard", "security"}},
		{File: "a.go", Line: 11, Summary: "s2", Personas: []string{"security", "concurrency"}},
	}}

	s := ScoreReview(c, r)
	require.Len(t, s.Bugs, 1)
	assert.ElementsMatch(t, []string{"standard", "security", "concurrency"}, s.Bugs[0].By)
}

func TestScoreReview_UnmatchedCountsFindingsThatMatchedNoBug(t *testing.T) {
	c := caseWithBugs(Bug{
		ID:        "b1",
		Locations: []Location{{File: "a.go", StartLine: 10, EndLine: 12}},
	})
	r := &review.Review{Findings: []review.Finding{
		{File: "a.go", Line: 11, Summary: "catches b1"},
		{File: "z.go", Line: 1, Summary: "matches nothing"},
	}}

	s := ScoreReview(c, r)
	assert.Equal(t, 2, s.Findings)
	assert.Equal(t, 1, s.Unmatched)
	assert.Equal(t, 1, s.Caught)
}

func TestScoreReview_OneFindingMatchingTwoBugsCountsForBothAndIsNotUnmatched(t *testing.T) {
	c := caseWithBugs(
		Bug{ID: "b1", Locations: []Location{{File: "a.go", StartLine: 10, EndLine: 12}}},
		Bug{ID: "b2", Locations: []Location{{File: "a.go", StartLine: 10, EndLine: 12}}},
	)
	r := &review.Review{Findings: []review.Finding{
		{File: "a.go", Line: 11, Summary: "hits both bugs at the same spot"},
	}}

	s := ScoreReview(c, r)
	require.Len(t, s.Bugs, 2)
	assert.True(t, s.Bugs[0].Caught)
	assert.True(t, s.Bugs[1].Caught)
	assert.Equal(t, 2, s.Caught)
	assert.Equal(t, 0, s.Unmatched)
	assert.Equal(t, 1, s.Findings)
}

func TestScoreReview_IncompleteCopiedFromReview(t *testing.T) {
	c := caseWithBugs(Bug{ID: "b1", Locations: []Location{{File: "a.go", StartLine: 1, EndLine: 1}}})

	s := ScoreReview(c, &review.Review{Incomplete: true})
	assert.True(t, s.Incomplete)

	s = ScoreReview(c, &review.Review{Incomplete: false})
	assert.False(t, s.Incomplete)
}

func TestScoreReview_CaughtTotalFindingsCorrect(t *testing.T) {
	c := caseWithBugs(
		Bug{ID: "b1", Locations: []Location{{File: "a.go", StartLine: 10, EndLine: 12}}},
		Bug{ID: "b2", Locations: []Location{{File: "b.go", StartLine: 20, EndLine: 22}}},
	)
	r := &review.Review{Findings: []review.Finding{
		{File: "a.go", Line: 11, Summary: "catches b1"},
		{File: "z.go", Line: 1, Summary: "matches nothing"},
	}}

	s := ScoreReview(c, r)
	assert.Equal(t, 2, s.Total)
	assert.Equal(t, 1, s.Caught)
	assert.Equal(t, 2, s.Findings)
}
