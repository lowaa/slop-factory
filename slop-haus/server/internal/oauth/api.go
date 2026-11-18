package oauth

import (
	"context"
	"errors"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/go-auth/clientip"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

// Handler is the board's side of authorization: the consent page and the
// device-code page ask it what is pending and tell it what the user
// decided, and the agents screen lists and revokes grants. All of it is a
// signed-in user's, acting within their tenant.
type Handler struct {
	svc    *Service
	logger *slog.Logger
}

func NewHandler(svc *Service, logHandler slog.Handler) *Handler {
	return &Handler{svc: svc, logger: slog.New(logHandler)}
}

// ErrAgentUnavailable is what an Agents implementation returns when the
// chosen agent does not exist in the tenant or is disabled.
var ErrAgentUnavailable = agent.ErrAgentUnavailable

// ErrAgentNameTaken is what an Agents implementation returns when a new
// agent's name is already used in the tenant.
var ErrAgentNameTaken = agent.ErrAgentNameTaken

// ErrAgentKindInvalid is what an Agents implementation returns when a new
// agent is asked for in a kind consent cannot create.
var ErrAgentKindInvalid = agent.ErrAgentKindInvalid

// ErrSlotCapInvalid is what an Agents implementation returns when a new
// supervisor's slot cap is outside what the server allows.
var ErrSlotCapInvalid = agent.ErrSlotCapInvalid

// ErrReplaceUnavailable is what an Agents implementation returns when the
// agent to replace is unknown, archived, already replaced, or not the user's
// to replace.
var ErrReplaceUnavailable = agent.ErrReplaceUnavailable

var errTable = httperr.Table{
	Rules: []httperr.Rule{
		{Err: ErrNotFound, Kind: httperr.NotFound, Message: "no such pending authorization; it may have expired or been decided already"},
		{Err: ErrAgentUnavailable, Kind: httperr.Invalid},
		{Err: ErrAgentKindInvalid, Kind: httperr.Invalid},
		{Err: ErrReplaceUnavailable, Kind: httperr.Invalid},
		{Err: ErrSlotCapInvalid, Kind: httperr.Invalid},
		{Err: ErrAgentNameTaken, Kind: httperr.Conflict},
		{Err: ErrTooManyLookups, Kind: httperr.TooMany},
	},
	Custom: func(err error) error {
		var oe *Error
		if errors.As(err, &oe) {
			return huma.NewError(oe.Status, oe.Description)
		}
		return nil
	},
	Unexpected: "unexpected oauth error",
}

func (h *Handler) mapError(err error) error {
	return httperr.Map(h.logger, err, &errTable)
}

func (h *Handler) user(ctx context.Context) (*authn.User, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	return who, nil
}

type AuthRequestIDRequest struct {
	ID uuid.UUID `path:"id"`
}

type PendingAuthResponse struct {
	Body struct {
		ClientName string `json:"client_name"`
		// RedirectHost is shown so the user can see the approval goes to
		// a program on their own machine (always loopback; see
		// validRedirectURI).
		RedirectHost string `json:"redirect_host"`
	}
}

func (h *Handler) GetAuthRequest(ctx context.Context, in *AuthRequestIDRequest) (*PendingAuthResponse, error) {
	p, err := h.svc.PendingAuthRequest(ctx, in.ID)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &PendingAuthResponse{}
	resp.Body.ClientName = p.ClientName
	resp.Body.RedirectHost = p.RedirectHost
	return resp, nil
}

// DecisionBody is the consent form: approve with an existing agent's id or
// a new agent's name, or refuse.
type DecisionBody struct {
	Approve   bool       `json:"approve"`
	AgentID   *uuid.UUID `json:"agent_id,omitempty"`
	AgentName string     `json:"agent_name,omitempty" maxLength:"100"`
	// AgentKind is the kind of a new agent, worker when omitted. It goes with
	// agent_name: an existing agent has its kind already.
	AgentKind string `json:"agent_kind,omitempty" enum:"worker,console,supervisor"`
	// ReplacesAgentID also goes with agent_name: the new agent takes over
	// from this one, which is disabled and archived.
	ReplacesAgentID *uuid.UUID `json:"replaces_agent_id,omitempty"`
	// SlotCap is how many workers a new supervisor may have at once. It goes
	// with agent_kind supervisor, which needs it, and with nothing else.
	SlotCap *int `json:"slot_cap,omitempty" minimum:"1" doc:"how many workers a new supervisor may have at once; the server refuses one above its own limit"`
}

func (b *DecisionBody) decision() (*Decision, error) {
	if b.Approve && (b.AgentID == nil) == (b.AgentName == "") {
		return nil, huma.Error422UnprocessableEntity("approving needs exactly one of agent_id or agent_name")
	}
	if b.Approve && b.AgentID != nil && b.AgentKind != "" {
		return nil, huma.Error422UnprocessableEntity("agent_kind applies to a new agent; an existing agent keeps its kind")
	}
	if b.Approve && b.ReplacesAgentID != nil && b.AgentID != nil {
		return nil, huma.Error422UnprocessableEntity("replaces_agent_id applies to a new agent, not an existing one")
	}
	supervisor := b.AgentKind == supervisorKind
	if b.Approve && supervisor != (b.SlotCap != nil) {
		return nil, huma.Error422UnprocessableEntity("slot_cap goes with agent_kind supervisor, and a supervisor needs one")
	}
	if b.Approve && supervisor && b.ReplacesAgentID != nil {
		return nil, huma.Error422UnprocessableEntity("a supervisor cannot take over from another agent")
	}
	return &Decision{Approve: b.Approve, AgentID: b.AgentID, AgentName: b.AgentName, Kind: b.AgentKind, ReplacesAgentID: b.ReplacesAgentID,
		SlotCap: b.SlotCap}, nil
}

type DecideAuthRequest struct {
	ID   uuid.UUID `path:"id"`
	Body DecisionBody
}

type RedirectResponse struct {
	Body struct {
		// RedirectTo is where the page sends the browser next -- back to
		// the client, with a code or an error.
		RedirectTo string `json:"redirect_to"`
	}
}

func (h *Handler) DecideAuthRequest(ctx context.Context, in *DecideAuthRequest) (*RedirectResponse, error) {
	who, err := h.user(ctx)
	if err != nil {
		return nil, err
	}
	d, err := in.Body.decision()
	if err != nil {
		return nil, err
	}
	to, err := h.svc.DecideAuthRequest(ctx, who, in.ID, d)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &RedirectResponse{}
	resp.Body.RedirectTo = to
	return resp, nil
}

type UserCodeRequest struct {
	Body struct {
		UserCode string `json:"user_code" minLength:"1" maxLength:"20"`
	}
}

type PendingDeviceResponse struct {
	Body struct {
		ClientName string    `json:"client_name"`
		UserCode   string    `json:"user_code"`
		ExpiresAt  time.Time `json:"expires_at"`
		// Where and when it was started, so the person approving can tell
		// a sign-in they started from one someone sent them.
		StartedFrom string    `json:"started_from" doc:"the address that started it; empty when unknown"`
		StartedAt   time.Time `json:"started_at"`
		// FromThisNetwork is whether it was started from the approver's own
		// network (the same address, or IPv6 /64); the page warns when not.
		FromThisNetwork bool `json:"from_this_network"`
	}
}

func (h *Handler) LookupDevice(ctx context.Context, in *UserCodeRequest) (*PendingDeviceResponse, error) {
	who, err := h.user(ctx)
	if err != nil {
		return nil, err
	}
	d, err := h.svc.LookupDevice(ctx, who, in.Body.UserCode)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &PendingDeviceResponse{}
	resp.Body.ClientName = d.ClientName
	resp.Body.UserCode = d.UserCode
	resp.Body.ExpiresAt = d.ExpiresAt
	resp.Body.StartedAt = d.CreatedAt
	if d.StartedFrom.IsValid() {
		resp.Body.StartedFrom = d.StartedFrom.String()
		approver := clientip.FromContext(ctx)
		resp.Body.FromThisNetwork = approver.IsValid() && clientip.RateKey(approver) == clientip.RateKey(d.StartedFrom)
	}
	return resp, nil
}

type DecideDeviceRequest struct {
	Body struct {
		UserCode string `json:"user_code" minLength:"1" maxLength:"20"`
		DecisionBody
	}
}

type NoContentResponse struct{}

func (h *Handler) DecideDevice(ctx context.Context, in *DecideDeviceRequest) (*NoContentResponse, error) {
	who, err := h.user(ctx)
	if err != nil {
		return nil, err
	}
	d, err := in.Body.decision()
	if err != nil {
		return nil, err
	}
	if err := h.svc.DecideDevice(ctx, who, in.Body.UserCode, d); err != nil {
		return nil, h.mapError(err)
	}
	return &NoContentResponse{}, nil
}

type GrantBody struct {
	ID         uuid.UUID  `json:"id"`
	AgentID    uuid.UUID  `json:"agent_id"`
	AgentName  string     `json:"agent_name"`
	AgentKind  string     `json:"agent_kind" enum:"worker,console,conductor,supervisor,steward"`
	ClientID   string     `json:"client_id"`
	ClientName string     `json:"client_name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

type GrantsResponse struct {
	Body struct {
		Grants []GrantBody `json:"grants"`
	}
}

func (h *Handler) ListGrants(ctx context.Context, _ *struct{}) (*GrantsResponse, error) {
	who, err := h.user(ctx)
	if err != nil {
		return nil, err
	}
	grants, err := h.svc.Grants(ctx, who)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &GrantsResponse{}
	resp.Body.Grants = make([]GrantBody, 0, len(grants))
	for _, g := range grants {
		resp.Body.Grants = append(resp.Body.Grants, GrantBody{ID: g.ID, AgentID: g.AgentID, AgentName: g.AgentName, AgentKind: g.AgentKind, ClientID: g.ClientID,
			ClientName: g.ClientName, CreatedAt: g.CreatedAt, LastUsedAt: g.LastUsedAt})
	}
	return resp, nil
}

type GrantIDRequest struct {
	ID uuid.UUID `path:"id"`
}

func (h *Handler) RevokeGrant(ctx context.Context, in *GrantIDRequest) (*NoContentResponse, error) {
	who, err := h.user(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.RevokeGrant(ctx, who, in.ID); err != nil {
		return nil, h.mapError(err)
	}
	return &NoContentResponse{}, nil
}

// RegisterRoutes mounts the board's authorization API.
func RegisterRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getAuthRequest",
		Method:      http.MethodGet,
		Path:        "/api/oauth/requests/{id}",
		Summary:     "Describe a pending authorization for the consent page",
	}, h.GetAuthRequest)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "decideAuthRequest",
		Method:      http.MethodPost,
		Path:        "/api/oauth/requests/{id}/decision",
		Summary:     "Approve (as an agent) or refuse a pending authorization",
	}, h.DecideAuthRequest)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "lookupDeviceCode",
		Method:      http.MethodPost,
		Path:        "/api/oauth/device/lookup",
		Summary:     "Describe the device authorization a user code belongs to",
	}, h.LookupDevice)
	huma.Register(api, huma.Operation{
		Metadata:      authn.RequireUser(),
		OperationID:   "decideDeviceCode",
		Method:        http.MethodPost,
		Path:          "/api/oauth/device/decision",
		Summary:       "Approve (as an agent) or refuse a device authorization",
		DefaultStatus: http.StatusNoContent,
	}, h.DecideDevice)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "listGrants",
		Method:      http.MethodGet,
		Path:        "/api/oauth/grants",
		Summary:     "Clients authorized to act as this tenant's agents",
	}, h.ListGrants)
	huma.Register(api, huma.Operation{
		Metadata:      authn.RequireUser(),
		OperationID:   "revokeGrant",
		Method:        http.MethodDelete,
		Path:          "/api/oauth/grants/{id}",
		Summary:       "Revoke an authorization and every token under it",
		DefaultStatus: http.StatusNoContent,
	}, h.RevokeGrant)
}
