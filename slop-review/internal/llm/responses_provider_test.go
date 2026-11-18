package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reply is one scripted answer from the fake /v1/responses endpoint:
// either an HTTP error (status + body) or a 200 SSE stream of events.
type reply struct {
	status int
	body   string
	events []string
	// keepAlive sends keep-alive comments around each event, as the
	// ChatGPT backend does while the model is still reasoning.
	keepAlive bool
}

func sse(events ...string) reply { return reply{status: http.StatusOK, events: events} }

func withKeepAlives(r reply) reply {
	r.keepAlive = true
	return r
}

func httpError(status int, body string) reply { return reply{status: status, body: body} }

// fakeResponses stands in for the Responses API, answering each request
// with the next scripted reply and recording what it was sent.
type fakeResponses struct {
	t       *testing.T
	server  *httptest.Server
	mu      sync.Mutex
	replies []reply
	bodies  []map[string]any
	raw     []string
	auth    []string
}

func newFakeResponses(t *testing.T, replies ...reply) *fakeResponses {
	f := &fakeResponses{t: t, replies: replies}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeResponses) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Equal(f.t, "/responses", r.URL.Path)
	b, err := io.ReadAll(r.Body)
	require.NoError(f.t, err)
	var body map[string]any
	require.NoError(f.t, json.Unmarshal(b, &body))
	f.bodies = append(f.bodies, body)
	f.raw = append(f.raw, string(b))
	f.auth = append(f.auth, r.Header.Get("Authorization"))

	if len(f.replies) == 0 {
		http.Error(w, "fake: no scripted reply left", http.StatusTeapot)
		return
	}
	rep := f.replies[0]
	f.replies = f.replies[1:]
	if rep.status != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rep.status)
		_, _ = w.Write([]byte(rep.body))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, ev := range rep.events {
		var typed struct {
			Type string `json:"type"`
		}
		require.NoError(f.t, json.Unmarshal([]byte(ev), &typed))
		if rep.keepAlive {
			// Two in a row, each its own write, as a backend that is
			// still thinking sends them.
			keepAlive(w)
			keepAlive(w)
		}
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typed.Type, ev)
		w.(http.Flusher).Flush()
	}
	if rep.keepAlive {
		keepAlive(w)
	}
}

func keepAlive(w http.ResponseWriter) {
	_, _ = fmt.Fprint(w, ": keep-alive\n\n")
	w.(http.Flusher).Flush()
}

func (f *fakeResponses) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

// Event builders, shaped like the real stream: output items arrive in
// output_item.done, and response.completed carries usage but (with store
// false) an empty output list.
func itemDone(index int, item string) string {
	return fmt.Sprintf(`{"type":"response.output_item.done","output_index":%d,"sequence_number":%d,"item":%s}`, index, index+1, item)
}

func completed(in, out int) string {
	return fmt.Sprintf(`{"type":"response.completed","sequence_number":99,"response":{"id":"resp_1","object":"response","status":"completed","output":[],"usage":{"input_tokens":%d,"output_tokens":%d,"total_tokens":%d}}}`, in, out, in+out)
}

const reasoningItem = `{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"gAAAA-opaque-reasoning"}`

func functionCallItem(id, callID, name, args string) string {
	a, _ := json.Marshal(args)
	return fmt.Sprintf(`{"id":%q,"type":"function_call","status":"completed","call_id":%q,"name":%q,"arguments":%s}`, id, callID, name, a)
}

func messageItem(id, text string) string {
	return fmt.Sprintf(`{"id":%q,"type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":%q,"annotations":[],"logprobs":[]}]}`, id, text)
}

// fakeTokens is a TokenSource handing out tok-1, then tok-2 on refresh.
type fakeTokens struct {
	mu         sync.Mutex
	tokens     []string
	tokenErr   error
	refreshErr error
	refreshes  int
}

func (f *fakeTokens) Token(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tokenErr != nil {
		return "", f.tokenErr
	}
	return f.tokens[0], nil
}

func (f *fakeTokens) Refresh(_ context.Context, stale string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshes++
	if f.refreshErr != nil {
		return "", f.refreshErr
	}
	if len(f.tokens) < 2 || f.tokens[0] != stale {
		return "", errors.New("fake: unexpected refresh")
	}
	f.tokens = f.tokens[1:]
	return f.tokens[0], nil
}

