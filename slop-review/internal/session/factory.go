package session

import (
	"context"

	"github.com/lowaa/slop-factory/slop-review/internal/llm"
	"github.com/lowaa/slop-factory/slop-review/internal/review"
)

// Session is one reviewer's pass over its part of a change.
type Session interface {
	// Run reviews to completion. A session that ran out of road but has
	// something to show returns it as an Incomplete review rather than an
	// error.
	Run(ctx context.Context) (review.Review, error)
	// Stats is read after Run returns, whether or not it succeeded.
	Stats() Stats
}

// Spec is what one session is asked to review: the same for every
// implementation, whoever ends up driving the agent loop.
type Spec struct {
	// RepoRoot is the repository toplevel every path in the prompts, and
	// every path Tools takes, is relative to.
	RepoRoot string
	// SystemPrompt is the persona; UserPrompt the diff and repo metadata.
	SystemPrompt string
	UserPrompt   string
	// Tools is this session's own dispatcher, already told which files
	// the prompt inlined, so its Unseen is what the session still owes.
	Tools ToolDispatcher
}

// Factory makes the sessions a review runs, so the orchestrator decides
// what to review and the factory decides what reviews it.
type Factory interface {
	New(spec *Spec) Session
}

// SlopReviewManagedFactory makes SlopReviewManagedSessions over one
// provider. The provider is shared across sessions, so must be safe for
// concurrent use.
type SlopReviewManagedFactory struct {
	Provider           llm.LLMProvider
	Model              string
	MaxTurns           int
	ContextBudgetBytes int
}

func (f *SlopReviewManagedFactory) New(spec *Spec) Session {
	return New(&Options{
		Provider:           f.Provider,
		Model:              f.Model,
		Tools:              spec.Tools,
		MaxTurns:           f.MaxTurns,
		ContextBudgetBytes: f.ContextBudgetBytes,
		SystemPrompt:       spec.SystemPrompt,
		UserPrompt:         spec.UserPrompt,
	})
}
