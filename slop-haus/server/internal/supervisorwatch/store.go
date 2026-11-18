package supervisorwatch

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Facts are what detection reads from one tenant.
type Facts struct {
	// Claimable is the tickets nothing stops an agent taking: open ones not
	// held back by a blocked_until date or an unfinished dependency, and
	// in-progress ones whose lease has lapsed (an abandoned ticket is
	// exactly what a dead fleet leaves behind). It is
	// tenant-wide, so it ignores what only a particular agent or the scopes
	// already held can rule out; the alert's other conditions absorb that.
	Claimable int64
	// LastPolledAt is when any supervisor last polled; nil if none ever has.
	LastPolledAt *time.Time
	// LastClaimedAt is when any agent last took a ticket; nil if none ever has.
	LastClaimedAt *time.Time
}

type PostgresStore struct {
	db *db.DB
}

func NewPostgresStore(database *db.DB) *PostgresStore { return &PostgresStore{db: database} }

// Facts reads the context's tenant.
func (s *PostgresStore) Facts(ctx context.Context) (*Facts, error) {
	var f Facts
	err := s.db.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM tickets t
		         WHERE (t.status = `+ticket.SQLStatusOpen+` OR (t.status = `+ticket.SQLStatusInProgress+` AND NOT EXISTS (
		                    SELECT 1 FROM leases l
		                     WHERE l.ticket_id = t.id AND l.released_at IS NULL AND l.expires_at > now())))
		           AND (t.blocked_until IS NULL OR t.blocked_until <= now())
		           AND NOT EXISTS (
		               SELECT 1 FROM ticket_deps d
		                 JOIN tickets dep ON dep.id = d.depends_on_ticket_id
		                WHERE d.ticket_id = t.id AND dep.status NOT IN `+ticket.TerminalStatusesSQL+`)),
		       (SELECT max(last_polled_at) FROM worker_polls),
		       (SELECT max(acquired_at) FROM leases)`).
		Scan(&f.Claimable, &f.LastPolledAt, &f.LastClaimedAt)
	if err != nil {
		return nil, fmt.Errorf("read supervisor facts: %w", err)
	}
	return &f, nil
}

// Tenants reads every tenant through the global handle; a scrape has no
// request to inherit one from.
func (s *PostgresStore) Tenants(ctx context.Context) ([]uuid.UUID, error) {
	return tenantstate.ActiveIDs(ctx, s.db.Global())
}

// ReportSlot stores the agent's latest report. phase_entered_at moves only
// when the phase or the ticket does, so a slot that repeats the same state
// keeps its age however often it reports. clock_timestamp() with GREATEST
// keeps reported_at from moving backwards when reports overlap (see
// wakeup's RecordPoll).
func (s *PostgresStore) ReportSlot(ctx context.Context, agentID uuid.UUID, r *SlotReport) error {
	if _, err := s.db.Exec(ctx, `
		INSERT INTO supervisor_slot_states AS st
		       (agent_id, phase, phase_entered_at, ticket_id, error_class, error_streak, pause_reason, reason, reason_detail, bundle_hash, idle, reported_at)
		VALUES ($1, $2, clock_timestamp() - make_interval(secs => $3), $4, $5, $6, $7, $8, $9, $10, $11, clock_timestamp())
		ON CONFLICT (tenant_id, agent_id) DO UPDATE
		SET phase_entered_at = CASE
		        WHEN st.phase = EXCLUDED.phase AND st.ticket_id IS NOT DISTINCT FROM EXCLUDED.ticket_id
		        THEN st.phase_entered_at ELSE EXCLUDED.phase_entered_at END,
		    phase = EXCLUDED.phase,
		    ticket_id = EXCLUDED.ticket_id,
		    error_class = EXCLUDED.error_class,
		    error_streak = EXCLUDED.error_streak,
		    pause_reason = EXCLUDED.pause_reason,
		    reason = EXCLUDED.reason,
		    reason_detail = EXCLUDED.reason_detail,
		    bundle_hash = EXCLUDED.bundle_hash,
		    idle = EXCLUDED.idle,
		    reported_at = GREATEST(st.reported_at, EXCLUDED.reported_at)`,
		agentID, string(r.Phase), r.PhaseAge.Seconds(), r.TicketID, r.ErrorClass, r.ErrorStreak, r.PauseReason, r.Reason, r.ReasonDetail, r.BundleHash, r.Idle); err != nil {
		return fmt.Errorf("record slot state: %w", err)
	}
	return nil
}

// SupervisorRow is a supervisor agent's last poll.
type SupervisorRow struct {
	AgentID      uuid.UUID
	AgentName    string
	Version      string
	Protocol     int
	LastPolledAt time.Time
}

// Supervisors reads the last poll of each supervisor agent that is neither
// disabled nor archived.
func (s *PostgresStore) Supervisors(ctx context.Context) ([]*SupervisorRow, error) {
	rows, err := s.db.Query(ctx, `
		SELECT a.id, a.name, p.version, p.protocol, p.last_polled_at
		  FROM worker_polls p
		  JOIN agents a ON a.id = p.agent_id
		 WHERE a.kind = 'supervisor' AND a.disabled_at IS NULL AND a.archived_at IS NULL
		 ORDER BY a.name, a.id`)
	if err != nil {
		return nil, fmt.Errorf("read supervisor polls: %w", err)
	}
	defer rows.Close()
	var out []*SupervisorRow
	for rows.Next() {
		var r SupervisorRow
		if err := rows.Scan(&r.AgentID, &r.AgentName, &r.Version, &r.Protocol, &r.LastPolledAt); err != nil {
			return nil, fmt.Errorf("scan supervisor poll: %w", err)
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// Slots reads the context tenant's slots of agents that are neither disabled
// nor archived. A lease held alive by the integration lock counts as having
// just had a heartbeat: it is expected to outlive its expiry.
func (s *PostgresStore) Slots(ctx context.Context) ([]*SlotRow, error) {
	rows, err := s.db.Query(ctx, `
		SELECT st.agent_id, a.name, a.supervisor_agent_id, st.phase, st.phase_entered_at, st.ticket_id, st.error_class, st.error_streak, st.pause_reason, st.reason, st.reason_detail, st.bundle_hash, st.idle, st.reported_at,
		       CASE WHEN l.id IS NULL THEN NULL
		            WHEN EXISTS (SELECT 1 FROM locks lk WHERE lk.holder_lease_id = l.id) THEN clock_timestamp()
		            ELSE l.expires_at - make_interval(secs => $1) END
		  FROM supervisor_slot_states st
		  JOIN agents a ON a.id = st.agent_id
		  LEFT JOIN leases l ON l.agent_id = st.agent_id AND `+ticket.ActiveLeaseClause+`
		 WHERE a.disabled_at IS NULL AND a.archived_at IS NULL
		 ORDER BY st.agent_id`, ticket.DefaultLeaseTTL.Seconds())
	if err != nil {
		return nil, fmt.Errorf("read slot states: %w", err)
	}
	defer rows.Close()
	var out []*SlotRow
	for rows.Next() {
		var r SlotRow
		var phase string
		if err := rows.Scan(&r.AgentID, &r.AgentName, &r.SupervisorID, &phase, &r.PhaseEnteredAt, &r.TicketID, &r.ErrorClass, &r.ErrorStreak, &r.PauseReason, &r.Reason, &r.ReasonDetail, &r.BundleHash, &r.Idle, &r.ReportedAt, &r.LeaseHeartbeatAt); err != nil {
			return nil, fmt.Errorf("scan slot state: %w", err)
		}
		r.Phase = Phase(phase)
		out = append(out, &r)
	}
	return out, rows.Err()
}
