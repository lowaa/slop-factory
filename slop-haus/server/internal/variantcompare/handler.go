package variantcompare

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

var errTable = httperr.Table{
	Rules: []httperr.Rule{
		{Err: ErrInvalid, Kind: httperr.Invalid},
		{Err: ErrTooManyTickets, Kind: httperr.Invalid},
		{Err: authn.ErrNotPerson, Kind: httperr.Forbidden},
	},
	Unexpected: "unexpected variant comparison error",
}

type Handler struct {
	svc    *Service
	logger *slog.Logger
}

func NewHandler(svc *Service, logHandler slog.Handler) *Handler {
	return &Handler{svc: svc, logger: slog.New(logHandler)}
}

type CompareBody struct {
	ReplayBatchID *uuid.UUID `json:"replay_batch_id,omitempty" doc:"A replay batch whose tickets are the set. Give this or ticket_ids."`
	TicketIDs     []int64    `json:"ticket_ids,omitempty" maxItems:"1000" doc:"The tickets of the set, as picked with the filter widget. At most 200 distinct ones."`
	MinDiffLines  *int       `json:"min_diff_lines,omitempty" minimum:"0" doc:"Keep tickets whose original (promoted) attempt changed at least this many lines."`
	MaxDiffLines  *int       `json:"max_diff_lines,omitempty" minimum:"0" doc:"Keep tickets whose original (promoted) attempt changed at most this many lines."`
	Baseline      string     `json:"baseline" maxLength:"64" doc:"The baseline variant's name; empty is the default variant (attempts routed to none)."`
	Challengers   []string   `json:"challengers" minItems:"1" maxItems:"10" doc:"The variants compared with the baseline, by name; empty is the default variant."`
}

type CompareRequest struct {
	Body CompareBody
}

type CompareResponse struct {
	Body *Result
}

func (h *Handler) Compare(ctx context.Context, in *CompareRequest) (*CompareResponse, error) {
	b := &in.Body
	res, err := h.svc.Compare(ctx, authn.PersonFromContext(ctx), &Request{
		ReplayBatchID: b.ReplayBatchID, TicketIDs: b.TicketIDs, MinDiffLines: b.MinDiffLines, MaxDiffLines: b.MaxDiffLines,
		Baseline: b.Baseline, Challengers: b.Challengers,
	})
	if err != nil {
		return nil, httperr.Map(h.logger, err, &errTable)
	}
	return &CompareResponse{Body: res}, nil
}

func RegisterRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "compareVariants",
		Method:      http.MethodPost,
		Path:        "/api/compare",
		Summary:     "Compare variants across a chosen set of tickets",
		Description: "For each ticket both the baseline and a challenger variant finished an attempt on, the " +
			"challenger's value minus the baseline's, then the median, spread (interquartile range, min, max) " +
			"and n over the tickets, for cost, human cost, quality and signals side by side, never combined. " +
			"A person only (403 for an agent), because it shows verdicts. The set is at most 200 tickets (422 over). " +
			"The response's notes say what is not counted.",
	}, h.Compare)
}
