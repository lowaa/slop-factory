package scriptedmodel_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/scriptedmodel"
)

func newServer(t *testing.T, opts *scriptedmodel.Options) (*scriptedmodel.Server, *scriptedmodel.Client) {
	t.Helper()
	server, err := scriptedmodel.New(opts)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, server.Close()) })
	return server, scriptedmodel.NewClient(server.URL(), nil)
}

type response struct {
	status      int
	contentType string
	body        []byte
}

func send(t *testing.T, method, url string, body any) *response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, ok := body.([]byte)
		if !ok {
			var err error
			data, err = json.Marshal(body)
			require.NoError(t, err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, url, reader)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return &response{status: resp.StatusCode, contentType: resp.Header.Get("Content-Type"), body: data}
}

// sendAsync posts from a goroutine; the channel gets the response, or nil if
// the request itself failed.
func sendAsync(ctx context.Context, url string, body any) <-chan *response {
	out := make(chan *response, 1)
	go func() {
		data, _ := json.Marshal(body)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
		if err != nil {
			out <- nil
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			out <- nil
			return
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		out <- &response{status: resp.StatusCode, body: raw}
	}()
	return out
}

func messagesBody(stream bool) map[string]any {
	return map[string]any{
		"model":    "scripted-1",
		"system":   []any{map[string]any{"type": "text", "text": "the briefing"}},
		"messages": []any{map[string]any{"role": "user", "content": "go"}},
		"tools":    []any{map[string]any{"name": "bash"}, map[string]any{"name": "mcp__slop-haus__ticket_next"}},
		"stream":   stream,
	}
}

func textTurn(text string, gate string) *scriptedmodel.Scenario {
	return &scriptedmodel.Scenario{Name: "s", Turns: []*scriptedmodel.Turn{{Text: text, Gate: gate, Usage: scriptedmodel.Usage{InputTokens: 4}}}}
}

func TestServerServesAStreamedTurnAndExposesTheRequest(t *testing.T) {
	t.Parallel()
	server, client := newServer(t, nil)
	require.NoError(t, client.Load(t.Context(), textTurn("hello", "")))

	resp := send(t, http.MethodPost, server.URL()+"/v1/messages?beta=true", messagesBody(true))
	require.Equal(t, http.StatusOK, resp.status, string(resp.body))
	assert.Equal(t, "text/event-stream", resp.contentType)
	assert.Contains(t, string(resp.body), `"input_tokens":4`)
	assert.Contains(t, string(resp.body), `"text":"hello"`)

	requests, err := client.Requests(t.Context())
	require.NoError(t, err)
	require.Len(t, requests, 1)
	assert.Equal(t, "the briefing", requests[0].System)
	assert.Equal(t, []string{"bash", "mcp__slop-haus__ticket_next"}, requests[0].ToolNames)
	assert.JSONEq(t, string(mustMarshal(messagesBody(true))), string(requests[0].Raw))
	remaining, err := client.Remaining(t.Context())
	require.NoError(t, err)
	assert.Zero(t, remaining)
}

// Every API serves the same API-free scenario.
func TestServerServesEveryAPIFromTheSameScenario(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		api, path string
		body      map[string]any
		want      []string
	}{
		{"anthropic", "/v1/messages", messagesBody(false), []string{`"text":"hello"`, `"input_tokens":4`}},
		{"openai", "/v1/chat/completions", map[string]any{
			"model":    "scripted-1",
			"messages": []any{map[string]any{"role": "system", "content": "the briefing"}, map[string]any{"role": "user", "content": "go"}},
			"tools":    []any{map[string]any{"type": "function", "function": map[string]any{"name": "bash"}}},
			"stream":   true,
		}, []string{`"content":"hello"`, `"prompt_tokens":4`, "data: [DONE]"}},
		{"responses", "/v1/responses", map[string]any{
			"model": "scripted-1", "instructions": "the briefing", "input": "go",
			"tools": []any{map[string]any{"type": "function", "name": "exec_command"}}, "stream": true,
		}, []string{`"text":"hello"`, "event: response.completed"}},
		{"gemini", "/v1beta/models/scripted-1:streamGenerateContent?alt=sse", map[string]any{
			"contents":          []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "go"}}}},
			"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": "the briefing"}}},
			"tools":             []any{map[string]any{"functionDeclarations": []any{map[string]any{"name": "run_command"}}}},
		}, []string{`"text":"hello"`, `"promptTokenCount":4`}},
	} {
		t.Run(tc.api, func(t *testing.T) {
			t.Parallel()
			server, _ := newServer(t, nil)
			require.NoError(t, server.Load(textTurn("hello", "")))
			resp := send(t, http.MethodPost, server.URL()+tc.path, tc.body)
			require.Equal(t, http.StatusOK, resp.status, string(resp.body))
			for _, want := range tc.want {
				assert.Contains(t, string(resp.body), want)
			}
			rec := server.Recording()
			require.Len(t, rec.Requests, 1)
			assert.Equal(t, tc.api, rec.Requests[0].API)
			assert.Equal(t, "the briefing", rec.Requests[0].System)
			assert.Equal(t, "scripted-1", rec.Requests[0].Model)
			assert.Empty(t, rec.Errors)
		})
	}
}

