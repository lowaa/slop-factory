package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/openai/openai-go/v3/option"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenAIAPILLMProvider_ToolCallRoundTrip runs the provider against a
// real HTTP server speaking the OpenAI chat-completions wire format (which
// OpenRouter also implements), verifying the full request/response
// translation -- including a tool call, which is the shape slop-review's
// review session actually depends on for repo navigation.
func TestOpenAIAPILLMProvider_ToolCallRoundTrip(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/chat/completions", r.URL.Path)
		assert.Equal(t, "Bearer test-api-key", r.Header.Get("Authorization"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-1",
			"object": "chat.completion",
			"created": 0,
			"model": "openai/gpt-5.6-luna",
			"choices": [{
				"index": 0,
				"finish_reason": "tool_calls",
				"message": {
					"role": "assistant",
					"content": "",
					"tool_calls": [{
						"id": "call-1",
						"type": "function",
						"function": {"name": "read_file", "arguments": "{\"path\":\"main.go\"}"}
					}]
				}
			}]
		}`))
	}))
	defer server.Close()

	provider := NewOpenAIAPILLMProvider(&OpenAIOptions{BaseURL: server.URL, APIKey: "test-api-key"})

	resp, err := provider.Complete(context.Background(), CompletionRequest{
		Model: "openai/gpt-5.6-luna",
		Messages: []Message{
			{Role: RoleSystem, Content: "You are a reviewer."},
			{Role: RoleUser, Content: "Review this diff."},
		},
		Tools: []ToolDefinition{
			{
				Name:        "read_file",
				Description: "Read a file",
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{"path": map[string]any{"type": "string"}},
					"required":   []string{"path"},
				},
			},
		},
	})
	require.NoError(t, err)

	require.Len(t, resp.Message.ToolCalls, 1)
	assert.Equal(t, "call-1", resp.Message.ToolCalls[0].ID)
	assert.Equal(t, "read_file", resp.Message.ToolCalls[0].Name)
	assert.JSONEq(t, `{"path":"main.go"}`, resp.Message.ToolCalls[0].ArgumentsJSON)

	assert.Equal(t, "openai/gpt-5.6-luna", gotBody["model"])
	messages, ok := gotBody["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 2)
	assert.Equal(t, "system", messages[0].(map[string]any)["role"])
	assert.Equal(t, "user", messages[1].(map[string]any)["role"])

	// Tool use must be forced, not left to the model's discretion -- a
	// session that answers in prose instead of calling submit_review has
	// no way to end (see internal/session.SlopReviewManagedSession.Run).
	assert.Equal(t, "required", gotBody["tool_choice"])
}

// TestOpenAIAPILLMProvider_AssistantToolCallRoundTripsBack verifies that an
// assistant message carrying ToolCalls (as appended by internal/session
// after a completion) serializes back into the request the same way the
// model originally sent it -- required for the model to keep track of which
// tool_call_id a later tool-result message answers.
func TestOpenAIAPILLMProvider_AssistantToolCallRoundTripsBack(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-2", "object": "chat.completion", "created": 0, "model": "m",
			"choices": [{"index": 0, "finish_reason": "stop", "message": {"role": "assistant", "content": "done"}}]
		}`))
	}))
	defer server.Close()

	provider := NewOpenAIAPILLMProvider(&OpenAIOptions{BaseURL: server.URL, APIKey: "test-api-key"})

	_, err := provider.Complete(context.Background(), CompletionRequest{
		Model: "m",
		Messages: []Message{
			{Role: RoleUser, Content: "review this"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call-1", Name: "read_file", ArgumentsJSON: `{"path":"main.go"}`}}},
			{Role: RoleTool, Content: "package main\n", ToolCallID: "call-1"},
		},
	})
	require.NoError(t, err)

	messages := gotBody["messages"].([]any)
	require.Len(t, messages, 3)

	assistantMsg := messages[1].(map[string]any)
	assert.Equal(t, "assistant", assistantMsg["role"])
	toolCalls := assistantMsg["tool_calls"].([]any)
	require.Len(t, toolCalls, 1)
	assert.Equal(t, "call-1", toolCalls[0].(map[string]any)["id"])

	toolMsg := messages[2].(map[string]any)
	assert.Equal(t, "tool", toolMsg["role"])
	assert.Equal(t, "call-1", toolMsg["tool_call_id"])
	assert.Equal(t, "package main\n", toolMsg["content"])
}

