package scriptedmodel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// openAIChat is the OpenAI chat completions API (POST /v1/chat/completions),
// streaming and not: what pi's custom provider speaks.
type openAIChat struct{}

type chatBody struct {
	Model    string            `json:"model"`
	Messages []json.RawMessage `json:"messages"`
	Tools    []struct {
		Function *struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
	Stream bool `json:"stream"`
}

type chatMessage struct {
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	ToolCalls []struct {
		Function struct {
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

func (openAIChat) name() string { return "openai" }

func (o openAIChat) parse(_ *http.Request, body []byte) (*Request, error) {
	var b chatBody
	if err := decodeBody(body, &b); err != nil {
		return nil, err
	}
	req := &Request{API: o.name(), Model: b.Model, Messages: b.Messages, Stream: b.Stream, Raw: body}
	for _, t := range b.Tools {
		if t.Function != nil && t.Function.Name != "" {
			req.ToolNames = append(req.ToolNames, t.Function.Name)
		}
	}
	req.System, req.MessageText, req.ToolResults = chatFlatten(b.Messages)
	return req.normalise(), nil
}

// chatFlatten returns the system text, all the other messages' text, and the
// tool results' text alone.
func chatFlatten(messages []json.RawMessage) (string, string, []string) {
	var system, texts, results []string
	for _, raw := range messages {
		var m chatMessage
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		text := textOf(m.Content, "text")
		if m.Role == "system" || m.Role == "developer" {
			system = append(system, text)
			continue
		}
		texts = append(texts, text)
		if m.Role == "tool" {
			results = append(results, text)
		}
		for _, call := range m.ToolCalls {
			texts = append(texts, call.Function.Arguments)
		}
	}
	return strings.Join(system, "\n"), joinNonEmpty(texts), results
}

// chatUsage counts cached tokens inside prompt_tokens, as OpenAI does;
// clients subtract them again, so the scenario's input_tokens round-trips as
// the client's input. The API has no cache-write counter, so
// cache_creation_input_tokens is dropped.
func chatUsage(u *Usage) map[string]any {
	prompt := u.InputTokens + u.CacheReadInputTokens
	return map[string]any{
		"prompt_tokens":         prompt,
		"completion_tokens":     u.OutputTokens,
		"total_tokens":          prompt + u.OutputTokens,
		"prompt_tokens_details": map[string]int{"cached_tokens": u.CacheReadInputTokens},
	}
}

func (openAIChat) encode(turn *Turn, req *Request, responseID string) [][]byte {
	var content any
	if turn.Text != "" {
		content = turn.Text
	}
	message := map[string]any{"role": "assistant", "content": content}
	calls := make([]map[string]any, 0, len(turn.ToolCalls))
	for n, call := range turn.ToolCalls {
		calls = append(calls, map[string]any{
			"id":       fmt.Sprintf("%s_%d", responseID, n),
			"type":     "function",
			"function": map[string]any{"name": call.Name, "arguments": call.inputJSON()},
		})
	}
	finish := "stop"
	if len(calls) > 0 {
		message["tool_calls"] = calls
		finish = "tool_calls"
	}
	envelope := func(object string) map[string]any {
		return map[string]any{"id": responseID, "model": req.Model, "created": 0, "object": object}
	}
	if !req.Stream {
		body := envelope("chat.completion")
		body["choices"] = []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}
		body["usage"] = chatUsage(&turn.Usage)
		return [][]byte{mustJSON(body)}
	}
	chunk := func(delta map[string]any, finishReason any) []byte {
		body := envelope("chat.completion.chunk")
		body["choices"] = []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finishReason}}
		return sseData(body)
	}
	chunks := [][]byte{chunk(map[string]any{"role": "assistant"}, nil)}
	if turn.Text != "" {
		chunks = append(chunks, chunk(map[string]any{"content": turn.Text}, nil))
	}
	for index, call := range calls {
		indexed := map[string]any{"index": index}
		for k, v := range call {
			indexed[k] = v
		}
		chunks = append(chunks, chunk(map[string]any{"tool_calls": []any{indexed}}, nil))
	}
	// The usage-only final chunk of stream_options.include_usage, which a
	// client that did not ask for it ignores.
	usage := envelope("chat.completion.chunk")
	usage["choices"] = []any{}
	usage["usage"] = chatUsage(&turn.Usage)
	return append(chunks, chunk(map[string]any{}, finish), sseData(usage), []byte("data: [DONE]\n\n"))
}

func (openAIChat) encodeError(_ int, message string) []byte {
	return mustJSON(map[string]any{"error": map[string]any{"type": "invalid_request_error", "message": message}})
}

func (openAIChat) summarise(req *Request) string { return summarise(req, "message") }
