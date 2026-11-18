package settings

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

// Handler is a thin HTTP wrapper over Service: every rule about which
// settings exist and what kind each holds lives there, so this file only
// translates between huma's request/response structs and the domain types,
// plus maps domain errors to HTTP status codes. Same split as
// ticket.Handler.
type Handler struct {
	svc    *Service
	logger *slog.Logger
}

func NewHandler(svc *Service, logHandler slog.Handler) *Handler {
	return &Handler{svc: svc, logger: slog.New(logHandler)}
}

// mapError translates a Service error into the huma error its HTTP status
// should carry. See ticket.Handler.mapError's doc comment for why the
// default branch never echoes err into the response.
var errTable = httperr.Table{
	Rules: []httperr.Rule{
		{Err: ErrUnknownSetting, Kind: httperr.NotFound},
		{Err: ErrTypeMismatch, Kind: httperr.Invalid},
		{Err: ErrInvalidSetting, Kind: httperr.Invalid},
		{Err: ErrNotOwner, Kind: httperr.Forbidden},
		{Err: ErrNoActor, Kind: httperr.Forbidden, Message: "only a signed-in person can change a setting"},
	},
	Unexpected: "unexpected settings error",
}

func (h *Handler) mapError(err error) error {
	return httperr.Map(h.logger, err, &errTable)
}

// -- Wire types --

// SettingBody mirrors the table's shape rather than collapsing to a single
// polymorphic "value" field: a JSON value that is sometimes a string and
// sometimes a number has no useful OpenAPI schema, and the generated
// client would type it as `unknown` and push the branching into every
// caller. The field named by `type` is the live one; the rest are null.
type SettingBody struct {
	Name string `json:"name"`
	Type string `json:"type" enum:"string,int,float,bool"`

	StringVal *string  `json:"string_val"`
	IntVal    *int64   `json:"int_val"`
	FloatVal  *float64 `json:"float_val"`
	// BoolVal is derived from int_val for type "bool". The storage detail
	// (a bool rides on int_val) is the schema's business, not the UI's --
	// a toggle should not have to know it is reading a 0 or a 1.
	BoolVal *bool `json:"bool_val"`

	// IsDefault reports that nothing has been written yet and this is the
	// declared default. The UI says so, which is the difference between
	// "nobody has touched this" and "somebody set it back".
	IsDefault bool       `json:"is_default"`
	UpdatedAt *time.Time `json:"updated_at"`
}

// The wire names the types as the API always has: the store's "str" is
// "string".
const (
	wireString = "string"
	wireInt    = "int"
	wireFloat  = "float"
	wireBool   = "bool"
)

func wireType(t Type) string {
	if t == TypeStr {
		return wireString
	}
	return string(t)
}

func settingToBody(s Setting) SettingBody {
	body := SettingBody{
		Name:      s.Name,
		Type:      wireType(s.Value.Type),
		IsDefault: s.IsDefault(),
	}
	if !s.IsDefault() {
		updated := s.UpdatedAt
		body.UpdatedAt = &updated
	}

	switch s.Value.Type {
	case TypeStr:
		value := s.Value.Str
		body.StringVal = &value
	case TypeInt:
		value := s.Value.Int
		body.IntVal = &value
	case TypeFloat:
		value := s.Value.Float
		body.FloatVal = &value
	case TypeBool:
		value := s.Value.Bool
		body.BoolVal = &value
	}
	return body
}

// UpdateSettingBody carries the new value. `type` is required and must
// match the setting's declared kind: making the caller state what it
// thinks it is writing turns a client that has drifted from the server
// into a 422 naming both kinds, rather than a silently ignored write.
type UpdateSettingBody struct {
	Type string `json:"type" enum:"string,int,float,bool"`

	StringVal *string  `json:"string_val,omitempty"`
	IntVal    *int64   `json:"int_val,omitempty"`
	FloatVal  *float64 `json:"float_val,omitempty"`
	BoolVal   *bool    `json:"bool_val,omitempty"`
}

// toValue picks the field `type` names, refusing a body that omits it.
func (b UpdateSettingBody) toValue() (Value, error) {
	switch b.Type {
	case wireString:
		if b.StringVal == nil {
			return Value{}, fmt.Errorf("%w: type is string but string_val is absent", ErrTypeMismatch)
		}
		return StrValue(*b.StringVal), nil
	case wireInt:
		if b.IntVal == nil {
			return Value{}, fmt.Errorf("%w: type is int but int_val is absent", ErrTypeMismatch)
		}
		return IntValue(*b.IntVal), nil
	case wireFloat:
		if b.FloatVal == nil {
			return Value{}, fmt.Errorf("%w: type is float but float_val is absent", ErrTypeMismatch)
		}
		return FloatValue(*b.FloatVal), nil
	case wireBool:
		if b.BoolVal == nil {
			return Value{}, fmt.Errorf("%w: type is bool but bool_val is absent", ErrTypeMismatch)
		}
		return BoolValue(*b.BoolVal), nil
	default:
		return Value{}, fmt.Errorf("%w: %q", ErrTypeMismatch, b.Type)
	}
}

// -- Operations --

