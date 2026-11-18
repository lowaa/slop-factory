package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lowaa/slop-factory/go-db/pgerr"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// ProvisionStore keeps the desired-state documents.
type ProvisionStore interface {
	// Load is the supervisor's document: its entries that are not retired.
	// It first closes the removed slot entries whose slot has been archived
	// past the grace, so the document can omit an entry that was open a moment ago.
	// A supervisor nobody wrote for has revision 0 and no entries.
	Load(ctx context.Context, supervisorID uuid.UUID) (*DesiredState, error)
	// Update runs edit on the live entries under a lock on the document and
	// stores the rows it returns (new ones, or changed ones with their new
	// generation), bumping the revision, all in one transaction.
	// ErrStaleRevision when baseRevision is not current; an error from edit
	// is returned as is and nothing is written; ErrDesiredNameHeld when a
	// new entry's name is still held.
	Update(ctx context.Context, supervisorID uuid.UUID, baseRevision int64, edit func(current []DesiredAgent) ([]DesiredAgent, error)) (*DesiredState, error)
	// Observe records the supervisor's reports. A report on the tenant's live
	// steward is stored by agent alone, whatever its generation. A report older than the last
	// one for an entry is ignored; one naming an entry that is not the
	// supervisor's, a generation the entry has not reached, or retired for
	// an entry that is not removed at that generation is ErrDesiredInvalid,
	// and none of the batch is stored.
	//
	// retire is called, under a lock on the entry, before a retired report
	// frees its name; if it fails nothing of the batch is stored. The lock is
	// what stops a delayed report from retiring an agent that has since
	// taken the name.
	Observe(ctx context.Context, supervisorID uuid.UUID, reports []ObservedReport, retire RetireFunc) error
	// RetireRemoved calls retire, under a lock on each entry, for those of
	// ids that are removed and not yet reported retired.
	RetireRemoved(ctx context.Context, supervisorID uuid.UUID, ids []uuid.UUID, retire RetireFunc) error
	// StewardReports are the stored steward reports, by agent id.
	StewardReports(ctx context.Context) (map[uuid.UUID]ObservedSteward, error)
}

// RetireFunc ends the real agent behind a removed entry. It must be idempotent.
type RetireFunc func(ctx context.Context, entry *DesiredAgent) error

// PostgresProvisionStore runs every query in the context's tenant.
type PostgresProvisionStore struct {
	db *db.DB
}

func NewPostgresProvisionStore(database *db.DB) *PostgresProvisionStore {
	return &PostgresProvisionStore{db: database}
}

var _ ProvisionStore = (*PostgresProvisionStore)(nil)

const desiredColumns = `id, kind, name, harness, state, launch, generation, resume_generation,
	observed_generation, observed_phase, failure_reason, observed_harness`

func scanDesired(row pgx.Row) (DesiredAgent, error) {
	var a DesiredAgent
	var kind, state, launch, phase string
	err := row.Scan(&a.ID, &kind, &a.Name, &a.Harness, &state, &launch, &a.Generation, &a.ResumeGeneration,
		&a.Observed.Generation, &phase, &a.Observed.Reason, &a.Observed.Harness)
	a.Kind, a.State, a.Launch, a.Observed.Phase = ProvisionKind(kind), ProvisionState(state), ProvisionLaunch(launch), Phase(phase)
	return a, err
}

func scanDesiredRetired(row pgx.Row, retired *bool) (DesiredAgent, error) {
	var a DesiredAgent
	var kind, state, launch, phase string
	err := row.Scan(&a.ID, &kind, &a.Name, &a.Harness, &state, &launch, &a.Generation, &a.ResumeGeneration,
		&a.Observed.Generation, &phase, &a.Observed.Reason, &a.Observed.Harness, retired)
	a.Kind, a.State, a.Launch, a.Observed.Phase = ProvisionKind(kind), ProvisionState(state), ProvisionLaunch(launch), Phase(phase)
	return a, err
}

func loadDesired(ctx context.Context, q db.Querier, supervisorID uuid.UUID, lock bool) (*DesiredState, error) {
	state := &DesiredState{Agents: []DesiredAgent{}}
	err := q.QueryRow(ctx, `SELECT revision FROM supervisor_desired_state WHERE supervisor_id = $1`, supervisorID).Scan(&state.Revision)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("agent: desired state: %w", err)
	}
	query := `SELECT ` + desiredColumns + ` FROM supervisor_desired_agents
		WHERE supervisor_id = $1 AND retired_at IS NULL ORDER BY created_at, id`
	if lock {
		query += ` FOR UPDATE`
	}
	rows, err := q.Query(ctx, query, supervisorID)
	if err != nil {
		return nil, fmt.Errorf("agent: desired agents: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		a, err := scanDesired(rows)
		if err != nil {
			return nil, fmt.Errorf("agent: scan desired agent: %w", err)
		}
		state.Agents = append(state.Agents, a)
	}
	return state, rows.Err()
}

