package scriptedmodel

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func geminiHTTPRequest(call, query string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+call+query, nil)
	r.SetPathValue("call", call)
	return r
}

func geminiTools() []any {
	return []any{map[string]any{"functionDeclarations": []any{
		map[string]any{"name": "run_command", "parameters": map[string]any{"type": "OBJECT"}},
		map[string]any{"name": "read_file", "parameters": map[string]any{"type": "OBJECT"}},
	}}}
}

// geminiBodyForTest is shaped as agy 1.5.18 sends it.
func geminiBodyForTest(tools []any) map[string]any {
	body := map[string]any{
		"contents":          []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "go"}}}},
		"systemInstruction": map[string]any{"role": "user", "parts": []any{map[string]any{"text": "the briefing"}}},
		"generationConfig":  map[string]any{"maxOutputTokens": 65536, "thinkingConfig": map[string]any{"includeThoughts": true}},
		"labels":            map[string]any{"client": "antigravity-cli"},
	}
	if tools != nil {
		body["tools"] = tools
	}
	return body
}

const geminiStreamCall = "gemini-3.1-pro-preview:streamGenerateContent"

func geminiEncode(t *testing.T, turn *Turn, call, query string) []byte {
	t.Helper()
	g := gemini{}
	return encodeTurn(g, turn, mustParse(t, g, geminiHTTPRequest(call, query), geminiBodyForTest(geminiTools())), "msg_1")
}

func TestGeminiParseExtractsWhatAssertionsNeed(t *testing.T) {
	t.Parallel()
	req := mustParse(t, gemini{}, geminiHTTPRequest(geminiStreamCall, "?alt=sse"), geminiBodyForTest(geminiTools()))
	assert.Equal(t, "gemini", req.API)
	assert.Equal(t, "gemini-3.1-pro-preview", req.Model, "the model comes from the path")
	assert.Equal(t, "the briefing", req.System)
	assert.Equal(t, "go", req.MessageText)
	assert.Equal(t, []string{"run_command", "read_file"}, req.ToolNames)
	assert.True(t, req.Stream)
	assert.False(t, req.Auxiliary)
}

func TestGeminiParseTakesSnakeCaseFieldNames(t *testing.T) {
	t.Parallel()
	body := map[string]any{
		"contents":           []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "go"}}}},
		"system_instruction": map[string]any{"parts": []any{map[string]any{"text": "the briefing"}}},
		"tools":              []any{map[string]any{"function_declarations": []any{map[string]any{"name": "bash"}}}},
	}
	req := mustParse(t, gemini{}, geminiHTTPRequest("m:generateContent", ""), body)
	assert.Equal(t, "the briefing", req.System)
	assert.Equal(t, []string{"bash"}, req.ToolNames)
	assert.False(t, req.Stream)
}

func TestGeminiParseFlattensFunctionCallsAndResponses(t *testing.T) {
	t.Parallel()
	body := geminiBodyForTest(geminiTools())
	// A replayed turn as agy sends it back: its own thoughtSignature on the
	// call, the tool's output under "output" in the response.
	body["contents"] = append(body["contents"].([]any),
		map[string]any{"role": "model", "parts": []any{
			map[string]any{"text": "thinking it over", "thought": true},
			map[string]any{"functionCall": map[string]any{"name": "run_command", "args": map[string]any{"CommandLine": "echo hi"}}, "thoughtSignature": "c2tpcA=="},
		}},
		map[string]any{"role": "user", "parts": []any{
			map[string]any{"functionResponse": map[string]any{"name": "run_command", "response": map[string]any{"output": "Command output:\nhi\r\n"}}},
			map[string]any{"functionResponse": map[string]any{"name": "read_file", "response": map[string]any{"content": "x", "lines": 1}}},
		}},
	)
	req := mustParse(t, gemini{}, geminiHTTPRequest(geminiStreamCall, "?alt=sse"), body)
	assert.Equal(t, []string{"Command output:\nhi\r\n", `{"content":"x","lines":1}`}, req.ToolResults)
	assert.Contains(t, req.MessageText, `{"CommandLine":"echo hi"}`)
	assert.Contains(t, req.MessageText, "Command output:")
	assert.NotContains(t, req.MessageText, "thinking it over", "the model's thoughts are not what it was shown")
	assert.Len(t, req.Messages, 3)
}

func TestGeminiARequestWithNoToolsIsTheHarnessesOwn(t *testing.T) {
	t.Parallel()
	assert.True(t, mustParse(t, gemini{}, geminiHTTPRequest(geminiStreamCall, "?alt=sse"), geminiBodyForTest(nil)).Auxiliary)
}

func TestGeminiParseRefusesWhatItDoesNotSpeak(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, call, query string }{
		{"another method", "m:countTokens", ""},
		{"no method", "m", ""},
		{"no model", ":generateContent", ""},
		{"a stream without alt=sse", "m:streamGenerateContent", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := gemini{}.parse(geminiHTTPRequest(tc.call, tc.query), toJSON(t, geminiBodyForTest(geminiTools())))
			require.ErrorIs(t, err, errGeminiMethod)
		})
	}
}

