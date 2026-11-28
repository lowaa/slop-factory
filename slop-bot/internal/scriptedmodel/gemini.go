package scriptedmodel

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// gemini is the Gemini API's generateContent and streamGenerateContent
// (POST /v1beta/models/{model}:<method>), plus its model list: what the
// Antigravity CLI (agy) speaks to a model gateway (AGY_LLM_GATEWAY_URL).
type gemini struct{}

type geminiFunctionCall struct {
	ID   string         `json:"id,omitempty"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

type geminiFunctionResponse struct {
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

type geminiPart struct {
	Text             *string                 `json:"text,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiFunctionDeclaration struct {
	Name string `json:"name"`
}

// The API takes both the proto JSON names and the snake_case field names.
type geminiTool struct {
	FunctionDeclarations      []geminiFunctionDeclaration `json:"functionDeclarations"`
	FunctionDeclarationsSnake []geminiFunctionDeclaration `json:"function_declarations"`
}

type geminiBody struct {
	Contents               []json.RawMessage `json:"contents"`
	SystemInstruction      *geminiContent    `json:"systemInstruction"`
	SystemInstructionSnake *geminiContent    `json:"system_instruction"`
	Tools                  []geminiTool      `json:"tools"`
}

const (
	geminiGenerate = "generateContent"
	geminiStream   = "streamGenerateContent"
)

var errGeminiMethod = errors.New("not a Gemini method this server speaks")

func (gemini) name() string { return "gemini" }

// geminiCall splits the {model}:{method} path segment.
func geminiCall(r *http.Request) (model string, stream bool, err error) {
	call := r.PathValue("call")
	model, method, ok := strings.Cut(call, ":")
	if !ok || model == "" {
		return "", false, fmt.Errorf("%w: %s", errGeminiMethod, call)
	}
	switch method {
	case geminiGenerate:
		return model, false, nil
	case geminiStream:
		// Without alt=sse the API streams one JSON array, which no harness
		// here asks for.
		if r.URL.Query().Get("alt") != "sse" {
			return "", false, fmt.Errorf("%w: %s without alt=sse", errGeminiMethod, method)
		}
		return model, true, nil
	default:
		return "", false, fmt.Errorf("%w: %s", errGeminiMethod, method)
	}
}

func (g gemini) parse(r *http.Request, body []byte) (*Request, error) {
	model, stream, err := geminiCall(r)
	if err != nil {
		return nil, err
	}
	var b geminiBody
	if err := decodeBody(body, &b); err != nil {
		return nil, err
	}
	req := &Request{API: g.name(), Model: model, Messages: b.Contents, Stream: stream, Raw: body}
	for _, tool := range b.Tools {
		for _, fn := range append(tool.FunctionDeclarations, tool.FunctionDeclarationsSnake...) {
			if fn.Name != "" {
				req.ToolNames = append(req.ToolNames, fn.Name)
			}
		}
	}
	system := b.SystemInstruction
	if system == nil {
		system = b.SystemInstructionSnake
	}
	if system != nil {
		req.System, _ = geminiFlatten([]geminiContent{*system})
	}
	var contents []geminiContent
	for _, raw := range b.Contents {
		var c geminiContent
		if json.Unmarshal(raw, &c) == nil {
			contents = append(contents, c)
		}
	}
	req.MessageText, req.ToolResults = geminiFlatten(contents)
	return req.normalise(), nil
}

// geminiFlatten returns all the text of the contents, function calls'
// arguments and responses included, and the function responses alone. The
// model's thoughts are left out: they are not what the harness showed it.
func geminiFlatten(contents []geminiContent) (string, []string) {
	var texts, results []string
	for _, c := range contents {
		for _, part := range c.Parts {
			switch {
			case part.Text != nil && !part.Thought:
				texts = append(texts, *part.Text)
			case part.FunctionCall != nil:
				texts = append(texts, string(mustJSON(part.FunctionCall.Args)))
			case part.FunctionResponse != nil:
				text := geminiResponseText(part.FunctionResponse.Response)
				texts = append(texts, text)
				results = append(results, text)
			}
		}
	}
	return joinNonEmpty(texts), results
}