// archivedSlotGrace is how long a removed slot's entry waits for its supervisor
// to report it retired after the slot was archived. The supervisor tears the
// checkout down after the archive, and the entry holds the name until it has;
// one still open after the grace belongs to a supervisor that went away.
const archivedSlotGrace = 10 * time.Minute

// closeArchivedSlots retires the removed slot entries of a supervisor whose
// slot has been archived on the server for longer than the grace. A supervisor
// whose machine went down before its final report never closes them itself,
// and the board would show them as retiring forever.
func closeArchivedSlots(ctx context.Context, q db.Querier, supervisorID uuid.UUID) error {
	if _, err := q.Exec(ctx, `
		UPDATE supervisor_desired_agents d
		   SET observed_generation = d.generation, observed_phase = 'retired', failure_reason = '',
		       observed_harness = '', retired_at = now()
		  FROM agents a
		 WHERE d.supervisor_id = $1 AND d.kind = 'slot' AND d.state = 'removed' AND d.retired_at IS NULL
		   AND a.tenant_id = d.tenant_id AND a.name = d.name AND a.supervisor_agent_id = d.supervisor_id
		   AND a.archived_at < now() - make_interval(secs => $2)`,
		supervisorID, archivedSlotGrace.Seconds()); err != nil {
		return fmt.Errorf("agent: desired agents: close archived slots: %w", err)
	}
	return nil
}

func (s *PostgresProvisionStore) Load(ctx context.Context, supervisorID uuid.UUID) (*DesiredState, error) {
	var state *DesiredState
	// A write transaction: reading is when the entries of a gone supervisor are swept.
	err := s.db.InTx(ctx, func(q db.Querier) (err error) {
		if err := closeArchivedSlots(ctx, q, supervisorID); err != nil {
			return err
		}
		state, err = loadDesired(ctx, q, supervisorID, false)
		return err
	})
	return state, err
}

// lockDocument creates the supervisor's header row if there is none and locks
// it, which serialises every writer of the document.
func lockDocument(ctx context.Context, q db.Querier, supervisorID uuid.UUID) (int64, error) {
	if _, err := q.Exec(ctx, `INSERT INTO supervisor_desired_state (supervisor_id) VALUES ($1) ON CONFLICT DO NOTHING`, supervisorID); err != nil {
		return 0, fmt.Errorf("agent: desired state: create: %w", err)
	}
	var revision int64
	if err := q.QueryRow(ctx, `SELECT revision FROM supervisor_desired_state WHERE supervisor_id = $1 FOR UPDATE`, supervisorID).Scan(&revision); err != nil {
		return 0, fmt.Errorf("agent: desired state: lock: %w", err)
	}
	return revision, nil
}

