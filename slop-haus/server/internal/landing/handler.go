package landing

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

// Handler serves the landing queue and the tenant's landing capabilities.
type Handler struct {
	status *Status
	logger *slog.Logger
}

func NewHandler(status *Status, logHandler slog.Handler) *Handler {
	return &Handler{status: status, logger: slog.New(logHandler)}
}

var errTable = httperr.Table{
	Rules:      []httperr.Rule{},
	Unexpected: "unexpected landing error",
}

func (h *Handler) mapError(err error) error { return httperr.Map(h.logger, err, &errTable) }

// RegisterRoutes mounts the landing read surface; every operation is a signed-in
// user's, as the settings are.
func RegisterRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getLandingCapabilities",
		Method:      http.MethodGet,
		Path:        "/api/landing/capabilities",
		Summary:     "Whether the tenant can land by the merge queue, and why not if it cannot",
	}, h.GetCapabilities)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "listLandingQueue",
		Method:      http.MethodGet,
		Path:        "/api/landing/queue",
		Summary:     "The landing queue in landing order, then the changes handed back",
	}, h.ListQueue)
}

// CapabilitiesBody carries the reason merge_queue is unavailable as separate
// flags, so the UI words it.
type CapabilitiesBody struct {
	MergeMethod       string `json:"merge_method" enum:"worker,merge_queue"`
	MergeQueueEnabled bool   `json:"merge_queue_enabled" doc:"The server-wide MERGE_QUEUE_ENABLED switch."`
	ChangeTools       bool   `json:"change_tools" doc:"The tenant has a code host credential."`
	// MergeQueueAvailable is both of the above.
	MergeQueueAvailable bool `json:"merge_queue_available"`
}

type CapabilitiesResponse struct{ Body CapabilitiesBody }

func (h *Handler) GetCapabilities(ctx context.Context, _ *struct{}) (*CapabilitiesResponse, error) {
	c, err := h.status.Capabilities(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &CapabilitiesResponse{Body: CapabilitiesBody{
		MergeMethod: c.MergeMethod, MergeQueueEnabled: c.MergeQueueEnabled,
		ChangeTools: c.ChangeTools, MergeQueueAvailable: c.MergeQueueAvailable(),
	}}, nil
}

// LandingEntryBody is one ticket's row in the landing queue.
type LandingEntryBody struct {
	TicketID      int64  `json:"ticket_id"`
	AttemptNumber int    `json:"attempt_number"`
	ChangeURL     string `json:"change_url"`
	State         string `json:"state" enum:"landing,back_with_agent"`
	// Position is 1 for the next change to land, in the order the runner takes
	// them (priority with aging, a begun landing first); 0 once handed back.
	Position int `json:"position"`
	// Step is what the runner is doing to a landing that has begun; empty while
	// queued and once handed back. A CI retry is not a step of its own: it shows
	// as waiting_checks.
	Step string `json:"step" enum:"merging_main,waiting_checks,merging,"`
	// BackReason is why the change was handed back; empty while landing.
	BackReason string `json:"back_reason"`
	// BatchID is the batch the change is tested in; null outside one, which is
	// every change until batches are written.
	BatchID *int64 `json:"batch_id" nullable:"true"`
	// BatchPosition is the change's place in its batch's merge order, from 1;
	// 0 outside a batch.
	BatchPosition int `json:"batch_position"`
	// SinceUpdateSeconds is how long the row has been as it is, by the database
	// clock: the time in the current step once begun, else the wait since the
	// last enqueue.
	SinceUpdateSeconds int64 `json:"since_update_seconds"`
}

func entryBody(r *LandingRow) LandingEntryBody {
	return LandingEntryBody{
		TicketID: r.TicketID, AttemptNumber: r.AttemptNumber, ChangeURL: r.ChangeURL,
		State: string(r.State), Position: r.Position, Step: string(r.Step), BackReason: string(r.BackReason),
		BatchID: r.BatchID, BatchPosition: r.BatchPosition, SinceUpdateSeconds: int64(r.SinceUpdate.Seconds()),
	}
}

type ListQueueResponse struct {
	Body struct {
		Entries []LandingEntryBody `json:"entries"`
	}
}

func (h *Handler) ListQueue(ctx context.Context, _ *struct{}) (*ListQueueResponse, error) {
	rows, err := h.status.Queue(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &ListQueueResponse{}
	resp.Body.Entries = make([]LandingEntryBody, 0, len(rows))
	for i := range rows {
		resp.Body.Entries = append(resp.Body.Entries, entryBody(&rows[i]))
	}
	return resp, nil
}
