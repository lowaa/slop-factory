package reviewreport

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// heldAttempt is the attempt agentID holds a live lease on, and whether a
// supervisor runs agentID.
type heldAttempt struct {
	TicketID   int64
	Attempt    int
	Supervised bool
}

func findHeldAttempt(ctx context.Context, q db.Querier, agentID uuid.UUID) (*heldAttempt, error) {
	var h heldAttempt
	err := q.QueryRow(ctx, `
		SELECT l.ticket_id, l.attempt_number, a.supervisor_agent_id IS NOT NULL
		  FROM leases l
		  JOIN agents a ON a.tenant_id = l.tenant_id AND a.id = l.agent_id
		 WHERE l.agent_id = $1 AND `+ticket.ActiveLeaseClause+`
		 ORDER BY l.id DESC
		 LIMIT 1`, agentID).Scan(&h.TicketID, &h.Attempt, &h.Supervised)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoHeldAttempt
	}
	if err != nil {
		return nil, fmt.Errorf("find the held attempt: %w", err)
	}
	return &h, nil
}

// requestBarrier is a test-only seam between the lease check and the write
// of a request, where the request's transaction has begun and its time has
// not been taken. Production leaves it nil.
var requestBarrier func()

func (s *PostgresStore) RequestReview(ctx context.Context, agentID uuid.UUID, headSHA, intent string) (*Request, error) {
	var out *Request
	err := s.db.InTx(ctx, func(q db.Querier) error {
		held, err := findHeldAttempt(ctx, q, agentID)
		if err != nil {
			return err
		}
		if !held.Supervised {
			return ErrNotSupervised
		}
		if requestBarrier != nil {
			requestBarrier()
		}
		req := &Request{TicketID: held.TicketID, Attempt: held.Attempt, HeadSHA: headSHA, Intent: intent}
		// The time is taken as the row is written, not when the transaction
		// began: a posting that began in between would otherwise count
		// towards a request it never saw (see OpenRequest). Every request is
		// stamped anew, so asking again for the same head is a new request.
		if err := q.QueryRow(ctx, `
			INSERT INTO review_requests (ticket_id, attempt_number, agent_id, head_sha, intent, requested_at)
			VALUES ($1, $2, $3, $4, $5, clock_timestamp())
			ON CONFLICT (tenant_id, ticket_id, attempt_number) DO UPDATE
			   SET agent_id = EXCLUDED.agent_id, head_sha = EXCLUDED.head_sha, intent = EXCLUDED.intent,
			       requested_at = EXCLUDED.requested_at
			RETURNING requested_at`,
			held.TicketID, held.Attempt, agentID, headSHA, intent).Scan(&req.RequestedAt); err != nil {
			return fmt.Errorf("store review request: %w", err)
		}
		out = req
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// OpenRequest reads closure from the runs themselves: a request is open until
// the supervisor-posted review runs of its attempt and head, received since
// it was made, name every required persona between them, whatever their
// status -- a failed run is the supervisor's answer too, which the agent
// reads with review_findings. A run counts only if received at or after the
// request: an earlier one reviewed an earlier request, and a re-request of
// the same head (after a failed round) has to be run again. received_at is
// its posting transaction's start and requested_at the moment the request
// was written, so a run counts only if its posting began after that. A
// supervisor starts a review only after reading the committed request, so
// the runs it posts for it always count, and a posting already under way
// when the request is made never does, whichever commits first.
func (s *PostgresStore) OpenRequest(ctx context.Context, agentID uuid.UUID, ticketID int64, attempt int) (*Request, error) {
	req := &Request{TicketID: ticketID, Attempt: attempt}
	err := s.db.QueryRow(ctx, `
		SELECT q.head_sha, q.intent, q.requested_at
		  FROM review_requests q
		 WHERE q.ticket_id = $1 AND q.attempt_number = $2 AND q.agent_id = $3
		   AND NOT COALESCE((
		       SELECT array_agg(DISTINCT p.persona)
		         FROM review_runs r
		        CROSS JOIN LATERAL unnest(r.personas) AS p(persona)
		        WHERE r.tenant_id = q.tenant_id AND r.ticket_id = q.ticket_id AND r.attempt_number = q.attempt_number
		          AND r.posted_by_kind = 'supervisor' AND r.kind = 'review' AND r.received_at >= q.requested_at
		          AND `+headMatches("r.head_sha", "q.head_sha")+`), '{}') @> $4::text[]`,
		ticketID, attempt, agentID, RequiredPersonas).Scan(&req.HeadSHA, &req.Intent, &req.RequestedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read review request: %w", err)
	}
	return req, nil
}

func (s *PostgresStore) HeldAttemptRuns(ctx context.Context, agentID uuid.UUID, head string, limit int) (*HeadReview, error) {
	var out *HeadReview
	err := s.db.InReadTx(ctx, func(q db.Querier) error {
		held, err := findHeldAttempt(ctx, q, agentID)
		if err != nil {
			return err
		}
		if head == "" {
			// The caller's own request, as OpenRequest reads it: a later holder
			// of the attempt names the head it wants.
			err := q.QueryRow(ctx, `SELECT head_sha FROM review_requests WHERE ticket_id = $1 AND attempt_number = $2 AND agent_id = $3`,
				held.TicketID, held.Attempt, agentID).Scan(&head)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNoRequest
			}
			if err != nil {
				return fmt.Errorf("read review request: %w", err)
			}
		}
		runs, err := listRuns(ctx, q, held.TicketID, held.Attempt, head, limit+1)
		if err != nil {
			return err
		}
		out = &HeadReview{TicketID: held.TicketID, Attempt: held.Attempt, HeadSHA: head, Runs: runs}
		if len(runs) > limit {
			out.Runs, out.Truncated = runs[:limit], true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