// TestOpenAIAPILLMProvider_ForceToolName checks the wire format for pinning
// the model to one specific tool, which internal/session relies on for its
// final turn.
func TestOpenAIAPILLMProvider_ForceToolName(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","object":"chat.completion","created":0,"model":"m",
			"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]}`))
	}))
	defer server.Close()

	provider := NewOpenAIAPILLMProvider(&OpenAIOptions{BaseURL: server.URL, APIKey: "test-api-key"})
	_, err := provider.Complete(context.Background(), CompletionRequest{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "go"}},
		Tools: []ToolDefinition{
			{Name: "read_file", Parameters: map[string]any{"type": "object"}},
			{Name: "submit_review", Parameters: map[string]any{"type": "object"}},
		},
		ForceToolName: "submit_review",
	})
	require.NoError(t, err)

	choice, ok := gotBody["tool_choice"].(map[string]any)
	require.True(t, ok, "tool_choice must be the named-function object form, got %#v", gotBody["tool_choice"])
	assert.Equal(t, "function", choice["type"])
	assert.Equal(t, "submit_review", choice["function"].(map[string]any)["name"])
}

// TestOpenAIAPILLMProvider_UsageIsPlumbedThrough checks that the response's
// token usage reaches CompletionResponse -- slop-review's own cost/telemetry
// reporting (see internal/session) has nothing else to read it from.
func TestOpenAIAPILLMProvider_UsageIsPlumbedThrough(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-3", "object": "chat.completion", "created": 0, "model": "m",
			"choices": [{"index": 0, "finish_reason": "stop", "message": {"role": "assistant", "content": "done"}}],
			"usage": {"prompt_tokens": 123, "completion_tokens": 45, "total_tokens": 168}
		}`))
	}))
	defer server.Close()

	provider := NewOpenAIAPILLMProvider(&OpenAIOptions{BaseURL: server.URL, APIKey: "test-api-key"})
	resp, err := provider.Complete(context.Background(), CompletionRequest{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "go"}},
	})
	require.NoError(t, err)

	assert.Equal(t, int64(123), resp.Usage.InputTokens)
	assert.Equal(t, int64(45), resp.Usage.OutputTokens)
}

func TestOpenAIAPILLMProvider_FailuresAreClassified(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		want   error
		hint   string
	}{
		"key rejected":   {http.StatusUnauthorized, ErrKeyRejected, "OPENROUTER_API_KEY"},
		"out of credits": {http.StatusPaymentRequired, ErrCredits, "top up"},
		"bad request":    {http.StatusBadRequest, ErrRequest, ""},
		"server error":   {http.StatusInternalServerError, ErrRequest, ""},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				// Echoes the key, the worst case for what ends up in a report.
				_, _ = w.Write([]byte(`{"error":{"message":"nope for key test-api-key","code":` + strconv.Itoa(tc.status) + `}}`))
			}))
			defer server.Close()

			provider := NewOpenAIAPILLMProvider(&OpenAIOptions{BaseURL: server.URL, APIKey: "test-api-key"})
			provider.chat.Options = append(provider.chat.Options, option.WithMaxRetries(0))
			_, err := provider.Complete(context.Background(), CompletionRequest{
				Model:    "m",
				Messages: []Message{{Role: RoleUser, Content: "hi"}},
			})
			require.ErrorIs(t, err, tc.want)
			for _, other := range []error{ErrKeyRejected, ErrCredits, ErrRequest} {
				if other != tc.want {
					assert.NotErrorIs(t, err, other)
				}
			}
			assert.Contains(t, err.Error(), tc.hint)
			assert.Contains(t, err.Error(), "nope for key [redacted]", "what the provider said is kept, without the key")
			assert.NotContains(t, err.Error(), server.URL, "nor the request URL, which can carry credentials")
			for e := err; e != nil; e = errors.Unwrap(e) {
				assert.NotContains(t, e.Error(), "test-api-key")
			}
		})
	}
}

// Other tools' OPENAI_* settings are no business of this provider's
// requests: OPENAI_CUSTOM_HEADERS especially, which openai-go's client
// would otherwise send to whatever endpoint this is.
func TestOpenAIAPILLMProvider_IgnoresOpenAIEnvironment(t *testing.T) {
	t.Setenv("OPENAI_CUSTOM_HEADERS", "X-Other-Tool: secret-header")
	t.Setenv("OPENAI_ORG_ID", "org-other")
	t.Setenv("OPENAI_PROJECT_ID", "proj-other")
	t.Setenv("OPENAI_API_KEY", "other-key")
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:1/other")
	var got http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c","object":"chat.completion","created":0,"model":"m",
			"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]}`))
	}))
	defer server.Close()

	provider := NewOpenAIAPILLMProvider(&OpenAIOptions{BaseURL: server.URL, APIKey: "test-api-key"})
	_, err := provider.Complete(context.Background(), CompletionRequest{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	require.NoError(t, err, "the request reached this server, not OPENAI_BASE_URL")
	assert.Equal(t, "Bearer test-api-key", got.Get("Authorization"))
	assert.Empty(t, got.Get("X-Other-Tool"))
	assert.Empty(t, got.Get("OpenAI-Organization"))
	assert.Empty(t, got.Get("OpenAI-Project"))
}

func TestOpenAIAPILLMProvider_TransportFailureIsARequestFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()

	provider := NewOpenAIAPILLMProvider(&OpenAIOptions{BaseURL: url, APIKey: "k"})
	provider.chat.Options = append(provider.chat.Options, option.WithMaxRetries(0))
	_, err := provider.Complete(context.Background(), CompletionRequest{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	require.ErrorIs(t, err, ErrRequest)
}

// usageServer answers every request with a completion carrying usageJSON,
// after failing the first failures requests with a retryable 500.
func usageServer(t *testing.T, failures int, usageJSON string) (*httptest.Server, *int) {
	t.Helper()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		if requests <= failures {
			// Retry at once rather than after the SDK's backoff.
			w.Header().Set("Retry-After-Ms", "0")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"try again"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"c","object":"chat.completion","created":0,"model":"m",
			"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"done"}}],
			"usage":` + usageJSON + `}`))
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

// OpenRouter's usage, as its docs record it: cached and reasoning token
// details, and the cost in credits (dollars).
const openRouterUsage = `{"prompt_tokens":1200,"prompt_tokens_details":{"cached_tokens":1000,"cache_write_tokens":0},
	"completion_tokens":300,"completion_tokens_details":{"reasoning_tokens":250},"total_tokens":1500,
	"cost":0.0018305,"cost_details":{"upstream_inference_cost":null}}`

func TestOpenAIAPILLMProvider_UsageDetailsAndReportedCost(t *testing.T) {
	cost := int64(1831)
	for name, tc := range map[string]struct {
		usage       string
		reportsCost bool
		want        Usage
	}{
		"openrouter": {openRouterUsage, true, Usage{
			Requests: 1, InputTokens: 200, CacheReadTokens: 1000, OutputTokens: 300, ReasoningTokens: 250, ReportedCostMicros: &cost,
		}},
		"a backend that reports no cost has its cost field ignored": {openRouterUsage, false, Usage{
			Requests: 1, InputTokens: 200, CacheReadTokens: 1000, OutputTokens: 300, ReasoningTokens: 250,
		}},
		"no details and no cost": {`{"prompt_tokens":123,"completion_tokens":45,"total_tokens":168}`, true, Usage{
			Requests: 1, InputTokens: 123, OutputTokens: 45,
		}},
		"a null cost is no cost": {`{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2,"cost":null}`, true, Usage{
			Requests: 1, InputTokens: 1, OutputTokens: 1,
		}},
	} {
		t.Run(name, func(t *testing.T) {
			server, _ := usageServer(t, 0, tc.usage)
			provider := NewOpenAIAPILLMProvider(&OpenAIOptions{BaseURL: server.URL, APIKey: "k", ReportsCost: tc.reportsCost})
			resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "m", Messages: []Message{{Role: RoleUser, Content: "go"}}})
			require.NoError(t, err)
			assert.Equal(t, tc.want, resp.Usage)
		})
	}
}

// The SDK retries a 500 inside one Complete; each attempt is a request.
func TestOpenAIAPILLMProvider_CountsEveryAttemptAsARequest(t *testing.T) {
	server, requests := usageServer(t, 1, `{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}`)
	provider := NewOpenAIAPILLMProvider(&OpenAIOptions{BaseURL: server.URL, APIKey: "k"})
	resp, err := provider.Complete(context.Background(), CompletionRequest{Model: "m", Messages: []Message{{Role: RoleUser, Content: "go"}}})
	require.NoError(t, err)
	require.Equal(t, 2, *requests)
	assert.Equal(t, int64(2), resp.Usage.Requests)

	// A turn that fails after its retries still reports what it sent.
	server, requests = usageServer(t, 100, `{}`)
	provider = NewOpenAIAPILLMProvider(&OpenAIOptions{BaseURL: server.URL, APIKey: "k"})
	resp, err = provider.Complete(context.Background(), CompletionRequest{Model: "m", Messages: []Message{{Role: RoleUser, Content: "go"}}})
	require.ErrorIs(t, err, ErrRequest)
	assert.Equal(t, int64(*requests), resp.Usage.Requests)
	assert.Greater(t, *requests, 1, "the SDK retried")
}

func TestDollarsToMicros(t *testing.T) {
	for raw, want := range map[string]int64{
		"0": 0, "0.95": 950_000, "0.0018305": 1831, "0.0000004": 0, "0.0000005": 1, "1.2e-05": 12, "12": 12_000_000,
	} {
		got := dollarsToMicros(raw)
		require.NotNil(t, got, raw)
		assert.Equal(t, want, *got, raw)
	}
	for _, raw := range []string{"-0.1", "null", `"0.1"`, "", "1e400000"} {
		assert.Nil(t, dollarsToMicros(raw), raw)
	}
}
