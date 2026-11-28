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

// openBlockerWhere is what makes a blocker open: anything but done. A
// cancelled blocker is open, so the ticket stays blocked until a person
// decides what it should wait on instead.
const openBlockerWhere = "t.status <> 'done'"

// Get implements tickets.TicketingSystem.
func (s *System) Get(ctx context.Context, id ident.TicketID) (*tickets.Ticket, error) {
	n, err := parseID(id)
	if err != nil {
		return nil, err
	}
	var t *tickets.Ticket
	err = s.read(ctx, func(ctx context.Context, tx bun.Tx) error {
		t, err = loadTicket(ctx, tx, n, true)
		return err
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

// loadTicket reads one ticket with its open blockers, and its comments if
// asked.
func loadTicket(ctx context.Context, db bun.IDB, id int64, withComments bool) (*tickets.Ticket, error) {
	row := &ticketRow{}
	if err := db.NewSelect().Model(row).Where("id = ?", id).Scan(ctx); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: ticket %d", tickets.ErrNotFound, id)
		}
		return nil, fmt.Errorf("local: reading ticket %d: %w", id, err)
	}
	t, err := row.toTicket()
	if err != nil {
		return nil, err
	}
	open, err := openBlockers(ctx, db, []int64{id})
	if err != nil {
		return nil, err
	}
	t.BlockedBy = open[id]
	if withComments {
		if t.Comments, err = comments(ctx, db, id); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// openBlockers maps each of ids to its open blockers, oldest ticket first.
func openBlockers(ctx context.Context, db bun.IDB, ids []int64) (map[int64][]tickets.TicketRef, error) {
	var rows []refRow
	err := db.NewRaw(`
		SELECT b.ticket_id AS owner, t.id, t.title, t.status
		  FROM blockers b JOIN tickets t ON t.id = b.blocker_id
		 WHERE b.ticket_id IN (?) AND `+openBlockerWhere+`
		 ORDER BY t.id`, bun.List(ids)).Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("local: reading open blockers: %w", err)
	}
	out := make(map[int64][]tickets.TicketRef, len(ids))
	for i := range rows {
		out[rows[i].Owner] = append(out[rows[i].Owner], rows[i].toRef())
	}
	return out, nil
}

func comments(ctx context.Context, db bun.IDB, ticket int64) ([]tickets.Comment, error) {
	var rows []commentRow
	if err := db.NewSelect().Model(&rows).Where("ticket_id = ?", ticket).Order("id").Scan(ctx); err != nil {
		return nil, fmt.Errorf("local: reading comments of %d: %w", ticket, err)
	}
	out := make([]tickets.Comment, len(rows))
	for i := range rows {
		out[i] = *rows[i].toComment()
	}
	return out, nil
}

// List implements tickets.TicketingSystem.
func (s *System) List(ctx context.Context, q *tickets.Query) ([]*tickets.Ticket, error) {
	var out []*tickets.Ticket
	err := s.read(ctx, func(ctx context.Context, tx bun.Tx) error {
		var err error
		out, err = list(ctx, tx, q)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func list(ctx context.Context, db bun.IDB, q *tickets.Query) ([]*tickets.Ticket, error) {
	var rows []ticketRow
	sel := db.NewSelect().Model(&rows).Order("id")
	if q != nil {
		applyQuery(sel, q)
	}
	if err := sel.Scan(ctx); err != nil {
		return nil, fmt.Errorf("local: listing tickets: %w", err)
	}
	if len(rows) == 0 {
		return []*tickets.Ticket{}, nil
	}
	ids := make([]int64, len(rows))
	for i := range rows {
		ids[i] = rows[i].ID
	}
	open, err := openBlockers(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	out := make([]*tickets.Ticket, len(rows))
	for i := range rows {
		if out[i], err = rows[i].toTicket(); err != nil {
			return nil, err
		}
		out[i].BlockedBy = open[rows[i].ID]
	}
	return out, nil
}

func applyQuery(sel *bun.SelectQuery, q *tickets.Query) {
	if len(q.Statuses) > 0 {
		sel.Where("status IN (?)", bun.List(q.Statuses))
	}
	if q.Assignee != nil {
		sel.Where("assignee = ?", string(*q.Assignee))
	}
	if q.Urgent != nil {
		sel.Where("urgent = ?", *q.Urgent)
	}
	if q.AgentCreated != nil {
		if *q.AgentCreated {
			sel.Where("created_by_agent IS NOT NULL")
		} else {
			sel.Where("created_by_agent IS NULL")
		}
	}
}

// CountOutstandingAgentCreated implements tickets.TicketingSystem.
func (s *System) CountOutstandingAgentCreated(ctx context.Context) (int, error) {
	n, err := s.db.NewSelect().Model((*ticketRow)(nil)).
		Where("created_by_agent IS NOT NULL").
		Where("status NOT IN (?)", bun.List([]tickets.Status{tickets.StatusDone, tickets.StatusCancelled})).
		Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("local: counting agent-filed tickets: %w", err)
	}
	return int(n), nil
}

// Blockers implements tickets.TicketingSystem.
func (s *System) Blockers(ctx context.Context, id ident.TicketID) (*tickets.BlockerSet, error) {
	n, err := parseID(id)
	if err != nil {
		return nil, err
	}
	var set *tickets.BlockerSet
	// One read transaction, so both directions are of the same graph.
	err = s.read(ctx, func(ctx context.Context, tx bun.Tx) error {
		if _, err := loadTicket(ctx, tx, n, false); err != nil {
			return err
		}
		by, err := edges(ctx, tx, blockedByQuery, n)
		if err != nil {
			return err
		}
		blocks, err := edges(ctx, tx, blocksQuery, n)
		set = &tickets.BlockerSet{BlockedBy: by, Blocks: blocks}
		return err
	})
	if err != nil {
		return nil, err
	}
	return set, nil
}

// The two directions of a ticket's edges, each joining the far end as t.
const (
	blockedByQuery = `
		SELECT t.id, t.title, t.status, b.added_by_agent, b.added_by_ticket
		  FROM blockers b JOIN tickets t ON t.id = b.blocker_id
		 WHERE b.ticket_id = ? ORDER BY t.id`
	blocksQuery = `
		SELECT t.id, t.title, t.status, b.added_by_agent, b.added_by_ticket
		  FROM blockers b JOIN tickets t ON t.id = b.ticket_id
		 WHERE b.blocker_id = ? ORDER BY t.id`
)

func edges(ctx context.Context, db bun.IDB, query string, id int64) ([]tickets.BlockerEdge, error) {
	var rows []refRow
	err := db.NewRaw(query, id).Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("local: reading blocker edges of %d: %w", id, err)
	}
	out := make([]tickets.BlockerEdge, len(rows))
	for i := range rows {
		out[i] = rows[i].toEdge()
	}
	return out, nil
}
