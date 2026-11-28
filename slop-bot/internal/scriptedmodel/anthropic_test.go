package scriptedmodel

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func anthropicTools() []any {
	return []any{map[string]any{"name": "bash"}, map[string]any{"name": "mcp__slop-haus__ticket_next"}}
}

func messageBody(tools []any, stream bool) map[string]any {
	body := map[string]any{
		"model":    "scripted-1",
		"system":   []any{map[string]any{"type": "text", "text": "the briefing"}},
		"messages": []any{map[string]any{"role": "user", "content": "go"}},
		"stream":   stream,
	}
	if tools != nil {
		body["tools"] = tools
	}
	return body
}

func anthropicEncode(t *testing.T, turn *Turn, stream bool) []byte {
	t.Helper()
	a := anthropicMessages{}
	return encodeTurn(a, turn, mustParse(t, a, nil, messageBody(anthropicTools(), stream)), "msg_1")
}

func TestAnthropicParseExtractsWhatAssertionsNeed(t *testing.T) {
	t.Parallel()
	req := mustParse(t, anthropicMessages{}, nil, messageBody(anthropicTools(), false))
	assert.Equal(t, "anthropic", req.API)
	assert.Equal(t, "scripted-1", req.Model)
	assert.Equal(t, "the briefing", req.System)
	assert.Equal(t, []string{"bash", "mcp__slop-haus__ticket_next"}, req.ToolNames)
	assert.False(t, req.Auxiliary)
	assert.False(t, req.Stream)
}

func TestAnthropicParseReadsAStringSystem(t *testing.T) {
	t.Parallel()
	body := messageBody(anthropicTools(), false)
	body["system"] = "plain"
	assert.Equal(t, "plain", mustParse(t, anthropicMessages{}, nil, body).System)
}

func TestAnthropicParseFlattensMessagesAndToolResults(t *testing.T) {
	t.Parallel()
	body := messageBody(anthropicTools(), false)
	body["messages"] = []any{
		map[string]any{"role": "user", "content": "go"},
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "t", "name": "bash", "input": map[string]any{}}}},
		map[string]any{"role": "user", "content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": "t", "content": []any{map[string]any{"type": "text", "text": "ticket #7"}},
		}}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "u", "content": "plain result"}}},
	}
	req := mustParse(t, anthropicMessages{}, nil, body)
	assert.Equal(t, []string{"ticket #7", "plain result"}, req.ToolResults)
	assert.Equal(t, "go\nticket #7\nplain result", req.MessageText)
	assert.Len(t, req.Messages, 4)
}

func TestAnthropicARequestWithNoToolsIsTheHarnessesOwn(t *testing.T) {
	t.Parallel()
	assert.True(t, mustParse(t, anthropicMessages{}, nil, messageBody(nil, false)).Auxiliary)
}

func TestAnthropicJSONResponseCarriesTextToolCallsAndExactUsage(t *testing.T) {
	t.Parallel()
	turn := &Turn{
		Text:      "working",
		ToolCalls: []*ToolCall{call("bash", map[string]any{"command": "git status"})},
		Usage:     Usage{InputTokens: 123, OutputTokens: 45, CacheReadInputTokens: 6, CacheCreationInputTokens: 7},
	}
	body := decodeJSON(t, anthropicEncode(t, turn, false))
	assert.Equal(t, "tool_use", body["stop_reason"])
	assert.Equal(t, []any{
		map[string]any{"type": "text", "text": "working"},
		map[string]any{"type": "tool_use", "id": "msg_1_0", "name": "bash", "input": map[string]any{"command": "git status"}},
	}, body["content"])
	assert.Equal(t, map[string]any{
		"input_tokens": 123.0, "output_tokens": 45.0, "cache_read_input_tokens": 6.0, "cache_creation_input_tokens": 7.0,
	}, body["usage"])
	assert.Equal(t, "scripted-1", body["model"])
}

func TestAnthropicTextOnlyTurnEndsTheTurn(t *testing.T) {
	t.Parallel()
	body := decodeJSON(t, anthropicEncode(t, &Turn{Text: "done"}, false))
	assert.Equal(t, "end_turn", body["stop_reason"])
}

func TestAnthropicSSEStreamIsACompleteMessage(t *testing.T) {
	t.Parallel()
	turn := &Turn{
		Text:      "hi",
		ToolCalls: []*ToolCall{call("bash", map[string]any{"command": "ls"})},
		Usage:     Usage{InputTokens: 9, OutputTokens: 3},
	}
	events := parseSSE(t, anthropicEncode(t, turn, true))
	names := make([]string, 0, len(events))
	for _, e := range events {
		names = append(names, e.event)
		assert.Equal(t, e.event, e.data["type"], "each event's data names its event")
	}
	assert.Equal(t, []string{
		"message_start",
		"content_block_start", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_delta", "content_block_stop",
		"message_delta", "message_stop",
	}, names)
	assert.InDelta(t, 9, path(t, events[0].data, "message", "usage", "input_tokens"), 0)
	assert.Nil(t, path(t, events[0].data, "message", "stop_reason"))
	assert.Equal(t, map[string]any{"type": "text_delta", "text": "hi"}, events[2].data["delta"])
	assert.Equal(t, map[string]any{"type": "tool_use", "id": "msg_1_0", "name": "bash", "input": map[string]any{}},
		events[4].data["content_block"])
	var input map[string]any
	require.NoError(t, json.Unmarshal([]byte(path(t, events[5].data, "delta", "partial_json").(string)), &input))
	assert.Equal(t, map[string]any{"command": "ls"}, input)
	assert.Equal(t, "tool_use", path(t, events[7].data, "delta", "stop_reason"))
	assert.InDelta(t, 3, path(t, events[7].data, "usage", "output_tokens"), 0)
}

func TestAnthropicErrorIsTheAPIsShape(t *testing.T) {
	t.Parallel()
	body := decodeJSON(t, anthropicMessages{}.encodeError(500, "boom"))
	assert.Equal(t, "error", body["type"])
	assert.Equal(t, "boom", path(t, body, "error", "message"))
}

func TestAnthropicParseRefusesBadJSON(t *testing.T) {
	t.Parallel()
	_, err := anthropicMessages{}.parse(nil, []byte("{"))
	require.Error(t, err)
}
