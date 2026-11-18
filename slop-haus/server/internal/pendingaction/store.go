package pendingaction

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

type PostgresStore struct {
	db *db.DB
}

func NewPostgresStore(database *db.DB) *PostgresStore {
	return &PostgresStore{db: database}
}

var _ Store = (*PostgresStore)(nil)

// effectiveState reads a pending proposal past its expiry as expired, so
// that nothing depends on the sweep having run. nowArg is the placeholder
// holding the time.
func effectiveState(nowArg string) string {
	return `(CASE WHEN status = 'pending' AND expires_at <= ` + nowArg + ` THEN 'expired' ELSE status END)`
}

// actionColumns is every column scanAction reads, with the state effective
// at the time in $1... placeholder named by nowArg.
func actionColumns(nowArg string) string {
	return `id, ticket_id, attempt_number, agent_id, kind, payload, base_status, base_revision, base_pr_url, base_review_started_at,
		` + effectiveState(nowArg) + `, outcome, created_at, expires_at, decided_at, decided_by_user_id, decided_by_user_id_deleted`
}

func scanAction(row pgx.Row) (*Action, error) {
	var a Action
	if err := row.Scan(&a.ID, &a.TicketID, &a.Attempt, &a.AgentID, &a.Kind, &a.Payload, &a.Basis.Status, &a.Basis.Revision,
		&a.Basis.PRURL, &a.Basis.ReviewStartedAt, &a.State, &a.Outcome, &a.CreatedAt, &a.ExpiresAt,
		&a.DecidedAt, &a.DecidedByUserID, &a.DecidedByDeleted); err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *PostgresStore) Propose(ctx context.Context, in *NewAction, check func(*Basis) error) (*Action, error) {
	var created *Action
	err := s.db.InTx(ctx, func(q db.Querier) error {
		// The agent's row is the lock that serialises one agent's proposals,
		// so its limits cannot be raced past; the ticket's is the one that
		// serialises proposals about the ticket, so the supersede below
		// cannot meet the unique index. Always taken in this order.
		var proposer, enabled bool
		err := q.QueryRow(ctx, `SELECT (kind IN ('console', 'conductor', 'steward')), disabled_at IS NULL FROM agents WHERE id = $1 FOR NO KEY UPDATE`,
			in.AgentID).Scan(&proposer, &enabled)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !(proposer && enabled)) {
			return ErrNotConsole
		}
		if err != nil {
			return fmt.Errorf("pending action: read proposer: %w", err)
		}

		// A proposal about the tenant has no ticket to lock, read or be held,
		// and so no basis to go stale against.
		basis := &Basis{}
		var attempt *int
		if in.TicketID != nil {
			var promoted *int
			err = q.QueryRow(ctx, `
				SELECT pa.number FROM `+ticket.TicketFrom+` WHERE t.id = $1 FOR NO KEY UPDATE OF t`,
				*in.TicketID).Scan(&promoted)
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: #%d", ErrTicketNotFound, *in.TicketID)
			}
			if err != nil {
				return fmt.Errorf("pending action: read ticket: %w", err)
			}

			// An answer or a decision is about the work, so about the attempt named,
			// or else the ticket's one live attempt or its promoted one; the CHECK
			// has the other kinds name none.
			if in.Kind.aboutAnAttempt() {
				named, err := ticket.ResolveAttempt(ctx, q, *in.TicketID, in.Attempt)
				if err != nil {
					return fmt.Errorf("pending action: %w", err)
				}
				switch {
				case named != nil:
					attempt = &named.Number
				case promoted == nil:
					return fmt.Errorf("pending action: #%d: %w", *in.TicketID, ticket.ErrNoPromotedAttempt)
				default:
					attempt = promoted
				}
			} else if in.Attempt != nil {
				return fmt.Errorf("%w: a %s proposal is about the ticket, not an attempt", ErrInvalid, in.Kind)
			}

			var held bool
			basis, held, err = readBasis(ctx, q, *in.TicketID, attempt)
			if err != nil {
				return err
			}
			if held {
				return fmt.Errorf("%w: #%d", ErrTicketHeld, *in.TicketID)
			}
			if err := check(basis); err != nil {
				return err
			}
		} else {
			if in.Attempt != nil {
				return fmt.Errorf("%w: a %s proposal is about the tenant, not an attempt", ErrInvalid, in.Kind)
			}
			// With no ticket row to lock, proposals of one kind are serialised
			// on the tenant and kind, so the supersede below cannot meet the
			// unique index when two agents propose at once.
			if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_tenant()::text || ':' || $1::text, 0))`,
				in.Kind); err != nil {
				return fmt.Errorf("pending action: lock kind: %w", err)
			}
		}

		// An expired row the sweep has not reached yet still holds the unique
		// index, so it is closed as expired rather than as superseded. Only the
		// same attempt's: the index is per attempt, and a proposal about one
		// attempt must not withdraw a sibling's.
		if _, err := q.Exec(ctx, `
			UPDATE pending_actions
			   SET status = CASE WHEN expires_at <= $3 THEN 'expired' ELSE 'withdrawn' END,
			       outcome = CASE WHEN expires_at <= $3 THEN 'expired' ELSE 'superseded by a newer proposal' END,
			       decided_at = $3
			 WHERE ticket_id IS NOT DISTINCT FROM $1 AND kind = $2 AND status = 'pending'
			   AND attempt_number IS NOT DISTINCT FROM $4`,
			in.TicketID, in.Kind, in.CreatedAt, attempt); err != nil {
			return fmt.Errorf("pending action: supersede: %w", err)
		}

		var recent, pending int
		if err := q.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE created_at > $2),
			       count(*) FILTER (WHERE status = 'pending' AND expires_at > $3)
			  FROM pending_actions WHERE agent_id = $1`,
			in.AgentID, in.CreatedAt.Add(-rateWindow), in.CreatedAt).Scan(&recent, &pending); err != nil {
			return fmt.Errorf("pending action: count: %w", err)
		}
		if recent >= in.RatePerMinute {
			return ErrRateLimited
		}
		if pending >= in.MaxPendingPerAgent {
			return fmt.Errorf("%w: %d", ErrTooManyPending, in.MaxPendingPerAgent)
		}

		created, err = scanAction(q.QueryRow(ctx, `
			INSERT INTO pending_actions
			    (id, ticket_id, attempt_number, agent_id, kind, payload, base_status, base_revision, base_pr_url,
			     base_review_started_at, created_at, expires_at)
			VALUES ($1, $2, $12, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			RETURNING `+actionColumns("$10"),
			in.ID, in.TicketID, in.AgentID, in.Kind, in.Payload, basis.Status, basis.Revision, basis.PRURL,
			basis.ReviewStartedAt, in.CreatedAt, in.ExpiresAt, attempt))
		if err != nil {
			return fmt.Errorf("pending action: insert: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

func (s *PostgresStore) Get(ctx context.Context, id uuid.UUID, now time.Time) (*Action, error) {
	found, err := scanAction(s.db.QueryRow(ctx,
		`SELECT `+actionColumns("$2")+` FROM pending_actions WHERE id = $1`, id, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("pending action: get: %w", err)
	}
	return found, nil
}

func (s *PostgresStore) List(ctx context.Context, filter *ListFilter, now time.Time) ([]Action, error) {
	limit := filter.Limit
	if limit == 0 {
		limit = DefaultListLimit
	}
	states := make([]string, len(filter.States))
	for i, st := range filter.States {
		states[i] = string(st)
	}
	args := []any{now, filter.AgentID, filter.TicketID, states, limit, filter.Kind, filter.IDs}
	// Keyset: strictly after the last proposal of the previous page, in the
	// order listed. Ties on created_at fall to the id, ascending either way.
	afterCond := "TRUE"
	order := "created_at DESC, id"
	if filter.Oldest {
		order = "created_at, id"
	}
	if filter.After != nil {
		beyond := "<"
		if filter.Oldest {
			beyond = ">"
		}
		args = append(args, filter.After.CreatedAt, filter.After.ID)
		afterCond = fmt.Sprintf("(created_at %s $%d OR (created_at = $%d AND id > $%d))", beyond, len(args)-1, len(args)-1, len(args))
	}
	// The ticket filter is the ticket lists' own predicate, so a proposal is
	// filtered by its ticket exactly as the ticket would be.
	ticketCond := "TRUE"
	if filter.Ticket != nil {
		where, ticketArgs := filter.Ticket.Predicate(len(args))
		ticketCond = `EXISTS (SELECT 1 FROM tickets t WHERE t.tenant_id = pending_actions.tenant_id
		                        AND t.id = pending_actions.ticket_id AND ` + where + `)`
		args = append(args, ticketArgs...)
	}
	rows, err := s.db.Query(ctx, `
		SELECT `+actionColumns("$1")+` FROM pending_actions
		 WHERE ($2::uuid IS NULL OR agent_id = $2)
		   AND ($3::bigint IS NULL OR ticket_id = $3)
		   AND (cardinality($4::text[]) = 0 OR `+effectiveState("$1")+` = ANY($4))
		   AND ($6::text IS NULL OR kind = $6)
		   AND ($7::uuid[] IS NULL OR id = ANY($7))
		   AND `+afterCond+`
		   AND `+ticketCond+`
		 ORDER BY `+order+`
		 LIMIT $5`, args...)
	if err != nil {
		return nil, fmt.Errorf("pending action: list: %w", err)
	}
	defer rows.Close()

	actions := []Action{}
	for rows.Next() {
		a, err := scanAction(rows)
		if err != nil {
			return nil, fmt.Errorf("pending action: scan: %w", err)
		}
		actions = append(actions, *a)
	}
	return actions, rows.Err()
}

func (s *PostgresStore) Withdraw(ctx context.Context, agentID, id uuid.UUID, now time.Time) (*Action, error) {
	var withdrawn *Action
	err := s.db.InTx(ctx, func(q db.Querier) error {
		var err error
		withdrawn, err = scanAction(q.QueryRow(ctx, `
			UPDATE pending_actions
			   SET status = 'withdrawn', outcome = 'withdrawn by its proposer', decided_at = $3
			 WHERE id = $2 AND agent_id = $1 AND status = 'pending' AND expires_at > $3
			RETURNING `+actionColumns("$3"), agentID, id, now))
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// Another agent's proposal reads as not found, not as not pending:
		// whether it exists is none of this agent's business.
		var owner uuid.UUID
		err = q.QueryRow(ctx, `SELECT agent_id FROM pending_actions WHERE id = $1`, id).Scan(&owner)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && owner != agentID) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return ErrNotPending
	})
	if err != nil {
		return nil, fmt.Errorf("pending action: withdraw: %w", err)
	}
	return withdrawn, nil
}

func (s *PostgresStore) VoidAgent(ctx context.Context, agentID uuid.UUID, reason string, now time.Time) (int64, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE pending_actions SET status = 'voided', outcome = $2, decided_at = $3
		 WHERE agent_id = $1 AND status = 'pending' AND expires_at > $3`, agentID, reason, now)
	if err != nil {
		return 0, fmt.Errorf("pending action: void: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ExpireDue records the expiry of every pending proposal past it, in the
// context's tenant. Reads already treat them as expired; this is what frees
// the one-pending-per-ticket slot and lets retention start counting.
func (s *PostgresStore) ExpireDue(ctx context.Context, now time.Time) (int64, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE pending_actions SET status = 'expired', outcome = 'expired', decided_at = $1
		 WHERE status = 'pending' AND expires_at <= $1`, now)
	if err != nil {
		return 0, fmt.Errorf("pending action: expire: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *PostgresStore) LockForDecision(ctx context.Context, q db.Querier, id uuid.UUID, now time.Time) (*Action, error) {
	// The proposer's row, then the ticket's, found through the proposal's
	// unlocked read: Propose's order. Sharing the proposer's row holds off a
	// concurrent disable or console change until this decision commits, so
	// the standing read afterwards is the one the decision commits under.
	var ticketID *int64
	var agentID uuid.UUID
	err := q.QueryRow(ctx, `SELECT ticket_id, agent_id FROM pending_actions WHERE id = $1`, id).Scan(&ticketID, &agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("pending action: read: %w", err)
	}
	if _, err := q.Exec(ctx, `SELECT 1 FROM agents WHERE id = $1 FOR SHARE`, agentID); err != nil {
		return nil, fmt.Errorf("pending action: lock proposer: %w", err)
	}
	if ticketID != nil {
		if _, err := q.Exec(ctx, `SELECT 1 FROM tickets WHERE id = $1 FOR NO KEY UPDATE`, *ticketID); err != nil {
			return nil, fmt.Errorf("pending action: lock ticket: %w", err)
		}
	}
	a, err := scanAction(q.QueryRow(ctx,
		`SELECT `+actionColumns("$2")+` FROM pending_actions WHERE id = $1 FOR UPDATE`, id, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("pending action: lock: %w", err)
	}
	return a, nil
}

func (s *PostgresStore) Standing(ctx context.Context, q db.Querier, agentID uuid.UUID) (*Standing, error) {
	var st Standing
	err := q.QueryRow(ctx, `SELECT name, (kind IN ('console', 'conductor', 'steward')), disabled_at IS NULL FROM agents WHERE id = $1`, agentID).
		Scan(&st.Name, &st.Proposer, &st.Enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return &Standing{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pending action: read proposer: %w", err)
	}
	return &st, nil
}

func (s *PostgresStore) LiveBasis(ctx context.Context, q db.Querier, ticketID int64, attempt *int) (*Basis, bool, error) {
	return readBasis(ctx, q, ticketID, attempt)
}

// basisStatus is the status a proposal about attempt pa of ticket t is judged
// on. A proposal about the ticket is judged on the ticket's. About an attempt,
// with one live attempt that is the ticket's, which is the attempt's. With
// several, the ticket's shows whichever waits on a person first, so it is
// the attempt's own when that is a discussion or review, and open when the
// ticket's discussion or review is a sibling's and this attempt is queued or
// running.
func basisStatus(attempt string) string {
	return `CASE WHEN ` + attempt + ` IS NOT NULL AND ` + severalLive + ` AND pa.status IN ` + ticket.WaitingOnPersonStatusesSQL + ` THEN pa.status
	           WHEN ` + attempt + ` IS NOT NULL AND ` + severalLive + ` AND t.status IN ` + ticket.WaitingOnPersonStatusesSQL + ` THEN 'open'
	           ELSE t.status END`
}

var severalLive = `(SELECT count(*) FROM attempts la WHERE la.tenant_id = t.tenant_id AND la.ticket_id = t.id
	                                 AND la.status NOT IN ` + ticket.AttemptEndedStatusesSQL + `) > 1`

// attemptNamed is the join of the attempt a proposal is about, as pa, for a
// proposal naming attempt $2 or, with none ($2 NULL), the ticket's promoted
// attempt: what every other ticket-level read shows (ticket.AttemptJoin).
const attemptNamed = `LEFT JOIN attempts pa ON pa.tenant_id = t.tenant_id AND pa.ticket_id = t.id
	        AND (pa.number = $2 OR ($2::integer IS NULL AND pa.promoted_at IS NOT NULL))`

// readBasis is the ticket as a proposal about attempt (nil: the ticket) sees it
// now, and whether an agent holds what it is about: the attempt's lease, or
// any lease on the ticket for a proposal about the ticket.
func readBasis(ctx context.Context, q db.Querier, ticketID int64, attempt *int) (*Basis, bool, error) {
	var b Basis
	var held bool
	err := q.QueryRow(ctx, `
		SELECT `+basisStatus(`$2::integer`)+`, t.revision, pa.pr_url, pa.review_started_at,
		       EXISTS (SELECT 1 FROM leases l WHERE l.ticket_id = t.id AND ($2::integer IS NULL OR l.attempt_number = $2)
		                  AND `+ticket.ActiveLeaseClause+`)
		  FROM tickets t `+attemptNamed+` WHERE t.id = $1`, ticketID, attempt).Scan(&b.Status, &b.Revision, &b.PRURL, &b.ReviewStartedAt, &held)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("%w: #%d", ErrTicketNotFound, ticketID)
	}
	if err != nil {
		return nil, false, fmt.Errorf("pending action: read ticket: %w", err)
	}
	return &b, held, nil
}

func (s *PostgresStore) HasPendingEdit(ctx context.Context, q db.Querier, ticketID int64, now time.Time) (bool, error) {
	var pending bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM pending_actions
		                WHERE ticket_id = $1 AND kind = 'edit' AND status = 'pending' AND expires_at > $2)`,
		ticketID, now).Scan(&pending)
	if err != nil {
		return false, fmt.Errorf("pending action: look for a pending edit: %w", err)
	}
	return pending, nil
}

func (s *PostgresStore) Settle(ctx context.Context, q db.Querier, id uuid.UUID, state State, outcome string, by uuid.UUID, now time.Time) (*Action, error) {
	settled, err := scanAction(q.QueryRow(ctx, `
		UPDATE pending_actions SET status = $2, outcome = $3, decided_by_user_id = $4, decided_at = $5
		 WHERE id = $1 AND status = 'pending' AND expires_at > $5
		RETURNING `+actionColumns("$5"), id, state, outcome, by, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotPending
	}
	if err != nil {
		return nil, fmt.Errorf("pending action: settle: %w", err)
	}
	return settled, nil
}

func (s *PostgresStore) SettleOwn(ctx context.Context, id uuid.UUID, state State, outcome string, by uuid.UUID, now time.Time) (*Action, error) {
	var settled *Action
	err := s.db.InTx(ctx, func(q db.Querier) error {
		var err error
		settled, err = s.Settle(ctx, q, id, state, outcome, by, now)
		return err
	})
	return settled, err
}

func (s *PostgresStore) Reject(ctx context.Context, ids []uuid.UUID, kinds []Kind, by uuid.UUID, reason string, now time.Time) ([]Action, error) {
	rows, err := s.db.Query(ctx, `
		UPDATE pending_actions SET status = 'rejected', outcome = $3, decided_by_user_id = $2, decided_at = $4
		 WHERE status = 'pending' AND expires_at > $4 AND ($1::uuid[] IS NULL OR id = ANY($1))
		   AND ($5::text[] IS NULL OR kind = ANY($5))
		RETURNING `+actionColumns("$4"), ids, by, reason, now, kinds)
	if err != nil {
		return nil, fmt.Errorf("pending action: reject: %w", err)
	}
	defer rows.Close()
	rejected := []Action{}
	for rows.Next() {
		a, err := scanAction(rows)
		if err != nil {
			return nil, fmt.Errorf("pending action: scan: %w", err)
		}
		rejected = append(rejected, *a)
	}
	return rejected, rows.Err()
}

func (s *PostgresStore) Describe(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]*Describing, error) {
	rows, err := s.db.Query(ctx, `
		SELECT p.id, a.name, COALESCE(t.title, ''), COALESCE(t.status, ''), COALESCE(`+basisStatus(`p.attempt_number`)+`, ''), COALESCE(t.revision, 0), pa.pr_url, pa.review_started_at,
		       EXISTS (SELECT 1 FROM leases l WHERE l.ticket_id = t.id
		                  AND (p.attempt_number IS NULL OR l.attempt_number = p.attempt_number) AND `+ticket.ActiveLeaseClause+`)
		  FROM pending_actions p
		  JOIN agents a ON a.tenant_id = p.tenant_id AND a.id = p.agent_id
		  LEFT JOIN tickets t ON t.tenant_id = p.tenant_id AND t.id = p.ticket_id
		  LEFT JOIN attempts pa ON pa.tenant_id = t.tenant_id AND pa.ticket_id = t.id
		        AND (pa.number = p.attempt_number OR (p.attempt_number IS NULL AND pa.promoted_at IS NOT NULL))
		 WHERE p.id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("pending action: describe: %w", err)
	}
	defer rows.Close()
	out := make(map[uuid.UUID]*Describing, len(ids))
	for rows.Next() {
		var id uuid.UUID
		var d Describing
		if err := rows.Scan(&id, &d.AgentName, &d.TicketTitle, &d.TicketStatus, &d.Live.Status, &d.Live.Revision, &d.Live.PRURL,
			&d.Live.ReviewStartedAt, &d.Held); err != nil {
			return nil, fmt.Errorf("pending action: scan: %w", err)
		}
		out[id] = &d
	}
	return out, rows.Err()
}
