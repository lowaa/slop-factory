package session

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/internal/llm"
	"github.com/lowaa/slop-factory/slop-review/internal/review"
)

func recordFindingCall(t *testing.T, id string, f review.Finding) llm.ToolCall {
	t.Helper()
	args, err := json.Marshal(f)
	require.NoError(t, err)
	return llm.ToolCall{ID: id, Name: review.RecordFindingToolName, ArgumentsJSON: string(args)}
}

func assistantCalls(calls ...llm.ToolCall) llm.CompletionResponse {
	return llm.CompletionResponse{Message: llm.Message{Role: llm.RoleAssistant, ToolCalls: calls}}
}

func readFileCall(id string) llm.ToolCall {
	return llm.ToolCall{ID: id, Name: "read_file", ArgumentsJSON: `{"path":"a.go"}`}
}

func TestRun_RecordedFindingsAreIncludedInTheSubmittedReview(t *testing.T) {
	recorded := review.Finding{File: "a.go", Line: 3, Severity: review.SeverityMinor, Summary: "recorded early", Detail: "d"}
	submitted := review.Finding{File: "b.go", Severity: review.SeverityNit, Summary: "submitted at the end", Detail: "d"}
	provider := &stubProvider{errAt: -1, responses: []llm.CompletionResponse{
		assistantCalls(recordFindingCall(t, "c1", recorded)),
		assistantCalls(submitReviewCall(t, review.Review{Verdict: review.VerdictApproveWithComments, Summary: "s", Findings: []review.Finding{submitted}})),
	}}
	s := New(&Options{Provider: provider, Model: "m", Tools: &stubDispatcher{}, MaxTurns: 5})

	got, err := s.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []review.Finding{recorded, submitted}, got.Findings)
	assert.False(t, got.Incomplete)
}

func TestRun_RecordedBlockerOverridesAnApprovingSubmission(t *testing.T) {
	blocker := review.Finding{File: "a.go", Severity: review.SeverityBlocker, Summary: "data loss", Detail: "d"}
	provider := &stubProvider{errAt: -1, responses: []llm.CompletionResponse{
		assistantCalls(recordFindingCall(t, "c1", blocker)),
		assistantCalls(submitReviewCall(t, review.Review{Verdict: review.VerdictApprove, Summary: "lgtm"})),
	}}
	s := New(&Options{Provider: provider, Model: "m", Tools: &stubDispatcher{}, MaxTurns: 5})

	got, err := s.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, review.VerdictRequestChanges, got.Verdict)
}

func TestRun_RunningOutOfTurnsReturnsRecordedFindingsAsIncomplete(t *testing.T) {
	recorded := review.Finding{File: "a.go", Severity: review.SeverityMajor, Summary: "found it", Detail: "d"}
	badSubmit := llm.ToolCall{ID: "c3", Name: review.SubmitReviewToolName, ArgumentsJSON: "not json"}
	provider := &stubProvider{errAt: -1, responses: []llm.CompletionResponse{
		assistantCalls(recordFindingCall(t, "c1", recorded)),
		assistantCalls(readFileCall("c2")),
		assistantCalls(badSubmit),
	}}
	dispatcher := &stubDispatcher{warnings: []string{"1 changed file(s) were never looked at by this review: z.go"}}
	s := New(&Options{Provider: provider, Model: "m", Tools: dispatcher, MaxTurns: 3})

	got, err := s.Run(context.Background())
	require.NoError(t, err, "recorded findings must be reported, not discarded with the session")
	assert.True(t, got.Incomplete)
	assert.Equal(t, []review.Finding{recorded}, got.Findings)
	assert.Equal(t, review.VerdictRequestChanges, got.Verdict)
	require.Len(t, got.Warnings, 2)
	assert.Contains(t, got.Warnings[0], "exceeded max turns")
	assert.Contains(t, got.Warnings[1], "z.go")
}

func TestRun_ProviderFailureAfterRecordingReturnsIncomplete(t *testing.T) {
	recorded := review.Finding{File: "a.go", Severity: review.SeverityMinor, Summary: "found it", Detail: "d"}
	provider := &stubProvider{errAt: 1, responses: []llm.CompletionResponse{
		assistantCalls(recordFindingCall(t, "c1", recorded)),
	}}
	s := New(&Options{Provider: provider, Model: "m", Tools: &stubDispatcher{}, MaxTurns: 5})

	got, err := s.Run(context.Background())
	require.NoError(t, err)
	assert.True(t, got.Incomplete)
	assert.Equal(t, []review.Finding{recorded}, got.Findings)
	assert.Contains(t, got.Warnings[0], "boom")
}

func TestRun_NothingRecordedAndNoSubmissionIsErrNoReview(t *testing.T) {
	provider := &stubProvider{errAt: -1, responses: []llm.CompletionResponse{
		assistantCalls(readFileCall("c1")),
		assistantCalls(readFileCall("c2")),
	}}
	s := New(&Options{Provider: provider, Model: "m", Tools: &stubDispatcher{}, MaxTurns: 2})

	_, err := s.Run(context.Background())
	assert.ErrorIs(t, err, ErrNoReview)
}

