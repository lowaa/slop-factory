// Package owntests classifies the test files an attempt added or changed by their results at the base and at the head, flagging tests that pass on the base.
package owntests

// Result is the outcome of running one test file at a given commit.
type Result string

const (
	ResultFail        Result = "fail"
	ResultCompileFail Result = "compile-fail"
	ResultPass        Result = "pass"
	ResultDidNotRun   Result = "did-not-run"
	ResultUnknown     Result = "unknown"
)

// Class says what a test file's base/head results show about the attempt.
type Class string

const (
	// ClassFailsOnBase is the good case: the test fails (or does not compile)
	// without the attempt's change and passes with it.
	ClassFailsOnBase Class = "fails-on-base"
	// ClassPassesOnBase proves nothing about the change, so it is flagged.
	ClassPassesOnBase Class = "passes-on-base"
	// ClassDoesNotRun means the file did not run at the base or the head.
	ClassDoesNotRun Class = "does-not-run"
	// ClassUnknown covers missing or unrecognised results, and a test that
	// does not pass at the head.
	ClassUnknown Class = "unknown"
)

// FileRun is the base and head result of one test file the attempt added or changed.
type FileRun struct {
	Path string
	Base Result
	Head Result
}

// Classification is the verdict for one file.
type Classification struct {
	Path    string
	Class   Class
	Flagged bool
}

// Classify judges each file. Only a pass at the head can support a claim, so
// a file that does not pass there is never classed as fails-on-base: a test
// failing on both sides says nothing about the change.
func Classify(runs []FileRun) []Classification {
	out := make([]Classification, 0, len(runs))
	for _, r := range runs {
		c := classify(r)
		out = append(out, Classification{Path: r.Path, Class: c, Flagged: c == ClassPassesOnBase})
	}
	return out
}

func classify(r FileRun) Class {
	switch {
	case r.Base == ResultDidNotRun || r.Head == ResultDidNotRun:
		return ClassDoesNotRun
	case !known(r.Base) || !known(r.Head) || r.Base == ResultUnknown || r.Head == ResultUnknown:
		return ClassUnknown
	case r.Head != ResultPass:
		return ClassUnknown
	case r.Base == ResultPass:
		return ClassPassesOnBase
	default:
		return ClassFailsOnBase
	}
}

func known(r Result) bool {
	switch r {
	case ResultFail, ResultCompileFail, ResultPass, ResultDidNotRun, ResultUnknown:
		return true
	}
	return false
}