func (s *PostgresProvisionStore) Update(ctx context.Context, supervisorID uuid.UUID, baseRevision int64, edit func(current []DesiredAgent) ([]DesiredAgent, error)) (*DesiredState, error) {
	var out *DesiredState
	err := s.db.InTx(ctx, func(q db.Querier) error {
		revision, err := lockDocument(ctx, q, supervisorID)
		if err != nil {
			return err
		}
		if revision != baseRevision {
			return ErrStaleRevision
		}
		current, err := loadDesired(ctx, q, supervisorID, true)
		if err != nil {
			return err
		}
		changed, err := edit(current.Agents)
		if err != nil {
			return err
		}
		for i := range changed {
			if err := upsertDesired(ctx, q, supervisorID, &changed[i]); err != nil {
				return err
			}
		}
		if _, err := q.Exec(ctx, `UPDATE supervisor_desired_state SET revision = revision + 1 WHERE supervisor_id = $1`, supervisorID); err != nil {
			return fmt.Errorf("agent: desired state: bump: %w", err)
		}
		out, err = loadDesired(ctx, q, supervisorID, false)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func upsertDesired(ctx context.Context, q db.Querier, supervisorID uuid.UUID, a *DesiredAgent) error {
	_, err := q.Exec(ctx, `
		INSERT INTO supervisor_desired_agents (id, supervisor_id, kind, name, harness, state, launch, generation, resume_generation)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (tenant_id, id) DO UPDATE
		   SET harness = EXCLUDED.harness, state = EXCLUDED.state, launch = EXCLUDED.launch,
		       generation = EXCLUDED.generation, resume_generation = EXCLUDED.resume_generation`,
		a.ID, supervisorID, string(a.Kind), a.Name, a.Harness, string(a.State), string(a.Launch), a.Generation, a.ResumeGeneration)
	if pgerr.IsUniqueViolation(err, "supervisor_desired_agents_live_name") {
		return fmt.Errorf("%w: %q", ErrDesiredNameHeld, a.Name)
	}
	if err != nil {
		return fmt.Errorf("agent: desired agent: write: %w", err)
	}
	return nil
}

func (s *PostgresProvisionStore) Observe(ctx context.Context, supervisorID uuid.UUID, reports []ObservedReport, retire RetireFunc) error {
	return s.db.InTx(ctx, func(q db.Querier) error {
		for _, r := range reports {
			stored, err := observeSteward(ctx, q, r)
			if err != nil {
				return err
			}
			if stored {
				continue
			}
			if r.NoGeneration {
				return desiredInvalid("agent %s needs a generation", r.ID)
			}
			var retired bool
			entry, err := scanDesiredRetired(q.QueryRow(ctx, `
				SELECT `+desiredColumns+`, retired_at IS NOT NULL FROM supervisor_desired_agents
				WHERE id = $1 AND supervisor_id = $2 FOR UPDATE`, r.ID, supervisorID), &retired)
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				return desiredInvalid("agent %s is not in your document", r.ID)
			case err != nil:
				return fmt.Errorf("agent: desired agent: read: %w", err)
			case retired:
				continue // a retry of the report that retired it
			case r.Generation > entry.Generation:
				return desiredInvalid("agent %s is at generation %d, not %d", r.ID, entry.Generation, r.Generation)
			case r.Phase == PhaseRetired && (entry.State != StateRemoved || r.Generation != entry.Generation):
				return desiredInvalid("agent %s is not removed at generation %d", r.ID, r.Generation)
			case r.Generation < entry.Observed.Generation:
				continue // overtaken by a later report
			}
			if r.Phase == PhaseRetired {
				r.Harness = ""
				if err := retire(ctx, &entry); err != nil {
					return err
				}
			}
			if _, err := q.Exec(ctx, `
				UPDATE supervisor_desired_agents
				   SET observed_generation = $3, observed_phase = $4, failure_reason = $5, observed_harness = $6,
				       retired_at = CASE WHEN $4 = 'retired' THEN now() END
				 WHERE id = $1 AND supervisor_id = $2`,
				r.ID, supervisorID, r.Generation, string(r.Phase), r.Reason, r.Harness); err != nil {
				return fmt.Errorf("agent: desired agent: observe: %w", err)
			}
		}
		return nil
	})
}

func (s *PostgresProvisionStore) RetireRemoved(ctx context.Context, supervisorID uuid.UUID, ids []uuid.UUID, retire RetireFunc) error {
	return s.db.InTx(ctx, func(q db.Querier) error {
		for _, id := range ids {
			entry, err := scanDesired(q.QueryRow(ctx, `
				SELECT `+desiredColumns+` FROM supervisor_desired_agents
				WHERE id = $1 AND supervisor_id = $2 AND state = 'removed' AND retired_at IS NULL FOR UPDATE`, id, supervisorID))
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return fmt.Errorf("agent: desired agent: read: %w", err)
			}
			if err := retire(ctx, &entry); err != nil {
				return err
			}
		}
		return nil
	})
}

// observeSteward stores r when it names the tenant's live steward. The
// steward is not in the document, so a retired report is left to the caller
// to refuse as an unknown entry.
func observeSteward(ctx context.Context, q db.Querier, r ObservedReport) (bool, error) {
	if r.Phase == PhaseRetired {
		return false, nil
	}
	tag, err := q.Exec(ctx, `
		UPDATE agents SET observed_phase = $2, observed_reason = $3, observed_at = now()
		WHERE id = $1 AND kind = 'steward' AND archived_at IS NULL`, r.ID, string(r.Phase), r.Reason)
	if err != nil {
		return false, fmt.Errorf("agent: steward observed: write: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *PostgresProvisionStore) StewardReports(ctx context.Context) (map[uuid.UUID]ObservedSteward, error) {
	rows, err := s.db.Query(ctx, `SELECT id, observed_phase, observed_reason, observed_at FROM agents WHERE observed_phase IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("agent: steward observed: read: %w", err)
	}
	defer rows.Close()
	out := map[uuid.UUID]ObservedSteward{}
	for rows.Next() {
		var id uuid.UUID
		var o ObservedSteward
		var phase string
		if err := rows.Scan(&id, &phase, &o.Reason, &o.ReportedAt); err != nil {
			return nil, fmt.Errorf("agent: steward observed: scan: %w", err)
		}
		o.Phase = Phase(phase)
		out[id] = o
	}
	return out, rows.Err()
}