// geminiResponseText is a function response's text. The API leaves the
// response object's shape to the caller; agy puts the tool's output in a
// string field (output), so a lone string field is taken as the text.
func geminiResponseText(raw json.RawMessage) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) == nil && len(fields) == 1 {
		for _, v := range fields {
			var s string
			if json.Unmarshal(v, &s) == nil {
				return s
			}
		}
	}
	return string(rawOrNull(raw))
}

// geminiUsage counts cached tokens inside promptTokenCount, as the API does
// (cachedContentTokenCount is the cached part of it). There is no
// cache-write counter, so cache_creation_input_tokens is dropped.
func geminiUsage(u *Usage) map[string]any {
	prompt := u.InputTokens + u.CacheReadInputTokens
	usage := map[string]any{
		"promptTokenCount":     prompt,
		"candidatesTokenCount": u.OutputTokens,
		"totalTokenCount":      prompt + u.OutputTokens,
	}
	if u.CacheReadInputTokens > 0 {
		usage["cachedContentTokenCount"] = u.CacheReadInputTokens
	}
	return usage
}

func geminiParts(turn *Turn, responseID string) []map[string]any {
	parts := []map[string]any{}
	if turn.Text != "" {
		parts = append(parts, map[string]any{"text": turn.Text})
	}
	for n, call := range turn.ToolCalls {
		parts = append(parts, map[string]any{"functionCall": map[string]any{
			"id": fmt.Sprintf("%s_%d", responseID, n), "name": call.Name, "args": call.inputOrEmpty(),
		}})
	}
	return parts
}

func (gemini) encode(turn *Turn, req *Request, responseID string) [][]byte {
	parts := geminiParts(turn, responseID)
	response := func(parts []map[string]any, last bool) map[string]any {
		candidate := map[string]any{"content": map[string]any{"role": "model", "parts": parts}, "index": 0}
		body := map[string]any{"candidates": []any{candidate}, "modelVersion": req.Model, "responseId": responseID}
		if last {
			// The API says STOP for a turn that ends in function calls too.
			candidate["finishReason"] = "STOP"
			body["usageMetadata"] = geminiUsage(&turn.Usage)
		}
		return body
	}
	if !req.Stream {
		return [][]byte{mustJSON(response(parts, true))}
	}
	// One part per chunk, as the API streams them, with the finish reason
	// and the usage on the last.
	chunks := make([][]byte, 0, len(parts))
	for i, part := range parts {
		chunks = append(chunks, sseData(response([]map[string]any{part}, i == len(parts)-1)))
	}
	return chunks
}

func geminiStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "INVALID_ARGUMENT"
	case http.StatusNotFound:
		return "NOT_FOUND"
	default:
		return "INTERNAL"
	}
}

func (gemini) encodeError(status int, message string) []byte {
	return mustJSON(map[string]any{"error": map[string]any{"code": status, "message": message, "status": geminiStatus(status)}})
}

func (gemini) summarise(req *Request) string { return summarise(req, "content") }

// geminiModelList is the model list the API serves at /v1beta/models. The
// /v1/models copy also carries OpenAI's {object, data} shape: agy asks for
// both, and which one it reads is not documented.
func geminiModelList(models []string, withOpenAI bool) []byte {
	list := make([]map[string]any, 0, len(models))
	data := make([]map[string]any, 0, len(models))
	for _, id := range models {
		list = append(list, map[string]any{
			"name":                       "models/" + id,
			"baseModelId":                id,
			"version":                    "scripted",
			"displayName":                id,
			"description":                "A scripted model for harness tests.",
			"inputTokenLimit":            1048576,
			"outputTokenLimit":           65536,
			"supportedGenerationMethods": []string{geminiGenerate, geminiStream, "countTokens"},
			"thinking":                   true,
		})
		data = append(data, map[string]any{"id": id, "object": "model", "created": 0, "owned_by": "scripted"})
	}
	body := map[string]any{"models": list}
	if withOpenAI {
		body["object"] = "list"
		body["data"] = data
	}
	return mustJSON(body)
}
