package ticket

import "strings"

// DiffOp is what a line of a diff does: stays, goes, or arrives.
type DiffOp string

const (
	DiffEqual  DiffOp = "equal"
	DiffDelete DiffOp = "delete"
	DiffInsert DiffOp = "insert"
)

type DiffLine struct {
	Op   DiffOp
	Text string
}

// maxDiffCells bounds the LCS table (4 bytes a cell, so 4MB). A description
// is a few KB, a few hundred lines at most; past this the changed middle is
// reported as wholly replaced rather than costing the request an
// unbounded allocation.
const maxDiffCells = 1 << 20

// DiffLines is a line-level diff of two texts, for displaying a revision.
// Revisions store full text, so this runs when one is shown, never when one
// is saved.
//
// A plain longest-common-subsequence diff: the common head and tail are
// trimmed first, which leaves the usual edit -- a few lines changed in
// place -- a tiny table. Within a changed run, deletions come before
// insertions, the order a reader expects.
func DiffLines(before, after string) []DiffLine {
	a, b := splitLines(before), splitLines(after)

	head := 0
	for head < len(a) && head < len(b) && a[head] == b[head] {
		head++
	}
	tail := 0
	for tail < len(a)-head && tail < len(b)-head && a[len(a)-1-tail] == b[len(b)-1-tail] {
		tail++
	}

	out := make([]DiffLine, 0, len(a)+len(b)-head-tail)
	for _, line := range a[:head] {
		out = append(out, DiffLine{Op: DiffEqual, Text: line})
	}
	out = append(out, diffMiddle(a[head:len(a)-tail], b[head:len(b)-tail])...)
	for _, line := range a[len(a)-tail:] {
		out = append(out, DiffLine{Op: DiffEqual, Text: line})
	}
	return out
}

// splitLines treats the empty text as no lines at all, so a description
// written from nothing diffs as pure insertion rather than as an empty line
// replaced.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func diffMiddle(a, b []string) []DiffLine {
	out := make([]DiffLine, 0, len(a)+len(b))
	if len(a) == 0 || len(b) == 0 || (len(a)+1)*(len(b)+1) > maxDiffCells {
		for _, line := range a {
			out = append(out, DiffLine{Op: DiffDelete, Text: line})
		}
		for _, line := range b {
			out = append(out, DiffLine{Op: DiffInsert, Text: line})
		}
		return out
	}

	// lcs[i][j] is the length of the longest common subsequence of a[i:]
	// and b[j:], so the walk below can go forwards.
	width := len(b) + 1
	lcs := make([]int32, (len(a)+1)*width)
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i*width+j] = lcs[(i+1)*width+j+1] + 1
			} else {
				lcs[i*width+j] = max(lcs[(i+1)*width+j], lcs[i*width+j+1])
			}
		}
	}

	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, DiffLine{Op: DiffEqual, Text: a[i]})
			i++
			j++
		case lcs[(i+1)*width+j] >= lcs[i*width+j+1]:
			out = append(out, DiffLine{Op: DiffDelete, Text: a[i]})
			i++
		default:
			out = append(out, DiffLine{Op: DiffInsert, Text: b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		out = append(out, DiffLine{Op: DiffDelete, Text: a[i]})
	}
	for ; j < len(b); j++ {
		out = append(out, DiffLine{Op: DiffInsert, Text: b[j]})
	}
	return out
}
