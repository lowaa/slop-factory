package ticket

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// ServerAgentName is the per-tenant agent the server acts as when it merges an
// approved ticket itself. It is a real agent so the merge runs through the
// same claim, lock queue and merge gate as any worker's, and the board and
// lock history name it like any other holder.
const ServerAgentName = "slop-haus"

// DefaultServerLeaseTTL is the lease the server takes on a ticket. The caller
// heartbeats (Heartbeat) while it waits for the lock queue, which a lease
// that is not yet pinned by the lock would otherwise not survive.
const DefaultServerLeaseTTL = DefaultLeaseTTL

var (
	// ErrServerAgentTaken is a tenant whose agent named ServerAgentName is not
	// the server's: it was registered by a person as something else.
	ErrServerAgentTaken = errors.New("ticket: an agent named " + ServerAgentName + " exists and is not a console")
	// ErrNotServerAgent is the server's operations asked of any other agent.
	ErrNotServerAgent = errors.New("ticket: not the server agent")
	// ErrServerAgentDisabled is the server's agent switched off by a person.
	ErrServerAgentDisabled = errors.New("ticket: the " + ServerAgentName + " agent is disabled")
	// ErrServerClaimNotPromoted is a server claim of an attempt that is not
	// the ticket's promoted one: only that one's change may merge.
	ErrServerClaimNotPromoted = errors.New("ticket: the attempt is not the ticket's promoted attempt")
	// ErrServerClaimNotApproved is a server claim of an attempt no person has
	// approved.
	ErrServerClaimNotApproved = errors.New("ticket: the attempt is not approved")
	// ErrServerClaimNotQueued is a server claim of an attempt that is not
	// waiting in the queue: in review, discussion, done, or held back.
	ErrServerClaimNotQueued = errors.New("ticket: the attempt is not waiting in the queue")
	// ErrServerClaimHeld is a server claim of an attempt an agent holds.
	ErrServerClaimHeld = errors.New("ticket: the attempt is held by an agent")
	// ErrServerClaimLaneHeld is a server claim of a queued landing whose app
	// has another entry active: one acting engine per app at a time.
	ErrServerClaimLaneHeld = errors.New("ticket: another landing is active in the entry's app")
)

