package evalset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/go-db/pgerr"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// Store keeps the tenant's eval sets. Every call runs in the context's
// tenant; row level security hides every other tenant's rows.
type Store interface {
	List(ctx context.Context) ([]Set, error)
	// Get is ErrNotFound for a set the tenant does not have.
	Get(ctx context.Context, id uuid.UUID) (*Set, error)
	// Create is ErrNameTaken for a name the tenant already uses.
	Create(ctx context.Context, def *Definition, by uuid.UUID) (*Set, error)
	// Replace overwrites the set's whole definition: ErrNotFound, ErrNameTaken.
	// When unchangedSince is set the write happens only if the set's
	// updated_at is still that, else ErrChanged: a caller that derived the
	// definition from an earlier read does not overwrite a later edit.
	Replace(ctx context.Context, id uuid.UUID, def *Definition, unchangedSince *time.Time) (*Set, error)
	// Delete removes the set and its history: ErrNotFound.
	Delete(ctx context.Context, id uuid.UUID) error
	// RecordBatch writes one replay batch into the set's history.
	RecordBatch(ctx context.Context, b *Batch) (*Batch, error)
	// Batches is the set's history, newest first: ErrNotFound for an unknown set.
	Batches(ctx context.Context, setID uuid.UUID) ([]Batch, error)
}

type PostgresStore struct {
	db *db.DB
}

func NewPostgresStore(database *db.DB) *PostgresStore { return &PostgresStore{db: database} }

var _ Store = (*PostgresStore)(nil)

const setColumns = `id, name, description, filter, ticket_ids, created_by, created_at, updated_at`

func scanSet(row pgx.Row) (*Set, error) {
	var s Set
	var filter []byte
	if err := row.Scan(&s.ID, &s.Name, &s.Description, &filter, &s.TicketIDs, &s.CreatedBy, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	if filter != nil {
		s.Filter = &Filter{}
		if err := json.Unmarshal(filter, s.Filter); err != nil {
			return nil, fmt.Errorf("filter of eval set %s: %w", s.ID, err)
		}
	}
	return &s, nil
}

// filterJSON is the column value of a definition's filter: SQL NULL for a
// pinned set, which the table's check requires.
func filterJSON(def *Definition) ([]byte, error) {
	if def.Filter == nil {
		return nil, nil
	}
	return json.Marshal(def.Filter)
}

func (s *PostgresStore) List(ctx context.Context) ([]Set, error) {
	out := []Set{}
	err := s.db.InReadTx(ctx, func(q db.Querier) error {
		rows, err := q.Query(ctx, `SELECT `+setColumns+` FROM eval_sets ORDER BY lower(name), name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			set, err := scanSet(rows)
			if err != nil {
				return err
			}
			out = append(out, *set)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("eval sets: list: %w", err)
	}
	return out, nil
}

func (s *PostgresStore) Get(ctx context.Context, id uuid.UUID) (*Set, error) {
	var set *Set
	err := s.db.InReadTx(ctx, func(q db.Querier) error {
		var err error
		set, err = scanSet(q.QueryRow(ctx, `SELECT `+setColumns+` FROM eval_sets WHERE id = $1`, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("eval sets: get: %w", err)
	}
	return set, nil
}

func (s *PostgresStore) Create(ctx context.Context, def *Definition, by uuid.UUID) (*Set, error) {
	filter, err := filterJSON(def)
	if err != nil {
		return nil, err
	}
	var set *Set
	err = s.db.InTx(ctx, func(q db.Querier) error {
		var err error
		set, err = scanSet(q.QueryRow(ctx, `
			INSERT INTO eval_sets (name, description, filter, ticket_ids, created_by)
			VALUES ($1, $2, $3, $4, $5) RETURNING `+setColumns,
			def.Name, def.Description, filter, def.TicketIDs, by))
		return err
	})
	if pgerr.IsUniqueViolation(err) {
		return nil, ErrNameTaken
	}
	if err != nil {
		return nil, fmt.Errorf("eval sets: create: %w", err)
	}
	return set, nil
}

func (s *PostgresStore) Replace(ctx context.Context, id uuid.UUID, def *Definition, unchangedSince *time.Time) (*Set, error) {
	filter, err := filterJSON(def)
	if err != nil {
		return nil, err
	}
	var set *Set
	err = s.db.InTx(ctx, func(q db.Querier) error {
		var err error
		set, err = scanSet(q.QueryRow(ctx, `
			UPDATE eval_sets SET name = $2, description = $3, filter = $4, ticket_ids = $5, updated_at = now()
			WHERE id = $1 AND ($6::timestamptz IS NULL OR updated_at = $6) RETURNING `+setColumns,
			id, def.Name, def.Description, filter, def.TicketIDs, unchangedSince))
		if errors.Is(err, pgx.ErrNoRows) {
			// Told apart from a missing set inside the transaction, so a
			// delete between the two cannot make it read as a change.
			var exists bool
			if qerr := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM eval_sets WHERE id = $1)`, id).Scan(&exists); qerr != nil {
				return qerr
			}
			if exists {
				return ErrChanged
			}
		}
		return err
	})
	if errors.Is(err, ErrChanged) {
		return nil, err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if pgerr.IsUniqueViolation(err) {
		return nil, ErrNameTaken
	}
	if err != nil {
		return nil, fmt.Errorf("eval sets: replace: %w", err)
	}
	return set, nil
}

func (s *PostgresStore) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM eval_sets WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("eval sets: delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) RecordBatch(ctx context.Context, b *Batch) (*Batch, error) {
	var out Batch
	err := s.db.InTx(ctx, func(q db.Querier) error {
		return q.QueryRow(ctx, `
			INSERT INTO eval_set_batches (eval_set_id, batch_id, version_ids, runs, ticket_count, created_by)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id, eval_set_id, batch_id, version_ids, runs, ticket_count, created_by, created_at`,
			b.SetID, b.BatchID, b.VersionIDs, b.Runs, b.TicketCount, b.CreatedBy).
			Scan(&out.ID, &out.SetID, &out.BatchID, &out.VersionIDs, &out.Runs, &out.TicketCount, &out.CreatedBy, &out.CreatedAt)
	})
	if err != nil {
		return nil, fmt.Errorf("eval sets: record batch: %w", err)
	}
	return &out, nil
}

func (s *PostgresStore) Batches(ctx context.Context, setID uuid.UUID) ([]Batch, error) {
	out := []Batch{}
	err := s.db.InReadTx(ctx, func(q db.Querier) error {
		var exists bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM eval_sets WHERE id = $1)`, setID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		rows, err := q.Query(ctx, `
			SELECT id, eval_set_id, batch_id, version_ids, runs, ticket_count, created_by, created_at
			FROM eval_set_batches WHERE eval_set_id = $1 ORDER BY created_at DESC, id`, setID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b Batch
			if err := rows.Scan(&b.ID, &b.SetID, &b.BatchID, &b.VersionIDs, &b.Runs, &b.TicketCount, &b.CreatedBy, &b.CreatedAt); err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	if errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("eval sets: batches: %w", err)
	}
	return out, nil
}
