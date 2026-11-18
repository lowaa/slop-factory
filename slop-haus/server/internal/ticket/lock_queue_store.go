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
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
)

// The integration lock is fair: an agent that finds it taken joins a queue,
// and when it comes free the head of the queue has the first right to it.
// Before, whoever retried just after a release won, and with holds of 20-40
// minutes one ticket waited hours behind others that retried luckier.
//
// A lock is free when it has no row in locks. The head of the queue is not
// handed a row: it is the only agent AcquireLock will give the lock to, for
// DefaultLockOfferBound. That keeps every route that frees the lock -- a
// release, a lease ending, a human -- a plain delete, with the next holder
// decided here, under the claim lock, when someone asks.
//
// A holder that releases and calls again joins the queue like any other
// agent -- at the back if anyone is waiting -- which is the point of
// fairness: back-to-back merges by one agent are exactly what starved the
// others. With nobody waiting it simply takes the lock again.

// Who is next is not the first to queue but the waiter with the lowest
// effective rank: the ticket's priority rank (urgent 0 .. low 3) minus one
// for every aging step it has waited, ties by queue order. Aging is what keeps
// a low-priority waiter from starving while the lock stays saturated. The
// priority is read from tickets whenever the head is chosen, so a ticket raised
// while it waits moves up. Whoever holds the lock is never displaced, and a
// head whose offer is still running keeps it: re-choosing under it would hand
// the offer to two agents in turn, each losing it before it could take it.
//
// PriorityRankSQL is the rank of a ticket's priority (urgent 0 .. low 3) over
// the alias t of the tickets table, and AgedStepsSQL the aging steps a waiter
// queued at w.queued_at has earned, with the step in seconds as $2. They are
// exported so that another queue of tickets (the landing queue) ranks by the
// same rule instead of a copy of it.
const (
	PriorityRankSQL = `CASE t.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END`
	AgedStepsSQL    = `floor(extract(epoch FROM now() - w.queued_at) / $2::float8)::int`
)

// queueOrderSQL is the one place that order is written; the offer, the head
// check and the listing all read it, so they cannot disagree.
const queueOrderSQL = `
	ranked AS (
		SELECT w.agent_id, w.lease_id, w.seq, w.queued_at, w.granted_at, t.priority,
		       ` + PriorityRankSQL + ` AS base_rank,
		       ` + AgedStepsSQL + ` AS aged_steps,
		       (w.granted_at IS NOT NULL AND extract(epoch FROM now() - w.granted_at) <= $3::float8) AS offered
		  FROM lock_waiters w
		  JOIN leases l ON l.id = w.lease_id
		  JOIN tickets t ON t.id = l.ticket_id
		 WHERE w.name = $1
		   AND l.released_at IS NULL AND l.expires_at > now()
	), ordered AS (
		SELECT r.*,
		       row_number() OVER (ORDER BY offered DESC, base_rank - aged_steps, seq) AS pos,
		       row_number() OVER (ORDER BY offered DESC, base_rank, seq) AS pos_by_priority,
		       row_number() OVER (ORDER BY seq) AS pos_by_seq
		  FROM ranked r
	)`

// queueOrderArgs are the arguments queueOrderSQL takes after the lock's name:
// the aging step and the offer bound, in seconds.
func queueOrderArgs(ctx context.Context, q db.Querier, name string) ([]any, error) {
	minutes, err := settings.IntIn(ctx, q, settings.IntegrationLockPriorityAgingMinutes)
	if err != nil {
		return nil, err
	}
	return []any{name, (time.Duration(minutes) * time.Minute).Seconds(), DefaultLockOfferBound.Seconds()}, nil
}

