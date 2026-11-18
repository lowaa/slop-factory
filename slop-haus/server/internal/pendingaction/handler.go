package pendingaction

import (
	"context"
	"log/slog"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Handler is the board's side of proposals: a signed-in person lists them
// and decides on them. There is no agent route, and no handler here reads
// an agent: see RegisterRoutes.
type Handler struct {
	svc    *Service
	logger *slog.Logger
	// controls is the owner's veto and undo of the steward's decisions.
	controls StewardControls
	// drafts applies the steward's drafts (assist mode).
	drafts DraftApplier
}

func NewHandler(svc *Service, logHandler slog.Handler) *Handler {
	return &Handler{svc: svc, logger: slog.New(logHandler)}
}

var errTable = httperr.Table{
	Rules: []httperr.Rule{
		{Err: ErrNotFound, Kind: httperr.NotFound, Message: "no such proposal"},
		{Err: authn.ErrNotPerson, Kind: httperr.Forbidden, Message: "only a signed-in person can decide on a proposal"},
		{Err: ErrNotApprover, Kind: httperr.Forbidden},
		{Err: ErrNotPending, Kind: httperr.Conflict},
		{Err: ErrStale, Kind: httperr.Conflict},
		{Err: ErrVoided, Kind: httperr.Conflict},
		{Err: ErrRefused, Kind: httperr.Conflict},
		{Err: ErrBlocked, Kind: httperr.Conflict},
		{Err: ErrNotRevertable, Kind: httperr.Conflict},
		{Err: ErrInvalid, Kind: httperr.Invalid},
		{Err: ticket.ErrInvalidEnum, Kind: httperr.Invalid},
		{Err: ticket.ErrInvalidSearch, Kind: httperr.Invalid},
		{Err: ticket.ErrInvalidPage, Kind: httperr.Invalid},
	},
	Unexpected: "unexpected pending action error",
}

func (h *Handler) mapError(err error) error {
	return httperr.Map(h.logger, err, &errTable)
}

// -- Wire types --

// ProposalBody is a proposal as the approvals view shows it.
type ProposalBody struct {
	ID string `json:"id"`
	// TicketID is null for a proposal about the workspace rather than a ticket;
	// the ticket fields are then empty.
	TicketID    *int64 `json:"ticket_id" nullable:"true"`
	TicketTitle string `json:"ticket_title"`
	// TicketStatus is the ticket's status now.
	TicketStatus string `json:"ticket_status,omitempty" enum:"preparing,open,blocked,in_progress,discussion,review,done,parked,cancelled"`
	// Subject is what the proposal is about.
	Subject string `json:"subject" enum:"ticket,tenant"`
	// ApproverRole is the least role that may decide it.
	ApproverRole string `json:"approver_role" enum:"member,owner"`
	AgentName    string `json:"agent_name"`
	Kind         string `json:"kind" enum:"discussion_answer,ready,status,review_decision,edit,manifest_edit,close_merged"`
	State        string `json:"state" enum:"pending,approved,rejected,withdrawn,expired,voided,stale,failed"`
	// ActionLine is what approving does, written by the server.
	ActionLine string `json:"action_line"`
	// Text is the proposer's own words -- the comment it wants posted, the
	// reason, or what to address. Untrusted: show it as plain text.
	Text string `json:"text"`
	// Status is where a status proposal moves the ticket.
	Status   string `json:"status,omitempty"`
	Decision string `json:"decision,omitempty" enum:"approve,request_changes"`
	Outcome  string `json:"outcome"`
	// Edit is an edit proposal's change against the ticket as it is now.
	Edit *EditBody `json:"edit,omitempty"`
	// ManifestEdit is a manifest_edit proposal's change against the manifest as it is now.
	ManifestEdit *ManifestEditBody `json:"manifest_edit,omitempty"`
	// CloseMerged is a close_merged proposal's check against the code host now.
	CloseMerged *CloseMergedBody `json:"close_merged,omitempty"`
	// Steward is the steward's verdict, present only when it assessed this
	// proposal.
	Steward *StewardBody `json:"steward,omitempty"`
	// Draft is the steward's pre-drafted decision (assist mode), present only
	// when it made one.
	Draft *DraftBody `json:"draft,omitempty"`
	// StaleReason is set on a pending proposal whose ticket has changed since
	// it was made: approving it would be refused.
	StaleReason     string     `json:"stale_reason"`
	CreatedAt       time.Time  `json:"created_at"`
	ExpiresAt       time.Time  `json:"expires_at"`
	DecidedAt       *time.Time `json:"decided_at"`
	DecidedByUserID *string    `json:"decided_by_user_id"`
	// DecidedByDeleted is true when the person who decided it has had their
	// account deleted: decided_by_user_id is then null.
	DecidedByDeleted bool `json:"decided_by_user_id_deleted,omitempty"`
}

// TextChangeBody is a title or body moving to New. Old is absent once the
// proposal is decided. Both are the proposer's words: show them as text.
type TextChangeBody struct {
	Old *string `json:"old,omitempty"`
	New string  `json:"new"`
	// Diff is the change line by line, for a pending proposal's body.
	Diff []ticket.DiffLineBody `json:"diff,omitempty"`
}

type PriorityChangeBody struct {
	Old *string `json:"old,omitempty" enum:"low,normal,high,urgent"`
	New string  `json:"new" enum:"low,normal,high,urgent"`
}

// EditBody is what approving an edit proposal changes.
type EditBody struct {
	Title    *TextChangeBody     `json:"title,omitempty"`
	Body     *TextChangeBody     `json:"body,omitempty"`
	Priority *PriorityChangeBody `json:"priority,omitempty"`
	// Paths are the ticket's filed paths after the edit; PathsAdded and
	// PathsRemoved are against the ticket as it is now, for a pending
	// proposal.
	Paths        []string                  `json:"paths,omitempty"`
	PathsAdded   []string                  `json:"paths_added,omitempty"`
	PathsRemoved []string                  `json:"paths_removed,omitempty"`
	Overlaps     []ticket.ScopeOverlapBody `json:"overlaps,omitempty"`
	// Open: approving also moves the ticket out of preparing.
	Open bool `json:"open,omitempty"`
}

// ManifestEditBody is what approving a manifest_edit proposal changes.
type ManifestEditBody struct {
	// BaseRevision is the manifest revision the proposal was made against.
	BaseRevision int64 `json:"base_revision"`
	// Repos is the whole manifest the proposal would put in place. The
	// proposer's words (paths, patterns, remotes): show them as text.
	Repos []manifest.Repo `json:"repos"`
	// Changes is what that changes against the manifest as it is now; absent
	// once the proposal is decided.
	Changes []manifest.RepoChange `json:"changes,omitempty"`
}

// StewardBody is the steward's server-verified verdict on a proposal.
type StewardBody struct {
	Category string `json:"category"`
	Tier     string `json:"tier" enum:"auto,veto,human"`
	Verdict  string `json:"verdict" enum:"would_auto,would_veto,needs_human"`
	Mode     string `json:"mode" enum:"shadow,live"`
	// Reason is the steward's own words: show as plain text.
	Reason   string                `json:"reason"`
	Evidence []StewardEvidenceBody `json:"evidence"`
	// VetoMinutes is the veto window of a would_veto verdict.
	VetoMinutes int `json:"veto_minutes,omitempty"`
}

type StewardEvidenceBody struct {
	Check  string `json:"check"`
	OK     bool   `json:"ok"`
	Ref    string `json:"ref,omitempty"`
	Detail string `json:"detail,omitempty"`
}

func toStewardBody(a *AssessmentView) *StewardBody {
	if a == nil {
		return nil
	}
	b := &StewardBody{Category: a.Category, Tier: a.Tier, Verdict: a.Verdict, Mode: a.Mode, Reason: a.Reason, VetoMinutes: a.VetoMinutes, Evidence: []StewardEvidenceBody{}}
	for _, e := range a.Evidence {
		b.Evidence = append(b.Evidence, StewardEvidenceBody{Check: e.Check, OK: e.OK, Ref: e.Ref, Detail: e.Detail})
	}
	return b
}

// CloseMergedBody is what the code host says about the merge a close_merged
// proposal rests on.
type CloseMergedBody struct {
	// Confirmations are what approving accepts: failed checks, an unapproved head.
	Confirmations []string `json:"confirmations"`
	// Problem is why approving would be refused now; empty when it would not.
	Problem string `json:"problem,omitempty"`
}

func toCloseMergedBody(c *CloseMergedView) *CloseMergedBody {
	if c == nil {
		return nil
	}
	return &CloseMergedBody{Confirmations: append([]string{}, c.Confirmations...), Problem: c.Problem}
}

func toManifestEditBody(m *ManifestEditView) *ManifestEditBody {
	if m == nil {
		return nil
	}
	return &ManifestEditBody{BaseRevision: m.BaseRevision, Repos: m.Repos, Changes: m.Changes}
}

func toEditBody(e *EditView) *EditBody {
	if e == nil {
		return nil
	}
	b := &EditBody{Paths: e.Paths, PathsAdded: e.PathsAdded, PathsRemoved: e.PathsRemoved, Open: e.Open}
	if e.Title != nil {
		b.Title = &TextChangeBody{Old: e.Title.Old, New: e.Title.New}
	}
	if e.Body != nil {
		b.Body = &TextChangeBody{Old: e.Body.Old, New: e.Body.New}
		if e.Body.Old != nil {
			for _, line := range ticket.DiffLines(*e.Body.Old, e.Body.New) {
				b.Body.Diff = append(b.Body.Diff, ticket.DiffLineBody{Op: string(line.Op), Text: line.Text})
			}
		}
	}
	if e.Priority != nil {
		b.Priority = &PriorityChangeBody{New: string(e.Priority.New)}
		if e.Priority.Old != nil {
			old := string(*e.Priority.Old)
			b.Priority.Old = &old
		}
	}
	for _, o := range e.Overlaps {
		b.Overlaps = append(b.Overlaps, ticket.ScopeOverlapBody{
			TicketID: o.TicketID, Title: o.Title, Status: o.Status, Path: o.Path, Other: o.Other, Resolve: o.Resolve,
		})
	}
	return b
}

func toBody(v *View) ProposalBody {
	b := ProposalBody{
		ID: v.ID.String(), TicketID: v.TicketID, Subject: string(v.Kind.Spec().Subject), ApproverRole: string(v.Kind.Spec().Approver), TicketTitle: v.TicketTitle, TicketStatus: string(v.TicketStatus),
		AgentName: v.AgentName, Kind: string(v.Kind), State: string(v.State), ActionLine: v.ActionLine,
		Text: v.Payload.Body, Status: string(v.Payload.Status), Decision: string(v.Payload.Decision),
		Outcome: v.Outcome, Edit: toEditBody(v.Edit), ManifestEdit: toManifestEditBody(v.ManifestEdit), CloseMerged: toCloseMergedBody(v.CloseMerged), Steward: toStewardBody(v.Assessment), Draft: toDraftBody(v.Draft), StaleReason: v.StaleReason, CreatedAt: v.CreatedAt, ExpiresAt: v.ExpiresAt, DecidedAt: v.DecidedAt,
		DecidedByDeleted: v.DecidedByDeleted,
	}
	if v.DecidedByUserID != nil {
		id := v.DecidedByUserID.String()
		b.DecidedByUserID = &id
	}
	return b
}

type ProposalResponse struct {
	Body ProposalBody
}

type ProposalIDRequest struct {
	ID string `path:"id"`
}

func parseID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.UUID{}, huma.Error404NotFound("no such proposal")
	}
	return id, nil
}

