package ticket

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

// InTransition runs fn in one transaction under the claim lock: every
// transition writes statuses, leases or dependencies that Claim reads, and
// a claim between a transition's check and its write would act on a state
// that transition was about to change.
func (s *PostgresStore) InTransition(ctx context.Context, fn func(tx TransitionTx) error) error {
	shared, err := s.sharedPaths(ctx)
	if err != nil {
		return err
	}
	return s.underClaimLock(ctx, func(q db.Querier) error {
		return fn(&pgTransition{q: q, shared: shared, queue: s.mergeQueue})
	})
}

// OnHeldLease runs fn in one transaction on the agent's held lease, or fails
// with ErrNoLease. See withHeldLease for why the lease is looked up only
// once the claim lock is held.
func (s *PostgresStore) OnHeldLease(ctx context.Context, agentID uuid.UUID, fn func(tx TransitionTx, lease *HeldLease) error) error {
	shared, err := s.sharedPaths(ctx)
	if err != nil {
		return err
	}
	return s.withHeldLease(ctx, agentID, func(q db.Querier, leaseID, ticketID int64) error {
		attempt, err := heldAttempt(ctx, q, agentID, ticketID)
		if err != nil {
			return err
		}
		return fn(&pgTransition{q: q, shared: shared, queue: s.mergeQueue, agent: &agentID, attempt: attempt},
			&HeldLease{ID: leaseID, TicketID: ticketID, AgentID: agentID, Attempt: attempt.Number})
	})
}

func (s *PostgresStore) underClaimLock(ctx context.Context, fn func(q db.Querier) error) error {
	return s.inTxSettling(ctx, func(q db.Querier) error {
		if err := db.TakeAdvisoryLock(ctx, q, claimAdvisoryKey); err != nil {
			return err
		}
		return fn(q)
	})
}

// pgTransition is TransitionTx over the transaction InTransition or
// OnHeldLease opened. Each method is a write the caller has already decided
// on; none of them judges whether it should happen.
type pgTransition struct {
	q db.Querier
	// shared is read before the transaction opens: reading it inside would
	// check out a second connection while this one holds the claim lock.
	shared *scope.SharedPaths
	// queue owns the landing rows this transaction may need removed.
	queue MergeQueue
	// agent is the holder, for a transition made on a held lease; nil for a
	// person's.
	agent *uuid.UUID
	// attempt is the holder's lease's attempt, which its writes are for; nil
	// for a person's, which are for the promoted attempt (see attemptsToWrite)
	// unless the person's decision is about one (ForAttempt).
	attempt *AttemptRef
	// cause is why the lease ended, once it has: the reason the status moved
	// that the record carries.
	cause string
}

// mover is who writes the next status: the holder, or the person the
// context names -- and the system for a route that names none.
func (t *pgTransition) mover(ctx context.Context) *statusMover {
	cause := t.cause
	if cause == "" {
		cause = causeFrom(ctx)
	}
	switch {
	case t.agent != nil:
		return agentMover(*t.agent, cause)
	case userFrom(ctx) != nil:
		return userMover(userFrom(ctx), cause)
	}
	return systemMover(cause)
}

func (t *pgTransition) Querier() db.Querier { return t.q }

func (t *pgTransition) Ticket(ctx context.Context, id int64) (*Ticket, error) {
	if t.agent == nil && t.attempt != nil {
		return loadTicketOnAttempt(ctx, t.q, t.attempt)
	}
	held, err := loadTicket(ctx, t.q, id)
	if err != nil || t.agent == nil || t.attempt == nil {
		return held, err
	}
	// The holder's decisions (done, review, the merge gate) are about its own
	// attempt: a sibling's discussion does not satisfy the flag for it.
	return held, seenByAttempt(ctx, t.q, held, t.attempt)
}

func (t *pgTransition) ForAttempt(ref *AttemptRef) TransitionTx {
	scoped := *t
	scoped.attempt = ref
	return &scoped
}

func (t *pgTransition) Tickets(ctx context.Context, ids []int64) ([]Ticket, error) {
	return listTickets(ctx, t.q, &ListFilter{IDs: ids})
}

func (t *pgTransition) UpdateFields(ctx context.Context, id int64, params *UpdateParams) error {
	return updateFields(ctx, t.q, id, params)
}

func (t *pgTransition) HoldingScope(ctx context.Context, ids []int64) (map[int64]bool, error) {
	return holdingScope(ctx, t.q, ids)
}

func (t *pgTransition) ScopeConflicts(ctx context.Context, ids []int64) ([]Blocked, error) {
	return scopeConflicts(ctx, t.q, ids, t.shared)
}

