package steward

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// DigestHandler serves the steward's read-only views: the Steward digest and
// the notices on the Approvals page. Any signed-in person may read them; the
// controls they link to (veto, undo) are the owner's and enforced there.
type DigestHandler struct {
	digester *Digester
	logger   *slog.Logger
}

func NewDigestHandler(digester *Digester, logHandler slog.Handler) *DigestHandler {
	return &DigestHandler{digester: digester, logger: slog.New(logHandler)}
}

// RegisterDigestRoutes mounts the steward's read endpoints. Persons only: the
// views show the owner's decisions, which no agent credential needs to read.
func RegisterDigestRoutes(api huma.API, h *DigestHandler) {
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getStewardDigest",
		Method:      http.MethodGet,
		Path:        "/api/steward/digest",
		Summary:     "What the steward did over the last 24 hours or 7 days, per category, with the owner's agreement rate",
	}, h.GetDigest)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getStewardNotices",
		Method:      http.MethodGet,
		Path:        "/api/steward/notices",
		Summary:     "What the steward will apply and has applied lately, for the owner to veto or undo",
	}, h.GetNotices)
}

type DigestCategoryBody struct {
	Category   string `json:"category"`
	Assessed   int    `json:"assessed"`
	Applied    int    `json:"applied"`
	Vetoed     int    `json:"vetoed"`
	Undone     int    `json:"undone"`
	NeedsHuman int    `json:"needs_human"`
	// Awaiting are the verdicts that claim something, not yet decided.
	Awaiting int `json:"awaiting"`
	// Unclassified are the assessments whose category is not a steward category.
	Unclassified int `json:"unclassified"`
	Agreed       int `json:"agreed"`
	Disagreed    int `json:"disagreed"`
	// AgreementRate is the share of the owner's decisions that matched the
	// steward's verdict, null while none is decided.
	AgreementRate *float64 `json:"agreement_rate" nullable:"true"`
}

type DigestDecisionBody struct {
	ProposalID string    `json:"proposal_id"`
	TicketID   *int64    `json:"ticket_id" nullable:"true"`
	Category   string    `json:"category"`
	Outcome    string    `json:"outcome" enum:"undone,vetoed,applied,needs_human,voided,waiting,owner_decided,shadow"`
	At         time.Time `json:"at"`
}

type StewardDigestRequest struct {
	Window string `query:"window" default:"24h" enum:"24h,7d" doc:"The span to summarise, ending now."`
}

type StewardDigestResponse struct {
	Body struct {
		Since        time.Time            `json:"since"`
		Until        time.Time            `json:"until"`
		Total        DigestCategoryBody   `json:"total"`
		Categories   []DigestCategoryBody `json:"categories"`
		Decisions    []DigestDecisionBody `json:"decisions" doc:"Newest first, at most 100."`
		DecisionsCut bool                 `json:"decisions_cut"`
		Discussions  DiscussionDigestBody `json:"discussions"`
	}
}

// DiscussionScoreBody is one category's agreement on the discussion record.
// Blank (null) while no round was decided.
type DiscussionScoreBody struct {
	Category      string   `json:"category" doc:"A discussion category, or yours for the rounds labelled yours; all for the total."`
	Decisions     int      `json:"decisions"`
	Agreed        int      `json:"agreed"`
	Disagreed     int      `json:"disagreed"`
	AgreementRate *float64 `json:"agreement_rate" nullable:"true"`
	// WarnFewDecisions and WarnLowAgreement apply the owner's warning
	// thresholds (tenant settings) to this row.
	WarnFewDecisions bool `json:"warn_few_decisions"`
	WarnLowAgreement bool `json:"warn_low_agreement"`
}

type DiscussionLabelDisagreementBody struct {
	Category   string    `json:"category"`
	TicketID   int64     `json:"ticket_id"`
	Attempt    int       `json:"attempt"`
	Round      int       `json:"round"`
	Label      string    `json:"label"`
	How        string    `json:"how" doc:"What the owner did instead."`
	Overridden bool      `json:"overridden" doc:"A routine label the owner answered another way; these lead."`
	At         time.Time `json:"at"`
}

// DiscussionAnswerDisagreementBody quotes the owner's text and the draft; both
// are untrusted text, to be shown as plain text.
type DiscussionAnswerDisagreementBody struct {
	Category     string    `json:"category"`
	TicketID     int64     `json:"ticket_id"`
	Attempt      int       `json:"attempt"`
	Round        int       `json:"round"`
	How          string    `json:"how" enum:"edit,reject"`
	OriginalBody string    `json:"original_body"`
	FinalBody    string    `json:"final_body" doc:"What the owner posted instead; empty for a reject."`
	Reason       string    `json:"reason"`
	At           time.Time `json:"at"`
}

type DiscussionDigestBody struct {
	Labels              []DiscussionScoreBody              `json:"labels" doc:"Per category, with the yours bucket."`
	LabelsTotal         DiscussionScoreBody                `json:"labels_total"`
	Answers             []DiscussionScoreBody              `json:"answers"`
	AnswersTotal        DiscussionScoreBody                `json:"answers_total"`
	LabelDisagreements  []DiscussionLabelDisagreementBody  `json:"label_disagreements" doc:"Overridden routine labels first, then newest first."`
	AnswerDisagreements []DiscussionAnswerDisagreementBody `json:"answer_disagreements" doc:"Newest first."`
}