// parseIDs is nil for none given, so a batch without ids is every pending
// proposal; given ids, even none, name exactly those.
func parseIDs(raw []string) ([]uuid.UUID, error) {
	if raw == nil {
		return nil, nil
	}
	ids := make([]uuid.UUID, len(raw))
	for i, r := range raw {
		id, err := uuid.Parse(r)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("not a proposal id: " + r)
		}
		ids[i] = id
	}
	return ids, nil
}

// -- List --

type ListProposalsRequest struct {
	// State narrows the list; every state when omitted.
	State string `query:"state" enum:"pending,approved,rejected,withdrawn,expired,voided,stale,failed"`
	// Order is newest first unless oldest is asked for, the order to work
	// through pending proposals in. A cursor is valid only for the order it
	// was issued under.
	Order string `query:"order" enum:"newest,oldest" default:"newest"`
	// Limit is the page size.
	Limit int `query:"limit" default:"50" minimum:"1" maximum:"100"`
	// Cursor is a previous response's next_cursor, valid only for the same
	// filter and order.
	Cursor string `query:"cursor"`
	// The ticket filter narrows the list to proposals about the tickets it
	// matches, exactly as it does on the ticket lists.
	ticket.FilterQuery
}

type ProposalListBody struct {
	Proposals []ProposalBody `json:"proposals"`
	// NextCursor is set when more follows.
	NextCursor string `json:"next_cursor,omitempty"`
}

