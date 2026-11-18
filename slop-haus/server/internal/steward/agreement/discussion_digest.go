package agreement

import (
	"context"
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
)

// DigestSource adapts the scorer's discussion report to the steward digest.
type DigestSource struct{ scorer *Scorer }

func NewDigestSource(scorer *Scorer) *DigestSource { return &DigestSource{scorer: scorer} }

var _ steward.DiscussionSource = (*DigestSource)(nil)

func (s *DigestSource) DiscussionReport(ctx context.Context, since, until time.Time) (*steward.DiscussionReport, error) {
	rep, err := s.scorer.ScoreDiscussions(ctx, Window{Since: since, Until: until})
	if err != nil {
		return nil, err
	}
	out := &steward.DiscussionReport{}
	for i := range rep.Labels {
		l := &rep.Labels[i]
		out.Labels = append(out.Labels, steward.DiscussionScore{Category: l.Category, Decisions: l.Decisions, Agreed: l.Agreed})
		for _, d := range l.Latest {
			out.LabelDisagreements = append(out.LabelDisagreements, steward.LabelDisagreement{Category: l.Category, TicketID: d.TicketID,
				Attempt: d.Attempt, Round: d.Round, Label: d.Label, How: d.How, Overridden: d.Overridden, At: d.At})
		}
	}
	for i := range rep.Answers {
		a := &rep.Answers[i]
		out.Answers = append(out.Answers, steward.DiscussionScore{Category: a.Category, Decisions: a.Decisions, Agreed: a.Agreed})
		for _, d := range a.Latest {
			out.AnswerDisagreements = append(out.AnswerDisagreements, steward.AnswerDisagreement{Category: a.Category, TicketID: d.TicketID,
				Attempt: d.Attempt, Round: d.Round, How: d.How, OriginalBody: d.OriginalBody, FinalBody: d.FinalBody, Reason: d.Reason, At: d.At})
		}
	}
	return out, nil
}
