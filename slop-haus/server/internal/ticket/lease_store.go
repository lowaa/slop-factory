package ticket

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

// claimAdvisoryKey serializes every claim. The key itself lives in
// internal/db with the rest of them, because the advisory key space is
// database-wide -- see db.ClaimLockKey, which also explains why settings
// writes take this same lock.
const claimAdvisoryKey = db.ClaimLockKey

// activeLeaseClause is the one definition of "held", shared by the expiry
// sweep, the heartbeat, release, completion and the conflict scan.
//
// The pinned clause is why this must be one definition rather than a rule
// the sweep applies alone. A merge that runs past the nominal TTL leaves
// expires_at in the past while the integration lock holds the lease alive;
// if only the sweep knew about pinning, the expires_at guard on change_merge and ticket_finish
// would reject the agent's legitimate completion of the very ticket it is
// still holding.
const activeLeaseClause = `
	l.released_at IS NULL
	AND (l.expires_at > now()
	     OR EXISTS (SELECT 1 FROM locks lk WHERE lk.holder_lease_id = l.id))`

// ActiveLeaseClause is activeLeaseClause for a caller outside the package,
// naming the lease row l: there is one definition of "held".
const ActiveLeaseClause = activeLeaseClause

// claimBarrier is a test-only seam. Production leaves it nil.
//
// It exists because the invariant the advisory lock protects cannot be
// exercised from the outside: a claim completes in about a millisecond, so
// goroutines started together still run one after another and each sees the
// previous one's committed lease. A test with no barrier passes with the
// lock removed -- verified -- which makes it worse than no test, since it
// looks like coverage of the one thing most worth covering.
//
// Tests set it to a function that blocks until a second claim has also
// reached this point, forcing a genuine overlap inside both transactions.
// With the lock in place the second claim never gets here while the first
// is waiting, so the barrier must time out rather than block forever.
var claimBarrier func()

// expireStale releases every lease past its deadline that no lock is
// pinning, and puts the attempts they were on back in the queue; each
// attempt's ticket is then worked out from its attempts again (see
// requeueAttempts) -- blocked if a blocker was added while it was held, and
// still in progress while a sibling attempt holds a live lease. Run inside
// the claim lock before conflicts are evaluated, so a dead agent's scope
// doesn't block a live one.
//
// One set-based statement for the leases rather than a transition per
// lease: it requeues exactly the attempts whose leases this statement
// expired, which a separate read-then-write could only approximate.
func expireStale(ctx context.Context, q db.Querier) error {
	if err := systemMover("lease_expired").set(ctx, q); err != nil {
		return err
	}
	rows, err := q.Query(ctx, `
		UPDATE leases l
		   SET released_at = now(), release_reason = 'expired'
		 WHERE l.released_at IS NULL
		   AND l.expires_at <= now()
		   AND NOT EXISTS (SELECT 1 FROM locks lk WHERE lk.holder_lease_id = l.id)
		 RETURNING l.ticket_id, l.attempt_number`)
	if err != nil {
		return fmt.Errorf("expire stale leases: %w", err)
	}
	expired, err := endedAttempts(rows)
	if err != nil {
		return fmt.Errorf("expire stale leases: %w", err)
	}
	return requeueAttempts(ctx, q, expired)
}

// activeScopes returns every path currently spoken for, with the ticket and
// agent holding it, so a conflict can name what actually blocked the claim.
//
// A ticket in review holds its scopes with no lease: its PR is unmerged, and
// another agent building on the paths it touches would be building on code
// that is about to change under it. Its agent is the one owed the ticket
// back, or uuid.Nil if none is. The attempt in review is the one that holds
// it, whether or not it is the promoted one: a shadow can be the one waiting
// on a person while the promoted attempt is still queued.
type heldScope struct {
	path     string
	ticketID int64
	agentID  uuid.UUID
	inReview bool
}

