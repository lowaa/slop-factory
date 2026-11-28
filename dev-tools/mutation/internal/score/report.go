package score

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/lowaa/slop-factory/dev-tools/mutation/internal/gremlins"
)

// Row is one package's run.
type Row struct {
	Package   string
	Counts    gremlins.Counts
	Survivors []gremlins.Survivor
}

// Total sums the rows' counts.
func Total(rows []*Row) gremlins.Counts {
	var t gremlins.Counts
	for _, r := range rows {
		t.Add(r.Counts)
	}
	return t
}

// Scores is the score of each row that decided at least one mutant: a
// package with none (all not covered) has no score to ratchet.
func Scores(rows []*Row) Snapshot {
	s := Snapshot{}
	for _, r := range rows {
		if v, ok := r.Counts.Score(); ok {
			s[r.Package] = Round(v)
		}
	}
	return s
}

func scoreCell(c *gremlins.Counts) string {
	v, ok := c.Score()
	if !ok {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%%", v)
}

// WriteSummary prints killed, survived, timed out and score per package, and
// the total.
func WriteSummary(w io.Writer, rows []*Row) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "package\tkilled\tsurvived\ttimed out\tnot covered\tscore")
	for _, r := range rows {
		_, _ = fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%s\n", r.Package,
			r.Counts.Killed, r.Counts.Lived, r.Counts.TimedOut, r.Counts.NotCovered, scoreCell(&r.Counts))
	}
	t := Total(rows)
	_, _ = fmt.Fprintf(tw, "total\t%d\t%d\t%d\t%d\t%s\n", t.Killed, t.Lived, t.TimedOut, t.NotCovered, scoreCell(&t))
	_ = tw.Flush()
}

// WriteSurvivors lists every surviving mutant as file:line:column, the
// package's directory first, so each line opens in an editor.
func WriteSurvivors(w io.Writer, rows []*Row) {
	for _, r := range rows {
		for _, s := range r.Survivors {
			_, _ = fmt.Fprintf(w, "%s/%s:%d:%d %s\n", strings.TrimSuffix(r.Package, "/"), s.File, s.Line, s.Column, s.Type)
		}
	}
}
