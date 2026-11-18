package session

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/internal/llm"
	"github.com/lowaa/slop-factory/slop-review/internal/review"
)

type stubProvider struct {
	responses []llm.CompletionResponse
	errAt     int // if >= 0, Complete errors on this call index instead of returning a scripted response
	// errUsage is the usage the erroring call reports: the requests it sent.
	errUsage llm.Usage
	calls    int
	requests []llm.CompletionRequest
}

func (p *stubProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	p.requests = append(p.requests, req)
	if p.errAt == p.calls {
		p.calls++
		return llm.CompletionResponse{Usage: p.errUsage}, errors.New("boom")
	}
	if p.calls >= len(p.responses) {
		return llm.CompletionResponse{}, errors.New("stub: ran out of scripted responses")
	}
	resp := p.responses[p.calls]
	p.calls++
	return resp, nil
}

type stubDispatcher struct {
	defs       []llm.ToolDefinition
	dispatched []string
	warnings   []string
	unseen     []string
	// result overrides the default "result for "+name reply when set --
	// used to make a tool result large enough to exercise eliding.
	result string
	// results overrides the reply for a call, keyed by its arguments or,
	// failing that, its tool name.
	results map[string]string
}

func (d *stubDispatcher) Definitions() []llm.ToolDefinition { return d.defs }

func (d *stubDispatcher) Dispatch(name, argumentsJSON string) string {
	d.dispatched = append(d.dispatched, name)
	if r, ok := d.results[argumentsJSON]; ok {
		return r
	}
	if r, ok := d.results[name]; ok {
		return r
	}
	if d.result != "" {
		return d.result
	}
	return "result for " + name
}

func (d *stubDispatcher) Warnings() []string { return d.warnings }

func (d *stubDispatcher) Unseen() []string { return d.unseen }

func submitReviewCall(t *testing.T, r review.Review) llm.ToolCall {
	t.Helper()
	args, err := json.Marshal(r)
	require.NoError(t, err)
	return llm.ToolCall{ID: "call-submit", Name: review.SubmitReviewToolName, ArgumentsJSON: string(args)}
}

func TestRun_ToolCallThenSubmit(t *testing.T) {
	wantReview := review.Review{
		Verdict: review.VerdictApprove,
		Summary: "looks fine",
		Findings: []review.Finding{
			{File: "a.go", Severity: review.SeverityNit, Summary: "nit", Detail: "d"},
		},
	}
	provider := &stubProvider{
		errAt: -1,
		responses: []llm.CompletionResponse{
			{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
				{ID: "call-1", Name: "read_file", ArgumentsJSON: `{"path":"a.go"}`},
			}}},
			{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{submitReviewCall(t, wantReview)}}},
		},
	}
	dispatcher := &stubDispatcher{}

	s := New(&Options{Provider: provider, Model: "test-model", Tools: dispatcher, MaxTurns: 5, SystemPrompt: "system prompt", UserPrompt: "user prompt"})
	got, err := s.Run(context.Background())
	require.NoError(t, err)

	assert.Equal(t, wantReview.Verdict, got.Verdict)
	assert.Equal(t, wantReview.Summary, got.Summary)
	assert.Equal(t, wantReview.Findings, got.Findings)
	assert.Equal(t, []string{"read_file"}, dispatcher.dispatched)

	// Second request's transcript must include the tool result from the
	// first turn -- otherwise the model has no memory of what it read.
	require.Len(t, provider.requests, 2)
	secondReqMessages := provider.requests[1].Messages
	found := false
	for _, m := range secondReqMessages {
		if m.Role == llm.RoleTool && m.Content == "result for read_file" {
			found = true
		}
	}
	assert.True(t, found, "tool result must be appended to the conversation before the next turn")
}

func TestRun_ImmediateSubmit(t *testing.T) {
	wantReview := review.Review{Verdict: review.VerdictRequestChanges, Summary: "needs work"}
	provider := &stubProvider{
		errAt: -1,
		responses: []llm.CompletionResponse{
			{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{submitReviewCall(t, wantReview)}}},
		},
	}
	s := New(&Options{Provider: provider, Model: "test-model", Tools: &stubDispatcher{}, MaxTurns: 5, SystemPrompt: "system", UserPrompt: "user"})

	got, err := s.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, review.VerdictRequestChanges, got.Verdict)
}

func TestRun_NoToolCallsIsNudgedThenRecovers(t *testing.T) {
	// The provider is asked to force tool use (see
	// OpenAIAPILLMProvider.Complete), but the session must not just discard
	// a review's progress if a backend ignores that and answers in prose --
	// nudge and retry, bounded by maxTurns, rather than erroring outright.
	wantReview := review.Review{Verdict: review.VerdictApprove, Summary: "ok"}
	provider := &stubProvider{
		errAt: -1,
		responses: []llm.CompletionResponse{
			{Message: llm.Message{Role: llm.RoleAssistant, Content: "just some prose, no tool call"}},
			{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{submitReviewCall(t, wantReview)}}},
		},
	}
	s := New(&Options{Provider: provider, Model: "test-model", Tools: &stubDispatcher{}, MaxTurns: 5, SystemPrompt: "system", UserPrompt: "user"})

	got, err := s.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, wantReview.Verdict, got.Verdict)

	require.Len(t, provider.requests, 2)
	nudge := provider.requests[1].Messages[len(provider.requests[1].Messages)-1]
	assert.Equal(t, llm.RoleUser, nudge.Role)
	assert.Contains(t, nudge.Content, "didn't call a tool")
}

