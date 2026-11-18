package review

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderMarkdown_NoFindings(t *testing.T) {
	md := RenderMarkdown(Review{Verdict: VerdictApprove, Summary: "Looks good."})

	assert.Contains(t, md, "# Review: Approved")
	assert.Contains(t, md, "Looks good.")
	assert.Contains(t, md, "No findings.")
}

func TestRenderMarkdown_GroupsBySeverityBlockersFirst(t *testing.T) {
	md := RenderMarkdown(Review{
		Verdict: VerdictRequestChanges,
		Findings: []Finding{
			{File: "b.go", Line: 5, Severity: SeverityNit, Summary: "nit here", Detail: "cosmetic"},
			{File: "a.go", Line: 10, Severity: SeverityBlocker, Summary: "resource leak", Detail: "file never closed"},
		},
	})

	blockerIdx := strings.Index(md, "## Blockers")
	nitIdx := strings.Index(md, "## Nits")
	require.NotEqual(t, -1, blockerIdx)
	require.NotEqual(t, -1, nitIdx)
	assert.Less(t, blockerIdx, nitIdx, "blockers section must render before nits regardless of input order")
	assert.Contains(t, md, "a.go:10")
	assert.Contains(t, md, "resource leak")
	assert.Contains(t, md, "file never closed")
}

func TestRenderMarkdown_OmitsLineWhenZero(t *testing.T) {
	md := RenderMarkdown(Review{
		Verdict:  VerdictApproveWithComments,
		Findings: []Finding{{File: "a.go", Severity: SeverityMinor, Summary: "style", Detail: "consider renaming"}},
	})

	assert.Contains(t, md, "**a.go**:")
	assert.NotContains(t, md, "a.go:0")
}

func TestRenderMarkdown_Warnings(t *testing.T) {
	md := RenderMarkdown(Review{
		Verdict:  VerdictApprove,
		Warnings: []string{"read .env despite secret denylist (--allow-secrets)"},
	})

	assert.Contains(t, md, "## Warnings")
	assert.Contains(t, md, "--allow-secrets")
}

func TestRenderMarkdown_AttributesFindingsToPersonas(t *testing.T) {
	out := RenderMarkdown(Review{
		Verdict:  VerdictRequestChanges,
		Summary:  "s",
		Findings: []Finding{{File: "a.go", Line: 3, Severity: SeverityMajor, Summary: "race", Personas: []string{"concurrency", "standard"}}},
	})
	assert.Contains(t, out, "- **a.go:3**: race _(concurrency, standard)_")
}

func TestRenderMarkdown_IncompleteVerdictHasItsOwnHeading(t *testing.T) {
	out := RenderMarkdown(Review{Verdict: VerdictIncomplete, Summary: "s", Incomplete: true})
	assert.Contains(t, out, "# Review: Incomplete\n")
}

func TestRenderMarkdown_FlagsAnIncompleteReview(t *testing.T) {
	out := RenderMarkdown(Review{Verdict: VerdictApprove, Summary: "s", Incomplete: true, Warnings: []string{"part 2 was not reviewed"}})
	assert.Contains(t, out, "# Review: Approved (incomplete")
	assert.Contains(t, out, "part 2 was not reviewed")
}

func TestRenderMarkdown_ShowsFindingID(t *testing.T) {
	id := uuid.New()
	out := RenderMarkdown(Review{
		Verdict:  VerdictRequestChanges,
		Findings: []Finding{{ID: id, File: "a.go", Severity: SeverityMajor, Summary: "x"}, {File: "b.go", Severity: SeverityMajor, Summary: "y"}},
	})

	assert.Contains(t, out, "(id: `"+id.String()+"`)")
	assert.Equal(t, 1, strings.Count(out, "(id:"), "a finding with no id shows none")
}