func TestServerAnswersAnAuxiliaryRequestWithoutUsingTheScript(t *testing.T) {
	t.Parallel()
	server, _ := newServer(t, nil)
	require.NoError(t, server.Load(textTurn("hello", "")))
	body := messagesBody(false)
	delete(body, "tools")
	resp := send(t, http.MethodPost, server.URL()+"/v1/messages", body)
	require.Equal(t, http.StatusOK, resp.status)
	assert.Contains(t, string(resp.body), `"text":"ok"`)
	assert.Equal(t, 1, server.Remaining())
	assert.Len(t, server.Recording().Requests, 1)
	assert.Empty(t, server.Recording().Conversation())
}

func TestServerAnUnexpectedRequestIsAnErrorTheHarnessAndTheTestBothSee(t *testing.T) {
	t.Parallel()
	server, client := newServer(t, nil)
	require.NoError(t, client.Load(t.Context(), textTurn("only", "")))
	require.Equal(t, http.StatusOK, send(t, http.MethodPost, server.URL()+"/v1/messages", messagesBody(false)).status)

	resp := send(t, http.MethodPost, server.URL()+"/v1/messages", messagesBody(false))
	assert.Equal(t, http.StatusInternalServerError, resp.status)
	var body struct {
		Error struct{ Message string } `json:"error"`
	}
	require.NoError(t, json.Unmarshal(resp.body, &body))
	assert.Contains(t, body.Error.Message, "only 1 turn(s)")

	errs, err := client.Errors(t.Context())
	require.NoError(t, err)
	assert.Len(t, errs, 1)
}

func TestServerAnAPIItDoesNotSpeakIsAnError(t *testing.T) {
	t.Parallel()
	server, client := newServer(t, nil)
	assert.Equal(t, http.StatusNotFound, send(t, http.MethodPost, server.URL()+"/v1/embeddings", map[string]any{"model": "x"}).status)
	assert.Equal(t, http.StatusBadRequest,
		send(t, http.MethodPost, server.URL()+"/v1beta/models/m:countTokens", map[string]any{}).status,
		"a Gemini method it does not speak is refused")
	errs, err := client.Errors(t.Context())
	require.NoError(t, err)
	require.Len(t, errs, 2)
	assert.Contains(t, errs[0], "/v1/embeddings")
	assert.Contains(t, errs[1], "countTokens")
}

func TestServerAnUnknownGETIsNotAnError(t *testing.T) {
	t.Parallel()
	server, _ := newServer(t, nil)
	assert.Equal(t, http.StatusNotFound, send(t, http.MethodGet, server.URL()+"/nothing", nil).status)
	assert.Empty(t, server.Recording().Errors)
}

func TestServerBadJSONIsAnError(t *testing.T) {
	t.Parallel()
	server, _ := newServer(t, nil)
	resp := send(t, http.MethodPost, server.URL()+"/v1/chat/completions", []byte("{"))
	assert.Equal(t, http.StatusBadRequest, resp.status)
	errs := server.Recording().Errors
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "bad JSON")
}

func TestServerAGatedTurnIsHeldUntilTheGateOpens(t *testing.T) {
	t.Parallel()
	server, _ := newServer(t, nil)
	require.NoError(t, server.Load(textTurn("late", "g")))
	result := sendAsync(t.Context(), server.URL()+"/v1/messages", messagesBody(false))

	select {
	case <-server.Held("g"):
	case r := <-result:
		t.Fatalf("answered before the gate was opened: %+v", r)
	}
	select {
	case r := <-result:
		t.Fatalf("answered before the gate was opened: %+v", r)
	default:
	}
	server.Release("g")
	r := <-result
	require.NotNil(t, r)
	assert.Equal(t, http.StatusOK, r.status)
	assert.Contains(t, string(r.body), "late")
}

func TestServerAGateOpenedEarlyLetsItsTurnThrough(t *testing.T) {
	t.Parallel()
	server, client := newServer(t, nil)
	require.NoError(t, server.Load(textTurn("late", "g")))
	require.NoError(t, client.Release(t.Context(), "g"))
	assert.Equal(t, http.StatusOK, send(t, http.MethodPost, server.URL()+"/v1/messages", messagesBody(false)).status)
}

func TestServerReplacingTheScenarioReleasesAHeldRequestWithoutAnError(t *testing.T) {
	t.Parallel()
	server, client := newServer(t, nil)
	require.NoError(t, client.Load(t.Context(), textTurn("late", "g")))
	result := sendAsync(t.Context(), server.URL()+"/v1/messages", messagesBody(false))
	require.NoError(t, client.HeldGate(t.Context(), "g"))
	require.NoError(t, client.Reset(t.Context()))
	r := <-result
	require.NotNil(t, r, "the held request was not answered")
	errs, err := client.Errors(t.Context())
	require.NoError(t, err)
	assert.Empty(t, errs)
}