// AcquireLock takes the named lock for the agent's current lease, pinning
// that lease against expiry for as long as it is held. If it cannot, the
// agent joins the queue (once: asking again keeps its place) and the error
// is a *LockHeldError saying where it stands.
//
// It runs on the held lease like any other operation on it. Ending a lease
// deletes its locks and releases it under the claim lock; a lease read
// before that lock could be one being revoked, and the insert would then
// land after the revocation's delete, leaving a lock pinned to a released
// lease that nothing sweeps and every other agent is refused.
//
// Re-acquiring a lock this agent already holds is a no-op, not a failure --
// an agent retrying after a timeout must not be told someone else took its
// own lock.
func (s *PostgresStore) AcquireLock(ctx context.Context, agentID uuid.UUID, name string) error {
	return s.lockAttemptOnHeldLease(ctx, agentID, name, true)
}

// AwaitLock is AcquireLock that stops short of taking the lock. It returns
// nil once the lock is the agent's to take -- already held, or reserved for it
// at the head of the queue -- and the same *LockHeldError otherwise.
//
// This is what a blocking wait polls. The wait runs on in the server after
// the agent's process has gone, so a wait that took the lock itself could hand
// it to an agent that will never learn it holds it, pinned to a lease that
// never expires. Reserving it instead costs the agent one more call, and a
// reservation nobody answers lapses by itself.
func (s *PostgresStore) AwaitLock(ctx context.Context, agentID uuid.UUID, name string) error {
	return s.lockAttemptOnHeldLease(ctx, agentID, name, false)
}

// lockAttemptOnHeldLease runs lockAttempt in a transaction that commits
// even when the agent is refused: joining the queue is a write the refusal
// reports, and rolling it back with the error would leave nobody queued.
func (s *PostgresStore) lockAttemptOnHeldLease(ctx context.Context, agentID uuid.UUID, name string, take bool) error {
	var refusal error
	err := s.withHeldLease(ctx, agentID, func(q db.Querier, leaseID, _ int64) error {
		// Only the integration lock gates a merge; the check is by the lease's
		// attempt, so a sibling that holds the ticket is refused too.
		if strings.HasPrefix(name, IntegrationLockName) {
			if err := requirePromotedAttempt(ctx, q, leaseID); err != nil {
				return err
			}
		}
		err := lockAttempt(ctx, q, agentID, leaseID, name, take)
		if errors.Is(err, ErrLockHeld) {
			refusal = err
			return nil
		}
		return err
	})
	if err != nil {
		return err
	}
	return refusal
}

// ErrAttemptAwaitsChoice is a merge step refused to an attempt that is not
// promoted. Such an attempt's change targets the ticket's base branch, never
// the repository's, and reaches the repository's only by a human promoting it.
var ErrAttemptAwaitsChoice = errors.New("this attempt is not promoted, so it awaits a human's choice between the ticket's attempts and cannot be merged: " +
	"finish your work and call ticket_finish with a handoff -- the attempt then waits in review, \"awaiting choice\", and your lease is released")

// requirePromotedAttempt refuses a lease on an attempt that is not promoted:
// its change cannot merge, so taking the lock, or a place in its queue, would
// only keep a promoted attempt's merge waiting.
func requirePromotedAttempt(ctx context.Context, q db.Querier, leaseID int64) error {
	var promoted bool
	err := q.QueryRow(ctx, `
		SELECT a.promoted_at IS NOT NULL
		  FROM leases l JOIN attempts a ON a.ticket_id = l.ticket_id AND a.number = l.attempt_number
		 WHERE l.id = $1`, leaseID).Scan(&promoted)
	if err != nil {
		return fmt.Errorf("read the lease's attempt: %w", err)
	}
	if !promoted {
		return fmt.Errorf("integration_lock refused: %w", ErrAttemptAwaitsChoice)
	}
	return nil
}

