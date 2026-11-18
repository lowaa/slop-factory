package ticket_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func lines(ops ...string) []ticket.DiffLine {
	out := make([]ticket.DiffLine, 0, len(ops))
	for _, op := range ops {
		kind := map[byte]ticket.DiffOp{' ': ticket.DiffEqual, '-': ticket.DiffDelete, '+': ticket.DiffInsert}[op[0]]
		out = append(out, ticket.DiffLine{Op: kind, Text: op[1:]})
	}
	return out
}

func TestDiffLines(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		before, after string
		want          []ticket.DiffLine
	}{
		"identical":             {"a\nb", "a\nb", lines(" a", " b")},
		"both empty":            {"", "", lines()},
		"written from nothing":  {"", "a\nb", lines("+a", "+b")},
		"cleared":               {"a\nb", "", lines("-a", "-b")},
		"one line changed":      {"a\nb\nc", "a\nB\nc", lines(" a", "-b", "+B", " c")},
		"line added in middle":  {"a\nc", "a\nb\nc", lines(" a", "+b", " c")},
		"line removed at start": {"a\nb\nc", "b\nc", lines("-a", " b", " c")},
		// A move is a delete and an insert, around the longest run that
		// stayed put.
		"moved":       {"a\nb\nc\nd", "b\nc\nd\na", lines("-a", " b", " c", " d", "+a")},
		"interleaved": {"x\na\ny\nb\nz", "a\nq\nb", lines("-x", " a", "-y", "+q", " b", "-z")},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, ticket.DiffLines(tc.before, tc.after))
		})
	}
}

// Past the table's size bound the changed middle is reported as replaced
// wholesale -- still a correct diff, just not a minimal one -- rather than
// allocating without limit. The unchanged head and tail are still found.
func TestDiffLines_LargeInputStaysCorrect(t *testing.T) {
	t.Parallel()
	var before, after []string
	before = append(before, "head")
	after = append(after, "head")
	for i := range 2000 {
		before = append(before, "old "+strings.Repeat("x", i%7))
		after = append(after, "new "+strings.Repeat("y", i%5))
	}
	before = append(before, "tail")
	after = append(after, "tail")

	got := ticket.DiffLines(strings.Join(before, "\n"), strings.Join(after, "\n"))

	var rebuiltBefore, rebuiltAfter []string
	for _, l := range got {
		if l.Op != ticket.DiffInsert {
			rebuiltBefore = append(rebuiltBefore, l.Text)
		}
		if l.Op != ticket.DiffDelete {
			rebuiltAfter = append(rebuiltAfter, l.Text)
		}
	}
	assert.Equal(t, before, rebuiltBefore)
	assert.Equal(t, after, rebuiltAfter)
	assert.Equal(t, ticket.DiffLine{Op: ticket.DiffEqual, Text: "head"}, got[0])
	assert.Equal(t, ticket.DiffLine{Op: ticket.DiffEqual, Text: "tail"}, got[len(got)-1])
}
