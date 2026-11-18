package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/internal/llm"
	"github.com/lowaa/slop-factory/slop-review/internal/review"
)

// withState attaches a distinct, sizeable ProviderState to an assistant
// turn, standing in for a reasoning model's encrypted reasoning.
func withState(resp llm.CompletionResponse, n int, size int) llm.CompletionResponse {
	state, _ := json.Marshal(map[string]string{"turn": fmt.Sprint(n), "blob": strings.Repeat("r", size)})
	resp.Message.ProviderState = state
	return resp
}

func assistantStates(msgs []llm.Message) []json.RawMessage {
	var out []json.RawMessage
	for _, m := range msgs {
		if m.Role == llm.RoleAssistant {
			out = append(out, m.ProviderState)
		}
	}
	return out
}

func TestRun_ProviderStateIsResentUnchangedThroughElision(t *testing.T) {
	responses := []llm.CompletionResponse{
		withState(assistantCalls(readFileCall("c1")), 1, 100),
		withState(assistantCalls(readFileCall("c2")), 2, 100),
		withState(assistantCalls(readFileCall("c3")), 3, 100),
		assistantCalls(submitReviewCall(t, review.Review{Verdict: review.VerdictApprove, Summary: "ok"})),
	}
	provider := &stubProvider{errAt: -1, responses: responses}
	s := New(&Options{
		Provider: provider, Model: "m", Tools: &bigDispatcher{}, MaxTurns: 10,
		ContextBudgetBytes: 2500, SystemPrompt: "persona", UserPrompt: "diff",
	})

	_, err := s.Run(context.Background())
	require.NoError(t, err)
	require.Positive(t, s.Stats().Elisions, "the budget must have forced elision for this test to mean anything")

	want := []json.RawMessage{responses[0].Message.ProviderState, responses[1].Message.ProviderState, responses[2].Message.ProviderState}
	assert.Equal(t, want, assistantStates(provider.requests[3].Messages))
}

func TestRun_ProviderStateDoesNotCountAgainstTheContextBudget(t *testing.T) {
	provider := &stubProvider{errAt: -1, responses: []llm.CompletionResponse{
		withState(assistantCalls(readFileCall("c1")), 1, 10000),
		withState(assistantCalls(readFileCall("c2")), 2, 10000),
		assistantCalls(submitReviewCall(t, review.Review{Verdict: review.VerdictApprove, Summary: "ok"})),
	}}
	s := New(&Options{
		// Two 1000-byte tool results fit the budget on their own; counted
		// with the state they would not, and the first would be elided.
		Provider: provider, Model: "m", Tools: &bigDispatcher{}, MaxTurns: 10,
		ContextBudgetBytes: 2500, SystemPrompt: "persona", UserPrompt: "diff",
	})

	_, err := s.Run(context.Background())
	require.NoError(t, err)
	assert.Zero(t, s.Stats().Elisions, "opaque provider state far over the budget must not elide small tool results")
}
