package evalset

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Handler is a thin HTTP wrapper over Service, like the other handlers.
type Handler struct {
	svc    *Service
	logger *slog.Logger
}

func NewHandler(svc *Service, logHandler slog.Handler) *Handler {
	return &Handler{svc: svc, logger: slog.New(logHandler)}
}

var errTable = httperr.Table{
	Rules: []httperr.Rule{
		{Err: authn.ErrNotPerson, Kind: httperr.Forbidden, Message: "only a signed-in person can use eval sets"},
		{Err: ErrInvalid, Kind: httperr.Invalid},
		{Err: ErrNotFound, Kind: httperr.NotFound},
		{Err: ErrNameTaken, Kind: httperr.Conflict},
		{Err: ErrEmpty, Kind: httperr.Conflict},
		{Err: ErrChanged, Kind: httperr.Conflict},
		{Err: ticket.ErrBatchTooLarge, Kind: httperr.Invalid},
		{Err: ErrReplaysDisabled, Kind: httperr.NotFound, Message: "not found"},
		{Err: ticket.ErrReplayBatchTooLarge, Kind: httperr.Invalid},
		{Err: ticket.ErrReplayBatchUnconfirmed, Kind: httperr.Invalid},
		{Err: ticket.ErrReplayBatchShape, Kind: httperr.Invalid},
		httperr.SignIn,
	},
	Unexpected: "unexpected eval sets error",
}

func (h *Handler) mapError(err error) error {
	return httperr.Map(h.logger, err, &errTable)
}

func (h *Handler) person(ctx context.Context) (*authn.User, error) {
	who := authn.PersonFromContext(ctx)
	if err := authn.RequirePerson(who); err != nil {
		return nil, h.mapError(err)
	}
	return who, nil
}

type FilterBody struct {
	Statuses    []string `json:"statuses,omitempty" doc:"Ticket statuses; none matches every status."`
	Priorities  []string `json:"priorities,omitempty"`
	Q           string   `json:"q,omitempty" doc:"The text filter of the ticket list."`
	ClosedFrom  string   `json:"closed_from,omitempty" format:"date"`
	ClosedTo    string   `json:"closed_to,omitempty" format:"date"`
	CreatedFrom string   `json:"created_from,omitempty" format:"date"`
	CreatedTo   string   `json:"created_to,omitempty" format:"date"`
	UpdatedFrom string   `json:"updated_from,omitempty" format:"date"`
	UpdatedTo   string   `json:"updated_to,omitempty" format:"date"`
}

func (b *FilterBody) filter() *Filter {
	if b == nil {
		return nil
	}
	f := Filter(*b)
	return &f
}

