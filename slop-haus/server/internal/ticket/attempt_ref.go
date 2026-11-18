package ticket

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// AttemptRef names one attempt of a ticket. Writes to the fields that moved
// to attempts take one rather than a bare ticket id, so a ticket with
// several attempts cannot have the wrong one changed.
//
// Lock order: the ticket's row, then the attempt's. While a deploy rolls, a
// pod of the previous release writes `tickets` and a trigger then writes the
// ticket's first attempt, and a pod of this one writes the attempt and a
// trigger then writes the ticket. Opposite orders over the same two rows
// deadlock, so every path that writes an attempt holds its ticket's row first
// (writeAttempt checks nothing: callers take it, and say how where they
// write).
type AttemptRef struct {
	TicketID int64
	Number   int
}

// ErrNoPromotedAttempt is a write to a ticket none of whose attempts is
// promoted. Not reachable before a ticket can have several attempts.
var ErrNoPromotedAttempt = errors.New("ticket: no promoted attempt")

// promotedAttempt is the attempt ticket-level writes go to: the one ticket-level
// reads come from (AttemptJoin).
func promotedAttempt(ctx context.Context, q db.Querier, ticketID int64) (*AttemptRef, error) {
	ref := AttemptRef{TicketID: ticketID}
	err := q.QueryRow(ctx, `SELECT number FROM attempts WHERE ticket_id = $1 AND promoted_at IS NOT NULL`,
		ticketID).Scan(&ref.Number)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("ticket #%d: %w", ticketID, ErrNoPromotedAttempt)
	}
	if err != nil {
		return nil, fmt.Errorf("resolve promoted attempt: %w", err)
	}
	return &ref, nil
}

// writeAttempt applies sets to the attempt ref names. $1 and $2 of the
// statement are the ticket and the number (the sets are numbered from $3),
// and the attempt is aliased pa, which a set's own subquery may use. The
// ticket's row must already be held by the caller's transaction (see
// AttemptRef).
func writeAttempt(ctx context.Context, q db.Querier, ref *AttemptRef, sets *setList) error {
	args := append([]any{ref.TicketID, ref.Number}, sets.args...)
	if _, err := q.Exec(ctx, `UPDATE attempts pa SET `+strings.Join(sets.clauses, ", ")+`
		 WHERE pa.ticket_id = $1 AND pa.number = $2`, args...); err != nil {
		return fmt.Errorf("write attempt %d of ticket #%d: %w", ref.Number, ref.TicketID, err)
	}
	return nil
}

// writeAttemptsWhere applies sets to the attempts of ticketID that cond
// selects, aliased pa. $1 is the ticket and the sets are numbered from $2.
// cond is SQL from this package, never input. The ticket's row must already
// be held by the caller's transaction (see AttemptRef).
func writeAttemptsWhere(ctx context.Context, q db.Querier, ticketID int64, cond string, sets *setList) error {
	args := append([]any{ticketID}, sets.args...)
	if _, err := q.Exec(ctx, `UPDATE attempts pa SET `+strings.Join(sets.clauses, ", ")+`
		 WHERE pa.ticket_id = $1 AND (`+cond+`)`, args...); err != nil {
		return fmt.Errorf("write attempts of ticket #%d: %w", ticketID, err)
	}
	return nil
}

// ErrNoLiveAttempt is a ticket whose status cannot be worked out: none of its
// attempts is live and none is promoted, as when every attempt has been
// closed.
var ErrNoLiveAttempt = errors.New("ticket: no live attempt and none promoted")

// deriveStatus sets the status of the tickets t that cond selects to what
// derivedStatus gives, and returns their ids. closed_at follows: set (once)
// when the status is done and cleared otherwise, so a finished sibling of a
// live attempt does not close its ticket. The tickets' rows must already
// be held by the caller's transaction, being what its earlier statement
// wrote, which is the lock order AttemptRef names; and the attempts it reads
// must already be written.
func deriveStatus(ctx context.Context, q db.Querier, cond string, args ...any) ([]int64, error) {
	ids, err := updatedIDs(ctx, q, `
		UPDATE tickets t SET status = `+derivedStatus+`, updated_at = now(),
		       closed_at = CASE WHEN `+derivedStatus+` = 'done' THEN COALESCE(t.closed_at, now()) END
		 WHERE `+cond+`
		 RETURNING t.id`, args...)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23502" && pgErr.ColumnName == "status" {
		return nil, ErrNoLiveAttempt
	}
	if err != nil {
		return nil, fmt.Errorf("derive ticket status: %w", err)
	}
	return ids, nil
}

