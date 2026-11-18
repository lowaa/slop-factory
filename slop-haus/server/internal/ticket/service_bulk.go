package ticket

import (
	"context"
	"fmt"
	"slices"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// MoveMany is the board's status edit on several tickets at once, under the
// same rules as Update's: all of them move, or -- if any is refused --
// none do, and the *BatchRefusedError names every refusal.
func (s *Service) MoveMany(ctx context.Context, ids []int64, status Status) ([]Ticket, error) {
	if !status.Valid() {
		return nil, fmt.Errorf("%w: status %q", ErrInvalidEnum, status)
	}
	ids, err := s.batch(ids)
	if err != nil {
		return nil, err
	}
	return s.moveTickets(ctx, ids, MoveTo(status))
}

// MarkReadyMany is MarkReady on several Preparing tickets at once: all of
// them enter the queue, or none do.
func (s *Service) MarkReadyMany(ctx context.Context, ids []int64) ([]Ticket, error) {
	ids, err := s.batch(ids)
	if err != nil {
		return nil, err
	}
	return s.moveTickets(ctx, ids, MoveReady())
}

// moveTickets makes move on every ticket in ids, or on none of them, and
// returns the moved tickets in the order ids names them.
//
// In one transaction under the claim lock, for the whole batch: the
// statuses it writes are what Claim reads, and a claim between two of its
// tickets would see half a batch.
func (s *Service) moveTickets(ctx context.Context, ids []int64, move Move) ([]Ticket, error) {
	var moved []Ticket
	var before []Ticket
	err := s.store.InTransition(ctx, func(tx TransitionTx) error {
		var err error
		if before, err = tx.Tickets(ctx, ids); err != nil {
			return err
		}
		if err := makeMove(ctx, tx, ids, move); err != nil {
			return err
		}
		loaded, err := tx.Tickets(ctx, ids)
		if err != nil {
			return err
		}
		moved, err = inOrder(ids, loaded)
		return err
	})
	if err != nil {
		return nil, err
	}
	for i := range moved {
		for j := range before {
			if before[j].ID == moved[i].ID && wasDrafted(before[j].Status) {
				s.noteBehind(ctx, &moved[i])
			}
		}
	}
	return moved, nil
}

// makeMove loads every ticket in ids, decides move on each, and makes the
// batch -- or, refusing any, writes nothing. See Move.decide, applyBatch
// and movedTickets.refuseGainedConflicts.
func makeMove(ctx context.Context, tx TransitionTx, ids []int64, move Move) error {
	moved, err := applyMove(ctx, tx, ids, move)
	if err != nil {
		return err
	}
	return moved.refuseGainedConflicts(ctx, tx)
}

// applyMove is makeMove without the scope check, for a caller that writes
// more before checking the ticket as it finally lands. It refuses only by
// Move.decide's rules.
func applyMove(ctx context.Context, tx TransitionTx, ids []int64, move Move) (*movedTickets, error) {
	current, err := tx.Tickets(ctx, ids)
	if err != nil {
		return nil, err
	}
	batch, err := move.decide(ids, current)
	if err != nil {
		return nil, err
	}
	// Read before the writes: whether a ticket held its scope before the
	// move is what refuseGainedConflicts judges by.
	held, err := tx.HoldingScope(ctx, ids)
	if err != nil {
		return nil, err
	}
	if err := applyBatch(ctx, tx, batch); err != nil {
		return nil, err
	}
	from := make(map[int64]Status, len(current))
	for _, t := range current {
		from[t.ID] = t.Status
	}
	return &movedTickets{move: move, from: from, heldBefore: held}, nil
}

// movedTickets is a batch applyMove has written, with what its tickets
// were before.
type movedTickets struct {
	move       Move
	from       map[int64]Status
	heldBefore map[int64]bool
}

// refuseGainedConflicts refuses the batch if any ticket it made hold a
// scope -- holding none before, one after: moved into review -- overlaps a
// scope another ticket holds, naming every such ticket in a
// *BatchRefusedError. Two tickets of the batch that overlap each other are
// both refused.
//
// Only tickets that gain a hold are checked: an overlap already on the
// board is not this move's doing, and refusing a move out of it -- a ticket
// taken out of review, say -- would refuse the cleanup. A move's other
// rules are judged before anything is written (Move.decide); this runs
// after, on the state the move leaves, so it is one rule for single and
// bulk moves alike.
func (m *movedTickets) refuseGainedConflicts(ctx context.Context, tx TransitionTx) error {
	var gained []int64
	for id := range m.from {
		if !m.heldBefore[id] {
			gained = append(gained, id)
		}
	}
	if len(gained) == 0 {
		return nil
	}
	conflicts, err := tx.ScopeConflicts(ctx, gained)
	if err != nil || len(conflicts) == 0 {
		return err
	}
	refusals := make([]Refusal, len(conflicts))
	for i, b := range conflicts {
		// A conflict with another ticket that gained its hold is with one
		// this same move took into review: not another agent's, and only
		// moving them apart resolves it.
		inBatch := slices.Contains(gained, b.ConflictingTicketID)
		refusals[i] = Refusal{
			TicketID: b.TicketID,
			From:     m.from[b.TicketID],
			Err:      &ScopeConflictError{Blocked: b, InBatch: inBatch},
			move:     &m.move,
		}
	}
	return &BatchRefusedError{Refusals: refusals}
}

// SetPriorityMany sets several tickets' priority at once: all of them, or
// none.
func (s *Service) SetPriorityMany(ctx context.Context, ids []int64, priority model.Priority) ([]Ticket, error) {
	if !priority.Valid() {
		return nil, fmt.Errorf("%w: priority %q", ErrInvalidEnum, priority)
	}
	ids, err := s.batch(ids)
	if err != nil {
		return nil, err
	}
	return s.store.SetPriority(ctx, ids, priority)
}

// batch is ids as a bulk action takes them: at least one, each once, and
// no more than the cap. Duplicates are dropped rather than refused -- the
// board naming a ticket twice still means one move -- and dropped before
// the cap is applied, since the cap is about how many tickets are written.
func (s *Service) batch(ids []int64) ([]int64, error) {
	ids = uniqueOf(ids)
	if len(ids) == 0 {
		return nil, ErrEmptyBatch
	}
	if len(ids) > s.bulkMax {
		return nil, fmt.Errorf("%w: %d tickets, at most %d", ErrBatchTooLarge, len(ids), s.bulkMax)
	}
	return ids, nil
}
