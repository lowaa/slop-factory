package agentmsg

import (
	"context"
	"log/slog"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

// Handler is the board's side of messages to agents: a signed-in person
// sends and reads them. No handler here reads an agent from the request
// context -- see RegisterRoutes.
type Handler struct {
	svc    *Service
	logger *slog.Logger
}

func NewHandler(svc *Service, logHandler slog.Handler) *Handler {
	return &Handler{svc: svc, logger: slog.New(logHandler)}
}

var errTable = httperr.Table{
	Rules: []httperr.Rule{
		{Err: authn.ErrNotPerson, Kind: httperr.Forbidden, Message: "only a signed-in person can message an agent"},
		{Err: ErrAgentNotFound, Kind: httperr.NotFound, Message: "no such agent"},
		{Err: ErrAgentDisabled, Kind: httperr.Conflict, Message: "that agent is disabled"},
		{Err: ErrTooManyPending, Kind: httperr.Conflict, Message: "that agent has too many undelivered messages; wait for it to pick them up"},
		{Err: ErrEmptyBody, Kind: httperr.Invalid},
		{Err: ErrTooLong, Kind: httperr.Invalid},
	},
	Unexpected: "unexpected agent message error",
}

func (h *Handler) mapError(err error) error {
	return httperr.Map(h.logger, err, &errTable)
}

// AgentMessageBody is a message as the Agents screen shows it.
type AgentMessageBody struct {
	ID      int64  `json:"id"`
	AgentID string `json:"agent_id"`
	Kind    string `json:"kind" enum:"message,ticket_comment"`
	// State is pending until the agent's next call carries the message to
	// it, then delivered; dropped when the agent's hold on the ticket ended
	// first. There is no read state: the server can't know.
	State        string  `json:"state" enum:"pending,delivered,dropped"`
	AuthorUserID *string `json:"author_user_id" doc:"null when the person's account was deleted"`
	// AuthorUserDeleted is true when the author's account was deleted;
	// author_user_id is then null.
	AuthorUserDeleted bool `json:"author_user_id_deleted,omitempty"`
	// TicketID is set for a person's comment on a ticket the agent held.
	TicketID *int64 `json:"ticket_id"`
	// Body is the person's own text: show it as plain text.
	Body        string     `json:"body"`
	CreatedAt   time.Time  `json:"created_at"`
	DeliveredAt *time.Time `json:"delivered_at"`
	DroppedAt   *time.Time `json:"dropped_at"`
}

func toBody(m *Message) AgentMessageBody {
	state := "pending"
	if m.DeliveredAt != nil {
		state = "delivered"
	} else if m.DroppedAt != nil {
		state = "dropped"
	}
	body := AgentMessageBody{
		ID: m.ID, AgentID: m.AgentID.String(), Kind: string(m.Kind), State: state,
		AuthorUserDeleted: m.AuthorUserDeleted, TicketID: m.TicketID, Body: m.Body,
		CreatedAt: m.CreatedAt, DeliveredAt: m.DeliveredAt, DroppedAt: m.DroppedAt,
	}
	if m.AuthorUserID != nil {
		id := m.AuthorUserID.String()
		body.AuthorUserID = &id
	}
	return body
}

type SendAgentMessageInput struct {
	ID   string `path:"id" format:"uuid" doc:"the agent to message"`
	Body struct {
		Body string `json:"body" doc:"what to tell the agent; plain text"`
	}
}

type SendAgentMessageOutput struct {
	Body AgentMessageBody
}

func (h *Handler) Send(ctx context.Context, in *SendAgentMessageInput) (*SendAgentMessageOutput, error) {
	agentID, err := uuid.Parse(in.ID)
	if err != nil {
		return nil, huma.Error422UnprocessableEntity("id must be a uuid")
	}
	m, err := h.svc.Send(ctx, authn.PersonFromContext(ctx), agentID, in.Body.Body)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &SendAgentMessageOutput{Body: toBody(m)}, nil
}

type ListAgentMessagesInput struct {
	Limit int `query:"limit" minimum:"1" maximum:"500" default:"100"`
}

type ListAgentMessagesOutput struct {
	Body struct {
		Messages []AgentMessageBody `json:"messages" doc:"newest first"`
	}
}

func (h *Handler) List(ctx context.Context, in *ListAgentMessagesInput) (*ListAgentMessagesOutput, error) {
	found, err := h.svc.Recent(ctx, authn.PersonFromContext(ctx), in.Limit)
	if err != nil {
		return nil, h.mapError(err)
	}
	out := &ListAgentMessagesOutput{}
	out.Body.Messages = make([]AgentMessageBody, 0, len(found))
	for i := range found {
		out.Body.Messages = append(out.Body.Messages, toBody(&found[i]))
	}
	return out, nil
}
