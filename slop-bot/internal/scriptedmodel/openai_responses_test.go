package scriptedmodel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func responsesTools() []any {
	return []any{
		map[string]any{"type": "function", "name": "exec_command", "parameters": map[string]any{}},
		map[string]any{"type": "namespace", "name": "mcp__slop_haus", "tools": []any{
			map[string]any{"type": "function", "name": "ticket_get", "parameters": map[string]any{}},
		}},
	}
}

func responsesBodyForTest(tools []any, stream bool) map[string]any {
	body := map[string]any{
		"model":        "scripted-model-1",
		"instructions": "the briefing",
		"input": []any{map[string]any{
			"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "go"}},
		}},
		"reasoning": map[string]any{"effort": "low"},
		"stream":    stream,
	}
	if tools != nil {
		body["tools"] = tools
	}
	return body
}

func responsesEncode(t *testing.T, turn *Turn, stream bool) []byte {
	t.Helper()
	a := openAIResponses{}
	return encodeTurn(a, turn, mustParse(t, a, nil, responsesBodyForTest(responsesTools(), stream)), "resp_1")
}

func TestResponsesParseExtractsWhatAssertionsNeed(t *testing.T) {
	t.Parallel()
	req := mustParse(t, openAIResponses{}, nil, responsesBodyForTest(responsesTools(), true))
	assert.Equal(t, "responses", req.API)
	assert.Equal(t, "the briefing", req.System)
	assert.Equal(t, []string{"exec_command", "ticket_get"}, req.ToolNames)
	assert.Equal(t, "go", req.MessageText)
	assert.False(t, req.Auxiliary)
	assert.True(t, req.Stream)
}

func TestResponsesParseReadsToolResultsAndDeveloperMessages(t *testing.T) {
	t.Parallel()
	body := responsesBodyForTest(responsesTools(), true)
	body["input"] = append(body["input"].([]any),
		map[string]any{"type": "message", "role": "developer", "content": []any{map[string]any{"type": "input_text", "text": "rules"}}},
		map[string]any{"type": "function_call", "name": "exec_command", "arguments": `{"cmd":"ls"}`, "call_id": "c1"},
		map[string]any{"type": "function_call_output", "call_id": "c1", "output": "file.txt"},
		map[string]any{"type": "custom_tool_call_output", "call_id": "c2", "output": []any{map[string]any{"type": "input_text", "text": "patched"}}},
	)
	req := mustParse(t, openAIResponses{}, nil, body)
	assert.Equal(t, "the briefing\nrules", req.System)
	assert.Equal(t, []string{"file.txt", "patched"}, req.ToolResults)
	assert.Contains(t, req.MessageText, `{"cmd":"ls"}`)
}

func TestResponsesParseTakesAStringInputAsAUserMessage(t *testing.T) {
	t.Parallel()
	body := responsesBodyForTest(responsesTools(), true)
	body["input"] = "hello"
	req := mustParse(t, openAIResponses{}, nil, body)
	assert.Equal(t, "hello", req.MessageText)
	require.Len(t, req.Messages, 1)
	assert.JSONEq(t, `{"type":"message","role":"user","content":"hello"}`, string(req.Messages[0]))
}

func TestResponsesARequestWithNoToolsIsTheHarnessesOwn(t *testing.T) {
	t.Parallel()
	assert.True(t, mustParse(t, openAIResponses{}, nil, responsesBodyForTest(nil, true)).Auxiliary)
}

func TestResponsesStreamIsCreatedItemsThenCompletedWithUsage(t *testing.T) {
	t.Parallel()
	turn := &Turn{
		Text:      "hi",
		ToolCalls: []*ToolCall{call("ticket_get", map[string]any{"ticket_id": 1})},
		Usage:     Usage{InputTokens: 100, OutputTokens: 10, CacheReadInputTokens: 20},
	}
	events := parseSSE(t, responsesEncode(t, turn, true))
	names := make([]string, 0, len(events))
	for _, e := range events {
		names = append(names, e.event)
	}
	assert.Equal(t, []string{"response.created", "response.output_item.done", "response.output_item.done", "response.completed"}, names)
	message, fnCall := events[1].data["item"], events[2].data["item"]
	assert.Equal(t, map[string]any{"type": "output_text", "text": "hi", "annotations": []any{}}, path(t, message, "content", 0))
	assert.Equal(t, "function_call", path(t, fnCall, "type"))
	assert.Equal(t, "ticket_get", path(t, fnCall, "name"))
	assert.Equal(t, "mcp__slop_haus", path(t, fnCall, "namespace"))
	assert.Equal(t, "resp_1_call0", path(t, fnCall, "call_id"))
	assert.JSONEq(t, `{"ticket_id":1}`, path(t, fnCall, "arguments").(string))
	usage := path(t, events[3].data, "response", "usage")
	assert.InDelta(t, 120, path(t, usage, "input_tokens"), 0)
	assert.InDelta(t, 10, path(t, usage, "output_tokens"), 0)
	assert.InDelta(t, 20, path(t, usage, "input_tokens_details", "cached_tokens"), 0)
	assert.Equal(t, "completed", path(t, events[3].data, "response", "status"))
}

func TestResponsesATopLevelToolHasNoNamespace(t *testing.T) {
	t.Parallel()
	events := parseSSE(t, responsesEncode(t, &Turn{ToolCalls: []*ToolCall{call("exec_command", map[string]any{"cmd": "ls"})}}, true))
	assert.NotContains(t, events[1].data["item"], "namespace")
}

func TestResponsesNonStreamedResponseIsOneObject(t *testing.T) {
	t.Parallel()
	body := decodeJSON(t, responsesEncode(t, &Turn{Text: "hi", Usage: Usage{InputTokens: 5, OutputTokens: 2}}, false))
	assert.Equal(t, "completed", body["status"])
	assert.Equal(t, "message", path(t, body, "output", 0, "type"))
	assert.InDelta(t, 7, path(t, body, "usage", "total_tokens"), 0)
}
