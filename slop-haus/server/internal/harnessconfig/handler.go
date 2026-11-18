package harnessconfig

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

// Handler is a thin HTTP wrapper over Service, the same split as
// settings.Handler.
type Handler struct {
	svc    *Service
	logger *slog.Logger
}

func NewHandler(svc *Service, logHandler slog.Handler) *Handler {
	return &Handler{svc: svc, logger: slog.New(logHandler)}
}

var errTable = httperr.Table{
	Rules: []httperr.Rule{
		{Err: authn.ErrNotPerson, Kind: httperr.Forbidden, Message: "only a signed-in person can change harness config"},
		{Err: ErrUnknownSetting, Kind: httperr.Invalid},
		{Err: ErrInvalidValue, Kind: httperr.Invalid},
		{Err: ErrNotConfigOwner, Kind: httperr.Forbidden, Message: "only the conductor's owner may change its harness config"},
		{Err: ErrAgentNotFound, Kind: httperr.NotFound, Message: "agent not found"},
	},
	Unexpected: "unexpected harness config error",
}

func (h *Handler) mapError(err error) error {
	return httperr.Map(h.logger, err, &errTable)
}

// -- Wire types --

type DefinitionBody struct {
	Harness string `json:"harness"`
	Name    string `json:"name"`
	Kind    string `json:"kind" enum:"model,provider,enum"`
	// Options are an enum's values; empty for any other kind.
	Options []string `json:"options"`
	// Pattern is what a model or provider value must match; empty for an
	// enum.
	Pattern string `json:"pattern"`
	Label   string `json:"label"`
	Help    string `json:"help"`
	// RehearsalB is a placeholder field for a workflow rehearsal.
	RehearsalB string `json:"rehearsal_b,omitempty"`
}

