package ticket

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// activeLeaseOf returns the agent's held lease id, or ErrNoLease.
func activeLeaseOf(ctx context.Context, q db.Querier, agentID uuid.UUID) (int64, int64, error) {
	var leaseID, ticketID int64
	err := q.QueryRow(ctx, `
		SELECT l.id, l.ticket_id FROM leases l
		 WHERE l.agent_id = $1 AND `+activeLeaseClause, agentID).Scan(&leaseID, &ticketID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, ErrNoLease
	}
	if err != nil {
		return 0, 0, fmt.Errorf("load active lease: %w", err)
	}
	return leaseID, ticketID, nil
}

// withHeldLease runs fn in one transaction on the agent's held lease, or
// fails with ErrNoLease.
//
// The claim lock is taken before the lease is looked up, never after: a
// human taking a ticket back revokes its lease under that lock, and a lease
// read before it could be one already revoked, letting fn act on a ticket
// the agent has lost. The lock also serializes fn with every claim, which
// decides on the statuses, leases and scopes fn writes.
func (s *PostgresStore) withHeldLease(ctx context.Context, agentID uuid.UUID, fn func(q db.Querier, leaseID, ticketID int64) error) error {
	return s.underClaimLock(ctx, func(q db.Querier) error {
		leaseID, ticketID, err := activeLeaseOf(ctx, q, agentID)
		if err != nil {
			return err
		}
		return fn(q, leaseID, ticketID)
	})
}

