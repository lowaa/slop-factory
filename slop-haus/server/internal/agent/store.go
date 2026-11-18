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

type PostgresStore struct {
	db *db.DB
}

func NewPostgresStore(database *db.DB) *PostgresStore {
	return &PostgresStore{db: database}
}

var _ Store = (*PostgresStore)(nil)

// The replacement links are read as subqueries on agents.id, so every query
// using these columns must name its row source "agents" (disable_agent's
// result is aliased to it).
const agentColumns = `agents.id, agents.tenant_id, agents.name, agents.created_at, agents.disabled_at, agents.archived_at, agents.kind,
	agents.owner_user_id, agents.owner_user_id_deleted, agents.supervisor_agent_id, agents.slot_cap, agents.run_by_supervisor_id,
	agents.replaces_agent_id, (SELECT p.name FROM agents p WHERE p.id = agents.replaces_agent_id) AS replaces_name,
	(SELECT n.id FROM agents n WHERE n.replaces_agent_id = agents.id) AS replaced_by_id,
	(SELECT n.name FROM agents n WHERE n.replaces_agent_id = agents.id) AS replaced_by_name`

// scanAgent reads agentColumns, then any extra destinations the query
// selects after them.
func scanAgent(row pgx.Row, extra ...any) (*Agent, error) {
	var (
		a                          Agent
		replacesID, replacedByID   *uuid.UUID
		replacesName, replacedName *string
	)
	dest := append([]any{&a.ID, &a.Tenant, &a.Name, &a.CreatedAt, &a.DisabledAt, &a.ArchivedAt, &a.Kind, &a.OwnerUserID, &a.OwnerUserDeleted, &a.SupervisorID, &a.SlotCap, &a.RunBySupervisorID,
		&replacesID, &replacesName, &replacedByID, &replacedName}, extra...)
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	if replacesID != nil {
		a.Replaces = &AgentRef{ID: *replacesID, Name: *replacesName}
	}
	if replacedByID != nil {
		a.ReplacedBy = &AgentRef{ID: *replacedByID, Name: *replacedName}
	}
	return &a, nil
}

// Create registers the agent in the context's tenant.
// Create registers a worker or a console.
func (s *PostgresStore) Create(ctx context.Context, id uuid.UUID, name string, kind Kind) (*Agent, error) {
	created, err := scanAgent(s.db.QueryRow(ctx,
		`INSERT INTO agents (id, name, kind) VALUES ($1, $2, $3) RETURNING `+agentColumns,
		id, name, string(kind)))
	if pgerr.IsUniqueViolation(err) {
		return nil, fmt.Errorf("%w: %q", ErrNameTaken, name)
	}
	if err != nil {
		return nil, fmt.Errorf("agent: create: %w", err)
	}
	return created, nil
}

// CreateSupervisor registers a supervisor that may have slotCap slots. The
// database refuses a cap outside 1..MaxSlotCap.
func (s *PostgresStore) CreateSupervisor(ctx context.Context, id uuid.UUID, name string, slotCap int) (*Agent, error) {
	created, err := scanAgent(s.db.QueryRow(ctx,
		`INSERT INTO agents (id, name, kind, slot_cap) VALUES ($1, $2, 'supervisor', $3) RETURNING `+agentColumns,
		id, name, slotCap))
	if pgerr.IsUniqueViolation(err) {
		return nil, fmt.Errorf("%w: %q", ErrNameTaken, name)
	}
	if err != nil {
		return nil, fmt.Errorf("agent: create supervisor: %w", err)
	}
	return created, nil
}

