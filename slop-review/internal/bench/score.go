package bench

import (
	"strings"

	"github.com/lowaa/slop-factory/slop-review/internal/review"
)

// lineWindow widens every bug location a little: a reviewer pointing at
// the line above the function it means has still found the bug.
const lineWindow = 3

type BugScore struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Caught bool   `json:"caught"`
	// By is every persona credited on a finding that caught the bug.
	By []string `json:"by,omitempty"`
}

// Score is one review of a case, scored.
type Score struct {
	Bugs   []BugScore `json:"bugs"`
	Caught int        `json:"caught"`
	Total  int        `json:"total"`
	// Findings is how many findings the review made; Unmatched how many of
	// them matched no planted bug. Unmatched findings are not necessarily
	// wrong -- the real change may have real issues -- but a variant that
	// raises recall by raising noise needs to show it here.
	Findings   int  `json:"findings"`
	Unmatched  int  `json:"unmatched"`
	Incomplete bool `json:"incomplete"`
}

// ScoreReview scores r against c's planted bugs.
func ScoreReview(c *Case, r *review.Review) *Score {
	s := &Score{Total: len(c.Bugs), Findings: len(r.Findings), Incomplete: r.Incomplete}
	matched := make([]bool, len(r.Findings))
	for _, bug := range c.Bugs {
		bs := BugScore{ID: bug.ID, Kind: bug.Kind}
		for i := range r.Findings {
			if !catches(&bug, &r.Findings[i]) {
				continue
			}
			matched[i] = true
			bs.Caught = true
			bs.By = appendNew(bs.By, r.Findings[i].Personas...)
		}
		if bs.Caught {
			s.Caught++
		}
		s.Bugs = append(s.Bugs, bs)
	}
	for _, m := range matched {
		if !m {
			s.Unmatched++
		}
	}
	return s
}

func catches(bug *Bug, f *review.Finding) bool {
	for _, loc := range bug.Locations {
		if f.File != loc.File {
			continue
		}
		if f.Line > 0 {
			if f.Line >= loc.StartLine-lineWindow && f.Line <= loc.EndLine+lineWindow {
				return true
			}
			continue
		}
		if mentionsAny(f.Summary+" "+f.Detail, bug.Keywords) {
			return true
		}
	}
	return false
}

func mentionsAny(text string, keywords []string) bool {
	text = strings.ToLower(text)
	for _, k := range keywords {
		if strings.Contains(text, strings.ToLower(k)) {
			return true
		}
	}
	return false
}

func appendNew(list []string, items ...string) []string {
	for _, item := range items {
		found := false
		for _, existing := range list {
			if existing == item {
				found = true
				break
			}
		}
		if !found {
			list = append(list, item)
		}
	}
	return list
}
