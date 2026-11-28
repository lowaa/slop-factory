package local

import (
	"context"
	"fmt"
	"slices"

	"github.com/uptrace/bun"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

// Every write below reads what the rules judge, judges it, and writes, in
// one transaction. sqlitex takes the write lock at BEGIN, so nothing can
// change the state between the judging and the write.

// Create implements tickets.TicketingSystem.
func (s *System) Create(ctx context.Context, by ident.Principal, nt *tickets.NewTicket) (*tickets.Ticket, error) {
	if err := tickets.CheckNew(by, &s.cfg, nt); err != nil {
		return nil, err
	}
	var created *tickets.Ticket
	err := s.write(ctx, func(ctx context.Context, tx bun.Tx) error {
		a, err := actorFor(ctx, tx, by)
		if err != nil {
			return err
		}
		var parent *tickets.Ticket
		if a.ticket != nil {
			if parent, err = loadTicket(ctx, tx, *a.ticket, false); err != nil {
				return err
			}
		}
		id, err := s.insertTicket(ctx, tx, a, nt, parent)
		if err != nil {
			return err
		}
		created, err = loadTicket(ctx, tx, id, true)
		return err
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

func (s *System) insertTicket(ctx context.Context, tx bun.Tx, a *actor, nt *tickets.NewTicket, parent *tickets.Ticket) (int64, error) {
	now := s.now()
	row := &ticketRow{CreatedByAgent: a.agent, ParentID: a.ticket, CreatedAt: now, UpdatedAt: now}
	err := row.fromTicket(&tickets.Ticket{
		Title: nt.Title, Body: nt.Body, Status: tickets.InitialStatus(nt),
		AssigneeID: nt.Assignee, Agent: nt.Agent, Repos: nt.Repos,
		AutoMerge: tickets.InheritedAutoMerge(a.by, nt, parent),
		Urgent:    nt.Urgent, UrgentReason: tickets.UrgentReason(nt.Urgent, nt.UrgentReason),
	})
	if err != nil {
		return 0, err
	}
	if _, err := tx.NewInsert().Model(row).Exec(ctx); err != nil {
		return 0, fmt.Errorf("local: filing ticket: %w", err)
	}
	// Before the blockers' events: a consumer must hear of the ticket first.
	if err := s.event(ctx, tx, a, tickets.EventCreated, row.ID); err != nil {
		return 0, err
	}
	if row.UrgentReason != "" {
		if _, err := s.insertComment(ctx, tx, a, row.ID, urgentComment(row.UrgentReason)); err != nil {
			return 0, err
		}
	}
	seen := map[ident.TicketID]bool{}
	for _, b := range nt.BlockedOn {
		if seen[b] {
			continue
		}
		seen[b] = true
		blocker, err := parseID(b)
		if err != nil {
			return 0, err
		}
		if _, err := s.addBlocker(ctx, tx, a, row.ID, blocker); err != nil {
			return 0, err
		}
	}
	return row.ID, nil
}

func urgentComment(reason string) string { return "Marked urgent: " + reason }

// Update implements tickets.TicketingSystem.
func (s *System) Update(ctx context.Context, by ident.Principal, id ident.TicketID, change *tickets.TicketChange) (*tickets.Ticket, error) {
	n, err := parseID(id)
	if err != nil {
		return nil, err
	}
	var updated *tickets.Ticket
	err = s.write(ctx, func(ctx context.Context, tx bun.Tx) error {
		a, err := actorFor(ctx, tx, by)
		if err != nil {
			return err
		}
		cur, err := loadTicket(ctx, tx, n, false)
		if err != nil {
			return err
		}
		if err := tickets.CheckChange(by, &s.cfg, cur, change); err != nil {
			return err
		}
		next := applyChange(cur, change)
		if err := s.writeChange(ctx, tx, a, n, cur, next); err != nil {
			return err
		}
		updated, err = loadTicket(ctx, tx, n, true)
		return err
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// applyChange is cur with change applied, cur left as it is.
func applyChange(cur *tickets.Ticket, change *tickets.TicketChange) *tickets.Ticket {
	next := *cur
	if change.Title != nil {
		next.Title = *change.Title
	}
	if change.Body != nil {
		next.Body = *change.Body
	}
	if change.Status != nil {
		next.Status = *change.Status
	}
	if change.Agent != nil {
		next.Agent = *change.Agent
	}
	if change.AutoMerge != nil {
		next.AutoMerge = *change.AutoMerge
	}
	if change.Urgent != nil {
		next.Urgent = change.Urgent.Urgent
		next.UrgentReason = tickets.UrgentReason(change.Urgent.Urgent, change.Urgent.Reason)
	}
	if change.Repos != nil {
		next.Repos = slices.Clone(*change.Repos)
	}
	return &next
}

// writeChange stores next over cur and records what goes with it. A change
// that changes nothing records nothing.
func (s *System) writeChange(ctx context.Context, tx bun.Tx, a *actor, id int64, cur, next *tickets.Ticket) error {
	if sameEditable(cur, next) {
		return nil
	}
	row := &ticketRow{ID: id, UpdatedAt: s.now()}
	if err := row.fromTicket(next); err != nil {
		return err
	}
	_, err := tx.NewUpdate().Model(row).
		Column("title", "body", "status", "harness", "model", "effort",
			"auto_merge", "urgent", "urgent_reason", "repos", "updated_at").
		WherePK().Exec(ctx)
	if err != nil {
		return fmt.Errorf("local: updating ticket %d: %w", id, err)
	}
	urgentNews := next.Urgent && next.UrgentReason != "" &&
		(!cur.Urgent || cur.UrgentReason != next.UrgentReason)
	if urgentNews {
		if _, err := s.insertComment(ctx, tx, a, id, urgentComment(next.UrgentReason)); err != nil {
			return err
		}
	}
	if err := s.event(ctx, tx, a, tickets.EventUpdated, id); err != nil {
		return err
	}
	if (cur.Status == tickets.StatusDone) != (next.Status == tickets.StatusDone) {
		return s.blockerFinishedOrReopened(ctx, tx, a, id)
	}
	return nil
}

func sameEditable(a, b *tickets.Ticket) bool {
	return a.Title == b.Title && a.Body == b.Body && a.Status == b.Status && a.Agent == b.Agent &&
		a.AutoMerge == b.AutoMerge && a.Urgent == b.Urgent && a.UrgentReason == b.UrgentReason &&
		slices.Equal(a.Repos, b.Repos)
}

// Assign implements tickets.TicketingSystem.
func (s *System) Assign(ctx context.Context, by ident.Principal, id ident.TicketID, assignee tickets.UserID) (*tickets.Ticket, error) {
	if err := tickets.CheckAssign(by); err != nil {
		return nil, err
	}
	n, err := parseID(id)
	if err != nil {
		return nil, err
	}
	var assigned *tickets.Ticket
	err = s.write(ctx, func(ctx context.Context, tx bun.Tx) error {
		a, err := actorFor(ctx, tx, by)
		if err != nil {
			return err
		}
		if assigned, err = loadTicket(ctx, tx, n, true); err != nil || assigned.AssigneeID == assignee {
			return err
		}
		_, err = tx.NewUpdate().Model((*ticketRow)(nil)).
			Set("assignee = ?", string(assignee)).Set("updated_at = ?", s.now()).
			Where("id = ?", n).Exec(ctx)
		if err != nil {
			return fmt.Errorf("local: assigning ticket %d: %w", n, err)
		}
		if err := s.event(ctx, tx, a, tickets.EventAssigned, n); err != nil {
			return err
		}
		assigned, err = loadTicket(ctx, tx, n, true)
		return err
	})
	if err != nil {
		return nil, err
	}
	return assigned, nil
}

// Comment implements tickets.TicketingSystem.
func (s *System) Comment(ctx context.Context, by ident.Principal, id ident.TicketID, body string) (*tickets.Comment, error) {
	if err := tickets.CheckComment(by, body); err != nil {
		return nil, err
	}
	n, err := parseID(id)
	if err != nil {
		return nil, err
	}
	var row *commentRow
	err = s.write(ctx, func(ctx context.Context, tx bun.Tx) error {
		a, err := actorFor(ctx, tx, by)
		if err != nil {
			return err
		}
		if _, err := loadTicket(ctx, tx, n, false); err != nil {
			return err
		}
		if row, err = s.insertComment(ctx, tx, a, n, body); err != nil {
			return err
		}
		return s.event(ctx, tx, a, tickets.EventCommented, n)
	})
	if err != nil {
		return nil, err
	}
	return row.toComment(), nil
}