type ListProposalsResponse struct {
	Body ProposalListBody
}

func (h *Handler) ListProposals(ctx context.Context, in *ListProposalsRequest) (*ListProposalsResponse, error) {
	tickets, err := in.FilterQuery.Filter()
	if err != nil {
		return nil, h.mapError(err)
	}
	filter := &ListFilter{Ticket: tickets, Limit: in.Limit, Oldest: in.Order == "oldest"}
	if in.State != "" {
		filter.States = []State{State(in.State)}
	}
	if in.Cursor != "" {
		if filter.After, err = DecodePageCursor(in.Cursor); err != nil {
			return nil, h.mapError(err)
		}
	}
	page, err := h.svc.ViewsPage(ctx, authn.PersonFromContext(ctx), filter)
	if err != nil {
		return nil, h.mapError(err)
	}
	out := ProposalListBody{Proposals: make([]ProposalBody, len(page.Views))}
	for i := range page.Views {
		out.Proposals[i] = toBody(&page.Views[i])
	}
	if page.Next != nil {
		out.NextCursor = page.Next.Encode()
	}
	return &ListProposalsResponse{Body: out}, nil
}

func (h *Handler) GetProposal(ctx context.Context, in *ProposalIDRequest) (*ProposalResponse, error) {
	id, err := parseID(in.ID)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.ViewOf(ctx, authn.PersonFromContext(ctx), id)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &ProposalResponse{Body: toBody(v)}, nil
}

