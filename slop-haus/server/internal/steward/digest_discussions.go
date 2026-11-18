package steward

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// MaxDiscussionDisagreements bounds each list of disagreements a digest quotes.
const MaxDiscussionDisagreements = 20

// DiscussionScore is how often the owner went with the steward's label, or
// with its drafted answer, for one category.
type DiscussionScore struct {
	Category  string
	Decisions int
	Agreed    int
	// WarnFewDecisions and WarnLowAgreement are the owner's warning
	// thresholds, applied; a level above human relies on them.
	WarnFewDecisions bool
	WarnLowAgreement bool
}

// AgreementRate is the share of decisions agreed with, false when none.
func (s *DiscussionScore) AgreementRate() (float64, bool) {
	return rate(s.Agreed, s.Decisions-s.Agreed)
}

// LabelDisagreement is a round whose label the owner went against.
type LabelDisagreement struct {
	Category string
	TicketID int64
	Attempt  int
	Round    int
	Label    string
	// How is what the owner did instead.
	How string
	// Overridden marks a routine label the owner answered another way.
	Overridden bool
	At         time.Time
}

// AnswerDisagreement is a drafted answer the owner edited or rejected.
type AnswerDisagreement struct {
	Category     string
	TicketID     int64
	Attempt      int
	Round        int
	How          string
	OriginalBody string
	// FinalBody is what the owner posted instead; empty for a reject.
	FinalBody string
	Reason    string
	At        time.Time
}

// DiscussionReport is the discussion record over a window as the agreement
// package scores it; the digest adds the warnings.
type DiscussionReport struct {
	Labels              []DiscussionScore
	Answers             []DiscussionScore
	LabelDisagreements  []LabelDisagreement
	AnswerDisagreements []AnswerDisagreement
}

// DiscussionSource scores the discussion rounds created in [since, until).
// The agreement package owns the scoring and imports this one, so the digest
// takes it injected.
type DiscussionSource interface {
	DiscussionReport(ctx context.Context, since, until time.Time) (*DiscussionReport, error)
}

// DiscussionDigest is the report with the warnings applied, plus an "all" row
// for each score.
type DiscussionDigest struct {
	Labels, Answers           []DiscussionScore
	LabelsTotal, AnswersTotal DiscussionScore
	LabelDisagreements        []LabelDisagreement
	AnswerDisagreements       []AnswerDisagreement
}

func (d *Digester) discussionDigest(ctx context.Context, since, until time.Time) (*DiscussionDigest, error) {
	cfg, err := d.settings.StewardConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("steward: read config: %w", err)
	}
	rep, err := d.discussions.DiscussionReport(ctx, since, until)
	if err != nil {
		return nil, err
	}
	out := &DiscussionDigest{Labels: rep.Labels, Answers: rep.Answers,
		LabelsTotal: DiscussionScore{Category: "all"}, AnswersTotal: DiscussionScore{Category: "all"}}
	warn(cfg, out.Labels, &out.LabelsTotal)
	warn(cfg, out.Answers, &out.AnswersTotal)

	ld := rep.LabelDisagreements
	// A routine label the owner answered another way is the guess the steward
	// is told never to make, so it leads.
	sort.SliceStable(ld, func(i, j int) bool {
		if ld[i].Overridden != ld[j].Overridden {
			return ld[i].Overridden
		}
		return ld[i].At.After(ld[j].At)
	})
	out.LabelDisagreements = ld[:min(len(ld), MaxDiscussionDisagreements)]
	ad := rep.AnswerDisagreements
	sort.SliceStable(ad, func(i, j int) bool { return ad[i].At.After(ad[j].At) })
	out.AnswerDisagreements = ad[:min(len(ad), MaxDiscussionDisagreements)]
	return out, nil
}

// warn sums scores into total and flags each row, and the total, against the
// owner's thresholds.
func warn(cfg *Config, scores []DiscussionScore, total *DiscussionScore) {
	for i := range scores {
		total.Decisions += scores[i].Decisions
		total.Agreed += scores[i].Agreed
		flag(cfg, &scores[i])
	}
	flag(cfg, total)
}

func flag(cfg *Config, s *DiscussionScore) {
	s.WarnFewDecisions = s.Decisions < cfg.WarnMinDecisions
	// No decisions have no percentage to compare.
	if r, ok := s.AgreementRate(); ok {
		s.WarnLowAgreement = 100*r < float64(cfg.WarnMinAgreementPct)
	}
}
