package ticket

import (
	"context"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// The blocking endpoints. Adding and removing a blocker are plain edits, so
// they sit with the rest of the ticket surface and need no agent
// credential -- a human curates what blocks what.

type graphResponse struct {
	Body *Graph
}

type blockersRequest struct {
	ID int64 `path:"id"`
}

type addBlockerRequest struct {
	ID   int64 `path:"id"`
	Body struct {
		// BlockerID is the ticket that must finish first.
		BlockerID int64 `json:"blocker_id"`
	}
}

type removeBlockerRequest struct {
	ID        int64 `path:"id"`
	BlockerID int64 `path:"blockerId"`
}

type blockerListResponse struct {
	Body struct {
		BlockedBy []int64         `json:"blocked_by"`
		Blockers  []TicketRefBody `json:"blockers"`
		// Status is the ticket's after the change: adding an unfinished
		// blocker to an open ticket makes it blocked, and removing the last
		// one opens it again.
		Status Status `json:"status"`
		// AllowedMoves and CanReady travel with Status, as on TicketBody:
		// the change can move the ticket between open and blocked, and
		// what a human may move it to differs between the two.
		AllowedMoves []Status `json:"allowed_moves"`
		CanReady     bool     `json:"can_ready"`
	}
}

// graphRequest takes the filter widget's fields. The filter decides which
// tickets are drawn; a ticket's blocked-ness is still worked out against
// the whole board (see PostgresStore.LoadGraph).
type graphRequest struct {
	FilterQuery
	// Limit caps the matching tickets drawn, newest kept.
	Limit int `query:"limit" minimum:"1" maximum:"2000"`
}

func (h *Handler) Graph(ctx context.Context, input *graphRequest) (*graphResponse, error) {
	filter, err := input.Filter()
	if err != nil {
		return nil, h.mapError(err)
	}
	limit := input.Limit
	if limit == 0 {
		limit = DefaultGraphLimit
	}
	graph, err := h.svc.Graph(ctx, &GraphQuery{Filter: *filter, Limit: limit})
	if err != nil {
		return nil, h.mapError(err)
	}
	return &graphResponse{Body: graph}, nil
}

// blockedRequest is the list query less a sort: the order is fixed.
type blockedRequest struct {
	FilterQuery
	Limit  int    `query:"limit" minimum:"1" maximum:"200"`
	Cursor string `query:"cursor"`
}

// BlockedTickets pages the tickets with an unfinished blocker, the one
// holding up the most first. The graph of any one of them is
// TicketBlockers, so the whole board's graph is never built for a page.
func (h *Handler) BlockedTickets(ctx context.Context, input *blockedRequest) (*ListTicketsResponse, error) {
	filter, err := input.Filter()
	if err != nil {
		return nil, h.mapError(err)
	}
	filter.Limit = input.Limit
	if filter.After, err = decodeCursor(input.Cursor); err != nil {
		return nil, h.mapError(err)
	}
	page, err := h.svc.BlockedPage(ctx, filter)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &ListTicketsResponse{}
	resp.Body.Tickets = ticketBodies(page.Tickets)
	if page.Next != nil {
		resp.Body.NextCursor = page.Next.Encode()
	}
	return resp, nil
}

func (h *Handler) TicketBlockers(ctx context.Context, input *blockersRequest) (*graphResponse, error) {
	graph, err := h.svc.Blockers(ctx, input.ID)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &graphResponse{Body: graph}, nil
}

// AddBlocker records that this ticket cannot start until another finishes.
//
// A cycle is refused rather than stored: a ticket that transitively blocks
// itself could never become claimable. The change is recorded as the
// signed-in user's comment.
func (h *Handler) AddBlocker(ctx context.Context, input *addBlockerRequest) (*blockerListResponse, error) {
	user, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("ticket route is missing its auth middleware")
	}
	change, err := h.svc.AddDependency(ctx, UserAuthor(user.ID), input.ID, input.Body.BlockerID)
	if err != nil {
		return nil, h.mapError(err)
	}
	return blockerList(change.Ticket), nil
}

func (h *Handler) RemoveBlocker(ctx context.Context, input *removeBlockerRequest) (*blockerListResponse, error) {
	user, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("ticket route is missing its auth middleware")
	}
	change, err := h.svc.RemoveDependency(ctx, UserAuthor(user.ID), input.ID, input.BlockerID)
	if err != nil {
		return nil, h.mapError(err)
	}
	return blockerList(change.Ticket), nil
}

type blockerCandidatesResponse struct {
	Body struct {
		Tickets []BlockerCandidate `json:"tickets"`
	}
}

func (h *Handler) BlockerCandidates(ctx context.Context, input *blockersRequest) (*blockerCandidatesResponse, error) {
	candidates, err := h.svc.BlockerCandidates(ctx, input.ID)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &blockerCandidatesResponse{}
	resp.Body.Tickets = candidates
	return resp, nil
}

func blockerList(found *Ticket) *blockerListResponse {
	resp := &blockerListResponse{}
	resp.Body.BlockedBy = found.DependsOn
	if resp.Body.BlockedBy == nil {
		resp.Body.BlockedBy = []int64{}
	}
	resp.Body.Blockers = ticketRefsToBody(found.Blockers)
	resp.Body.Status = found.Status
	resp.Body.AllowedMoves = AllowedMoves(found.Status)
	resp.Body.CanReady = CanReady(found.Status)
	return resp
}

// RegisterGraphRoutes mounts the blocking surface.
func RegisterGraphRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getBlockingGraph",
		Method:      http.MethodGet,
		Path:        "/api/tickets/graph",
		Summary:     "The blocking graph of the tickets a filter matches, with layers already worked out",
	}, h.Graph)

	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "listBlockedTickets",
		Method:      http.MethodGet,
		Path:        "/api/tickets/blocked",
		Summary:     "A page of the tickets with an unfinished blocker, the one that would free most first",
	}, h.BlockedTickets)

	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getTicketBlockers",
		Method:      http.MethodGet,
		Path:        "/api/tickets/{id}/blockers",
		Summary:     "What is holding one ticket up, and what is behind that",
	}, h.TicketBlockers)

	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getBlockerCandidates",
		Method:      http.MethodGet,
		Path:        "/api/tickets/{id}/blocker_candidates",
		Summary:     "Tickets this one can be blocked on without closing a cycle",
	}, h.BlockerCandidates)

	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "addTicketBlocker",
		Method:      http.MethodPost,
		Path:        "/api/tickets/{id}/blockers",
		Summary:     "Record that this ticket is blocked by another",
	}, h.AddBlocker)

	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "removeTicketBlocker",
		Method:      http.MethodDelete,
		Path:        "/api/tickets/{id}/blockers/{blockerId}",
		Summary:     "Remove a blocking relationship",
	}, h.RemoveBlocker)
}
