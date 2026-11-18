package variant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/harnessconfig"
)

// Store keeps the tenant's versions. Every call runs in the context's
// tenant; row level security hides every other tenant's rows.
type Store interface {
	// List is the tenant's versions; archived ones only when asked for.
	List(ctx context.Context, includeArchived bool) ([]Version, error)
	// Create saves v as the next version of v.Name (v.Version and v.ID are
	// ignored). ErrInvalid for a fleet-instructions version the tenant
	// does not have.
	Create(ctx context.Context, v *Version) (*Version, error)
	// SetVersionArchived hides or lists one version; ErrNotFound if the
	// tenant has none such. Repeating it changes nothing.
	SetVersionArchived(ctx context.Context, id uuid.UUID, archive bool, by uuid.UUID) (*Version, error)
	// SetGroupArchived does the same for every version of name and returns
	// how many changed. ErrNotFound for an unknown name.
	SetGroupArchived(ctx context.Context, name string, archive bool, by uuid.UUID) (int, error)
	// SetAgentVariant puts the agent on a version, or takes it off one with
	// nil, and returns the version it was on and the one it is on (nil for none). A version that
	// is archived cannot be newly assigned (ErrInvalid; an agent already on
	// it staying put is not a new assignment), and a version the tenant does
	// not have is ErrNotFound. ErrAgentNotFound for an unknown agent. Taking
	// the next config revision is what marks the agent's running session
	// stale.
	SetAgentVariant(ctx context.Context, agentID uuid.UUID, versionID *uuid.UUID) (previous, now *Version, err error)
}

var _ harnessconfig.Variants = (*PostgresStore)(nil)

// AgentVariants is the owner of the agents table, which holds the variant an
// agent is on. It runs in the caller's transaction so the assignment stays
// atomic with the checks around it. A port because agent depends on variant.
type AgentVariants interface {
	SetVariant(ctx context.Context, q db.Querier, agentID uuid.UUID, versionID *uuid.UUID, revision int64) error
}

type PostgresStore struct {
	db     *db.DB
	agents AgentVariants
}

func NewPostgresStore(database *db.DB, agents AgentVariants) *PostgresStore {
	return &PostgresStore{db: database, agents: agents}
}

var _ Store = (*PostgresStore)(nil)

const versionColumns = `v.id, v.name, v.version, v.harness, v.settings, v.fleet_instructions_version_id, v.created_by, v.created_by_deleted, v.created_at, a.archived_at`

// versionFrom is the versions joined to their archive rows, which exist only
// for archived versions.
const versionFrom = `variant_versions v
		  LEFT JOIN variant_archives a ON a.tenant_id = v.tenant_id AND a.version_id = v.id`

func scanVersion(row pgx.Row) (*Version, error) {
	var v Version
	var raw []byte
	if err := row.Scan(&v.ID, &v.Name, &v.Version, &v.Harness, &raw, &v.FleetInstructionsVersionID, &v.CreatedBy, &v.CreatedByDeleted, &v.CreatedAt, &v.ArchivedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &v.Settings); err != nil {
		return nil, fmt.Errorf("settings of variant %s: %w", v.ID, err)
	}
	// What was stored was validated on write; this is the check on the way
	// out, for a setting dropped or narrowed since.
	v.Settings = harnessconfig.Values{v.Harness: v.Settings}.Known()[v.Harness]
	return &v, nil
}

