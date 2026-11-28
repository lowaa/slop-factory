package scriptedmodel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// anthropicMessages is the Anthropic Messages API (POST /v1/messages),
// streaming and not: what Claude Code speaks.
type anthropicMessages struct{}

type anthropicBody struct {
	Model    string            `json:"model"`
	System   json.RawMessage   `json:"system"`
	Messages []json.RawMessage `json:"messages"`
	Tools    []struct {
		Name string `json:"name"`
	} `json:"tools"`
	Stream bool `json:"stream"`
}

type anthropicMessage struct {
	Content json.RawMessage `json:"content"`
}

type anthropicBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Content json.RawMessage `json:"content"`
}

func (anthropicMessages) name() string { return "anthropic" }

func (a anthropicMessages) parse(_ *http.Request, body []byte) (*Request, error) {
	var b anthropicBody
	if err := decodeBody(body, &b); err != nil {
		return nil, err
	}
	req := &Request{
		API:      a.name(),
		Model:    b.Model,
		System:   textOf(b.System, "text"),
		Messages: b.Messages,
		Stream:   b.Stream,
		Raw:      body,
	}
	for _, t := range b.Tools {
		if t.Name != "" {
			req.ToolNames = append(req.ToolNames, t.Name)
		}
	}
	req.MessageText, req.ToolResults = anthropicFlatten(b.Messages)
	return req.normalise(), nil
}

// anthropicFlatten returns all the message text of a conversation, and the
// tool results' text alone.
func anthropicFlatten(messages []json.RawMessage) (string, []string) {
	var texts, results []string
	for _, raw := range messages {
		var m anthropicMessage
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		var s string
		if json.Unmarshal(m.Content, &s) == nil {
			texts = append(texts, s)
			continue
		}
		var blocks []anthropicBlock
		_ = json.Unmarshal(m.Content, &blocks)
		for _, block := range blocks {
			switch block.Type {
			case "text":
				texts = append(texts, block.Text)
			case "tool_result":
				text := textOf(block.Content, "text")
				results = append(results, text)
				texts = append(texts, text)
			}
		}
	}
	return strings.Join(texts, "\n"), results
}

func anthropicUsage(u *Usage) map[string]int {
	return map[string]int{
		"input_tokens":                u.InputTokens,
		"output_tokens":               u.OutputTokens,
		"cache_read_input_tokens":     u.CacheReadInputTokens,
		"cache_creation_input_tokens": u.CacheCreationInputTokens,
	}
}

func (anthropicMessages) encode(turn *Turn, req *Request, responseID string) [][]byte {
	content := []map[string]any{}
	if turn.Text != "" {
		content = append(content, map[string]any{"type": "text", "text": turn.Text})
	}
	for n, call := range turn.ToolCalls {
		content = append(content, map[string]any{
			"type": "tool_use", "id": fmt.Sprintf("%s_%d", responseID, n), "name": call.Name, "input": call.inputOrEmpty(),
		})
	}
	stopReason := "end_turn"
	if len(turn.ToolCalls) > 0 {
		stopReason = "tool_use"
	}
	message := map[string]any{
		"id":            responseID,
		"type":          "message",
		"role":          "assistant",
		"model":         req.Model,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage":         anthropicUsage(&turn.Usage),
	}
	if !req.Stream {
		message["content"] = content
		return [][]byte{mustJSON(message)}
	}
	return anthropicStream(message, content, turn)
}

func anthropicStream(message map[string]any, content []map[string]any, turn *Turn) [][]byte {
	start := map[string]any{}
	for k, v := range message {
		start[k] = v
	}
	start["content"] = []any{}
	start["stop_reason"] = nil
	chunks := [][]byte{sseEvent("message_start", map[string]any{"type": "message_start", "message": start})}
	for index, block := range content {
		var blockStart, delta map[string]any
		if block["type"] == "text" {
			blockStart = map[string]any{"type": "text", "text": ""}
			delta = map[string]any{"type": "text_delta", "text": block["text"]}
		} else {
			blockStart = map[string]any{"type": "tool_use", "id": block["id"], "name": block["name"], "input": map[string]any{}}
			delta = map[string]any{"type": "input_json_delta", "partial_json": string(mustJSON(block["input"]))}
		}
		chunks = append(chunks,
			sseEvent("content_block_start", map[string]any{"type": "content_block_start", "index": index, "content_block": blockStart}),
			sseEvent("content_block_delta", map[string]any{"type": "content_block_delta", "index": index, "delta": delta}),
			sseEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": index}),
		)
	}
	return append(chunks,
		sseEvent("message_delta", map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": message["stop_reason"], "stop_sequence": nil},
			"usage": anthropicUsage(&turn.Usage),
		}),
		sseEvent("message_stop", map[string]any{"type": "message_stop"}),
	)
}

func (anthropicMessages) encodeError(_ int, message string) []byte {
	return mustJSON(map[string]any{"type": "error", "error": map[string]any{"type": "invalid_request_error", "message": message}})
}

func (anthropicMessages) summarise(req *Request) string { return summarise(req, "message") }
