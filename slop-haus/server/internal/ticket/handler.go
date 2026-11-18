package ticket

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// Handler is a thin HTTP wrapper over Service: every rule about what a
// valid ticket transition is lives there (see ticket.go's package doc), so
// this file only translates between huma's request/response structs and
// Service's domain types, plus maps domain errors to HTTP status codes.
type Handler struct {
	svc    *Service
	logger *slog.Logger
	// attemptsAdd is whether AddTicketAttempt is served; off, it answers 404 with
	// FeatureDisabledCode.
	attemptsAdd bool
	// replays is whether addTicketAttempt takes replay: true. Off, a replay
	// request answers like a disabled feature.
	replays bool
	// costs is where a board card's worker cost is read; nil leaves it empty.
	costs AttemptCosts
	// signals is where an attempt's signals are read; nil leaves them null.
	signals AttemptSignals
	// verdicts is where an attempt's verdict is read; nil leaves it null.
	verdicts AttemptVerdicts
	// discussions is where an attempt's open discussion round is read; nil
	// leaves it null.
	discussions AttemptDiscussions
	// attemptChanges closes a closed attempt's change on the code host; nil
	// leaves it open.
	attemptChanges AttemptChangeCloser
	// attemptsPromote is whether PromoteTicketAttempt is served; off, it
	// answers 404 with FeatureDisabledCode.
	attemptsPromote bool
	// promotions checks and carries out a promotion on the code host; nil
	// leaves the changes as they are.
	promotions AttemptPromotionChanges
	// carries tracks promotions being carried to the code host after their
	// response, so a test (or a shutdown) can wait for them.
	carries sync.WaitGroup
}

// promotionCarryTimeout bounds one promotion's trip to the code host. It is
// not tied to the request: the API server's write deadline is shorter than a
// slow host, and the person is answered before the carry starts.
const promotionCarryTimeout = 2 * time.Minute

// WaitForCarries blocks until every promotion carried to the code host after
// its response has finished.
func (h *Handler) WaitForCarries() { h.carries.Wait() }

// AttemptPromotionChanges is what a promotion does on the code host. The
// change package implements it; it imports this one, so the handler takes the
// slice it needs.
type AttemptPromotionChanges interface {
	// CheckPromotable is called before the promotion is made, and refuses
	// (wrapping ErrPromotionBlocked or ErrPromotionUnverified) an attempt whose
	// change is not fit to reach the base branch.
	CheckPromotable(ctx context.Context, tenant uuid.UUID, ticketID int64, attempt int) error
	// ApplyPromotion retargets and retitles the ticket's changes to match
	// which attempt is promoted now (not the one this request promoted, so a
	// request that finishes late cannot undo a later choice). It is called
	// after the commit, with no lock held, and again on a repeated request; a
	// failure leaves the promotion in place.
	ApplyPromotion(ctx context.Context, tenant uuid.UUID, ticketID int64) error
}

// WithAttemptPromotion serves the promote-attempt endpoint and carries a
// promotion to the changes on the code host. Off by default: see
// config.Config.AttemptsPromoteEnabled. changes may be nil.
func WithAttemptPromotion(enabled bool, changes AttemptPromotionChanges) HandlerOption {
	return func(h *Handler) { h.attemptsPromote, h.promotions = enabled, changes }
}

// AttemptChangeCloser closes the change (pull request) an attempt has open.
// The change package implements it; it imports this one, so the handler takes
// the slice it needs.
type AttemptChangeCloser interface {
	CloseAttemptChange(ctx context.Context, tenant uuid.UUID, ticketID int64, attempt int) error
}

// WithAttemptChangeCloser closes the change of an attempt a person closes.
func WithAttemptChangeCloser(c AttemptChangeCloser) HandlerOption {
	return func(h *Handler) { h.attemptChanges = c }
}

// WithAttemptCosts reads the worker cost of the attempt cards the board and
// its pages list.
func WithAttemptCosts(costs AttemptCosts) HandlerOption {
	return func(h *Handler) { h.costs = costs }
}

// WithAttemptSignals serves each attempt's signals on the ticket detail.
func WithAttemptSignals(signals AttemptSignals) HandlerOption {
	return func(h *Handler) { h.signals = signals }
}

// HandlerOption configures a Handler.
type HandlerOption func(*Handler)

