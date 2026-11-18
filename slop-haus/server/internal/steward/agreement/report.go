package agreement

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Reporter renders the scorer's numbers as the prose the steward starts a run
// from.
type Reporter struct {
	scorer *Scorer
}

func NewReporter(scorer *Scorer) *Reporter { return &Reporter{scorer: scorer} }

// Text reports, per category with assessments in [since, until), how often
// the owner decided as the steward would have, then the discussion rounds'
// label and answer agreement. A zero bound is open; the text is empty when
// nothing is scored yet.
func (r *Reporter) Text(ctx context.Context, since, until time.Time) (string, error) {
	w := Window{Since: since, Until: until}
	scores, err := r.scorer.Score(ctx, w)
	if err != nil {
		return "", err
	}
	discussions, err := r.scorer.ScoreDiscussions(ctx, w)
	if err != nil {
		return "", err
	}
	return FormatText(scores, discussions), nil
}

// FormatText renders the scores for the model.
func FormatText(scores []CategoryScore, discussions *DiscussionReport) string {
	var b strings.Builder
	for i := range scores {
		c := &scores[i]
		fmt.Fprintf(&b, "- %s: %d decided, owner agreed %d (%.0f%%), disagreed %d\n",
			c.Category, c.Decisions, c.Agreed, c.AgreementPct, c.Decisions-c.Agreed)
		for j := range c.Latest {
			d := &c.Latest[j]
			line := fmt.Sprintf("  - you said %s; the owner %s", d.Verdict, d.How)
			if d.OwnerReason != "" {
				line += ": " + oneLine(d.OwnerReason)
			}
			fmt.Fprintf(&b, "%s (you gave: %s)\n", line, oneLine(d.StewardReason))
		}
	}
	if discussions == nil {
		return b.String()
	}
	for i := range discussions.Labels {
		l := &discussions.Labels[i]
		fmt.Fprintf(&b, "- discussion labels, %s: %d decided, owner agreed %d (%.0f%%)\n", l.Category, l.Decisions, l.Agreed, l.AgreementPct)
		for j := range l.Latest {
			d := &l.Latest[j]
			fmt.Fprintf(&b, "  - you labelled #%d round %d %s; the owner %s\n", d.TicketID, d.Round, d.Label, d.How)
		}
	}
	for i := range discussions.Answers {
		a := &discussions.Answers[i]
		fmt.Fprintf(&b, "- discussion answers, %s: %d decided, owner agreed %d (%.0f%%)\n", a.Category, a.Decisions, a.Agreed, a.AgreementPct)
		for j := range a.Latest {
			d := &a.Latest[j]
			fmt.Fprintf(&b, "  - your draft for #%d round %d: the owner %s\n", d.TicketID, d.Round, d.How)
		}
	}
	return b.String()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
