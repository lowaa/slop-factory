package session

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lowaa/slop-factory/slop-review/internal/codexcli"
	"github.com/lowaa/slop-factory/slop-review/internal/review"
)

// CodexExecutor runs one prompt through the Codex CLI. *codexcli.Runner
// satisfies this; tests fake it.
type CodexExecutor interface {
	Exec(ctx context.Context, req *codexcli.ExecRequest) (*codexcli.ExecResult, error)
}

// codexToolNotes maps what the persona prompts say onto what Codex can
// actually do. The personas are written for SlopReviewManagedSession's
// tools; restating them here keeps one persona library for both.
const codexToolNotes = `
How to work in this session:
- The tools named above (get_diff, read_file, list_dir, grep, git_show, git_log, git_blame) are not available here. Use your own read-only shell instead: "get_diff <path>" is "git diff <the Diffed against ref> -- <path>", which includes uncommitted edits; the others are the obvious git and file commands.
- There is no record_finding or submit_review. Your final message is the review itself, in the JSON shape you have been given: "verdict", "summary" and every finding in "findings".
- Every finding needs every field. Use 0 for "line" when a finding is not about one line.
`

var codexReviewSchema = func() []byte {
	b, err := json.Marshal(codexcli.StrictSchema(review.SubmitReviewTool.Parameters))
	if err != nil {
		panic(fmt.Sprintf("session: codex review schema: %v", err))
	}
	return b
}()

// CodexCLISession is a Session whose agent loop is Codex's: one `codex
// exec` reviews the whole part, with Codex's own tools, and its final
// message is the review.
//
// It never calls Spec.Tools, so the dispatcher's secret redaction does not
// apply to what Codex reads (the review warns of that), and Unseen stays
// what it was when the session started: the files the prompt did not
// inline. Codex may well
// have read them itself, but nothing here can show it did, so a review
// that relied on it is reported Incomplete rather than assumed whole.
type CodexCLISession struct {
	exec            CodexExecutor
	model           string
	reasoningEffort string
	runChecks       bool
	spec            *Spec
	now             func() time.Time

	startedAt time.Time
	ran       bool
	effort    string
	tokens    int
}

func (s *CodexCLISession) Run(ctx context.Context) (review.Review, error) {
	s.startedAt = s.now()
	s.ran = true
	res, err := s.exec.Exec(ctx, &codexcli.ExecRequest{
		Cwd:             s.spec.RepoRoot,
		Prompt:          s.spec.SystemPrompt + "\n" + codexToolNotes + checkNotes(s.runChecks) + "\n" + s.spec.UserPrompt,
		Schema:          codexReviewSchema,
		Model:           s.model,
		ReasoningEffort: s.reasoningEffort,
	})
	if err != nil {
		return review.Review{}, cliErr("codex", err)
	}

	s.effort = res.Effort
	s.tokens = res.TokensUsed

	var r review.Review
	if err := json.Unmarshal(res.JSON, &r); err != nil {
		return review.Review{}, fmt.Errorf("session: codex: decode review: %w", err)
	}
	if err := r.Validate(); err != nil {
		return review.Review{}, fmt.Errorf("session: codex: invalid review: %w", err)
	}

	r.Warnings = append(r.Warnings, s.spec.Tools.Warnings()...)
	if s.model != "" && res.Model != "" && res.Model != s.model {
		r.Warnings = append(r.Warnings, fmt.Sprintf("asked Codex for model %s, but it reported running %s", s.model, res.Model))
	}
	if s.reasoningEffort != "" && res.Effort != "" && res.Effort != s.reasoningEffort {
		r.Warnings = append(r.Warnings, fmt.Sprintf("asked Codex for reasoning effort %s, but it reported running %s", s.reasoningEffort, res.Effort))
	}
	r.Incomplete = len(s.spec.Tools.Unseen()) > 0
	return r, nil
}

// Stats has no turns or elisions to report: those happen inside Codex.
func (s *CodexCLISession) Stats() Stats {
	stats := Stats{UnseenFiles: s.spec.Tools.Unseen(), Effort: s.effort, Tokens: s.tokens, Usage: cliUsage(s.tokens)}
	if s.ran {
		stats.Duration = s.now().Sub(s.startedAt)
	}
	return stats
}

// CodexCLIFactory makes CodexCLISessions sharing one executor.
type CodexCLIFactory struct {
	Exec            CodexExecutor
	Model           string
	ReasoningEffort string
	// RunChecks must match what Exec's sandbox allows: it only changes
	// what the prompt tells the model it may do.
	RunChecks bool
	// Now nil means time.Now.
	Now func() time.Time
}

func (f *CodexCLIFactory) New(spec *Spec) Session {
	now := f.Now
	if now == nil {
		now = time.Now
	}
	return &CodexCLISession{exec: f.Exec, model: f.Model, reasoningEffort: f.ReasoningEffort, runChecks: f.RunChecks, spec: spec, now: now}
}
