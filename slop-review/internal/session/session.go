// Package session runs one review: the bounded tool-calling loop between
// an llm.LLMProvider and a read-only ToolDispatcher, terminated by the
// model calling review.SubmitReviewTool rather than by free-text parsing.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	llmusage "github.com/lowaa/slop-factory/go-llm-usage"

	"github.com/lowaa/slop-factory/slop-review/internal/llm"
	"github.com/lowaa/slop-factory/slop-review/internal/review"
)

// ToolDispatcher is the read-only navigation toolset a Session drives.
// internal/toolset.Dispatcher satisfies this.
type ToolDispatcher interface {
	Definitions() []llm.ToolDefinition
	Dispatch(name, argumentsJSON string) string
	Warnings() []string
	// Unseen names the files the session was answerable for but never
	// shown in full.
	Unseen() []string
}

// ErrNoReview means the session ended with neither a submitted review nor
// any recorded findings -- nothing that could honestly be reported.
var ErrNoReview = errors.New("session: no review")

type Options struct {
	Provider llm.LLMProvider
	Model    string
	Tools    ToolDispatcher
	MaxTurns int
	// ContextBudgetBytes bounds the conversation resent on every turn. Once
	// it is exceeded, the oldest tool results are elided (see elide). Zero
	// means unbounded.
	ContextBudgetBytes int
	// SystemPrompt is the persona; UserPrompt the diff and repo metadata.
	SystemPrompt string
	UserPrompt   string
	// Now nil means time.Now; tests fix it to time tool calls exactly.
	Now func() time.Time
}

type SlopReviewManagedSession struct {
	provider      llm.LLMProvider
	model         string
	dispatcher    ToolDispatcher
	maxTurns      int
	contextBudget int
	messages      []llm.Message
	// recorded is every finding banked through record_finding, kept apart
	// from the conversation so eliding context can never lose one.
	recorded []review.Finding

	// startedAt, turnsUsed, usage and tools are telemetry gathered whether
	// or not the session ends cleanly -- see Stats, which the orchestrator
	// reads after Run returns either way. elisions is incremented by elide
	// (elide.go), same package.
	now       func() time.Time
	startedAt time.Time
	turnsUsed int
	elisions  int
	usage     llm.Usage
	tools     map[string]*llmusage.ToolUsage
	// offered is the tools the model was given, by name.
	offered map[string]bool
}

// UnknownTool is the name a call to a tool the session never offered is
// counted under. The model chooses the name, and the report must stay
// valid and bounded whatever it makes up.
const UnknownTool = "unknown_tool"

// Stats is one session's telemetry: what it cost and how it went,
// independent of whether it produced a usable review. Read after Run
// returns, success or failure alike.
type Stats struct {
	Turns       int
	Elisions    int
	UnseenFiles []string
	// Usage sums every completion's, failed turns' requests included.
	Usage llm.Usage
	// Tools is every tool the model called, by name, sorted: the
	// navigation tools and record_finding/submit_review alike.
	Tools    []llmusage.ToolUsage
	Duration time.Duration
	// Effort is the reasoning effort a CLI-driven session ran at, and
	// Tokens the total it reported; the built-in loop leaves both zero
	// (its cost is in Usage).
	Effort string
	Tokens int
}

// Stats reports s's telemetry as of now. Call after Run returns.
func (s *SlopReviewManagedSession) Stats() Stats {
	tools := make([]llmusage.ToolUsage, 0, len(s.tools))
	for _, t := range s.tools {
		tools = append(tools, *t)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Tool < tools[j].Tool })
	return Stats{
		Turns:       s.turnsUsed,
		Elisions:    s.elisions,
		UnseenFiles: s.dispatcher.Unseen(),
		Usage:       s.usage,
		Tools:       tools,
		Duration:    s.now().Sub(s.startedAt),
	}
}

// New seeds a session with the persona and the review input as the first
// two messages.
func New(opts *Options) *SlopReviewManagedSession {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &SlopReviewManagedSession{
		now:           now,
		provider:      opts.Provider,
		model:         opts.Model,
		dispatcher:    opts.Tools,
		tools:         map[string]*llmusage.ToolUsage{},
		maxTurns:      opts.MaxTurns,
		contextBudget: opts.ContextBudgetBytes,
		messages: []llm.Message{
			{Role: llm.RoleSystem, Content: opts.SystemPrompt},
			{Role: llm.RoleUser, Content: opts.UserPrompt},
		},
	}
}

