// Package review is the structured output shape a slop-review session
// produces: the JSON schema the model submits its final review through (see
// SubmitReviewTool), and the Review/Finding types that JSON decodes into.
package review

import (
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-review/internal/llm"
)

type Severity string

const (
	SeverityBlocker Severity = "blocker"
	SeverityMajor   Severity = "major"
	SeverityMinor   Severity = "minor"
	SeverityNit     Severity = "nit"
)

type Verdict string

const (
	VerdictApprove             Verdict = "approve"
	VerdictApproveWithComments Verdict = "approve_with_comments"
	VerdictRequestChanges      Verdict = "request_changes"
	// VerdictIncomplete is assigned by the orchestrator only, when part of
	// the change went unreviewed and no finding already forces
	// request_changes. Review.Validate rejects it from a model.
	VerdictIncomplete Verdict = "incomplete"
)

// IsApproval reports whether v lets the change through: an incomplete run
// does not, since the part nobody looked at may hold the blocker.
func (v Verdict) IsApproval() bool {
	return v == VerdictApprove || v == VerdictApproveWithComments
}

type Finding struct {
	// ID identifies a merged finding for the lifetime of this review: set
	// by the orchestrator once findings are deduplicated, never by the
	// model. It's what lets the report posted to the report server (see
	// internal/runreport) and the same finding printed on stdout be
	// labelled by the same id later.
	ID       uuid.UUID `json:"id,omitempty"`
	File     string    `json:"file"`
	Line     int       `json:"line,omitempty"`
	Severity Severity  `json:"severity"`
	Summary  string    `json:"summary"`
	Detail   string    `json:"detail"`
	// Personas records which review personas raised this finding. Set by
	// the orchestrator, never by the model.
	Personas []string `json:"personas,omitempty"`
}

// Review is the final, structured output of a review session -- decoded
// from the submit_review tool call's arguments.
type Review struct {
	Verdict  Verdict   `json:"verdict"`
	Summary  string    `json:"summary"`
	Findings []Finding `json:"findings"`
	// Warnings carries operational notices unrelated to the code itself,
	// e.g. a secret-shaped file the session read under --allow-secrets --
	// appended by the toolset dispatcher, not the model.
	Warnings []string `json:"warnings,omitempty"`
	// Incomplete means part of the change went unreviewed: a session
	// failed, or ran out of turns before submitting. The findings are real
	// but the absence of others proves nothing.
	Incomplete bool `json:"incomplete,omitempty"`
}

// Validate reports whether r is a well-formed review: JSON decoding alone
// accepts any shape (an empty object included), and the submit_review tool
// schema's enums aren't enforced server-side across every backend
// OpenRouter can route to -- so internal/session calls this before treating
// a submit_review call as the final answer, rejecting it back to the model
// as a tool error instead of silently reporting an invalid result as a
// successful review.
func (r Review) Validate() error {
	switch r.Verdict {
	case VerdictApprove, VerdictApproveWithComments, VerdictRequestChanges:
	default:
		return fmt.Errorf("verdict %q must be one of %q, %q, %q", r.Verdict, VerdictApprove, VerdictApproveWithComments, VerdictRequestChanges)
	}
	return r.validateBody()
}

// ValidateOutput is Validate for the orchestrator's own output, which may
// carry VerdictIncomplete.
func (r Review) ValidateOutput() error {
	if r.Verdict == VerdictIncomplete {
		return r.validateBody()
	}
	return r.Validate()
}