// WithAttemptsAdd serves the add-attempt endpoint. Off by default: see
// config.Config.AttemptsAddEnabled.
func WithAttemptsAdd(enabled bool) HandlerOption {
	return func(h *Handler) { h.attemptsAdd = enabled }
}

// WithReplays lets the add-attempt endpoint add replays to done tickets. Off
// by default, and only meaningful with WithAttemptsAdd.
func WithReplays(enabled bool) HandlerOption {
	return func(h *Handler) { h.replays = enabled }
}

// FeatureDisabledCode is FeatureDisabledBody's code.
const FeatureDisabledCode = "feature_disabled"

// FeatureDisabledBody is the 404 of an endpoint its env var switched off. The
// status stays 404, so a client that predates the code behaves as before; the
// code lets a current one tell it from a ticket or attempt that is not there.
type FeatureDisabledBody struct {
	huma.ErrorModel
	Code string `json:"code" enum:"feature_disabled"`
}

func featureDisabledError() *FeatureDisabledBody {
	return &FeatureDisabledBody{
		ErrorModel: huma.ErrorModel{
			Status: http.StatusNotFound,
			Title:  http.StatusText(http.StatusNotFound),
			Detail: "not found",
		},
		Code: FeatureDisabledCode,
	}
}

func NewHandler(svc *Service, logHandler slog.Handler, opts ...HandlerOption) *Handler {
	h := &Handler{svc: svc, logger: slog.New(logHandler)}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// -- Tickets --

// TicketQuery is the query every ticket list takes: Filter, Sort, Page.
// Statuses and Priorities are repeated ("explode"), i.e.
// ?status=open&status=blocked, rather than huma's default comma-separated
// style -- it reads more naturally as a REST filter and matches how a
// browser's URLSearchParams.append would build it.
//
// Clients send an explicit status list: a request with none matches every
// status, and a cursor is bound to the filter it was issued for, so the
// list must not depend on a default the server could change.
type TicketQuery struct {
	FilterQuery
	// Sort is one of SortNames; empty is the list's default order.
	Sort string `query:"sort"`
	// Limit is the page size: DefaultPageSize when omitted. Every list is
	// bounded; there is no way to ask for all of it.
	Limit int `query:"limit" minimum:"1" maximum:"200"`
	// Cursor is a previous response's next_cursor, valid only for the
	// same filter and sort.
	Cursor string `query:"cursor"`
	// Summary leaves each ticket's body out; GET /tickets/{id} has it all.
	Summary bool `query:"summary"`
}

// FilterQuery is the part of the query that says which tickets: the filter
// widget's fields. Lists of other things that hang off tickets (LLM usage by
// ticket) take it too, so a ticket is filtered the same way wherever it is
// listed.
type FilterQuery struct {
	Statuses   []string `query:"status,explode"`
	Priorities []string `query:"priority,explode"`
	// Q is the text filter; see ListFilter.Text. Its length limit is the
	// service's (MaxSearchTextLen), not a schema maxLength, so an MCP caller
	// of the same service gets the same limit.
	Q string `query:"q"`
	// The dates are UTC days, both ends inclusive.
	ClosedFrom  string `query:"closed_from" format:"date"`
	ClosedTo    string `query:"closed_to" format:"date"`
	CreatedFrom string `query:"created_from" format:"date"`
	CreatedTo   string `query:"created_to" format:"date"`
	UpdatedFrom string `query:"updated_from" format:"date"`
	UpdatedTo   string `query:"updated_to" format:"date"`
}

func (q *TicketQuery) filter() (*ListFilter, error) {
	f, err := q.FilterQuery.Filter()
	if err != nil {
		return nil, err
	}
	f.Limit, f.Summary = q.Limit, q.Summary
	if q.Sort != "" {
		if f.Sort, err = SortByName(q.Sort); err != nil {
			return nil, err
		}
	}
	return f, nil
}

func (q *FilterQuery) Filter() (*ListFilter, error) {
	filter := &ListFilter{Text: q.Q}
	for _, s := range q.Statuses {
		filter.Statuses = append(filter.Statuses, Status(s))
	}
	for _, p := range q.Priorities {
		filter.Priorities = append(filter.Priorities, model.Priority(p))
	}
	day := func(s string) (*time.Time, error) {
		if s == "" {
			return nil, nil
		}
		t, err := time.Parse(time.DateOnly, s)
		if err != nil {
			return nil, fmt.Errorf("%w: %q is not a date", ErrInvalidPage, s)
		}
		return &t, nil
	}
	// to is a whole day, so the range ends at the start of the next.
	for _, r := range []struct {
		from, to string
		into     *TimeRange
	}{
		{q.ClosedFrom, q.ClosedTo, &filter.Closed},
		{q.CreatedFrom, q.CreatedTo, &filter.Created},
		{q.UpdatedFrom, q.UpdatedTo, &filter.Updated},
	} {
		var err error
		if r.into.From, err = day(r.from); err != nil {
			return nil, err
		}
		to, err := day(r.to)
		if err != nil {
			return nil, err
		}
		if to != nil {
			end := to.AddDate(0, 0, 1)
			r.into.Before = &end
		}
	}
	return filter, nil
}

type ListTicketsRequest struct {
	TicketQuery
	// AwaitingPostReview narrows the list to done tickets nobody has looked
	// at since they merged unapproved; see ListFilter.
	AwaitingPostReview bool `query:"awaiting_post_review"`
}

// Body is an object wrapping the array rather than a bare top-level
// array: a JSON array has nowhere to put a cursor or a count without
// breaking every existing client.
type ListTicketsResponse struct {
	Body struct {
		Tickets []TicketBody `json:"tickets"`
		// NextCursor is set when more follows.
		NextCursor string `json:"next_cursor,omitempty"`
	}
}

func (h *Handler) ListTickets(ctx context.Context, input *ListTicketsRequest) (*ListTicketsResponse, error) {
	filter, err := input.filter()
	if err != nil {
		return nil, h.mapError(err)
	}
	filter.AwaitingPostReview = input.AwaitingPostReview
	if filter.After, err = decodeCursor(input.Cursor); err != nil {
		return nil, h.mapError(err)
	}
	// A column's later pages are the same list as its first, which the board
	// served as attempt cards or as tickets: the cursor says which.
	filter.AttemptCards = filter.After != nil && filter.After.Cards
	page, err := h.svc.ListPage(ctx, filter)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &ListTicketsResponse{}
	resp.Body.Tickets = ticketBodies(page.Tickets)
	if err := fillCosts(ctx, h.costs, resp.Body.Tickets); err != nil {
		return nil, h.mapError(err)
	}
	if page.Next != nil {
		resp.Body.NextCursor = page.Next.Encode()
	}
	return resp, nil
}

func decodeCursor(s string) (*PageCursor, error) {
	if s == "" {
		return nil, nil
	}
	return DecodePageCursor(s)
}

// BoardRequest is the list query less its cursor: the board is the first
// page of every column, and a column is paged with ListTickets.
type BoardRequest struct {
	TicketQuery
}

type BoardColumnBody struct {
	Status Status `json:"status"`
	// Count is every matching ticket in the column, not just this page.
	Count   int          `json:"count"`
	Tickets []TicketBody `json:"tickets"`
	// NextCursor loads the column's next page from ListTickets, with this
	// request's filter and sort and status set to just this column's.
	NextCursor string `json:"next_cursor,omitempty"`
}

type BoardResponse struct {
	Body struct {
		Columns []BoardColumnBody `json:"columns"`
	}
}

func (h *Handler) GetBoard(ctx context.Context, input *BoardRequest) (*BoardResponse, error) {
	if input.Cursor != "" {
		return nil, h.mapError(fmt.Errorf("%w: the board has no cursor, page a column", ErrInvalidPage))
	}
	filter, err := input.filter()
	if err != nil {
		return nil, h.mapError(err)
	}
	columns, err := h.svc.ListBoard(ctx, filter)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &BoardResponse{}
	resp.Body.Columns = make([]BoardColumnBody, len(columns))
	for i, c := range columns {
		resp.Body.Columns[i] = BoardColumnBody{Status: c.Status, Count: c.Count, Tickets: ticketBodies(c.Page.Tickets)}
		if c.Page.Next != nil {
			resp.Body.Columns[i].NextCursor = c.Page.Next.Encode()
		}
	}
	// One read of the cost of every card on the board.
	pages := make([][]TicketBody, len(resp.Body.Columns))
	for i := range resp.Body.Columns {
		pages[i] = resp.Body.Columns[i].Tickets
	}
	if err := fillCosts(ctx, h.costs, pages...); err != nil {
		return nil, h.mapError(err)
	}
	return resp, nil
}

// CreateTicketBody carries no creator: this route is authn.RequireUser(),
// so the creator is always the signed-in user making the call, taken from
// the request context rather than trusted from the body. A body field here
// used to let any caller attribute a ticket to an arbitrary agent id -- see
// RegisterRoutes's doc comment on this route's trust boundary.
type CreateTicketBody struct {
	Title           string         `json:"title"`
	Body            string         `json:"body,omitempty"`
	Priority        model.Priority `json:"priority,omitempty"`
	Paths           []PathInput    `json:"paths,omitempty"`
	NeedsDiscussion bool           `json:"needs_discussion,omitempty"`
	// RequiresApproval holds the ticket's PR for a human's approval.
	RequiresApproval bool `json:"requires_approval,omitempty"`
	// Status is preparing or open; omitted files it open, as this route
	// always has. The board's two create buttons always send it.
	Status string `json:"status,omitempty" enum:"preparing,open"`
	// RoleIDs are roles the claiming agent will need; RoleMatch says
	// whether any one (the default) or all of them.
	RoleIDs   []string `json:"role_ids,omitempty" maxItems:"100"`
	RoleMatch string   `json:"role_match,omitempty" enum:"any,all"`
	// BaseRef is a commit hash (full or short) or a tag the work starts from
	// instead of the default branch's head. A branch is refused: a base must
	// not move.
	BaseRef string `json:"base_ref,omitempty" maxLength:"256"`
	// BaseTag false leaves out the tag that keeps a commit-hash base
	// reachable; it defaults to true.
	BaseTag *bool `json:"base_tag,omitempty"`
}

type CreateTicketRequest struct {
	Body CreateTicketBody
}

type TicketResponse struct {
	Body TicketBody
}

func (h *Handler) CreateTicket(ctx context.Context, input *CreateTicketRequest) (*TicketResponse, error) {
	user, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("ticket route is missing its auth middleware")
	}

	params := &CreateParams{
		Title:            input.Body.Title,
		Body:             input.Body.Body,
		Priority:         input.Body.Priority,
		CreatedByUserID:  &user.ID,
		NeedsDiscussion:  input.Body.NeedsDiscussion,
		RequiresApproval: input.Body.RequiresApproval,
		Status:           Status(input.Body.Status),
		BaseRef:          input.Body.BaseRef,
		SkipBaseTag:      input.Body.BaseTag != nil && !*input.Body.BaseTag,
	}
	params.Roles, err = roleRequirementFrom(input.Body.RoleIDs, input.Body.RoleMatch)
	if err != nil {
		return nil, err
	}
	for _, p := range input.Body.Paths {
		params.Paths = append(params.Paths, pathFromInput(p))
	}

	t, err := h.svc.Create(ctx, params)
	if err != nil {
		return nil, h.mapError(err)
	}
	return h.withScopeOverlaps(ctx, t)
}