// lockAttempt is the one decision both calls make, under the claim lock.
func lockAttempt(ctx context.Context, q db.Querier, agentID uuid.UUID, leaseID int64, name string, take bool) error {
	if err := dropDeadWaiters(ctx, q, name); err != nil {
		return err
	}

	var holderID uuid.UUID
	err := q.QueryRow(ctx, `SELECT holder_agent_id FROM locks WHERE name = $1`, name).Scan(&holderID)
	switch {
	case err == nil:
		if holderID == agentID {
			return leaveQueue(ctx, q, name, agentID)
		}
		return joinQueue(ctx, q, name, agentID, leaseID)
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("check lock holder: %w", err)
	}

	// Free. It is this agent's if nobody is ahead of it, or if it is the head
	// -- and a head that has sat on the offer past the bound is passed over.
	// Priority can put another waiter ahead of one whose offer lapsed, so the
	// lapsed offers are cleared first rather than only when they reach the
	// head. The asking agent keeps its place: it is answering its offer late.
	if _, err := q.Exec(ctx, `
		DELETE FROM lock_waiters
		 WHERE name = $1 AND agent_id <> $2 AND granted_at IS NOT NULL
		   AND extract(epoch FROM now() - granted_at) > $3::float8`,
		name, agentID, DefaultLockOfferBound.Seconds()); err != nil {
		return fmt.Errorf("pass over lapsed offers: %w", err)
	}
	args, err := queueOrderArgs(ctx, q, name)
	if err != nil {
		return err
	}
	var headID uuid.UUID
	var offered bool
	err = q.QueryRow(ctx, `WITH `+queueOrderSQL+`
		SELECT agent_id, granted_at IS NOT NULL FROM ordered ORDER BY pos LIMIT 1`, args...).Scan(&headID, &offered)
	if errors.Is(err, pgx.ErrNoRows) {
		return takeOrReserve(ctx, q, name, agentID, leaseID, take)
	}
	if err != nil {
		return fmt.Errorf("read head of lock queue: %w", err)
	}
	if headID == agentID {
		return takeOrReserve(ctx, q, name, agentID, leaseID, take)
	}
	if !offered {
		if _, err := q.Exec(ctx, `
			UPDATE lock_waiters SET granted_at = now() WHERE agent_id = $2 AND name = $1`, name, headID); err != nil {
			return fmt.Errorf("offer lock to head of queue: %w", err)
		}
	}
	return joinQueue(ctx, q, name, agentID, leaseID)
}

// takeOrReserve gives the agent the free lock: a row for it, or, for a wait
// that does not take it, the head of the queue with the offer starting now.
func takeOrReserve(ctx context.Context, q db.Querier, name string, agentID uuid.UUID, leaseID int64, take bool) error {
	if !take {
		_, err := q.Exec(ctx, `
			INSERT INTO lock_waiters (name, agent_id, lease_id, granted_at) VALUES ($1, $2, $3, now())
			ON CONFLICT (tenant_id, name, agent_id) DO UPDATE
			   SET granted_at = COALESCE(lock_waiters.granted_at, now())`, name, agentID, leaseID)
		if err != nil {
			return fmt.Errorf("reserve lock: %w", err)
		}
		return nil
	}
	// The lock goes in before the queue entry goes out: the lock_holds trigger
	// reads when the agent queued from the entry.
	if _, err := q.Exec(ctx, `
		INSERT INTO locks (name, holder_agent_id, holder_lease_id) VALUES ($1, $2, $3)`,
		name, agentID, leaseID); err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	return leaveQueue(ctx, q, name, agentID)
}

func leaveQueue(ctx context.Context, q db.Querier, name string, agentID uuid.UUID) error {
	if _, err := q.Exec(ctx,
		`DELETE FROM lock_waiters WHERE name = $1 AND agent_id = $2`, name, agentID); err != nil {
		return fmt.Errorf("leave lock queue: %w", err)
	}
	return nil
}

// dropDeadWaiters removes entries whose lease has ended or run out. A queued
// agent pins nothing, so its lease expires like any other's, and an entry
// left behind would hold the head of the queue for an agent that is gone.
func dropDeadWaiters(ctx context.Context, q db.Querier, name string) error {
	if _, err := q.Exec(ctx, `
		DELETE FROM lock_waiters w
		 WHERE w.name = $1
		   AND NOT EXISTS (SELECT 1 FROM leases l
		                    WHERE l.id = w.lease_id AND l.released_at IS NULL AND l.expires_at > now())`, name); err != nil {
		return fmt.Errorf("drop dead lock waiters: %w", err)
	}
	return nil
}

