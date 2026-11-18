package fleetinstructions

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// Store keeps the tenant's versions and default. Every call runs in the
// context's tenant; row level security hides every other tenant's rows.
type Store interface {
	// Load is the tenant's versions of a kind; archived ones only when asked for.
	Load(ctx context.Context, kind Kind, includeArchived bool) (*Tenant, error)
	// Create saves the next version of name, and makes it the default when
	// asked, in one transaction. It returns the version and the default it
	// replaced (nil for none).
	Create(ctx context.Context, kind Kind, name, body string, by uuid.UUID, makeDefault bool) (created *Version, previous *Version, err error)
	// SetDefault points the default at a version, or clears it with nil. It
	// returns the version it replaced. ErrNotFound for a version the tenant
	// does not have.
	SetDefault(ctx context.Context, kind Kind, id *uuid.UUID, by uuid.UUID) (previous *Version, err error)
	// Default is the fleet version agents are given, nil for none.
	Default(ctx context.Context) (*Version, error)
	// DefaultOf is the pinned version of a kind, nil for none.
	DefaultOf(ctx context.Context, kind Kind) (*Version, error)
	// Get is one version, archived or not: a variant pins a version, and
	// hiding it from the list must not change what the pin gives. ErrNotFound
	// if the tenant has none such.
	Get(ctx context.Context, id uuid.UUID) (*Version, error)
	// ArchiveVersion hides one version; ErrNotFound if the tenant has none
	// such, ErrConflict if it is the default. Archiving an archived version
	// changes nothing.
	ArchiveVersion(ctx context.Context, id uuid.UUID, by uuid.UUID) (*Version, error)
	// ArchiveGroup hides every version of name, and returns how many were
	// newly hidden. ErrNotFound for an unknown name, ErrConflict if the
	// default is among them (nothing is archived then).
	ArchiveGroup(ctx context.Context, kind Kind, name string, by uuid.UUID) (int, error)
	UnarchiveVersion(ctx context.Context, id uuid.UUID) (*Version, error)
	// UnarchiveGroup returns how many versions were restored.
	UnarchiveGroup(ctx context.Context, kind Kind, name string) (int, error)
}

type PostgresStore struct {
	db *db.DB
}

func NewPostgresStore(database *db.DB) *PostgresStore { return &PostgresStore{db: database} }

var _ Store = (*PostgresStore)(nil)

const versionColumns = `v.id, v.name, v.version, v.kind, v.body, v.created_by, v.created_by_deleted, v.created_at, a.archived_at`

// versionFrom is the versions joined to their archive rows, which exist only
// for archived versions.
const versionFrom = `fleet_instruction_versions v
		  LEFT JOIN fleet_instruction_archives a ON a.tenant_id = v.tenant_id AND a.version_id = v.id`

func scanVersion(row pgx.Row) (*Version, error) {
	var v Version
	if err := row.Scan(&v.ID, &v.Name, &v.Version, &v.Kind, &v.Body, &v.CreatedBy, &v.CreatedByDeleted, &v.CreatedAt, &v.ArchivedAt); err != nil {
		return nil, err
	}
	return &v, nil
}