func (t *pgTransition) SetRequiredRoles(ctx context.Context, id int64, req *RoleRequirement) error {
	return setRequiredRoles(ctx, t.q, id, req)
}

// WriteTicket writes the ticket first, its attempts second and its status
// last. The first write always runs (updated_at moves on every write), so it
// is what takes the ticket's row; the attempt write after it then finds the
// row held. That is the lock order of the rollout, ticket then attempt,
// which a pod of the previous release also takes: see AttemptRef.
//
// The status is the attempts' to decide: the write moves the attempts it is
// for (attemptsToWrite) and the ticket's status is then worked out from all
// of them (derivedStatus). That is a statement of its own because it reads the
// attempts just written, and the cooldown columns the first statement wrote. A
// person's preparing, parked and cancelled are the ticket's own and are set
// by the first statement; an agent's write leaves a ticket outside the queue
// lifecycle where a person put it.
func (t *pgTransition) WriteTicket(ctx context.Context, id int64, w *TicketWrite) error {
	if err := t.mover(ctx).set(ctx, t.q); err != nil {
		return err
	}
	ticketSets, attemptSets := ticketWriteSets(w)
	if _, err := t.q.Exec(ctx, `UPDATE tickets t SET `+strings.Join(ticketSets.clauses, ", ")+` WHERE t.id = $1`,
		append([]any{id}, ticketSets.args...)...); err != nil {
		return fmt.Errorf("write ticket #%d: %w", id, err)
	}
	if err := writeAttemptsWhere(ctx, t.q, id, t.attemptsToWrite(w), attemptSets); err != nil {
		return fmt.Errorf("write ticket #%d: %w", id, err)
	}
	if err := t.writeDiscussion(ctx, id, w); err != nil {
		return err
	}
	if _, moved := w.Status.Status(); t.agent == nil && !w.KeepsLanding && (moved || w.Status.IsQueue()) {
		if err := t.dropLandingsOutOfQueue(ctx, id); err != nil {
			return err
		}
	}
	if status, ok := w.Status.Status(); !w.Status.IsQueue() && (!ok || settledDirectly(status)) {
		return nil
	}
	cond := `t.id = $1`
	if t.agent != nil {
		cond += ` AND ` + inQueueLifecycle
	}
	if _, err := deriveStatus(ctx, t.q, cond, id); err != nil {
		return fmt.Errorf("write ticket #%d: %w", id, err)
	}
	return nil
}

// attemptsToWrite selects, as SQL on an attempt pa, the attempts w is for:
//   - the holder's: the attempt of the lease it is acting on;
//   - a person's, the promoted attempt, or while none is promoted (before any
//     attempt has started) every unfinished one -- a person's move is not
//     for one of several queued attempts more than another;
//   - cancelling a ticket closes every live attempt, and the promoted one
//     as well, which may be done.
func (t *pgTransition) attemptsToWrite(w *TicketWrite) string {
	if t.attempt != nil {
		return fmt.Sprintf("pa.number = %d", t.attempt.Number)
	}
	unfinished := `pa.status NOT IN ` + AttemptEndedStatusesSQL
	if status, ok := w.Status.Status(); ok && status == StatusCancelled {
		return `pa.promoted_at IS NOT NULL OR ` + unfinished
	}
	return `pa.promoted_at IS NOT NULL OR (` + unfinished + ` AND NOT ` +
		`EXISTS (SELECT 1 FROM attempts p WHERE p.ticket_id = pa.ticket_id AND p.promoted_at IS NOT NULL))`
}

// setList is the SET clauses of one UPDATE and their arguments, numbered from
// first: the ticket's write keys its row by $1, the attempt's by $1 and $2.
type setList struct {
	clauses []string
	args    []any
	// first is the number of the first argument's placeholder.
	first int
}

func (l *setList) raw(clauses ...string) { l.clauses = append(l.clauses, clauses...) }

func (l *setList) add(column string, value any) {
	l.args = append(l.args, value)
	l.clauses = append(l.clauses, fmt.Sprintf("%s = $%d", column, len(l.args)+l.first-1))
}

