package scriptedmodel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// An adapter is one wire API. Adding another is a new type and its routes in
// Server.routes.
type adapter interface {
	name() string
	// parse reads a request; r gives the path values of the route.
	parse(r *http.Request, body []byte) (*Request, error)
	// encode is the response body for the turn as the chunks it is written
	// in, in whatever framing the request asked for.
	encode(turn *Turn, req *Request, responseID string) [][]byte
	encodeError(status int, message string) []byte
	summarise(req *Request) string
}

// The turn an auxiliary request gets: the harness's housekeeping only needs
// a well-formed answer.
var auxiliaryTurn = &Turn{Text: "ok"}

func mustJSON(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		// Every value marshalled here is built from JSON-safe types.
		panic(fmt.Sprintf("marshal %T: %v", v, err))
	}
	return data
}

func sseEvent(event string, v any) []byte {
	return []byte("event: " + event + "\ndata: " + string(mustJSON(v)) + "\n\n")
}

func sseData(v any) []byte {
	return []byte("data: " + string(mustJSON(v)) + "\n\n")
}

// A textPart is the {type, text} shape every API uses for a piece of text.
type textPart struct {
	Type string  `json:"type"`
	Text *string `json:"text"`
}

// textOf reads content that is either a string or a list of parts, joining
// the text of the parts of partType (any type, if empty).
func textOf(raw json.RawMessage, partType string) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []textPart
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	texts := make([]string, 0, len(parts))
	for _, p := range parts {
		if p.Text != nil && (partType == "" || p.Type == partType) {
			texts = append(texts, *p.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func joinNonEmpty(texts []string) string {
	kept := make([]string, 0, len(texts))
	for _, t := range texts {
		if t != "" {
			kept = append(kept, t)
		}
	}
	return strings.Join(kept, "\n")
}

// summarise is the one-line description of a request that goes into an
// unexpected-request error: enough to see which step of the conversation it was.
func summarise(req *Request, itemsName string) string {
	last := "null"
	if n := len(req.Messages); n > 0 {
		last = string(req.Messages[n-1])
	}
	if len(last) > 600 {
		last = last[:600]
	}
	return fmt.Sprintf("%s request: model=%q tools=%d %s=%d last_%s=%s",
		req.API, req.Model, len(req.ToolNames), itemsName+"s", len(req.Messages), itemsName, last)
}

func decodeBody(body []byte, v any) error {
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("bad JSON: %w", err)
	}
	return nil
}

func rawOrNull(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	return raw
}