func TestRun_InvalidRecordFindingIsRejectedAndNotKept(t *testing.T) {
	provider := &stubProvider{errAt: -1, responses: []llm.CompletionResponse{
		assistantCalls(llm.ToolCall{ID: "c1", Name: review.RecordFindingToolName, ArgumentsJSON: `{"file":"a.go","severity":"catastrophic","summary":"s"}`}),
		assistantCalls(submitReviewCall(t, review.Review{Verdict: review.VerdictApprove, Summary: "ok"})),
	}}
	s := New(&Options{Provider: provider, Model: "m", Tools: &stubDispatcher{}, MaxTurns: 5})

	got, err := s.Run(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got.Findings)

	rejection := provider.requests[1].Messages[len(provider.requests[1].Messages)-1]
	assert.Equal(t, llm.RoleTool, rejection.Role)
	assert.Contains(t, rejection.Content, "error: invalid record_finding")
}

// bigDispatcher returns a large result for every call, to push a session
// over its context budget.
type bigDispatcher struct{ stubDispatcher }

func (d *bigDispatcher) Dispatch(name, _ string) string {
	return name + ":" + strings.Repeat("x", 1000)
}

func TestRun_ElidesOldestToolResultsOverTheContextBudget(t *testing.T) {
	provider := &stubProvider{errAt: -1, responses: []llm.CompletionResponse{
		assistantCalls(readFileCall("c1")),
		assistantCalls(readFileCall("c2")),
		assistantCalls(readFileCall("c3")),
		assistantCalls(submitReviewCall(t, review.Review{Verdict: review.VerdictApprove, Summary: "ok"})),
	}}
	userPrompt := "the diff " + strings.Repeat("d", 500)
	s := New(&Options{
		Provider: provider, Model: "m", Tools: &bigDispatcher{}, MaxTurns: 10,
		ContextBudgetBytes: 2500,
		SystemPrompt:       "persona",
		UserPrompt:         userPrompt,
	})

	_, err := s.Run(context.Background())
	require.NoError(t, err)

	last := provider.requests[3].Messages
	assert.Equal(t, "persona", last[0].Content, "the system prompt is never elided")
	assert.Equal(t, userPrompt, last[1].Content, "the review input is never elided")

	var toolResults []string
	for _, m := range last {
		if m.Role == llm.RoleTool {
			toolResults = append(toolResults, m.Content)
		}
	}
	require.Len(t, toolResults, 3)
	assert.Contains(t, toolResults[0], "result elided")
	assert.Contains(t, toolResults[0], `read_file {"path":"a.go"}`, "the stub names the call so it can be repeated")
	assert.True(t, strings.HasPrefix(toolResults[2], "read_file:xxx"), "the latest turn's result is never elided")

	total := 0
	for _, m := range last {
		total += messageSize(m)
	}
	assert.LessOrEqual(t, total, 2500)
}

func TestRun_NoContextBudgetKeepsEverything(t *testing.T) {
	provider := &stubProvider{errAt: -1, responses: []llm.CompletionResponse{
		assistantCalls(readFileCall("c1")),
		assistantCalls(readFileCall("c2")),
		assistantCalls(submitReviewCall(t, review.Review{Verdict: review.VerdictApprove, Summary: "ok"})),
	}}
	s := New(&Options{Provider: provider, Model: "m", Tools: &bigDispatcher{}, MaxTurns: 10})

	_, err := s.Run(context.Background())
	require.NoError(t, err)
	for _, m := range provider.requests[2].Messages {
		assert.NotContains(t, m.Content, "elided")
	}
}

func TestRun_UnreachableBudgetStillKeepsTheLatestResults(t *testing.T) {
	provider := &stubProvider{errAt: -1, responses: []llm.CompletionResponse{
		assistantCalls(readFileCall("c1")),
		assistantCalls(readFileCall("c2")),
		assistantCalls(submitReviewCall(t, review.Review{Verdict: review.VerdictApprove, Summary: "ok"})),
	}}
	s := New(&Options{Provider: provider, Model: "m", Tools: &bigDispatcher{}, MaxTurns: 10, ContextBudgetBytes: 1})

	_, err := s.Run(context.Background())
	require.NoError(t, err)

	last := provider.requests[2].Messages
	var toolResults []string
	for _, m := range last {
		if m.Role == llm.RoleTool {
			toolResults = append(toolResults, m.Content)
		}
	}
	require.Len(t, toolResults, 2)
	assert.Contains(t, toolResults[0], "result elided")
	assert.True(t, strings.HasPrefix(toolResults[1], "read_file:xxx"), "the result the model is about to act on must survive any budget")
}

func TestRun_SubmittingWithUnseenFilesIsIncomplete(t *testing.T) {
	provider := &stubProvider{errAt: -1, responses: []llm.CompletionResponse{
		assistantCalls(submitReviewCall(t, review.Review{Verdict: review.VerdictApprove, Summary: "lgtm"})),
	}}
	dispatcher := &stubDispatcher{unseen: []string{"big.go"}, warnings: []string{"1 changed file(s) were never looked at by this review: big.go"}}
	s := New(&Options{Provider: provider, Model: "m", Tools: dispatcher, MaxTurns: 5})

	got, err := s.Run(context.Background())
	require.NoError(t, err)
	assert.True(t, got.Incomplete, "an approval that skipped an owned file must not read as a complete one")
	assert.Contains(t, got.Warnings[0], "big.go")
}

func TestRun_SubmittingHavingSeenEverythingIsComplete(t *testing.T) {
	provider := &stubProvider{errAt: -1, responses: []llm.CompletionResponse{
		assistantCalls(submitReviewCall(t, review.Review{Verdict: review.VerdictApprove, Summary: "lgtm"})),
	}}
	s := New(&Options{Provider: provider, Model: "m", Tools: &stubDispatcher{}, MaxTurns: 5})

	got, err := s.Run(context.Background())
	require.NoError(t, err)
	assert.False(t, got.Incomplete)
}