func roleRequirementFrom(rawIDs []string, match string) (RoleRequirement, error) {
	req := RoleRequirement{Match: model.RoleMatch(match)}
	for _, raw := range rawIDs {
		id, err := uuid.Parse(raw)
		if err != nil {
			return RoleRequirement{}, huma.Error422UnprocessableEntity("role ids must be valid uuids")
		}
		req.IDs = append(req.IDs, id)
	}
	return req, nil
}

// SetTicketRolesBody is the whole requirement: role_ids replaces the
// ticket's roles, an empty list clearing them. Its own route rather than a
// PATCH field, because the PATCH's "absent means leave alone" has no way to
// say "clear".
type SetTicketRolesBody struct {
	RoleIDs   []string `json:"role_ids" maxItems:"100"`
	RoleMatch string   `json:"role_match,omitempty" enum:"any,all"`
}

type SetTicketRolesRequest struct {
	ID   int64 `path:"id"`
	Body SetTicketRolesBody
}

// SetTicketRoles replaces the roles a ticket requires. It can loosen a
// restriction, which only a person may; the service refuses anyone else.
func (h *Handler) SetTicketRoles(ctx context.Context, input *SetTicketRolesRequest) (*TicketResponse, error) {
	req, err := roleRequirementFrom(input.Body.RoleIDs, input.Body.RoleMatch)
	if err != nil {
		return nil, err
	}
	t, err := h.svc.SetRequiredRoles(ctx, authn.PersonFromContext(ctx), input.ID, &req)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &TicketResponse{Body: ticketToBody(*t)}, nil
}

