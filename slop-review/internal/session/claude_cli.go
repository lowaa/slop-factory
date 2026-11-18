package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lowaa/slop-factory/slop-review/internal/claudecli"
	"github.com/lowaa/slop-factory/slop-review/internal/review"
)

// ClaudeExecutor runs one prompt through the Claude Code CLI.
// *claudecli.Runner satisfies this; tests fake it.
type ClaudeExecutor interface {
	Exec(ctx context.Context, req *claudecli.ExecRequest) (*claudecli.ExecResult, error)
}

// claudeToolNotes maps what the persona prompts say onto what Claude Code
// can actually do; see codexToolNotes.
const claudeToolNotes = `
How to work in this session:
- The repository under review is at %[1]s. Every path in this review is relative to it, but your working directory is elsewhere: give the Read, Grep and Glob tools absolute paths under it, and start shell commands with "cd %[1]s &&".
- The tools named above (get_diff, read_file, list_dir, grep, git_show, git_log, git_blame) are not available here. Use Read, Grep, Glob and your read-only, offline shell instead: "get_diff <path>" is "git diff <the Diffed against ref> -- <path>", which includes uncommitted edits; the others are the obvious git and file commands.
- There is no record_finding or submit_review. Your final answer is the review itself, in the JSON shape you have been given: "verdict", "summary" and every finding in "findings".
`

var claudeReviewSchema = func() []byte {
	b, err := json.Marshal(review.SubmitReviewTool.Parameters)
	if err != nil {
		panic(fmt.Sprintf("session: claude review schema: %v", err))
	}
	return b
}()

// ClaudeCLISession is a Session whose agent loop is Claude Code's: one
// `claude -p` reviews the whole part, with Claude Code's own tools, and
// its structured result is the review.
//
// Like CodexCLISession it never calls Spec.Tools, so secret redaction does
// not apply to what Claude Code reads and Unseen stays the files the
// prompt did not inline.
type ClaudeCLISession struct {
	exec            ClaudeExecutor
	model           string
	reasoningEffort string
	runChecks       bool
	spec            *Spec
	now             func() time.Time

	startedAt time.Time
	ran       bool
	tokens    int
}

func (s *ClaudeCLISession) Run(ctx context.Context) (review.Review, error) {
	s.startedAt = s.now()
	s.ran = true
	res, err := s.exec.Exec(ctx, &claudecli.ExecRequest{
		RepoRoot:        s.spec.RepoRoot,
		Prompt:          s.spec.SystemPrompt + "\n" + fmt.Sprintf(claudeToolNotes, s.spec.RepoRoot) + checkNotes(s.runChecks) + "\n" + s.spec.UserPrompt,
		Schema:          claudeReviewSchema,
		Model:           s.model,
		ReasoningEffort: s.reasoningEffort,
	})
	if err != nil {
		return review.Review{}, cliErr("claude", err)
	}
	s.tokens = res.Tokens

	var r review.Review
	if err := json.Unmarshal(res.JSON, &r); err != nil {
		return review.Review{}, fmt.Errorf("session: claude: decode review: %w", err)
	}
	if err := r.Validate(); err != nil {
		return review.Review{}, fmt.Errorf("session: claude: invalid review: %w", err)
	}

	r.Warnings = append(r.Warnings, s.spec.Tools.Warnings()...)
	if s.model != "" && !ranModel(res.Models, s.model) {
		r.Warnings = append(r.Warnings, fmt.Sprintf("asked Claude Code for model %s, but it reported running %s", s.model, strings.Join(res.Models, ", ")))
	}
	r.Incomplete = len(s.spec.Tools.Unseen()) > 0
	return r, nil
}

// ranModel reports whether want is among the models a run reported. A
// substring match, so an alias ("opus") matches the model it resolved to;
// a run that reported nothing can't be said to have run the wrong one.
func ranModel(reported []string, want string) bool {
	if len(reported) == 0 {
		return true
	}
	for _, m := range reported {
		if strings.Contains(m, want) {
			return true
		}
	}
	return false
}

// Stats has no turns or elisions to report: those happen inside Claude
// Code.
func (s *ClaudeCLISession) Stats() Stats {
	stats := Stats{UnseenFiles: s.spec.Tools.Unseen(), Tokens: s.tokens, Usage: cliUsage(s.tokens)}
	if s.ran {
		stats.Duration = s.now().Sub(s.startedAt)
	}
	return stats
}

// ClaudeCLIFactory makes ClaudeCLISessions sharing one executor.
type ClaudeCLIFactory struct {
	Exec            ClaudeExecutor
	Model           string
	ReasoningEffort string
	// RunChecks must match what Exec's sandbox allows: it only changes
	// what the prompt tells the model it may do.
	RunChecks bool
	// Now nil means time.Now.
	Now func() time.Time
}

func (f *ClaudeCLIFactory) New(spec *Spec) Session {
	now := f.Now
	if now == nil {
		now = time.Now
	}
	return &ClaudeCLISession{exec: f.Exec, model: f.Model, reasoningEffort: f.ReasoningEffort, runChecks: f.RunChecks, spec: spec, now: now}
}
