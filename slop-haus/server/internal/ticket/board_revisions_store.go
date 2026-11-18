package ticket

import (
	"context"
	"fmt"
)

var _ BoardRevisions = (*PostgresStore)(nil)

// BoardRevision reads the tenant's board revision, 0 before anything on the
// board has ever changed. The bump_board_revision triggers move it; nothing
// in Go writes it.
func (s *PostgresStore) BoardRevision(ctx context.Context) (int64, error) {
	var revision int64
	err := s.db.QueryRow(ctx, `SELECT COALESCE((SELECT revision FROM board_revisions), 0)`).Scan(&revision)
	if err != nil {
		return 0, fmt.Errorf("board revision: %w", err)
	}
	return revision, nil
}

// BoardDelta is what changed on the board between two revisions: the
// tickets as they stand now, or Reload when that cannot be said. A change to
// any of a ticket's attempts is a change to the ticket, which the board's
// triggers record by ticket (in board_changes).
type BoardDelta struct {
	Tickets []Ticket
	// Cards are the attempt cards of those Tickets that have several
	// attempts, in the work columns: the ones to draw in their place.
	Cards  []Ticket
	Reload bool
}

// maxBoardDeltaTickets is the most tickets a delta names. Past it a reload
// is cheaper for the client than applying that many one at a time.
const maxBoardDeltaTickets = 50

// BoardDelta reports what changed in the revisions after since, up to and
// including upTo, from the board_changes log the triggers keep. It
// asks for a reload when it cannot name the tickets: a change that touched
// no ticket, a ticket that no longer exists, too many tickets, or a log
// that no longer reaches back to since (every revision writes at least one
// row, so a revision without one has been pruned).
//
// The tickets are read after the log, so one may already be newer than upTo;
// the revision that made it newer is sent after, and applies the same ticket
// again.
func (s *PostgresStore) BoardDelta(ctx context.Context, since, upTo int64) (*BoardDelta, error) {
	var ids []int64
	var revisions int64
	err := s.db.QueryRow(ctx, `
		SELECT COALESCE(array_agg(DISTINCT ticket_id), '{}'), count(DISTINCT revision)
		  FROM board_changes WHERE revision > $1 AND revision <= $2`, since, upTo).Scan(&ids, &revisions)
	if err != nil {
		return nil, fmt.Errorf("board changes: %w", err)
	}
	if revisions != upTo-since || len(ids) > maxBoardDeltaTickets || (len(ids) > 0 && ids[0] == 0) {
		return &BoardDelta{Reload: true}, nil
	}
	if len(ids) == 0 {
		return &BoardDelta{}, nil
	}
	tickets, err := s.ListTickets(ctx, &ListFilter{IDs: ids, Summary: true})
	if err != nil {
		return nil, err
	}
	if len(tickets) != len(ids) {
		return &BoardDelta{Reload: true}, nil
	}
	cards, err := s.ListTickets(ctx, &ListFilter{IDs: ids, Summary: true, AttemptCards: true})
	if err != nil {
		return nil, err
	}
	delta := &BoardDelta{Tickets: tickets}
	for _, c := range cards {
		if c.Card.Multiple() {
			delta.Cards = append(delta.Cards, c)
		}
	}
	return delta, nil
}