type GetTicketRequest struct {
	ID int64 `path:"id"`
}

func (h *Handler) GetTicket(ctx context.Context, input *GetTicketRequest) (*TicketResponse, error) {
	t, err := h.svc.Get(ctx, input.ID)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp, err := h.withScopeOverlaps(ctx, t)
	if err != nil {
		return nil, err
	}
	attempts, err := h.svc.Attempts(ctx, input.ID)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp.Body.Attempts = attemptsToBody(attempts)
	if err := fillAttemptCosts(ctx, h.costs, input.ID, resp.Body.Attempts); err != nil {
		return nil, h.mapError(err)
	}
	if err := fillAttemptSignals(ctx, h.signals, input.ID, resp.Body.Attempts); err != nil {
		return nil, h.mapError(err)
	}
	if authn.PersonFromContext(ctx) != nil {
		if err := fillAttemptVerdicts(ctx, h.verdicts, input.ID, resp.Body.Attempts); err != nil {
			return nil, h.mapError(err)
		}
		if err := fillAttemptDiscussions(ctx, h.discussions, input.ID, resp.Body.Attempts); err != nil {
			return nil, h.mapError(err)
		}
	}
	return resp, nil
}

type SetTicketVariantBody struct {
	// VersionID is the variant version to route the ticket's attempt to; null
	// clears it, so only agents in no variant claim it.
	VersionID *string `json:"version_id" doc:"A variant version id; null for none."`
}

