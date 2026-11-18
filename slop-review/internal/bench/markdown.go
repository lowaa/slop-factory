package bench

import (
	"fmt"
	"strings"
	"time"
)

// RenderMarkdown renders a report as a bug-by-variant table of how often
// each bug was caught, then per-variant totals.
func RenderMarkdown(r *Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Benchmark: %s\n\n", r.Case)

	b.WriteString("| Bug | Kind |")
	for _, v := range r.Variants {
		fmt.Fprintf(&b, " %s |", v.Variant.Name)
	}
	b.WriteString("\n|---|---|")
	for range r.Variants {
		b.WriteString("---|")
	}
	b.WriteString("\n")

	for _, bug := range r.Bugs {
		fmt.Fprintf(&b, "| %s | %s |", bug.ID, bug.Kind)
		for _, v := range r.Variants {
			caught, scored := 0, 0
			for _, run := range v.Runs {
				if run.Score == nil {
					continue
				}
				scored++
				for _, bs := range run.Score.Bugs {
					if bs.ID == bug.ID && bs.Caught {
						caught++
					}
				}
			}
			fmt.Fprintf(&b, " %d/%d |", caught, scored)
		}
		b.WriteString("\n")
	}

	b.WriteString("\n| Variant | Mean caught | Mean findings | Mean unmatched | Failed runs | Incomplete runs | Mean duration |\n|---|---|---|---|---|---|---|\n")
	for _, v := range r.Variants {
		var caught, findings, unmatched, failed, incomplete, scored int
		var duration time.Duration
		for _, run := range v.Runs {
			duration += run.Duration
			if run.Score == nil {
				failed++
				continue
			}
			scored++
			caught += run.Score.Caught
			findings += run.Score.Findings
			unmatched += run.Score.Unmatched
			if run.Score.Incomplete {
				incomplete++
			}
		}
		fmt.Fprintf(&b, "| %s | %s of %d | %s | %s | %d | %d | %s |\n",
			v.Variant.Name, mean(caught, scored), len(r.Bugs), mean(findings, scored), mean(unmatched, scored),
			failed, incomplete, (duration / time.Duration(max(len(v.Runs), 1))).Round(time.Second))
	}
	return b.String()
}

func mean(total, n int) string {
	if n == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f", float64(total)/float64(n))
}
