package usersettings

import (
	"context"
	"log/slog"
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// TenantHandler serves the signed-in user's own settings within the tenant
// they are signed into, in the shapes /api/me/settings uses.
type TenantHandler struct {
	svc    *TenantService
	logger *slog.Logger
}

func NewTenantHandler(svc *TenantService, logHandler slog.Handler) *TenantHandler {
	return &TenantHandler{svc: svc, logger: slog.New(logHandler)}
}

func (h *TenantHandler) mapError(err error) error { return mapError(h.logger, err) }

func (h *TenantHandler) List(ctx context.Context, _ *ListRequest) (*ListResponse, error) {
	user, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	found, err := h.svc.List(ctx, user.ID)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &ListResponse{}
	resp.Body.Settings = make([]UserSettingBody, 0, len(found))
	for _, s := range found {
		resp.Body.Settings = append(resp.Body.Settings, toBody(s))
	}
	return resp, nil
}

type GetRequest struct {
	Name string `path:"name"`
}

func (h *TenantHandler) Get(ctx context.Context, in *GetRequest) (*SetResponse, error) {
	user, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	s, err := h.svc.Get(ctx, user.ID, in.Name)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &SetResponse{Body: toBody(*s)}, nil
}

func (h *TenantHandler) Set(ctx context.Context, in *SetRequest) (*SetResponse, error) {
	user, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	v, err := in.Body.value()
	if err != nil {
		return nil, h.mapError(err)
	}
	s, err := h.svc.Set(ctx, user.ID, in.Name, v)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &SetResponse{Body: toBody(*s)}, nil
}

// SettingChangeBody is one change in a setting's history, valued like
// UserSettingBody.
type SettingChangeBody struct {
	ID       int64     `json:"id"`
	Type     string    `json:"type" enum:"str,int,bool,float"`
	StrVal   *string   `json:"str_val"`
	IntVal   *int64    `json:"int_val"`
	BoolVal  *bool     `json:"bool_val"`
	FloatVal *float64  `json:"float_val"`
	SetAt    time.Time `json:"set_at"`
	// EarlierMembership is true for a change made before the user's current
	// membership of the tenant began.
	EarlierMembership bool `json:"earlier_membership"`
}

type SettingHistoryRequest struct {
	Name   string `path:"name"`
	Limit  int    `query:"limit" default:"50" minimum:"1" maximum:"200"`
	Before int64  `query:"before" minimum:"0" doc:"a previous response's next_before; 0 for the newest"`
}

type SettingHistoryResponse struct {
	Body struct {
		Changes []SettingChangeBody `json:"changes"`
		// NextBefore is set when older changes follow; pass it as before.
		NextBefore *int64 `json:"next_before,omitempty"`
	}
}

func (h *TenantHandler) History(ctx context.Context, in *SettingHistoryRequest) (*SettingHistoryResponse, error) {
	user, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	changes, next, err := h.svc.History(ctx, user.ID, in.Name, in.Before, in.Limit)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &SettingHistoryResponse{}
	resp.Body.Changes = make([]SettingChangeBody, 0, len(changes))
	for _, c := range changes {
		b := SettingChangeBody{ID: c.ID, Type: string(c.Value.Type), SetAt: c.CreatedAt, EarlierMembership: c.EarlierMembership}
		b.StrVal, b.IntVal, b.BoolVal, b.FloatVal = valueFields(c.Value)
		resp.Body.Changes = append(resp.Body.Changes, b)
	}
	if next != 0 {
		resp.Body.NextBefore = &next
	}
	return resp, nil
}
