package harnessconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lowaa/slop-factory/go-db/pgerr"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// Store is the persistence Service needs. Every call runs in the
// context's tenant; row level security hides every other tenant's rows.
type Store interface {
	// Load returns every saved scope of the tenant.
	Load(ctx context.Context) (*Tenant, error)
	// Replace saves values as the scope's whole set under the tenant's
	// next revision, and returns what it replaced (nil when nothing had
	// been saved) and what it wrote. ErrAgentNotFound for an agent the
	// tenant does not have.
	Replace(ctx context.Context, agentID *uuid.UUID, values Values, by uuid.UUID) (before, after *Scope, err error)
	// Remove deletes an agent's override, so it runs on the defaults, and
	// returns what it removed (nil when it had none) and the revision the
	// removal took. Removing from an agent without an override is a no-op;
	// ErrAgentNotFound for an agent the tenant does not have.
	Remove(ctx context.Context, agentID uuid.UUID) (before *Scope, revision int64, err error)
}

type PostgresStore struct {
	db *db.DB
}

func NewPostgresStore(database *db.DB) *PostgresStore {
	return &PostgresStore{db: database}
}

var _ Store = (*PostgresStore)(nil)

const scopeColumns = `agent_id, settings, revision, updated_by, updated_by_deleted, updated_at`

func scanScope(row pgx.Row) (*Scope, error) {
	var s Scope
	var raw []byte
	if err := row.Scan(&s.AgentID, &raw, &s.Revision, &s.UpdatedBy, &s.UpdatedByDeleted, &s.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &s.Values); err != nil {
		return nil, fmt.Errorf("decode harness config: %w", err)
	}
	return &s, nil
}

func (s *PostgresStore) Load(ctx context.Context) (*Tenant, error) {
	rows, err := s.db.Query(ctx, `SELECT `+scopeColumns+` FROM harness_config`)
	if err != nil {
		return nil, fmt.Errorf("load harness config: %w", err)
	}
	defer rows.Close()

	t := &Tenant{Overrides: map[uuid.UUID]*Scope{}}
	for rows.Next() {
		scope, err := scanScope(rows)
		if err != nil {
			return nil, fmt.Errorf("scan harness config: %w", err)
		}
		if scope.AgentID == nil {
			t.Defaults = scope
		} else {
			t.Overrides[*scope.AgentID] = scope
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load harness config: %w", err)
	}
	return t, nil
}

// Replace takes the revision first: the counter row's lock is held until
// commit, so two saves in one tenant run one after the other and each
// reads, as "before", what the previous one wrote.
func (s *PostgresStore) Replace(ctx context.Context, agentID *uuid.UUID, values Values, by uuid.UUID) (*Scope, *Scope, error) {
	raw, err := json.Marshal(values)
	if err != nil {
		return nil, nil, fmt.Errorf("encode harness config: %w", err)
	}

	var before, after *Scope
	err = s.db.InTx(ctx, func(q db.Querier) error {
		revision, err := NextRevision(ctx, q)
		if err != nil {
			return err
		}

		found, err := scanScope(q.QueryRow(ctx,
			`SELECT `+scopeColumns+` FROM harness_config WHERE agent_id IS NOT DISTINCT FROM $1`, agentID))
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return fmt.Errorf("read harness config: %w", err)
		default:
			before = found
		}

		after, err = scanScope(q.QueryRow(ctx, `
			INSERT INTO harness_config (agent_id, settings, revision, updated_by)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (tenant_id, agent_id) DO UPDATE
			   SET settings = EXCLUDED.settings,
			       revision = EXCLUDED.revision,
			       updated_by = EXCLUDED.updated_by,
			       updated_by_deleted = false,
			       updated_at = now()
			RETURNING `+scopeColumns,
			agentID, raw, revision, by))
		if pgerr.IsForeignKeyViolation(err) {
			return ErrAgentNotFound
		}
		if err != nil {
			return fmt.Errorf("save harness config: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return before, after, nil
}

// Remove takes the revision first, like Replace, so the two take their locks
// in the same order and cannot deadlock. A removal that finds nothing still
// moves the counter, which is harmless: every agent's revision is derived from
// the scopes' own, never from the counter.
func (s *PostgresStore) Remove(ctx context.Context, agentID uuid.UUID) (*Scope, int64, error) {
	var before *Scope
	var revision int64
	err := s.db.InTx(ctx, func(q db.Querier) error {
		var err error
		if revision, err = NextRevision(ctx, q); err != nil {
			return err
		}
		found, err := scanScope(q.QueryRow(ctx,
			`DELETE FROM harness_config WHERE agent_id = $1 RETURNING `+scopeColumns, agentID))
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return fmt.Errorf("remove harness config: %w", err)
		default:
			before = found
			return nil
		}
		var exists bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM agents WHERE id = $1)`, agentID).Scan(&exists); err != nil {
			return fmt.Errorf("find agent: %w", err)
		}
		if !exists {
			return ErrAgentNotFound
		}
		return nil
	})
	return before, revision, err
}

// NextRevision takes the tenant's next config revision. The counter row's
// lock is held until the transaction ends, so changes that take a revision
// -- saving a scope, or moving an agent between variants -- run one after
// the other.
func NextRevision(ctx context.Context, q db.Querier) (int64, error) {
	var revision int64
	if err := q.QueryRow(ctx, `
		INSERT INTO harness_config_revisions (revision) VALUES (1)
		ON CONFLICT (tenant_id) DO UPDATE
		   SET revision = harness_config_revisions.revision + 1
		RETURNING revision`).Scan(&revision); err != nil {
		return 0, fmt.Errorf("next harness config revision: %w", err)
	}
	return revision, nil
}