func (r Review) validateBody() error {
	if strings.TrimSpace(r.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	for i := range r.Findings {
		if err := r.Findings[i].Validate(); err != nil {
			return fmt.Errorf("findings[%d]: %w", i, err)
		}
	}
	return nil
}

// Validate reports whether f is well-formed, for the same reason
// Review.Validate exists: record_finding's schema isn't enforced by every
// backend either.
func (f *Finding) Validate() error {
	if strings.TrimSpace(f.File) == "" {
		return fmt.Errorf("file is required")
	}
	if _, ok := severityRank[f.Severity]; !ok {
		return fmt.Errorf("severity %q must be one of %q, %q, %q, %q", f.Severity, SeverityBlocker, SeverityMajor, SeverityMinor, SeverityNit)
	}
	if strings.TrimSpace(f.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	if f.Line < 0 {
		return fmt.Errorf("line must not be negative, got %d", f.Line)
	}
	return nil
}

// severityRank orders severities worst-first; lower is worse.
var severityRank = map[Severity]int{SeverityBlocker: 0, SeverityMajor: 1, SeverityMinor: 2, SeverityNit: 3}

// WorseSeverity returns whichever of a and b is more severe.
func WorseSeverity(a, b Severity) Severity {
	if severityRank[b] < severityRank[a] {
		return b
	}
	return a
}

var verdictRank = map[Verdict]int{VerdictRequestChanges: 0, VerdictIncomplete: 1, VerdictApproveWithComments: 2, VerdictApprove: 3}

// WorseVerdict returns whichever of a and b is less favourable.
func WorseVerdict(a, b Verdict) Verdict {
	if verdictRank[b] < verdictRank[a] {
		return b
	}
	return a
}

// VerdictFor is the verdict findings alone imply, following
// SubmitReviewTool's own definition of each verdict. Used wherever there is
// no reviewer-chosen verdict to trust: a merged multi-session review, or a
// session that ran out of turns before submitting.
func VerdictFor(findings []Finding) Verdict {
	verdict := VerdictApprove
	for i := range findings {
		switch findings[i].Severity {
		case SeverityBlocker, SeverityMajor:
			return VerdictRequestChanges
		default:
			verdict = VerdictApproveWithComments
		}
	}
	return verdict
}

// SubmitReviewToolName is the terminal tool the session loop watches for:
// once the model calls it, its arguments (matching Review's JSON shape) are
// the final answer and the loop stops.
const SubmitReviewToolName = "submit_review"

var findingSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"file":     map[string]any{"type": "string"},
		"line":     map[string]any{"type": "integer", "description": "1-based line number in file, if applicable."},
		"severity": map[string]any{"type": "string", "enum": []string{string(SeverityBlocker), string(SeverityMajor), string(SeverityMinor), string(SeverityNit)}},
		"summary":  map[string]any{"type": "string", "description": "One-sentence statement of the issue."},
		"detail":   map[string]any{"type": "string", "description": "Why it matters and, if useful, how to fix it."},
	},
	"required": []string{"file", "severity", "summary", "detail"},
}

// RecordFindingToolName lets a session bank findings as it goes, so a
// session that runs out of turns (or context) on a big change still
// reports what it found rather than nothing.
const RecordFindingToolName = "record_finding"

var RecordFindingTool = llm.ToolDefinition{
	Name:        RecordFindingToolName,
	Description: "Record one finding as soon as you have confirmed it, then carry on reviewing. Recorded findings are kept even if the session ends early, and are included in submit_review automatically.",
	Parameters:  findingSchema,
}

// SubmitReviewTool is the tool definition passed to the LLM provider
// alongside the read-only navigation tools.
var SubmitReviewTool = llm.ToolDefinition{
	Name:        SubmitReviewToolName,
	Description: "Submit the finished review. Call this exactly once, after you've gathered whatever context you needed via the read-only tools -- this ends the review session. Findings already recorded with record_finding are included automatically.",
	Parameters: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"verdict": map[string]any{
				"type":        "string",
				"enum":        []string{string(VerdictApprove), string(VerdictApproveWithComments), string(VerdictRequestChanges)},
				"description": "approve: no issues found. approve_with_comments: non-blocking issues found. request_changes: at least one blocker/major finding.",
			},
			"summary": map[string]any{
				"type":        "string",
				"description": "A short (2-4 sentence) prose summary of the change and the review's overall take.",
			},
			"findings": map[string]any{
				"type":        "array",
				"description": "Any findings not already recorded with record_finding. Do not repeat recorded ones.",
				"items":       findingSchema,
			},
		},
		"required": []string{"verdict", "summary", "findings"},
	},
}
