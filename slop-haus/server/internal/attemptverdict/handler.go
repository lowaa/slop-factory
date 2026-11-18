package attemptverdict

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

var errTable = httperr.Table{
	Rules: []httperr.Rule{
		{Err: ticket.ErrNoSuchAttempt, Kind: httperr.NotFound},
		{Err: ErrNoVerdict, Kind: httperr.NotFound},
		{Err: ErrInvalidVerdict, Kind: httperr.Invalid},
		{Err: ErrNoteTooLong, Kind: httperr.Invalid},
		{Err: ErrNotFinished, Kind: httperr.Conflict},
		{Err: authn.ErrNotPerson, Kind: httperr.Forbidden},
	},
	Unexpected: "unexpected attempt verdict error",
}

type Handler struct {
	svc    *Service
	logger *slog.Logger
}

func NewHandler(svc *Service, logHandler slog.Handler) *Handler {
	return &Handler{svc: svc, logger: slog.New(logHandler)}
}

type AttemptPath struct {
	ID      int64 `path:"id"`
	Attempt int   `path:"n" minimum:"1"`
}

type SetVerdictBody struct {
	Verdict string `json:"verdict" enum:"would_merge,would_merge_with_changes,would_not_merge"`
	Note    string `json:"note,omitempty" maxLength:"1000" doc:"An optional short note."`
}

type SetVerdictRequest struct {
	AttemptPath
	Body SetVerdictBody
}

type VerdictResponse struct {
	Body *ticket.AttemptVerdictBody
}

func (h *Handler) SetVerdict(ctx context.Context, in *SetVerdictRequest) (*VerdictResponse, error) {
	ref := ticket.AttemptRef{TicketID: in.ID, Number: in.Attempt}
	body, err := h.svc.Set(ctx, authn.PersonFromContext(ctx), ref, in.Body.Verdict, in.Body.Note)
	if err != nil {
		return nil, httperr.Map(h.logger, err, &errTable)
	}
	return &VerdictResponse{Body: body}, nil
}

func (h *Handler) GetVerdict(ctx context.Context, in *AttemptPath) (*VerdictResponse, error) {
	body, err := h.svc.Get(ctx, authn.PersonFromContext(ctx), ticket.AttemptRef{TicketID: in.ID, Number: in.Attempt})
	if err != nil {
		return nil, httperr.Map(h.logger, err, &errTable)
	}
	return &VerdictResponse{Body: body}, nil
}

func RegisterRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "setAttemptVerdict",
		Method:      http.MethodPut,
		Path:        "/api/tickets/{id}/attempts/{n}/verdict",
		Summary:     "Give a finished attempt your verdict: would merge, with changes, or not",
		Description: "Any member who is a person; an agent is refused (403). Replaces your current verdict and keeps " +
			"the earlier ones in its history. 409 until the attempt has finished; 404 for a ticket or attempt the " +
			"tenant does not have.",
	}, h.SetVerdict)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getAttemptVerdict",
		Method:      http.MethodGet,
		Path:        "/api/tickets/{id}/attempts/{n}/verdict",
		Summary:     "Get an attempt's verdict and its history",
		Description: "People only (403 for an agent). 404 when nobody has given one.",
	}, h.GetVerdict)
}