func newTestProvider(f *fakeResponses, tokens TokenSource, effort string) *ResponsesLLMProvider {
	return NewResponsesLLMProvider(&ResponsesOptions{BaseURL: f.server.URL, Tokens: tokens, ReasoningEffort: effort, MaxRetries: 2})
}

var reviewTools = []ToolDefinition{
	{Name: "read_file", Description: "Read a file", Parameters: map[string]any{
		"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"},
	}},
	{Name: "submit_review", Description: "Submit", Parameters: map[string]any{"type": "object"}},
}

func firstTurn() CompletionRequest {
	return CompletionRequest{
		Model: "gpt-5.6-luna",
		Messages: []Message{
			{Role: RoleSystem, Content: "You are a reviewer."},
			{Role: RoleUser, Content: "Review this diff."},
		},
		Tools: reviewTools,
	}
}

func okTurn() reply {
	return sse(itemDone(0, functionCallItem("fc_1", "call_1", "read_file", `{"path":"a.go"}`)), completed(10, 2))
}

func TestResponses_RequestShapeForASignInWithChatGPTToken(t *testing.T) {
	f := newFakeResponses(t, okTurn())
	p := newTestProvider(f, &fakeTokens{tokens: []string{"tok-1"}}, "high")

	_, err := p.Complete(context.Background(), firstTurn())
	require.NoError(t, err)

	body := f.bodies[0]
	assert.Equal(t, "Bearer tok-1", f.auth[0])
	assert.Equal(t, "gpt-5.6-luna", body["model"])
	assert.Equal(t, true, body["stream"], "the backend refuses a non-streamed request")
	assert.Equal(t, false, body["store"])
	assert.Equal(t, []any{"reasoning.encrypted_content"}, body["include"])
	assert.Equal(t, map[string]any{"effort": "high"}, body["reasoning"])
	assert.Equal(t, "You are a reviewer.", body["instructions"])
	for _, rejected := range []string{"max_output_tokens", "temperature", "prompt_cache_retention", "prompt_cache_options"} {
		assert.NotContains(t, body, rejected, "a Sign in with ChatGPT token is refused with %s set", rejected)
	}

	assert.Equal(t, "required", body["tool_choice"], "tool use must be forced, as for the chat-completions provider")
	tools := body["tools"].([]any)
	require.Len(t, tools, 2)
	tool := tools[0].(map[string]any)
	assert.Equal(t, "function", tool["type"])
	assert.Equal(t, "read_file", tool["name"])
	assert.Equal(t, false, tool["strict"])
	assert.Equal(t, "object", tool["parameters"].(map[string]any)["type"])

	input := body["input"].([]any)
	require.Len(t, input, 1, "the system prompt goes in instructions, not input")
	assert.Equal(t, "user", input[0].(map[string]any)["role"])
	assert.Equal(t, "Review this diff.", input[0].(map[string]any)["content"])
}

func TestResponses_NoEffortLeavesTheModelDefault(t *testing.T) {
	f := newFakeResponses(t, okTurn())
	p := newTestProvider(f, &fakeTokens{tokens: []string{"tok-1"}}, "")

	_, err := p.Complete(context.Background(), firstTurn())
	require.NoError(t, err)
	assert.NotContains(t, f.bodies[0], "reasoning")
	assert.Equal(t, []any{"reasoning.encrypted_content"}, f.bodies[0]["include"], "reasoning is still returned for replay")
}

func TestResponses_ForcedFinalToolIsNamed(t *testing.T) {
	f := newFakeResponses(t, okTurn())
	p := newTestProvider(f, &fakeTokens{tokens: []string{"tok-1"}}, "")

	req := firstTurn()
	req.ForceToolName = "submit_review"
	_, err := p.Complete(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"type": "function", "name": "submit_review"}, f.bodies[0]["tool_choice"])
}