func (s *PostgresStore) SetSlotCap(ctx context.Context, id uuid.UUID, slotCap int) (*Agent, int, error) {
	var updated *Agent
	var previous int
	err := s.db.InTx(ctx, func(q db.Querier) error {
		sup, err := scanAgent(q.QueryRow(ctx, `SELECT `+agentColumns+` FROM agents WHERE id = $1 FOR UPDATE`, id))
		if err == nil && (!sup.IsSupervisor() || sup.Archived()) {
			// An archived supervisor reads as missing, as it does to the rest of the provisioning service.
			return ErrNotFound
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("agent: lock supervisor: %w", err)
		}
		previous = *sup.SlotCap
		updated, err = scanAgent(q.QueryRow(ctx, `UPDATE agents SET slot_cap = $2 WHERE id = $1 RETURNING `+agentColumns, id, slotCap))
		if err != nil {
			return fmt.Errorf("agent: set slot cap: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return updated, previous, nil
}

// StewardName is what the tenant's steward is called when a supervisor mints it.
const StewardName = "steward"

// EnsureSteward is the tenant's live steward, made on the first call. The
// supervisor's row is locked for its state check, so a supervisor switched off
// never mints one; the tenant's advisory lock serialises the lookup and the
// insert across supervisors, with the database's one-live-steward index as the
// backstop.
func (s *PostgresStore) EnsureSteward(ctx context.Context, id, supervisorID uuid.UUID) (*Agent, error) {
	var steward *Agent
	err := s.db.InTx(ctx, func(q db.Querier) error {
		sup, err := scanAgent(q.QueryRow(ctx, `SELECT `+agentColumns+` FROM agents WHERE id = $1 FOR UPDATE`, supervisorID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("agent: lock supervisor: %w", err)
		}
		switch {
		case !sup.IsSupervisor():
			return ErrNotSupervisor
		case sup.Archived():
			return ErrArchived
		case sup.Disabled():
			return ErrIsDisabled
		}
		// Supervisors lock only their own row, so two of them minting at once
		// would both see no steward; the tenant's lock makes the loser find it.
		if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_tenant()::text || '/steward', 0))`); err != nil {
			return fmt.Errorf("agent: lock steward minting: %w", err)
		}
		steward, err = scanAgent(q.QueryRow(ctx,
			`SELECT `+agentColumns+` FROM agents WHERE kind = 'steward' AND archived_at IS NULL`))
		if err == nil {
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("agent: look up steward: %w", err)
		}
		steward, err = scanAgent(q.QueryRow(ctx,
			`INSERT INTO agents (id, name, kind, steward_minted_by) VALUES ($1, $2, 'steward', $3) RETURNING `+agentColumns,
			id, StewardName, supervisorID))
		if pgerr.IsUniqueViolation(err) {
			return fmt.Errorf("%w: %q", ErrNameTaken, StewardName)
		}
		if err != nil {
			return fmt.Errorf("agent: create steward: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return steward, nil
}

// ListSupervisors is the tenant's supervisors, archived ones only when asked.
func (s *PostgresStore) ListSupervisors(ctx context.Context, includeArchived bool) ([]Agent, error) {
	return s.list(ctx, `SELECT `+agentColumns+` FROM agents
		WHERE kind = 'supervisor' AND ($1 OR archived_at IS NULL) ORDER BY name`, includeArchived)
}

// CreateConductor registers a conductor owned by ownerID, who must be a
// member of the context's tenant (the owner foreign key refuses anyone else),
// and run by the supervisor runBy when one made it.
func (s *PostgresStore) CreateConductor(ctx context.Context, id uuid.UUID, name string, ownerID uuid.UUID, runBy *uuid.UUID) (*Agent, error) {
	created, err := scanAgent(s.db.QueryRow(ctx,
		`INSERT INTO agents (id, name, kind, owner_user_id, run_by_supervisor_id) VALUES ($1, $2, 'conductor', $3, $4) RETURNING `+agentColumns,
		id, name, ownerID, runBy))
	if pgerr.IsUniqueViolation(err) {
		return nil, fmt.Errorf("%w: %q", ErrNameTaken, name)
	}
	if pgerr.IsForeignKeyViolation(err) {
		return nil, ErrNotOwner
	}
	if err != nil {
		return nil, fmt.Errorf("agent: create conductor: %w", err)
	}
	return created, nil
}

func (s *PostgresStore) Get(ctx context.Context, id uuid.UUID) (*Agent, error) {
	return s.one(ctx, `SELECT `+agentColumns+` FROM agents WHERE id = $1`, id)
}

func (s *PostgresStore) GetByName(ctx context.Context, name string) (*Agent, error) {
	return s.one(ctx, `SELECT `+agentColumns+` FROM agents WHERE name = $1`, name)
}

func (s *PostgresStore) one(ctx context.Context, query string, args ...any) (*Agent, error) {
	found, err := scanAgent(s.db.QueryRow(ctx, query, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("agent: query: %w", err)
	}
	return found, nil
}

// List is the tenant's workers and consoles, archived ones only when asked:
// conductors are listed through ListConductors, to the people allowed to see
// them.
func (s *PostgresStore) List(ctx context.Context, includeArchived bool) ([]Agent, error) {
	return s.list(ctx, `SELECT `+agentColumns+` FROM agents
		WHERE kind IN ('worker', 'console', 'steward') AND ($1 OR archived_at IS NULL) ORDER BY name`, includeArchived)
}

// ListConductors is the conductors owned by ownerID, or the tenant's
// when ownerID is nil; archived ones only when asked.
func (s *PostgresStore) ListConductors(ctx context.Context, ownerID *uuid.UUID, includeArchived bool) ([]Agent, error) {
	return s.list(ctx, `SELECT `+agentColumns+` FROM agents
		WHERE kind = 'conductor' AND ($1::uuid IS NULL OR owner_user_id = $1) AND ($2 OR archived_at IS NULL)
		ORDER BY name`, ownerID, includeArchived)
}

// ListConductorDirectory is every conductor in the tenant with when its host
// last polled and whether a conversation is running on it. Only those two
// facts are read from the conversations table: nothing that identifies or
// describes a conversation leaves the store.
func (s *PostgresStore) ListConductorDirectory(ctx context.Context, includeArchived bool) ([]DirectoryEntry, error) {
	rows, err := s.db.Query(ctx, `SELECT `+agentColumns+`,
			(SELECT COALESCE(NULLIF(u.display_name, ''), 'a member') FROM global_users u WHERE u.id = agents.owner_user_id),
			(SELECT q.host_seen_at FROM conductor_queues q WHERE q.conductor_id = agents.id),
			EXISTS (SELECT 1 FROM conductor_conversations c WHERE c.conductor_id = agents.id AND c.state IN ('starting', 'ready'))
		FROM agents
		WHERE kind = 'conductor' AND ($1 OR archived_at IS NULL)
		ORDER BY name`, includeArchived)
	if err != nil {
		return nil, fmt.Errorf("agent: list conductor directory: %w", err)
	}
	defer rows.Close()

	entries := []DirectoryEntry{}
	for rows.Next() {
		var e DirectoryEntry
		a, err := scanAgent(rows, &e.OwnerName, &e.HostSeenAt, &e.HasLiveSession)
		if err != nil {
			return nil, fmt.Errorf("agent: scan conductor directory: %w", err)
		}
		e.Agent = *a
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (s *PostgresStore) list(ctx context.Context, query string, args ...any) ([]Agent, error) {
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("agent: list: %w", err)
	}
	defer rows.Close()

	agents := []Agent{}
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("agent: scan: %w", err)
		}
		agents = append(agents, *a)
	}
	return agents, rows.Err()
}

// SetDisabled flips the switch on the agents row. Disabling goes through the
// database's disable_agent, the one place that also voids the agent's pending
// proposals and revokes its grants (the membership removal calls it too).
//
// Releasing whatever the agent was holding is the ticket package's job --
// it owns the leases, locks and tickets tables, and doing it from here
// meant a second writer to all three, outside the advisory lock that
// serializes the claim path. Service.SetDisabled sequences the two.
func (s *PostgresStore) SetDisabled(ctx context.Context, id uuid.UUID, disabled bool) (*Agent, error) {
	var row pgx.Row
	if disabled {
		row = s.db.QueryRow(ctx, `SELECT `+agentColumns+` FROM disable_agent($1, 'agent disabled') AS agents`, id)
	} else {
		row = s.db.QueryRow(ctx, `UPDATE agents SET disabled_at = NULL WHERE id = $1 AND archived_at IS NULL RETURNING `+agentColumns, id)
	}

	updated, err := scanAgent(row)
	if errors.Is(err, pgx.ErrNoRows) {
		// Only the enable can match nothing for a row that exists.
		return nil, s.missingOrArchived(ctx, id)
	}
	if err != nil {
		return nil, fmt.Errorf("agent: set disabled: %w", err)
	}
	return updated, nil
}

// missingOrArchived says why an update that matched no row did not: there is
// no such agent, or it is archived.
func (s *PostgresStore) missingOrArchived(ctx context.Context, id uuid.UUID) error {
	found, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if found.Archived() {
		return ErrArchived
	}
	return ErrNotFound
}

// SetArchived puts a disabled agent away or brings it back. Archiving an
// agent that is still enabled is ErrNotDisabled; archiving twice keeps the
// first time. Unarchiving leaves the agent disabled.
func (s *PostgresStore) SetArchived(ctx context.Context, id uuid.UUID, archived bool) (*Agent, error) {
	query := `UPDATE agents SET archived_at = NULL WHERE id = $1 RETURNING ` + agentColumns
	if archived {
		query = `UPDATE agents SET archived_at = COALESCE(archived_at, now())
			WHERE id = $1 AND disabled_at IS NOT NULL RETURNING ` + agentColumns
	}
	updated, err := scanAgent(s.db.QueryRow(ctx, query, id))
	if errors.Is(err, pgx.ErrNoRows) {
		if _, getErr := s.Get(ctx, id); getErr != nil {
			return nil, getErr
		}
		return nil, ErrNotDisabled
	}
	if err != nil {
		return nil, fmt.Errorf("agent: set archived: %w", err)
	}
	return updated, nil
}

// Replace registers a new agent that takes over from oldID, and disables and
// archives oldID, in one transaction: a failure part way leaves both as they
// were, never a new agent beside a live old one or the old one gone with no
// successor. The old agent's row is locked first, so two replacements racing
// for it are serialised and the second finds it archived.
//
// What the old agent was holding is released by the caller afterwards, as for
// any disable (see ReleaseLeasesFor): those tables belong to the ticket
// package. The returned old agent carries the disable's time for that.
func (s *PostgresStore) Replace(ctx context.Context, newID uuid.UUID, name string, kind Kind, oldID uuid.UUID) (created, old *Agent, err error) {
	err = s.db.InTx(ctx, func(q db.Querier) error {
		current, err := scanAgent(q.QueryRow(ctx, `SELECT `+agentColumns+` FROM agents WHERE id = $1 FOR UPDATE`, oldID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("agent: lock replaced agent: %w", err)
		}
		if current.Archived() || current.ReplacedBy != nil {
			return ErrNotReplaceable
		}
		created, err = scanAgent(q.QueryRow(ctx,
			`INSERT INTO agents (id, name, kind, replaces_agent_id) VALUES ($1, $2, $3, $4) RETURNING `+agentColumns,
			newID, name, string(kind), oldID))
		if pgerr.IsUniqueViolation(err) {
			return fmt.Errorf("%w: %q", ErrNameTaken, name)
		}
		if err != nil {
			return fmt.Errorf("agent: create replacement: %w", err)
		}
		if _, err := scanAgent(q.QueryRow(ctx, `SELECT `+agentColumns+` FROM disable_agent($1, 'agent replaced') AS agents`, oldID)); err != nil {
			return fmt.Errorf("agent: disable replaced agent: %w", err)
		}
		old, err = scanAgent(q.QueryRow(ctx,
			`UPDATE agents SET archived_at = now() WHERE id = $1 RETURNING `+agentColumns, oldID))
		if err != nil {
			return fmt.Errorf("agent: archive replaced agent: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return created, old, nil
}

// CreateSlot registers a worker as one of the supervisor's slots. The
// supervisor's row is locked for the check and the insert, so two creates
// racing for the last place are serialised and the second counts the first;
// disabling the supervisor takes the same lock, so a slot is never created
// by a supervisor that has just been switched off.
//
// Archived slots do not count towards the cap: retiring one frees its place.
// A disabled one still does, until it is retired.
func (s *PostgresStore) CreateSlot(ctx context.Context, id, supervisorID uuid.UUID, name string) (*Agent, error) {
	var created *Agent
	err := s.db.InTx(ctx, func(q db.Querier) error {
		if err := lockSlotRoom(ctx, q, supervisorID); err != nil {
			return err
		}
		revived, err := reviveRetiredSlot(ctx, q, supervisorID, name)
		if err != nil {
			return err
		}
		if revived != nil {
			created = revived
			return nil
		}
		created, err = scanAgent(q.QueryRow(ctx,
			`INSERT INTO agents (id, name, kind, supervisor_agent_id) VALUES ($1, $2, 'worker', $3) RETURNING `+agentColumns,
			id, name, supervisorID))
		if pgerr.IsUniqueViolation(err) {
			return fmt.Errorf("%w: %q", ErrNameTaken, name)
		}
		if err != nil {
			return fmt.Errorf("agent: create slot: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// lockSlotRoom locks the supervisor's row and checks it is a live supervisor
// with a place free under its cap.
func lockSlotRoom(ctx context.Context, q db.Querier, supervisorID uuid.UUID) error {
	sup, err := scanAgent(q.QueryRow(ctx, `SELECT `+agentColumns+` FROM agents WHERE id = $1 FOR UPDATE`, supervisorID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("agent: lock supervisor: %w", err)
	}
	switch {
	case !sup.IsSupervisor():
		return ErrNotSupervisor
	case sup.Archived():
		return ErrArchived
	case sup.Disabled():
		return ErrIsDisabled
	}
	var live int
	if err := q.QueryRow(ctx,
		`SELECT count(*) FROM agents WHERE supervisor_agent_id = $1 AND archived_at IS NULL`, supervisorID).Scan(&live); err != nil {
		return fmt.Errorf("agent: count slots: %w", err)
	}
	if live >= *sup.SlotCap {
		return ErrSlotCapReached
	}
	return nil
}

// AdoptSlot links an existing worker that has no supervisor, e.g. one that
// began as an interactive worker and was reused as a slot, to the supervisor.
// It takes the same lock and cap check as CreateSlot. A worker already linked
// to this supervisor is returned as it is, so a retry is harmless; one linked
// to another supervisor reads as not found, so names cannot be probed across
// supervisors. Archived agents and other kinds are refused.
func (s *PostgresStore) AdoptSlot(ctx context.Context, supervisorID uuid.UUID, name string) (*Agent, error) {
	var adopted *Agent
	err := s.db.InTx(ctx, func(q db.Querier) error {
		// A full supervisor may still be re-adopting its own slot.
		capErr := lockSlotRoom(ctx, q, supervisorID)
		if capErr != nil && !errors.Is(capErr, ErrSlotCapReached) {
			return capErr
		}
		held, err := scanAgent(q.QueryRow(ctx, `SELECT `+agentColumns+` FROM agents WHERE name = $1 FOR UPDATE`, name))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("agent: look up slot to adopt: %w", err)
		}
		switch {
		case held.SupervisorID != nil && *held.SupervisorID == supervisorID && !held.Archived():
			adopted = held
			return nil
		case held.SupervisorID != nil:
			return ErrNotFound
		case held.Kind != KindWorker:
			return ErrNotAdoptable
		case held.Archived():
			return ErrArchived
		case capErr != nil:
			return capErr
		}
		adopted, err = scanAgent(q.QueryRow(ctx,
			`UPDATE agents SET supervisor_agent_id = $2 WHERE id = $1 RETURNING `+agentColumns, held.ID, supervisorID))
		if err != nil {
			return fmt.Errorf("agent: adopt slot: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return adopted, nil
}

// reviveRetiredSlot is how a slot name survives a `slot add` that failed after
// the agent was made: the rollback retires (archives) the agent, and archived
// agents keep their names. When name is held by one of this supervisor's own
// retired slots that never did anything (no lease, comment or ticket), it is
// brought back live instead of the name being lost for good. Nil when nothing
// holds the name. Any other holder is ErrNameTaken, and a retired slot of the
// supervisor's with a past is ErrNameRetired: its history keeps the name.
// The history is read from leases, which are pruned after their retention, so
// this is for the half-made slot of a failed `slot add`, not an archive.
func reviveRetiredSlot(ctx context.Context, q db.Querier, supervisorID uuid.UUID, name string) (*Agent, error) {
	held, err := scanAgent(q.QueryRow(ctx, `SELECT `+agentColumns+` FROM agents WHERE name = $1 FOR UPDATE`, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("agent: look up slot name: %w", err)
	}
	// A replaced slot's identity has moved on to its successor.
	if held.SupervisorID == nil || *held.SupervisorID != supervisorID || !held.Archived() || held.ReplacedBy != nil {
		return nil, fmt.Errorf("%w: %q", ErrNameTaken, name)
	}
	var used bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM leases WHERE agent_id = $1)
		OR EXISTS (SELECT 1 FROM ticket_comments WHERE author_agent_id = $1)
		OR EXISTS (SELECT 1 FROM tickets WHERE created_by_agent_id = $1)`, held.ID).Scan(&used); err != nil {
		return nil, fmt.Errorf("agent: check retired slot history: %w", err)
	}
	if used {
		return nil, fmt.Errorf("%w: %q", ErrNameRetired, name)
	}
	revived, err := scanAgent(q.QueryRow(ctx,
		`UPDATE agents SET archived_at = NULL, disabled_at = NULL WHERE id = $1 RETURNING `+agentColumns, held.ID))
	if err != nil {
		return nil, fmt.Errorf("agent: revive retired slot: %w", err)
	}
	return revived, nil
}

func (s *PostgresStore) ListSlots(ctx context.Context, supervisorID uuid.UUID, includeArchived bool) ([]Agent, error) {
	return s.list(ctx, `SELECT `+agentColumns+` FROM agents
		WHERE supervisor_agent_id = $1 AND ($2 OR archived_at IS NULL) ORDER BY name`, supervisorID, includeArchived)
}

func (s *PostgresStore) SupervisorLastSeen(ctx context.Context, id uuid.UUID) (time.Time, error) {
	var seen time.Time
	err := s.db.QueryRow(ctx, `
		SELECT GREATEST(a.created_at,
		       COALESCE((SELECT max(p.last_polled_at) FROM worker_polls p WHERE p.agent_id = a.id), a.created_at),
		       COALESCE((SELECT max(st.reported_at) FROM supervisor_slot_states st
		                   JOIN agents sl ON sl.id = st.agent_id WHERE sl.supervisor_agent_id = a.id), a.created_at))
		  FROM agents a WHERE a.id = $1 AND a.kind = 'supervisor'`, id).Scan(&seen)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, ErrNotFound
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("agent: supervisor last seen: %w", err)
	}
	return seen, nil
}

func (s *PostgresStore) DisableSlots(ctx context.Context, supervisorID uuid.UUID, reason string) ([]Agent, error) {
	return s.list(ctx, `SELECT `+agentColumns+` FROM disable_supervisor_slots($1, $2) AS agents ORDER BY name`, supervisorID, reason)
}

func (s *PostgresStore) ListEnabled(ctx context.Context) ([]Agent, error) {
	return s.list(ctx, `SELECT `+agentColumns+` FROM agents WHERE disabled_at IS NULL ORDER BY created_at, id`)
}
