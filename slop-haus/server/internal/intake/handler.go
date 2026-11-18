package intake

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

// Handler is the board's side: registering sources, and seeing what their
// deliveries did.
type Handler struct {
	svc    *Service
	logger *slog.Logger
}

func NewHandler(svc *Service, logHandler slog.Handler) *Handler {
	return &Handler{svc: svc, logger: slog.New(logHandler)}
}

var errTable = httperr.Table{
	Rules: []httperr.Rule{
		{Err: ErrDisabled, Kind: httperr.Unavailable, Code: "intake_disabled"},
		{Err: ErrHMACDisabled, Kind: httperr.Invalid},
		{Err: ErrInvalidSource, Kind: httperr.Invalid},
		{Err: ErrNotOwner, Kind: httperr.Forbidden},
		{Err: scope.ErrInvalidPath, Kind: httperr.Invalid},
		{Err: ErrNotFound, Kind: httperr.NotFound},
	},
	Custom: func(err error) error {
		if p := user.ProblemFor(err); p != nil {
			return p
		}
		return nil
	},
	Unexpected: "unexpected intake error",
}

func (h *Handler) mapError(err error) error {
	return httperr.Map(h.logger, err, &errTable)
}

// -- Wire types --

type IntakeSourceBody struct {
	ID              uuid.UUID  `json:"id"`
	Name            string     `json:"name"`
	Adapter         string     `json:"adapter" enum:"native,alertmanager"`
	Trust           string     `json:"trust" enum:"trusted,untrusted"`
	Auth            string     `json:"auth" enum:"bearer,hmac"`
	DefaultStatus   string     `json:"default_status" enum:"open,preparing"`
	DefaultPriority string     `json:"default_priority" enum:"low,normal,high,urgent"`
	MaxPriority     string     `json:"max_priority" enum:"low,normal,high,urgent"`
	Roles           []string   `json:"roles" doc:"role names the source's tickets require"`
	RoleMatch       string     `json:"role_match" enum:"any,all"`
	Paths           []string   `json:"paths"`
	ReopenPolicy    string     `json:"reopen_policy" enum:"new,reopen" doc:"what an open does when the key's ticket is done"`
	ResolveCloses   bool       `json:"resolve_closes" doc:"a resolve from this source cancels the key's ticket while no agent or person has touched it; never on for an untrusted source"`
	URL             string     `json:"url" doc:"where the sender posts"`
	CreatedBy       *uuid.UUID `json:"created_by"`
	// CreatedByDeleted is true when the person who set the source up has had
	// their account deleted: created_by is then null.
	CreatedByDeleted bool       `json:"created_by_deleted,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	RevokedAt        *time.Time `json:"revoked_at"`
	LastDeliveryAt   *time.Time `json:"last_delivery_at"`
}

func (h *Handler) sourceToBody(s *Source) IntakeSourceBody {
	return IntakeSourceBody{
		ID: s.ID, Name: s.Name, Adapter: s.Adapter, Trust: string(s.Trust), Auth: string(s.AuthKind),
		DefaultStatus: string(s.DefaultStatus), DefaultPriority: string(s.DefaultPriority),
		MaxPriority: string(s.MaxPriority), Roles: nonNil(s.RoleNames), RoleMatch: string(s.RoleMatch),
		Paths: nonNil(s.Paths), ReopenPolicy: string(s.ReopenPolicy), ResolveCloses: s.ResolveCloses, URL: h.svc.IntakeURL(s.ID),
		CreatedBy: s.CreatedBy, CreatedByDeleted: s.CreatedByDeleted, CreatedAt: s.CreatedAt, RevokedAt: s.RevokedAt, LastDeliveryAt: s.LastDeliveryAt,
	}
}

type IntakeDeliveryBody struct {
	ID         string    `json:"id"`
	SourceID   uuid.UUID `json:"source_id"`
	Event      string    `json:"event"`
	Key        string    `json:"key"`
	Outcome    string    `json:"outcome" enum:"processing,applied,ignored,rejected,rate_limited,error"`
	Downgraded bool      `json:"downgraded" doc:"the sender asked for open and a trust rule made it preparing"`
	Detail     string    `json:"detail"`
	TicketID   *int64    `json:"ticket_id"`
	ReceivedAt time.Time `json:"received_at"`
}

func deliveryToBody(d *Delivery) IntakeDeliveryBody {
	return IntakeDeliveryBody{ID: d.ID, SourceID: d.SourceID, Event: d.Event, Key: d.Key, Outcome: string(d.Outcome),
		Downgraded: d.Downgraded, Detail: d.Detail, TicketID: d.TicketID, ReceivedAt: d.ReceivedAt}
}

// -- Operations --

type IntakeSourcesResponse struct {
	Body struct {
		Enabled    bool                 `json:"enabled" doc:"false when this server has no intake listener"`
		HMAC       bool                 `json:"hmac" doc:"whether HMAC sources can be made: the server has an INTAKE_KEY"`
		Sources    []IntakeSourceBody   `json:"sources"`
		Deliveries []IntakeDeliveryBody `json:"deliveries" doc:"the most recent, newest first"`
	}
}

func (h *Handler) ListSources(ctx context.Context, _ *struct{}) (*IntakeSourcesResponse, error) {
	sources, err := h.svc.List(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	deliveries, err := h.svc.Deliveries(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &IntakeSourcesResponse{}
	resp.Body.Enabled = h.svc.Enabled()
	resp.Body.HMAC = h.svc.cfg.Secrets != nil
	resp.Body.Sources = make([]IntakeSourceBody, 0, len(sources))
	for i := range sources {
		resp.Body.Sources = append(resp.Body.Sources, h.sourceToBody(&sources[i]))
	}
	resp.Body.Deliveries = make([]IntakeDeliveryBody, 0, len(deliveries))
	for i := range deliveries {
		resp.Body.Deliveries = append(resp.Body.Deliveries, deliveryToBody(&deliveries[i]))
	}
	return resp, nil
}

type CreateIntakeSourceRequest struct {
	Body struct {
		Name            string   `json:"name" minLength:"1" maxLength:"100"`
		Adapter         string   `json:"adapter" enum:"native,alertmanager"`
		Trust           string   `json:"trust" enum:"trusted,untrusted" doc:"untrusted sources' tickets are always created preparing"`
		Auth            string   `json:"auth" enum:"bearer,hmac"`
		DefaultStatus   string   `json:"default_status,omitempty" enum:"open,preparing,"`
		DefaultPriority string   `json:"default_priority,omitempty" enum:"low,normal,high,urgent,"`
		MaxPriority     string   `json:"max_priority,omitempty" enum:"low,normal,high,urgent,"`
		Roles           []string `json:"roles,omitempty"`
		RoleMatch       string   `json:"role_match,omitempty" enum:"any,all,"`
		Paths           []string `json:"paths,omitempty"`
		ReopenPolicy    string   `json:"reopen_policy,omitempty" enum:"new,reopen,"`
		ResolveCloses   bool     `json:"resolve_closes,omitempty" doc:"let a resolve cancel the key's unclaimed ticket; trusted sources only, and only the team's owner may set it"`
	}
}

type IntakeCredentialBody struct {
	Source IntakeSourceBody `json:"source"`
	Token  string           `json:"token,omitempty" doc:"the bearer token to give the sender; shown this once"`
	Secret string           `json:"secret,omitempty" doc:"the HMAC signing secret to give the sender; shown this once"`
	Header string           `json:"signature_header,omitempty" doc:"the header an HMAC sender puts sha256=<hex of the body's HMAC> in"`
}

type IntakeCredentialResponse struct {
	Body IntakeCredentialBody
}

func (h *Handler) credentialResponse(c *Created) *IntakeCredentialResponse {
	body := IntakeCredentialBody{Source: h.sourceToBody(c.Source), Token: c.Token, Secret: c.Secret}
	if c.Secret != "" {
		body.Header = SignatureHeader
	}
	return &IntakeCredentialResponse{Body: body}
}

func (h *Handler) CreateSource(ctx context.Context, in *CreateIntakeSourceRequest) (*IntakeCredentialResponse, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	b := &in.Body
	created, err := h.svc.Create(ctx, who, &Spec{
		Name: b.Name, Adapter: b.Adapter, Trust: Trust(b.Trust), AuthKind: AuthKind(b.Auth),
		DefaultStatus: ticket.Status(b.DefaultStatus), DefaultPriority: model.Priority(b.DefaultPriority),
		MaxPriority: model.Priority(b.MaxPriority), RoleNames: b.Roles, RoleMatch: model.RoleMatch(b.RoleMatch),
		Paths: b.Paths, ReopenPolicy: ReopenPolicy(b.ReopenPolicy), ResolveCloses: b.ResolveCloses,
	})
	if err != nil {
		return nil, h.mapError(err)
	}
	return h.credentialResponse(created), nil
}

type IntakeSourceIDRequest struct {
	ID uuid.UUID `path:"id"`
}

func (h *Handler) RotateSource(ctx context.Context, in *IntakeSourceIDRequest) (*IntakeCredentialResponse, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	created, err := h.svc.Rotate(ctx, who, in.ID)
	if err != nil {
		return nil, h.mapError(err)
	}
	return h.credentialResponse(created), nil
}

type IntakeNoContentResponse struct{}

func (h *Handler) RevokeSource(ctx context.Context, in *IntakeSourceIDRequest) (*IntakeNoContentResponse, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	if err := h.svc.Revoke(ctx, who, in.ID); err != nil {
		return nil, h.mapError(err)
	}
	return &IntakeNoContentResponse{}, nil
}