func activeScopes(ctx context.Context, q db.Querier) ([]heldScope, error) {
	rows, err := q.Query(ctx, `
		SELECT p.path, l.ticket_id, l.agent_id, false
		  FROM leases l
		  JOIN ticket_paths p ON p.ticket_id = l.ticket_id
		 WHERE `+activeLeaseClause+` AND NOT `+shadowLeaseClause+`
		UNION ALL
		SELECT p.path, t.id, COALESCE(
		         (SELECT ra.resume_agent_id FROM attempts ra
		           WHERE ra.tenant_id = t.tenant_id AND ra.ticket_id = t.id AND ra.status = 'review'
		           ORDER BY ra.promoted_at IS NULL, ra.number LIMIT 1),
		         '00000000-0000-0000-0000-000000000000'), true
		  FROM tickets t
		  JOIN ticket_paths p ON p.ticket_id = t.id
		 WHERE t.status = 'review'`)
	if err != nil {
		return nil, fmt.Errorf("load active scopes: %w", err)
	}
	defer rows.Close()

	scopes := []heldScope{}
	for rows.Next() {
		var s heldScope
		if err := rows.Scan(&s.path, &s.ticketID, &s.agentID, &s.inReview); err != nil {
			return nil, fmt.Errorf("scan active scope: %w", err)
		}
		scopes = append(scopes, s)
	}
	return scopes, rows.Err()
}

// enabledWorkerClause names the agents that come back for work on their own:
// a worker that is not a console and not disabled. It is the one definition of
// who a first-claim window is written for and who it is honoured for, so the
// two cannot drift when an agent is toggled to a console mid-window.
const enabledWorkerClause = `a.kind = 'worker' AND a.disabled_at IS NULL`

// idleWorkerClause is enabledWorkerClause for an agent holding no live lease:
// one that is busy on another ticket cannot take a reserved attempt soon, so
// reserving it would only idle the attempt while other slots are free.
const idleWorkerClause = enabledWorkerClause + `
	AND NOT EXISTS (SELECT 1 FROM leases l WHERE l.agent_id = a.id AND l.tenant_id = a.tenant_id AND ` + activeLeaseClause + `)`

