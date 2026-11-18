package usersettings

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	gosettings "github.com/lowaa/slop-factory/go-settings"
	"github.com/lowaa/slop-factory/go-settings/pgstore"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// Stored is one row of the log.
type Stored = gosettings.Stored

// Store is the persistence Service needs.
type Store interface {
	Append(ctx context.Context, userID uuid.UUID, name string, v Value) (*Stored, error)
	// Latest returns the newest row of each of the given names that the
	// user has written; a name with no row is absent.
	Latest(ctx context.Context, userID uuid.UUID, names []string) ([]Stored, error)
}

// PostgresStore reads and writes global_user_settings. The table has no
// tenant and no RLS, so every query here names the user itself: that user_id
// predicate is the only thing keeping one user's rows from another.
type PostgresStore struct {
	db    *db.DB
	table *pgstore.Table
}

func NewPostgresStore(database *db.DB) *PostgresStore {
	return &PostgresStore{db: database, table: pgstore.GlobalUserTable("global_user_settings")}
}

var _ Store = (*PostgresStore)(nil)

func (s *PostgresStore) Append(ctx context.Context, userID uuid.UUID, name string, v Value) (*Stored, error) {
	row, err := s.table.Append(ctx, s.db.Global(), userID, name, v)
	if err != nil {
		return nil, fmt.Errorf("usersettings: %w", err)
	}
	return row, nil
}

func (s *PostgresStore) Latest(ctx context.Context, userID uuid.UUID, names []string) ([]Stored, error) {
	out, err := s.table.Latest(ctx, s.db.Global(), userID, names)
	if err != nil {
		return nil, fmt.Errorf("usersettings: %w", err)
	}
	return out, nil
}
