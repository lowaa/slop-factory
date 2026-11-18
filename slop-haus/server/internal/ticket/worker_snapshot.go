package ticket

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// WorkerSnapshot is where an agent's latest lease stands, held or not. A
// headless run's supervisor reads it after the run exits, when the lease may
// already be gone because the run ended it (review, discussion, done): the
// ticket's and the attempt's status then say how.
type WorkerSnapshot struct {
	LeaseID  int64
	TicketID int64
	Attempt  int
	// Active is whether the lease is still held.
	Active    bool
	ExpiresAt time.Time
	// TicketStatus and AttemptStatus are as they are now, not as the lease
	// left them: a human may have moved the ticket since.
	TicketStatus  Status
	AttemptStatus string
	Promoted      bool
	// Session is what a run reported on the attempt; nil when none has.
	Session *AttemptSession
}

// WorkerSnapshot reads the agent's latest lease with its ticket and attempt,
// or fails with ErrNoLease for an agent that never held one. A read: it
// extends nothing and takes no revocation notice, so polling it cannot keep a
// lease alive nor swallow the notice an MCP call is owed.
func (s *PostgresStore) WorkerSnapshot(ctx context.Context, agentID uuid.UUID) (*WorkerSnapshot, error) {
	var w WorkerSnapshot
	var status string
	var attemptStatus, harness, sessionID *string
	err := s.db.QueryRow(ctx, `
		SELECT l.id, l.ticket_id, l.attempt_number, (`+activeLeaseClause+`), l.expires_at,
		       t.status, a.status, COALESCE(a.promoted_at IS NOT NULL, false), a.harness, a.harness_session_id
		  FROM leases l
		  JOIN tickets t ON t.id = l.ticket_id
		  LEFT JOIN attempts a ON a.ticket_id = l.ticket_id AND a.number = l.attempt_number
		 WHERE l.agent_id = $1
		 ORDER BY l.id DESC LIMIT 1`, agentID).
		Scan(&w.LeaseID, &w.TicketID, &w.Attempt, &w.Active, &w.ExpiresAt, &status, &attemptStatus, &w.Promoted, &harness, &sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoLease
	}
	if err != nil {
		return nil, fmt.Errorf("load worker snapshot: %w", err)
	}
	w.TicketStatus = Status(status)
	if attemptStatus != nil {
		w.AttemptStatus = *attemptStatus
	}
	if harness != nil && sessionID != nil {
		w.Session = &AttemptSession{Harness: *harness, SessionID: *sessionID}
	}
	return &w, nil
}
