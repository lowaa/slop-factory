package orchestrator

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-review/internal/gitctx"
	"github.com/lowaa/slop-factory/slop-review/internal/review"
)

// checkFindings drops findings about files that exist neither in the
// repository nor in the change, and clears a line number past the end of
// its file. Reviewers here have reported findings against files that do
// not exist; that is checkable for free, and a fabricated finding costs a
// human far more to disprove than it costs to drop.
func checkFindings(scope *gitctx.Scope, findings []review.Finding) ([]review.Finding, []string) {
	inChange := map[string]bool{}
	for i := range scope.Files {
		inChange[scope.Files[i].Path] = true
		if scope.Files[i].OldPath != "" {
			inChange[scope.Files[i].OldPath] = true
		}
	}

	lineCounts := map[string]int{}
	var kept []review.Finding
	var warnings []string
	for _, f := range findings {
		f.File = resolveFindingPath(scope, f.File, inChange, lineCounts)
		lines, exists := countLines(scope.RepoRoot, f.File, lineCounts)
		if !exists && !inChange[f.File] {
			warnings = append(warnings, fmt.Sprintf("dropped a %s finding about %s, which is in neither the repository nor the change: %q", strings.Join(f.Personas, "/"), f.File, f.Summary))
			continue
		}
		if exists && f.Line > lines {
			f.Detail = strings.TrimSpace(f.Detail + fmt.Sprintf("\n\n(The reviewer cited line %d, past the end of this %d-line file.)", f.Line, lines))
			f.Line = 0
		}
		kept = append(kept, f)
	}
	return kept, warnings
}

// resolveFindingPath maps a reported path onto the repo-root-relative
// path it means. Reviewers are told paths are relative to the repository
// root, but a review run from a subdirectory still gets paths relative to
// that subdirectory back now and then -- and dropping a real finding as
// "nonexistent" is a worse failure than the fabrication the check exists
// to catch. So before giving up on a path, try it under the review's
// scope, then as the unique changed file it is a suffix of.
func resolveFindingPath(scope *gitctx.Scope, reported string, inChange map[string]bool, lineCounts map[string]int) string {
	p := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(reported)), "./")
	if inChange[p] {
		return p
	}
	if _, exists := countLines(scope.RepoRoot, p, lineCounts); exists {
		return p
	}
	if scope.ScopePath != "" {
		scoped := path.Join(scope.ScopePath, p)
		if _, exists := countLines(scope.RepoRoot, scoped, lineCounts); exists || inChange[scoped] {
			return scoped
		}
	}
	match := ""
	for changed := range inChange {
		if strings.HasSuffix(changed, "/"+p) {
			if match != "" {
				return p
			}
			match = changed
		}
	}
	if match != "" {
		return match
	}
	return p
}

// countLines reports how many lines relPath has under root, memoised in
// cache, and whether it exists as a regular file there at all. A path
// escaping the root is treated as nonexistent rather than looked up.
func countLines(root, relPath string, cache map[string]int) (int, bool) {
	if n, ok := cache[relPath]; ok {
		return n, n >= 0
	}
	cache[relPath] = -1
	if filepath.IsAbs(relPath) || relPath == ".." || strings.HasPrefix(relPath, "../") {
		return 0, false
	}
	content, err := os.ReadFile(filepath.Join(root, relPath))
	if err != nil {
		return 0, false
	}
	n := bytes.Count(content, []byte("\n"))
	if len(content) > 0 && content[len(content)-1] != '\n' {
		n++
	}
	cache[relPath] = n
	return n, true
}

// dedupeLineWindow is how close two findings' lines must be to be the same
// issue: reviewers cite the start of a function, the offending statement,
// or the line after it, for the same problem.
const dedupeLineWindow = 3

// dedupeMinSimilarity is the word overlap two summaries need to count as
// the same issue. Deliberately conservative: merging two distinct bugs on
// neighbouring lines loses one, while leaving a duplicate costs a reader a
// few seconds.
const dedupeMinSimilarity = 0.3

