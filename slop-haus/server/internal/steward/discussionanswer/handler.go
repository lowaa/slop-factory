package discussionanswer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionlabel"
)

// ErrNotOwner is a caller who is not a tenant owner.
var ErrNotOwner = errors.New("discussionanswer: only a tenant owner can answer a discussion with the steward")

var (
	// ErrNotScheduled is vetoing a label the steward is not about to apply.
	ErrNotScheduled = errors.New("discussionanswer: the steward is not applying this answer")
	// ErrWindowClosed is vetoing after the window ran out or the answer was decided.
	ErrWindowClosed = errors.New("discussionanswer: the veto window has closed")
)

// ApplyBadge is what the steward's own applying of a label says on the draft.
type ApplyBadge struct {
	// State is "", "applied", "scheduled" or "vetoed".
	State   string
	At      *time.Time
	CanVeto bool
	CanUndo bool
	Reason  string
}

// Applying is the steward's own posting of routine answers, which the draft
// shows and the owner may veto; discussionapply.Service is one.
type Applying interface {
	DraftBadge(ctx context.Context, rec *discussionlabel.Record) (*ApplyBadge, error)
	Veto(ctx context.Context, r *Round, actor string) (*Decision, error)
}

// Answers is what the routes ask of the service; *Service is one.
type Answers interface {
	Draft(ctx context.Context, r *Round) (*Draft, error)
	AssessNow(ctx context.Context, r *Round) (*discussionlabel.Record, error)
	Approve(ctx context.Context, r *Round, actor string) (*Decision, error)
	Edit(ctx context.Context, r *Round, text, actor string) (*Decision, error)
	Reject(ctx context.Context, r *Round, reason, actor string) (*Decision, error)
}

// Roles says who owns a tenant.
type Roles interface {
	IsTenantOwner(ctx context.Context, userID, tenantID uuid.UUID) (bool, error)
}

// Handler serves the owner's view of a discussion round's draft and the
// decisions on it.
type Handler struct {
	answers  Answers
	roles    Roles
	applying Applying
	logger   *slog.Logger
}

// WithApplying serves the steward-apply badge and the veto route.
func (h *Handler) WithApplying(a Applying) *Handler {
	h.applying = a
	return h
}

func NewHandler(answers Answers, roles Roles, logHandler slog.Handler) *Handler {
	return &Handler{answers: answers, roles: roles, logger: slog.New(logHandler)}
}

var errTable = httperr.Table{
	Rules: []httperr.Rule{
		{Err: authn.ErrNotPerson, Kind: httperr.Forbidden, Message: "only a signed-in person can answer a discussion"},
		{Err: ErrNotOwner, Kind: httperr.Forbidden},
		{Err: discussionlabel.ErrNotFound, Kind: httperr.NotFound, Message: "no steward label for that discussion round"},
		{Err: ErrNotRoutine, Kind: httperr.Invalid},
		{Err: ErrEmpty, Kind: httperr.Invalid},
		{Err: ErrNoDraft, Kind: httperr.Conflict},
		{Err: ErrStale, Kind: httperr.Conflict},
		{Err: ErrChecksFailed, Kind: httperr.Conflict},
		{Err: ErrAlreadyDecided, Kind: httperr.Conflict},
		{Err: ErrNotScheduled, Kind: httperr.Conflict},
		{Err: ErrWindowClosed, Kind: httperr.Conflict},
		{Err: ErrOff, Kind: httperr.Conflict},
		{Err: discussionlabel.ErrNotAssessable, Kind: httperr.Conflict},
		{Err: discussionlabel.ErrDailyCap, Kind: httperr.TooMany},
		{Err: ErrUnavailable, Kind: httperr.Unavailable},
	},
	Unexpected: "unexpected discussion answer error",
}

func (h *Handler) mapError(err error) error { return httperr.Map(h.logger, err, &errTable) }

