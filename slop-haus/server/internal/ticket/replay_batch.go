package ticket

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// DefaultReplayBatchMax is how many attempts one replay batch may add unless
// configured otherwise. Conservative: each attempt is a paid worker run.
const DefaultReplayBatchMax = 50

var (
	// ErrReplayBatchTooLarge is a batch that would add more attempts than the
	// server's per-batch maximum. Nothing is added.
	ErrReplayBatchTooLarge = errors.New("ticket: the replay batch is over the server's maximum")
	// ErrReplayBatchUnconfirmed is a batch whose confirmation does not name
	// the number of attempts it would add.
	ErrReplayBatchUnconfirmed = errors.New("ticket: the replay batch was not confirmed with its attempt count")
	// ErrReplayBatchShape is a batch with no tickets, no variants, a bad
	// number of runs, or more attempts per ticket than a ticket can have live.
	ErrReplayBatchShape = errors.New("ticket: the replay batch is malformed")
)

// ReplayBatchRequest asks for Runs replays of every ticket for every variant.
type ReplayBatchRequest struct {
	TicketIDs []int64
	// VersionIDs are the variant versions to replay under; a nil entry is the
	// default variant (no routing).
	VersionIDs []*uuid.UUID
	// Runs is how many replays each variant gets per ticket.
	Runs int
	// ConfirmAttempts must equal the number of attempts the batch adds
	// (Count): the person has seen and agreed to that number.
	ConfirmAttempts int
}

// ReplayBatchLimits are what the board shows before a batch starts.
type ReplayBatchLimits struct {
	// MaxAttempts is the most attempts one batch may add.
	MaxAttempts int
	// MaxPerTicket is the most live attempts a ticket may have; a batch whose
	// variants times runs exceed it can never fit.
	MaxPerTicket int
}

// ReplayBatchAdded is the replays added to one ticket.
type ReplayBatchAdded struct {
	TicketID int64
	Attempts []int
}

// ReplayBatchRefusal is a ticket the batch could not add to, and why.
type ReplayBatchRefusal struct {
	TicketID int64
	Reason   string
}

// ReplayBatchResult is what a batch did. Tickets are independent: one that is
// refused leaves the others' replays added.
type ReplayBatchResult struct {
	BatchID uuid.UUID
	Added   []ReplayBatchAdded
	Refused []ReplayBatchRefusal
}

// ReplayBatchLimits reports the server's limits.
func (s *Service) ReplayBatchLimits() ReplayBatchLimits {
	return ReplayBatchLimits{MaxAttempts: s.replayBatchMax, MaxPerTicket: MaxLiveAttempts}
}

// rows is the version of every attempt one ticket gets: each variant, Runs
// times.
func (r *ReplayBatchRequest) rows() []*uuid.UUID {
	var rows []*uuid.UUID
	for _, v := range r.VersionIDs {
		for i := 0; i < r.Runs; i++ {
			rows = append(rows, v)
		}
	}
	return rows
}

// Count is how many attempts the batch adds if every ticket takes them.
func (r *ReplayBatchRequest) Count() int {
	return len(r.TicketIDs) * len(r.VersionIDs) * r.Runs
}

// AddReplayBatch adds the replays to each ticket, one transaction per ticket,
// under one batch id. The request is refused whole, adding nothing, when it is
// malformed, over the per-batch maximum or not confirmed with its count. Past
// that, a ticket the five-live-attempts cap (or its status) refuses is
// reported in Refused rather than queued: a replay that waits for room would
// start unattended, long after the person confirmed the cost.
func (s *Service) AddReplayBatch(ctx context.Context, userID uuid.UUID, req *ReplayBatchRequest) (*ReplayBatchResult, error) {
	ids, err := s.batch(req.TicketIDs)
	if err != nil {
		return nil, err
	}
	req = &ReplayBatchRequest{TicketIDs: ids, VersionIDs: uniqueVersions(req.VersionIDs), Runs: req.Runs, ConfirmAttempts: req.ConfirmAttempts}
	perTicket := len(req.VersionIDs) * req.Runs
	switch {
	case len(req.VersionIDs) == 0 || req.Runs < 1:
		return nil, fmt.Errorf("%w: give at least one variant and one run", ErrReplayBatchShape)
	case perTicket > MaxLiveAttempts:
		return nil, fmt.Errorf("%w: %d variants x %d runs is %d attempts per ticket, and a ticket holds at most %d",
			ErrReplayBatchShape, len(req.VersionIDs), req.Runs, perTicket, MaxLiveAttempts)
	case req.Count() > s.replayBatchMax:
		return nil, fmt.Errorf("%w: %d attempts, at most %d", ErrReplayBatchTooLarge, req.Count(), s.replayBatchMax)
	case req.ConfirmAttempts != req.Count():
		return nil, fmt.Errorf("%w: this adds %d attempts, confirmed %d", ErrReplayBatchUnconfirmed, req.Count(), req.ConfirmAttempts)
	}

	batchID := uuid.New()
	result := &ReplayBatchResult{BatchID: batchID}
	rows := req.rows()
	for _, id := range ids {
		added, err := s.addAttemptsInBatch(ctx, userID, id, rows, true, &batchID)
		if err != nil {
			reason, ok := replayRefusal(err)
			if !ok {
				s.logger.ErrorContext(ctx, "add a replay batch's attempts", "batch_id", batchID, "ticket", id, "err", err)
				reason = "the replays could not be added"
			}
			result.Refused = append(result.Refused, ReplayBatchRefusal{TicketID: id, Reason: reason})
			continue
		}
		entry := ReplayBatchAdded{TicketID: id}
		for _, a := range added {
			entry.Attempts = append(entry.Attempts, a.Number)
		}
		result.Added = append(result.Added, entry)
	}
	return result, nil
}