func TestGeminiJSONResponseCarriesTextFunctionCallsAndUsage(t *testing.T) {
	t.Parallel()
	turn := &Turn{
		Text:      "working",
		ToolCalls: []*ToolCall{call("run_command", map[string]any{"CommandLine": "ls"}), call("read_file", nil)},
		Usage:     Usage{InputTokens: 100, OutputTokens: 7, CacheReadInputTokens: 20, CacheCreationInputTokens: 3},
	}
	body := decodeJSON(t, geminiEncode(t, turn, "gemini-3.1-pro-preview:generateContent", ""))
	candidate := path(t, body, "candidates", 0)
	assert.Equal(t, "model", path(t, candidate, "content", "role"))
	assert.Equal(t, []any{
		map[string]any{"text": "working"},
		map[string]any{"functionCall": map[string]any{"id": "msg_1_0", "name": "run_command", "args": map[string]any{"CommandLine": "ls"}}},
		map[string]any{"functionCall": map[string]any{"id": "msg_1_1", "name": "read_file", "args": map[string]any{}}},
	}, path(t, candidate, "content", "parts"))
	assert.Equal(t, "STOP", path(t, candidate, "finishReason"))
	// Cached tokens are inside promptTokenCount, as the API counts them.
	assert.Equal(t, map[string]any{
		"promptTokenCount": 120.0, "candidatesTokenCount": 7.0, "totalTokenCount": 127.0, "cachedContentTokenCount": 20.0,
	}, body["usageMetadata"])
	assert.Equal(t, "gemini-3.1-pro-preview", body["modelVersion"])
	assert.Equal(t, "msg_1", body["responseId"])
}

func TestGeminiUsageLeavesOutAnEmptyCacheCount(t *testing.T) {
	t.Parallel()
	body := decodeJSON(t, geminiEncode(t, &Turn{Text: "hi", Usage: Usage{InputTokens: 4, OutputTokens: 1}}, "m:generateContent", ""))
	assert.NotContains(t, body["usageMetadata"], "cachedContentTokenCount")
}

func TestGeminiSSEStreamIsOnePartPerChunkFinishingOnTheLast(t *testing.T) {
	t.Parallel()
	turn := &Turn{
		Text:      "hi",
		ToolCalls: []*ToolCall{call("run_command", map[string]any{"CommandLine": "ls"})},
		Usage:     Usage{InputTokens: 9, OutputTokens: 3},
	}
	chunks := parseDataLines(t, geminiEncode(t, turn, geminiStreamCall, "?alt=sse"))
	require.Len(t, chunks, 2)
	assert.Equal(t, []any{map[string]any{"text": "hi"}}, path(t, chunks[0], "candidates", 0, "content", "parts"))
	assert.NotContains(t, path(t, chunks[0], "candidates", 0), "finishReason")
	assert.NotContains(t, chunks[0], "usageMetadata")
	assert.Equal(t, "run_command", path(t, chunks[1], "candidates", 0, "content", "parts", 0, "functionCall", "name"))
	assert.Equal(t, "STOP", path(t, chunks[1], "candidates", 0, "finishReason"))
	assert.InDelta(t, 9, path(t, chunks[1], "usageMetadata", "promptTokenCount"), 0)
	assert.InDelta(t, 3, path(t, chunks[1], "usageMetadata", "candidatesTokenCount"), 0)
}

func TestGeminiErrorIsTheAPIsShape(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status int
		want   string
	}{
		{http.StatusInternalServerError, "INTERNAL"},
		{http.StatusBadRequest, "INVALID_ARGUMENT"},
		{http.StatusNotFound, "NOT_FOUND"},
	} {
		body := decodeJSON(t, gemini{}.encodeError(tc.status, "boom"))
		assert.Equal(t, map[string]any{"code": float64(tc.status), "message": "boom", "status": tc.want}, body["error"])
	}
}

func TestGeminiModelList(t *testing.T) {
	t.Parallel()
	gemini := decodeJSON(t, geminiModelList([]string{"m1", "m2"}, false))
	assert.Equal(t, "models/m1", path(t, gemini, "models", 0, "name"))
	assert.Equal(t, "m2", path(t, gemini, "models", 1, "baseModelId"))
	assert.Contains(t, path(t, gemini, "models", 0, "supportedGenerationMethods"), "streamGenerateContent")
	assert.NotContains(t, gemini, "data")

	both := decodeJSON(t, geminiModelList([]string{"m1"}, true))
	assert.Equal(t, "models/m1", path(t, both, "models", 0, "name"))
	assert.Equal(t, "list", both["object"])
	assert.Equal(t, "m1", path(t, both, "data", 0, "id"))
}
