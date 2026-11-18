package ticket

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// ReviewParams is what an agent hands a human when it stops for a PR review.
type ReviewParams struct {
	// PRURL is the change to review. Required: validated like ticket_finish's.
	PRURL string
	// Handoff is saved as an agent context, for whoever claims the ticket
	// after the review -- the author, first, or anyone once its first claim
	// lapses.
	Handoff string
	// Reason is why an agent wants a human to read a change it was free to
	// merge. Required, and only then, for the promoted attempt of a ticket
	// that does not require approval.
	Reason string
	// Within, when set, runs in the transaction that hands the ticket over,
	// after it, with that transaction's querier: what it writes lands with
	// the review or not at all.
	Within func(q db.Querier) error
}

// ErrReviewNeedsReason refuses a review of a ticket that does not require
// approval, asked for by its promoted attempt without a reason.
var ErrReviewNeedsReason = errors.New("ticket: this ticket does not require approval, so merge it yourself (the integration lock steps) " +
	"-- or, if you want a human to read the finished change before it merges (risky, security-sensitive, a migration you are unsure of), " +
	"call ticket_finish again with a reason. A question about direction, spec or approach is ticket_discuss, not a review")

// VoluntaryReviewLead starts the comment a voluntary review's reason is
// posted as.
const VoluntaryReviewLead = "Review requested though not required: "

// Review hands the agent's held ticket to a human to review its PR. Like
// Discuss, the handoff is required: the agent that picks the ticket up after
// the review -- to merge it, or to address the review -- starts from it.
//
// The PR link is validated before anything is written, so a bad one leaves
// the ticket held for the agent to retry.
func (s *Service) Review(ctx context.Context, agentID uuid.UUID, params *ReviewParams) (*Ticket, error) {
	prURL, err := parsePRURL(params.PRURL)
	if err != nil {
		return nil, err
	}
	if prURL == nil {
		return nil, ErrPRURLRequired
	}
	if strings.TrimSpace(params.Handoff) == "" {
		return nil, ErrEmptyHandoff
	}
	// Decided in the exit's own transaction: a human promoting the attempt
	// meanwhile must not slip a reasonless review past the rule.
	exit := func(tx TransitionTx, lease *HeldLease) heldExit {
		return func(held *Ticket, agentID uuid.UUID) (*Change, error) {
			reason := strings.TrimSpace(params.Reason)
			var promoted bool
			if err := tx.Querier().QueryRow(ctx, `SELECT promoted_at IS NOT NULL FROM attempts WHERE ticket_id = $1 AND number = $2`,
				lease.TicketID, lease.Attempt).Scan(&promoted); err != nil {
				return nil, fmt.Errorf("read attempt %d: %w", lease.Attempt, err)
			}
			if !promoted || held.RequiresApproval {
				reason = ""
			} else if reason == "" {
				return nil, fmt.Errorf("%w (ticket #%d)", ErrReviewNeedsReason, held.ID)
			}
			return s.inTicketsRepo(ctx, prURL, reviewExit(*prURL, params.Handoff, reason))(held, agentID)
		}
	}
	return s.exitHeldChosen(ctx, agentID, exit, params.Within)
}

// ErrLandNotPromoted refuses landing an attempt that is not the ticket's promoted one.
var ErrLandNotPromoted = errors.New("ticket: only the promoted attempt's change lands; call ticket_finish again, and it goes to a human's review")

// LandParams are what a finished ticket hands to the landing queue.
type LandParams struct {
	Handoff string
	// Within runs in the transaction that hands the ticket over, after it: the
	// queue row it writes lands with the hand-over or not at all.
	Within func(q db.Querier) error
}

// Land hands the agent's held ticket to the server's landing queue. The agent
// no longer holds it; the server claims it when its row reaches the head.
func (s *Service) Land(ctx context.Context, agentID uuid.UUID, params *LandParams) (*Ticket, error) {
	// The gate ticket_finish read before this is stale by now: a person may have
	// revoked the approval or moved the promotion, and the server would then
	// merge what nobody allowed. So it is decided again in the hand-over's
	// transaction, as Review does.
	exit := func(tx TransitionTx, lease *HeldLease) heldExit {
		return func(held *Ticket, agentID uuid.UUID) (*Change, error) {
			var promoted, approved bool
			if err := tx.Querier().QueryRow(ctx, `SELECT promoted_at IS NOT NULL, approved_at IS NOT NULL FROM attempts WHERE ticket_id = $1 AND number = $2`,
				lease.TicketID, lease.Attempt).Scan(&promoted, &approved); err != nil {
				return nil, fmt.Errorf("read attempt %d: %w", lease.Attempt, err)
			}
			switch {
			case !promoted:
				return nil, fmt.Errorf("%w (ticket #%d)", ErrLandNotPromoted, held.ID)
			case held.RequiresApproval && !approved:
				return nil, fmt.Errorf("%w (ticket #%d)", ErrNotApproved, held.ID)
			}
			return landExit(params.Handoff)(held, agentID)
		}
	}
	return s.exitHeldChosen(ctx, agentID, exit, params.Within)
}

// Approve is a human approving the PR of a ticket in review. The ticket goes
// back to the queue with its author holding first claim (unless the author is
// busy on another ticket, when anyone may take it), and whoever claims
// it is told to merge. See ErrNotApproved for what this unlocks.
func (s *Service) Approve(ctx context.Context, ticketID int64, userID uuid.UUID) (*Ticket, error) {
	return s.ApproveAttempt(ctx, ticketID, nil, userID)
}

// ApproveAttempt is Approve for the attempt the person named, as
// ResolveAttempt returned it (nil when they named none). The decision is about
// that attempt's review alone: it requeues that attempt, and a sibling's
// review is untouched.
func (s *Service) ApproveAttempt(ctx context.Context, ticketID int64, attempt *AttemptRef, userID uuid.UUID) (*Ticket, error) {
	var left *Ticket
	var approval *Approval
	err := s.store.InTransition(ctx, func(tx TransitionTx) error {
		var err error
		left, approval = nil, nil
		left, approval, err = ApproveReviewRecorded(ctx, tx, ticketID, attempt, userID, s.affinity)
		return err
	})
	if err != nil {
		return left, err
	}
	s.NotifyApproved(ctx, approval)
	return left, nil
}

// RequestChanges is a human sending a ticket in review back for more work.
// The comment is required -- it is what the author is asked to address,
// alongside whatever is on the PR itself -- and any earlier approval is
// cleared.
func (s *Service) RequestChanges(ctx context.Context, ticketID int64, userID uuid.UUID, comment string) (*Ticket, error) {
	return s.RequestChangesAttempt(ctx, ticketID, nil, userID, comment)
}

// RequestChangesAttempt is RequestChanges for the attempt the person named; see
// ApproveAttempt.
func (s *Service) RequestChangesAttempt(ctx context.Context, ticketID int64, attempt *AttemptRef, userID uuid.UUID, comment string) (*Ticket, error) {
	var left *Ticket
	err := s.store.InTransition(ctx, func(tx TransitionTx) error {
		var err error
		left, err = RequestReviewChanges(ctx, tx, ticketID, attempt, userID, comment, s.affinity)
		return err
	})
	return left, err
}
