package regressions

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassify(t *testing.T) {
	own := FailedTest{Name: "TestNew", File: "a/new_test.go"}
	other := FailedTest{Name: "TestOld", File: "b/old_test.go"}
	noFile := FailedTest{Name: "TestMystery"}
	touched := []string{"a/new_test.go"}

	cases := []struct {
		name string
		in   Input
		want Result
	}{
		{"no runs is unknown", Input{}, Result{ChecksPassed: Unknown}},
		{"single pass", Input{Runs: []Run{{Pipeline: 1, Passed: true}}}, Result{ChecksPassed: Yes}},
		{"pass after two failures counts reruns, ignores old failures",
			Input{Runs: []Run{
				{Pipeline: 3, Passed: true},
				{Pipeline: 1, FailedTests: []FailedTest{other}},
				{Pipeline: 2, FailedTests: []FailedTest{other}},
			}, TouchedTestFiles: touched},
			Result{ChecksPassed: Yes, Reruns: 2}},
		{"failures split into regressions and own",
			Input{Runs: []Run{{Pipeline: 1, FailedTests: []FailedTest{own, other, noFile, other}}}, TouchedTestFiles: touched},
			Result{ChecksPassed: No, OwnTestFailures: []FailedTest{own}, Regressions: []FailedTest{other, noFile}}},
		{"path is cleaned before matching",
			Input{Runs: []Run{{Pipeline: 1, FailedTests: []FailedTest{{Name: "TestNew", File: "./a/new_test.go"}}}}, TouchedTestFiles: touched},
			Result{ChecksPassed: No, OwnTestFailures: []FailedTest{{Name: "TestNew", File: "./a/new_test.go"}}}},
		{"nothing touched means all regressions",
			Input{Runs: []Run{{Pipeline: 1, FailedTests: []FailedTest{own}}}},
			Result{ChecksPassed: No, Regressions: []FailedTest{own}}},
		{"failed run without test names is unattributed",
			Input{Runs: []Run{{Pipeline: 1}}},
			Result{ChecksPassed: No, Unattributed: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, &c.want, Classify(&c.in))
		})
	}
}

func TestClassifyDoesNotReorderCallersRuns(t *testing.T) {
	runs := []Run{{Pipeline: 2, Passed: true}, {Pipeline: 1}}
	Classify(&Input{Runs: runs})
	assert.Equal(t, 2, runs[0].Pipeline)
}
