package ticket

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// registerReviewRoutes mounts the PR review routes: a person's two answers
// to a ticket in review, and a person's note that they have looked over a
// change that merged without one. They are RequireUser with no agent route to
// them at all -- an approval an agent could give is no gate. An agent hands a
// ticket to review through the MCP ticket_finish tool only.
func registerReviewRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "approveTicket",
		Method:      http.MethodPost,
		Path:        "/api/tickets/{id}/approve",
		Summary:     "Approve the PR of a ticket in review, returning it to the queue to be merged",
	}, h.ApproveTicket)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "requestTicketChanges",
		Method:      http.MethodPost,
		Path:        "/api/tickets/{id}/request_changes",
		Summary:     "Ask for changes to the PR of a ticket in review, returning it to the queue",
	}, h.RequestTicketChanges)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "markTicketPostReviewed",
		Method:      http.MethodPost,
		Path:        "/api/tickets/{id}/post_review",
		Summary:     "Record that you have looked over a done ticket's merged change",
	}, h.MarkTicketPostReviewed)
}

type ApproveTicketRequest struct {
	ID      int64 `path:"id"`
	Attempt int   `query:"attempt" required:"false" minimum:"0" doc:"the attempt approved; 0 or absent names none, which is refused when the ticket has several live attempts"`
}

func (h *Handler) ApproveTicket(ctx context.Context, input *ApproveTicketRequest) (*TicketResponse, error) {
	user, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("ticket route is missing its auth middleware")
	}
	addressed, err := h.addressedAttempt(ctx, input.ID, input.Attempt)
	if err != nil {
		return nil, err
	}
	t, err := h.svc.ApproveAttempt(ctx, input.ID, addressed, user.ID)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &TicketResponse{Body: ticketToBody(*t)}, nil
}

type RequestTicketChangesBody struct {
	Comment string `json:"comment" doc:"what to change; posted as your comment on the ticket"`
}

type RequestTicketChangesRequest struct {
	ID      int64 `path:"id"`
	Attempt int   `query:"attempt" required:"false" minimum:"0" doc:"the attempt changes are asked of; 0 or absent names none, which is refused when the ticket has several live attempts"`
	Body    RequestTicketChangesBody
}

func (h *Handler) RequestTicketChanges(ctx context.Context, input *RequestTicketChangesRequest) (*TicketResponse, error) {
	user, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("ticket route is missing its auth middleware")
	}
	addressed, err := h.addressedAttempt(ctx, input.ID, input.Attempt)
	if err != nil {
		return nil, err
	}
	t, err := h.svc.RequestChangesAttempt(ctx, input.ID, addressed, user.ID, input.Body.Comment)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &TicketResponse{Body: ticketToBody(*t)}, nil
}

type MarkTicketPostReviewedBody struct {
	Comment string `json:"comment,omitempty" doc:"anything you found; posted as your comment on the ticket"`
}

// Body is optional: marking a change reviewed with nothing to say about it
// is the common case.
type MarkTicketPostReviewedRequest struct {
	ID   int64                       `path:"id"`
	Body *MarkTicketPostReviewedBody `required:"false"`
}

func (h *Handler) MarkTicketPostReviewed(ctx context.Context, input *MarkTicketPostReviewedRequest) (*TicketResponse, error) {
	user, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("ticket route is missing its auth middleware")
	}
	var comment string
	if input.Body != nil {
		comment = input.Body.Comment
	}
	t, err := h.svc.MarkPostReviewed(ctx, input.ID, user.ID, comment)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &TicketResponse{Body: ticketToBody(*t)}, nil
}