func TestRun_PersistentNoToolCallsExceedsMaxTurns(t *testing.T) {
	prose := llm.CompletionResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: "still just prose"}}
	provider := &stubProvider{errAt: -1, responses: []llm.CompletionResponse{prose, prose, prose}}
	s := New(&Options{Provider: provider, Model: "test-model", Tools: &stubDispatcher{}, MaxTurns: 3, SystemPrompt: "system", UserPrompt: "user"})

	_, err := s.Run(context.Background())
	assert.ErrorContains(t, err, "exceeded max turns")
}

func TestRun_ExceedsMaxTurns(t *testing.T) {
	loopForever := llm.CompletionResponse{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
		{ID: "call-1", Name: "read_file", ArgumentsJSON: `{"path":"a.go"}`},
	}}}
	provider := &stubProvider{errAt: -1, responses: []llm.CompletionResponse{loopForever, loopForever, loopForever}}
	s := New(&Options{Provider: provider, Model: "test-model", Tools: &stubDispatcher{}, MaxTurns: 3, SystemPrompt: "system", UserPrompt: "user"})

	_, err := s.Run(context.Background())
	assert.ErrorContains(t, err, "exceeded max turns")
}

func TestRun_InvalidSubmitReviewArgumentsAllowsRetry(t *testing.T) {
	wantReview := review.Review{Verdict: review.VerdictApprove, Summary: "ok now"}
	provider := &stubProvider{
		errAt: -1,
		responses: []llm.CompletionResponse{
			{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
				{ID: "call-1", Name: review.SubmitReviewToolName, ArgumentsJSON: "not json"},
			}}},
			{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{submitReviewCall(t, wantReview)}}},
		},
	}
	s := New(&Options{Provider: provider, Model: "test-model", Tools: &stubDispatcher{}, MaxTurns: 5, SystemPrompt: "system", UserPrompt: "user"})

	got, err := s.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, wantReview.Verdict, got.Verdict)
}

func TestRun_InvalidSubmitReviewContentAllowsRetry(t *testing.T) {
	// Valid JSON, but semantically empty -- e.g. an unknown verdict -- must
	// be rejected the same way malformed JSON is, not accepted as a
	// finished review (see review.Review.Validate).
	wantReview := review.Review{Verdict: review.VerdictApprove, Summary: "ok now"}
	provider := &stubProvider{
		errAt: -1,
		responses: []llm.CompletionResponse{
			{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
				{ID: "call-1", Name: review.SubmitReviewToolName, ArgumentsJSON: `{}`},
			}}},
			{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{submitReviewCall(t, wantReview)}}},
		},
	}
	s := New(&Options{Provider: provider, Model: "test-model", Tools: &stubDispatcher{}, MaxTurns: 5, SystemPrompt: "system", UserPrompt: "user"})

	got, err := s.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, wantReview.Verdict, got.Verdict)

	require.Len(t, provider.requests, 2)
	rejectionMsg := provider.requests[1].Messages[len(provider.requests[1].Messages)-1]
	assert.Equal(t, llm.RoleTool, rejectionMsg.Role)
	assert.Contains(t, rejectionMsg.Content, "error:")
}

func TestRun_MergesDispatcherWarningsIntoFinalReview(t *testing.T) {
	wantReview := review.Review{Verdict: review.VerdictApprove, Summary: "ok"}
	provider := &stubProvider{
		errAt: -1,
		responses: []llm.CompletionResponse{
			{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{submitReviewCall(t, wantReview)}}},
		},
	}
	dispatcher := &stubDispatcher{warnings: []string{"read .env under --allow-secrets"}}
	s := New(&Options{Provider: provider, Model: "test-model", Tools: dispatcher, MaxTurns: 5, SystemPrompt: "system", UserPrompt: "user"})

	got, err := s.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"read .env under --allow-secrets"}, got.Warnings)
}

func TestRun_ProviderErrorIsWrapped(t *testing.T) {
	provider := &stubProvider{errAt: 0}
	s := New(&Options{Provider: provider, Model: "test-model", Tools: &stubDispatcher{}, MaxTurns: 5, SystemPrompt: "system", UserPrompt: "user"})

	_, err := s.Run(context.Background())
	assert.ErrorContains(t, err, "turn 1")
	assert.ErrorContains(t, err, "boom")
}

func TestRun_FinalTurnForcesSubmitReview(t *testing.T) {
	// A heavy explorer that would otherwise still be gathering context on
	// the last turn must be made to submit, rather than the whole session
	// (and its cost) being discarded for want of one more turn.
	keepExploring := llm.CompletionResponse{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
		{ID: "call-1", Name: "read_file", ArgumentsJSON: `{"path":"a.go"}`},
	}}}
	wantReview := review.Review{Verdict: review.VerdictApproveWithComments, Summary: "forced out"}
	provider := &stubProvider{
		errAt: -1,
		responses: []llm.CompletionResponse{
			keepExploring,
			keepExploring,
			{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{submitReviewCall(t, wantReview)}}},
		},
	}
	s := New(&Options{Provider: provider, Model: "test-model", Tools: &stubDispatcher{}, MaxTurns: 3, SystemPrompt: "system", UserPrompt: "user"})

	got, err := s.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, wantReview.Verdict, got.Verdict)

	require.Len(t, provider.requests, 3)
	assert.Empty(t, provider.requests[0].ForceToolName, "earlier turns must leave the model free to explore")
	assert.Empty(t, provider.requests[1].ForceToolName)
	assert.Equal(t, review.SubmitReviewToolName, provider.requests[2].ForceToolName, "the final turn must force submit_review")

	finalPrompt := provider.requests[2].Messages[len(provider.requests[2].Messages)-1]
	assert.Equal(t, llm.RoleUser, finalPrompt.Role)
	assert.Contains(t, finalPrompt.Content, "final turn")
}
