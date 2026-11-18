package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/reviewreport"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Reviews is the slice of reviewreport.Service the slop-review tools use:
// the same service, and so the same rules, as the report routes.
type Reviews interface {
	RequestReview(ctx context.Context, headSHA, intent string) (*reviewreport.Request, error)
	Findings(ctx context.Context, headSHA string) (*reviewreport.HeadReview, error)
	Label(ctx context.Context, findingID uuid.UUID, l *reviewreport.Label) error
}

// notSupervised is review_request's answer to an agent no supervisor runs.
const notSupervised = "No supervisor runs you, so nobody would act on a review request. " +
	"Run slop-review yourself, as the repo's CLAUDE.md (or AGENTS.md) describes, and label its findings with review_label."

func registerReviewTools(server *mcp.Server, h *Handler) {
	addTool(server, h, &mcp.Tool{
		Name: "review_request",
		Description: "Ask the supervisor running you to slop-review the head you pushed, in one pass over the whole " +
			"change. Push first. intent is the change's PR description, " +
			"without your reasoning or suspicions. A new request replaces the last. Then END YOUR TURN: the supervisor " +
			"runs the review and resumes you, and review_findings reads the results. Only for a supervisor's agent " +
			"holding a ticket; anyone else runs slop-review as the repo's CLAUDE.md describes.",
	}, leaseHeld, h.reviewRequest)

	addTool(server, h, &mcp.Tool{
		Name: "review_findings",
		Description: "Read the slop-review runs of the attempt you hold, at head_sha or at the head you last requested a " +
			"review of: each run's verdict or failure, and its findings with their ids and current labels. " +
			"Validate each finding before acting on it (reproduce it, or read the call sites): reviewers produce false " +
			"positives and fabrications. Label each with review_label.",
	}, leaseHeld, h.reviewFindings)

	addTool(server, h, &mcp.Tool{
		Name: "review_label",
		Description: "Label a finding of a slop-review run of the attempt you hold: confirmed (pass fixed_in, the commit " +
			"that fixed it, once it is fixed), false_positive, fabricated, duplicate or wont_fix, with a reason. The newest " +
			"label is the finding's current one. Labels record your judgement; they never clear a blocker or major finding " +
			"for the steward, which approves only a head whose review raised none.",
	}, leaseHeld, h.reviewLabel)
}

type reviewRequestInput struct {
	HeadSHA string `json:"head_sha" jsonschema:"the full 40-character sha of the commit you pushed"`
	Intent  string `json:"intent" jsonschema:"the change's PR description: what it is meant to do"`
}

func (h *Handler) reviewRequest(ctx context.Context, agentID uuid.UUID, in reviewRequestInput) (*mcp.CallToolResult, okOutput, error) {
	req, err := h.reviews.RequestReview(ctx, in.HeadSHA, in.Intent)
	switch {
	case errors.Is(err, reviewreport.ErrNotSupervised):
		return okSays(false, "%s", notSupervised)
	case errors.Is(err, reviewreport.ErrNoHeldAttempt):
		return nil, okOutput{}, h.leaseLost(ctx, agentID, ticket.ErrNoLease)
	case err != nil:
		return nil, okOutput{}, reviewError(err)
	}
	h.progress(ctx, agentID)
	return okSays(true, "Review of %s requested for ticket #%d attempt %d. End your turn now: the supervisor runs the "+
		"review and resumes you with its results, which review_findings reads.", short(req.HeadSHA), req.TicketID, req.Attempt)
}

type reviewFindingsInput struct {
	HeadSHA string `json:"head_sha,omitempty" jsonschema:"the commit to read the runs of; omitted, the head you last requested a review of"`
}

type reviewLabelRef struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason,omitempty"`
	FixedIn string `json:"fixed_in,omitempty"`
}

type reviewFindingRef struct {
	ID       string          `json:"id"`
	Severity string          `json:"severity"`
	File     string          `json:"file"`
	Line     int             `json:"line,omitempty"`
	Summary  string          `json:"summary"`
	Detail   string          `json:"detail"`
	Personas []string        `json:"personas"`
	Label    *reviewLabelRef `json:"label,omitempty" jsonschema:"the current label; absent when nobody has labelled it"`
}

type reviewRunRef struct {
	ID         string             `json:"id"`
	PostedBy   string             `json:"posted_by" jsonschema:"supervisor or agent"`
	Personas   []string           `json:"personas"`
	Status     string             `json:"status" jsonschema:"completed or failed"`
	Verdict    string             `json:"verdict,omitempty"`
	Incomplete bool               `json:"incomplete,omitempty" jsonschema:"part of the change went unreviewed: not an approval"`
	Failure    string             `json:"failure,omitempty" jsonschema:"why a failed run failed"`
	Summary    string             `json:"summary,omitempty"`
	Findings   []reviewFindingRef `json:"findings"`
}

type reviewFindingsOutput struct {
	toolNotes
	TicketID int64          `json:"ticket_id"`
	Attempt  int            `json:"attempt"`
	HeadSHA  string         `json:"head_sha"`
	Runs     []reviewRunRef `json:"runs" jsonschema:"newest first"`
	// Truncated: older runs of the head were left out.
	Truncated bool `json:"truncated,omitempty" jsonschema:"older runs of this head were left out"`
}