// RegisterRoutes mounts the discussion answer routes. Persons only, and
// owners only: the answer is posted as the owner.
func RegisterRoutes(api huma.API, h *Handler) {
	const base = "/api/tickets/{ticket_id}/attempts/{attempt}/discussions/{round}/steward"
	huma.Register(api, huma.Operation{
		Metadata: authn.RequireUser(), OperationID: "getDiscussionDraft", Method: http.MethodGet, Path: base,
		Summary: "The steward's label and draft answer for a discussion round",
	}, h.GetDraft)
	huma.Register(api, huma.Operation{
		Metadata: authn.RequireUser(), OperationID: "assessDiscussionNow", Method: http.MethodPost, Path: base + "/assess",
		Summary:     "Run the steward's classification of a not-assessed discussion round now",
		Description: "409 when the label is not awaiting an assessment; 503 where no classifier is configured.",
	}, h.AssessNow)
	huma.Register(api, huma.Operation{
		Metadata: authn.RequireUser(), OperationID: "approveDiscussionDraft", Method: http.MethodPost, Path: base + "/approve",
		Summary: "Post the steward's draft as the owner's answer",
		Description: "Posts exactly the category's template. 422 when the label is not routine; 409 when the draft is " +
			"stale, its checks no longer pass or it was already decided. Repeating an approval whose post may not have " +
			"landed finishes it instead, even if the ticket has since left discussion.",
	}, h.Approve)
	huma.Register(api, huma.Operation{
		Metadata: authn.RequireUser(), OperationID: "editDiscussionDraft", Method: http.MethodPost, Path: base + "/edit",
		Summary: "Post the owner's own text instead of the steward's draft",
	}, h.Edit)
	huma.Register(api, huma.Operation{
		Metadata: authn.RequireUser(), OperationID: "rejectDiscussionDraft", Method: http.MethodPost, Path: base + "/reject",
		Summary: "Dismiss the steward's draft; nothing is posted",
	}, h.Reject)
	huma.Register(api, huma.Operation{
		Metadata: authn.RequireUser(), OperationID: "vetoDiscussionApply", Method: http.MethodPost, Path: base + "/veto",
		Summary:     "Stop the steward from posting a scheduled answer",
		Description: "Owners only. 409 when the steward is not about to apply this answer or its window has closed. Recorded as a rejection.",
	}, h.Veto)
}

type RoundRequest struct {
	TicketID int64 `path:"ticket_id"`
	Attempt  int   `path:"attempt" minimum:"1"`
	Round    int   `path:"round" minimum:"1"`
}

func (r *RoundRequest) round() *Round {
	return &Round{TicketID: r.TicketID, Attempt: r.Attempt, Round: r.Round}
}

type EditRequest struct {
	RoundRequest
	Body struct {
		Text string `json:"text" minLength:"1" doc:"The answer to post, in place of the draft."`
	}
}

type RejectRequest struct {
	RoundRequest
	Body struct {
		Reason string `json:"reason" minLength:"1"`
	}
}

type DiscussionEvidenceBody struct {
	Check  string `json:"check"`
	OK     bool   `json:"ok"`
	Ref    string `json:"ref"`
	Detail string `json:"detail"`
}

type DiscussionLabelBody struct {
	Label    string                   `json:"label" enum:"pending,routine,yours,not_assessed"`
	Category string                   `json:"category"`
	Reason   string                   `json:"reason"`
	Evidence []DiscussionEvidenceBody `json:"evidence"`
	// TicketRevision and HeadSHA are what the label was made against.
	TicketRevision *int       `json:"ticket_revision" nullable:"true"`
	HeadSHA        *string    `json:"head_sha" nullable:"true"`
	AssessedAt     *time.Time `json:"assessed_at" nullable:"true"`
}

type DiscussionDecisionBody struct {
	Action       string    `json:"action" enum:"approve,edit,reject"`
	OriginalBody string    `json:"original_body"`
	FinalBody    string    `json:"final_body"`
	Reason       string    `json:"reason"`
	CreatedAt    time.Time `json:"created_at"`
}

// StewardApplyBody is the steward applying the answer itself.
type StewardApplyBody struct {
	State string `json:"state" enum:"applied,scheduled,vetoed"`
	// At is when a scheduled answer applies, or when an applied one was posted.
	At      *time.Time `json:"at" nullable:"true"`
	CanVeto bool       `json:"can_veto"`
	// CanUndo is false for discussion answers: a worker may already have read them.
	CanUndo bool `json:"can_undo"`
}

type DiscussionDraftBody struct {
	// StewardApply is set when the steward applies, or applied, this answer.
	StewardApply *StewardApplyBody    `json:"steward_apply,omitempty"`
	Label        *DiscussionLabelBody `json:"label"`
	// Body is the templated answer; empty unless the label is routine.
	Body string `json:"body"`
	// Decision is set once the owner has decided.
	Decision *DiscussionDecisionBody `json:"decision,omitempty"`
}

type DraftResponse struct{ Body DiscussionDraftBody }

