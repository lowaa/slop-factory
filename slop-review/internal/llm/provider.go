// Package llm is the thin, provider-agnostic abstraction slop-review talks
// to: a chat-completion request/response shape plus tool-calling, decoupled
// from any specific SDK so internal/session never imports openai-go directly.
package llm

import (
	"context"
	"encoding/json"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall is one function-call the model asked for in an assistant turn.
type ToolCall struct {
	ID            string
	Name          string
	ArgumentsJSON string
}

// Message is one turn in the conversation. ToolCalls is only set on an
// assistant message that called tools instead of (or alongside) answering in
// Content; ToolCallID is only set on a tool-role message, and must match the
// ToolCall.ID it's answering.
type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
	// ProviderState is whatever the provider that produced an assistant
	// message needs back, unchanged, when the conversation is resent -- a
	// reasoning model's encrypted reasoning, say, which the model must see
	// again to carry its reasoning across tool calls. It is opaque to
	// everything but that provider: callers keep it with its message and
	// never inspect, trim or log it.
	ProviderState json.RawMessage
}

// ToolDefinition describes one callable tool to the model, in the same
// shape OpenAI/OpenRouter's function-calling expects: Parameters is a JSON
// Schema object.
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  map[string]any
}

type CompletionRequest struct {
	Model    string
	Messages []Message
	Tools    []ToolDefinition
	// ForceToolName, when set, requires the model to call that specific
	// tool this turn rather than choosing among Tools freely. The session
	// loop uses it on its final turn to get a review out of a model that
	// would otherwise still be exploring (see session.SlopReviewManagedSession.Run).
	ForceToolName string
}

type CompletionResponse struct {
	// Message is the model's assistant turn: either Content is a final
	// answer, or ToolCalls is non-empty and the caller must dispatch them
	// and continue the conversation.
	Message Message
	// Usage is what the completion used. Unlike Message it is set when
	// Complete fails too: the requests a failed turn sent still count.
	Usage Usage
}

// Usage is LLM usage in go-llm-usage's Record terms, which the report
// sends on unchanged: InputTokens excludes the cached part of the prompt
// (CacheReadTokens), where both OpenAI APIs count it in, so a provider
// subtracts it; ReasoningTokens is the part of OutputTokens spent thinking,
// never in addition to it. A count the backend didn't report is zero.
type Usage struct {
	// Requests counts every HTTP request sent, retries and failed
	// attempts included.
	Requests        int64
	InputTokens     int64
	CacheReadTokens int64
	OutputTokens    int64
	ReasoningTokens int64
	// ReportedCostMicros is what the provider said it charged, in
	// micro-dollars, when it says (OpenRouter's usage.cost) -- nil when it
	// doesn't, never inferred.
	ReportedCostMicros *int64
}

// Add accumulates o into u. A reported cost stays nil until some
// completion reports one.
func (u *Usage) Add(o *Usage) {
	u.Requests += o.Requests
	u.InputTokens += o.InputTokens
	u.CacheReadTokens += o.CacheReadTokens
	u.OutputTokens += o.OutputTokens
	u.ReasoningTokens += o.ReasoningTokens
	if o.ReportedCostMicros != nil {
		sum := *o.ReportedCostMicros
		if u.ReportedCostMicros != nil {
			sum += *u.ReportedCostMicros
		}
		u.ReportedCostMicros = &sum
	}
}

// promptUsage splits an OpenAI-style prompt count, which includes its
// cached part, into uncached input and cache reads. A backend reporting
// more cached tokens than prompt tokens is wrong about one of them; the
// whole prompt is then counted as cached rather than going negative.
func promptUsage(prompt, cached int64) (input, cacheRead int64) {
	cached = min(max(cached, 0), max(prompt, 0))
	return max(prompt, 0) - cached, cached
}

// reasoningUsage bounds reported reasoning tokens by the output they are a
// part of, so a backend's inconsistency can't make the record invalid.
func reasoningUsage(output, reasoning int64) int64 {
	return min(max(reasoning, 0), max(output, 0))
}

// LLMProvider is implemented per backend (see OpenAIAPILLMProvider for the
// OpenAI-compatible one, which covers OpenRouter and OpenAI itself).
type LLMProvider interface {
	Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
}
