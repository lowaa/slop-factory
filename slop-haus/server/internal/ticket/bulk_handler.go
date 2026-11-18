package ticket

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// registerBulkRoutes mounts the board's bulk actions. A person's only: an
// agent works the one ticket it holds, and has no business moving others.
func registerBulkRoutes(api huma.API, h *Handler) {
	refused := bulkRefusedResponses(api)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "bulkSetTicketStatus",
		Method:      http.MethodPost,
		Path:        "/api/tickets/bulk/status",
		Summary:     "Move several tickets to a status: all of them, or none",
		Description: "The same rules as a single ticket's status edit. If any ticket is refused, none is moved, " +
			"and the error lists every refused ticket with its reason: 404 when any was not found, 409 otherwise. " +
			"422 for no ids, more than the server's cap, or an unknown status.",
		Responses: refused,
		Errors:    []int{http.StatusUnprocessableEntity},
	}, h.BulkSetStatus)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "bulkMarkTicketsReady",
		Method:      http.MethodPost,
		Path:        "/api/tickets/bulk/ready",
		Summary:     "Move several Preparing tickets into the queue: all of them, or none",
		Description: "Each lands open, or blocked on unfinished dependencies, as opening a single ticket does. " +
			"If any ticket is not preparing, none is moved, and the error lists every refused ticket " +
			"with its reason: 404 when any was not found, 409 otherwise. 422 for no ids or more than the server's cap.",
		Responses: refused,
		Errors:    []int{http.StatusUnprocessableEntity},
	}, h.BulkMarkReady)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "bulkSetTicketPriority",
		Method:      http.MethodPost,
		Path:        "/api/tickets/bulk/priority",
		Summary:     "Set several tickets' priority: all of them, or none",
		Description: "404, listing them, if any ticket was not found; none is changed. " +
			"422 for no ids, more than the server's cap, or an unknown priority.",
		Responses: refused,
		Errors:    []int{http.StatusUnprocessableEntity},
	}, h.BulkSetPriority)
}

// bulkRefusedResponses documents a refused batch's 404 and 409, whose body
// is BulkRefusedBody rather than huma's plain error model.
func bulkRefusedResponses(api huma.API) map[string]*huma.Response {
	schema := api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[BulkRefusedBody](), true, "BulkRefusedBody")
	responses := map[string]*huma.Response{}
	for _, status := range []int{http.StatusNotFound, http.StatusConflict} {
		responses[fmt.Sprint(status)] = &huma.Response{
			Description: http.StatusText(status) + ": the batch was refused, and nothing changed",
			Content:     map[string]*huma.MediaType{"application/problem+json": {Schema: schema}},
		}
	}
	return responses
}

type BulkStatusBody struct {
	IDs    []int64 `json:"ids" minItems:"1"`
	Status Status  `json:"status"`
}

type BulkStatusRequest struct {
	Body BulkStatusBody
}

type BulkReadyBody struct {
	IDs []int64 `json:"ids" minItems:"1"`
}

type BulkReadyRequest struct {
	Body BulkReadyBody
}

type BulkPriorityBody struct {
	IDs      []int64        `json:"ids" minItems:"1"`
	Priority model.Priority `json:"priority"`
}

type BulkPriorityRequest struct {
	Body BulkPriorityBody
}

// BulkTicketsResponse is every ticket the batch changed, as it left them,
// in the order the request named them (duplicates once).
type BulkTicketsResponse struct {
	Body struct {
		Tickets []TicketBody `json:"tickets"`
	}
}

// BulkRefusedCode is BulkRefusedBody's code.
const BulkRefusedCode = "bulk_refused"

// BulkRefusedBody is a refused batch: huma's error envelope plus every
// ticket refused and why, so the board can show them all at once rather
// than one per attempt.
type BulkRefusedBody struct {
	huma.ErrorModel
	// Code tells a client which refusal this is, so it can read Refusals.
	Code     string             `json:"code" enum:"bulk_refused"`
	Refusals []BulkRefusalEntry `json:"refusals"`
}

type BulkRefusalEntry struct {
	ID int64 `json:"id"`
	// Status is the ticket's status when it was refused; null when it was
	// not found.
	Status *Status `json:"status"`
	// Message names the ticket, its status and the rule, for showing as
	// is, e.g. "#12 in progress: only a preparing or open ticket can be
	// parked -- ...".
	Message string `json:"message"`
}

func bulkRefusedBody(e *BatchRefusedError) *BulkRefusedBody {
	status := http.StatusConflict
	for i := range e.Refusals {
		if errors.Is(e.Refusals[i].Err, ErrNotFound) {
			status = http.StatusNotFound
		}
	}
	body := &BulkRefusedBody{
		ErrorModel: huma.ErrorModel{
			Status: status,
			Title:  http.StatusText(status),
			Detail: fmt.Sprintf("%d ticket(s) refused; nothing was changed.", len(e.Refusals)),
		},
		Code:     BulkRefusedCode,
		Refusals: make([]BulkRefusalEntry, len(e.Refusals)),
	}
	for i := range e.Refusals {
		r := &e.Refusals[i]
		body.Refusals[i] = BulkRefusalEntry{ID: r.TicketID, Message: r.Reason()}
		if r.From != "" {
			body.Refusals[i].Status = &r.From
		}
	}
	return body
}

func bulkResponse(tickets []Ticket) *BulkTicketsResponse {
	resp := &BulkTicketsResponse{}
	resp.Body.Tickets = make([]TicketBody, len(tickets))
	for i, t := range tickets {
		resp.Body.Tickets[i] = ticketToBody(t)
	}
	return resp
}

func (h *Handler) BulkSetStatus(ctx context.Context, input *BulkStatusRequest) (*BulkTicketsResponse, error) {
	tickets, err := h.svc.MoveMany(byUser(ctx), input.Body.IDs, input.Body.Status)
	if err != nil {
		return nil, h.mapBulkError(err)
	}
	return bulkResponse(tickets), nil
}

func (h *Handler) BulkMarkReady(ctx context.Context, input *BulkReadyRequest) (*BulkTicketsResponse, error) {
	tickets, err := h.svc.MarkReadyMany(byUser(ctx), input.Body.IDs)
	if err != nil {
		return nil, h.mapBulkError(err)
	}
	return bulkResponse(tickets), nil
}

func (h *Handler) BulkSetPriority(ctx context.Context, input *BulkPriorityRequest) (*BulkTicketsResponse, error) {
	tickets, err := h.svc.SetPriorityMany(ctx, input.Body.IDs, input.Body.Priority)
	if err != nil {
		return nil, h.mapBulkError(err)
	}
	return bulkResponse(tickets), nil
}