// requeueAttempts puts the attempts the ended leases were on back to queued,
// and works out the status of their tickets again. For the statements that
// end many leases at once (an expiry, a disabled agent's release): the ticket
// shows in_progress while a sibling attempt still runs, and is open or
// blocked otherwise. Only a ticket still in progress is moved; a person's
// move of it in the meantime stands.
//
// The tickets' rows are taken first, in id order, then the attempts' -- the
// lock order AttemptRef names.
func requeueAttempts(ctx context.Context, q db.Querier, ended []AttemptRef) error {
	if len(ended) == 0 {
		return nil
	}
	tickets, numbers := make([]int64, len(ended)), attemptNumbers(ended)
	for i, ref := range ended {
		tickets[i] = ref.TicketID
	}
	if _, err := q.Exec(ctx, `SELECT id FROM tickets WHERE id = ANY($1) ORDER BY id FOR UPDATE`, tickets); err != nil {
		return fmt.Errorf("lock the tickets of %d attempts: %w", len(ended), err)
	}
	if _, err := q.Exec(ctx, `
		UPDATE attempts pa SET status = 'queued', updated_at = now()
		  FROM unnest($1::bigint[], $2::int[]) AS e(ticket_id, number)
		 WHERE pa.ticket_id = e.ticket_id AND pa.number = e.number AND pa.status = 'in_progress'`,
		tickets, numbers); err != nil {
		return fmt.Errorf("requeue %d attempts: %w", len(ended), err)
	}
	_, err := deriveStatus(ctx, q, `t.id = ANY($1) AND t.status = 'in_progress'`, tickets)
	return err
}

func attemptNumbers(refs []AttemptRef) []int32 {
	numbers := make([]int32, len(refs))
	for i, ref := range refs {
		numbers[i] = int32(ref.Number)
	}
	return numbers
}

// endedAttempts collects the (ticket, attempt) pairs a statement returned.
func endedAttempts(rows pgx.Rows) ([]AttemptRef, error) {
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (AttemptRef, error) {
		var ref AttemptRef
		err := row.Scan(&ref.TicketID, &ref.Number)
		return ref, err
	})
}

// updatedIDs runs an UPDATE ... RETURNING id and collects the ids.
func updatedIDs(ctx context.Context, q db.Querier, sql string, args ...any) ([]int64, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// heldAttempt resolves the attempt an agent's lease on ticketID is for.
// Workers pass only a ticket id: an agent holds one lease, and a lease is for
// one attempt, so (agent, ticket) names it. ErrNoLease when the agent holds
// no live lease on that ticket.
func heldAttempt(ctx context.Context, q db.Querier, agentID uuid.UUID, ticketID int64) (*AttemptRef, error) {
	ref := AttemptRef{TicketID: ticketID}
	err := q.QueryRow(ctx, `
		SELECT l.attempt_number FROM leases l
		 WHERE l.agent_id = $1 AND l.ticket_id = $2 AND `+activeLeaseClause, agentID, ticketID).Scan(&ref.Number)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoLease
	}
	if err != nil {
		return nil, fmt.Errorf("resolve attempt: %w", err)
	}
	return &ref, nil
}

// HeldAttempt is the attempt the agent's live lease on ticketID is for.
func (s *PostgresStore) HeldAttempt(ctx context.Context, agentID uuid.UUID, ticketID int64) (*AttemptRef, error) {
	return heldAttempt(ctx, s.db, agentID, ticketID)
}

// ErrNoAttemptForAgent is a claim of a ticket none of whose attempts the agent
// may take: each is leased, routed to another variant version, or held for
// another agent. Candidate selection already excludes such a ticket, and the
// claim lock serialises it with SetAttemptVariant, so it is reached only if
// routing is bypassed.
var ErrNoAttemptForAgent = errors.New("ticket: no attempt of the ticket is available to the agent")

// attemptVariantMatches is true, for a ticket's attempt aliased attempt, when
// the agent whose id is the SQL expression agent may take it as far as
// variants go. Strict both ways: an agent in a variant takes only attempts
// naming exactly its version, and an agent in none takes only attempts naming
// none. Roles are a separate condition (meetsRoles) applied on top. agent is
// only ever a constant from this package -- a placeholder or a column --
// never input.
func attemptVariantMatches(attempt, agent string) string {
	return attempt + `.variant_version_id IS NOT DISTINCT FROM (SELECT av.variant_id FROM agents av WHERE av.id = ` + agent + `)`
}

