package review

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReview_Validate_Valid(t *testing.T) {
	r := Review{
		Verdict: VerdictApproveWithComments,
		Summary: "Mostly fine, one nit.",
		Findings: []Finding{
			{File: "a.go", Severity: SeverityNit, Summary: "rename this", Detail: "clearer name would help"},
		},
	}
	assert.NoError(t, r.Validate())
}

func TestReview_Validate_ValidWithNoFindings(t *testing.T) {
	r := Review{Verdict: VerdictApprove, Summary: "Looks good."}
	assert.NoError(t, r.Validate())
}

func TestReview_Validate_EmptyReviewIsInvalid(t *testing.T) {
	assert.Error(t, Review{}.Validate(), "a zero-value Review must not be accepted as a valid submission")
}

func TestReview_Validate_UnknownVerdict(t *testing.T) {
	r := Review{Verdict: "looks_great", Summary: "s"}
	assert.ErrorContains(t, r.Validate(), "verdict")
}

func TestReview_Validate_EmptySummary(t *testing.T) {
	r := Review{Verdict: VerdictApprove, Summary: "   "}
	assert.ErrorContains(t, r.Validate(), "summary")
}

func TestReview_Validate_FindingMissingFile(t *testing.T) {
	r := Review{Verdict: VerdictApprove, Summary: "s", Findings: []Finding{
		{Severity: SeverityMinor, Summary: "issue"},
	}}
	assert.ErrorContains(t, r.Validate(), "file")
}

func TestReview_Validate_FindingUnknownSeverity(t *testing.T) {
	r := Review{Verdict: VerdictApprove, Summary: "s", Findings: []Finding{
		{File: "a.go", Severity: "catastrophic", Summary: "issue"},
	}}
	assert.ErrorContains(t, r.Validate(), "severity")
}

func TestReview_Validate_FindingMissingSummary(t *testing.T) {
	r := Review{Verdict: VerdictApprove, Summary: "s", Findings: []Finding{
		{File: "a.go", Severity: SeverityMinor},
	}}
	assert.ErrorContains(t, r.Validate(), "summary")
}

func TestReview_Validate_FindingNegativeLine(t *testing.T) {
	r := Review{Verdict: VerdictApprove, Summary: "s", Findings: []Finding{
		{File: "a.go", Severity: SeverityMinor, Summary: "issue", Line: -1},
	}}
	assert.ErrorContains(t, r.Validate(), "line")
}

func TestReview_Validate_FindingZeroLineIsValid(t *testing.T) {
	// Zero means "no specific line" (see review.RenderMarkdown), not an
	// invalid one.
	r := Review{Verdict: VerdictApprove, Summary: "s", Findings: []Finding{
		{File: "a.go", Severity: SeverityMinor, Summary: "issue", Line: 0},
	}}
	assert.NoError(t, r.Validate())
}

func TestVerdictFor(t *testing.T) {
	assert.Equal(t, VerdictApprove, VerdictFor(nil))
	assert.Equal(t, VerdictApproveWithComments, VerdictFor([]Finding{{Severity: SeverityNit}, {Severity: SeverityMinor}}))
	assert.Equal(t, VerdictRequestChanges, VerdictFor([]Finding{{Severity: SeverityNit}, {Severity: SeverityMajor}}))
	assert.Equal(t, VerdictRequestChanges, VerdictFor([]Finding{{Severity: SeverityBlocker}}))
}

func TestWorseVerdict(t *testing.T) {
	assert.Equal(t, VerdictRequestChanges, WorseVerdict(VerdictApprove, VerdictRequestChanges))
	assert.Equal(t, VerdictRequestChanges, WorseVerdict(VerdictRequestChanges, VerdictApproveWithComments))
	assert.Equal(t, VerdictApproveWithComments, WorseVerdict(VerdictApprove, VerdictApproveWithComments))
	assert.Equal(t, VerdictApprove, WorseVerdict(VerdictApprove, VerdictApprove))
}

func TestWorseSeverity(t *testing.T) {
	assert.Equal(t, SeverityBlocker, WorseSeverity(SeverityNit, SeverityBlocker))
	assert.Equal(t, SeverityMajor, WorseSeverity(SeverityMajor, SeverityMinor))
}

func TestFinding_Validate(t *testing.T) {
	valid := Finding{File: "a.go", Severity: SeverityMinor, Summary: "s"}
	assert.NoError(t, valid.Validate())

	unknown := valid
	unknown.Severity = "catastrophic"
	assert.ErrorContains(t, unknown.Validate(), "severity")

	noFile := valid
	noFile.File = " "
	assert.ErrorContains(t, noFile.Validate(), "file")
}
