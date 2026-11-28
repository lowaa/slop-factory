package scriptedmodel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// openAIResponses is the OpenAI Responses API (POST /v1/responses), which
// Codex's custom provider speaks (wire_api = "responses"). It is streamed as
// Codex reads it: response.created, one response.output_item.done per item,
// then response.completed with the usage.
type openAIResponses struct{}

type responsesTool struct {
	Type  string          `json:"type"`
	Name  string          `json:"name"`
	Tools []responsesTool `json:"tools"`
}

type responsesBody struct {
	Model        string          `json:"model"`
	Instructions string          `json:"instructions"`
	Input        json.RawMessage `json:"input"`
	Tools        []responsesTool `json:"tools"`
	Stream       bool            `json:"stream"`
}

type responsesItem struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	Output    json.RawMessage `json:"output"`
	Arguments *string         `json:"arguments"`
	Input     *string         `json:"input"`
}

func (openAIResponses) name() string { return "responses" }

// responsesToolNamespaces maps each tool name to the namespace it is offered
// in ("" for none). Codex offers an MCP server's tools as the children of a
// namespace tool, and the model calls one by its bare name plus that namespace.
func responsesToolNamespaces(tools []responsesTool) ([]string, map[string]string) {
	var names []string
	namespaces := map[string]string{}
	add := func(name, namespace string) {
		if _, seen := namespaces[name]; !seen {
			names = append(names, name)
		}
		namespaces[name] = namespace
	}
	for _, tool := range tools {
		if tool.Type == "namespace" {
			for _, child := range tool.Tools {
				if child.Name != "" {
					add(child.Name, tool.Name)
				}
			}
		} else if tool.Name != "" {
			add(tool.Name, "")
		}
	}
	return names, namespaces
}

func (o openAIResponses) parse(_ *http.Request, body []byte) (*Request, error) {
	var b responsesBody
	if err := decodeBody(body, &b); err != nil {
		return nil, err
	}
	var items []json.RawMessage
	var prompt string
	if json.Unmarshal(b.Input, &prompt) == nil {
		items = []json.RawMessage{mustJSON(map[string]any{"type": "message", "role": "user", "content": prompt})}
	} else if len(b.Input) > 0 {
		if err := json.Unmarshal(b.Input, &items); err != nil {
			return nil, fmt.Errorf("input is neither a string nor a list of items: %w", err)
		}
	}
	req := &Request{API: o.name(), Model: b.Model, Messages: items, Stream: b.Stream, Raw: body}
	req.ToolNames, _ = responsesToolNamespaces(b.Tools)
	req.System, req.MessageText, req.ToolResults = responsesFlatten(b.Instructions, items)
	return req.normalise(), nil
}

// responsesFlatten returns the system text, all the conversation's text, and
// the tool results' text alone.
func responsesFlatten(instructions string, items []json.RawMessage) (string, string, []string) {
	var system, texts, results []string
	if instructions != "" {
		system = append(system, instructions)
	}
	for _, raw := range items {
		var item responsesItem
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		switch item.Type {
		case "", "message":
			text := textOf(item.Content, "")
			if item.Role == "system" || item.Role == "developer" {
				system = append(system, text)
			} else {
				texts = append(texts, text)
			}
		case "function_call_output", "custom_tool_call_output":
			text := responsesOutputText(item.Output)
			texts = append(texts, text)
			results = append(results, text)
		case "function_call", "custom_tool_call":
			if item.Arguments != nil {
				texts = append(texts, *item.Arguments)
			} else if item.Input != nil {
				texts = append(texts, *item.Input)
			}
		}
	}
	return strings.Join(system, "\n"), joinNonEmpty(texts), results
}

func responsesOutputText(output json.RawMessage) string {
	if text := textOf(output, ""); text != "" {
		return text
	}
	var s string
	if json.Unmarshal(output, &s) == nil {
		return s
	}
	return string(rawOrNull(output))
}

// responsesUsage counts cached tokens inside input_tokens, as in chat
// completions; there is no cache-write counter.
func responsesUsage(u *Usage) map[string]any {
	prompt := u.InputTokens + u.CacheReadInputTokens
	return map[string]any{
		"input_tokens":          prompt,
		"input_tokens_details":  map[string]int{"cached_tokens": u.CacheReadInputTokens},
		"output_tokens":         u.OutputTokens,
		"output_tokens_details": map[string]int{"reasoning_tokens": 0},
		"total_tokens":          prompt + u.OutputTokens,
	}
}

func (openAIResponses) encode(turn *Turn, req *Request, responseID string) [][]byte {
	var b responsesBody
	_ = json.Unmarshal(req.Raw, &b)
	_, namespaces := responsesToolNamespaces(b.Tools)
	items := []map[string]any{}
	if turn.Text != "" {
		items = append(items, map[string]any{
			"type":    "message",
			"id":      responseID + "_msg",
			"role":    "assistant",
			"status":  "completed",
			"content": []any{map[string]any{"type": "output_text", "text": turn.Text, "annotations": []any{}}},
		})
	}
	for n, call := range turn.ToolCalls {
		item := map[string]any{
			"type":      "function_call",
			"id":        fmt.Sprintf("%s_fc%d", responseID, n),
			"call_id":   fmt.Sprintf("%s_call%d", responseID, n),
			"name":      call.Name,
			"arguments": call.inputJSON(),
			"status":    "completed",
		}
		if ns := namespaces[call.Name]; ns != "" {
			item["namespace"] = ns
		}
		items = append(items, item)
	}
	envelope := func(status string, output any) map[string]any {
		return map[string]any{"id": responseID, "object": "response", "model": req.Model, "created_at": 0, "status": status, "output": output}
	}
	done := envelope("completed", items)
	done["usage"] = responsesUsage(&turn.Usage)
	if !req.Stream {
		return [][]byte{mustJSON(done)}
	}
	chunks := [][]byte{sseEvent("response.created", map[string]any{"type": "response.created", "response": envelope("in_progress", []any{})})}
	for index, item := range items {
		chunks = append(chunks, sseEvent("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": index, "item": item}))
	}
	// Codex reads the usage and the end of the turn from this event alone;
	// the items were sent above.
	done["output"] = []any{}
	return append(chunks, sseEvent("response.completed", map[string]any{"type": "response.completed", "response": done}))
}

func (openAIResponses) encodeError(_ int, message string) []byte {
	return mustJSON(map[string]any{"error": map[string]any{"type": "invalid_request_error", "message": message}})
}

func (openAIResponses) summarise(req *Request) string { return summarise(req, "item") }