func TestServerAGateNobodyOpensIsAnError(t *testing.T) {
	t.Parallel()
	// The timeout is the behaviour under test, not a wait: nothing opens the
	// gate, so it runs out whatever its length.
	server, _ := newServer(t, &scriptedmodel.Options{GateTimeout: time.Millisecond})
	require.NoError(t, server.Load(textTurn("late", "g")))
	resp := send(t, http.MethodPost, server.URL()+"/v1/messages", messagesBody(false))
	assert.Equal(t, http.StatusInternalServerError, resp.status)
	assert.Contains(t, string(resp.body), "was not opened")
	assert.Len(t, server.Recording().Errors, 1)
}

func TestServerCloseReleasesAHeldRequest(t *testing.T) {
	t.Parallel()
	server, err := scriptedmodel.New(nil)
	require.NoError(t, err)
	require.NoError(t, server.Load(textTurn("late", "g")))
	result := sendAsync(t.Context(), server.URL()+"/v1/messages", messagesBody(false))
	<-server.Held("g")
	require.NoError(t, server.Close())
	<-result
	require.NoError(t, server.Close(), "a second Close is a no-op")
}

func TestServerABadScenarioIsRefused(t *testing.T) {
	t.Parallel()
	server, client := newServer(t, nil)
	resp := send(t, http.MethodPut, server.URL()+"/_scripted/scenario", map[string]any{"turns": []any{map[string]any{}}})
	assert.Equal(t, http.StatusBadRequest, resp.status)
	assert.Contains(t, string(resp.body), "needs text")
	err := client.Load(t.Context(), &scriptedmodel.Scenario{Turns: []*scriptedmodel.Turn{{}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "400")
	require.ErrorIs(t, server.Load(&scriptedmodel.Scenario{Turns: []*scriptedmodel.Turn{{}}}), scriptedmodel.ErrScenario)
}

func TestServerControlEndpointsMatchThePythonServers(t *testing.T) {
	t.Parallel()
	server, _ := newServer(t, nil)
	assert.JSONEq(t, `{"ok":true}`, string(send(t, http.MethodGet, server.URL()+"/healthz", nil).body))
	assert.JSONEq(t, `[]`, string(send(t, http.MethodGet, server.URL()+"/_scripted/requests", nil).body))
	assert.JSONEq(t, `[]`, string(send(t, http.MethodGet, server.URL()+"/_scripted/errors", nil).body))
	assert.JSONEq(t, `{"remaining":0}`, string(send(t, http.MethodGet, server.URL()+"/_scripted/remaining", nil).body))
	put := send(t, http.MethodPut, server.URL()+"/_scripted/scenario", []byte(`{"name":"s","turns":[{"text":"a"},{"text":"b"}]}`))
	assert.Equal(t, http.StatusOK, put.status, string(put.body))
	assert.JSONEq(t, `{"remaining":2}`, string(send(t, http.MethodGet, server.URL()+"/_scripted/remaining", nil).body))
	assert.Equal(t, http.StatusOK, send(t, http.MethodPut, server.URL()+"/_scripted/gates/g", nil).status)
	assert.Equal(t, http.StatusOK, send(t, http.MethodDelete, server.URL()+"/_scripted/scenario", nil).status)
	assert.JSONEq(t, `{"remaining":0}`, string(send(t, http.MethodGet, server.URL()+"/_scripted/remaining", nil).body))
}

func TestServerServesGeminiModelLists(t *testing.T) {
	t.Parallel()
	server, _ := newServer(t, &scriptedmodel.Options{Models: []string{"gemini-3.1-pro-preview"}})
	beta := send(t, http.MethodGet, server.URL()+"/v1beta/models?pageSize=1000", nil)
	require.Equal(t, http.StatusOK, beta.status)
	assert.Contains(t, string(beta.body), `"name":"models/gemini-3.1-pro-preview"`)
	v1 := send(t, http.MethodGet, server.URL()+"/v1/models", nil)
	require.Equal(t, http.StatusOK, v1.status)
	assert.Contains(t, string(v1.body), `"name":"models/gemini-3.1-pro-preview"`)
	assert.Contains(t, string(v1.body), `"id":"gemini-3.1-pro-preview"`)
	assert.Empty(t, server.Recording().Requests, "a model list is not a model request")
}

func TestServerListsTheDefaultModels(t *testing.T) {
	t.Parallel()
	server, _ := newServer(t, nil)
	body := string(send(t, http.MethodGet, server.URL()+"/v1beta/models", nil).body)
	for _, m := range scriptedmodel.DefaultModels {
		assert.True(t, strings.Contains(body, "models/"+m), m)
	}
}