func (s *PostgresStore) Load(ctx context.Context, kind Kind, includeArchived bool) (*Tenant, error) {
	t := &Tenant{Versions: []Version{}}
	err := s.db.InReadTx(ctx, func(q db.Querier) error {
		rows, err := q.Query(ctx, `SELECT `+versionColumns+` FROM `+versionFrom+`
			 WHERE v.kind = $1 AND ($2 OR a.version_id IS NULL)
			 ORDER BY lower(v.name), v.name, v.version DESC`, kind, includeArchived)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanVersion(rows)
			if err != nil {
				return err
			}
			t.Versions = append(t.Versions, *v)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		err = q.QueryRow(ctx, `SELECT version_id FROM fleet_instruction_defaults WHERE kind = $1`, kind).Scan(&t.DefaultID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("fleet instructions: load: %w", err)
	}
	return t, nil
}

func (s *PostgresStore) Get(ctx context.Context, id uuid.UUID) (*Version, error) {
	v, err := scanVersion(s.db.QueryRow(ctx, `SELECT `+versionColumns+` FROM `+versionFrom+` WHERE v.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("fleet instructions: get: %w", err)
	}
	return v, nil
}

func (s *PostgresStore) Default(ctx context.Context) (*Version, error) {
	return s.DefaultOf(ctx, KindFleet)
}

func (s *PostgresStore) DefaultOf(ctx context.Context, kind Kind) (*Version, error) {
	v, err := scanVersion(s.db.QueryRow(ctx, `
		SELECT `+versionColumns+`
		  FROM fleet_instruction_defaults d
		  JOIN `+versionFrom+` ON v.tenant_id = d.tenant_id AND v.id = d.version_id
		 WHERE d.kind = $1`, kind))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("fleet instructions: default: %w", err)
	}
	return v, nil
}

func (s *PostgresStore) Create(ctx context.Context, kind Kind, name, body string, by uuid.UUID, makeDefault bool) (*Version, *Version, error) {
	var created, previous *Version
	err := s.db.InTx(ctx, func(q db.Querier) error {
		// Two saves of one name must not both read the same highest
		// version: the lock makes the second wait and see the first.
		if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_tenant()::text || '/' || $1 || '/' || $2, 0))`, kind, name); err != nil {
			return err
		}
		var err error
		created, err = scanVersion(q.QueryRow(ctx, `
			INSERT INTO fleet_instruction_versions AS v (kind, name, version, body, created_by)
			SELECT $1, $2, coalesce(max(version), 0) + 1, $3, $4 FROM fleet_instruction_versions WHERE kind = $1 AND name = $2
			RETURNING v.id, v.name, v.version, v.kind, v.body, v.created_by, v.created_by_deleted, v.created_at, NULL::timestamptz`, kind, name, body, by))
		if err != nil {
			return err
		}
		if makeDefault {
			previous, err = setDefault(ctx, q, kind, &created.ID, by)
		}
		return err
	})
	if err != nil {
		return nil, nil, fmt.Errorf("fleet instructions: create: %w", err)
	}
	return created, previous, nil
}

func (s *PostgresStore) SetDefault(ctx context.Context, kind Kind, id *uuid.UUID, by uuid.UUID) (*Version, error) {
	var previous *Version
	err := s.db.InTx(ctx, func(q db.Querier) error {
		var err error
		previous, err = setDefault(ctx, q, kind, id, by)
		return err
	})
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("fleet instructions: set default: %w", err)
	}
	return previous, nil
}

