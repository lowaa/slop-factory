package agreement

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// YoursBucket groups label scores of rounds labelled yours, which name no
// category.
const YoursBucket = "yours"

// LabelDisagreement is a round whose label the owner went against.
type LabelDisagreement struct {
	TicketID int64
	Attempt  int
	Round    int
	Label    string
	// How is what the owner did: "answered elsewhere" or "answered with the template".
	How string
	// Overridden marks a routine label the owner answered by another route;
	// these are listed first.
	Overridden bool
	At         time.Time
}

// AnswerDisagreement is a drafted answer the owner edited or rejected.
type AnswerDisagreement struct {
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

// DiscussionLabelScore is the label agreement of one category.
type DiscussionLabelScore struct {
	Category     string
	Decisions    int
	Agreed       int
	AgreementPct float64
	Latest       []LabelDisagreement
}

// DiscussionAnswerScore is the answer agreement of one category.
type DiscussionAnswerScore struct {
	Category     string
	Decisions    int
	Agreed       int
	AgreementPct float64
	Latest       []AnswerDisagreement
}

// DiscussionReport keeps the two scores apart: the label says whether the
// classification was right, the answer whether the draft was accepted.
type DiscussionReport struct {
	Labels  []DiscussionLabelScore
	Answers []DiscussionAnswerScore
}

type discussionRow struct {
	DiscussionRound
	Reason string
	At     time.Time
}

// ScoreDiscussions scores the labelled discussion rounds created in the window.
func (s *Scorer) ScoreDiscussions(ctx context.Context, w Window) (*DiscussionReport, error) {
	rows, err := s.discussionRows(ctx, w)
	if err != nil {
		return nil, err
	}
	return scoreDiscussionRows(rows), nil
}

func (s *Scorer) discussionRows(ctx context.Context, w Window) ([]discussionRow, error) {
	var since, until *time.Time
	if !w.Since.IsZero() {
		since = &w.Since
	}
	if !w.Until.IsZero() {
		until = &w.Until
	}
	// The owner's answer by another route is their first comment on the ticket
	// after the label, before the attempt's next round: written by them, or by
	// an agent speaking for them (Remote Control), and addressed to this
	// attempt or to none. A decision, which posts its own comment, takes
	// precedence.
	q, err := s.db.Query(ctx, `
		SELECT l.ticket_id, l.attempt, l.round, l.label, l.category, COALESCE(l.head_sha, ''),
		       COALESCE(d.action, ''), COALESCE(d.original_body, ''), COALESCE(d.final_body, ''), COALESCE(d.reason, ''),
		       COALESCE(d.created_at, c.created_at, l.created_at),
		       CASE WHEN d.id IS NULL THEN COALESCE(c.body, '') ELSE '' END
		  FROM steward_discussion_labels l
		  LEFT JOIN steward_discussion_decisions d ON d.tenant_id = l.tenant_id AND d.label_id = l.id
		  LEFT JOIN LATERAL (
		        SELECT tc.body, tc.created_at FROM ticket_comments tc
		         WHERE tc.tenant_id = l.tenant_id AND tc.ticket_id = l.ticket_id AND (tc.author_user_id IS NOT NULL OR tc.on_behalf_of_user_id IS NOT NULL)
		           AND (tc.attempt_number IS NULL OR tc.attempt_number = l.attempt)
		           AND tc.created_at > l.created_at
		           AND tc.created_at < COALESCE((SELECT min(n.created_at) FROM steward_discussion_labels n
		                 WHERE n.tenant_id = l.tenant_id AND n.ticket_id = l.ticket_id AND n.attempt = l.attempt AND n.round > l.round), 'infinity')
		         ORDER BY tc.created_at LIMIT 1) c ON d.id IS NULL
		 WHERE ($1::timestamptz IS NULL OR l.created_at >= $1) AND ($2::timestamptz IS NULL OR l.created_at < $2)
		 ORDER BY l.created_at LIMIT $3`, since, until, s.scanLimit+1)
	if err != nil {
		return nil, fmt.Errorf("agreement: read discussion rounds: %w", err)
	}
	defer q.Close()
	var out []discussionRow
	for q.Next() {
		var r discussionRow
		if err := q.Scan(&r.TicketID, &r.Attempt, &r.Round, &r.Label, &r.Category, &r.HeadSHA,
			&r.Action, &r.OriginalBody, &r.FinalBody, &r.Reason, &r.At, &r.OtherAnswer); err != nil {
			return nil, fmt.Errorf("agreement: read discussion rounds: %w", err)
		}
		out = append(out, r)
	}
	if err := q.Err(); err != nil {
		return nil, fmt.Errorf("agreement: read discussion rounds: %w", err)
	}
	if len(out) > s.scanLimit {
		return nil, ErrWindowTooLarge
	}
	return out, nil
}

func scoreDiscussionRows(rows []discussionRow) *DiscussionReport {
	labels := map[string]*DiscussionLabelScore{}
	answers := map[string]*DiscussionAnswerScore{}
	for i := range rows {
		r := &rows[i]
		bucket := r.Category
		if r.Label == LabelYours {
			bucket = YoursBucket
		}
		if bucket == "" {
			continue
		}
		switch CompareLabel(&r.DiscussionRound) {
		case Agreed:
			ls := labelScore(labels, bucket)
			ls.Decisions++
			ls.Agreed++
		case Disagreed:
			ls := labelScore(labels, bucket)
			ls.Decisions++
			ls.Latest = append(ls.Latest, LabelDisagreement{TicketID: r.TicketID, Attempt: r.Attempt, Round: r.Round,
				Label: r.Label, How: labelHow(r), Overridden: OverriddenRoutine(&r.DiscussionRound), At: r.At})
		}
		switch CompareAnswer(&r.DiscussionRound) {
		case Agreed:
			as := answerScore(answers, bucket)
			as.Decisions++
			as.Agreed++
		case Disagreed:
			as := answerScore(answers, bucket)
			as.Decisions++
			as.Latest = append(as.Latest, AnswerDisagreement{TicketID: r.TicketID, Attempt: r.Attempt, Round: r.Round,
				How: r.Action, OriginalBody: r.OriginalBody, FinalBody: r.FinalBody, Reason: r.Reason, At: r.At})
		}
	}
	rep := &DiscussionReport{}
	for _, ls := range labels {
		ls.AgreementPct = 100 * float64(ls.Agreed) / float64(ls.Decisions)
		d := ls.Latest
		// The routine-but-owner-went-elsewhere guess is the one the steward is
		// told never to make, so it leads.
		sort.SliceStable(d, func(i, j int) bool {
			if d[i].Overridden != d[j].Overridden {
				return d[i].Overridden
			}
			return d[i].At.After(d[j].At)
		})
		ls.Latest = d[:min(len(d), MaxDisagreements)]
		rep.Labels = append(rep.Labels, *ls)
	}
	for _, as := range answers {
		as.AgreementPct = 100 * float64(as.Agreed) / float64(as.Decisions)
		d := as.Latest
		sort.SliceStable(d, func(i, j int) bool { return d[i].At.After(d[j].At) })
		as.Latest = d[:min(len(d), MaxDisagreements)]
		rep.Answers = append(rep.Answers, *as)
	}
	sort.Slice(rep.Labels, func(i, j int) bool { return rep.Labels[i].Category < rep.Labels[j].Category })
	sort.Slice(rep.Answers, func(i, j int) bool { return rep.Answers[i].Category < rep.Answers[j].Category })
	return rep
}

func labelHow(r *discussionRow) string {
	if r.Label == LabelRoutine {
		return "answered elsewhere"
	}
	return "answered with the template"
}

func labelScore(m map[string]*DiscussionLabelScore, k string) *DiscussionLabelScore {
	if m[k] == nil {
		m[k] = &DiscussionLabelScore{Category: k}
	}
	return m[k]
}

func answerScore(m map[string]*DiscussionAnswerScore, k string) *DiscussionAnswerScore {
	if m[k] == nil {
		m[k] = &DiscussionAnswerScore{Category: k}
	}
	return m[k]
}