// replayRefusal is the reason a ticket is refused, for the errors a batch
// reports per ticket rather than failing on.
func replayRefusal(err error) (string, bool) {
	for _, known := range []error{ErrNotFound, ErrReplayNotDone, ErrNoBase, ErrNoSnapshot, ErrAttemptCap, ErrVariantNotFound, ErrInvalidVariant} {
		if errors.Is(err, known) {
			return err.Error(), true
		}
	}
	return "", false
}

func uniqueVersions(in []*uuid.UUID) []*uuid.UUID {
	seen := map[uuid.UUID]bool{}
	hasDefault := false
	var out []*uuid.UUID
	for _, v := range in {
		switch {
		case v == nil && hasDefault, v != nil && seen[*v]:
		case v == nil:
			hasDefault = true
			out = append(out, v)
		default:
			seen[*v] = true
			out = append(out, v)
		}
	}
	return out
}

type ReplayBatchBody struct {
	TicketIDs []int64 `json:"ticket_ids" minItems:"1" doc:"The done tickets to replay."`
	// VersionIDs is a list because the default variant is a row too: null is the default.
	VersionIDs      []*string `json:"version_ids" minItems:"1" doc:"Variant versions to replay under; null is the default variant, which the board offers beside the challengers for a fair comparison."`
	Runs            int       `json:"runs" minimum:"1" maximum:"5" doc:"Replays per variant per ticket."`
	ConfirmAttempts int       `json:"confirm_attempts" doc:"The number of attempts the batch adds (tickets x variants x runs), which the person confirmed. Refused with 422 when it differs."`
}

type ReplayBatchRequestInput struct {
	Body ReplayBatchBody
}

type ReplayBatchResponseBody struct {
	BatchID uuid.UUID                `json:"batch_id"`
	Added   []ReplayBatchAddedBody   `json:"added"`
	Refused []ReplayBatchRefusedBody `json:"refused"`
}

type ReplayBatchAddedBody struct {
	TicketID int64 `json:"ticket_id"`
	Attempts []int `json:"attempts"`
}

type ReplayBatchRefusedBody struct {
	TicketID int64  `json:"ticket_id"`
	Reason   string `json:"reason"`
}

type ReplayBatchResponse struct {
	Body ReplayBatchResponseBody
}

type ReplayBatchLimitsResponse struct {
	Body struct {
		MaxAttempts  int `json:"max_attempts" doc:"The most attempts one replay batch may add."`
		MaxPerTicket int `json:"max_per_ticket" doc:"The most live attempts a ticket may have, so the most variants x runs a batch may give each ticket."`
	}
}

func registerReplayBatchRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getReplayBatchLimits",
		Method:      http.MethodGet,
		Path:        "/api/replay_batches/limits",
		Summary:     "The limits of a replay batch",
		Description: "404 feature_disabled when the server does not serve replays.",
	}, h.GetReplayBatchLimits)
	huma.Register(api, huma.Operation{
		Metadata:      authn.RequireUser(),
		OperationID:   "addReplayBatch",
		Method:        http.MethodPost,
		Path:          "/api/replay_batches",
		Summary:       "Replay several done tickets under several variants",
		DefaultStatus: http.StatusCreated,
		Description: "Adds runs replays per variant to each ticket, all under one batch id. 422, adding nothing, when confirm_attempts is not " +
			"tickets x variants x runs, when that is over the server's maximum, or when variants x runs exceed five per ticket. " +
			"Past that, each ticket is its own transaction: one that is not done, has no base commit or snapshot, or would pass five live attempts " +
			"is listed in refused with its reason (never queued) and the others are added. 404 feature_disabled when the server does not serve replays.",
		Errors: []int{http.StatusUnprocessableEntity},
	}, h.AddReplayBatch)
}

func (h *Handler) GetReplayBatchLimits(_ context.Context, _ *struct{}) (*ReplayBatchLimitsResponse, error) {
	if !h.attemptsAdd || !h.replays {
		return nil, featureDisabledError()
	}
	limits := h.svc.ReplayBatchLimits()
	resp := &ReplayBatchLimitsResponse{}
	resp.Body.MaxAttempts = limits.MaxAttempts
	resp.Body.MaxPerTicket = limits.MaxPerTicket
	return resp, nil
}

func (h *Handler) AddReplayBatch(ctx context.Context, input *ReplayBatchRequestInput) (*ReplayBatchResponse, error) {
	if !h.attemptsAdd || !h.replays {
		return nil, featureDisabledError()
	}
	user, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("ticket route is missing its auth middleware")
	}
	req := &ReplayBatchRequest{TicketIDs: input.Body.TicketIDs, Runs: input.Body.Runs, ConfirmAttempts: input.Body.ConfirmAttempts}
	for _, raw := range input.Body.VersionIDs {
		id, err := parseVersionID(raw)
		if err != nil {
			return nil, err
		}
		req.VersionIDs = append(req.VersionIDs, id)
	}
	result, err := h.svc.AddReplayBatch(ctx, user.ID, req)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &ReplayBatchResponse{Body: ReplayBatchResponseBody{
		BatchID: result.BatchID, Added: []ReplayBatchAddedBody{}, Refused: []ReplayBatchRefusedBody{},
	}}
	for _, a := range result.Added {
		resp.Body.Added = append(resp.Body.Added, ReplayBatchAddedBody{TicketID: a.TicketID, Attempts: a.Attempts})
	}
	for _, r := range result.Refused {
		resp.Body.Refused = append(resp.Body.Refused, ReplayBatchRefusedBody{TicketID: r.TicketID, Reason: r.Reason})
	}
	return resp, nil
}
