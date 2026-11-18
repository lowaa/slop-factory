package session

import (
	"context"
	"strings"
	"testing"
	"time"

	llmusage "github.com/lowaa/slop-factory/go-llm-usage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/internal/llm"
	"github.com/lowaa/slop-factory/slop-review/internal/review"
)

// TestStats_TurnsTokensAndUnseenFiles checks the telemetry Stats reports
// after a normal run: turns actually used, tokens summed across every
// completion, and the dispatcher's own unseen-files list passed through --
// this is what internal/orchestrator and eventually internal/runreport
// build the reported run from, so a wrong number here is invisible until
// it shows up wrong in a report.
func TestStats_TurnsTokensAndUnseenFiles(t *testing.T) {
	wantReview := review.Review{Verdict: review.VerdictApprove, Summary: "ok"}
	provider := &stubProvider{
		errAt: -1,
		responses: []llm.CompletionResponse{
			{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{readFileCall("call-1")}}, Usage: llm.Usage{Requests: 1, InputTokens: 100, OutputTokens: 20}},
			{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{submitReviewCall(t, wantReview)}}, Usage: llm.Usage{Requests: 1, InputTokens: 150, OutputTokens: 30}},
		},
	}
	dispatcher := &stubDispatcher{unseen: []string{"b.go"}}
	s := New(&Options{Provider: provider, Model: "m", Tools: dispatcher, MaxTurns: 5, SystemPrompt: "system", UserPrompt: "user"})

	_, err := s.Run(context.Background())
	require.NoError(t, err)

	stats := s.Stats()
	assert.Equal(t, 2, stats.Turns)
	assert.Equal(t, int64(250), stats.Usage.InputTokens)
	assert.Equal(t, int64(50), stats.Usage.OutputTokens)
	assert.Equal(t, []string{"b.go"}, stats.UnseenFiles)
	assert.Equal(t, 0, stats.Elisions, "nothing was ever big enough to need eliding")
	assert.GreaterOrEqual(t, stats.Duration, time.Duration(0))
}

// TestStats_ElisionsAreCounted forces the context budget small enough that
// an old tool result must be elided before a later turn's request, and
// checks that shows up in Stats -- not just that the conversation shrank
// (elide.go's own behaviour), which is what actually gets reported.
func TestStats_ElisionsAreCounted(t *testing.T) {
	wantReview := review.Review{Verdict: review.VerdictApprove, Summary: "ok"}
	big := strings.Repeat("x", 500)
	provider := &stubProvider{
		errAt: -1,
		responses: []llm.CompletionResponse{
			{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{readFileCall("call-1")}}},
			{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{readFileCall("call-2")}}},
			{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{submitReviewCall(t, wantReview)}}},
		},
	}
	dispatcher := &stubDispatcher{result: big}
	s := New(&Options{
		Provider: provider, Model: "m", Tools: dispatcher, MaxTurns: 5,
		ContextBudgetBytes: 100, SystemPrompt: "system", UserPrompt: "user",
	})

	_, err := s.Run(context.Background())
	require.NoError(t, err)

	stats := s.Stats()
	assert.Equal(t, 3, stats.Turns)
	assert.GreaterOrEqual(t, stats.Elisions, 1, "the first tool result must have been elided before the third turn's request")
}

// stepClock is a clock that moves on by step every time it is read, so a
// tool call's duration is exact: the reads around one call are one step
// apart.
type stepClock struct {
	t    time.Time
	step time.Duration
}

func (c *stepClock) now() time.Time {
	c.t = c.t.Add(c.step)
	return c.t
}

// TestStats_UsageAndToolsAreCounted checks what the run's report carries
// as the session's LLM usage: every completion's usage summed -- the
// requests of a turn that failed included -- and every tool call counted
// by name, with its errors, time and result size. A tool the session never
// offered is counted under one name, whatever the model called it.
func TestStats_UsageAndToolsAreCounted(t *testing.T) {
	cost := int64(7)
	missing := llm.ToolCall{ID: "call-2", Name: "read_file", ArgumentsJSON: `{"path":"missing.go"}`}
	invented := llm.ToolCall{ID: "call-3", Name: "rm -rf /", ArgumentsJSON: `{}`}
	badFinding := llm.ToolCall{ID: "call-4", Name: review.RecordFindingToolName, ArgumentsJSON: "{not json"}
	finding := review.Finding{File: "a.go", Line: 1, Severity: review.SeverityMinor, Summary: "s", Detail: "d"}
	provider := &stubProvider{
		errAt:    2,
		errUsage: llm.Usage{Requests: 3},
		responses: []llm.CompletionResponse{
			{
				Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{readFileCall("call-1"), missing, invented}},
				Usage:   llm.Usage{Requests: 2, InputTokens: 100, CacheReadTokens: 900, OutputTokens: 20, ReasoningTokens: 5, ReportedCostMicros: &cost},
			},
			{
				Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{badFinding, recordFindingCall(t, "call-5", finding)}},
				Usage:   llm.Usage{Requests: 1, InputTokens: 50, OutputTokens: 10},
			},
		},
	}
	dispatcher := &stubDispatcher{defs: []llm.ToolDefinition{{Name: "read_file"}}, results: map[string]string{
		missing.ArgumentsJSON: "error: no such file",
		invented.Name:         `error: unknown tool "rm -rf /"`,
	}}
	clock := &stepClock{t: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), step: 5 * time.Millisecond}
	s := New(&Options{
		Provider: provider, Model: "m", Tools: dispatcher, MaxTurns: 5, SystemPrompt: "system", UserPrompt: "user",
		Now: clock.now,
	})

	_, err := s.Run(context.Background())
	require.NoError(t, err, "the recorded finding makes an incomplete review, not a failure")

	stats := s.Stats()
	sum := int64(7)
	assert.Equal(t, llm.Usage{
		Requests: 6, InputTokens: 150, CacheReadTokens: 900, OutputTokens: 30, ReasoningTokens: 5, ReportedCostMicros: &sum,
	}, stats.Usage, "the failed third turn's three requests count")

	answer := func(callID string) int64 { return int64(len(toolAnswer(t, s, callID))) }
	assert.Equal(t, []llmusage.ToolUsage{
		{Tool: "read_file", Calls: 2, Errors: 1, DurationMs: 10, ResultBytes: answer("call-1") + answer("call-2")},
		{Tool: review.RecordFindingToolName, Calls: 2, Errors: 1, DurationMs: 10, ResultBytes: answer("call-4") + answer("call-5")},
		{Tool: UnknownTool, Calls: 1, Errors: 1, DurationMs: 5, ResultBytes: answer("call-3")},
	}, stats.Tools)
}

// toolAnswer is the tool-result message the session answered callID with.
func toolAnswer(t *testing.T, s *SlopReviewManagedSession, callID string) string {
	t.Helper()
	for _, m := range s.messages {
		if m.Role == llm.RoleTool && m.ToolCallID == callID {
			return m.Content
		}
	}
	t.Fatalf("no answer to %s", callID)
	return ""
}