// lockDefault serialises everything that decides what the default may be:
// setting it and archiving. Without it, an archive that has just checked
// "not the default" could commit after a set-default that has just checked
// "not archived", leaving the default archived. The default row alone cannot
// carry this, since there may be no row yet.
func lockDefault(ctx context.Context, q db.Querier) error {
	_, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_tenant()::text || '/fleet-default', 0))`)
	return err
}

func setDefault(ctx context.Context, q db.Querier, kind Kind, id *uuid.UUID, by uuid.UUID) (*Version, error) {
	if err := lockDefault(ctx, q); err != nil {
		return nil, err
	}
	previous, err := scanVersion(q.QueryRow(ctx, `
		SELECT `+versionColumns+`
		  FROM fleet_instruction_defaults d
		  JOIN `+versionFrom+` ON v.tenant_id = d.tenant_id AND v.id = d.version_id
		 WHERE d.kind = $1
		 FOR UPDATE OF d`, kind))
	if errors.Is(err, pgx.ErrNoRows) {
		previous, err = nil, nil
	}
	if err != nil {
		return nil, err
	}
	if id == nil {
		_, err := q.Exec(ctx, `DELETE FROM fleet_instruction_defaults WHERE kind = $1`, kind)
		return previous, err
	}
	// The foreign key also holds this, but a visible row first gives a
	// plain not-found rather than a constraint error.
	var exists, archived bool
	var versionKind Kind
	if err := q.QueryRow(ctx, `
		SELECT true, kind, EXISTS (SELECT 1 FROM fleet_instruction_archives WHERE version_id = $1)
		  FROM fleet_instruction_versions WHERE id = $1`, *id).Scan(&exists, &versionKind, &archived); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}
	if versionKind != kind {
		return nil, invalid("that version is a %s text, not a %s one", versionKind, kind)
	}
	if archived {
		return nil, conflict("that version is archived; unarchive it first")
	}
	_, err = q.Exec(ctx, `
		INSERT INTO fleet_instruction_defaults (kind, version_id, updated_by) VALUES ($1, $2, $3)
		ON CONFLICT (tenant_id, kind) DO UPDATE
		   SET version_id = EXCLUDED.version_id, updated_by = EXCLUDED.updated_by, updated_at = now()`, kind, *id, by)
	return previous, err
}

// archiveIDs archives the versions matching where under the default lock, so
// that the default check and the archive are one decision.
func (s *PostgresStore) archiveIDs(ctx context.Context, where string, args []any, by uuid.UUID) (int, *Version, error) {
	var n int
	var first *Version
	err := s.db.InTx(ctx, func(q db.Querier) error {
		if err := lockDefault(ctx, q); err != nil {
			return err
		}
		var ids []uuid.UUID
		rows, err := q.Query(ctx, `SELECT v.id FROM fleet_instruction_versions v WHERE `+where, args...)
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
		var isDefault bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM fleet_instruction_defaults WHERE version_id = ANY($1))`, ids).Scan(&isDefault); err != nil {
			return err
		}
		if isDefault {
			return conflict("the default version cannot be archived, nor a group holding it; choose another default or none first")
		}
		tag, err := q.Exec(ctx, `
			INSERT INTO fleet_instruction_archives (version_id, archived_by)
			SELECT unnest($1::uuid[]), $2 ON CONFLICT DO NOTHING`, ids, by)
		if err != nil {
			return err
		}
		n = int(tag.RowsAffected())
		if len(ids) == 1 {
			first, err = scanVersion(q.QueryRow(ctx, `SELECT `+versionColumns+` FROM `+versionFrom+` WHERE v.id = $1`, ids[0]))
		}
		return err
	})
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) {
		return 0, nil, err
	}
	if err != nil {
		return 0, nil, fmt.Errorf("fleet instructions: archive: %w", err)
	}
	return n, first, nil
}

func (s *PostgresStore) ArchiveVersion(ctx context.Context, id uuid.UUID, by uuid.UUID) (*Version, error) {
	_, v, err := s.archiveIDs(ctx, `v.id = $1`, []any{id}, by)
	return v, err
}

func (s *PostgresStore) ArchiveGroup(ctx context.Context, kind Kind, name string, by uuid.UUID) (int, error) {
	n, _, err := s.archiveIDs(ctx, `v.kind = $1 AND v.name = $2`, []any{kind, name}, by)
	return n, err
}

func (s *PostgresStore) unarchive(ctx context.Context, where string, args []any) (int, *Version, error) {
	var n int
	var first *Version
	err := s.db.InTx(ctx, func(q db.Querier) error {
		var count int
		if err := q.QueryRow(ctx, `SELECT count(*) FROM fleet_instruction_versions v WHERE `+where, args...).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return ErrNotFound
		}
		tag, err := q.Exec(ctx, `
			DELETE FROM fleet_instruction_archives
			 WHERE version_id IN (SELECT v.id FROM fleet_instruction_versions v WHERE `+where+`)`, args...)
		if err != nil {
			return err
		}
		n = int(tag.RowsAffected())
		if count == 1 {
			first, err = scanVersion(q.QueryRow(ctx, `SELECT `+versionColumns+` FROM `+versionFrom+` WHERE `+where, args...))
		}
		return err
	})
	if errors.Is(err, ErrNotFound) {
		return 0, nil, err
	}
	if err != nil {
		return 0, nil, fmt.Errorf("fleet instructions: unarchive: %w", err)
	}
	return n, first, nil
}

func (s *PostgresStore) UnarchiveVersion(ctx context.Context, id uuid.UUID) (*Version, error) {
	_, v, err := s.unarchive(ctx, `v.id = $1`, []any{id})
	return v, err
}

func (s *PostgresStore) UnarchiveGroup(ctx context.Context, kind Kind, name string) (int, error) {
	n, _, err := s.unarchive(ctx, `v.kind = $1 AND v.name = $2`, []any{kind, name})
	return n, err
}
