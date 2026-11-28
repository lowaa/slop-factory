package scriptedmodel

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func chatTools() []any {
	return []any{
		map[string]any{"type": "function", "function": map[string]any{"name": "bash", "parameters": map[string]any{}}},
		map[string]any{"type": "function", "function": map[string]any{"name": "ticket_next", "parameters": map[string]any{}}},
	}
}

func chatBodyForTest(tools []any, stream bool) map[string]any {
	body := map[string]any{
		"model": "scripted-1",
		"messages": []any{
			map[string]any{"role": "system", "content": "the briefing"},
			map[string]any{"role": "user", "content": "go"},
		},
		"stream": stream,
	}
	if tools != nil {
		body["tools"] = tools
	}
	return body
}

func chatEncode(t *testing.T, turn *Turn, stream bool) []byte {
	t.Helper()
	a := openAIChat{}
	return encodeTurn(a, turn, mustParse(t, a, nil, chatBodyForTest(chatTools(), stream)), "msg_1")
}

func TestChatParseExtractsWhatAssertionsNeed(t *testing.T) {
	t.Parallel()
	req := mustParse(t, openAIChat{}, nil, chatBodyForTest(chatTools(), false))
	assert.Equal(t, "openai", req.API)
	assert.Equal(t, "scripted-1", req.Model)
	assert.Equal(t, "the briefing", req.System)
	assert.Equal(t, "go", req.MessageText)
	assert.Equal(t, []string{"bash", "ticket_next"}, req.ToolNames)
	assert.False(t, req.Auxiliary)
}

func TestChatParseFlattensToolResultsAndPartArrays(t *testing.T) {
	t.Parallel()
	body := chatBodyForTest(chatTools(), false)
	body["messages"] = append(body["messages"].([]any),
		map[string]any{"role": "developer", "content": []any{map[string]any{"type": "text", "text": "rules"}}},
		map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{
			"id": "t", "type": "function", "function": map[string]any{"name": "bash", "arguments": `{"command":"ls"}`},
		}}},
		map[string]any{"role": "tool", "tool_call_id": "t", "content": []any{map[string]any{"type": "text", "text": "ticket #7"}}},
	)
	req := mustParse(t, openAIChat{}, nil, body)
	assert.Equal(t, []string{"ticket #7"}, req.ToolResults)
	assert.Equal(t, "the briefing\nrules", req.System)
	assert.Contains(t, req.MessageText, "ticket #7")
	assert.Contains(t, req.MessageText, `{"command":"ls"}`)
	assert.NotContains(t, req.MessageText, "the briefing")
}

func TestChatARequestWithNoToolsIsTheHarnessesOwn(t *testing.T) {
	t.Parallel()
	assert.True(t, mustParse(t, openAIChat{}, nil, chatBodyForTest(nil, false)).Auxiliary)
}

func TestChatJSONResponseCarriesTextToolCallsAndUsage(t *testing.T) {
	t.Parallel()
	turn := &Turn{
		Text:      "working",
		ToolCalls: []*ToolCall{call("bash", map[string]any{"command": "git status"})},
		Usage:     Usage{InputTokens: 123, OutputTokens: 45, CacheReadInputTokens: 6},
	}
	body := decodeJSON(t, chatEncode(t, turn, false))
	choice := path(t, body, "choices", 0)
	assert.Equal(t, "tool_calls", path(t, choice, "finish_reason"))
	assert.Equal(t, "working", path(t, choice, "message", "content"))
	assert.Equal(t, "msg_1_0", path(t, choice, "message", "tool_calls", 0, "id"))
	assert.Equal(t, "bash", path(t, choice, "message", "tool_calls", 0, "function", "name"))
	var args map[string]any
	require.NoError(t, json.Unmarshal([]byte(path(t, choice, "message", "tool_calls", 0, "function", "arguments").(string)), &args))
	assert.Equal(t, map[string]any{"command": "git status"}, args)
	// Cached tokens are inside prompt_tokens, as OpenAI reports them.
	assert.InDelta(t, 129, path(t, body, "usage", "prompt_tokens"), 0)
	assert.InDelta(t, 6, path(t, body, "usage", "prompt_tokens_details", "cached_tokens"), 0)
	assert.InDelta(t, 45, path(t, body, "usage", "completion_tokens"), 0)
	assert.InDelta(t, 174, path(t, body, "usage", "total_tokens"), 0)
}

func TestChatTextOnlyTurnStops(t *testing.T) {
	t.Parallel()
	body := decodeJSON(t, chatEncode(t, &Turn{Text: "done"}, false))
	assert.Equal(t, "stop", path(t, body, "choices", 0, "finish_reason"))
	assert.NotContains(t, path(t, body, "choices", 0, "message"), "tool_calls")
}

func TestChatToolOnlyTurnHasNullContent(t *testing.T) {
	t.Parallel()
	body := decodeJSON(t, chatEncode(t, &Turn{ToolCalls: []*ToolCall{call("bash", nil)}}, false))
	message := path(t, body, "choices", 0, "message").(map[string]any)
	assert.Contains(t, message, "content")
	assert.Nil(t, message["content"])
	assert.Equal(t, "{}", path(t, message, "tool_calls", 0, "function", "arguments"))
}

func TestChatSSEStreamEndsWithUsageAndDone(t *testing.T) {
	t.Parallel()
	turn := &Turn{Text: "hi", ToolCalls: []*ToolCall{call("bash", map[string]any{"command": "ls"})}, Usage: Usage{InputTokens: 9, OutputTokens: 3}}
	chunks := parseDataLines(t, chatEncode(t, turn, true))
	n := len(chunks)
	require.Equal(t, 6, n)
	assert.Equal(t, "[DONE]", chunks[n-1])
	assert.Equal(t, []any{}, path(t, chunks[n-2], "choices"))
	assert.InDelta(t, 9, path(t, chunks[n-2], "usage", "prompt_tokens"), 0)
	assert.InDelta(t, 3, path(t, chunks[n-2], "usage", "completion_tokens"), 0)
	assert.Equal(t, map[string]any{"role": "assistant"}, path(t, chunks[0], "choices", 0, "delta"))
	assert.Equal(t, map[string]any{"content": "hi"}, path(t, chunks[1], "choices", 0, "delta"))
	toolCall := path(t, chunks[2], "choices", 0, "delta", "tool_calls", 0)
	assert.InDelta(t, 0, path(t, toolCall, "index"), 0)
	assert.Equal(t, "bash", path(t, toolCall, "function", "name"))
	assert.JSONEq(t, `{"command":"ls"}`, path(t, toolCall, "function", "arguments").(string))
	assert.Equal(t, "tool_calls", path(t, chunks[n-3], "choices", 0, "finish_reason"))
	for _, c := range chunks[:n-1] {
		assert.Equal(t, "chat.completion.chunk", path(t, c, "object"))
	}
}
