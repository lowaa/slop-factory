package ticket

import (
	"context"
	"fmt"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// needs_discussion is the ticket's flag, but whether it still binds an
// attempt is the attempt's: each attempt plans and asks on its own, so one
// asking must not release a sibling that has not planned yet. The flag
// clears for everyone once every live attempt has discussed, and a person
// setting it again starts every attempt's discussion over.

// writeDiscussion carries out the parts of w that span attempts, after the
// ticket's row and the written attempts: the flag clears when the last live
// attempt has discussed, and setting it forgets earlier discussions.
func (t *pgTransition) writeDiscussion(ctx context.Context, ticketID int64, w *TicketWrite) error {
	if w.NeedsDiscussion != nil && *w.NeedsDiscussion {
		if err := forgetDiscussions(ctx, t.q, ticketID); err != nil {
			return err
		}
	}
	if !w.Discussed {
		return nil
	}
	if _, err := t.q.Exec(ctx, `
		UPDATE tickets t SET needs_discussion = false
		 WHERE t.id = $1 AND t.needs_discussion
		   AND NOT EXISTS (SELECT 1 FROM attempts a
		                    WHERE a.ticket_id = t.id AND a.status NOT IN `+AttemptEndedStatusesSQL+`
		                      AND a.discussed_at IS NULL)`, ticketID); err != nil {
		return fmt.Errorf("clear needs_discussion of #%d: %w", ticketID, err)
	}
	return nil
}

func forgetDiscussions(ctx context.Context, q db.Querier, ticketID int64) error {
	if _, err := q.Exec(ctx,
		`UPDATE attempts SET discussed_at = NULL WHERE ticket_id = $1 AND discussed_at IS NOT NULL`, ticketID); err != nil {
		return fmt.Errorf("forget discussions of #%d: %w", ticketID, err)
	}
	return nil
}

// seenByAttempt narrows t.NeedsDiscussion to the attempt: false once that
// attempt has had its discussion, whatever its siblings still owe.
func seenByAttempt(ctx context.Context, q db.Querier, t *Ticket, ref *AttemptRef) error {
	if !t.NeedsDiscussion {
		return nil
	}
	var discussed bool
	if err := q.QueryRow(ctx,
		`SELECT discussed_at IS NOT NULL FROM attempts WHERE ticket_id = $1 AND number = $2`,
		ref.TicketID, ref.Number).Scan(&discussed); err != nil {
		return fmt.Errorf("read the discussion of %s: %w", ref, err)
	}
	t.NeedsDiscussion = !discussed
	return nil
}