// candidateTickets returns claimable tickets in the order they should be
// offered: every dependency terminal, highest priority first, oldest first
// within a priority.
//
// "Terminal" is done *or* cancelled, matching Status.IsTerminal. If this
// query accepted only 'done', cancelling a ticket would silently strand
// everything depending on it, blocked forever on work nobody will do.
//
// A ticket whose discussion or review a human has just answered is held back
// for the agent that handed it over until its affinity window closes -- see
// ResumeAffinity. Not if that agent has since been disabled: nothing is
// coming back for it, and holding it would only idle the answer; likewise an
// agent since made a console. The window is also written only for an enabled
// worker (ticketWriteSets), so the ticket page never promises one to anyone else.
//
// Roles are checked here too, beside the affinity rule's disabled_at: a
// ticket requiring roles is a candidate only for an agent that meets them
// (see meetsRoles). Here, rather than in Service, so Claim and Peek -- the
// Stop hook's probe -- can't disagree about it, and so Claim judges it in
// the transaction that grants the lease.
//
// A done ticket is a candidate for the sake of its shadows: one still queued
// when the promoted attempt merged keeps running to its own end (its PR and CI
// with it), so it must stay claimable. Its dependencies are not asked again
// (they were met when it ran), and only an attempt that is not the promoted
// one counts. The ticket stays done throughout (derivedStatus puts done first).
//
// includeStale widens it to tickets that are still marked in_progress but
// whose lease has lapsed. Claim doesn't need that -- its expiry sweep has
// already re-opened them -- but Peek must not run the sweep (it mutates
// lease state, which a hook may not do), so without this a probe would
// report an idle queue while an abandoned ticket sat waiting.
func candidateTickets(ctx context.Context, q db.Querier, agentID uuid.UUID, only *int64, includeStale bool) ([]int64, error) {
	rows, err := q.Query(ctx, `
		SELECT t.id
		  FROM tickets t
		 WHERE (t.status IN `+holdingStatusesSQL+` OR t.status = 'parked')
		   AND ($1::bigint IS NULL OR t.id = $1)
		   AND (t.blocked_until IS NULL OR t.blocked_until <= now())
		   AND EXISTS (SELECT 1 FROM attempts ca WHERE `+takeableAttempt("ca", "t.id", "$3", "$2::boolean")+`
		                  AND (t.status <> 'done' OR ca.promoted_at IS NULL))
		   AND (t.status IN ('done', 'parked') OR NOT EXISTS (
		       SELECT 1 FROM ticket_deps d
		         JOIN tickets dep ON dep.id = d.depends_on_ticket_id
		        WHERE d.ticket_id = t.id
		          AND dep.status NOT IN `+TerminalStatusesSQL+`))
		   AND `+meetsRoles("$3")+`
		 ORDER BY CASE t.priority
		            WHEN 'urgent' THEN 0
		            WHEN 'high'   THEN 1
		            WHEN 'normal' THEN 2
		            ELSE 3
		          END, t.id`, only, includeStale, agentID)
	if err != nil {
		return nil, fmt.Errorf("load candidates: %w", err)
	}
	defer rows.Close()

	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan candidate: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func pathsForTicket(ctx context.Context, q db.Querier, ticketID int64) ([]string, error) {
	rows, err := q.Query(ctx,
		`SELECT path FROM ticket_paths WHERE ticket_id = $1 ORDER BY path`, ticketID)
	if err != nil {
		return nil, fmt.Errorf("load ticket paths: %w", err)
	}
	defer rows.Close()

	paths := []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("scan ticket path: %w", err)
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}

// Claim is the whole claim path, serialized by an advisory lock held for
// the transaction.
//
// SELECT ... FOR UPDATE SKIP LOCKED is the usual Postgres queue pattern and
// is the wrong fit here: skipping locked rows gives each claimer a partial
// view, but deciding whether a ticket is claimable needs a global view of
// every active lease's paths. Two agents claiming concurrently could each
// conclude their ticket was conflict-free and both be right in isolation,
// and wrong together.
func (s *PostgresStore) Claim(ctx context.Context, agentID uuid.UUID, opts *ClaimOptions, ttl time.Duration, configs ConfigResolver) (*ClaimResult, error) {
	var result ClaimResult
	shared, err := s.sharedPaths(ctx)
	if err != nil {
		return nil, err
	}

	err = s.db.InTx(ctx, func(q db.Querier) error {
		if err := db.TakeAdvisoryLock(ctx, q, claimAdvisoryKey); err != nil {
			return err
		}
		served, proceed, err := claimPreconditions(ctx, q, agentID, opts, configs)
		if err != nil || !proceed {
			return err
		}
		return pickCandidate(ctx, q, agentID, opts.Only, ttl, served, shared, &result)
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// claimPreconditions is everything a claim checks before it looks at a
// ticket, in the claim's transaction. proceed is false for an agent that is
// skipped: it is answered with an empty result, not an error.
func claimPreconditions(ctx context.Context, q db.Querier, agentID uuid.UUID, opts *ClaimOptions, configs ConfigResolver) (served *ServedConfig, proceed bool, err error) {
	// The fleet-wide switch, read inside this transaction and under the
	// claim lock rather than by the caller beforehand. Checked outside,
	// it is a time-of-check/time-of-use gap: the human disables
	// processing between the check and the grant, and one more agent
	// is handed work anyway -- precisely at the moment they were trying
	// to stop that happening.
	//
	// Same reason the disabled_at check below is here rather than in
	// Service. Fleet-level state is checked before agent-level state
	// because it is the broader answer: "nobody is being served" is
	// never misleading, whereas the reverse order would report an
	// agent-specific reason for a fleet-wide pause.
	enabled, err := settings.EnabledIn(ctx, q, settings.TicketProcessingEnabled)
	if err != nil {
		return nil, false, err
	}
	if !enabled {
		return nil, false, ErrProcessingDisabled
	}

	if err := expireStale(ctx, q); err != nil {
		return nil, false, err
	}
	if err := requireNoActiveLease(ctx, q, agentID, "check existing lease"); err != nil {
		return nil, false, err
	}

	// Read straight from agents, in this transaction, so the answer is
	// the one the grant is made against. It asks one column a yes/no
	// question and repeats none of agent's logic, so it needs no helper
	// of its own; the disabled-agent claim tests catch a schema change
	// under it.
	//
	// FOR SHARE, because disabling an agent does not take the claim
	// lock: without it a claim that reads "enabled" while a disable is
	// still uncommitted goes on to grant a lease stamped after the
	// disable's disabled_at, which the timestamp-bounded
	// ReleaseLeasesFor then spares. Waiting on the disable's row lock
	// makes the claim see the committed disable and refuse.
	var skip, console bool
	if err := q.QueryRow(ctx,
		`SELECT disabled_at IS NOT NULL OR kind IN ('conductor', 'supervisor', 'steward'), kind = 'console'
		   FROM agents WHERE id = $1 FOR SHARE`, agentID).Scan(&skip, &console); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, fmt.Errorf("%w: agent %s", ErrNotFound, agentID)
		}
		return nil, false, fmt.Errorf("check agent: %w", err)
	}
	if skip {
		// A disabled agent is skipped rather than served, so switching
		// one off stops it picking up new work immediately instead of
		// at the end of whatever it currently holds. A conductor, a
		// steward or a supervisor is skipped the same way: none is ever handed work
		// (a supervisor's slots take the tickets).
		return nil, false, nil
	}
	// A console takes the ticket it is told to and nothing from the
	// queue. Refused outright, not answered with an empty queue: it would
	// read as "come back later" and the console would wait for work.
	if console && opts.Only == nil {
		return nil, false, ErrConsoleUnnamedClaim
	}
	// Resolved here, after the agent's row is locked: moving an agent
	// into or out of a variant updates that row, so it waits for this
	// claim to commit, and what is served and checked is the membership
	// the grant is made under.
	served, err = resolveServed(ctx, configs, agentID)
	if err != nil {
		return nil, false, err
	}
	// Read once, in the claim's transaction beside the grant: the harness
	// checked is the harness recorded.
	harness, err := claimHarness(ctx, q, agentID, opts.Harness)
	if err != nil {
		return nil, false, err
	}
	if err := checkVariantClaim(served, harness); err != nil {
		return nil, false, err
	}
	return served.withRun(harness, opts), true, nil
}

// requireNoActiveLease is ErrAlreadyHolding when the agent holds a lease.
// what names the check in the error of a failed read.
func requireNoActiveLease(ctx context.Context, q db.Querier, agentID uuid.UUID, what string) error {
	var holds bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM leases l WHERE l.agent_id = $1 AND `+activeLeaseClause+`)`,
		agentID).Scan(&holds); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if holds {
		return ErrAlreadyHolding
	}
	return nil
}

// pickCandidate grants the first candidate no held scope blocks, filling
// result, and records the scope blocks of those it looked at. Nothing
// grantable leaves result.Blocked as the first blockage seen, if any.
func pickCandidate(ctx context.Context, q db.Querier, agentID uuid.UUID, only *int64, ttl time.Duration, served *ServedConfig, shared *scope.SharedPaths, result *ClaimResult) error {
	held, err := activeScopes(ctx, q)
	if err != nil {
		return err
	}
	locking, err := scopeLocking(ctx, q)
	if err != nil {
		return err
	}
	candidates, err := candidateTickets(ctx, q, agentID, only, false)
	if err != nil {
		return err
	}

	if err := endScopeBlocksOfSettled(ctx, q); err != nil {
		return err
	}
	// The tickets this claim looked at and found held back by a scope,
	// for the scope cost report.
	evaluated := make([]int64, 0, len(candidates))
	blockedBy := map[int64]*Blocked{}

	for _, candidateID := range candidates {
		paths, err := pathsForTicket(ctx, q, candidateID)
		if err != nil {
			return err
		}

		if claimBarrier != nil {
			claimBarrier()
		}

		evaluated = append(evaluated, candidateID)
		scan := scanConflicts(candidateID, paths, held, shared, locking)
		if scan.Blocked != nil {
			blockedBy[candidateID] = scan.Blocked
			// Remember the first blockage, but keep looking: a later
			// candidate may be free. Only report it if nothing is.
			if result.Blocked == nil {
				result.Blocked = scan.Blocked
			}
			continue
		}

		granted, attempt, err := grant(ctx, q, agentID, candidateID, ttl, served)
		if err != nil {
			return err
		}
		result.Ticket = granted
		result.Attempt = attempt
		result.SharedOverlaps = scan.Shared
		result.LockingOffOverlaps = scan.LockingOff
		result.Blocked = nil
		return finishGrant(ctx, q, candidateID, evaluated, blockedBy, result)
	}
	if err := recordScopeBlocks(ctx, q, evaluated, blockedBy); err != nil {
		return err
	}
	// Nothing granted for a claim of one ticket: if the reason is a
	// role the agent lacks, say that rather than "queue empty".
	if only != nil && len(candidates) == 0 {
		missing, err := missingRole(ctx, q, agentID, *only)
		if err != nil {
			return err
		}
		if missing != nil {
			return missing
		}
	}
	return nil
}

// finishGrant reads what the granted attempt carries back onto result and
// settles the scope-block bookkeeping the grant ends.
func finishGrant(ctx context.Context, q db.Querier, ticketID int64, evaluated []int64, blockedBy map[int64]*Blocked, result *ClaimResult) error {
	if err := loadGrantedAttempt(ctx, q, result); err != nil {
		return err
	}
	if err := endScopeBlocksOf(ctx, q, ticketID, scopeBlockClaimed); err != nil {
		return err
	}
	return recordScopeBlocks(ctx, q, evaluated, blockedBy)
}

// loadGrantedAttempt fills result.Unpromoted and result.Session from the
// attempt the grant just made.
func loadGrantedAttempt(ctx context.Context, q db.Querier, result *ClaimResult) error {
	var harness, sessionID *string
	if err := q.QueryRow(ctx, `SELECT promoted_at IS NULL, harness, harness_session_id FROM attempts WHERE ticket_id = $1 AND number = $2`,
		result.Attempt.TicketID, result.Attempt.Number).Scan(&result.Unpromoted, &harness, &sessionID); err != nil {
		return err
	}
	if harness != nil && sessionID != nil {
		result.Session = &AttemptSession{Harness: *harness, SessionID: *sessionID}
	}
	return nil
}

// holdsScopeClause is true of a ticket t that holds its scope: in review,
// or with a live lease -- what activeScopes counts as held.
const holdsScopeClause = `(t.status = 'review'
	OR EXISTS (SELECT 1 FROM leases l WHERE l.ticket_id = t.id AND ` + activeLeaseClause + ` AND NOT ` + shadowLeaseClause + `))`

// shadowLeaseClause is true of a lease l that is on a shadow attempt. A
// shadow runs but never merges, so it holds no scope: scope locks exist to
// keep merges apart.
const shadowLeaseClause = `EXISTS (SELECT 1 FROM attempts sh WHERE sh.ticket_id = l.ticket_id
	AND sh.number = l.attempt_number AND sh.shadow)`

// holdingScope is which of ids hold their scope now.
func holdingScope(ctx context.Context, q db.Querier, ids []int64) (map[int64]bool, error) {
	rows, err := q.Query(ctx, `SELECT t.id FROM tickets t WHERE t.id = ANY($1) AND `+holdsScopeClause, ids)
	if err != nil {
		return nil, fmt.Errorf("check held scopes: %w", err)
	}
	holders, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, fmt.Errorf("scan held scopes: %w", err)
	}
	held := make(map[int64]bool, len(holders))
	for _, id := range holders {
		held[id] = true
	}
	return held, nil
}

// scopeConflicts reports, for each of ids that holds its scope, the first
// scope another ticket holds that its paths overlap -- in id order, and
// only the ones that conflict. A ticket holding no scope -- no live lease,
// not in review -- is skipped: it takes no files from anyone, and a claim
// checks it when it is taken.
func scopeConflicts(ctx context.Context, q db.Querier, ids []int64, shared *scope.SharedPaths) ([]Blocked, error) {
	locking, err := scopeLocking(ctx, q)
	if err != nil || !locking {
		return nil, err
	}
	holding, err := holdingScope(ctx, q, ids)
	if err != nil || len(holding) == 0 {
		return nil, err
	}
	held, err := activeScopes(ctx, q)
	if err != nil {
		return nil, err
	}
	var conflicts []Blocked
	for _, id := range slices.Sorted(maps.Keys(holding)) {
		paths, err := pathsForTicket(ctx, q, id)
		if err != nil {
			return nil, err
		}
		if blocked := firstConflict(id, paths, heldByOthers(held, id), shared, true); blocked != nil {
			conflicts = append(conflicts, *blocked)
		}
	}
	return conflicts, nil
}

// heldByOthers is held without ticketID's own scopes, which are no
// conflict with themselves: without this, every change to a ticket that
// already holds scopes would be refused by its own.
func heldByOthers(held []heldScope, ticketID int64) []heldScope {
	others := held[:0:0]
	for _, h := range held {
		if h.ticketID != ticketID {
			others = append(others, h)
		}
	}
	return others
}

// scopeLocking reads whether overlapping scopes block, inside the caller's
// transaction so a toggle cannot land between a decision and its grant.
func scopeLocking(ctx context.Context, q db.Querier) (bool, error) {
	return settings.EnabledIn(ctx, q, settings.TicketScopeLocking)
}

// ScopeLocking reads the setting outside a claim, for display and feedback.
func (s *PostgresStore) ScopeLocking(ctx context.Context) (bool, error) {
	var locking bool
	err := s.db.InReadTx(ctx, func(q db.Querier) error {
		var err error
		locking, err = scopeLocking(ctx, q)
		return err
	})
	return locking, err
}

// firstConflict reports the first held scope any of paths collides with,
// leaving out collisions wholly inside a shared path, and nil whatever
// collides when locking is off.
// A ticket with no declared scope conflicts with nothing -- it is the
// filer's statement that the work touches nothing exclusive. Neither does a
// ticket conflict with its own held scopes: sibling attempts share the
// ticket's paths, so attempt 2 must not be blocked by attempt 1's lease.
func firstConflict(candidateID int64, paths []string, held []heldScope, shared *scope.SharedPaths, locking bool) *Blocked {
	return scanConflicts(candidateID, paths, held, shared, locking).Blocked
}

// conflictScan is what scanConflicts found: the collision that blocks, if
// any, and the ones that were let through.
type conflictScan struct {
	Blocked *Blocked
	// Shared are collisions inside a shared path.
	Shared []SharedOverlap
	// LockingOff are collisions that would have blocked, with locking off.
	LockingOff []LockingOffOverlap
}

// scanConflicts is firstConflict, and the collisions it let through: those
// inside a shared path, which whoever is granted the scope will meet when
// they merge main, and, with locking off, the ones that would have blocked.
// It is the one place that decides a collision blocks, so a call site cannot
// keep blocking by forgetting the setting.
func scanConflicts(candidateID int64, paths []string, held []heldScope, shared *scope.SharedPaths, locking bool) *conflictScan {
	var scan conflictScan
	for _, p := range paths {
		for _, h := range held {
			if h.ticketID == candidateID {
				continue
			}
			conflict, resolve := scope.SharedClash(p, h.path, shared)
			switch {
			case !conflict:
			case resolve == "" && locking:
				return &conflictScan{Blocked: &Blocked{
					TicketID:            candidateID,
					ConflictingTicketID: h.ticketID,
					ConflictingPath:     h.path,
					HolderAgentID:       h.agentID,
				}}
			case resolve == "":
				o := LockingOffOverlap{TicketID: h.ticketID, Path: scope.OverlapOf(p, h.path)}
				if !slices.Contains(scan.LockingOff, o) {
					scan.LockingOff = append(scan.LockingOff, o)
				}
			default:
				o := SharedOverlap{TicketID: h.ticketID, Path: scope.OverlapOf(p, h.path), Resolve: resolve}
				if !slices.Contains(scan.Shared, o) {
					scan.Shared = append(scan.Shared, o)
				}
			}
		}
	}
	return &scan
}

// grant stamps acquired_at with the wall clock, not the column's now()
// default: now() is the claim transaction's start, which can predate a
// disable-and-re-enable that happened while the claim waited, and
// ReleaseLeasesFor bounds its release by acquired_at.
func grant(ctx context.Context, q db.Querier, agentID uuid.UUID, ticketID int64, ttl time.Duration, served *ServedConfig) (*Ticket, *AttemptRef, error) {
	attempt, err := attemptToClaim(ctx, q, agentID, ticketID)
	if err != nil {
		return nil, nil, err
	}
	granted, err := grantAttempt(ctx, q, agentID, attempt, ttl, served, AttemptStatusInProgress)
	return granted, attempt, err
}

// grantAttempt leases the attempt to agentID and sets it to held, the status the
// holder's work shows as: in_progress for a worker, and for the server landing
// an attempt in the merge queue, in_merge_queue, since the queue owns that
// ticket and a worker never holds it.
func grantAttempt(ctx context.Context, q db.Querier, agentID uuid.UUID, attempt *AttemptRef, ttl time.Duration, served *ServedConfig, held AttemptStatus) (*Ticket, error) {
	ticketID := attempt.TicketID
	var leaseID int64
	if err := q.QueryRow(ctx, `
		INSERT INTO leases (ticket_id, attempt_number, agent_id, acquired_at, last_heartbeat_at, expires_at)
		VALUES ($1, $2, $3, clock_timestamp(), clock_timestamp(), clock_timestamp() + $4::interval)
		RETURNING id`,
		ticketID, attempt.Number, agentID, ttl.String()).Scan(&leaseID); err != nil {
		return nil, fmt.Errorf("insert lease: %w", err)
	}
	if err := recordLeaseConfig(ctx, q, leaseID, served); err != nil {
		return nil, err
	}
	// A claim ends any discussion's hold on the ticket, whoever makes it:
	// the asking agent coming back, or anyone once its window has closed.
	if err := agentMover(agentID, "claimed").set(ctx, q); err != nil {
		return nil, err
	}
	// The ticket's row first, then its attempt: the lock order AttemptRef names.
	if err := q.QueryRow(ctx, `SELECT id FROM tickets WHERE id = $1 FOR UPDATE`, ticketID).Scan(new(int64)); err != nil {
		return nil, fmt.Errorf("lock ticket: %w", err)
	}
	if err := takeClaimSnapshot(ctx, q, attempt); err != nil {
		return nil, err
	}
	// started_revision records the first claim only: NULL means the attempt
	// has never started, and a re-claim leaves it.
	if err := writeAttempt(ctx, q, attempt, &setList{
		clauses: []string{
			"resume_agent_id = NULL", "affinity_until = NULL",
			"started_revision = COALESCE(pa.started_revision, (SELECT t.revision FROM tickets t WHERE t.id = pa.ticket_id))",
			"status = '" + string(held) + "'", "updated_at = now()",
		}, first: 3,
	}); err != nil {
		return nil, fmt.Errorf("end the hold on the ticket: %w", err)
	}
	// Worked out from the attempts rather than set: claiming a queued sibling
	// must not overwrite a ticket showing review or discussion, nor a parked
	// benchmark whose replay is claimed.
	if _, err := deriveStatus(ctx, q, "t.id = $1 AND "+inQueueLifecycle, ticketID); err != nil {
		return nil, err
	}
	// The ticket as the claimed attempt sees it: a sibling's approval or PR
	// is not this attempt's to be told of.
	conv, err := attemptConversation(ctx, q, attempt)
	if err != nil {
		return nil, err
	}
	return ticketSeenBy(ctx, q, ticketID, conv)
}

// Peek answers "is there work this agent could take" without taking it.
//
// The Stop hook needs exactly this. Using Claim would mean a hook endpoint
// assigning work -- handing a ticket to an agent the human may be about to
// ctrl-C, and putting work assignment behind a door that is not the agent's
// own tool call. So this runs the same dependency and conflict evaluation
// and then grants nothing.
//
// It deliberately does not run the expiry sweep either, since that mutates
// lease state. candidateTickets' includeStale covers what the sweep would
// have freed.
func (s *PostgresStore) Peek(ctx context.Context, agentID uuid.UUID, only *int64) (*ClaimResult, error) {
	var result ClaimResult
	shared, err := s.sharedPaths(ctx)
	if err != nil {
		return nil, err
	}

	err = s.db.InTx(ctx, func(q db.Querier) error {
		held, err := activeScopes(ctx, q)
		if err != nil {
			return err
		}
		locking, err := scopeLocking(ctx, q)
		if err != nil {
			return err
		}
		// Scopes held by this agent's own lease are not an obstacle to it.
		// Those of a ticket it has in review are: Claim would refuse them.
		mine := held[:0:0]
		for _, h := range held {
			if h.agentID != agentID || h.inReview {
				mine = append(mine, h)
			}
		}

		candidates, err := candidateTickets(ctx, q, agentID, only, true)
		if err != nil {
			return err
		}
		for _, candidateID := range candidates {
			paths, err := pathsForTicket(ctx, q, candidateID)
			if err != nil {
				return err
			}
			if blocked := firstConflict(candidateID, paths, mine, shared, locking); blocked != nil {
				if result.Blocked == nil {
					result.Blocked = blocked
				}
				continue
			}
			result.Ticket, err = loadTicket(ctx, q, candidateID)
			if err != nil {
				return err
			}
			result.Blocked = nil
			return nil
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// heldBy is the paths held under ticketID.
func heldBy(held []heldScope, ticketID int64) []string {
	var paths []string
	for _, h := range held {
		if h.ticketID == ticketID {
			paths = append(paths, h.path)
		}
	}
	return paths
}
