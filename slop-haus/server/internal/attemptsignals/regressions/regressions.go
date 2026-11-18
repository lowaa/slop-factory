// Package regressions classifies an attempt's CI results into the signals
// shown per attempt: did checks pass, how many reruns it took, and whether
// the failures are regressions or failures in tests the attempt itself wrote.
package regressions

import (
	"path"
	"sort"
)

// Tri is a yes/no answer that may be unknowable.
type Tri string

const (
	Yes     Tri = "yes"
	No      Tri = "no"
	Unknown Tri = "unknown"
)

// FailedTest identifies one failing test. File may be empty when the CI log
// does not say where the test lives.
type FailedTest struct {
	Name string
	File string
}

// Run is one CI pipeline run for the attempt (for a shadow attempt, a run
// against the base: the caller decides which runs belong to the attempt).
type Run struct {
	Pipeline    int
	Passed      bool
	FailedTests []FailedTest
}

type Input struct {
	Runs []Run
	// TouchedTestFiles are the test files the attempt added or changed.
	TouchedTestFiles []string
}

type Result struct {
	ChecksPassed Tri
	// Reruns counts runs after the first one.
	Reruns int
	// Regressions are failures in tests the attempt did not touch.
	Regressions []FailedTest
	// OwnTestFailures are failures in test files the attempt added or changed.
	OwnTestFailures []FailedTest
	// Unattributed is set when the latest run failed without naming a failed
	// test, so the failure can be neither a regression nor an own failure.
	Unattributed bool
}

// Classify judges the attempt by its latest run (highest pipeline number):
// failures that a rerun cleared are flakes, not the attempt's state.
// Failures without a file are regressions: a test we cannot place was not
// shown to be the attempt's own.
func Classify(in *Input) *Result {
	res := &Result{ChecksPassed: Unknown}
	if len(in.Runs) == 0 {
		return res
	}
	runs := append([]Run(nil), in.Runs...)
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].Pipeline < runs[j].Pipeline })
	latest := runs[len(runs)-1]
	res.Reruns = len(runs) - 1
	if latest.Passed {
		res.ChecksPassed = Yes
		return res
	}
	res.ChecksPassed = No

	touched := make(map[string]bool, len(in.TouchedTestFiles))
	for _, f := range in.TouchedTestFiles {
		touched[path.Clean(f)] = true
	}
	seen := map[FailedTest]bool{}
	for _, ft := range latest.FailedTests {
		if seen[ft] {
			continue
		}
		seen[ft] = true
		if ft.File != "" && touched[path.Clean(ft.File)] {
			res.OwnTestFailures = append(res.OwnTestFailures, ft)
		} else {
			res.Regressions = append(res.Regressions, ft)
		}
	}
	res.Unattributed = len(latest.FailedTests) == 0
	return res
}
