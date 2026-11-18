package bench

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderMarkdown_PerBugCellIsCaughtOverScored(t *testing.T) {
	report := &Report{
		Case: "the-case",
		Bugs: []Bug{{ID: "b1", Kind: "logic"}},
		Variants: []VariantReport{
			{
				Variant: Variant{Name: "v1"},
				Runs: []RunResult{
					{Score: &Score{Bugs: []BugScore{{ID: "b1", Caught: true}}}},
					{Score: &Score{Bugs: []BugScore{{ID: "b1", Caught: false}}}},
					// A failed run: excluded from "scored", so it must not
					// appear in the denominator.
					{Error: "boom"},
				},
			},
		},
	}

	out := RenderMarkdown(report)
	line := findTableRow(t, out, "| b1 |")
	cells := tableCells(t, line)
	require.Len(t, cells, 3)
	// caught=1, scored=2 (the failed run is excluded from the denominator).
	assert.Equal(t, "1/2", cells[2])
}

func TestRenderMarkdown_SummaryTableFailedRunsAndMeanCaught(t *testing.T) {
	report := &Report{
		Case: "the-case",
		Bugs: []Bug{{ID: "b1"}, {ID: "b2"}},
		Variants: []VariantReport{
			{
				Variant: Variant{Name: "v1"},
				Runs: []RunResult{
					{Score: &Score{Caught: 2, Bugs: []BugScore{{ID: "b1", Caught: true}, {ID: "b2", Caught: true}}}},
					{Score: &Score{Caught: 0, Bugs: []BugScore{{ID: "b1", Caught: false}, {ID: "b2", Caught: false}}}},
					{Error: "boom"},
				},
			},
		},
	}

	out := RenderMarkdown(report)
	line := findTableRow(t, out, "| v1 |")
	cells := tableCells(t, line)
	// Columns: Variant | Mean caught | Mean findings | Mean unmatched | Failed runs | Incomplete runs | Mean duration
	require.Len(t, cells, 7)
	// Mean caught over the two scored runs: (2+0)/2 = 1.0, of 2 bugs.
	assert.Equal(t, "1.0 of 2", cells[1])
	// One run failed, out of the three total.
	assert.Equal(t, "1", cells[4])
}

func TestRenderMarkdown_VariantAllFailedShowsDashForMeans(t *testing.T) {
	report := &Report{
		Case: "the-case",
		Bugs: []Bug{{ID: "b1"}},
		Variants: []VariantReport{
			{
				Variant: Variant{Name: "v1"},
				Runs: []RunResult{
					{Error: "boom"},
					{Error: "boom again"},
				},
			},
		},
	}

	out := RenderMarkdown(report)
	line := findTableRow(t, out, "| v1 |")
	cells := tableCells(t, line)
	require.Len(t, cells, 7)
	assert.Equal(t, "- of 1", cells[1])
	assert.Equal(t, "-", cells[2], "mean findings should be -")
	assert.Equal(t, "-", cells[3], "mean unmatched should be -")
	assert.Equal(t, "2", cells[4], "both runs failed")
}

// findTableRow returns the first line in md that starts with prefix (after
// trimming), failing the test if none is found.
func findTableRow(t *testing.T, md, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return line
		}
	}
	require.Failf(t, "no table row found", "prefix %q not found in:\n%s", prefix, md)
	return ""
}

// tableCells splits a markdown table row like "| a | b | c |" into its
// trimmed cells, dropping the empty strings before the leading and after
// the trailing pipe.
func tableCells(t *testing.T, line string) []string {
	t.Helper()
	parts := strings.Split(line, "|")
	require.True(t, len(parts) >= 2, "not a table row: %q", line)
	inner := parts[1 : len(parts)-1]
	cells := make([]string, len(inner))
	for i, c := range inner {
		cells[i] = strings.TrimSpace(c)
	}
	return cells
}
