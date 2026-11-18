package intake

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// nativeAdapter reads slop-haus's own payload:
//
//	{"key": "...", "event": "open|update|resolve", "title": "...", "body": "...",
//	 "status": "open|preparing", "priority": "low|normal|high|urgent",
//	 "link": "https://...", "context": {...}}
//
// Parsed strictly: an unknown field -- roles, paths, assignment, anything a
// sender might hope to set -- rejects the delivery rather than being quietly
// dropped, so a sender that believes it set one is told it did not.
type nativeAdapter struct{}

type nativePayload struct {
	Key      string          `json:"key"`
	Event    string          `json:"event"`
	Title    string          `json:"title"`
	Body     string          `json:"body"`
	Status   string          `json:"status"`
	Priority string          `json:"priority"`
	Link     string          `json:"link"`
	Context  json.RawMessage `json:"context"`
}

func (nativeAdapter) Parse(body []byte) (*Event, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var p nativePayload
	if err := dec.Decode(&p); err != nil {
		// The decoder's message names a field or a position, never the
		// value, so it is safe to hand back.
		return nil, fmt.Errorf("not a native intake event: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("not a native intake event: more than one JSON value")
	}
	return &Event{
		Key: p.Key, Kind: EventKind(p.Event), Title: p.Title, Body: p.Body, Link: p.Link,
		Status: ticket.Status(p.Status), Priority: model.Priority(p.Priority), Context: p.Context,
	}, nil
}
