package ticket

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// Approval is a review approval that has been recorded and committed.
type Approval struct {
	TenantID   uuid.UUID
	TicketID   int64
	Attempt    int
	ApproverID uuid.UUID
	ApprovedAt time.Time
}

// ApprovalHook is told of every approval after the transaction that recorded
// it has committed, whichever route made it: a person on the board, a
// proposal's approval (auto-approved ones too), the code host's review
// webhook, or a promotion of an attempt that was awaiting the choice.
//
// It runs off the approval's own goroutine and context, so a slow or failing
// hook can neither delay nor undo an approval that has already happened.
type ApprovalHook interface {
	Approved(ctx context.Context, a *Approval) error
}

// approvalHookTimeout bounds one hook call; the approval it reports is
// already committed, so the bound only protects the process from a hook
// that never returns.
const approvalHookTimeout = 30 * time.Second

// WithApprovalHook sets the hook told of approvals. Without one, none is.
func WithApprovalHook(h ApprovalHook) ServiceOption {
	return func(s *Service) { s.approvalHook = h }
}

// NotifyApproved tells the approval hook of an approval whose transaction
// has committed. A nil approval is no approval and is ignored, so callers
// can pass what a route returned without checking.
func (s *Service) NotifyApproved(ctx context.Context, a *Approval) {
	if a == nil || s.approvalHook == nil {
		return
	}
	hook := s.approvalHook
	s.approvalCalls.Add(1)
	// The request that approved may end the moment it answers.
	ctx = context.WithoutCancel(ctx)
	go func() {
		defer s.approvalCalls.Done()
		defer func() {
			if r := recover(); r != nil {
				s.logger.ErrorContext(ctx, "approval hook panicked", "ticket", a.TicketID, "attempt", a.Attempt, "panic", r)
			}
		}()
		ctx, cancel := context.WithTimeout(ctx, approvalHookTimeout)
		defer cancel()
		if err := hook.Approved(ctx, a); err != nil {
			s.logger.ErrorContext(ctx, "approval hook failed", "ticket", a.TicketID, "attempt", a.Attempt, "error", err)
		}
	}()
}

// WaitApprovalHooks blocks until the hook calls started so far have
// returned: for a shutdown that should not cut one off, and for tests.
func (s *Service) WaitApprovalHooks() { s.approvalCalls.Wait() }

// ApproveReviewRecorded is ApproveReview that also returns the approval it
// recorded, read in tx. The caller hands it to Service.NotifyApproved once tx
// has committed, and not before: a transaction that rolls back approved
// nothing. ApproveReview itself reports nothing to the hook.
func ApproveReviewRecorded(ctx context.Context, tx TransitionTx, ticketID int64, attempt *AttemptRef, userID uuid.UUID, affinity time.Duration) (*Ticket, *Approval, error) {
	t, err := ApproveReview(ctx, tx, ticketID, attempt, userID, affinity)
	if err != nil {
		return nil, nil, err
	}
	ref := attempt
	if ref == nil {
		if ref, err = promotedAttempt(ctx, tx.Querier(), ticketID); err != nil {
			return nil, nil, err
		}
	}
	a, err := recordedApproval(ctx, tx.Querier(), ref)
	if err != nil {
		return nil, nil, err
	}
	return t, a, nil
}

// recordedApproval reads the approval just written on the attempt.
func recordedApproval(ctx context.Context, q db.Querier, ref *AttemptRef) (*Approval, error) {
	tenant, err := db.TenantFrom(ctx)
	if err != nil {
		return nil, fmt.Errorf("read approval of %s: %w", ref, err)
	}
	a := &Approval{TenantID: tenant, TicketID: ref.TicketID, Attempt: ref.Number}
	var approver *uuid.UUID
	if err := q.QueryRow(ctx, `SELECT approved_at, approved_by_user_id FROM attempts WHERE ticket_id = $1 AND number = $2 AND approved_at IS NOT NULL`,
		ref.TicketID, ref.Number).Scan(&a.ApprovedAt, &approver); err != nil {
		return nil, fmt.Errorf("read approval of %s: %w", ref, err)
	}
	if approver != nil {
		a.ApproverID = *approver
	}
	return a, nil
}