// Heartbeat extends the agent's lease. Every tool call does this, so there
// is no separate heartbeat to forget to send.
//
// The extension is conditional, never a blind UPDATE. A heartbeat races the
// claim path: an agent's request can be in flight while Claim expires that
// same lease and re-opens the ticket for someone else. A blind extend would
// resurrect a lease the server has already given away, and two agents would
// hold the same ticket -- the one failure this whole design exists to
// prevent.
//
// Zero rows updated is a meaningful result, not a no-op: the agent's ticket
// was taken from under it, so it gets ErrLeaseLost rather than a silent
// success, and can stop and check in.
func (s *PostgresStore) Heartbeat(ctx context.Context, agentID uuid.UUID, ttl time.Duration) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE leases l SET expires_at = now() + $2::interval, last_heartbeat_at = now()
		 WHERE l.agent_id = $1 AND `+activeLeaseClause, agentID, ttl.String())
	if err != nil {
		return fmt.Errorf("heartbeat: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return deliverRevocation(ctx, s.db, agentID)
	}
	return nil
}

// deliverRevocation returns the agent's undelivered LeaseRevokedError,
// marking it delivered, or ErrNoLease if there is none.
//
// Only the agent's most recent lease counts: a notice for a ticket it has
// since moved on from is history, not news. And only within a lease TTL of
// the revocation -- an agent that has made no call for that long would
// have lost the lease to expiry anyway, and telling it on its next session's
// first call that a human took a ticket away hours ago only confuses it.
//
// Marking it delivered in the same statement that reads it is what makes it
// once-only even when two of the agent's calls race here.
func deliverRevocation(ctx context.Context, q db.Querier, agentID uuid.UUID) error {
	var ticketID int64
	var attempt int
	var status string
	err := q.QueryRow(ctx, `
		UPDATE leases l SET revocation_delivered_at = now()
		 WHERE l.id = (SELECT id FROM leases WHERE agent_id = $1 ORDER BY id DESC LIMIT 1)
		   AND l.revoked_to IS NOT NULL
		   AND l.revocation_delivered_at IS NULL
		   AND l.released_at > now() - $2::interval
		 RETURNING l.ticket_id, l.attempt_number, l.revoked_to`, agentID, DefaultLeaseTTL.String()).Scan(&ticketID, &attempt, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoLease
	}
	if err != nil {
		return fmt.Errorf("deliver lease revocation: %w", err)
	}
	return &LeaseRevokedError{TicketID: ticketID, Attempt: attempt, Status: Status(status)}
}

// RevocationContext is what an agent can still be told after its revocation
// notice has been delivered: its latest lease was ended by a human within a
// lease TTL. Read-only; it never repeats the notice.
type RevocationContext struct {
	TicketID int64
	Attempt  int
	Status   Status
	At       time.Time
}

// Describe is the one line that explains why the agent holds nothing.
func (r *RevocationContext) Describe() string {
	if r.Status == revokedToClosed {
		return fmt.Sprintf("You hold no lease: your last attempt, attempt %d of ticket #%d, was closed by a human at %s.",
			r.Attempt, r.TicketID, r.At.UTC().Format(time.RFC3339))
	}
	moved := "moved to " + statusLabel(r.Status)
	if r.Status == StatusCancelled {
		moved = "cancelled"
	}
	return fmt.Sprintf("You hold no lease: your last ticket, #%d, was %s by a human at %s.",
		r.TicketID, moved, r.At.UTC().Format(time.RFC3339))
}

// RecentRevocation is the agent's latest lease when a human ended it within
// ttl and its notice has already been delivered, or nil. The same lease and
// window deliverRevocation uses, but it changes nothing.
func (s *PostgresStore) RecentRevocation(ctx context.Context, agentID uuid.UUID, ttl time.Duration) (*RevocationContext, error) {
	var r RevocationContext
	var status string
	err := s.db.QueryRow(ctx, `
		SELECT l.ticket_id, l.attempt_number, l.revoked_to, l.released_at FROM leases l
		 WHERE l.id = (SELECT id FROM leases WHERE agent_id = $1 ORDER BY id DESC LIMIT 1)
		   AND l.revoked_to IS NOT NULL
		   AND l.revocation_delivered_at IS NOT NULL
		   AND l.released_at > now() - $2::interval`, agentID, ttl.String()).Scan(&r.TicketID, &r.Attempt, &status, &r.At)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load recent revocation: %w", err)
	}
	r.Status = Status(status)
	return &r, nil
}

// revokeLeaseOn ends whatever lease is held on the ticket because a human
// moved it to status, recording that so the agent is told why -- see
// LeaseRevokedError. Through endLease like every other route out of a
// lease, so the agent's integration lock goes with it.
//
// Every unreleased lease, not only live ones: an agent whose lease lapsed
// without the sweep having run yet is still working the ticket as far as it
// knows, and is owed the same notice.
//
// Callers hold the claim lock.
func revokeLeaseOn(ctx context.Context, q db.Querier, ticketID int64, status Status) error {
	return revokeLeases(ctx, q, `ticket_id = $1`, []any{ticketID}, "revoked: moved to "+string(status)+" by a human", status)
}

// revokedToClosed is revoked_to for a lease ended because a person closed its
// attempt: not a ticket status, so not a Status of the board.
const revokedToClosed Status = "closed"

// revokeAttemptLease is revokeLeaseOn for one attempt: the person closed it, and
// its siblings' leases are theirs.
//
// Callers hold the claim lock.
func revokeAttemptLease(ctx context.Context, q db.Querier, ref *AttemptRef) error {
	return revokeLeases(ctx, q, `ticket_id = $1 AND attempt_number = $2`, []any{ref.TicketID, ref.Number},
		"revoked: attempt closed by a human", revokedToClosed)
}

// revokeLeases ends the unreleased leases where selects (SQL from this
// package, never input), noting revokedTo for the notice.
func revokeLeases(ctx context.Context, q db.Querier, where string, args []any, reason string, revokedTo Status) error {
	rows, err := q.Query(ctx, `SELECT id FROM leases WHERE released_at IS NULL AND `+where, args...)
	if err != nil {
		return fmt.Errorf("find leases to revoke: %w", err)
	}
	leaseIDs, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return fmt.Errorf("scan leases to revoke: %w", err)
	}
	for _, leaseID := range leaseIDs {
		if err := endLease(ctx, q, leaseID, reason); err != nil {
			return err
		}
		if _, err := q.Exec(ctx,
			`UPDATE leases SET revoked_to = $2 WHERE id = $1`, leaseID, string(revokedTo)); err != nil {
			return fmt.Errorf("record revocation: %w", err)
		}
	}
	return nil
}

// unblockSatisfied reopens every blocked ticket whose dependencies are all
// terminal and whose cooldown, if any, has ended.
//
// Blocked-ness is a predicate, re-evaluated whenever something it depends on
// changes -- a ticket finishing or being cancelled, a dependency being
// removed -- not a flag set once. For a long time nothing re-evaluated it at
// all: a ticket released blocked on unfinished work stayed blocked after
// that work was done, and since only open tickets are claimable, it was
// stranded for good. A sweep over every blocked ticket rather than the
// finished ticket's dependents, so no path that changes a status has to know
// which tickets care; the blocked set is small.
//
// Callers hold the claim lock: this rewrites statuses Claim reads.
func unblockSatisfied(ctx context.Context, q db.Querier) error {
	if err := systemMover("unblocked").set(ctx, q); err != nil {
		return err
	}
	// Only the ticket's queue state changes: its attempts are queued either way.
	_, err := deriveStatus(ctx, q, `t.status = 'blocked' AND NOT `+isBlocked)
	if err != nil {
		return fmt.Errorf("unblock satisfied tickets: %w", err)
	}
	return nil
}

// endLease releases a lease and drops any lock it was holding.
//
// Every route out of a lease goes through here, which is what keeps the
// integration lock's lifetime genuinely derived from its holder's lease. An
// agent that gives up mid-merge would otherwise leave a lock row behind
// whose lease is gone and whose pin no longer applies to anything, stalling
// all integration until a human noticed. Expiry is the one route that
// cannot end a pinned lease -- that is the point of the pin.
func endLease(ctx context.Context, q db.Querier, leaseID int64, reason string) error {
	var freed []string
	rows, err := q.Query(ctx,
		`DELETE FROM locks WHERE holder_lease_id = $1 RETURNING name`, leaseID)
	if err != nil {
		return fmt.Errorf("drop locks: %w", err)
	}
	freed, err = pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("drop locks: %w", err)
	}
	// An agent whose lease ends has nothing left to take the lock for, so it
	// leaves the queue; a lock it held goes to whoever is now at the head.
	if _, err := q.Exec(ctx, `DELETE FROM lock_waiters WHERE lease_id = $1`, leaseID); err != nil {
		return fmt.Errorf("leave lock queue: %w", err)
	}
	for _, name := range freed {
		if err := offerToHead(ctx, q, name); err != nil {
			return err
		}
	}
	if _, err := q.Exec(ctx, `
		UPDATE leases SET released_at = now(), release_reason = $2 WHERE id = $1`,
		leaseID, reason); err != nil {
		return fmt.Errorf("release lease: %w", err)
	}
	return nil
}

// ReleaseLeasesFor releases the leases an agent held when it was disabled and
// returns those tickets to the queue. Used when an agent is disabled: leaving
// its work held would mean waiting out the TTL for a ticket nobody is going
// to touch.
//
// Only leases acquired at or before acquiredBy -- the moment of the disable --
// are released. Disabling is two steps in two packages (mark disabled, then
// this), so the agent can be re-enabled and claim a ticket in between;
// releasing everything would take that fresh lease from it and let another
// agent claim the same ticket. Not "release only if still disabled": a
// re-enable in the gap would then also strand the lease it held when it was
// disabled. Both clocks are the database's.
//
// This lives here rather than in the agent package because the tickets,
// leases and locks tables have one owner, and releasing a lease is a ticket
// state transition like any other. It takes the same advisory lock as the
// claim path for the same reason: without it, a claim can observe this
// multi-statement release half-applied.
//
// The reopen is driven by a CTE returning only the leases released *by this
// statement*. Matching on release_reason instead would sweep up every
// historical lease the agent ever had released for the same reason -- so
// disabling an agent a second time would reopen a ticket some *other* agent
// had since claimed and was working on.
func (s *PostgresStore) ReleaseLeasesFor(ctx context.Context, agentID uuid.UUID, reason string, acquiredBy time.Time) error {
	return s.db.InTx(ctx, func(q db.Querier) error {
		if err := db.TakeAdvisoryLock(ctx, q, claimAdvisoryKey); err != nil {
			return err
		}
		if err := systemMover(reason).set(ctx, q); err != nil {
			return err
		}
		rows, err := q.Query(ctx, `
			WITH released AS (
				UPDATE leases l
				   SET released_at = now(), release_reason = $2
				 WHERE l.agent_id = $1 AND l.released_at IS NULL
				   AND l.acquired_at <= $3
				 RETURNING l.id, l.ticket_id, l.attempt_number
			),
			-- Any lock those leases held goes with them. A disabled agent
			-- holding the integration lock would otherwise leave it behind
			-- with no lease to derive its lifetime from, stalling every
			-- merge until a human noticed.
			unlocked AS (
				DELETE FROM locks WHERE holder_lease_id IN (SELECT id FROM released)
			)
			SELECT ticket_id, attempt_number FROM released`,
			agentID, reason, acquiredBy)
		if err != nil {
			return fmt.Errorf("release leases for agent: %w", err)
		}
		released, err := endedAttempts(rows)
		if err != nil {
			return fmt.Errorf("release leases for agent: %w", err)
		}
		if err := requeueAttempts(ctx, q, released); err != nil {
			return err
		}
		// The lock this may have freed goes to whoever is now at the head,
		// as it does when the holder releases it -- and the offer sweeps the
		// queue of entries on the leases just ended, so a disabled agent
		// leaves it here too. The statement above is a CTE that cannot say
		// which lock it freed, so ask of every lock with a queue.
		return offerToHeads(ctx, q)
	})
}

// ReleaseLock drops the named lock, un-pinning its lease so normal expiry
// resumes.
//
// It has no lease lookup to race today, but it takes the claim lock anyway,
// as everything else that writes locks does: any check added here later
// (whose lease the lock is pinned to, say) is then made against a lease no
// revocation can end mid-transaction.
func (s *PostgresStore) ReleaseLock(ctx context.Context, agentID uuid.UUID) error {
	return s.underClaimLock(ctx, func(q db.Querier) error {
		// By holder, not by the name the ticket's base branch gives now: an
		// owner changing the manifest while the lock is held must not strand
		// it. Agents can only hold integration locks.
		rows, err := q.Query(ctx, `DELETE FROM locks WHERE holder_agent_id = $1 RETURNING name`, agentID)
		if err != nil {
			return fmt.Errorf("release lock: %w", err)
		}
		names, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("release lock: %w", err)
		}
		if len(names) == 0 {
			return ErrNoSuchLock
		}
		for _, name := range names {
			if err := offerToHead(ctx, q, name); err != nil {
				return err
			}
		}
		return nil
	})
}

// LeaseFor returns the agent's active lease, for the Agents screen.
func (s *PostgresStore) LeaseFor(ctx context.Context, agentID uuid.UUID) (*Lease, error) {
	var l Lease
	var reason *string
	err := s.db.QueryRow(ctx, `
		SELECT l.id, l.ticket_id, l.agent_id, l.acquired_at, l.expires_at,
		       l.released_at, l.release_reason
		  FROM leases l WHERE l.agent_id = $1 AND `+activeLeaseClause, agentID).
		Scan(&l.ID, &l.TicketID, &l.AgentID, &l.AcquiredAt, &l.ExpiresAt, &l.ReleasedAt, &reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoLease
	}
	if err != nil {
		return nil, fmt.Errorf("load lease: %w", err)
	}
	if reason != nil {
		l.ReleaseReason = *reason
	}
	return &l, nil
}

// HeldScopes is every path of every scope held now -- a live lease, or the
// ticket in review -- the one definition of held that a claim uses.
func (s *PostgresStore) HeldScopes(ctx context.Context) ([]HeldScope, error) {
	rows, err := s.db.Query(ctx, `
		SELECT t.id, t.title, t.status, p.path
		  FROM tickets t
		  JOIN ticket_paths p ON p.ticket_id = t.id
		 WHERE `+holdsScopeClause+`
		 ORDER BY t.id, p.path`)
	if err != nil {
		return nil, fmt.Errorf("load held scopes: %w", err)
	}
	defer rows.Close()
	held := []HeldScope{}
	for rows.Next() {
		var h HeldScope
		if err := rows.Scan(&h.TicketID, &h.Title, &h.Status, &h.Path); err != nil {
			return nil, fmt.Errorf("scan held scope: %w", err)
		}
		held = append(held, h)
	}
	return held, rows.Err()
}

func joinQuoted(paths []string) string {
	quoted := make([]string, len(paths))
	for i, p := range paths {
		quoted[i] = fmt.Sprintf("%q", p)
	}
	return strings.Join(quoted, ", ")
}