// ticketWriteSets are w's SET clauses, split by the row they write: the
// ticket's, and the attempt's -- the fields that moved to attempts, its
// status and its times. Both are numbered from $2: the ticket's row is keyed
// by $1, and an attempt's by the ticket in $1 and a condition with no
// arguments.
func ticketWriteSets(w *TicketWrite) (ticket, attempt *setList) {
	ticket, attempt = &setList{clauses: []string{"updated_at = now()"}, first: 2},
		&setList{clauses: []string{"updated_at = now()"}, first: 2}
	// The attempt takes the status asked for; the ticket's is worked out
	// from its attempts afterwards, apart from the few a person sets
	// directly (see WriteTicket).
	if status, ok := w.Status.Status(); ok {
		attempt.args = append(attempt.args, string(status))
		n := len(attempt.args) + attempt.first - 1
		// The right-hand sides read the row as it was, so a write that finds
		// the attempt already in discussion opens no new round.
		attempt.raw(fmt.Sprintf("discussion_round = discussion_round + CASE WHEN status <> 'discussion' AND attempt_status_for_ticket($%d::text) = 'discussion' THEN 1 ELSE 0 END", n),
			fmt.Sprintf("status = attempt_status_for_ticket($%d::text)", n))
		if settledDirectly(status) {
			ticket.add("status", string(status))
		}
	}
	if w.Status.IsQueue() {
		attempt.raw("status = 'queued'")
	}
	if w.Cooldown != nil {
		ticket.add("blocked_until", w.Cooldown.Until)
		ticket.add("cooldown_releases", w.Cooldown.Streak.Releases)
		ticket.add("cooldown_since", w.Cooldown.Streak.Since)
	} else if _, ok := w.Status.Status(); ok || w.Status.IsQueue() {
		ticket.raw("blocked_until = NULL", "cooldown_releases = 0", "cooldown_since = NULL")
	}
	switch w.Closing {
	case closingKept:
	case closingClosed:
		// The ticket is closed when its status is terminal, which for a status
		// worked out from its attempts is deriveStatus's to say: an attempt
		// finishing is not the ticket finishing while a sibling is live.
		if status, ok := w.Status.Status(); ok && settledDirectly(status) {
			ticket.raw("closed_at = now()")
		}
		attempt.raw("closed_at = now()")
	case closingReopened:
		ticket.raw("closed_at = NULL")
		attempt.raw("closed_at = NULL")
	}
	if w.NeedsDiscussion != nil {
		ticket.add("needs_discussion", *w.NeedsDiscussion)
	}
	if w.Discussed {
		attempt.raw("discussed_at = now()")
	}
	if w.PRURL != nil {
		attempt.add("pr_url", *w.PRURL)
	}
	switch w.FirstClaim.kind {
	case firstClaimKept:
	case firstClaimCleared:
		attempt.raw("resume_agent_id = NULL", "affinity_until = NULL")
	case firstClaimOwed:
		attempt.add("resume_agent_id", w.FirstClaim.agent)
		attempt.raw("affinity_until = NULL")
	case firstClaimWindow:
		// now() rather than a time computed here: claims compare the window
		// with the database's clock, which this process's need not match.
		attempt.args = append(attempt.args, w.FirstClaim.window.String())
		// Only a worker polls for work and so comes back for the ticket on
		// its own. A console acts when its human types and a conductor is
		// never sent to claim, so a window for them would only idle the
		// answer -- and the ticket page would promise them a first claim
		// nobody takes. resume_agent_id still records who asked.
		attempt.raw(fmt.Sprintf(`affinity_until = CASE WHEN EXISTS (
			SELECT 1 FROM agents a
			 WHERE a.id = pa.resume_agent_id
			   AND `+idleWorkerClause+`)
			THEN now() + $%d::interval END`, len(attempt.args)+attempt.first-1))
	}
	switch w.Review.kind {
	case reviewKept:
	case reviewDiscarded:
		attempt.raw(reviewOutcomeCleared...)
	case reviewStarted:
		attempt.raw(reviewOutcomeCleared...)
		attempt.raw(reviewStartedNow)
	case reviewApproved:
		attempt.raw("approved_at = now()")
		attempt.add("approved_by_user_id", w.Review.by)
		attempt.raw("changes_requested_at = NULL")
	case reviewChangesRequested:
		attempt.raw("approved_at = NULL", "approved_by_user_id = NULL", "approved_by_user_id_deleted = false", "changes_requested_at = now()")
	}
	if w.ClearsPostReview {
		ticket.raw(postReviewCleared...)
	}
	return ticket, attempt
}

func (t *pgTransition) Comment(ctx context.Context, ticketID int64, c *NewComment) (*Comment, error) {
	// A person's decision about one attempt speaks to that attempt: its
	// comment is that attempt's and reaches only its holder.
	if c.Attempt == nil && t.agent == nil && t.attempt != nil {
		c = &NewComment{By: c.By, Body: c.Body, Attempt: t.attempt, IdempotencyKey: c.IdempotencyKey}
	}
	if c.Attempt != nil {
		if c.Attempt.TicketID != ticketID {
			return nil, fmt.Errorf("comment on #%d addressed to %s", ticketID, c.Attempt)
		}
		return insertCommentOn(ctx, t.q, OnAttempt(c.Attempt), c.By, c.Body, c.IdempotencyKey)
	}
	return insertComment(ctx, t.q, ticketID, c.By, c.Body)
}