func (s *PostgresStore) List(ctx context.Context, includeArchived bool) ([]Version, error) {
	out := []Version{}
	err := s.db.InReadTx(ctx, func(q db.Querier) error {
		rows, err := q.Query(ctx, `SELECT `+versionColumns+` FROM `+versionFrom+`
			 WHERE $1 OR a.version_id IS NULL
			 ORDER BY lower(v.name), v.name, v.version DESC`, includeArchived)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanVersion(rows)
			if err != nil {
				return err
			}
			out = append(out, *v)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("variants: list: %w", err)
	}
	return out, nil
}

func (s *PostgresStore) Create(ctx context.Context, in *Version) (*Version, error) {
	var created *Version
	err := s.db.InTx(ctx, func(q db.Querier) error {
		// A visible row first gives a plain refusal rather than a
		// foreign key error; row level security makes another tenant's
		// version look absent.
		if in.FleetInstructionsVersionID != nil {
			var exists bool
			if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM fleet_instruction_versions WHERE id = $1 AND kind = 'fleet')`,
				*in.FleetInstructionsVersionID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return invalid("no such fleet instructions version")
			}
		}
		settings, err := json.Marshal(in.Settings)
		if err != nil {
			return err
		}
		// Two saves of one name must not both read the same highest
		// version: the lock makes the second wait and see the first.
		if err := lockName(ctx, q, in.Name); err != nil {
			return err
		}
		created, err = scanVersion(q.QueryRow(ctx, `
			INSERT INTO variant_versions AS v (name, version, harness, settings, fleet_instructions_version_id, created_by)
			SELECT $1, coalesce(max(version), 0) + 1, $2, $3, $4, $5 FROM variant_versions WHERE name = $1
			RETURNING v.id, v.name, v.version, v.harness, v.settings, v.fleet_instructions_version_id, v.created_by, v.created_by_deleted, v.created_at, NULL::timestamptz`,
			in.Name, in.Harness, settings, in.FleetInstructionsVersionID, in.CreatedBy))
		return err
	})
	if errors.Is(err, ErrInvalid) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("variants: create: %w", err)
	}
	return created, nil
}

// lockName serialises what decides the versions a name has: saving one,
// archiving or restoring the name or one of its versions, and putting an
// agent on one -- which would otherwise miss a version saved between its read
// and its write, or assign one archived in between.
func lockName(ctx context.Context, q db.Querier, name string) error {
	_, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_tenant()::text || '/variant/' || $1, 0))`, name)
	return err
}

// setArchived hides or lists the versions matching where, holding the
// name's lock when lockedName is set, and returns how
// many changed and, when exactly one matched, that version.
func (s *PostgresStore) setArchived(ctx context.Context, where string, arg any, archive bool, by uuid.UUID, lockedName string) (int, *Version, error) {
	var n int
	var only *Version
	err := s.db.InTx(ctx, func(q db.Querier) error {
		if lockedName != "" {
			if err := lockName(ctx, q, lockedName); err != nil {
				return err
			}
		}
		var ids []uuid.UUID
		rows, err := q.Query(ctx, `SELECT v.id FROM variant_versions v WHERE `+where, arg)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(ids) == 0 {
			return ErrNotFound
		}
		if archive {
			tag, err := q.Exec(ctx, `
				INSERT INTO variant_archives (version_id, archived_by)
				SELECT unnest($1::uuid[]), $2 ON CONFLICT DO NOTHING`, ids, by)
			if err != nil {
				return err
			}
			n = int(tag.RowsAffected())
		} else {
			tag, err := q.Exec(ctx, `DELETE FROM variant_archives WHERE version_id = ANY($1)`, ids)
			if err != nil {
				return err
			}
			n = int(tag.RowsAffected())
		}
		if len(ids) == 1 {
			only, err = scanVersion(q.QueryRow(ctx, `SELECT `+versionColumns+` FROM `+versionFrom+` WHERE v.id = $1`, ids[0]))
		}
		return err
	})
	if errors.Is(err, ErrNotFound) {
		return 0, nil, err
	}
	if err != nil {
		return 0, nil, fmt.Errorf("variants: archive: %w", err)
	}
	return n, only, nil
}

func (s *PostgresStore) SetVersionArchived(ctx context.Context, id uuid.UUID, archive bool, by uuid.UUID) (*Version, error) {
	// A name never changes, so reading it ahead of the transaction is safe;
	// the name's lock is what orders this against assigning the version.
	var name string
	if err := s.db.QueryRow(ctx, `SELECT name FROM variant_versions WHERE id = $1`, id).Scan(&name); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("variants: archive: %w", err)
	}
	_, v, err := s.setArchived(ctx, `v.id = $1`, id, archive, by, name)
	return v, err
}

func (s *PostgresStore) SetGroupArchived(ctx context.Context, name string, archive bool, by uuid.UUID) (int, error) {
	n, _, err := s.setArchived(ctx, `v.name = $1`, name, archive, by, name)
	return n, err
}

func (s *PostgresStore) SetAgentVariant(ctx context.Context, agentID uuid.UUID, versionID *uuid.UUID) (*Version, *Version, error) {
	var previous, now *Version
	err := s.db.InTx(ctx, func(q db.Querier) error {
		// The revision first, as harnessconfig's writes take it: a save of
		// the agent's override then takes the agent row (its foreign key),
		// so the other order could deadlock. A change that turns out to be
		// none wastes a number, which is harmless.
		revision, err := harnessconfig.NextRevision(ctx, q)
		if err != nil {
			return err
		}
		// Locking the agent row serialises changes to one agent, so
		// "previous" is what this change replaced.
		var current *uuid.UUID
		if err := q.QueryRow(ctx, `SELECT variant_id FROM agents WHERE id = $1 FOR UPDATE`, agentID).Scan(&current); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrAgentNotFound
			}
			return err
		}
		load := func(id uuid.UUID) (*Version, error) {
			v, err := scanVersion(q.QueryRow(ctx, `SELECT `+versionColumns+` FROM `+versionFrom+` WHERE v.id = $1`, id))
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrNotFound
			}
			return v, err
		}
		if current != nil {
			if previous, err = load(*current); err != nil {
				return err
			}
		}
		if versionID != nil {
			if now, err = load(*versionID); err != nil {
				return err
			}
			// Archiving takes the same lock, so the archive state read
			// below cannot change before this commits.
			if err := lockName(ctx, q, now.Name); err != nil {
				return err
			}
			if now, err = load(*versionID); err != nil {
				return err
			}
		}
		if previous == nil && now == nil || previous != nil && now != nil && previous.ID == now.ID {
			return nil
		}
		if now != nil && now.ArchivedAt != nil {
			return invalid("that version is archived, so no agent can newly be put on it")
		}
		var assigned *uuid.UUID
		if now != nil {
			assigned = &now.ID
		}
		return s.agents.SetVariant(ctx, q, agentID, assigned, revision)
	})
	if errors.Is(err, ErrAgentNotFound) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalid) {
		return nil, nil, err
	}
	if err != nil {
		return nil, nil, fmt.Errorf("variants: set agent variant: %w", err)
	}
	return previous, now, nil
}

