package local

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/uptrace/bun"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

type ticketRow struct {
	bun.BaseModel `bun:"table:tickets"`

	ID             int64   `bun:"id,pk,autoincrement"`
	Title          string  `bun:"title"`
	Body           string  `bun:"body"`
	Status         string  `bun:"status"`
	Assignee       string  `bun:"assignee"`
	Harness        string  `bun:"harness"`
	Model          string  `bun:"model"`
	Effort         string  `bun:"effort"`
	AutoMerge      bool    `bun:"auto_merge"`
	Urgent         bool    `bun:"urgent"`
	UrgentReason   string  `bun:"urgent_reason"`
	CreatedByAgent *string `bun:"created_by_agent"`
	ParentID       *int64  `bun:"parent_id"`
	Repos          string  `bun:"repos"`
	CreatedAt      int64   `bun:"created_at"`
	UpdatedAt      int64   `bun:"updated_at"`
}

type commentRow struct {
	bun.BaseModel `bun:"table:comments"`

	ID           int64   `bun:"id,pk,autoincrement"`
	TicketID     int64   `bun:"ticket_id"`
	AuthorAgent  *string `bun:"author_agent"`
	AuthorTicket *int64  `bun:"author_ticket"`
	Body         string  `bun:"body"`
	CreatedAt    int64   `bun:"created_at"`
}

type blockerRow struct {
	bun.BaseModel `bun:"table:blockers"`

	TicketID      int64   `bun:"ticket_id,pk"`
	BlockerID     int64   `bun:"blocker_id,pk"`
	AddedByAgent  *string `bun:"added_by_agent"`
	AddedByTicket *int64  `bun:"added_by_ticket"`
	CreatedAt     int64   `bun:"created_at"`
}

type eventRow struct {
	bun.BaseModel `bun:"table:events"`

	Seq      int64   `bun:"seq,pk,autoincrement"`
	Kind     string  `bun:"kind"`
	TicketID int64   `bun:"ticket_id"`
	ByAgent  *string `bun:"by_agent"`
	ByTicket *int64  `bun:"by_ticket"`
	At       int64   `bun:"at"`
}

// refRow is the other end of a blocker edge, with who added the edge.
type refRow struct {
	Owner         int64   `bun:"owner"`
	ID            int64   `bun:"id"`
	Title         string  `bun:"title"`
	Status        string  `bun:"status"`
	AddedByAgent  *string `bun:"added_by_agent"`
	AddedByTicket *int64  `bun:"added_by_ticket"`
}

// Ticket ids are the rows' integer keys, in decimal.
func ticketID(id int64) ident.TicketID { return ident.TicketID(strconv.FormatInt(id, 10)) }

func parseID(id ident.TicketID) (int64, error) {
	n, err := strconv.ParseInt(string(id), 10, 64)
	// Canonical spellings only: "01" and "+1" are other ids to anything that
	// compares TicketIDs as strings, so they must not reach ticket 1 here.
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != string(id) {
		return 0, fmt.Errorf("%w: ticket %q", tickets.ErrNotFound, id)
	}
	return n, nil
}

func nanos(t time.Time) int64 { return t.UnixNano() }

func fromNanos(n int64) time.Time { return time.Unix(0, n).UTC() }

// principalCols is how a principal is stored: an agent id and the agent's
// ticket, both nil for the user. The principal must already be valid.
func principalCols(by ident.Principal) (*string, *int64, error) {
	if by.IsUser() {
		return nil, nil, nil
	}
	tid, err := parseID(by.Ticket)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: agent %s's ticket %q", tickets.ErrInvalidPrincipal, by.Agent, by.Ticket)
	}
	agent := string(by.Agent)
	return &agent, &tid, nil
}

func principalOf(agent *string, ticket *int64) ident.Principal {
	if agent == nil || ticket == nil {
		return ident.UserPrincipal()
	}
	return ident.AgentPrincipal(ident.AgentID(*agent), ticketID(*ticket))
}

func (r *ticketRow) toTicket() (*tickets.Ticket, error) {
	var repos []string
	if err := json.Unmarshal([]byte(r.Repos), &repos); err != nil {
		return nil, fmt.Errorf("local: ticket %d's repos: %w", r.ID, err)
	}
	t := &tickets.Ticket{
		ID:           ticketID(r.ID),
		Title:        r.Title,
		Body:         r.Body,
		Status:       tickets.Status(r.Status),
		AssigneeID:   tickets.UserID(r.Assignee),
		Agent:        tickets.AgentSpec{Harness: r.Harness, Model: r.Model, Effort: r.Effort},
		AutoMerge:    r.AutoMerge,
		Urgent:       r.Urgent,
		UrgentReason: r.UrgentReason,
		CreatedBy:    principalOf(r.CreatedByAgent, r.ParentID),
		Repos:        repos,
		CreatedAt:    fromNanos(r.CreatedAt),
		UpdatedAt:    fromNanos(r.UpdatedAt),
	}
	if r.ParentID != nil {
		t.Parent = ticketID(*r.ParentID)
	}
	return t, nil
}

// fromTicket writes t's editable fields onto r.
func (r *ticketRow) fromTicket(t *tickets.Ticket) error {
	repos := t.Repos
	if repos == nil {
		repos = []string{}
	}
	raw, err := json.Marshal(repos)
	if err != nil {
		return fmt.Errorf("local: encoding repos: %w", err)
	}
	r.Title, r.Body, r.Status = t.Title, t.Body, string(t.Status)
	r.Assignee = string(t.AssigneeID)
	r.Harness, r.Model, r.Effort = t.Agent.Harness, t.Agent.Model, t.Agent.Effort
	r.AutoMerge, r.Urgent, r.UrgentReason = t.AutoMerge, t.Urgent, t.UrgentReason
	r.Repos = string(raw)
	return nil
}

func (r *commentRow) toComment() *tickets.Comment {
	return &tickets.Comment{
		ID:        strconv.FormatInt(r.ID, 10),
		Ticket:    ticketID(r.TicketID),
		Author:    principalOf(r.AuthorAgent, r.AuthorTicket),
		Body:      r.Body,
		CreatedAt: fromNanos(r.CreatedAt),
	}
}

func (r *refRow) toRef() tickets.TicketRef {
	return tickets.TicketRef{ID: ticketID(r.ID), Title: r.Title, Status: tickets.Status(r.Status)}
}

func (r *refRow) toEdge() tickets.BlockerEdge {
	return tickets.BlockerEdge{Ticket: r.toRef(), AddedBy: principalOf(r.AddedByAgent, r.AddedByTicket)}
}

func (r *eventRow) toEvent() tickets.TicketEvent {
	return tickets.TicketEvent{
		Cursor: encodeCursor(r.Seq),
		Kind:   tickets.EventKind(r.Kind),
		Ticket: ticketID(r.TicketID),
		By:     principalOf(r.ByAgent, r.ByTicket),
		At:     fromNanos(r.At),
	}
}

// Cursors are the event seq, zero-padded so that string order is seq order,
// as tickets.Cursor promises.
func encodeCursor(seq int64) tickets.Cursor { return tickets.Cursor(fmt.Sprintf("%020d", seq)) }

func decodeCursor(c tickets.Cursor) (int64, error) {
	if c == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(string(c), 10, 64)
	if err != nil || n < 0 || len(c) != 20 {
		return 0, fmt.Errorf("%w: %q", tickets.ErrInvalidCursor, c)
	}
	return n, nil
}