// ScopeBody is one saved scope. AgentID is null for the tenant's defaults.
type ScopeBody struct {
	AgentID   *string `json:"agent_id"`
	Settings  Values  `json:"settings"`
	Revision  int64   `json:"revision"`
	UpdatedBy *string `json:"updated_by" doc:"null when the person's account was deleted"`
	// UpdatedByDeleted is true when the person who last saved it has had
	// their account deleted: updated_by is then null.
	UpdatedByDeleted bool      `json:"updated_by_deleted,omitempty"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func scopeToBody(s *Scope) *ScopeBody {
	if s == nil {
		return nil
	}
	body := &ScopeBody{
		Settings:         s.Values.Known(),
		Revision:         s.Revision,
		UpdatedByDeleted: s.UpdatedByDeleted,
		UpdatedAt:        s.UpdatedAt,
	}
	if s.AgentID != nil {
		id := s.AgentID.String()
		body.AgentID = &id
	}
	if s.UpdatedBy != nil {
		id := s.UpdatedBy.String()
		body.UpdatedBy = &id
	}
	return body
}

// -- Read the tenant's config --

type GetHarnessConfigRequest struct{}

type GetHarnessConfigResponse struct {
	Body struct {
		Definitions []DefinitionBody `json:"definitions"`
		// Defaults is null until they are first saved.
		Defaults  *ScopeBody  `json:"defaults"`
		Overrides []ScopeBody `json:"overrides"`
	}
}

func (h *Handler) GetHarnessConfig(ctx context.Context, _ *GetHarnessConfigRequest) (*GetHarnessConfigResponse, error) {
	t, err := h.svc.Load(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &GetHarnessConfigResponse{}
	for _, d := range Definitions() {
		options := d.Options
		if options == nil {
			options = []string{}
		}
		resp.Body.Definitions = append(resp.Body.Definitions, DefinitionBody{
			Harness: d.Harness, Name: d.Name, Kind: string(d.Kind), Options: options, Pattern: d.Pattern(), Label: d.Label, Help: d.Help,
		})
	}
	resp.Body.Defaults = scopeToBody(t.Defaults)
	resp.Body.Overrides = []ScopeBody{}
	for _, o := range t.Overrides {
		resp.Body.Overrides = append(resp.Body.Overrides, *scopeToBody(o))
	}
	return resp, nil
}

// -- Save a scope --

// SaveHarnessConfigBody replaces the scope's settings: a setting left out
// is unset, which for an override means the agent takes the default.
type SaveHarnessConfigBody struct {
	Settings Values `json:"settings"`
}

type SaveDefaultsRequest struct {
	Body SaveHarnessConfigBody
}

type ScopeResponse struct {
	Body *ScopeBody
}

func (h *Handler) SaveDefaults(ctx context.Context, input *SaveDefaultsRequest) (*ScopeResponse, error) {
	return h.save(ctx, nil, input.Body.Settings)
}

type SaveOverrideRequest struct {
	ID   string `path:"id"`
	Body SaveHarnessConfigBody
}

func (h *Handler) SaveOverride(ctx context.Context, input *SaveOverrideRequest) (*ScopeResponse, error) {
	id, err := uuid.Parse(input.ID)
	if err != nil {
		return nil, huma.Error422UnprocessableEntity("agent id must be a valid uuid")
	}
	return h.save(ctx, &id, input.Body.Settings)
}

func (h *Handler) save(ctx context.Context, agentID *uuid.UUID, values Values) (*ScopeResponse, error) {
	saved, err := h.svc.Save(ctx, agentID, values, authn.PersonFromContext(ctx))
	if err != nil {
		return nil, h.mapError(err)
	}
	return &ScopeResponse{Body: scopeToBody(saved)}, nil
}

type RemoveOverrideRequest struct {
	ID string `path:"id"`
}

type RemoveOverrideResponse struct{}

func (h *Handler) RemoveOverride(ctx context.Context, input *RemoveOverrideRequest) (*RemoveOverrideResponse, error) {
	id, err := uuid.Parse(input.ID)
	if err != nil {
		return nil, huma.Error422UnprocessableEntity("agent id must be a valid uuid")
	}
	if err := h.svc.RemoveOverride(ctx, id, authn.PersonFromContext(ctx)); err != nil {
		return nil, h.mapError(err)
	}
	return &RemoveOverrideResponse{}, nil
}

// -- The calling agent's own config --

type GetWorkspaceHarnessConfigRequest struct{}

// WorkspaceHarnessConfigBody is what run_remote_control.sh saves as
// .slop-haus/harness-config.json and harness-config.py maps.
type WorkspaceHarnessConfigBody struct {
	Revision int64  `json:"revision"`
	Settings Values `json:"settings"`
	// Harness is the harness the agent's variant runs; omitted for an agent
	// in none.
	Harness string `json:"harness,omitempty"`
}

// GetWorkspaceHarnessConfigResponse is 204 with no body while the tenant
// has never saved any config, so a workspace without python3 -- which it
// needs only to map a config -- runs exactly as it did before there was
// any.
type GetWorkspaceHarnessConfigResponse struct {
	Status int
	Body   *WorkspaceHarnessConfigBody
}

// GetWorkspaceHarnessConfig answers for the agent the token belongs to and
// no other: there is no id to ask about, so no agent can read another's.
func (h *Handler) GetWorkspaceHarnessConfig(ctx context.Context, _ *GetWorkspaceHarnessConfigRequest) (*GetWorkspaceHarnessConfigResponse, error) {
	who, err := authn.FromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	eff, err := h.svc.Effective(ctx, who.AgentID())
	if err != nil {
		return nil, h.mapError(err)
	}
	if eff.Revision == 0 {
		return &GetWorkspaceHarnessConfigResponse{Status: http.StatusNoContent}, nil
	}
	return &GetWorkspaceHarnessConfigResponse{
		Status: http.StatusOK,
		Body:   &WorkspaceHarnessConfigBody{Revision: eff.Revision, Settings: eff.Values, Harness: eff.Harness},
	}, nil
}