type SetTicketVariantRequest struct {
	ID   int64 `path:"id"`
	Body SetTicketVariantBody
}

// parseVersionID reads an optional variant version id from a request body.
func parseVersionID(raw *string) (*uuid.UUID, error) {
	if raw == nil {
		return nil, nil
	}
	id, err := uuid.Parse(*raw)
	if err != nil {
		return nil, huma.Error422UnprocessableEntity("version_id must be a uuid")
	}
	return &id, nil
}

// SetTicketVariant routes the ticket's promoted (else only) attempt to a
// variant version. A ticket with several attempts and none promoted names the
// attempt with SetTicketAttemptVariant.
func (h *Handler) SetTicketVariant(ctx context.Context, input *SetTicketVariantRequest) (*TicketResponse, error) {
	user, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("ticket route is missing its auth middleware")
	}
	versionID, err := parseVersionID(input.Body.VersionID)
	if err != nil {
		return nil, err
	}
	if _, err := h.svc.SetAttemptVariant(ctx, user.ID, input.ID, versionID); err != nil {
		return nil, h.mapError(err)
	}
	return h.GetTicket(ctx, &GetTicketRequest{ID: input.ID})
}

type SetTicketAttemptVariantRequest struct {
	ID      int64 `path:"id"`
	Attempt int   `path:"n" minimum:"1" doc:"The attempt's number."`
	Body    SetTicketVariantBody
}

// SetTicketAttemptVariant routes the named attempt to a variant version.
func (h *Handler) SetTicketAttemptVariant(ctx context.Context, input *SetTicketAttemptVariantRequest) (*TicketResponse, error) {
	user, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("ticket route is missing its auth middleware")
	}
	versionID, err := parseVersionID(input.Body.VersionID)
	if err != nil {
		return nil, err
	}
	if _, err := h.svc.SetAttemptVariantAt(ctx, user.ID, input.ID, input.Attempt, versionID); err != nil {
		return nil, h.mapError(err)
	}
	return h.GetTicket(ctx, &GetTicketRequest{ID: input.ID})
}

// AddAttemptRow is one attempt of a batch.
type AddAttemptRow struct {
	VersionID *string `json:"version_id,omitempty" doc:"A variant version id; absent or null for none."`
}

type AddTicketAttemptBody struct {
	// VersionID is the variant version to route the new attempt to; absent or
	// null for none.
	VersionID *string `json:"version_id,omitempty" doc:"A variant version id; absent or null for none."`
	// Attempts adds several attempts, all or none, instead of VersionID's one.
	Attempts []AddAttemptRow `json:"attempts,omitempty" doc:"Several attempts to add together, all or none: one to five rows. Not with version_id."`
	// Replay adds the attempts as replays of a done ticket.
	Replay bool `json:"replay,omitempty" doc:"Add the attempts as replays of a done ticket: shadows from the original base commit that see the ticket as first given, never promoted or merged. Required for a done ticket, refused for any other."`
}