// Run drives the loop to completion: each turn, the model either calls
// navigation tools (dispatched and fed back as tool-result messages),
// records findings, or calls submit_review, which ends the loop.
//
// If the model never submits within maxTurns, whatever it recorded is
// returned as an Incomplete review rather than discarded; only a session
// with nothing at all to show returns ErrNoReview.
func (s *SlopReviewManagedSession) Run(ctx context.Context) (review.Review, error) {
	s.startedAt = s.now()
	toolDefs := append(s.dispatcher.Definitions(), review.RecordFindingTool, review.SubmitReviewTool)
	s.offered = make(map[string]bool, len(toolDefs))
	for _, d := range toolDefs {
		s.offered[d.Name] = true
	}

	for turn := 1; turn <= s.maxTurns; turn++ {
		s.turnsUsed = turn
		// On the last turn, stop letting the model keep exploring: make
		// submit_review the only call it can make, so a heavy explorer
		// yields the review it has instead of the whole session (and its
		// cost) being thrown away for want of one more turn.
		forceToolName := ""
		if turn == s.maxTurns {
			forceToolName = review.SubmitReviewToolName
			s.messages = append(s.messages, llm.Message{
				Role:    llm.RoleUser,
				Content: "This is the final turn - no more context gathering is possible. Call submit_review now with the review you have.",
			})
		}

		s.elide()
		resp, err := s.provider.Complete(ctx, llm.CompletionRequest{
			Model:         s.model,
			Messages:      s.messages,
			Tools:         toolDefs,
			ForceToolName: forceToolName,
		})
		// Before the error check: a failed turn's requests were sent.
		s.usage.Add(&resp.Usage)
		if err != nil {
			return s.partial(fmt.Errorf("session: turn %d: %w", turn, err))
		}
		s.messages = append(s.messages, resp.Message)

		if len(resp.Message.ToolCalls) == 0 {
			// The provider is asked to force tool use (see
			// OpenAIAPILLMProvider.Complete's tool_choice), but OpenRouter
			// can route to backends that don't honor that -- nudge and let
			// the turn loop's maxTurns bound be the backstop, rather than
			// discarding whatever review progress this session has made.
			s.messages = append(s.messages, llm.Message{
				Role:    llm.RoleUser,
				Content: "You didn't call a tool. If you need more context, call one of the read-only tools; once you're ready, call submit_review with your finished review.",
			})
			continue
		}

		final := s.handleToolCalls(resp.Message.ToolCalls)
		if final != nil {
			// A reviewer that recorded a blocker many turns ago and then
			// submitted "approve" may have lost track of it; the finding is
			// the more specific claim, so it wins. Findings submitted
			// alongside the verdict were weighed with it, so they don't.
			final.Verdict = review.WorseVerdict(final.Verdict, review.VerdictFor(s.recorded))
			final.Findings = append(append([]review.Finding(nil), s.recorded...), final.Findings...)
			final.Warnings = append(final.Warnings, s.dispatcher.Warnings()...)
			// A verdict on files the reviewer never looked at is not a
			// verdict on them; the warning alone would leave an approval
			// reading as a clean one to anything that only checks the
			// verdict.
			final.Incomplete = len(s.dispatcher.Unseen()) > 0
			return *final, nil
		}
	}

	// Reached only if the final turn's forced submit_review was itself
	// rejected (unparseable or invalid arguments).
	return s.partial(fmt.Errorf("session: exceeded max turns (%d) without a valid submitted review", s.maxTurns))
}

// partial turns a session that ended without submitting into an
// Incomplete review of whatever it recorded, or reports cause if it
// recorded nothing.
func (s *SlopReviewManagedSession) partial(cause error) (review.Review, error) {
	if len(s.recorded) == 0 {
		return review.Review{}, fmt.Errorf("%w: %w", ErrNoReview, cause)
	}
	return review.Review{
		Verdict:    review.VerdictFor(s.recorded),
		Summary:    "The review session ended before submitting; these are the findings it recorded along the way.",
		Findings:   s.recorded,
		Warnings:   append([]string{cause.Error()}, s.dispatcher.Warnings()...),
		Incomplete: true,
	}, nil
}

// handleToolCalls dispatches every tool call from one assistant turn,
// appending a tool-result message for each -- every call must get a
// response, including submit_review, or the next request to the API is
// malformed. Returns the submitted review, if any of this turn's calls was
// submit_review.
func (s *SlopReviewManagedSession) handleToolCalls(calls []llm.ToolCall) *review.Review {
	var final *review.Review
	for _, tc := range calls {
		started := s.now()
		switch tc.Name {
		case review.SubmitReviewToolName:
			var r review.Review
			if err := json.Unmarshal([]byte(tc.ArgumentsJSON), &r); err != nil {
				s.rejectToolCall(tc, started, err)
				continue
			}
			if err := r.Validate(); err != nil {
				s.rejectToolCall(tc, started, err)
				continue
			}
			final = &r
			s.respond(tc, started, "review recorded", false)
		case review.RecordFindingToolName:
			var f review.Finding
			if err := json.Unmarshal([]byte(tc.ArgumentsJSON), &f); err != nil {
				s.rejectToolCall(tc, started, err)
				continue
			}
			if err := f.Validate(); err != nil {
				s.rejectToolCall(tc, started, err)
				continue
			}
			s.recorded = append(s.recorded, f)
			s.respond(tc, started, fmt.Sprintf("finding recorded (%d so far)", len(s.recorded)), false)
		default:
			result := s.dispatcher.Dispatch(tc.Name, tc.ArgumentsJSON)
			// The toolset answers a failed call with "error: ..." for the
			// model to read, rather than a Go error.
			s.respond(tc, started, result, strings.HasPrefix(result, "error:"))
		}
	}
	return final
}

// respond answers a tool call, and counts it: one call to tc.Name, taking
// since started, returning content.
func (s *SlopReviewManagedSession) respond(tc llm.ToolCall, started time.Time, content string, failed bool) {
	s.messages = append(s.messages, llm.Message{Role: llm.RoleTool, Content: content, ToolCallID: tc.ID})
	name := tc.Name
	if !s.offered[name] {
		name = UnknownTool
	}
	u, ok := s.tools[name]
	if !ok {
		u = &llmusage.ToolUsage{Tool: name}
		s.tools[name] = u
	}
	u.Calls++
	if failed {
		u.Errors++
	}
	u.DurationMs += s.now().Sub(started).Milliseconds()
	u.ResultBytes += int64(len(content))
}

// rejectToolCall answers a submit_review or record_finding call that
// failed to decode or validate with an error the model can see and retry
// on, rather than the loop silently accepting a bad result.
func (s *SlopReviewManagedSession) rejectToolCall(tc llm.ToolCall, started time.Time, err error) {
	s.respond(tc, started, fmt.Sprintf("error: invalid %s arguments: %v", tc.Name, err), true)
}