// TestResponses_ToolCallingSessionReplaysProviderState drives two turns
// the way internal/session does: the first turn's reasoning must go back
// byte for byte, ahead of the call it led to, with the call's item id,
// followed by the call's result.
func TestResponses_ToolCallingSessionReplaysProviderState(t *testing.T) {
	f := newFakeResponses(t,
		sse(
			itemDone(0, reasoningItem),
			itemDone(1, messageItem("msg_1", "Reading a.go first.")),
			itemDone(2, functionCallItem("fc_1", "call_1", "read_file", `{"path":"a.go"}`)),
			completed(100, 20),
		),
		sse(itemDone(0, functionCallItem("fc_2", "call_2", "submit_review", `{"verdict":"approve"}`)), completed(150, 5)),
	)
	p := newTestProvider(f, &fakeTokens{tokens: []string{"tok-1"}}, "")

	req := firstTurn()
	first, err := p.Complete(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "Reading a.go first.", first.Message.Content)
	assert.Equal(t, []ToolCall{{ID: "call_1", Name: "read_file", ArgumentsJSON: `{"path":"a.go"}`}}, first.Message.ToolCalls)
	assert.Equal(t, int64(100), first.Usage.InputTokens)
	assert.Equal(t, int64(20), first.Usage.OutputTokens)
	require.NotEmpty(t, first.Message.ProviderState)

	req.Messages = append(req.Messages, first.Message, Message{Role: RoleTool, ToolCallID: "call_1", Content: "package a\n"})
	second, err := p.Complete(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "submit_review", second.Message.ToolCalls[0].Name)

	assert.Contains(t, f.raw[1], reasoningItem, "the reasoning item is replayed byte for byte")
	input := f.bodies[1]["input"].([]any)
	require.Len(t, input, 5)
	assert.Equal(t, "user", input[0].(map[string]any)["role"])
	assert.Equal(t, "reasoning", input[1].(map[string]any)["type"])
	assert.Equal(t, map[string]any{
		"type": "message", "role": "assistant", "id": "msg_1", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": "Reading a.go first.", "annotations": []any{}}},
	}, input[2])
	assert.Equal(t, map[string]any{
		"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "read_file", "arguments": `{"path":"a.go"}`,
	}, input[3])
	assert.Equal(t, map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "package a\n"}, input[4])
}

func TestResponses_ItemsAreOrderedByOutputIndexNotArrival(t *testing.T) {
	f := newFakeResponses(t, sse(
		itemDone(2, functionCallItem("fc_2", "call_2", "read_file", `{"path":"b.go"}`)),
		itemDone(0, reasoningItem),
		itemDone(1, functionCallItem("fc_1", "call_1", "read_file", `{"path":"a.go"}`)),
		completed(1, 1),
	))
	p := newTestProvider(f, &fakeTokens{tokens: []string{"tok-1"}}, "")

	resp, err := p.Complete(context.Background(), firstTurn())
	require.NoError(t, err)
	require.Len(t, resp.Message.ToolCalls, 2)
	assert.Equal(t, "call_1", resp.Message.ToolCalls[0].ID)
	assert.Equal(t, "call_2", resp.Message.ToolCalls[1].ID)

	var state struct{ Items []struct{ Type, ID string } }
	require.NoError(t, json.Unmarshal(resp.Message.ProviderState, &state))
	require.Len(t, state.Items, 3)
	assert.Equal(t, []string{"rs_1", "fc_1", "fc_2"}, []string{state.Items[0].ID, state.Items[1].ID, state.Items[2].ID})
}

func TestResponses_AssistantTurnWithoutProviderStateIsRebuilt(t *testing.T) {
	f := newFakeResponses(t, okTurn())
	p := newTestProvider(f, &fakeTokens{tokens: []string{"tok-1"}}, "")

	req := firstTurn()
	req.Messages = append(req.Messages,
		Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_1", Name: "read_file", ArgumentsJSON: `{"path":"a.go"}`}}},
		Message{Role: RoleTool, ToolCallID: "call_1", Content: "x"},
	)
	_, err := p.Complete(context.Background(), req)
	require.NoError(t, err)

	input := f.bodies[0]["input"].([]any)
	require.Len(t, input, 3)
	call := input[1].(map[string]any)
	assert.Equal(t, "function_call", call["type"])
	assert.Equal(t, "call_1", call["call_id"])
	assert.NotContains(t, call, "id")
}

