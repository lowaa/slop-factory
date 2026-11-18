package usersettings

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	gosettings "github.com/lowaa/slop-factory/go-settings"
	"github.com/lowaa/slop-factory/go-settings/pgstore"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// ErrNotMember means the user has no membership in the tenant, so they have
// no tenant settings to write.
var ErrNotMember = errors.New("usersettings: not a member of this tenant")

// Change is one row of a tenant setting's history.
type Change = gosettings.Change

// TenantStore is the persistence TenantService needs. Every method acts for
// one user in the context's tenant.
type TenantStore interface {
	Append(ctx context.Context, userID uuid.UUID, name string, v Value) (*Stored, error)
	// Latest returns the newest row of each of the given names written since
	// the user's current membership began; a name with none is absent.
	Latest(ctx context.Context, userID uuid.UUID, names []string) ([]Stored, error)
	// History returns the user's rows for a name, newest first, those with an
	// id below before (0 for the newest), at most limit.
	History(ctx context.Context, userID uuid.UUID, name string, before int64, limit int) ([]Change, error)
}

// settingUserKey is the transaction setting user_tenant_settings' restrictive
// policy reads (user_isolation).
const settingUserKey = "app.current_user_id"

// PostgresTenantStore reads and writes user_tenant_settings. Row-level
// security keeps a query to the context's tenant and, through
// settingUserKey, to one user, so a statement that forgets its user filter
// still cannot see another user's rows. Every statement here names both
// anyway.
type PostgresTenantStore struct {
	db    *db.DB
	table *pgstore.Table
}

func NewPostgresTenantStore(database *db.DB) *PostgresTenantStore {
	return &PostgresTenantStore{db: database, table: pgstore.UserTenantTable("user_tenant_settings", &pgstore.Membership{Table: "global_tenant_memberships"})}
}

var _ TenantStore = (*PostgresTenantStore)(nil)

// asUser scopes the transaction to the user. The setting is transaction-local,
// so a pooled connection never carries one user into the next request.
func asUser(ctx context.Context, q db.Querier, userID uuid.UUID) error {
	if userID == uuid.Nil {
		return errors.New("usersettings: no user")
	}
	if _, err := q.Exec(ctx, `SELECT set_config($1, $2, true)`, settingUserKey, userID.String()); err != nil {
		return fmt.Errorf("usersettings: scope to user: %w", err)
	}
	return nil
}

// requireMember refuses a user with no current membership. The access token
// is stateless, so someone removed from the tenant can still reach these
// routes until it expires; their rows stay as history but are not served.
func requireMember(ctx context.Context, q db.Querier, userID uuid.UUID) error {
	var ok bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM global_tenant_memberships WHERE tenant = current_tenant() AND user_id = $1)`, userID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrNotMember
	}
	return nil
}

// Append refuses a user with no membership rather than writing a row the
// membership rule would never read.
func (s *PostgresTenantStore) Append(ctx context.Context, userID uuid.UUID, name string, v Value) (*Stored, error) {
	var row *Stored
	err := s.db.InTx(ctx, func(q db.Querier) error {
		if err := asUser(ctx, q, userID); err != nil {
			return err
		}
		var err error
		row, err = s.table.Append(ctx, q, userID, name, v)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotMember
	}
	if err != nil {
		return nil, fmt.Errorf("usersettings: %w", err)
	}
	return row, nil
}

func (s *PostgresTenantStore) Latest(ctx context.Context, userID uuid.UUID, names []string) ([]Stored, error) {
	var out []Stored
	err := s.db.InReadTx(ctx, func(q db.Querier) error {
		if err := asUser(ctx, q, userID); err != nil {
			return err
		}
		if err := requireMember(ctx, q, userID); err != nil {
			return err
		}
		var err error
		out, err = s.table.Latest(ctx, q, userID, names)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("usersettings: %w", err)
	}
	return out, nil
}

func (s *PostgresTenantStore) History(ctx context.Context, userID uuid.UUID, name string, before int64, limit int) ([]Change, error) {
	var out []Change
	err := s.db.InReadTx(ctx, func(q db.Querier) error {
		if err := asUser(ctx, q, userID); err != nil {
			return err
		}
		if err := requireMember(ctx, q, userID); err != nil {
			return err
		}
		var err error
		out, err = s.table.History(ctx, q, userID, name, before, limit)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("usersettings: %w", err)
	}
	return out, nil
}
