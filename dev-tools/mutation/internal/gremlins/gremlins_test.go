package gremlins

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sample = `{"go_module":"m","files":[
 {"file_name":"b.go","mutations":[
  {"type":"CONDITIONALS_NEGATION","status":"LIVED","line":9,"column":2},
  {"type":"ARITHMETIC_BASE","status":"KILLED","line":3,"column":1}]},
 {"file_name":"a.go","mutations":[
  {"type":"CONDITIONALS_BOUNDARY","status":"LIVED","line":20,"column":4},
  {"type":"CONDITIONALS_BOUNDARY","status":"LIVED","line":5,"column":7},
  {"type":"INCREMENT_DECREMENT","status":"TIMED OUT","line":1,"column":1},
  {"type":"INVERT_NEGATIVES","status":"NOT COVERED","line":2,"column":1},
  {"type":"INVERT_NEGATIVES","status":"NOT VIABLE","line":3,"column":1}]}]}`

func parseSample(t *testing.T) *Report {
	t.Helper()
	rep, err := Parse(strings.NewReader(sample))
	require.NoError(t, err)
	return rep
}

func TestCountsByStatus(t *testing.T) {
	t.Parallel()
	assert.Equal(t, Counts{Killed: 1, Lived: 3, TimedOut: 1, NotCovered: 1, NotViable: 1}, parseSample(t).Counts())
}

func TestScoreCountsATimeoutAsCaughtAndIgnoresUndecided(t *testing.T) {
	t.Parallel()
	c := parseSample(t).Counts()
	v, ok := c.Score()
	require.True(t, ok)
	assert.InDelta(t, 40.0, v, 0.001, "(1 killed + 1 timed out) of 5 decided")
}

func TestScoreOfNothingDecidedIsNotAScore(t *testing.T) {
	t.Parallel()
	c := Counts{NotCovered: 4}
	_, ok := c.Score()
	assert.False(t, ok)
}

func TestSurvivorsAreSortedByFileThenPosition(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []Survivor{
		{File: "a.go", Line: 5, Column: 7, Type: "CONDITIONALS_BOUNDARY"},
		{File: "a.go", Line: 20, Column: 4, Type: "CONDITIONALS_BOUNDARY"},
		{File: "b.go", Line: 9, Column: 2, Type: "CONDITIONALS_NEGATION"},
	}, parseSample(t).Survivors())
}

func TestParseRejectsGarbage(t *testing.T) {
	t.Parallel()
	_, err := Parse(strings.NewReader("not json"))
	assert.ErrorContains(t, err, "reading the gremlins report")
}

func TestReadFileNamesTheFileOnAParseError(t *testing.T) {
	t.Parallel()
	_, err := ReadFile("/nonexistent/report.json")
	assert.Error(t, err)
}

func TestAddSumsEveryOutcome(t *testing.T) {
	t.Parallel()
	c := Counts{Killed: 1, Lived: 2, TimedOut: 3, NotCovered: 4, NotViable: 5}
	c.Add(Counts{Killed: 10, Lived: 20, TimedOut: 30, NotCovered: 40, NotViable: 50})
	assert.Equal(t, Counts{Killed: 11, Lived: 22, TimedOut: 33, NotCovered: 44, NotViable: 55}, c)
}
