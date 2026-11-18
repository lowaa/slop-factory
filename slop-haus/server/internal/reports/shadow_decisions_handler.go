package reports

import (
	"context"
	"time"
)

type ShadowReasonBody struct {
	// Decision is would_accept or would_refuse.
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
	Count    int64  `json:"count"`
}

type ShadowFailureBody struct {
	TicketID int64  `json:"ticket_id"`
	Title    string `json:"title"`
	ChangeID int64  `json:"change_id"`
	// HeadSHA is the head the rule would have accepted; CIHeadSHA the later
	// head whose checks failed.
	HeadSHA      string    `json:"head_sha"`
	CIHeadSHA    string    `json:"ci_head_sha"`
	DriftDirs    []string  `json:"drift_dirs"`
	DecidedAt    time.Time `json:"decided_at"`
	CIRecordedAt time.Time `json:"ci_recorded_at"`
}

// ShadowDecisionsBody is the independent-apps shadow decisions section, over
// [from, to).
type ShadowDecisionsBody struct {
	From        time.Time `json:"from"`
	To          time.Time `json:"to"`
	WouldAccept int64     `json:"would_accept"`
	WouldRefuse int64     `json:"would_refuse"`
	// Of the would-accepts, by what the later checks said.
	AcceptCIPassed   int64              `json:"accept_ci_passed"`
	AcceptCIFailed   int64              `json:"accept_ci_failed"`
	AcceptAwaitingCI int64              `json:"accept_awaiting_ci"`
	Reasons          []ShadowReasonBody `json:"reasons"`
	// Failures are the most recent would-accepts whose checks failed, capped.
	Failures []ShadowFailureBody `json:"failures"`
}

func shadowDecisionsBody(r *ShadowDecisions) *ShadowDecisionsBody {
	b := &ShadowDecisionsBody{
		From: r.From.UTC(), To: r.To.UTC(), WouldAccept: r.WouldAccept, WouldRefuse: r.WouldRefuse,
		AcceptCIPassed: r.AcceptCIPassed, AcceptCIFailed: r.AcceptCIFailed, AcceptAwaitingCI: r.AcceptAwaitingCI,
		Reasons: make([]ShadowReasonBody, 0, len(r.Reasons)), Failures: make([]ShadowFailureBody, 0, len(r.Failures)),
	}
	for _, x := range r.Reasons {
		b.Reasons = append(b.Reasons, ShadowReasonBody{Decision: x.Decision, Reason: x.Reason, Count: x.Count})
	}
	for _, f := range r.Failures {
		dirs := f.DriftDirs
		if dirs == nil {
			dirs = []string{}
		}
		b.Failures = append(b.Failures, ShadowFailureBody{TicketID: f.TicketID, Title: f.Title, ChangeID: f.ChangeID, HeadSHA: f.HeadSHA,
			CIHeadSHA: f.CIHeadSHA, DriftDirs: dirs, DecidedAt: f.DecidedAt.UTC(), CIRecordedAt: f.CIRecordedAt.UTC()})
	}
	return b
}

type GetShadowDecisionsRequest struct {
	// Days is the length of the period, ending now.
	Days int `query:"days" default:"30" enum:"7,30"`
}

type GetShadowDecisionsResponse struct {
	Body *ShadowDecisionsBody
}

func (h *Handler) GetShadowDecisions(ctx context.Context, in *GetShadowDecisionsRequest) (*GetShadowDecisionsResponse, error) {
	r, err := h.svc.ShadowDecisions(ctx, in.Days)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &GetShadowDecisionsResponse{Body: shadowDecisionsBody(r)}, nil
}
