package local

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/uptrace/bun"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

// Lifted from slop-haus (internal/ticket/blockers*.go): the permission
// rules, the cycle check committing with the insert, a comment recording
// each change as whoever made it, and a no-op recording nothing.

// AddBlocker implements tickets.TicketingSystem.
func (s *System) AddBlocker(ctx context.Context, by ident.Principal, ticket, blocker ident.TicketID) (*tickets.BlockerChange, error) {
	return s.editBlockers(ctx, by, ticket, blocker, s.addBlocker)
}

// RemoveBlocker implements tickets.TicketingSystem.
func (s *System) RemoveBlocker(ctx context.Context, by ident.Principal, ticket, blocker ident.TicketID) (*tickets.BlockerChange, error) {
	return s.editBlockers(ctx, by, ticket, blocker, s.removeBlocker)
}

type edgeEdit func(ctx context.Context, tx bun.Tx, a *actor, ticket, blocker int64) (bool, error)

// editBlockers is the frame both edits share: the permission check and the
// edit in one transaction, and the ticket as the edit left it.
func (s *System) editBlockers(ctx context.Context, by ident.Principal, ticket, blocker ident.TicketID, edit edgeEdit) (*tickets.BlockerChange, error) {
	tid, err := parseID(ticket)
	if err != nil {
		return nil, err
	}
	bid, err := parseID(blocker)
	if err != nil {
		return nil, err
	}
	change := &tickets.BlockerChange{}
	err = s.write(ctx, func(ctx context.Context, tx bun.Tx) error {
		a, err := actorFor(ctx, tx, by)
		if err != nil {
			return err
		}
		t, err := loadTicket(ctx, tx, tid, false)
		if err != nil {
			return err
		}
		if err := tickets.CheckBlockerEdit(by, t); err != nil {
			return err
		}
		if change.Changed, err = edit(ctx, tx, a, tid, bid); err != nil {
			return err
		}
		change.Ticket, err = loadTicket(ctx, tx, tid, true)
		return err
	})
	if err != nil {
		return nil, err
	}
	return change, nil
}

// addBlocker inserts the edge ticket -> blocker, refusing one that would
// close a cycle, and reports whether it is new. Every edge comes through
// here, Create's BlockedOn included, so no route can skip the check.
//
// The check is sound only because nothing else can insert an edge between
// it and the insert: the caller's transaction holds SQLite's write lock.
// Two opposite edges added at once would each look acyclic alone.
func (s *System) addBlocker(ctx context.Context, tx bun.Tx, a *actor, ticket, blocker int64) (bool, error) {
	// Read first so a missing blocker is named on its own: the foreign key
	// can only say that one of the two ends is missing.
	b, err := loadTicket(ctx, tx, blocker, false)
	if err != nil {
		return false, err
	}
	if ticket == blocker {
		return false, fmt.Errorf("%w: %d cannot wait on itself", tickets.ErrCycle, ticket)
	}
	// The new edge closes a cycle exactly when blocker already reaches ticket.
	if reaches, err := waitsOnTransitively(ctx, tx, blocker, ticket); err != nil || reaches {
		if err == nil {
			err = fmt.Errorf("%w: %d already waits on %d", tickets.ErrCycle, blocker, ticket)
		}
		return false, err
	}
	// DO NOTHING keeps whoever added an existing edge as its owner: an agent
	// re-adding a person's edge must not gain the right to remove it.
	res, err := tx.NewInsert().Model(&blockerRow{
		TicketID: ticket, BlockerID: blocker, AddedByAgent: a.agent, AddedByTicket: a.ticket, CreatedAt: s.now(),
	}).On("CONFLICT DO NOTHING").Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("local: adding blocker: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return false, err
	}
	if _, err := s.insertComment(ctx, tx, a, ticket, fmt.Sprintf("Blocked on #%d: %s", blocker, b.Title)); err != nil {
		return false, err
	}
	return true, s.event(ctx, tx, a, tickets.EventBlockersChanged, ticket)
}

// removeBlocker deletes the edge, if there is one and by may remove it.
func (s *System) removeBlocker(ctx context.Context, tx bun.Tx, a *actor, ticket, blocker int64) (bool, error) {
	edge := &blockerRow{}
	err := tx.NewSelect().Model(edge).Where("ticket_id = ? AND blocker_id = ?", ticket, blocker).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("local: reading blocker: %w", err)
	}
	if err := tickets.CheckBlockerRemoval(a.by, principalOf(edge.AddedByAgent, edge.AddedByTicket)); err != nil {
		return false, err
	}
	if _, err := tx.NewDelete().Model(edge).WherePK().Exec(ctx); err != nil {
		return false, fmt.Errorf("local: removing blocker: %w", err)
	}
	b, err := loadTicket(ctx, tx, blocker, false)
	if err != nil {
		return false, err
	}
	if _, err := s.insertComment(ctx, tx, a, ticket, fmt.Sprintf("No longer blocked on #%d: %s", blocker, b.Title)); err != nil {
		return false, err
	}
	if err := s.event(ctx, tx, a, tickets.EventBlockersChanged, ticket); err != nil {
		return false, err
	}
	if b.Status == tickets.StatusDone {
		return true, nil // a done blocker held nothing up
	}
	return true, s.unblockedIfClear(ctx, tx, a, ticket)
}

// blockerFinishedOrReopened tells every ticket waiting on blocker that its
// open blockers changed, and those it was the last open one of that they
// are unblocked.
func (s *System) blockerFinishedOrReopened(ctx context.Context, tx bun.Tx, a *actor, blocker int64) error {
	var waiting []int64
	err := tx.NewSelect().Model((*blockerRow)(nil)).Column("ticket_id").
		Where("blocker_id = ?", blocker).Order("ticket_id").Scan(ctx, &waiting)
	if err != nil {
		return fmt.Errorf("local: reading tickets waiting on %d: %w", blocker, err)
	}
	for _, id := range waiting {
		if err := s.event(ctx, tx, a, tickets.EventBlockersChanged, id); err != nil {
			return err
		}
		if err := s.unblockedIfClear(ctx, tx, a, id); err != nil {
			return err
		}
	}
	return nil
}

// unblockedIfClear sends EventUnblocked for a live ticket with no open
// blockers left.
func (s *System) unblockedIfClear(ctx context.Context, tx bun.Tx, a *actor, ticket int64) error {
	t, err := loadTicket(ctx, tx, ticket, false)
	if err != nil {
		return err
	}
	if len(t.BlockedBy) > 0 || t.Status.Terminal() {
		return nil
	}
	return s.event(ctx, tx, a, tickets.EventUnblocked, ticket)
}

// waitsOnTransitively reports whether from waits on to, directly or not.
func waitsOnTransitively(ctx context.Context, tx bun.IDB, from, to int64) (bool, error) {
	var found bool
	err := tx.NewRaw(`
		WITH RECURSIVE reachable(id) AS (
			SELECT blocker_id FROM blockers WHERE ticket_id = ?
			UNION
			SELECT b.blocker_id FROM blockers b JOIN reachable r ON b.ticket_id = r.id
		)
		SELECT EXISTS (SELECT 1 FROM reachable WHERE id = ?)`, from, to).Scan(ctx, &found)
	if err != nil {
		return false, fmt.Errorf("local: walking blockers: %w", err)
	}
	return found, nil
}
