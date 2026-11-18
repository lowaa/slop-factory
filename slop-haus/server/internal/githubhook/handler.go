package githubhook

import (
	"context"
	"log/slog"
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

// Handler is the board's side: seeing what the App's deliveries did. A thin wrapper over Service, as ticket.Handler is.
type Handler struct {
	svc    *Service
	logger *slog.Logger
}

func NewHandler(svc *Service, logHandler slog.Handler) *Handler {
	return &Handler{svc: svc, logger: slog.New(logHandler)}
}

var errTable = httperr.Table{
	Rules: []httperr.Rule{
		{Err: ErrDisabled, Kind: httperr.Unavailable, Code: "webhooks_disabled"},
	},
	Custom: func(err error) error {
		if p := user.ProblemFor(err); p != nil {
			return p
		}
		return nil
	},
	Unexpected: "unexpected github webhook error",
}

func (h *Handler) mapError(err error) error {
	return httperr.Map(h.logger, err, &errTable)
}

// -- Wire types --

type DeliveryBody struct {
	ID             string    `json:"id" doc:"GitHub's X-GitHub-Delivery"`
	InstallationID int64     `json:"installation_id"`
	Event          string    `json:"event"`
	Action         string    `json:"action"`
	Outcome        string    `json:"outcome" enum:"processing,applied,ignored,error"`
	Detail         string    `json:"detail" doc:"what the delivery did, or why it did nothing"`
	TicketID       *int64    `json:"ticket_id"`
	ReceivedAt     time.Time `json:"received_at"`
}

func deliveryToBody(d *Delivery) DeliveryBody {
	return DeliveryBody{ID: d.ID, InstallationID: d.InstallationID, Event: d.Event, Action: d.Action,
		Outcome: string(d.Outcome), Detail: d.Detail, TicketID: d.TicketID, ReceivedAt: d.ReceivedAt}
}

// -- Operations --

type WebhooksResponse struct {
	Body struct {
		Enabled    bool           `json:"enabled" doc:"false when this server has no WEBHOOK_PUBLIC_BASE_URL: nothing can be delivered"`
		Deliveries []DeliveryBody `json:"deliveries" doc:"the most recent, newest first"`
	}
}

func (h *Handler) ListWebhooks(ctx context.Context, _ *struct{}) (*WebhooksResponse, error) {
	deliveries, err := h.svc.Deliveries(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &WebhooksResponse{}
	resp.Body.Enabled = h.svc.Enabled()
	resp.Body.Deliveries = make([]DeliveryBody, 0, len(deliveries))
	for i := range deliveries {
		resp.Body.Deliveries = append(resp.Body.Deliveries, deliveryToBody(&deliveries[i]))
	}
	return resp, nil
}