type ListSettingsRequest struct{}

// ListSettingsResponse wraps its array in an object, matching the other
// collection endpoints -- a bare top-level array has nowhere to grow.
type ListSettingsResponse struct {
	Body struct {
		Settings []SettingBody `json:"settings"`
	}
}

func (h *Handler) ListSettings(ctx context.Context, _ *ListSettingsRequest) (*ListSettingsResponse, error) {
	found, err := h.svc.List(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}

	resp := &ListSettingsResponse{}
	resp.Body.Settings = make([]SettingBody, 0, len(found))
	for _, s := range found {
		resp.Body.Settings = append(resp.Body.Settings, settingToBody(s))
	}
	return resp, nil
}

type GetSettingRequest struct {
	Name string `path:"name"`
}

type SettingResponse struct {
	Body SettingBody
}

func (h *Handler) GetSetting(ctx context.Context, input *GetSettingRequest) (*SettingResponse, error) {
	found, err := h.svc.Get(ctx, input.Name)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &SettingResponse{Body: settingToBody(found)}, nil
}

type UpdateSettingRequest struct {
	Name string `path:"name"`
	Body UpdateSettingBody
}

func (h *Handler) UpdateSetting(ctx context.Context, input *UpdateSettingRequest) (*SettingResponse, error) {
	value, err := input.Body.toValue()
	if err != nil {
		return nil, h.mapError(err)
	}
	// The service refuses a write with no person to record (ErrNoActor).
	var by uuid.UUID
	if who := authn.PersonFromContext(ctx); who != nil {
		by = who.ID
	}
	written, err := h.svc.Set(ctx, input.Name, value, by)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &SettingResponse{Body: settingToBody(written)}, nil
}

// SettingValueBody is one value of a setting, laid out like SettingBody's
// value fields: the one `type` names is set and the rest are null.
type SettingValueBody struct {
	Type      string   `json:"type" enum:"string,int,float,bool"`
	StringVal *string  `json:"string_val"`
	IntVal    *int64   `json:"int_val"`
	FloatVal  *float64 `json:"float_val"`
	BoolVal   *bool    `json:"bool_val"`
}

func valueToBody(v Value) SettingValueBody {
	b := SettingValueBody{Type: wireType(v.Type)}
	switch v.Type {
	case TypeStr:
		b.StringVal = &v.Str
	case TypeInt:
		b.IntVal = &v.Int
	case TypeFloat:
		b.FloatVal = &v.Float
	case TypeBool:
		b.BoolVal = &v.Bool
	}
	return b
}

// ServerSettingChangeBody is one change in a setting's history.
//
// Who made it is three cases the UI must keep apart: a person (changed_by_id
// and, if they set one, changed_by_name; changed_by_former_member when they
// have left the tenant), a person whose account was removed
// (changed_by_deleted), and a change from before history was kept
// (before_history_kept), which is neither of the others.
type ServerSettingChangeBody struct {
	ID        int64            `json:"id"`
	From      SettingValueBody `json:"from" doc:"what the setting read before this change: its default for the first"`
	To        SettingValueBody `json:"to"`
	ChangedAt time.Time        `json:"changed_at"`

	ChangedByID           *uuid.UUID `json:"changed_by_id"`
	ChangedByName         *string    `json:"changed_by_name" doc:"null when they have set no name"`
	ChangedByFormerMember bool       `json:"changed_by_former_member"`
	ChangedByDeleted      bool       `json:"changed_by_deleted"`
	BeforeHistoryKept     bool       `json:"before_history_kept"`
}

type ServerSettingHistoryRequest struct {
	Name   string `path:"name"`
	Limit  int    `query:"limit" default:"50" minimum:"1" maximum:"200"`
	Before int64  `query:"before" minimum:"0" doc:"a previous response's next_before; 0 for the newest"`
}

type ServerSettingHistoryResponse struct {
	Body struct {
		Changes []ServerSettingChangeBody `json:"changes"`
		// NextBefore is set when older changes follow; pass it as before.
		NextBefore *int64 `json:"next_before,omitempty"`
	}
}

func (h *Handler) SettingHistory(ctx context.Context, in *ServerSettingHistoryRequest) (*ServerSettingHistoryResponse, error) {
	changes, next, err := h.svc.History(ctx, in.Name, in.Before, in.Limit)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &ServerSettingHistoryResponse{}
	resp.Body.Changes = make([]ServerSettingChangeBody, 0, len(changes))
	for _, c := range changes {
		b := ServerSettingChangeBody{
			ID: c.ID, From: valueToBody(c.From), To: valueToBody(c.To), ChangedAt: c.At,
			ChangedByDeleted: c.ByDeleted, BeforeHistoryKept: c.BeforeHistoryKept(),
		}
		if c.By != nil {
			id := c.By.ID
			b.ChangedByID, b.ChangedByFormerMember = &id, c.By.FormerMember
			if c.By.Name != "" {
				name := c.By.Name
				b.ChangedByName = &name
			}
		}
		resp.Body.Changes = append(resp.Body.Changes, b)
	}
	if next != 0 {
		resp.Body.NextBefore = &next
	}
	return resp, nil
}
