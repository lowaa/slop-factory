package steward

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// ReviewDecisionApprove is the only review decision the steward may take.
const ReviewDecisionApprove = "approve"

// SlopReviewPersonas are the slop-review personas every change is reviewed
// by before it is handed over; one missing means the change was not fully
// reviewed. The supervisor's one pass is the standard persona over the
// whole change.
var SlopReviewPersonas = []string{"standard"}

// LineCount is the size of a change in lines, split by whether it counts.
type LineCount struct {
	// Counted are the lines added or removed in hand-written files.
	Counted int
	// Generated are the lines in generated files, which do not count: a
	// regenerated client is not something a reviewer reads.
	Generated int
}

// PersonaReview is the slop-review of one persona on one head: the newest
// supervisor-posted run that covers it.
type PersonaReview struct {
	Persona string
	// Approved is the run's verdict.
	Approved bool
	// Blocking are the blocker and major findings the run raised, whichever
	// of its passes raised them. Labels never clear them: they are the
	// reviewed agent's own word, so the way past one is a fix and a new
	// review of the new head.
	Blocking int
	// Incomplete is a review that did not cover the whole change.
	Incomplete bool
}

type (
	// LineReader counts the lines of the ticket's change as it is now.
	LineReader interface {
		ChangedLines(ctx context.Context, ticketID int64) (*LineCount, error)
	}
	// ReviewLimitReader gives the tenant's size limit for a review the
	// steward may approve, in changed lines.
	ReviewLimitReader interface {
		ReviewMaxLines(ctx context.Context) (int, error)
	}
	// SlopReviewReader gives the slop-reviews of a head the supervisor ran,
	// one per persona at most.
	SlopReviewReader interface {
		SlopReviews(ctx context.Context, ticketID int64, headSHA string) (reviews []PersonaReview, ref string, err error)
	}
	// ReviewMarkReader gives the reason the agent asked for a human to read a
	// change it was free to merge; empty when it gave none.
	ReviewMarkReader interface {
		ReviewRequestReason(ctx context.Context, ticketID int64) (reason, ref string, err error)
	}
	// ProtectedPathReader says which of the paths the manifest's
	// protected_paths cover, as "path (rule reason)" lines.
	ProtectedPathReader interface {
		ProtectedAmong(ctx context.Context, paths []string) ([]string, error)
	}
)

// GeneratedLinesFactor bounds the generated lines a change may carry, as a
// multiple of the size limit. Which files are generated is read from the
// change itself (a header, a .gitattributes it adds), so an author could mark
// hand-written code; the bound keeps that from hiding an unreadable change.
const GeneratedLinesFactor = 10

// DiffSizeWithinLimit holds when the change's hand-written lines are within
// the tenant's limit. The counted lines and the limit are in the evidence.
func DiffSizeWithinLimit(lines LineReader, limit ReviewLimitReader) Check {
	return checkFunc{CheckDiffSizeWithinLimit, func(ctx context.Context, s *Subject) (Evidence, error) {
		max, err := limit.ReviewMaxLines(ctx)
		if err != nil {
			return Evidence{}, err
		}
		n, err := lines.ChangedLines(ctx, s.TicketID)
		if err != nil {
			return Evidence{}, err
		}
		detail := fmt.Sprintf("%d changed lines counted, limit %d (%d in generated files not counted)", n.Counted, max, n.Generated)
		return Evidence{OK: n.Counted <= max && n.Generated <= max*GeneratedLinesFactor, Detail: detail}, nil
	}}
}

// SlopReviewComplete holds when every one of SlopReviewPersonas has a
// supervisor-posted review of the head the steward read that approves
// (approve or approve_with_comments), covered the whole change, and raised
// no blocker or major finding.
func SlopReviewComplete(r SlopReviewReader) Check {
	return checkFunc{CheckSlopReviewComplete, func(ctx context.Context, s *Subject) (Evidence, error) {
		if s.HeadSHA == "" {
			return Evidence{Detail: "no head to check"}, nil
		}
		reviews, ref, err := r.SlopReviews(ctx, s.TicketID, s.HeadSHA)
		if err != nil {
			return Evidence{}, err
		}
		var problems []string
		for _, persona := range SlopReviewPersonas {
			i := slices.IndexFunc(reviews, func(p PersonaReview) bool { return p.Persona == persona })
			switch {
			case i < 0:
				problems = append(problems, persona+": not recorded for "+short(s.HeadSHA))
			case reviews[i].Incomplete:
				problems = append(problems, persona+": incomplete")
			case !reviews[i].Approved:
				problems = append(problems, persona+": not approved")
			case reviews[i].Blocking > 0:
				problems = append(problems, fmt.Sprintf("%s: %d blocker or major findings", persona, reviews[i].Blocking))
			}
		}
		if len(problems) > 0 {
			return Evidence{Ref: ref, Detail: strings.Join(problems, "; ")}, nil
		}
		return Evidence{OK: true, Ref: ref, Detail: "slop-review (" + strings.Join(SlopReviewPersonas, ", ") + ") complete and approving"}, nil
	}}
}

// NoReviewRequestReason holds when the agent did not ask for a human to read
// the change. A reason is the agent saying the change is risky; the steward
// does not overrule that.
func NoReviewRequestReason(r ReviewMarkReader) Check {
	return checkFunc{CheckNoReviewRequestReason, func(ctx context.Context, s *Subject) (Evidence, error) {
		reason, ref, err := r.ReviewRequestReason(ctx, s.TicketID)
		if err != nil {
			return Evidence{}, err
		}
		if reason != "" {
			return Evidence{Ref: ref, Detail: "the agent asked for a human to read it: " + reason}, nil
		}
		return Evidence{OK: true, Ref: ref}, nil
	}}
}

// NoProtectedPath holds when none of the ticket's paths, the change's paths
// or a proposed edit's paths is covered by a protected_paths rule.
func NoProtectedPath(p ProtectedPathReader, d DiffReader, t TicketReader) Check {
	return checkFunc{CheckNoProtectedPath, func(ctx context.Context, s *Subject) (Evidence, error) {
		paths := slices.Clone(s.ProposedPaths)
		scope, err := t.Paths(ctx, s.TicketID)
		if err != nil {
			return Evidence{}, err
		}
		paths = append(paths, scope...)
		// A category that concerns no change (an edit, an answer) has none to
		// read; for a change the diff is read, and an unreadable one fails.
		if s.HeadSHA != "" {
			changed, err := d.ChangedPaths(ctx, s.TicketID)
			if err != nil {
				return Evidence{}, err
			}
			paths = append(paths, changed...)
		}
		hits, err := p.ProtectedAmong(ctx, paths)
		if err != nil {
			return Evidence{}, err
		}
		if len(hits) > 0 {
			return Evidence{Detail: "protected: " + strings.Join(hits, ", ")}, nil
		}
		return Evidence{OK: true, Detail: fmt.Sprintf("%d paths, none protected", len(paths))}, nil
	}}
}