// takeableAttempt is true of a ticket's attempt aliased attempt that the
// agent may take now: queued (or in progress with no live lease, when stale
// is true -- Peek's probe, which cannot run the expiry sweep), with no live
// lease, routed to the agent's variant, and not held back for another agent
// by its own resume affinity (not if that agent has since been disabled or
// made a console: nothing is coming back for it). ticket, agent and stale are
// SQL expressions from this package, never input. The one definition shared
// by the claim's choice (attemptToClaim) and the candidate query, so a ticket
// is never offered for an attempt the claim then refuses.
func takeableAttempt(attempt, ticket, agent, stale string) string {
	return `(` + attempt + `.ticket_id = ` + ticket + `
		AND (` + attempt + `.replay OR NOT EXISTS (SELECT 1 FROM tickets pt
		                                            WHERE pt.id = ` + attempt + `.ticket_id AND pt.status = 'parked'))
		AND (` + attempt + `.status = 'queued'
		     OR (` + stale + ` AND ` + attempt + `.status = 'in_progress'))
		AND NOT EXISTS (SELECT 1 FROM leases l
		                 WHERE l.ticket_id = ` + attempt + `.ticket_id AND l.attempt_number = ` + attempt + `.number
		                   AND ` + activeLeaseClause + `)
		AND ` + attemptVariantMatches(attempt, agent) + `
		AND NOT EXISTS (SELECT 1 FROM landings ln
		                 WHERE ln.ticket_id = ` + attempt + `.ticket_id AND ln.attempt_number = ` + attempt + `.number
		                   AND ln.queue_state <> 'back_with_agent')
		AND NOT (COALESCE(` + attempt + `.affinity_until > now(), false)
		         AND ` + attempt + `.resume_agent_id IS DISTINCT FROM ` + agent + `
		         AND EXISTS (SELECT 1 FROM agents a
		                      WHERE a.id = ` + attempt + `.resume_agent_id AND ` + enabledWorkerClause + `)))`
}

// attemptToClaim is the attempt a new lease on ticketID is for: the
// lowest-numbered one the agent may take (takeableAttempt), shadows included.
// Two agents can so hold two attempts of one ticket. ErrNoAttemptForAgent
// when none qualifies. The one place a claim's attempt is chosen.
func attemptToClaim(ctx context.Context, q db.Querier, agentID uuid.UUID, ticketID int64) (*AttemptRef, error) {
	ref := AttemptRef{TicketID: ticketID}
	err := q.QueryRow(ctx, `
		SELECT ca.number FROM attempts ca
		 WHERE `+takeableAttempt("ca", "$1", "$2", "false")+`
		 ORDER BY ca.number LIMIT 1`, ticketID, agentID).Scan(&ref.Number)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("ticket #%d: %w", ticketID, ErrNoAttemptForAgent)
	}
	if err != nil {
		return nil, fmt.Errorf("choose the attempt to claim: %w", err)
	}
	return &ref, nil
}

// HeldAttemptOf is the attempt the agent's live lease on ticketID is for,
// read through q, for a write in the caller's transaction that belongs to the
// agent's attempt (a change it opens).
func HeldAttemptOf(ctx context.Context, q db.Querier, agentID uuid.UUID, ticketID int64) (*AttemptRef, error) {
	return heldAttempt(ctx, q, agentID, ticketID)
}

// loadTicketOnAttempt is loadTicket for a person's decision about one
// attempt: the ticket with that attempt's status in place of the one worked
// out for the ticket, and its own PR, review and first-claim fields in place
// of the promoted attempt's. A transition written for a ticket (answer a
// discussion, end a review) then decides on the attempt it is for. A queued
// attempt reads as open: the ticket's own status says whether it is blocked,
// and no decision about an attempt turns on that.
func loadTicketOnAttempt(ctx context.Context, q db.Querier, ref *AttemptRef) (*Ticket, error) {
	t, err := loadTicket(ctx, q, ref.TicketID)
	if err != nil {
		return nil, err
	}
	var status string
	var discussed, promoted, replay bool
	err = q.QueryRow(ctx, `
		SELECT status, promoted_at IS NOT NULL, resume_agent_id, affinity_until, pr_url, approved_at, approved_by_user_id, approved_by_user_id_deleted,
		       changes_requested_at, review_started_at, discussed_at IS NOT NULL, replay
		  FROM attempts WHERE ticket_id = $1 AND number = $2`, ref.TicketID, ref.Number).
		Scan(&status, &promoted, &t.ResumeAgentID, &t.AffinityUntil, &t.PRURL, &t.ApprovedAt, &t.ApprovedByUserID, &t.ApprovedByDeleted,
			&t.ChangesRequestedAt, &t.ReviewStartedAt, &discussed, &replay)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%s: %w", ref, ErrNoSuchAttempt)
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", ref, err)
	}
	t.NeedsDiscussion = t.NeedsDiscussion && !discussed
	// Read before the status below replaces the ticket's own. Only a live
	// shadow is told to carry on; a finished one has nothing left to do.
	t.DoneThroughOther = (t.Status == StatusDone && !promoted && status != "done" && status != "closed") ||
		(t.Status == StatusParked && replay && status != "done" && status != "closed")
	t.Replay = t.DoneThroughOther && replay
	t.Status = Status(status)
	if status == "queued" && t.Status != StatusBlocked {
		t.Status = StatusOpen
	}
	return t, nil
}