// -- Decide --

// ApproveProposal takes nothing from the request but the proposal's id: what
// is applied is what the proposal stored.
func (h *Handler) ApproveProposal(ctx context.Context, in *ProposalIDRequest) (*ProposalResponse, error) {
	who := authn.PersonFromContext(ctx)
	id, err := parseID(in.ID)
	if err != nil {
		return nil, err
	}
	if _, err := h.svc.Approve(ctx, who, id); err != nil {
		return nil, h.mapError(err)
	}
	v, err := h.svc.ViewOf(ctx, authn.PersonFromContext(ctx), id)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &ProposalResponse{Body: toBody(v)}, nil
}

type ReasonBody struct {
	// Reason is shown to the console that proposed it.
	Reason string `json:"reason,omitempty" maxLength:"1000"`
}

type RejectProposalRequest struct {
	ID   string `path:"id"`
	Body ReasonBody
}

func (h *Handler) RejectProposal(ctx context.Context, in *RejectProposalRequest) (*ProposalResponse, error) {
	who := authn.PersonFromContext(ctx)
	id, err := parseID(in.ID)
	if err != nil {
		return nil, err
	}
	if _, err := h.svc.Reject(ctx, who, id, in.Body.Reason); err != nil {
		return nil, h.mapError(err)
	}
	v, err := h.svc.ViewOf(ctx, authn.PersonFromContext(ctx), id)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &ProposalResponse{Body: toBody(v)}, nil
}

// ResultBody is how one proposal of a batch ended.
type ResultBody struct {
	ID      string `json:"id"`
	State   string `json:"state" enum:"pending,approved,rejected,withdrawn,expired,voided,stale,failed"`
	Outcome string `json:"outcome"`
	// Error is why it was refused, empty when it was applied.
	Error string `json:"error"`
}

type BatchBody struct {
	Results []ResultBody `json:"results"`
}

type BatchResponse struct {
	Body BatchBody
}

type ApproveAllBody struct {
	// Kind is the one kind to approve: never mixed.
	Kind string `json:"kind" enum:"ready,discussion_answer"`
	// IDs narrows the batch to these proposals, so a person approving what a
	// filtered list shows decides nothing it hides. Absent is every pending
	// proposal of the kind.
	IDs []string `json:"ids,omitempty" maxItems:"100"`
}

type ApproveAllRequest struct {
	Body ApproveAllBody
}

func (h *Handler) ApproveAll(ctx context.Context, in *ApproveAllRequest) (*BatchResponse, error) {
	who := authn.PersonFromContext(ctx)
	ids, err := parseIDs(in.Body.IDs)
	if err != nil {
		return nil, err
	}
	results, err := h.svc.ApproveAll(ctx, who, Kind(in.Body.Kind), ids)
	if err != nil {
		return nil, h.mapError(err)
	}
	out := BatchBody{Results: make([]ResultBody, len(results))}
	for i, r := range results {
		out.Results[i] = ResultBody{ID: r.ID.String(), State: string(r.State), Outcome: r.Outcome}
		if r.Err != nil {
			out.Results[i].Error = r.Err.Error()
		}
	}
	return &BatchResponse{Body: out}, nil
}

type RejectAllBody struct {
	ReasonBody
	// IDs narrows the batch to these proposals; see ApproveAllBody.
	IDs []string `json:"ids,omitempty" maxItems:"100"`
}

type RejectAllRequest struct {
	Body RejectAllBody
}

func (h *Handler) RejectAll(ctx context.Context, in *RejectAllRequest) (*BatchResponse, error) {
	who := authn.PersonFromContext(ctx)
	ids, err := parseIDs(in.Body.IDs)
	if err != nil {
		return nil, err
	}
	rejected, err := h.svc.RejectAll(ctx, who, ids, in.Body.Reason)
	if err != nil {
		return nil, h.mapError(err)
	}
	out := BatchBody{Results: make([]ResultBody, len(rejected))}
	for i := range rejected {
		out.Results[i] = ResultBody{ID: rejected[i].ID.String(), State: string(rejected[i].State), Outcome: rejected[i].Outcome}
	}
	return &BatchResponse{Body: out}, nil
}
