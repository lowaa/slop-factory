package ticket

import (
	"context"
	"time"
)

// ExternalRefBody says a ticket came from an intake source.
type ExternalRefBody struct {
	SourceID string `json:"source_id"`
	Key      string `json:"key" doc:"the sender's id for the issue"`
	Title    string `json:"title" doc:"what the sender called it: external text"`
}

// ExternalEventBody is one thing a source sent. Every text field is the
// sender's: shown as external content, never as slop-haus's own words.
type ExternalEventBody struct {
	ID         int64     `json:"id"`
	SourceID   string    `json:"source_id"`
	SourceName string    `json:"source_name"`
	Key        string    `json:"key"`
	Event      string    `json:"event" enum:"open,update,resolve"`
	Title      string    `json:"title"`
	Body       string    `json:"body"`
	Link       string    `json:"link"`
	Context    string    `json:"context" doc:"the sender's metadata, as compact JSON text"`
	At         time.Time `json:"at"`
}

type ListTicketExternalRequest struct {
	ID int64 `path:"id"`
}

type ListTicketExternalResponse struct {
	Body struct {
		Events []ExternalEventBody `json:"events"`
	}
}

// ListTicketExternal is what the board's detail page shows in its block of
// external content.
func (h *Handler) ListTicketExternal(ctx context.Context, input *ListTicketExternalRequest) (*ListTicketExternalResponse, error) {
	if _, err := h.svc.Get(ctx, input.ID); err != nil {
		return nil, h.mapError(err)
	}
	events, err := h.svc.ListExternal(ctx, input.ID)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &ListTicketExternalResponse{}
	resp.Body.Events = make([]ExternalEventBody, len(events))
	for i := range events {
		e := &events[i]
		resp.Body.Events[i] = ExternalEventBody{
			ID: e.ID, SourceID: e.SourceID.String(), SourceName: e.SourceName, Key: e.Key, Event: e.Event,
			Title: e.Title, Body: e.Body, Link: e.Link, Context: string(e.Context), At: e.At,
		}
	}
	return resp, nil
}