type AddTicketAttemptRequest struct {
	ID   int64 `path:"id"`
	Body AddTicketAttemptBody
}

// AddTicketAttempt adds an attempt, or a batch of them, to the ticket and
// returns the ticket.
func (h *Handler) AddTicketAttempt(ctx context.Context, input *AddTicketAttemptRequest) (*TicketResponse, error) {
	if !h.attemptsAdd {
		return nil, featureDisabledError()
	}
	user, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("ticket route is missing its auth middleware")
	}
	rows := input.Body.Attempts
	if rows == nil {
		rows = []AddAttemptRow{{VersionID: input.Body.VersionID}}
	} else if input.Body.VersionID != nil {
		return nil, huma.Error422UnprocessableEntity("give version_id or attempts, not both")
	}
	versionIDs := make([]*uuid.UUID, len(rows))
	for i := range rows {
		if versionIDs[i], err = parseVersionID(rows[i].VersionID); err != nil {
			return nil, err
		}
	}
	add := h.svc.AddAttempts
	if input.Body.Replay {
		if !h.replays {
			return nil, featureDisabledError()
		}
		add = h.svc.AddReplays
	}
	if _, err := add(ctx, user.ID, input.ID, versionIDs); err != nil {
		return nil, h.mapError(err)
	}
	return h.GetTicket(ctx, &GetTicketRequest{ID: input.ID})
}

type CloseTicketAttemptRequest struct {
	ID      int64 `path:"id"`
	Attempt int   `path:"n" minimum:"1"`
}

// CloseTicketAttempt closes an attempt of the ticket and returns the ticket.
func (h *Handler) CloseTicketAttempt(ctx context.Context, input *CloseTicketAttemptRequest) (*TicketResponse, error) {
	if !h.attemptsAdd {
		return nil, featureDisabledError()
	}
	user, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("ticket route is missing its auth middleware")
	}
	if _, err := h.svc.CloseAttempt(ctx, user.ID, input.ID, input.Attempt); err != nil {
		return nil, h.mapError(err)
	}
	// The attempt is closed whatever the host says: a failure is audited by
	// the closer and the change is left for a person to close.
	if h.attemptChanges != nil {
		if err := h.attemptChanges.CloseAttemptChange(ctx, user.TenantID, input.ID, input.Attempt); err != nil {
			h.logger.WarnContext(ctx, "close the change of a closed attempt", "ticket", input.ID, "attempt", input.Attempt, "err", err)
		}
	}
	return h.GetTicket(ctx, &GetTicketRequest{ID: input.ID})
}

type PromoteTicketAttemptRequest struct {
	ID      int64 `path:"id"`
	Attempt int   `path:"n" minimum:"1"`
}

// PromoteTicketAttempt promotes an attempt of the ticket and returns the
// ticket.
func (h *Handler) PromoteTicketAttempt(ctx context.Context, input *PromoteTicketAttemptRequest) (*TicketResponse, error) {
	// Off, the route is indistinguishable from one that does not exist.
	if !h.attemptsPromote {
		return nil, featureDisabledError()
	}
	user, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("ticket route is missing its auth middleware")
	}
	// Checked before the commit: once promoted, the attempt's change targets
	// the base branch, and a head that took in main must not be let there.
	if h.promotions != nil {
		if err := h.promotions.CheckPromotable(ctx, user.TenantID, input.ID, input.Attempt); err != nil {
			return nil, h.mapError(err)
		}
	}
	if _, err := h.svc.PromoteAttempt(ctx, user.ID, input.ID, input.Attempt); err != nil {
		return nil, h.mapError(err)
	}
	// The promotion stands whatever the host says, so the person is answered
	// now and the pull requests follow: the host's calls can outlast the API
	// server's write deadline, which would drop the response of a promotion
	// that happened. A failure is audited by the changes and the pull requests
	// are left as they were; a repeated Promote carries them again.
	if h.promotions != nil {
		h.carryPromotion(ctx, user.TenantID, input.ID, input.Attempt)
	}
	return h.GetTicket(ctx, &GetTicketRequest{ID: input.ID})
}

