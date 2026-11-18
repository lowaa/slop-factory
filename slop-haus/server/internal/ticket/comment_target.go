package ticket

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lowaa/slop-factory/go-db/pgerr"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// CommentTarget is what a comment is addressed to: the ticket's own
// conversation, or one attempt's. A type rather than a nullable number, and
// only insertCommentOn turns it into SQL, so no caller can write
// attempt_number itself and lose a ticket's own comment to an attempt (or the
// reverse).
type CommentTarget struct {
	ticketID int64
	attempt  *AttemptRef
}

// OnTicket addresses a comment to the ticket itself.
func OnTicket(ticketID int64) CommentTarget { return CommentTarget{ticketID: ticketID} }

// OnAttempt addresses a comment to one attempt.
func OnAttempt(ref *AttemptRef) CommentTarget {
	return CommentTarget{ticketID: ref.TicketID, attempt: ref}
}

// attemptNumber is the column's value: NULL for the ticket's own.
func (c CommentTarget) attemptNumber() *int {
	if c.attempt == nil {
		return nil
	}
	return &c.attempt.Number
}

// commentTargetFor is where a comment by `by` on ticketID goes:
//   - an agent's, while it holds a lease on the ticket, is its attempt's;
//     without one it is the ticket's
//   - a person's is the ticket's until its work has started, and the
//     promoted attempt's after -- the rule C3a backfilled by. Work has
//     started at the first lease or the first move to in_progress, whichever
//     is earlier: released leases are pruned, so the status history is what
//     still knows. While several attempts are live it is the ticket's
//     again: guidance for all of them, which the delivery trigger hands to
//     every live attempt's holder, not one attempt's.
func commentTargetFor(ctx context.Context, q db.Querier, ticketID int64, by Author) (CommentTarget, error) {
	if by.AgentID != nil {
		ref, err := heldAttempt(ctx, q, *by.AgentID, ticketID)
		if errors.Is(err, ErrNoLease) {
			return OnTicket(ticketID), nil
		}
		if err != nil {
			return CommentTarget{}, err
		}
		return OnAttempt(ref), nil
	}
	var started bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM leases WHERE ticket_id = $1)
		    OR EXISTS (SELECT 1 FROM ticket_status_changes WHERE ticket_id = $1 AND to_status = 'in_progress')`,
		ticketID).Scan(&started); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return CommentTarget{}, fmt.Errorf("read whether work has started: %w", err)
	}
	if !started {
		return OnTicket(ticketID), nil
	}
	states, err := attemptStates(ctx, q, ticketID)
	if err != nil {
		return CommentTarget{}, err
	}
	live := 0
	promotedLive := false
	for _, a := range states {
		if a.live() {
			live++
			promotedLive = promotedLive || a.promoted
		}
	}
	// Several live, or one live that is not the promoted: no single promoted
	// attempt is the one at work, and a comment to it would be to nobody.
	if live > 1 || (live == 1 && !promotedLive) {
		return OnTicket(ticketID), nil
	}
	ref, err := promotedAttempt(ctx, q, ticketID)
	// With no attempt promoted, no attempt is the person's to speak to: the
	// comment is the ticket's.
	if errors.Is(err, ErrNoPromotedAttempt) {
		return OnTicket(ticketID), nil
	}
	if err != nil {
		return CommentTarget{}, err
	}
	return OnAttempt(ref), nil
}

// insertComment writes a comment inside the caller's transaction, for a
// change that must not be recorded without it, addressed by who wrote it.
func insertComment(ctx context.Context, q db.Querier, ticketID int64, by Author, body string) (*Comment, error) {
	target, err := commentTargetFor(ctx, q, ticketID, by)
	if err != nil {
		return nil, err
	}
	return insertCommentOn(ctx, q, target, by, body, "")
}

// InsertTicketComment is insertComment for callers outside the package: a
// change that must be recorded on the ticket atomically with the write that
// made it.
func InsertTicketComment(ctx context.Context, q db.Querier, ticketID int64, by Author, body string) (*Comment, error) {
	return insertComment(ctx, q, ticketID, by, body)
}

// InsertAttemptComment writes a comment addressed to ref's attempt inside the
// caller's transaction. Unlike an agent's comment through the service, it does
// not depend on the lease the author holds, so it is right in the same
// transaction as an exit that ends that lease.
func InsertAttemptComment(ctx context.Context, q db.Querier, ref *AttemptRef, by Author, body string) (*Comment, error) {
	if body == "" {
		return nil, ErrEmptyComment
	}
	if !by.valid() {
		return nil, ErrInvalidAuthor
	}
	return insertCommentOn(ctx, q, OnAttempt(ref), by, body, "")
}

// insertCommentOn is the only statement that writes a ticket_comments row.
// A non-empty key marks the comment as one a caller may repeat; see
// commentByKey.
func insertCommentOn(ctx context.Context, q db.Querier, target CommentTarget, by Author, body, key string) (*Comment, error) {
	var c Comment
	var onBehalf *uuid.UUID
	var idempotencyKey *string
	if key != "" {
		idempotencyKey = &key
	}
	if by.speaksFor != nil {
		onBehalf = &by.speaksFor.UserID
	}
	err := q.QueryRow(ctx, `
		INSERT INTO ticket_comments (ticket_id, attempt_number, author_agent_id, author_user_id, on_behalf_of_user_id, body, idempotency_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, attempt_number, author_agent_id, author_user_id, on_behalf_of_user_id, body, created_at`,
		target.ticketID, target.attemptNumber(), by.AgentID, by.UserID, onBehalf, body, idempotencyKey).
		Scan(&c.ID, &c.AttemptNumber, &c.AuthorAgentID, &c.AuthorUserID, &c.OnBehalfOfUserID, &c.Body, &c.CreatedAt)
	if err != nil {
		if pgerr.IsForeignKeyViolation(err) {
			return nil, fmt.Errorf("%w: #%d", ErrNotFound, target.ticketID)
		}
		return nil, fmt.Errorf("record comment: %w", err)
	}
	return &c, nil
}

// commentByKey is the comment a caller already wrote under key on the ticket,
// or nil. The caller's transaction holds the claim lock, so no other write of
// the same key can slip in between this read and its insert; the unique index
// is the backstop.
func commentByKey(ctx context.Context, q db.Querier, ticketID int64, key string) (*Comment, error) {
	var c Comment
	err := q.QueryRow(ctx, `
		SELECT id, attempt_number, author_agent_id, author_user_id, on_behalf_of_user_id, body, created_at
		  FROM ticket_comments WHERE ticket_id = $1 AND idempotency_key = $2`, ticketID, key).
		Scan(&c.ID, &c.AttemptNumber, &c.AuthorAgentID, &c.AuthorUserID, &c.OnBehalfOfUserID, &c.Body, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read comment by key: %w", err)
	}
	return &c, nil
}
