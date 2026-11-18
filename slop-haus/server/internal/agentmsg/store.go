package agentmsg

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// PostgresStore is the Store implementation: SQL and nothing else. Every
// rule about what may be sent lives in Service.
type PostgresStore struct {
	db *db.DB
}

func NewPostgresStore(database *db.DB) *PostgresStore {
	return &PostgresStore{db: database}
}

var _ Store = (*PostgresStore)(nil)

const messageColumns = `m.id, m.agent_id, m.kind, m.author_user_id, m.author_user_id_deleted, coalesce(u.display_name, ''),
	coalesce(v.name, ''), m.ticket_id, m.body, m.created_at, m.delivered_at, m.dropped_at`

func scanMessage(row pgx.Row) (*Message, error) {
	var m Message
	if err := row.Scan(&m.ID, &m.AgentID, &m.Kind, &m.AuthorUserID, &m.AuthorUserDeleted, &m.AuthorName, &m.ViaAgentName,
		&m.TicketID, &m.Body, &m.CreatedAt, &m.DeliveredAt, &m.DroppedAt); err != nil {
		return nil, err
	}
	return &m, nil
}

// Insert stores a message for the agent, unless it is not in the caller's
// tenant (row-level security makes another tenant's agent look absent), is a
// conductor (messages to one are its owner's conversations, not this),
// is disabled, or already has maxPending undelivered.
//
// An advisory lock on the agent serializes this count with every other
// producer for it -- the trigger that copies a person's ticket comment takes
// the same one -- so two at the cap cannot both see room.
func (s *PostgresStore) Insert(ctx context.Context, agentID, authorUserID uuid.UUID, body string, maxPending int) (*Message, error) {
	var inserted *Message
	err := s.db.InTx(ctx, func(tx db.Querier) error {
		var disabled, notARecipient bool
		err := tx.QueryRow(ctx, `SELECT disabled_at IS NOT NULL, kind IN ('conductor', 'supervisor', 'steward') FROM agents WHERE id = $1`, agentID).Scan(&disabled, &notARecipient)
		// A conductor belongs to its owner: a message from anyone else in the
		// tenant would be steering it, which only the owner may do. A supervisor
		// runs no model, so there is nobody to read one; a steward's run is started
		// by the supervisor and takes no messages.
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && notARecipient) {
			return ErrAgentNotFound
		}
		if err != nil {
			return fmt.Errorf("agentmsg: read agent: %w", err)
		}
		if disabled {
			return ErrAgentDisabled
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('agent_messages:' || $1::text, 0))`, agentID); err != nil {
			return fmt.Errorf("agentmsg: lock agent: %w", err)
		}
		var pending int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM agent_messages WHERE agent_id = $1 AND delivered_at IS NULL AND dropped_at IS NULL`, agentID).Scan(&pending); err != nil {
			return fmt.Errorf("agentmsg: count pending: %w", err)
		}
		if pending >= maxPending {
			return ErrTooManyPending
		}
		var id int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO agent_messages (agent_id, kind, author_user_id, body)
			VALUES ($1, 'message', $2, $3) RETURNING id`, agentID, authorUserID, body).Scan(&id); err != nil {
			return fmt.Errorf("agentmsg: insert: %w", err)
		}
		inserted, err = scanMessage(tx.QueryRow(ctx, `
			SELECT `+messageColumns+` FROM agent_messages m LEFT JOIN global_users u ON u.id = m.author_user_id
		  LEFT JOIN agents v ON v.id = m.via_agent_id
			 WHERE m.id = $1`, id))
		return err
	})
	if err != nil {
		return nil, err
	}
	return inserted, nil
}

// Claim marks every deliverable message for the agent delivered and returns
// them, oldest first. A direct message is always deliverable; a ticket
// comment only while the agent still holds that ticket's attempt, so one
// queued for a lease that has since ended (and not yet been closed by the
// schema's trigger) never reaches the agent on another ticket.
//
// One UPDATE is the whole of "exactly once": a concurrent claim blocks on
// the rows this one has locked, and when it resumes they no longer match
// delivered_at IS NULL, so each message goes to exactly one caller. A
// SELECT followed by an UPDATE would hand the same message to both.
func (s *PostgresStore) Claim(ctx context.Context, agentID uuid.UUID) ([]Message, error) {
	rows, err := s.db.Query(ctx, `
		WITH claimed AS (
			UPDATE agent_messages SET delivered_at = now()
			 WHERE agent_id = $1 AND delivered_at IS NULL AND dropped_at IS NULL
			   AND (ticket_id IS NULL OR EXISTS (
			       SELECT 1 FROM leases l
			        WHERE l.tenant_id = agent_messages.tenant_id AND l.agent_id = agent_messages.agent_id
			          AND l.ticket_id = agent_messages.ticket_id AND l.attempt_number = agent_messages.attempt_number
			          AND l.released_at IS NULL))
			RETURNING *)
		SELECT `+messageColumns+`
		  FROM claimed m LEFT JOIN global_users u ON u.id = m.author_user_id
		  LEFT JOIN agents v ON v.id = m.via_agent_id
		 ORDER BY m.id`, agentID)
	if err != nil {
		return nil, fmt.Errorf("agentmsg: claim: %w", err)
	}
	return collect(rows)
}

func (s *PostgresStore) CountPending(ctx context.Context, agentID uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `
		SELECT count(*) FROM agent_messages WHERE agent_id = $1 AND delivered_at IS NULL AND dropped_at IS NULL`, agentID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("agentmsg: count pending: %w", err)
	}
	return n, nil
}

func (s *PostgresStore) Recent(ctx context.Context, limit int) ([]Message, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+messageColumns+`
		  FROM agent_messages m LEFT JOIN global_users u ON u.id = m.author_user_id
		  LEFT JOIN agents v ON v.id = m.via_agent_id
		 ORDER BY m.id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("agentmsg: list: %w", err)
	}
	return collect(rows)
}

func collect(rows pgx.Rows) ([]Message, error) {
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("agentmsg: scan: %w", err)
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}
