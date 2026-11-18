package prune

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/go-privacy/retention"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
)

type PostgresStore struct {
	db *db.DB
	// pendingActions owns its table's expiry rule; pruning only schedules it.
	pendingActions *pendingaction.PostgresStore
	sweeper        *retention.Sweeper
	tenantScopes   retention.Scopes
	globalScope    retention.Scopes
}

func NewPostgresStore(database *db.DB, logHandler slog.Handler) *PostgresStore {
	s := &PostgresStore{db: database, pendingActions: pendingaction.NewPostgresStore(database)}
	s.sweeper = retention.NewSweeper(&retention.Config{BatchSize: batchSize}, logHandler)
	s.tenantScopes = retention.NewTenantScopes(database.Engine(), s.Tenants)
	s.globalScope = retention.NewGlobalScope(database.Engine())
	return s
}

var _ Store = (*PostgresStore)(nil)

// Tenants lists every tenant, archived ones too, through the global handle.
func (s *PostgresStore) Tenants(ctx context.Context) ([]uuid.UUID, error) {
	return tenantstate.All(s.db.Global()).Tenants(ctx)
}

// Sweep deletes the rows rule has expired, in every tenant or, for a global
// rule, outside any, and returns how many.
func (s *PostgresStore) Sweep(ctx context.Context, rule *Rule) (int64, error) {
	scopes := s.tenantScopes
	if rule.Global {
		scopes = s.globalScope
	}
	res, err := s.sweeper.Sweep(ctx, &rule.Rule, scopes, nil)
	return res.Deleted, err
}

// ExpirePendingActions closes the context's tenant's proposals past their
// expiry.
func (s *PostgresStore) ExpirePendingActions(ctx context.Context, now time.Time) error {
	_, err := s.pendingActions.ExpireDue(ctx, now)
	return err
}
