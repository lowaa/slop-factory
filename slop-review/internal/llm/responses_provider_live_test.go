package llm_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/internal/llm"
	"github.com/lowaa/slop-factory/slop-review/internal/piauth"
)

// TestResponsesLLMProvider_LiveChatGPT runs a two-turn tool-calling
// session against the real backend with this machine's ChatGPT login. It
// spends subscription usage, so it runs only when asked for with
// SLOP_REVIEW_E2E_CHATGPT=1, and skips unless pi reports the login ready.
func TestResponsesLLMProvider_LiveChatGPT(t *testing.T) {
	if os.Getenv("SLOP_REVIEW_E2E_CHATGPT") != "1" {
		t.Skip("set SLOP_REVIEW_E2E_CHATGPT=1 to run against the real ChatGPT backend")
	}
	out, err := exec.Command("pi", "auth", "check", "--provider", "openai", "--json").Output()
	var status struct {
		Status   string `json:"status"`
		AuthType string `json:"authType"`
	}
	if err != nil || json.Unmarshal(out, &status) != nil || status.Status != "ready" || status.AuthType != "oauth" {
		t.Skip("pi has no ready Sign in with ChatGPT login for provider openai")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	p := llm.NewResponsesLLMProvider(&llm.ResponsesOptions{
		BaseURL:         "https://api.openai.com/v1",
		Tokens:          piauth.New(&piauth.Options{}),
		ReasoningEffort: "medium",
		MaxRetries:      1,
		RetryDelay:      time.Second,
	})
	tools := []llm.ToolDefinition{
		{Name: "lookup", Description: "Look up the value stored under a key.", Parameters: map[string]any{
			"type": "object", "properties": map[string]any{"key": map[string]any{"type": "string"}}, "required": []string{"key"},
		}},
		{Name: "answer", Description: "Give the final answer.", Parameters: map[string]any{
			"type": "object", "properties": map[string]any{"value": map[string]any{"type": "integer"}}, "required": []string{"value"},
		}},
	}
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "You answer by calling tools only."},
		// Working out the key takes some reasoning, so the first turn
		// carries a reasoning item for the second to replay.
		{Role: llm.RoleUser, Content: "Look up the key that is the 10th prime number written in English words, lowercase (e.g. \"eleven\"). Then answer with the value you found multiplied by 7."},
	}

	first, err := p.Complete(ctx, llm.CompletionRequest{Model: "gpt-5.6-luna", Messages: messages, Tools: tools})
	require.NoError(t, err)
	require.NotEmpty(t, first.Message.ToolCalls)
	assert.Equal(t, "lookup", first.Message.ToolCalls[0].Name)
	assert.Positive(t, first.Usage.InputTokens)
	require.NotEmpty(t, first.Message.ProviderState)
	var state struct{ Items []struct{ Type string } }
	require.NoError(t, json.Unmarshal(first.Message.ProviderState, &state))
	require.NotEmpty(t, state.Items)
	assert.Equal(t, "reasoning", state.Items[0].Type, "no reasoning item came back, so this run doesn't prove one is accepted on replay")
	assert.Contains(t, first.Message.ToolCalls[0].ArgumentsJSON, "twenty")

	messages = append(messages, first.Message)
	for _, tc := range first.Message.ToolCalls {
		messages = append(messages, llm.Message{Role: llm.RoleTool, ToolCallID: tc.ID, Content: "6"})
	}
	second, err := p.Complete(ctx, llm.CompletionRequest{Model: "gpt-5.6-luna", Messages: messages, Tools: tools, ForceToolName: "answer"})
	require.NoError(t, err, "the first turn's provider state must be accepted back")
	require.NotEmpty(t, second.Message.ToolCalls)
	assert.Equal(t, "answer", second.Message.ToolCalls[0].Name)
	assert.JSONEq(t, `{"value":42}`, second.Message.ToolCalls[0].ArgumentsJSON)
}