func (h *Handler) carryPromotion(ctx context.Context, tenant uuid.UUID, ticketID int64, attempt int) {
	// The request's values (trace, logger fields) are kept; its cancellation,
	// which comes with the response, is not.
	carryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), promotionCarryTimeout)
	h.carries.Add(1)
	go func() {
		defer h.carries.Done()
		defer cancel()
		if err := h.promotions.ApplyPromotion(carryCtx, tenant, ticketID); err != nil {
			h.logger.WarnContext(carryCtx, "carry a promotion to its changes", "ticket", ticketID, "attempt", attempt, "err", err)
		}
	}()
}

// withScopeOverlaps is the ticket's body with the overlaps a human filing or
// editing its scopes should see. They are feedback: failing to compute them
// never fails the read or the edit already made.
func (h *Handler) withScopeOverlaps(ctx context.Context, t *Ticket) (*TicketResponse, error) {
	body := ticketToBody(*t)
	if t.Status == StatusPreparing || t.Status == StatusOpen {
		overlaps, err := h.svc.ScopeOverlaps(ctx, t.ID)
		if err != nil {
			h.logger.WarnContext(ctx, "compute scope overlaps", "ticket", t.ID, "err", err)
		} else {
			body.ScopeOverlaps = overlapsToBody(overlaps)
		}
	}
	return &TicketResponse{Body: body}, nil
}

// UpdateTicketBody carries only the fields a caller wants changed; a nil
// pointer (or an absent paths field) means "leave alone", mirroring
// Service.UpdateParams -- see its doc comment for why. Title and body are
// edited through CreateTicketRevision instead, which keeps their history.
type UpdateTicketBody struct {
	Priority *model.Priority `json:"priority,omitempty"`
	Status   *Status         `json:"status,omitempty"`
	Paths    []PathInput     `json:"paths,omitempty"`
	// NeedsDiscussion sends a ticket back for a planning pass, or drops the
	// request for one.
	NeedsDiscussion *bool `json:"needs_discussion,omitempty"`
	// RequiresApproval turns the human approval gate on or off.
	RequiresApproval *bool `json:"requires_approval,omitempty"`
	// BaseRef re-points the base of a ticket nobody has claimed yet; an empty
	// string clears it back to the default branch's head.
	BaseRef *string `json:"base_ref,omitempty" maxLength:"256"`
}

type UpdateTicketRequest struct {
	ID   int64 `path:"id"`
	Body UpdateTicketBody
}

func (h *Handler) UpdateTicket(ctx context.Context, input *UpdateTicketRequest) (*TicketResponse, error) {
	params := &UpdateParams{
		Priority:         input.Body.Priority,
		Status:           input.Body.Status,
		NeedsDiscussion:  input.Body.NeedsDiscussion,
		RequiresApproval: input.Body.RequiresApproval,
		BaseRef:          input.Body.BaseRef,
	}
	if input.Body.Paths != nil {
		params.Paths = make([]model.Path, 0, len(input.Body.Paths))
		for _, p := range input.Body.Paths {
			params.Paths = append(params.Paths, pathFromInput(p))
		}
	}

	t, err := h.svc.Update(byUser(ctx), input.ID, params)
	if err != nil {
		return nil, h.mapError(err)
	}
	return h.withScopeOverlaps(ctx, t)
}

type MarkTicketReadyRequest struct {
	ID int64 `path:"id"`
}

// MarkTicketReady is the human's Ready on a Preparing ticket. A route of
// its own rather than a status in the PATCH body, because where the ticket
// lands -- open, or blocked on unfinished dependencies -- is the server's to
// decide, not the caller's.
func (h *Handler) MarkTicketReady(ctx context.Context, input *MarkTicketReadyRequest) (*TicketResponse, error) {
	t, err := h.svc.MarkReady(byUser(ctx), input.ID)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &TicketResponse{Body: ticketToBody(*t)}, nil
}

// -- Ticket comments --

type ListTicketCommentsRequest struct {
	ID int64 `path:"id"`
}

type ListTicketCommentsResponse struct {
	Body struct {
		Comments []CommentBody `json:"comments"`
	}
}