func TestResponses_UsageLimitIsReportedAndNotRetried(t *testing.T) {
	for name, rep := range map[string]reply{
		"http 429": httpError(http.StatusTooManyRequests, `{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"You've hit your usage limit.","type":"usage_limit"}}`),
		// The marker can arrive in any one of the fields, or in a body
		// without the usual error object.
		"marker in the type only": httpError(http.StatusTooManyRequests, `{"error":{"type":"usage_limit","message":"You've hit your usage limit."}}`),
		"marker in the code only": httpError(http.StatusTooManyRequests, `{"error":{"code":"usage_limit_reached","message":"You've hit your usage limit."}}`),
		"detail body":             httpError(http.StatusTooManyRequests, `{"detail":"usage_limit: You've hit your usage limit."}`),
		"stream":                  sse(`{"type":"response.failed","sequence_number":1,"response":{"id":"r","object":"response","status":"failed","output":[],"error":{"code":"usage_limit_reached","message":"You've hit your usage limit."}}}`),
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeResponses(t, rep, okTurn())
			p := newTestProvider(f, &fakeTokens{tokens: []string{"tok-1"}}, "")

			_, err := p.Complete(context.Background(), firstTurn())
			require.ErrorIs(t, err, ErrUsageLimit)
			assert.Contains(t, err.Error(), "You've hit your usage limit.", "what the backend said is kept")
			assert.Contains(t, err.Error(), "https://chatgpt.com/settings/usage")
			assert.Contains(t, err.Error(), "SLOP_REVIEW_PROVIDER=openrouter")
			assert.Equal(t, 1, f.requests(), "a usage limit is never retried")
		})
	}
}

func TestResponses_RejectedTokenIsRefreshedAndTheTurnRetriedOnce(t *testing.T) {
	f := newFakeResponses(t, httpError(http.StatusUnauthorized, `{"error":{"message":"expired"}}`), okTurn())
	tokens := &fakeTokens{tokens: []string{"tok-1", "tok-2"}}
	p := newTestProvider(f, tokens, "")

	resp, err := p.Complete(context.Background(), firstTurn())
	require.NoError(t, err)
	assert.Equal(t, "read_file", resp.Message.ToolCalls[0].Name)
	assert.Equal(t, []string{"Bearer tok-1", "Bearer tok-2"}, f.auth)
	assert.Equal(t, 1, tokens.refreshes)
}

func TestResponses_AuthFailures(t *testing.T) {
	unauthorized := httpError(http.StatusUnauthorized, `{"error":{"message":"bad token"}}`)
	for name, tc := range map[string]struct {
		tokens   *fakeTokens
		replies  []reply
		requests int
	}{
		"not logged in":            {&fakeTokens{tokenErr: errors.New("pi: not configured")}, nil, 0},
		"rejected, refresh fails":  {&fakeTokens{tokens: []string{"tok-1"}, refreshErr: errors.New("same token")}, []reply{unauthorized}, 1},
		"rejected after a refresh": {&fakeTokens{tokens: []string{"tok-1", "tok-2"}}, []reply{unauthorized, unauthorized, okTurn()}, 2},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeResponses(t, tc.replies...)
			p := newTestProvider(f, tc.tokens, "")

			resp, err := p.Complete(context.Background(), firstTurn())
			require.ErrorIs(t, err, ErrAuth)
			assert.Contains(t, err.Error(), "/login")
			assert.Contains(t, err.Error(), "SLOP_REVIEW_PROVIDER=openrouter")
			assert.Equal(t, tc.requests, f.requests())
			assert.Equal(t, int64(tc.requests), resp.Usage.Requests)
		})
	}
}

// A keep-alive is a comment alone between blank lines, which openai-go v2
// decoded as an empty event and failed the turn on ("unexpected end of
// JSON input"), on every retry too, as a long-reasoning turn draws one
// each time.
func TestResponses_KeepAlivesAreIgnored(t *testing.T) {
	f := newFakeResponses(t, withKeepAlives(okTurn()))
	p := newTestProvider(f, &fakeTokens{tokens: []string{"tok-1"}}, "")

	resp, err := p.Complete(context.Background(), firstTurn())
	require.NoError(t, err)
	assert.Equal(t, "read_file", resp.Message.ToolCalls[0].Name)
	assert.Equal(t, 1, f.requests())
}

func TestResponses_TransientFailuresAreRetried(t *testing.T) {
	f := newFakeResponses(t,
		httpError(http.StatusInternalServerError, `{"error":{"message":"oops"}}`),
		sse(itemDone(0, functionCallItem("fc_1", "call_1", "read_file", `{}`))), // cut off before completing
		okTurn(),
	)
	p := newTestProvider(f, &fakeTokens{tokens: []string{"tok-1"}}, "")

	resp, err := p.Complete(context.Background(), firstTurn())
	require.NoError(t, err)
	assert.Equal(t, 3, f.requests())
	assert.Equal(t, int64(3), resp.Usage.Requests, "every attempt is a request, the failed ones included")
}

