package pendingaction

import (
	"context"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// DraftView is the decision the steward pre-drafted for a pending proposal
// (assist mode), as the approvals view and the proposing console show it.
// Strings, so this package need not import the steward's.
type DraftView struct {
	Category string
	// Body is the answer, the review notes or a summary of the edit: the
	// steward's own words, untrusted.
	Body string
	// WouldSendBack is a review the steward would request changes on: notes
	// only, never applied.
	WouldSendBack bool
	Reason        string
	// Evidence is every check as it stood when the draft was made.
	Evidence []AssessmentEvidence
	// TicketRevision and HeadSHA are what the draft was read against.
	TicketRevision int
	HeadSHA        string
	// Applied is true once the owner applied it.
	Applied bool
}

// Drafts reads the steward's drafts; a proposal it never drafted is absent
// from the result.
type Drafts interface {
	Drafts(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]*DraftView, error)
}

// WithDrafts shows the steward's draft on each proposal it has one for. A
// failure to read them leaves the views without: a draft is advice.
func WithDrafts(d Drafts) Option {
	return func(s *Service) { s.drafts = d }
}

// DraftApplier applies the steward's draft of a proposal as the owner. An
// implementation answers with this package's errors: ErrNotFound (no draft),
// ErrNotApprover (not an owner), ErrNotPending, ErrRefused (the draft is out
// of date, or assist is off) and ErrInvalid (a draft that is notes only).
type DraftApplier interface {
	ApplyDraft(ctx context.Context, owner *authn.User, proposalID uuid.UUID) error
}

// WithDraftApplier serves the apply-draft route. Without it the route
// answers with a server error.
func (h *Handler) WithDraftApplier(a DraftApplier) *Handler {
	h.drafts = a
	return h
}

// ApplyStewardDraft is the owner's click on the steward's draft.
func (h *Handler) ApplyStewardDraft(ctx context.Context, in *ProposalIDRequest) (*ProposalResponse, error) {
	who := authn.PersonFromContext(ctx)
	id, err := parseID(in.ID)
	if err != nil {
		return nil, err
	}
	if h.drafts == nil {
		return nil, h.mapError(ErrNotConfigured)
	}
	if err := h.drafts.ApplyDraft(ctx, who, id); err != nil {
		return nil, h.mapError(err)
	}
	v, err := h.svc.ViewOf(ctx, who, id)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &ProposalResponse{Body: toBody(v)}, nil
}

// DraftBody is the steward's draft on a proposal.
type DraftBody struct {
	Category string `json:"category"`
	// Body is the steward's own words: show as plain text.
	Body          string                `json:"body"`
	WouldSendBack bool                  `json:"would_send_back"`
	Reason        string                `json:"reason"`
	Evidence      []StewardEvidenceBody `json:"evidence"`
	// Revision and HeadSHA are what the draft was read against.
	Revision int    `json:"revision"`
	HeadSHA  string `json:"head_sha,omitempty"`
	Applied  bool   `json:"applied"`
}

func toDraftBody(d *DraftView) *DraftBody {
	if d == nil {
		return nil
	}
	b := &DraftBody{Category: d.Category, Body: d.Body, WouldSendBack: d.WouldSendBack, Reason: d.Reason,
		Revision: d.TicketRevision, HeadSHA: d.HeadSHA, Applied: d.Applied, Evidence: []StewardEvidenceBody{}}
	for _, e := range d.Evidence {
		b.Evidence = append(b.Evidence, StewardEvidenceBody{Check: e.Check, OK: e.OK, Ref: e.Ref, Detail: e.Detail})
	}
	return b
}
