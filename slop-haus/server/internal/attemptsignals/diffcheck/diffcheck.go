// Package diffcheck derives cheating and size signals from a unified diff.
// It is pure: callers supply the diff, scopes and generated-file rules.
package diffcheck

import (
	"path"
	"regexp"
	"sort"
	"strings"
)

// Input holds what a check needs. Missing inputs make the dependent signals
// unknown rather than guessed.
type Input struct {
	Diff      string   // git-style unified diff against the base
	Scopes    []string // ticket path scopes, repo relative; a scope covers itself and anything beneath it
	Generated *Generated
}

// Signal is a list-valued finding. Known is false when the inputs needed to
// compute it were missing; Files is then empty and must not be read as "none".
type Signal struct {
	Known bool     `json:"known"`
	Files []string `json:"files"`
}

func (s Signal) Count() int { return len(s.Files) }

type Size struct {
	Known        bool `json:"known"`
	LinesAdded   int  `json:"lines_added"`
	LinesRemoved int  `json:"lines_removed"`
	FilesTouched int  `json:"files_touched"`
	// GeneratedSkipped and GeneratedLines are the files and changed lines left
	// out of the totals as generated. Rules come from the caller's
	// .gitattributes, which a change can itself edit: read them from the base,
	// or treat a large GeneratedLines as a signal in its own right.
	GeneratedSkipped int `json:"generated_skipped"`
	GeneratedLines   int `json:"generated_lines"`
	// GeneratedRulesApplied is false when Input.Generated was nil, so the
	// totals count generated files as hand-written.
	GeneratedRulesApplied bool `json:"generated_rules_applied"`
	// BinaryFiles counts files with no line-countable patch; they add to
	// FilesTouched but contribute zero lines.
	BinaryFiles int `json:"binary_files"`
}

type Result struct {
	TestsDeleted      Signal `json:"tests_deleted"`
	SkipsAdded        Signal `json:"skips_added"`
	AssertionsRemoved Signal `json:"assertions_removed"`
	OutOfScope        Signal `json:"out_of_scope"`
	Size              Size   `json:"size"`
}

var (
	testFileRe = regexp.MustCompile(`(_test\.go|(^|/)test_[^/]*\.py|_test\.py|\.(test|spec)\.[cm]?[jt]sx?|\.bats)$`)
	testDirRe  = regexp.MustCompile(`(^|/)(tests?|__tests__)/`)

	skipRe = regexp.MustCompile(`\bt\.Skip(f|Now)?\(|@pytest\.mark\.skip|\bpytest\.skip\(|@unittest\.skip|\bself\.skipTest\(|\b(it|test|describe)\.(skip|todo)\b|\bx(it|describe|test)\(|^\s*skip(\s|$)`)

	assertRe = regexp.MustCompile(`\b(assert|require)\.\w+\(|\bassert\w*\(|\bassert_\w+|\bself\.assert\w+\(|\bexpect\(|\bt\.(Error|Errorf|Fatal|Fatalf)\(|^\s*assert\s`)
)

func isTestFile(p string) bool { return testFileRe.MatchString(p) || testDirRe.MatchString(p) }

// Check computes the signals. An empty, truncated or unparseable diff makes
// every signal unknown.
func Check(in Input) *Result {
	r := &Result{}
	if strings.TrimSpace(in.Diff) == "" {
		return r
	}
	files, err := parseDiff(in.Diff)
	if err != nil || len(files) == 0 {
		return r // truncated or unrecognisable: unknown, not clean
	}
	r.TestsDeleted.Known, r.SkipsAdded.Known, r.AssertionsRemoved.Known = true, true, true
	r.Size.Known = true
	r.Size.GeneratedRulesApplied = in.Generated != nil
	r.TestsDeleted.Files, r.SkipsAdded.Files, r.AssertionsRemoved.Files = []string{}, []string{}, []string{}

	for _, f := range files {
		if in.Generated.Matches(f.path) {
			r.Size.GeneratedSkipped++
			r.Size.GeneratedLines += f.nAdded + f.nRemove
		} else {
			r.Size.FilesTouched++
			if f.binary {
				r.Size.BinaryFiles++
			}
			r.Size.LinesAdded += f.nAdded
			r.Size.LinesRemoved += f.nRemove
		}
		if !isTestFile(f.path) && !isTestFile(f.oldPath) {
			continue
		}
		if f.deleted {
			r.TestsDeleted.Files = append(r.TestsDeleted.Files, f.path)
			continue
		}
		if countMatching(f.added, skipRe) > countMatching(f.removed, skipRe) {
			r.SkipsAdded.Files = append(r.SkipsAdded.Files, f.path)
		}
		if countMatching(f.removed, assertRe) > countMatching(f.added, assertRe) {
			r.AssertionsRemoved.Files = append(r.AssertionsRemoved.Files, f.path)
		}
	}

	if len(in.Scopes) > 0 {
		r.OutOfScope = Signal{Known: true, Files: []string{}}
		for _, f := range files {
			for _, p := range uniq(f.path, f.oldPath) {
				if !inScope(p, in.Scopes) {
					r.OutOfScope.Files = append(r.OutOfScope.Files, p)
				}
			}
		}
	}
	for _, s := range []*Signal{&r.TestsDeleted, &r.SkipsAdded, &r.AssertionsRemoved, &r.OutOfScope} {
		sort.Strings(s.Files)
		s.Files = dedupe(s.Files)
	}
	return r
}

func countMatching(lines []string, re *regexp.Regexp) int {
	n := 0
	for _, l := range lines {
		if re.MatchString(l) {
			n++
		}
	}
	return n
}

func inScope(p string, scopes []string) bool {
	p = path.Clean(p)
	for _, s := range scopes {
		s = strings.TrimSuffix(path.Clean(s), "/")
		if p == s || strings.HasPrefix(p, s+"/") {
			return true
		}
	}
	return false
}

func uniq(a, b string) []string {
	if a == b {
		return []string{a}
	}
	return []string{a, b}
}

func dedupe(s []string) []string {
	if len(s) < 2 {
		return s
	}
	out := s[:1]
	for _, v := range s[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}