func TestResponses_BackendFailuresNameTheSwitch(t *testing.T) {
	for name, tc := range map[string]struct {
		replies  []reply
		requests int
	}{
		"retries exhausted": {[]reply{
			httpError(http.StatusBadGateway, `{}`), httpError(http.StatusBadGateway, `{}`), httpError(http.StatusBadGateway, `{}`), okTurn(),
		}, 3},
		"permanent":    {[]reply{httpError(http.StatusBadRequest, `{"detail":"Unsupported parameter"}`), okTurn()}, 1},
		"stream error": {[]reply{sse(`{"type":"error","sequence_number":1,"code":"invalid_prompt","message":"no"}`), okTurn()}, 1},
		"incomplete":   {[]reply{sse(`{"type":"response.incomplete","sequence_number":1,"response":{"id":"r","object":"response","status":"incomplete","output":[],"incomplete_details":{"reason":"content_filter"}}}`), okTurn()}, 1},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeResponses(t, tc.replies...)
			p := newTestProvider(f, &fakeTokens{tokens: []string{"tok-1"}}, "")

			resp, err := p.Complete(context.Background(), firstTurn())
			require.ErrorIs(t, err, ErrBackend)
			assert.Contains(t, err.Error(), "SLOP_REVIEW_PROVIDER=openrouter")
			assert.Equal(t, tc.requests, f.requests())
			assert.Equal(t, int64(tc.requests), resp.Usage.Requests, "a failed turn still reports the requests it sent")
		})
	}
}

func TestResponses_TokenNeverAppearsInErrors(t *testing.T) {
	const secret = "tok-very-secret"
	// A backend that echoes the credential back is the worst case: the
	// error text must still not carry it, nor anything it wraps.
	echo := httpError(http.StatusBadRequest, `{"error":{"message":"bad header Bearer `+secret+`"}}`)
	f := newFakeResponses(t, echo)
	p := newTestProvider(f, &fakeTokens{tokens: []string{secret}}, "")

	_, err := p.Complete(context.Background(), firstTurn())
	require.Error(t, err)
	for e := err; e != nil; e = errors.Unwrap(e) {
		assert.NotContains(t, e.Error(), secret)
	}
	assert.Contains(t, err.Error(), "[redacted]")
}

func TestClassify(t *testing.T) {
	assert.Equal(t, classTransient, classify(errors.New("connection reset by peer")))
	assert.Equal(t, classUsageLimit, classify(errors.New("received error while streaming: {\"code\":\"usage_limit_reached\"}")))
	assert.Equal(t, classTransient, classify(&streamError{code: "server_error"}))
	assert.Equal(t, classBackend, classify(&streamError{code: "invalid_prompt"}))
	assert.True(t, strings.HasPrefix(ErrUsageLimit.Error(), "ChatGPT"))
}

// A recorded response.completed with the usage details the Responses API
// sends: the cached part of the prompt comes out of input_tokens, which
// counts it in, and reasoning stays a part of output.
func TestResponses_UsageDetailsAreRead(t *testing.T) {
	withDetails := `{"type":"response.completed","sequence_number":99,"response":{"id":"resp_1","object":"response","status":"completed","output":[],` +
		`"usage":{"input_tokens":1200,"input_tokens_details":{"cached_tokens":1000},"output_tokens":300,"output_tokens_details":{"reasoning_tokens":250},"total_tokens":1500}}}`
	for name, tc := range map[string]struct {
		completed string
		want      Usage
	}{
		"with details":    {withDetails, Usage{Requests: 1, InputTokens: 200, CacheReadTokens: 1000, OutputTokens: 300, ReasoningTokens: 250}},
		"without details": {completed(1200, 300), Usage{Requests: 1, InputTokens: 1200, OutputTokens: 300}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeResponses(t, sse(itemDone(0, functionCallItem("fc_1", "call_1", "read_file", `{}`)), tc.completed))
			p := newTestProvider(f, &fakeTokens{tokens: []string{"tok-1"}}, "")
			resp, err := p.Complete(context.Background(), firstTurn())
			require.NoError(t, err)
			assert.Equal(t, tc.want, resp.Usage)
			assert.Nil(t, resp.Usage.ReportedCostMicros, "a subscription reports no cost")
		})
	}
}