type DecisionResponse struct{ Body DiscussionDecisionBody }

func toDiscussionLabelBody(rec *discussionlabel.Record) *DiscussionLabelBody {
	b := &DiscussionLabelBody{
		Label: string(rec.Label), Category: rec.Category, Reason: rec.Reason, Evidence: []DiscussionEvidenceBody{},
		TicketRevision: rec.TicketRevision, HeadSHA: rec.HeadSHA, AssessedAt: rec.AssessedAt,
	}
	for _, e := range rec.Evidence {
		b.Evidence = append(b.Evidence, DiscussionEvidenceBody{Check: e.Check, OK: e.OK, Ref: e.Ref, Detail: e.Detail})
	}
	return b
}

func toDiscussionDecisionBody(d *Decision) DiscussionDecisionBody {
	return DiscussionDecisionBody{Action: string(d.Action), OriginalBody: d.OriginalBody, FinalBody: d.FinalBody, Reason: d.Reason, CreatedAt: d.CreatedAt}
}

// owner is the signed-in tenant owner, or the error that refuses the caller.
func (h *Handler) owner(ctx context.Context) (*authn.User, error) {
	who := authn.PersonFromContext(ctx)
	if err := authn.RequirePerson(who); err != nil {
		return nil, h.mapError(err)
	}
	isOwner, err := h.roles.IsTenantOwner(ctx, who.ID, who.TenantID)
	if err != nil {
		return nil, h.mapError(fmt.Errorf("read role: %w", err))
	}
	if !isOwner {
		return nil, h.mapError(ErrNotOwner)
	}
	return who, nil
}

func (h *Handler) GetDraft(ctx context.Context, in *RoundRequest) (*DraftResponse, error) {
	if _, err := h.owner(ctx); err != nil {
		return nil, err
	}
	d, err := h.answers.Draft(ctx, in.round())
	if err != nil {
		return nil, h.mapError(err)
	}
	out := &DraftResponse{Body: DiscussionDraftBody{Label: toDiscussionLabelBody(d.Label), Body: d.Body}}
	if d.Decision != nil {
		b := toDiscussionDecisionBody(d.Decision)
		out.Body.Decision = &b
	}
	if h.applying != nil {
		badge, err := h.applying.DraftBadge(ctx, d.Label)
		if err != nil {
			return nil, h.mapError(err)
		}
		// A schedule the steward gave up is the owner's again: no badge.
		if badge.State != "" {
			out.Body.StewardApply = &StewardApplyBody{State: badge.State, At: badge.At, CanVeto: badge.CanVeto, CanUndo: badge.CanUndo}
		}
	}
	return out, nil
}

func (h *Handler) AssessNow(ctx context.Context, in *RoundRequest) (*DraftResponse, error) {
	if _, err := h.owner(ctx); err != nil {
		return nil, err
	}
	if _, err := h.answers.AssessNow(ctx, in.round()); err != nil {
		return nil, h.mapError(err)
	}
	return h.GetDraft(ctx, in)
}

func (h *Handler) Approve(ctx context.Context, in *RoundRequest) (*DecisionResponse, error) {
	who, err := h.owner(ctx)
	if err != nil {
		return nil, err
	}
	return h.decided(h.answers.Approve(ctx, in.round(), who.ID.String()))
}

func (h *Handler) Edit(ctx context.Context, in *EditRequest) (*DecisionResponse, error) {
	who, err := h.owner(ctx)
	if err != nil {
		return nil, err
	}
	return h.decided(h.answers.Edit(ctx, in.round(), in.Body.Text, who.ID.String()))
}

func (h *Handler) Reject(ctx context.Context, in *RejectRequest) (*DecisionResponse, error) {
	who, err := h.owner(ctx)
	if err != nil {
		return nil, err
	}
	return h.decided(h.answers.Reject(ctx, in.round(), in.Body.Reason, who.ID.String()))
}

func (h *Handler) decided(d *Decision, err error) (*DecisionResponse, error) {
	if err != nil {
		return nil, h.mapError(err)
	}
	return &DecisionResponse{Body: toDiscussionDecisionBody(d)}, nil
}

func (h *Handler) Veto(ctx context.Context, in *RoundRequest) (*DecisionResponse, error) {
	who, err := h.owner(ctx)
	if err != nil {
		return nil, err
	}
	if h.applying == nil {
		return nil, h.mapError(ErrNotScheduled)
	}
	return h.decided(h.applying.Veto(ctx, in.round(), who.ID.String()))
}
