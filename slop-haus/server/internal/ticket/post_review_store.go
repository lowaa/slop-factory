package ticket

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// awaitingPostReview is the WHERE predicate for a ticket that merged without
// a person looking at its change first, and that nobody has marked reviewed
// since. approved_at is checked as well as requires_approval: an agent may
// ask for a review nobody required, and an approved change has been looked
// at either way.
var awaitingPostReview = `t.status = 'done' AND NOT t.requires_approval
	AND NOT ` + promotedAttemptHas("pp.approved_at IS NOT NULL") + ` AND t.post_reviewed_at IS NULL`

// postReviewCleared are the SET clauses that forget a post-merge review.
var postReviewCleared = []string{"post_reviewed_at = NULL", "post_reviewed_by_user_id = NULL", "post_reviewed_by_user_id_deleted = false"}

// Reviewed is per ticket and does not wait for a shadow attempt still running:
// the review is of the change that merged, which is the promoted attempt's. A
// shadow never takes in main, and its own PR is a separate change; waiting
// on it would keep a merged change unreviewed for as long as a sibling runs.
//
// MarkPostReviewed records the review and, when there is one, the comment,
// in one transaction: a comment saying what was found must not land on a
// ticket the mark then refused, where it would read as a review that
// never happened.
//
// No claim lock: nothing Claim reads is written. The conditional UPDATE is
// what keeps a concurrent status edit or a second mark from interleaving --
// the row lock it takes re-checks the WHERE against whichever committed
// first.
func (s *PostgresStore) MarkPostReviewed(ctx context.Context, ticketID int64, userID uuid.UUID, comment string) (*Ticket, error) {
	var marked *Ticket
	err := s.db.InTx(ctx, func(q db.Querier) error {
		tag, err := q.Exec(ctx, `
			UPDATE tickets SET post_reviewed_at = now(), post_reviewed_by_user_id = $2
			 WHERE id = $1 AND status = 'done' AND post_reviewed_at IS NULL`, ticketID, userID)
		if err != nil {
			return fmt.Errorf("mark reviewed: %w", err)
		}
		if tag.RowsAffected() == 0 {
			current, err := loadTicket(ctx, q, ticketID)
			if err != nil {
				return err
			}
			if current.Status != StatusDone {
				return fmt.Errorf("%w: #%d is %s", ErrNotDone, ticketID, current.Status)
			}
			return fmt.Errorf("%w: #%d", ErrAlreadyPostReviewed, ticketID)
		}
		if comment != "" {
			if _, err := insertComment(ctx, q, ticketID, UserAuthor(userID), comment); err != nil {
				return fmt.Errorf("record review comment: %w", err)
			}
		}
		marked, err = loadTicket(ctx, q, ticketID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return marked, nil
}