type EvalSetBody struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Filter      *FilterBody `json:"filter" doc:"The saved filter, re-resolved each time the set is used; null for a pinned set."`
	TicketIDs   []int64     `json:"ticket_ids" nullable:"true" doc:"The pinned tickets; null for a filter set."`
	CreatedBy   *string     `json:"created_by"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

func setBody(s *Set) EvalSetBody {
	out := EvalSetBody{
		ID: s.ID.String(), Name: s.Name, Description: s.Description, TicketIDs: s.TicketIDs,
		CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
	if s.Filter != nil {
		f := FilterBody(*s.Filter)
		out.Filter = &f
	}
	if s.TicketIDs != nil && out.TicketIDs == nil {
		out.TicketIDs = []int64{}
	}
	if s.CreatedBy != nil {
		id := s.CreatedBy.String()
		out.CreatedBy = &id
	}
	return out
}

type EvalSetPath struct {
	ID string `path:"id" format:"uuid"`
}

func (p *EvalSetPath) parse() (uuid.UUID, error) {
	id, err := uuid.Parse(p.ID)
	if err != nil {
		return uuid.Nil, huma.Error422UnprocessableEntity("id must be a uuid")
	}
	return id, nil
}

type EvalSetResponse struct {
	Body EvalSetBody
}

type ListEvalSetsResponse struct {
	Body struct {
		EvalSets []EvalSetBody `json:"eval_sets" doc:"By name."`
	}
}

func (h *Handler) List(ctx context.Context, _ *struct{}) (*ListEvalSetsResponse, error) {
	who, err := h.person(ctx)
	if err != nil {
		return nil, err
	}
	sets, err := h.svc.List(ctx, who)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &ListEvalSetsResponse{}
	resp.Body.EvalSets = make([]EvalSetBody, len(sets))
	for i := range sets {
		resp.Body.EvalSets[i] = setBody(&sets[i])
	}
	return resp, nil
}

type EvalSetDefinitionBody struct {
	Name        string      `json:"name" minLength:"1" maxLength:"512"`
	Description string      `json:"description" maxLength:"16000"`
	Filter      *FilterBody `json:"filter,omitempty" doc:"Exactly one of filter and ticket_ids."`
	TicketIDs   []int64     `json:"ticket_ids,omitempty" doc:"Exactly one of filter and ticket_ids. An empty list is a pinned set with no tickets yet."`
}

func (b *EvalSetDefinitionBody) definition() *Definition {
	return &Definition{Name: b.Name, Description: b.Description, Filter: b.Filter.filter(), TicketIDs: b.TicketIDs}
}

type CreateEvalSetRequest struct {
	Body EvalSetDefinitionBody
}

func (h *Handler) Create(ctx context.Context, in *CreateEvalSetRequest) (*EvalSetResponse, error) {
	who, err := h.person(ctx)
	if err != nil {
		return nil, err
	}
	set, err := h.svc.Create(ctx, who, in.Body.definition())
	if err != nil {
		return nil, h.mapError(err)
	}
	return &EvalSetResponse{Body: setBody(set)}, nil
}

type EvalSetRequest struct{ EvalSetPath }

func (h *Handler) Get(ctx context.Context, in *EvalSetRequest) (*EvalSetResponse, error) {
	who, id, err := h.personAndID(ctx, &in.EvalSetPath)
	if err != nil {
		return nil, err
	}
	set, err := h.svc.Get(ctx, who, id)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &EvalSetResponse{Body: setBody(set)}, nil
}

func (h *Handler) personAndID(ctx context.Context, p *EvalSetPath) (*authn.User, uuid.UUID, error) {
	who, err := h.person(ctx)
	if err != nil {
		return nil, uuid.Nil, err
	}
	id, err := p.parse()
	return who, id, err
}

type ReplaceEvalSetRequest struct {
	EvalSetPath
	Body EvalSetDefinitionBody
}

func (h *Handler) Replace(ctx context.Context, in *ReplaceEvalSetRequest) (*EvalSetResponse, error) {
	who, id, err := h.personAndID(ctx, &in.EvalSetPath)
	if err != nil {
		return nil, err
	}
	set, err := h.svc.Replace(ctx, who, id, in.Body.definition())
	if err != nil {
		return nil, h.mapError(err)
	}
	return &EvalSetResponse{Body: setBody(set)}, nil
}

func (h *Handler) Delete(ctx context.Context, in *EvalSetRequest) (*struct{}, error) {
	who, id, err := h.personAndID(ctx, &in.EvalSetPath)
	if err != nil {
		return nil, err
	}
	if err := h.svc.Delete(ctx, who, id); err != nil {
		return nil, h.mapError(err)
	}
	return nil, nil
}

func (h *Handler) PinMatches(ctx context.Context, in *EvalSetRequest) (*EvalSetResponse, error) {
	who, id, err := h.personAndID(ctx, &in.EvalSetPath)
	if err != nil {
		return nil, err
	}
	set, err := h.svc.PinMatches(ctx, who, id)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &EvalSetResponse{Body: setBody(set)}, nil
}

type RunEvalSetRequest struct {
	EvalSetPath
	Body struct {
		VersionIDs      []*string `json:"version_ids" minItems:"1" doc:"Variant versions to replay under; null is the default variant."`
		Runs            int       `json:"runs" minimum:"1" maximum:"5" doc:"Replays per variant per ticket."`
		ConfirmAttempts int       `json:"confirm_attempts" doc:"The number of attempts the run adds (the set's tickets x variants x runs). Refused with 422 when it differs."`
	}
}

type RunEvalSetResponse struct {
	Body struct {
		BatchID uuid.UUID                       `json:"batch_id"`
		Added   []ticket.ReplayBatchAddedBody   `json:"added"`
		Refused []ticket.ReplayBatchRefusedBody `json:"refused" doc:"Tickets the replay skipped, each with its reason."`
	}
}

func (h *Handler) Run(ctx context.Context, in *RunEvalSetRequest) (*RunEvalSetResponse, error) {
	who, id, err := h.personAndID(ctx, &in.EvalSetPath)
	if err != nil {
		return nil, err
	}
	req := &RunRequest{Runs: in.Body.Runs, ConfirmAttempts: in.Body.ConfirmAttempts}
	for _, raw := range in.Body.VersionIDs {
		if raw == nil {
			req.VersionIDs = append(req.VersionIDs, nil)
			continue
		}
		v, err := uuid.Parse(*raw)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("version_id must be a uuid")
		}
		req.VersionIDs = append(req.VersionIDs, &v)
	}
	result, err := h.svc.Run(ctx, who, id, req)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &RunEvalSetResponse{}
	resp.Body.BatchID = result.Batch.BatchID
	resp.Body.Added = []ticket.ReplayBatchAddedBody{}
	resp.Body.Refused = []ticket.ReplayBatchRefusedBody{}
	for _, a := range result.Batch.Added {
		resp.Body.Added = append(resp.Body.Added, ticket.ReplayBatchAddedBody{TicketID: a.TicketID, Attempts: a.Attempts})
	}
	for _, r := range result.Batch.Refused {
		resp.Body.Refused = append(resp.Body.Refused, ticket.ReplayBatchRefusedBody{TicketID: r.TicketID, Reason: r.Reason})
	}
	return resp, nil
}

type EvalSetBatchBody struct {
	BatchID     string    `json:"batch_id"`
	VersionIDs  []*string `json:"version_ids" doc:"The variants replayed; null is the default variant."`
	Runs        int       `json:"runs"`
	TicketCount int       `json:"ticket_count" doc:"How many tickets the batch added replays to."`
	CreatedBy   *string   `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
}

