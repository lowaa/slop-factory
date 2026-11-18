package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

type RoleBody struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

func roleToBody(r Role) RoleBody {
	return RoleBody{ID: r.ID.String(), Name: r.Name, Description: r.Description, CreatedAt: r.CreatedAt}
}

// rolesToBody never returns nil, so an agent with no roles is `[]` on the
// wire rather than `null`.
func rolesToBody(roles []Role) []RoleBody {
	out := make([]RoleBody, len(roles))
	for i, r := range roles {
		out[i] = roleToBody(r)
	}
	return out
}

func parseRolePathID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.UUID{}, huma.Error422UnprocessableEntity("role id must be a valid uuid")
	}
	return id, nil
}

// -- List --

type ListAgentRolesRequest struct{}

type ListAgentRolesResponse struct {
	Body struct {
		Roles []RoleBody `json:"roles"`
	}
}

func (h *Handler) ListAgentRoles(ctx context.Context, _ *ListAgentRolesRequest) (*ListAgentRolesResponse, error) {
	roles, err := h.roles.List(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &ListAgentRolesResponse{}
	resp.Body.Roles = rolesToBody(roles)
	return resp, nil
}

// -- Create --

type CreateAgentRoleBody struct {
	Name        string `json:"name" maxLength:"64"`
	Description string `json:"description,omitempty" maxLength:"500"`
}

type CreateAgentRoleRequest struct {
	Body CreateAgentRoleBody
}

type AgentRoleResponse struct {
	Body RoleBody
}

func (h *Handler) CreateAgentRole(ctx context.Context, input *CreateAgentRoleRequest) (*AgentRoleResponse, error) {
	created, err := h.roles.Create(ctx, authn.PersonFromContext(ctx), input.Body.Name, input.Body.Description)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &AgentRoleResponse{Body: roleToBody(*created)}, nil
}

// -- Update --

type UpdateAgentRoleBody struct {
	Name        *string `json:"name,omitempty" maxLength:"64"`
	Description *string `json:"description,omitempty" maxLength:"500"`
}

type UpdateAgentRoleRequest struct {
	ID   string `path:"id"`
	Body UpdateAgentRoleBody
}

func (h *Handler) UpdateAgentRole(ctx context.Context, input *UpdateAgentRoleRequest) (*AgentRoleResponse, error) {
	id, err := parseRolePathID(input.ID)
	if err != nil {
		return nil, err
	}
	updated, err := h.roles.Update(ctx, authn.PersonFromContext(ctx), id, &RoleParams{Name: input.Body.Name, Description: input.Body.Description})
	if err != nil {
		return nil, h.mapError(err)
	}
	return &AgentRoleResponse{Body: roleToBody(*updated)}, nil
}

// -- Delete --

type DeleteAgentRoleRequest struct {
	ID string `path:"id"`
	// ClearTickets removes the role from the tickets that require it, and
	// deletes it. Without it, a role tickets require is refused with 409.
	ClearTickets bool `query:"clear_tickets"`
}

// RoleInUseBody is the 409 for a role tickets still require: huma's error
// envelope plus the tickets, so the board can ask whether to clear it from
// them without a second request.
type RoleInUseBody struct {
	huma.ErrorModel
	// Code tells a client which refusal this is, so it can read Tickets.
	Code    string            `json:"code" enum:"role_in_use"`
	Tickets []RoleInUseTicket `json:"tickets"`
}

// RoleInUseCode is RoleInUseBody's code.
const RoleInUseCode = "role_in_use"

type RoleInUseTicket struct {
	ID     int64         `json:"id"`
	Title  string        `json:"title"`
	Status ticket.Status `json:"status"`
}

func roleInUseBody(e *RoleInUseError) *RoleInUseBody {
	body := &RoleInUseBody{
		ErrorModel: huma.ErrorModel{
			Status: http.StatusConflict,
			Title:  http.StatusText(http.StatusConflict),
			Detail: fmt.Sprintf("%d ticket(s) require this role. Delete with clear_tickets=true to remove it from them "+
				"as well; any ticket it was the only role of becomes claimable by every agent.", len(e.Tickets)),
		},
		Code:    RoleInUseCode,
		Tickets: make([]RoleInUseTicket, len(e.Tickets)),
	}
	for i, t := range e.Tickets {
		body.Tickets[i] = RoleInUseTicket{ID: t.ID, Title: t.Title, Status: t.Status}
	}
	return body
}

type DeleteAgentRoleResponse struct{}

func (h *Handler) DeleteAgentRole(ctx context.Context, input *DeleteAgentRoleRequest) (*DeleteAgentRoleResponse, error) {
	id, err := parseRolePathID(input.ID)
	if err != nil {
		return nil, err
	}
	if err := h.roles.Delete(ctx, authn.PersonFromContext(ctx), id, input.ClearTickets); err != nil {
		var inUse *RoleInUseError
		if errors.As(err, &inUse) {
			return nil, roleInUseBody(inUse)
		}
		return nil, h.mapError(err)
	}
	return &DeleteAgentRoleResponse{}, nil
}

// -- Set an agent's roles --

type SetAgentRolesBody struct {
	RoleIDs []string `json:"role_ids" maxItems:"100"`
}

type SetAgentRolesRequest struct {
	ID   string `path:"id"`
	Body SetAgentRolesBody
}

func (h *Handler) SetAgentRoles(ctx context.Context, input *SetAgentRolesRequest) (*AgentResponse, error) {
	id, err := parseAgentPathID(input.ID)
	if err != nil {
		return nil, err
	}
	roleIDs := make([]uuid.UUID, len(input.Body.RoleIDs))
	for i, raw := range input.Body.RoleIDs {
		if roleIDs[i], err = uuid.Parse(raw); err != nil {
			return nil, huma.Error422UnprocessableEntity("role ids must be valid uuids")
		}
	}
	if _, err := h.svc.SetWorkerRoles(ctx, authn.PersonFromContext(ctx), h.roles, id, roleIDs); err != nil {
		if errors.Is(err, ErrRoleNotFound) {
			// The agent was found; one of the roles named in the body wasn't.
			return nil, huma.Error422UnprocessableEntity("no such role in this tenant")
		}
		return nil, h.mapError(err)
	}
	a, err := h.svc.GetWorker(ctx, id)
	if err != nil {
		return nil, h.mapError(err)
	}
	return h.agentResponse(ctx, a)
}