const membershipSelect = `SELECT ag.id, ag.variant_revision, ` + versionColumns + `
	  FROM agents ag
	  JOIN ` + versionFrom + ` ON v.tenant_id = ag.tenant_id AND v.id = ag.variant_id`

func scanMembership(row pgx.Row) (uuid.UUID, *harnessconfig.VariantMembership, error) {
	var agentID uuid.UUID
	var revision int64
	var v Version
	var raw []byte
	if err := row.Scan(&agentID, &revision, &v.ID, &v.Name, &v.Version, &v.Harness, &raw, &v.FleetInstructionsVersionID, &v.CreatedBy, &v.CreatedByDeleted, &v.CreatedAt, &v.ArchivedAt); err != nil {
		return uuid.Nil, nil, err
	}
	if err := json.Unmarshal(raw, &v.Settings); err != nil {
		return uuid.Nil, nil, fmt.Errorf("settings of variant %s: %w", v.ID, err)
	}
	return agentID, &harnessconfig.VariantMembership{
		VersionID: v.ID, Name: v.Name, Version: v.Version, Harness: v.Harness,
		// Checked on the way out, as scanVersion does.
		Settings:                   harnessconfig.Values{v.Harness: v.Settings}.Known()[v.Harness],
		FleetInstructionsVersionID: v.FleetInstructionsVersionID, Revision: revision,
	}, nil
}

func (s *PostgresStore) InVariants(ctx context.Context) (map[uuid.UUID]*harnessconfig.VariantMembership, error) {
	rows, err := s.db.Query(ctx, membershipSelect)
	if err != nil {
		return nil, fmt.Errorf("variants: memberships: %w", err)
	}
	defer rows.Close()
	out := map[uuid.UUID]*harnessconfig.VariantMembership{}
	for rows.Next() {
		id, m, err := scanMembership(rows)
		if err != nil {
			return nil, fmt.Errorf("variants: memberships: %w", err)
		}
		out[id] = m
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("variants: memberships: %w", err)
	}
	return out, nil
}

func (s *PostgresStore) ForAgent(ctx context.Context, agentID uuid.UUID) (*harnessconfig.VariantMembership, error) {
	_, m, err := scanMembership(s.db.QueryRow(ctx, membershipSelect+` WHERE ag.id = $1`, agentID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("variants: membership: %w", err)
	}
	return m, nil
}

// AssignableVersion is the version id as something may newly be pointed at it
// (an attempt routed to it), read through q in the caller's transaction.
// ErrNotFound for one the tenant does not have, ErrInvalid for an archived
// one. It takes the name's lock, as SetAgentVariant does, so an archive
// cannot land between this check and the caller's commit.
func AssignableVersion(ctx context.Context, q db.Querier, id uuid.UUID) (*Version, error) {
	load := func() (*Version, error) {
		v, err := scanVersion(q.QueryRow(ctx, `SELECT `+versionColumns+` FROM `+versionFrom+` WHERE v.id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return v, err
	}
	v, err := load()
	if err != nil {
		return nil, err
	}
	if err := lockName(ctx, q, v.Name); err != nil {
		return nil, err
	}
	if v, err = load(); err != nil {
		return nil, err
	}
	if v.ArchivedAt != nil {
		return nil, invalid("that version is archived, so nothing can newly be routed to it")
	}
	return v, nil
}