// EnsureServerAgent returns the id of the context tenant's server agent,
// registering it first when the tenant has none. Safe to call again and from
// concurrent callers.
//
// It is a console: the queue refuses a console's unnamed claim
// (ErrConsoleUnnamedClaim), so the server agent is never offered ordinary
// work, and no first-claim window is ever written for it.
func (s *PostgresStore) EnsureServerAgent(ctx context.Context) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.db.InTx(ctx, func(q db.Querier) error {
		if _, err := q.Exec(ctx, `
			INSERT INTO agents (id, name, kind) VALUES ($1, $2, 'console')
			ON CONFLICT (tenant_id, name) DO NOTHING`, uuid.New(), ServerAgentName); err != nil {
			return fmt.Errorf("register the server agent: %w", err)
		}
		var kind string
		var disabled bool
		if err := q.QueryRow(ctx, `
			SELECT id, kind, disabled_at IS NOT NULL FROM agents WHERE name = $1`, ServerAgentName).
			Scan(&id, &kind, &disabled); err != nil {
			return fmt.Errorf("read the server agent: %w", err)
		}
		switch {
		case kind != "console":
			return ErrServerAgentTaken
		case disabled:
			return ErrServerAgentDisabled
		}
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// ServerClaim takes the promoted attempt of an approved ticket, or of one that
// does not require approval and its author finished into the landing queue, for the server
// agent, which merges it. It is the claim of one named ticket that skips
// what a worker's claim honours for the author: the first-claim window, which
// is the author's to come back in and is not the server's to wait out.
//
// It refuses what the merge gate would: an attempt that is not the promoted
// one, that no person approved, that is not waiting in the queue, or that an
// agent holds. A ticket whose scope another ticket holds is not granted, as
// for any claim, and comes back as Blocked. The fleet-wide processing switch
// is not read: the approval was a person's, and the merge of it is not new
// work being handed out.
//
// The author is still owed the ticket (resume_agent_id) while the server
// holds it, so ServerHandBack can return it to them.
func (s *PostgresStore) ServerClaim(ctx context.Context, serverAgent uuid.UUID, ticketID int64, attempt int, ttl time.Duration) (*ClaimResult, error) {
	return s.serverClaim(ctx, serverAgent, ticketID, attempt, ttl, false)
}

// ServerClaimLanding is ServerClaim for the landing queue's head: the attempt
// is in_merge_queue and stays so while the server holds it, since the queue
// owns the ticket and no worker holds it. A queued attempt is taken too, and
// becomes in_merge_queue: a ticket enqueued by the release before the status
// existed, or one whose status the repair sweep has not reached.
func (s *PostgresStore) ServerClaimLanding(ctx context.Context, serverAgent uuid.UUID, ticketID int64, attempt int, ttl time.Duration) (*ClaimResult, error) {
	return s.serverClaim(ctx, serverAgent, ticketID, attempt, ttl, true)
}

func (s *PostgresStore) serverClaim(ctx context.Context, serverAgent uuid.UUID, ticketID int64, attempt int, ttl time.Duration, landing bool) (*ClaimResult, error) {
	shared, err := s.sharedPaths(ctx)
	if err != nil {
		return nil, err
	}
	apps, err := s.laneApps(ctx)
	if err != nil {
		return nil, err
	}
	ref := &AttemptRef{TicketID: ticketID, Number: attempt}
	var result ClaimResult
	err = s.underClaimLock(ctx, func(q db.Querier) error {
		if err := requireServerAgent(ctx, q, serverAgent); err != nil {
			return err
		}
		if err := expireStale(ctx, q); err != nil {
			return err
		}
		own, err := reclaimOwnLease(ctx, q, apps, serverAgent, ref, ttl)
		if err != nil {
			return err
		}
		if own != nil {
			result.Ticket, result.Attempt = own, ref
			return nil
		}
		if err := requireNoActiveLease(ctx, q, serverAgent, "check the server's lease"); err != nil {
			return err
		}

		// The ticket's row first, then its attempt: the lock order AttemptRef names.
		if err := q.QueryRow(ctx, `SELECT id FROM tickets WHERE id = $1 FOR UPDATE`, ticketID).Scan(new(int64)); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: #%d", ErrNotFound, ticketID)
			}
			return fmt.Errorf("lock ticket: %w", err)
		}
		var status string
		var promoted, approved, needsApproval, leased, delayed, hasLanding bool
		err = q.QueryRow(ctx, `
			SELECT a.status, a.promoted_at IS NOT NULL, a.approved_at IS NOT NULL,
			       EXISTS (SELECT 1 FROM leases l
			                WHERE l.ticket_id = a.ticket_id AND l.attempt_number = a.number AND `+activeLeaseClause+`),
			       COALESCE(t.blocked_until > now(), false), t.requires_approval,
			       EXISTS (SELECT 1 FROM landings w WHERE w.ticket_id = a.ticket_id AND w.queue_state <> 'back_with_agent')
			  FROM attempts a JOIN tickets t ON t.id = a.ticket_id
			 WHERE a.ticket_id = $1 AND a.number = $2 FOR UPDATE OF a`, ticketID, attempt).
			Scan(&status, &promoted, &approved, &leased, &delayed, &needsApproval, &hasLanding)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%s: %w", ref, ErrNoSuchAttempt)
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", ref, err)
		}
		switch {
		case !promoted:
			return fmt.Errorf("%s: %w", ref, ErrServerClaimNotPromoted)
		case leased || status == "in_progress":
			return fmt.Errorf("%s: %w", ref, ErrServerClaimHeld)
		case !claimableByServer(status, landing) || delayed || !landing && hasLanding:
			return fmt.Errorf("%s: %w", ref, ErrServerClaimNotQueued)
		case needsApproval && !approved:
			return fmt.Errorf("%s: %w", ref, ErrServerClaimNotApproved)
		}

		if landing {
			if err := requireLaneFree(ctx, q, apps, ticketID); err != nil {
				return fmt.Errorf("%s: %w", ref, err)
			}
		}

		var author *uuid.UUID
		if err := q.QueryRow(ctx, `SELECT resume_agent_id FROM attempts WHERE ticket_id = $1 AND number = $2`,
			ticketID, attempt).Scan(&author); err != nil {
			return fmt.Errorf("read who is owed %s: %w", ref, err)
		}

		paths, err := pathsForTicket(ctx, q, ticketID)
		if err != nil {
			return err
		}
		held, err := activeScopes(ctx, q)
		if err != nil {
			return err
		}
		blocked, overlaps := scanConflicts(ticketID, paths, held, shared)
		if blocked != nil {
			result.Blocked = blocked
			return nil
		}

		holdAs := AttemptStatusInProgress
		if landing {
			holdAs = AttemptStatusInMergeQueue
		}
		granted, err := grantAttempt(ctx, q, serverAgent, ref, ttl, nil, holdAs)
		if err != nil {
			return err
		}
		// The grant ends the author's hold; the server's own is brief, and
		// nothing else remembers whom the ticket is owed to (released leases
		// are pruned) for ServerHandBack to return it to.
		if author != nil {
			if _, err := q.Exec(ctx, `UPDATE attempts SET resume_agent_id = $3 WHERE ticket_id = $1 AND number = $2`,
				ticketID, attempt, *author); err != nil {
				return fmt.Errorf("keep who is owed %s: %w", ref, err)
			}
		}
		result.Ticket, result.Attempt, result.SharedOverlaps = granted, ref, overlaps
		return endScopeBlocksOf(ctx, q, ticketID, scopeBlockClaimed)
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// claimableByServer is whether an attempt in status is one the server may take
// for the flow: the landing queue's is in the merge queue (or queued, see
// ServerClaimLanding), the approved-ticket merge's is queued.
func claimableByServer(status string, landing bool) bool {
	return status == string(AttemptStatusQueued) || landing && status == string(AttemptStatusInMergeQueue)
}

// reclaimOwnLease takes back a lease the server itself left behind on a ticket
// in the landing queue, whose runner reconciles from the code host (the other
// server-merge flow is recovered by its own reaper, so it is not touched here).
// The process that held it died holding the integration lock, which pins the
// lease past its expiry, so the ordinary sweep never frees it and no claim
// could be made over it. Nothing else may take the attempt over a lease
// that is still within its TTL, so a live landing is never taken from. A
// runner that was merely cut off from the database past the TTL could still be
// running beside its reclaimer; the merge itself is single-flight in the
// change store, which is what bounds that. It returns nil when there is no
// such lease.
func reclaimOwnLease(ctx context.Context, q db.Querier, apps []string, serverAgent uuid.UUID, ref *AttemptRef, ttl time.Duration) (*Ticket, error) {
	if err := requireLaneFree(ctx, q, apps, ref.TicketID); err != nil {
		if errors.Is(err, ErrServerClaimLaneHeld) {
			// Not an error of the reclaim: the row is not one it may take back yet.
			return nil, nil
		}
		return nil, err
	}
	tag, err := q.Exec(ctx, `
		UPDATE leases l SET expires_at = clock_timestamp() + $4::interval, last_heartbeat_at = clock_timestamp()
		 WHERE l.agent_id = $1 AND l.ticket_id = $2 AND l.attempt_number = $3
		   AND l.released_at IS NULL AND l.expires_at <= now()
		   AND EXISTS (SELECT 1 FROM landings w WHERE w.ticket_id = l.ticket_id AND w.queue_state <> 'back_with_agent')
		   AND EXISTS (SELECT 1 FROM locks lk WHERE lk.holder_lease_id = l.id)`,
		serverAgent, ref.TicketID, ref.Number, ttl.String())
	if err != nil {
		return nil, fmt.Errorf("reclaim the server's lease: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, nil
	}
	conv, err := attemptConversation(ctx, q, ref)
	if err != nil {
		return nil, err
	}
	return ticketSeenBy(ctx, q, ref.TicketID, conv)
}

// ServerHandBack ends the server agent's lease on ticketID and returns the
// ticket to the queue for its author, who holds first claim again for
// affinity, with guidance -- what failed and what to do -- as a comment on the
// attempt, which the author is shown when it claims. The integration lock the
// server held goes to the next in the queue.
func (s *PostgresStore) ServerHandBack(ctx context.Context, serverAgent uuid.UUID, ticketID int64, guidance string, affinity time.Duration) error {
	if guidance == "" {
		return ErrEmptyComment
	}
	return s.underClaimLock(ctx, func(q db.Querier) error {
		if err := requireServerAgent(ctx, q, serverAgent); err != nil {
			return err
		}
		var leaseID int64
		var number int
		err := q.QueryRow(ctx, `
			SELECT l.id, l.attempt_number FROM leases l
			 WHERE l.agent_id = $1 AND l.ticket_id = $2 AND `+activeLeaseClause, serverAgent, ticketID).
			Scan(&leaseID, &number)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoLease
		}
		if err != nil {
			return fmt.Errorf("load the server's lease: %w", err)
		}
		ref := &AttemptRef{TicketID: ticketID, Number: number}
		if err := agentMover(serverAgent, "server_hand_back").set(ctx, q); err != nil {
			return err
		}
		if err := endLease(ctx, q, leaseID, "handed back"); err != nil {
			return err
		}
		if err := requeueAttempts(ctx, q, []AttemptRef{*ref}); err != nil {
			return err
		}
		if err := requeueFromMergeQueue(ctx, q, ref); err != nil {
			return err
		}
		// The window follows the rule every first-claim window does: written
		// only for an enabled worker, since nobody else comes back on its own.
		if err := writeAttempt(ctx, q, ref, &setList{
			clauses: []string{`affinity_until = CASE WHEN EXISTS (
				SELECT 1 FROM agents a
				 WHERE a.id = pa.resume_agent_id AND ` + idleWorkerClause + `)
				THEN now() + $3::interval END`},
			args: []any{affinity.String()}, first: 3,
		}); err != nil {
			return err
		}
		_, err = InsertAttemptComment(ctx, q, ref, AgentAuthor(serverAgent), guidance)
		return err
	})
}

// ServerRequeueMember moves a landing batch member, whose landings row has
// been handed back, out of the merge queue to its author's queue. The member
// holds no lease, so there is nothing to end; without this the attempt stays
// in_merge_queue until RepairMergeQueue puts it right.
func (s *PostgresStore) ServerRequeueMember(ctx context.Context, serverAgent uuid.UUID, ref *AttemptRef) error {
	return s.underClaimLock(ctx, func(q db.Querier) error {
		if err := requireServerAgent(ctx, q, serverAgent); err != nil {
			return err
		}
		if err := agentMover(serverAgent, "server_hand_back").set(ctx, q); err != nil {
			return err
		}
		return requeueFromMergeQueue(ctx, q, ref)
	})
}

// ServerReleaseLanding ends the server agent's lease on the landing ticketID
// (an attempt in the merge queue), for a runner that is stopping. It names the
// ticket because the server agent is one per tenant: a replica must not end a
// lease another has since taken. Unlike ServerHandBack the
// ticket is not returned to its author: the attempt stays in the merge queue,
// so the next ServerClaimLanding, here or on another replica, takes it at once
// instead of waiting out the lease. The integration lock the lease held goes
// to the next in the queue. It is a no-op when the server holds no lease on it,
// and leaves the lease of the approved-ticket flow (an attempt in progress),
// which its own reaper recovers, alone.
func (s *PostgresStore) ServerReleaseLanding(ctx context.Context, serverAgent uuid.UUID, ticketID int64) error {
	return s.underClaimLock(ctx, func(q db.Querier) error {
		if err := requireServerAgent(ctx, q, serverAgent); err != nil {
			return err
		}
		rows, err := q.Query(ctx, `
			SELECT l.id FROM leases l
			  JOIN attempts a ON a.ticket_id = l.ticket_id AND a.number = l.attempt_number
			 WHERE l.agent_id = $1 AND l.ticket_id = $2 AND a.status = 'in_merge_queue' AND l.released_at IS NULL`, serverAgent, ticketID)
		if err != nil {
			return fmt.Errorf("load the server's landing leases: %w", err)
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return fmt.Errorf("load the server's landing leases: %w", err)
		}
		for _, id := range ids {
			if err := endLease(ctx, q, id, "server stopped"); err != nil {
				return err
			}
		}
		return nil
	})
}

// requeueFromMergeQueue puts an attempt that was in the merge queue back to
// queued and works out its ticket's status again (open, or blocked while a
// dependency or cooldown holds it). requeueAttempts is not widened to do this:
// it also serves a lapsed lease, and a landing whose server lease lapsed
// mid-CI stays in the queue for reclaimOwnLease to recover.
func requeueFromMergeQueue(ctx context.Context, q db.Querier, ref *AttemptRef) error {
	tag, err := q.Exec(ctx, `
		UPDATE attempts SET status = 'queued', updated_at = now()
		 WHERE ticket_id = $1 AND number = $2 AND status = 'in_merge_queue'`, ref.TicketID, ref.Number)
	if err != nil {
		return fmt.Errorf("requeue %s from the merge queue: %w", ref, err)
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	_, err = deriveStatus(ctx, q, `t.id = $1 AND t.status = 'in_merge_queue'`, ref.TicketID)
	return err
}

// requireServerAgent refuses any agent but the tenant's enabled server agent,
// so the claim's bypass of the first-claim window and the hand-back's
// attribution cannot be used by an ordinary worker. FOR SHARE, as Claim does,
// so a disable that is still uncommitted is waited out.
func requireServerAgent(ctx context.Context, q db.Querier, id uuid.UUID) error {
	var name, kind string
	var disabled bool
	err := q.QueryRow(ctx, `SELECT name, kind, disabled_at IS NOT NULL FROM agents WHERE id = $1 FOR SHARE`, id).
		Scan(&name, &kind, &disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: agent %s", ErrNotFound, id)
	}
	if err != nil {
		return fmt.Errorf("check the server agent: %w", err)
	}
	switch {
	case name != ServerAgentName || kind != "console":
		return fmt.Errorf("%w: agent %s", ErrNotServerAgent, id)
	case disabled:
		return ErrServerAgentDisabled
	}
	return nil
}
