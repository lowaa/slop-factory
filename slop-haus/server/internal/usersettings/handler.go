package usersettings

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

type Handler struct {
	svc    *Service
	logger *slog.Logger
}

func NewHandler(svc *Service, logHandler slog.Handler) *Handler {
	return &Handler{svc: svc, logger: slog.New(logHandler)}
}

// UserSettingBody carries one value field per type rather than a polymorphic
// "value": a JSON field that is sometimes a string and sometimes a bool has
// no useful OpenAPI schema. The field `type` names is the live one.
type UserSettingBody struct {
	Name      string     `json:"name"`
	Type      string     `json:"type" enum:"str,int,bool,float"`
	StrVal    *string    `json:"str_val"`
	IntVal    *int64     `json:"int_val"`
	BoolVal   *bool      `json:"bool_val"`
	FloatVal  *float64   `json:"float_val"`
	IsDefault bool       `json:"is_default"`
	UpdatedAt *time.Time `json:"updated_at"`
}

func toBody(s Setting) UserSettingBody {
	b := UserSettingBody{Name: s.Name, Type: string(s.Value.Type), IsDefault: s.UpdatedAt == nil, UpdatedAt: s.UpdatedAt}
	b.StrVal, b.IntVal, b.BoolVal, b.FloatVal = valueFields(s.Value)
	return b
}

// valueFields splits a Value into the wire's one-field-per-type shape; only
// the field its Type names is set.
func valueFields(v Value) (str *string, i *int64, b *bool, f *float64) {
	switch v.Type {
	case TypeStr:
		str = &v.Str
	case TypeInt:
		i = &v.Int
	case TypeBool:
		b = &v.Bool
	case TypeFloat:
		f = &v.Float
	}
	return
}

// SetUserSettingBody states the type as well as the value, so a client that has drifted
// from the registry gets a 422 naming both rather than a coerced write.
type SetUserSettingBody struct {
	Type     string   `json:"type" enum:"str,int,bool,float"`
	StrVal   *string  `json:"str_val,omitempty"`
	IntVal   *int64   `json:"int_val,omitempty"`
	BoolVal  *bool    `json:"bool_val,omitempty"`
	FloatVal *float64 `json:"float_val,omitempty"`
}

func (b SetUserSettingBody) value() (Value, error) {
	missing := func() (Value, error) {
		return Value{}, fmt.Errorf("%w: type is %s but its value field is absent", ErrTypeMismatch, b.Type)
	}
	switch Type(b.Type) {
	case TypeStr:
		if b.StrVal == nil {
			return missing()
		}
		return StrValue(*b.StrVal), nil
	case TypeInt:
		if b.IntVal == nil {
			return missing()
		}
		return IntValue(*b.IntVal), nil
	case TypeBool:
		if b.BoolVal == nil {
			return missing()
		}
		return BoolValue(*b.BoolVal), nil
	case TypeFloat:
		if b.FloatVal == nil {
			return missing()
		}
		return FloatValue(*b.FloatVal), nil
	default:
		return Value{}, fmt.Errorf("%w: %q", ErrTypeMismatch, b.Type)
	}
}

var errTable = httperr.Table{
	Rules: []httperr.Rule{
		{Err: ErrNotMember, Kind: httperr.Forbidden, Message: "not a member of this tenant"},
		{Err: ErrUnknownSetting, Kind: httperr.NotFound},
		{Err: ErrTypeMismatch, Kind: httperr.Invalid},
	},
	Unexpected: "unexpected user settings error",
}

func (h *Handler) mapError(err error) error { return mapError(h.logger, err) }

func mapError(logger *slog.Logger, err error) error {
	return httperr.Map(logger, err, &errTable)
}

type ListRequest struct{}

type ListResponse struct {
	Body struct {
		Settings []UserSettingBody `json:"settings"`
		// RehearsalA is a throwaway field for the shared-paths rehearsal.
		RehearsalA string `json:"rehearsal_a,omitempty"`
	}
}

func (h *Handler) List(ctx context.Context, _ *ListRequest) (*ListResponse, error) {
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

type SetRequest struct {
	Name string `path:"name"`
	Body SetUserSettingBody
}

type SetResponse struct {
	Body UserSettingBody
}

func (h *Handler) Set(ctx context.Context, in *SetRequest) (*SetResponse, error) {
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