// offerToHead starts the clock on the head of the queue if the lock is free,
// so the bound runs from the moment it came free rather than from whenever
// someone next asks.
func offerToHead(ctx context.Context, q db.Querier, name string) error {
	// Without the sweep the offer could land on an entry that is already
	// dead, and the live one behind it start its clock late.
	if err := dropDeadWaiters(ctx, q, name); err != nil {
		return err
	}
	args, err := queueOrderArgs(ctx, q, name)
	if err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `WITH `+queueOrderSQL+`
		UPDATE lock_waiters SET granted_at = now()
		 WHERE name = $1 AND granted_at IS NULL
		   AND agent_id = (SELECT agent_id FROM ordered ORDER BY pos LIMIT 1)
		   AND NOT EXISTS (SELECT 1 FROM locks WHERE name = $1)`, args...); err != nil {
		return fmt.Errorf("offer lock to head of queue: %w", err)
	}
	return nil
}

// offerToHeads is offerToHead for every lock that has a queue.
func offerToHeads(ctx context.Context, q db.Querier) error {
	rows, err := q.Query(ctx, `SELECT DISTINCT name FROM lock_waiters`)
	if err != nil {
		return fmt.Errorf("list queued locks: %w", err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("list queued locks: %w", err)
	}
	// The sweep deletes waiters of ended leases, so a lock whose queue it
	// empties is not listed: it has nobody to offer to.
	for _, name := range names {
		if err := offerToHead(ctx, q, name); err != nil {
			return err
		}
	}
	return nil
}

// joinQueue puts the agent in the queue if it is not already -- keeping its
// place if it is -- and refuses it, saying where it stands.
func joinQueue(ctx context.Context, q db.Querier, name string, agentID uuid.UUID, leaseID int64) error {
	if _, err := q.Exec(ctx, `
		INSERT INTO lock_waiters (name, agent_id, lease_id) VALUES ($1, $2, $3)
		ON CONFLICT (tenant_id, name, agent_id) DO NOTHING`, name, agentID, leaseID); err != nil {
		return fmt.Errorf("join lock queue: %w", err)
	}
	state, err := loadLockState(ctx, q, name)
	if err != nil {
		return err
	}
	for i := range state.Queue {
		if state.Queue[i].AgentID == agentID {
			return &LockHeldError{Holder: state.Holder, Position: i + 1, MovedUpBy: state.Queue[i].MovedUpBy, Ahead: state.Queue[:i]}
		}
	}
	return fmt.Errorf("join lock queue: agent %s missing from the queue it just joined", agentID)
}

// loadLockState reads the holder and the queue, durations by the database
// clock.
func loadLockState(ctx context.Context, q db.Querier, name string) (*LockState, error) {
	state := &LockState{Queue: []LockQueueEntry{}}

	var h LockHolder
	var heldSeconds, sinceHeartbeat float64
	var pr *string
	err := q.QueryRow(ctx, `
		SELECT k.holder_agent_id, a.name, t.id, t.title, la.pr_url, k.acquired_at,
		       extract(epoch FROM now() - k.acquired_at),
		       l.last_heartbeat_at, extract(epoch FROM now() - l.last_heartbeat_at)
		  FROM locks k
		  JOIN agents a ON a.id = k.holder_agent_id
		  JOIN leases l ON l.id = k.holder_lease_id
		  JOIN tickets t ON t.id = l.ticket_id
		  LEFT JOIN attempts la ON la.tenant_id = l.tenant_id AND la.ticket_id = l.ticket_id AND la.number = l.attempt_number
		 WHERE k.name = $1`, name).
		Scan(&h.AgentID, &h.AgentName, &h.TicketID, &h.TicketTitle, &pr, &h.AcquiredAt, &heldSeconds, &h.LastHeartbeatAt, &sinceHeartbeat)
	switch {
	case err == nil:
		h.Held = seconds(heldSeconds)
		h.SinceHeartbeat = seconds(sinceHeartbeat)
		deadAfter, err := settings.IntIn(ctx, q, settings.IntegrationLockDeadHolderAfter)
		if err != nil {
			return nil, err
		}
		h.Dead = h.SinceHeartbeat >= time.Duration(deadAfter)*time.Minute
		if pr != nil {
			h.PRURL = *pr
		}
		state.Holder = &h
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("load lock holder: %w", err)
	}

	args, err := queueOrderArgs(ctx, q, name)
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `WITH `+queueOrderSQL+`
		SELECT o.agent_id, a.name, t.id, t.title, o.priority, o.queued_at,
		       extract(epoch FROM now() - o.queued_at), o.granted_at,
		       CASE WHEN o.pos >= o.pos_by_seq THEN ''
		            WHEN o.pos_by_priority < o.pos_by_seq THEN 'priority'
		            ELSE 'aging' END
		  FROM ordered o
		  JOIN agents a ON a.id = o.agent_id
		  JOIN leases l ON l.id = o.lease_id
		  JOIN tickets t ON t.id = l.ticket_id
		 ORDER BY o.pos`, args...)
	if err != nil {
		return nil, fmt.Errorf("load lock queue: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var e LockQueueEntry
		var waited float64
		if err := rows.Scan(&e.AgentID, &e.AgentName, &e.TicketID, &e.TicketTitle, &e.Priority, &e.QueuedAt, &waited, &e.OfferedAt, &e.MovedUpBy); err != nil {
			return nil, fmt.Errorf("scan lock queue: %w", err)
		}
		e.Waited = seconds(waited)
		state.Queue = append(state.Queue, e)
	}
	return state, rows.Err()
}

func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

// LockState is the lock and its queue as the board shows them. Read-only: an
// entry whose lease has run out is left for the next attempt to sweep, but
// loadLockState does not show it, since it will never be granted anything.
func (s *PostgresStore) LockState(ctx context.Context, name string) (*LockState, error) {
	var state *LockState
	err := s.db.InTx(ctx, func(q db.Querier) error {
		var err error
		state, err = loadLockState(ctx, q, name)
		return err
	})
	if err != nil {
		return nil, err
	}
	return state, nil
}

// LockNames is every lock with a holder or a queue entry, sorted. A lock with
// neither has no row anywhere, so the default lock is the caller's to add.
func (s *PostgresStore) LockNames(ctx context.Context) ([]string, error) {
	rows, err := s.db.Query(ctx, `
		SELECT name FROM locks UNION SELECT name FROM lock_waiters ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list locks: %w", err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("list locks: %w", err)
	}
	return names, nil
}

// ForceReleaseLock drops the lock whoever holds it: a human's answer to a
// holder that is dead but whose lease the lock itself keeps from expiring.
// The holder's lease is left to expire as usual now that nothing pins it.
// Returns the holder it removed.
func (s *PostgresStore) ForceReleaseLock(ctx context.Context, name string) (*LockHolder, error) {
	var holder *LockHolder
	err := s.underClaimLock(ctx, func(q db.Querier) error {
		state, err := loadLockState(ctx, q, name)
		if err != nil {
			return err
		}
		if state.Holder == nil {
			return ErrNoSuchLock
		}
		if _, err := q.Exec(ctx, `DELETE FROM locks WHERE name = $1`, name); err != nil {
			return fmt.Errorf("release lock: %w", err)
		}
		holder = state.Holder
		return offerToHead(ctx, q, name)
	})
	return holder, err
}

// DropLockWaiter removes an agent from the queue. If it was at the head of a
// free lock, the next in line is offered it.
func (s *PostgresStore) DropLockWaiter(ctx context.Context, name string, agentID uuid.UUID) error {
	return s.underClaimLock(ctx, func(q db.Querier) error {
		tag, err := q.Exec(ctx,
			`DELETE FROM lock_waiters WHERE name = $1 AND agent_id = $2`, name, agentID)
		if err != nil {
			return fmt.Errorf("drop lock waiter: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotQueued
		}
		return offerToHead(ctx, q, name)
	})
}