func scoreBody(s *DiscussionScore) DiscussionScoreBody {
	b := DiscussionScoreBody{Category: s.Category, Decisions: s.Decisions, Agreed: s.Agreed, Disagreed: s.Decisions - s.Agreed,
		WarnFewDecisions: s.WarnFewDecisions, WarnLowAgreement: s.WarnLowAgreement}
	if r, ok := s.AgreementRate(); ok {
		b.AgreementRate = &r
	}
	return b
}

func scoreBodies(ss []DiscussionScore) []DiscussionScoreBody {
	out := make([]DiscussionScoreBody, 0, len(ss))
	for i := range ss {
		out = append(out, scoreBody(&ss[i]))
	}
	return out
}

func discussionBody(d *DiscussionDigest) DiscussionDigestBody {
	b := DiscussionDigestBody{
		Labels: scoreBodies(d.Labels), LabelsTotal: scoreBody(&d.LabelsTotal),
		Answers: scoreBodies(d.Answers), AnswersTotal: scoreBody(&d.AnswersTotal),
		LabelDisagreements:  make([]DiscussionLabelDisagreementBody, 0, len(d.LabelDisagreements)),
		AnswerDisagreements: make([]DiscussionAnswerDisagreementBody, 0, len(d.AnswerDisagreements)),
	}
	for _, x := range d.LabelDisagreements {
		b.LabelDisagreements = append(b.LabelDisagreements, DiscussionLabelDisagreementBody(x))
	}
	for _, x := range d.AnswerDisagreements {
		b.AnswerDisagreements = append(b.AnswerDisagreements, DiscussionAnswerDisagreementBody(x))
	}
	return b
}

func categoryBody(c *CategoryDigest) DigestCategoryBody {
	b := DigestCategoryBody{Category: c.Category, Assessed: c.Assessed, Applied: c.Applied, Vetoed: c.Vetoed, Undone: c.Undone,
		NeedsHuman: c.NeedsHuman, Awaiting: c.Awaiting, Unclassified: c.Unclassified, Agreed: c.Agreed, Disagreed: c.Disagreed}
	if r, ok := c.AgreementRate(); ok {
		b.AgreementRate = &r
	}
	return b
}

func (h *DigestHandler) GetDigest(ctx context.Context, in *StewardDigestRequest) (*StewardDigestResponse, error) {
	d, err := h.digester.Digest(ctx, in.Window)
	switch {
	case errors.Is(err, ErrUnknownWindow):
		return nil, huma.Error422UnprocessableEntity("window must be 24h or 7d")
	case errors.Is(err, ErrWindowTooLarge):
		return nil, huma.Error409Conflict("too many assessments in the window to summarise")
	case err != nil:
		h.logger.ErrorContext(ctx, "steward digest failed", "error", err)
		return nil, huma.Error500InternalServerError("could not read the steward digest")
	}
	resp := &StewardDigestResponse{}
	resp.Body.Since, resp.Body.Until = d.Since, d.Until
	resp.Body.Total = categoryBody(&d.Total)
	resp.Body.Categories = make([]DigestCategoryBody, 0, len(d.Categories))
	for i := range d.Categories {
		resp.Body.Categories = append(resp.Body.Categories, categoryBody(&d.Categories[i]))
	}
	resp.Body.Decisions = make([]DigestDecisionBody, 0, len(d.Decisions))
	for _, x := range d.Decisions {
		resp.Body.Decisions = append(resp.Body.Decisions, DigestDecisionBody{
			ProposalID: x.PendingActionID.String(), TicketID: x.TicketID, Category: x.Category, Outcome: string(x.Outcome), At: x.At})
	}
	resp.Body.DecisionsCut = d.DecisionsCut
	resp.Body.Discussions = discussionBody(&d.Discussions)
	return resp, nil
}

type NoticeBody struct {
	ProposalID string `json:"proposal_id"`
	TicketID   *int64 `json:"ticket_id" nullable:"true"`
	Category   string `json:"category"`
	// At is when it is, or was, applied; null while its window is not open.
	At *time.Time `json:"at" nullable:"true"`
}

type StewardNoticesResponse struct {
	Body struct {
		Mode       string       `json:"mode" enum:"off,shadow,live"`
		Pending    []NoticeBody `json:"pending" doc:"Decisions waiting out their veto window: veto to stop them."`
		Applied    []NoticeBody `json:"applied" doc:"Applied in the last hour: undo to take back."`
		CapReached bool         `json:"cap_reached" doc:"The daily cap on applications is used up; further decisions wait for the owner."`
		DailyCap   int          `json:"daily_cap"`
	}
}

func noticeBodies(ns []Notice) []NoticeBody {
	out := make([]NoticeBody, 0, len(ns))
	for _, n := range ns {
		out = append(out, NoticeBody{ProposalID: n.PendingActionID.String(), TicketID: n.TicketID, Category: n.Category, At: n.At})
	}
	return out
}

func (h *DigestHandler) GetNotices(ctx context.Context, _ *struct{}) (*StewardNoticesResponse, error) {
	n, err := h.digester.Notices(ctx)
	if err != nil {
		h.logger.ErrorContext(ctx, "steward notices failed", "error", err)
		return nil, huma.Error500InternalServerError("could not read the steward notices")
	}
	resp := &StewardNoticesResponse{}
	resp.Body.Mode = string(n.Mode)
	resp.Body.Pending, resp.Body.Applied = noticeBodies(n.Pending), noticeBodies(n.Applied)
	resp.Body.CapReached, resp.Body.DailyCap = n.CapReached, n.DailyCap
	return resp, nil
}