func (h *Handler) ListTicketComments(ctx context.Context, input *ListTicketCommentsRequest) (*ListTicketCommentsResponse, error) {
	comments, err := h.svc.ListComments(ctx, input.ID)
	if err != nil {
		return nil, h.mapError(err)
	}
	body := make([]CommentBody, len(comments))
	for i, c := range comments {
		body[i] = commentToBody(c)
	}
	resp := &ListTicketCommentsResponse{}
	resp.Body.Comments = body
	return resp, nil
}

// CreateTicketCommentBody carries no author: this route is
// authn.RequireUser(), so the author is always the signed-in user making
// the call. A body-supplied author_agent_id used to let any caller
// impersonate an agent's comment -- see RegisterRoutes's doc comment.
type CreateTicketCommentBody struct {
	Body string `json:"body"`
	// NeedsDiscussion, when present, is written to the ticket with the
	// comment in one transaction -- see Service.CommentSettingNeedsDiscussion.
	// On the comment rather than a PATCH beside it because two requests
	// leave a window for an agent to claim the ticket between them.
	NeedsDiscussion *bool `json:"needs_discussion,omitempty"`
}

type CreateTicketCommentRequest struct {
	ID int64 `path:"id"`
	// Attempt addresses the comment to one attempt. Left out, a comment on a
	// ticket with several live attempts goes to all of them (and answers no
	// attempt's discussion).
	Attempt int `query:"attempt" required:"false" minimum:"0" doc:"the attempt the comment is addressed to; 0 or absent names none: with several live attempts the comment goes to every one of them, and answers no attempt's discussion"`
	Body    CreateTicketCommentBody
}

// CreatedCommentBody is the comment, and the ticket as the same write left
// it when needs_discussion was sent -- so the board shows what happened
// (a requeue, a withdrawn agent) rather than guessing it.
type CreatedCommentBody struct {
	CommentBody
	Ticket *TicketBody `json:"ticket,omitempty"`
}

type CreateTicketCommentResponse struct {
	Body CreatedCommentBody
}

func (h *Handler) CreateTicketComment(ctx context.Context, input *CreateTicketCommentRequest) (*CreateTicketCommentResponse, error) {
	user, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("ticket route is missing its auth middleware")
	}

	addressed, err := h.addressedCommentAttempt(ctx, input.ID, input.Attempt)
	if err != nil {
		return nil, err
	}
	result, err := h.svc.PostComment(ctx, &HumanComment{
		TicketID: input.ID, UserID: user.ID, Body: input.Body.Body,
		NeedsDiscussion: input.Body.NeedsDiscussion, Attempt: addressed,
	})
	if err != nil {
		return nil, h.mapError(err)
	}
	if input.Body.NeedsDiscussion == nil {
		return &CreateTicketCommentResponse{Body: CreatedCommentBody{CommentBody: commentToBody(*result.Comment)}}, nil
	}
	t := ticketToBody(*result.Ticket)
	return &CreateTicketCommentResponse{Body: CreatedCommentBody{CommentBody: commentToBody(*result.Comment), Ticket: &t}}, nil
}

// AgentContextBody is a handoff on the wire.
type AgentContextBody struct {
	ID            int64     `json:"id"`
	AgentID       string    `json:"agent_id"`
	Body          string    `json:"body"`
	CreatedAt     time.Time `json:"created_at"`
	AttemptNumber int64     `json:"attempt_number"`
}

type ListAgentContextsRequest struct {
	ID int64 `path:"id"`
}

type ListAgentContextsResponse struct {
	Body struct {
		Contexts []AgentContextBody `json:"contexts"`
	}
}

// ListAgentContexts serves a ticket's handoffs, oldest first. Read-only:
// only an agent entering a discussion writes one (Service.Discuss), which
// is what keeps a handoff trustworthy as the agent's own account.
func (h *Handler) ListAgentContexts(ctx context.Context, input *ListAgentContextsRequest) (*ListAgentContextsResponse, error) {
	contexts, err := h.svc.ListAgentContexts(ctx, input.ID)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &ListAgentContextsResponse{}
	resp.Body.Contexts = make([]AgentContextBody, len(contexts))
	for i, c := range contexts {
		resp.Body.Contexts[i] = AgentContextBody{ID: c.ID, AgentID: c.AgentID.String(), Body: c.Body, CreatedAt: c.CreatedAt, AttemptNumber: c.AttemptNumber}
	}
	return resp, nil
}