type ListEvalSetBatchesResponse struct {
	Body struct {
		Batches []EvalSetBatchBody `json:"batches" doc:"Newest first."`
	}
}

func (h *Handler) History(ctx context.Context, in *EvalSetRequest) (*ListEvalSetBatchesResponse, error) {
	who, id, err := h.personAndID(ctx, &in.EvalSetPath)
	if err != nil {
		return nil, err
	}
	batches, err := h.svc.History(ctx, who, id)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &ListEvalSetBatchesResponse{}
	resp.Body.Batches = make([]EvalSetBatchBody, len(batches))
	for i := range batches {
		b := &batches[i]
		body := EvalSetBatchBody{
			BatchID: b.BatchID.String(), VersionIDs: make([]*string, len(b.VersionIDs)),
			Runs: b.Runs, TicketCount: b.TicketCount, CreatedAt: b.CreatedAt,
		}
		for j, v := range b.VersionIDs {
			if v != nil {
				s := v.String()
				body.VersionIDs[j] = &s
			}
		}
		if b.CreatedBy != nil {
			s := b.CreatedBy.String()
			body.CreatedBy = &s
		}
		resp.Body.Batches[i] = body
	}
	return resp, nil
}

func RegisterRoutes(api huma.API, h *Handler) {
	const person = " Only a signed-in person (403 for an agent)."
	huma.Register(api, huma.Operation{
		Metadata: authn.RequireUser(), OperationID: "listEvalSets", Method: http.MethodGet, Path: "/api/eval_sets",
		Summary: "The tenant's eval sets", Description: person[1:],
	}, h.List)
	huma.Register(api, huma.Operation{
		Metadata: authn.RequireUser(), OperationID: "createEvalSet", Method: http.MethodPost, Path: "/api/eval_sets",
		Summary: "Save an eval set", DefaultStatus: http.StatusCreated, Errors: []int{http.StatusUnprocessableEntity, http.StatusConflict},
		Description: "Exactly one of filter and ticket_ids. 409 for a name another set has." + person,
	}, h.Create)
	huma.Register(api, huma.Operation{
		Metadata: authn.RequireUser(), OperationID: "getEvalSet", Method: http.MethodGet, Path: "/api/eval_sets/{id}",
		Summary: "One eval set", Description: person[1:],
	}, h.Get)
	huma.Register(api, huma.Operation{
		Metadata: authn.RequireUser(), OperationID: "replaceEvalSet", Method: http.MethodPut, Path: "/api/eval_sets/{id}",
		Summary: "Overwrite an eval set", Errors: []int{http.StatusUnprocessableEntity, http.StatusConflict},
		Description: "Replaces the name, description and filter or pinned tickets." + person,
	}, h.Replace)
	huma.Register(api, huma.Operation{
		Metadata: authn.RequireUser(), OperationID: "deleteEvalSet", Method: http.MethodDelete, Path: "/api/eval_sets/{id}",
		Summary: "Delete an eval set and its history", DefaultStatus: http.StatusNoContent, Description: person[1:],
	}, h.Delete)
	huma.Register(api, huma.Operation{
		Metadata: authn.RequireUser(), OperationID: "pinEvalSetMatches", Method: http.MethodPost, Path: "/api/eval_sets/{id}/pin",
		Summary: "Pin the tickets a filter set matches now",
		Description: "Turns a filter set into a pinned list of the tickets it matches at this moment; 422 when it matches more than the set's maximum. " +
			"A set that is already pinned is returned unchanged." + person,
		Errors: []int{http.StatusUnprocessableEntity},
	}, h.PinMatches)
	huma.Register(api, huma.Operation{
		Metadata: authn.RequireUser(), OperationID: "runEvalSet", Method: http.MethodPost, Path: "/api/eval_sets/{id}/runs",
		Summary: "Replay the set's tickets under several variants as one batch", DefaultStatus: http.StatusCreated,
		Description: "Creates one replay batch through the same service as POST /api/replay_batches, so its confirmation, per-batch maximum " +
			"and five-attempts-per-ticket rules apply unchanged: 422, adding nothing, on a bad confirm_attempts. A ticket that cannot be replayed " +
			"is listed in refused with its reason. 409 for a set with no tickets. 404 feature_disabled-style when the server does not serve replays." + person,
		Errors: []int{http.StatusUnprocessableEntity, http.StatusConflict},
	}, h.Run)
	huma.Register(api, huma.Operation{
		Metadata: authn.RequireUser(), OperationID: "listEvalSetBatches", Method: http.MethodGet, Path: "/api/eval_sets/{id}/batches",
		Summary: "The replay batches run across a set", Description: "Newest first, each with its variants and date." + person,
	}, h.History)
}