func (h *Handler) reviewFindings(ctx context.Context, agentID uuid.UUID, in reviewFindingsInput) (*mcp.CallToolResult, reviewFindingsOutput, error) {
	got, err := h.reviews.Findings(ctx, in.HeadSHA)
	switch {
	case errors.Is(err, reviewreport.ErrNoRequest):
		msg := "You have not requested a review of this attempt: push, then call review_request."
		return text("%s", msg), reviewFindingsOutput{}, nil
	case errors.Is(err, reviewreport.ErrNoHeldAttempt):
		return nil, reviewFindingsOutput{}, h.leaseLost(ctx, agentID, ticket.ErrNoLease)
	case err != nil:
		return nil, reviewFindingsOutput{}, reviewError(err)
	}
	out := reviewFindingsOutput{TicketID: got.TicketID, Attempt: got.Attempt, HeadSHA: got.HeadSHA, Truncated: got.Truncated,
		Runs: make([]reviewRunRef, 0, len(got.Runs))}
	var b strings.Builder
	fmt.Fprintf(&b, "Ticket #%d attempt %d at %s: %d runs.", got.TicketID, got.Attempt, short(got.HeadSHA), len(got.Runs))
	if got.Truncated {
		b.WriteString(" Older runs of this head are left out: only the newest are shown.")
	}
	if len(got.Runs) == 0 {
		b.WriteString(" None yet: if you requested this head, the supervisor has not posted its review.")
	}
	for i := range got.Runs {
		r := runRef(&got.Runs[i])
		out.Runs = append(out.Runs, r)
		writeRun(&b, &r)
	}
	return text("%s", b.String()), out, nil
}

func runRef(r *reviewreport.AttemptRun) reviewRunRef {
	ref := reviewRunRef{ID: r.ID.String(), PostedBy: string(r.Poster.Kind), Personas: r.Personas, Status: r.Status,
		Verdict: r.Verdict, Incomplete: r.Incomplete, Summary: r.Summary, Findings: make([]reviewFindingRef, 0, len(r.Findings))}
	if r.Status == "failed" {
		ref.Failure = strings.TrimSpace(r.FailureClass + ": " + r.FailureMessage)
	}
	for i := range r.Findings {
		f := &r.Findings[i]
		fr := reviewFindingRef{ID: f.ID.String(), Severity: f.Severity, File: f.File, Line: f.Line, Summary: f.Summary,
			Detail: f.Detail, Personas: f.Personas}
		if l := f.Label; l != nil {
			fr.Label = &reviewLabelRef{Verdict: l.Verdict, Reason: l.Reason, FixedIn: l.FixedIn}
		}
		ref.Findings = append(ref.Findings, fr)
	}
	return ref
}

func writeRun(b *strings.Builder, r *reviewRunRef) {
	fmt.Fprintf(b, "\n\nRun %s (%s, posted by %s): ", r.ID, strings.Join(r.Personas, ", "), r.PostedBy)
	switch {
	case r.Status == "failed":
		fmt.Fprintf(b, "FAILED, %s", r.Failure)
	case r.Incomplete:
		fmt.Fprintf(b, "%s, INCOMPLETE (part of the change went unreviewed)", r.Verdict)
	default:
		b.WriteString(r.Verdict)
	}
	if r.Summary != "" {
		fmt.Fprintf(b, "\n%s", r.Summary)
	}
	for _, f := range r.Findings {
		at := f.File
		if f.Line > 0 {
			at = fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		fmt.Fprintf(b, "\n- %s [%s] %s (%s): %s", f.ID, f.Severity, at, strings.Join(f.Personas, ", "), f.Summary)
		if f.Detail != "" {
			fmt.Fprintf(b, "\n  %s", strings.ReplaceAll(f.Detail, "\n", "\n  "))
		}
		if l := f.Label; l != nil {
			fmt.Fprintf(b, "\n  label: %s", l.Verdict)
			if l.FixedIn != "" {
				fmt.Fprintf(b, " in %s", l.FixedIn)
			}
			if l.Reason != "" {
				fmt.Fprintf(b, " -- %s", l.Reason)
			}
		} else {
			b.WriteString("\n  label: none")
		}
	}
}

type reviewLabelInput struct {
	FindingID string `json:"finding_id" jsonschema:"the finding's id, as review_findings shows it"`
	Verdict   string `json:"verdict" jsonschema:"confirmed, false_positive, fabricated, duplicate or wont_fix"`
	Reason    string `json:"reason,omitempty" jsonschema:"why: what you checked, or why it is not worth fixing"`
	FixedIn   string `json:"fixed_in,omitempty" jsonschema:"for confirmed: the commit that fixed it"`
}

func (h *Handler) reviewLabel(ctx context.Context, agentID uuid.UUID, in reviewLabelInput) (*mcp.CallToolResult, okOutput, error) {
	id, err := uuid.Parse(in.FindingID)
	if err != nil {
		return nil, okOutput{}, reviewError(reviewreport.ErrNoFinding)
	}
	if err := h.reviews.Label(ctx, id, &reviewreport.Label{Verdict: in.Verdict, Reason: in.Reason, FixedIn: in.FixedIn}); err != nil {
		return nil, okOutput{}, reviewError(err)
	}
	h.progress(ctx, agentID)
	return okSays(true, "Labelled %s %s.", in.FindingID, in.Verdict)
}

// reviewError says a refusal in the agent's terms; anything else is the
// server's failure, which the shared preamble reports.
func reviewError(err error) error {
	switch {
	case errors.Is(err, reviewreport.ErrNoFinding):
		return errors.New("no such finding among the review runs of the attempt you hold")
	case errors.Is(err, reviewreport.ErrRateLimited):
		return errors.New("too many review calls just now: wait before the next")
	case errors.Is(err, reviewreport.ErrInvalid):
		return errors.New(strings.TrimPrefix(err.Error(), reviewreport.ErrInvalid.Error()+": "))
	}
	return err
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