// dedupeFarSimilarity is the word overlap at which two summaries in one
// file are the same issue whatever their lines. One race was reported five
// times at different lines by different personas and parts; the line window
// alone never merged them.
const dedupeFarSimilarity = 0.8

// dedupeFarMinWords is how many content words both summaries need before
// far-line merging applies. Short generic wording ("missing error
// handling") is true of many places in a file, and a lower floor at 0.8
// merged distinct findings.
const dedupeFarMinWords = 7

// dedupe merges findings that are the same issue reported by several
// sessions -- the same persona from overlapping context, or different
// personas from different angles -- into one, keeping the most severe
// rating and crediting every persona that raised it. The result is sorted
// most severe first.
func dedupe(findings []review.Finding) []review.Finding {
	var out []review.Finding
	for _, f := range findings {
		merged := false
		for i := range out {
			if sameIssue(&out[i], &f) {
				out[i] = combine(&out[i], &f)
				merged = true
				break
			}
		}
		if !merged {
			out = append(out, f)
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return review.WorseSeverity(out[i].Severity, out[j].Severity) == out[i].Severity
		}
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	// Every finding that survives the merge is identified from here on:
	// stdout prints this id, and internal/runreport sends the same one, so
	// a later `slop-review label <id>` means the same finding on both
	// sides.
	for i := range out {
		out[i].ID = uuid.New()
	}
	return out
}

// sameIssue decides whether two findings are one issue. Identical wording
// is not enough on its own -- "missing error handling" can be true of two
// different places in one file -- so the locations must agree too; a
// finding with no line only merges on identical wording.
func sameIssue(a, b *review.Finding) bool {
	if a.File != b.File {
		return false
	}
	bothLined := a.Line > 0 && b.Line > 0
	if strings.EqualFold(strings.TrimSpace(a.Summary), strings.TrimSpace(b.Summary)) {
		return !bothLined || abs(a.Line-b.Line) <= dedupeLineWindow || farSimilar(a.Summary, b.Summary)
	}
	if bothLined && abs(a.Line-b.Line) <= dedupeLineWindow && similarity(a.Summary, b.Summary) >= dedupeMinSimilarity {
		return true
	}
	return farSimilar(a.Summary, b.Summary)
}

// farSimilar is the line-independent merge: near-identical wording, and
// enough of it to be specific to one place.
func farSimilar(a, b string) bool {
	wa, wb := contentWords(a), contentWords(b)
	if len(wa) < dedupeFarMinWords || len(wb) < dedupeFarMinWords {
		return false
	}
	return similarity(a, b) >= dedupeFarSimilarity
}

// combine keeps the more severe finding's wording, since that is the
// reviewer who took it most seriously, and credits both.
func combine(a, b *review.Finding) review.Finding {
	keep, other := *a, b
	if review.WorseSeverity(a.Severity, b.Severity) != a.Severity {
		keep, other = *b, a
	}
	keep.Personas = unionSorted(a.Personas, b.Personas)
	if len(other.Detail) > 0 && !strings.Contains(keep.Detail, other.Detail) && len(keep.Detail) < len(other.Detail)/2 {
		keep.Detail = other.Detail
	}
	return keep
}

func unionSorted(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string(nil), a...), b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// similarity is the Jaccard overlap of two summaries' content words.
func similarity(a, b string) float64 {
	wa, wb := contentWords(a), contentWords(b)
	if len(wa) == 0 || len(wb) == 0 {
		return 0
	}
	shared := 0
	for w := range wa {
		if wb[w] {
			shared++
		}
	}
	return float64(shared) / float64(len(wa)+len(wb)-shared)
}

var stopwords = map[string]bool{
	"the": true, "and": true, "for": true, "that": true, "this": true, "with": true,
	"not": true, "are": true, "can": true, "but": true, "its": true, "from": true,
	"when": true, "which": true, "into": true, "than": true, "has": true, "was": true,
}

func contentWords(s string) map[string]bool {
	words := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_')
	}) {
		if len(w) >= 3 && !stopwords[w] {
			words[w] = true
		}
	}
	return words
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
