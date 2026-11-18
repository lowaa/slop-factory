package ticket

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// Conversation is whose comments and handoffs a read of a ticket shows. A
// ticket's own (from preparing, and the guidance given to every attempt) are
// everyone's; an attempt's are its holder's alone, and no agent is shown a
// sibling's. People on the board see everything.
type Conversation struct {
	// Everything is every attempt's: the board's view.
	Everything bool
	// Attempt is the attempt shown beside the ticket's own, when Everything
	// is not set. Nil shows the ticket's own alone.
	Attempt *int
	// Replay shows the ticket's own as its snapshot froze it, whatever was
	// edited or said since: a replay is not told what the original attempt
	// went on to learn.
	Replay bool
}

// EveryConversation is the board's view of a ticket.
func EveryConversation() Conversation { return Conversation{Everything: true} }

// ConversationOf is what agentID is shown of ticketID's conversation:
//   - the attempt it holds, else the one it is owed back (a discussion or
//     review it left, awaiting its answer), with the ticket's own;
//   - everything on a ticket with a single attempt, which has no sibling
//     whose words could leak;
//   - the ticket's own alone otherwise: an agent with no attempt of its own on
//     a ticket with several is not shown any one of them.
//
// An attempt is owed to the agent only by an explicit match: an attempt owed
// to nobody (resume_agent_id NULL) is no agent's.
func (s *PostgresStore) ConversationOf(ctx context.Context, ticketID int64, agentID uuid.UUID) (Conversation, error) {
	return conversationOf(ctx, s.db, ticketID, agentID)
}

func conversationOf(ctx context.Context, q db.Querier, ticketID int64, agentID uuid.UUID) (Conversation, error) {
	held, err := heldAttempt(ctx, q, agentID, ticketID)
	if err == nil {
		return attemptConversation(ctx, q, held)
	}
	if !errors.Is(err, ErrNoLease) {
		return Conversation{}, err
	}
	var count int
	var owed []int
	rows, err := q.Query(ctx, `
		SELECT number, resume_agent_id = $2 FROM attempts WHERE ticket_id = $1 ORDER BY number`,
		ticketID, agentID)
	if err != nil {
		return Conversation{}, fmt.Errorf("read the attempts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var number int
		var isOwed *bool
		if err := rows.Scan(&number, &isOwed); err != nil {
			return Conversation{}, fmt.Errorf("read the attempts: %w", err)
		}
		count++
		if isOwed != nil && *isOwed {
			owed = append(owed, number)
		}
	}
	if err := rows.Err(); err != nil {
		return Conversation{}, fmt.Errorf("read the attempts: %w", err)
	}
	switch {
	case count <= 1:
		return EveryConversation(), nil
	case len(owed) == 1:
		return attemptConversation(ctx, q, &AttemptRef{TicketID: ticketID, Number: owed[0]})
	}
	return Conversation{}, nil
}

// TicketSeenBy is the ticket as an agent in conv sees it: with several
// attempts, the fields that live on an attempt (its PR, its review) are its
// own and not the promoted attempt's, so a sibling's approval is not read as
// the agent's. Whatever else conv shows, the ticket alone is the same.
func (s *PostgresStore) TicketSeenBy(ctx context.Context, ticketID int64, conv Conversation) (*Ticket, error) {
	return ticketSeenBy(ctx, s.db, ticketID, conv)
}

func ticketSeenBy(ctx context.Context, q db.Querier, ticketID int64, conv Conversation) (*Ticket, error) {
	t, err := liveTicketSeenBy(ctx, q, ticketID, conv)
	if err != nil || !conv.Replay {
		return t, err
	}
	snap, err := latestSnapshot(ctx, q, ticketID)
	if err != nil {
		return nil, err
	}
	snap.frozen(t)
	return t, nil
}

func liveTicketSeenBy(ctx context.Context, q db.Querier, ticketID int64, conv Conversation) (*Ticket, error) {
	if conv.Everything {
		return loadTicket(ctx, q, ticketID)
	}
	var attempts int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM attempts WHERE ticket_id = $1`, ticketID).Scan(&attempts); err != nil {
		return nil, fmt.Errorf("count the attempts: %w", err)
	}
	if attempts <= 1 {
		return loadTicket(ctx, q, ticketID)
	}
	if conv.Attempt == nil {
		// An agent with no attempt of its own is not shown the promoted one's
		// PR or review as though they were the ticket's.
		t, err := loadTicket(ctx, q, ticketID)
		if err != nil {
			return nil, err
		}
		t.PRURL, t.ResumeAgentID, t.AffinityUntil = nil, nil, nil
		t.ApprovedAt, t.ApprovedByUserID, t.ApprovedByDeleted, t.ChangesRequestedAt, t.ReviewStartedAt = nil, nil, false, nil, nil
		return t, nil
	}
	return loadTicketOnAttempt(ctx, q, &AttemptRef{TicketID: ticketID, Number: *conv.Attempt})
}

// ListCommentsIn is a ticket's comments as conv shows them, oldest first.
func (s *PostgresStore) ListCommentsIn(ctx context.Context, ticketID int64, conv Conversation) ([]Comment, error) {
	// The ticket's own comments a replay is shown stop where the snapshot's
	// conversation did; its attempt's own are its own.
	var through *int64
	if conv.Replay {
		snap, err := latestSnapshot(ctx, s.db, ticketID)
		if err != nil {
			return nil, err
		}
		through = &snap.CommentsThroughID
	}
	rows, err := s.db.Query(ctx, `
		SELECT id, attempt_number, author_agent_id, author_user_id, author_user_id_deleted, on_behalf_of_user_id, body, created_at FROM ticket_comments
		 WHERE ticket_id = $1 AND ($2 OR (attempt_number IS NULL AND ($4::bigint IS NULL OR id <= $4)) OR attempt_number = $3)
		 ORDER BY id`, ticketID, conv.Everything, conv.Attempt, through)
	if err != nil {
		return nil, fmt.Errorf("list comments: %w", err)
	}
	defer rows.Close()

	comments := []Comment{}
	for rows.Next() {
		var c Comment
		if err := rows.Scan(&c.ID, &c.AttemptNumber, &c.AuthorAgentID, &c.AuthorUserID, &c.AuthorUserDeleted, &c.OnBehalfOfUserID, &c.Body, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan comment: %w", err)
		}
		comments = append(comments, c)
	}
	return comments, rows.Err()
}

// ListAgentContextsIn is a ticket's handoffs as conv shows them, oldest first.
// A handoff is always an attempt's, so an agent in no attempt's conversation
// is shown none.
func (s *PostgresStore) ListAgentContextsIn(ctx context.Context, ticketID int64, conv Conversation) ([]AgentContext, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, ticket_id, attempt_number, agent_id, body, created_at FROM agent_contexts
		 WHERE ticket_id = $1 AND ($2 OR attempt_number = $3)
		 ORDER BY id`, ticketID, conv.Everything, conv.Attempt)
	if err != nil {
		return nil, fmt.Errorf("list agent contexts: %w", err)
	}
	defer rows.Close()

	contexts := []AgentContext{}
	for rows.Next() {
		var c AgentContext
		if err := rows.Scan(&c.ID, &c.TicketID, &c.AttemptNumber, &c.AgentID, &c.Body, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan agent context: %w", err)
		}
		contexts = append(contexts, c)
	}
	return contexts, rows.Err()
}