func (t *pgTransition) Handoff(ctx context.Context, ticketID int64, agentID uuid.UUID, body string) error {
	attempt, err := heldAttempt(ctx, t.q, agentID, ticketID)
	if err != nil {
		return fmt.Errorf("record handoff: %w", err)
	}
	if _, err := t.q.Exec(ctx, `
		INSERT INTO agent_contexts (ticket_id, attempt_number, agent_id, body)
		VALUES ($1, $2, $3, $4)`, ticketID, attempt.Number, agentID, body); err != nil {
		return fmt.Errorf("record handoff: %w", err)
	}
	return nil
}

func (t *pgTransition) EndLease(ctx context.Context, leaseID int64, reason string) error {
	t.cause = reason
	return endLease(ctx, t.q, leaseID, reason)
}

// AddBlocker stays SQL through and through: the cycle check is graph-wide,
// no constraint can express it, and it is only sound atomically with the
// insert it guards -- see addDependency.
func (t *pgTransition) AddBlocker(ctx context.Context, by Author, ticketID, dependsOnID int64, note string) (bool, error) {
	return addBlocker(ctx, t.q, by, ticketID, dependsOnID, note)
}

func (t *pgTransition) CooldownStreak(ctx context.Context, ticketID int64) (CooldownStreak, error) {
	var streak CooldownStreak
	if err := t.q.QueryRow(ctx,
		`SELECT cooldown_releases, cooldown_since FROM tickets WHERE id = $1`, ticketID).Scan(&streak.Releases, &streak.Since); err != nil {
		return CooldownStreak{}, fmt.Errorf("read cooldown streak of #%d: %w", ticketID, err)
	}
	return streak, nil
}

// UnblockSatisfied is a set-based sweep rather than a decision per ticket:
// see unblockSatisfied.
func (t *pgTransition) UnblockSatisfied(ctx context.Context) error {
	return unblockSatisfied(ctx, t.q)
}

// RevokeLeases first puts the attempts whose leases it ends back in the queue
// and works the ticket's status out again: a person's move wrote the
// promoted attempt, and a sibling still running on a lease taken from it
// would otherwise be left in progress with no one to claim it. Then it reads
// the status to revoke to rather than taking it from the caller: where the
// ticket landed was decided by SQL, in the writes before this.
func (t *pgTransition) RevokeLeases(ctx context.Context, ticketID int64) error {
	rows, err := t.q.Query(ctx, `
		SELECT l.ticket_id, l.attempt_number FROM leases l
		 WHERE l.ticket_id = $1 AND l.released_at IS NULL`, ticketID)
	if err != nil {
		return fmt.Errorf("find the attempts to requeue: %w", err)
	}
	held, err := endedAttempts(rows)
	if err != nil {
		return fmt.Errorf("find the attempts to requeue: %w", err)
	}
	if len(held) > 0 {
		requeued, err := t.q.Exec(ctx, `
			UPDATE attempts pa SET status = 'queued', updated_at = now()
			 WHERE pa.ticket_id = $1 AND pa.number = ANY($2) AND pa.status = 'in_progress'`,
			ticketID, attemptNumbers(held))
		if err != nil {
			return fmt.Errorf("requeue the revoked attempts: %w", err)
		}
		if requeued.RowsAffected() > 0 {
			if _, err := deriveStatus(ctx, t.q, `t.id = $1 AND `+inQueueLifecycle, ticketID); err != nil {
				return err
			}
		}
	}
	var status Status
	if err := t.q.QueryRow(ctx, `SELECT status FROM tickets WHERE id = $1`, ticketID).Scan(&status); err != nil {
		return fmt.Errorf("read status to revoke to: %w", err)
	}
	return revokeLeaseOn(ctx, t.q, ticketID, status)
}

// dropLandingsOutOfQueue removes the ticket's row from the landing queue when
// its attempt is no longer in the merge queue: a person moved the ticket out,
// and a row left behind would have the server land what they took back. A row
// handed back to its author is not the queue's to drop, and stays.
func (t *pgTransition) dropLandingsOutOfQueue(ctx context.Context, ticketID int64) error {
	_, err := t.queue.Dequeue(ctx, t.q, &DequeueRequest{TicketID: ticketID, Reason: DequeueLeftQueue})
	return err
}

func (t *pgTransition) Dequeue(ctx context.Context, req *DequeueRequest) (bool, error) {
	return t.queue.Dequeue(ctx, t.q, req)
}
