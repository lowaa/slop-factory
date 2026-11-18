package tenantstate_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
)

func TestArchivedAndActiveIDs(t *testing.T) {
	ctx := context.Background()
	database := dbtest.WithDB(t)
	live := dbtest.NewTenant(t, database)
	gone := dbtest.NewTenant(t, database)
	dbtest.ArchiveTenant(t, database, gone)

	archived, err := tenantstate.Archived(ctx, database.Global(), gone)
	require.NoError(t, err)
	require.True(t, archived)
	archived, err = tenantstate.Archived(ctx, database.Global(), live)
	require.NoError(t, err)
	require.False(t, archived)
	archived, err = tenantstate.Archived(ctx, database.Global(), uuid.New())
	require.NoError(t, err)
	require.False(t, archived, "a tenant with no row is not archived")

	ids, err := tenantstate.ActiveIDs(ctx, database.Global())
	require.NoError(t, err)
	require.Contains(t, ids, live)
	require.NotContains(t, ids, gone)
}

func TestEach_VisitsTheRosterInTenantContextsAndKeepsGoingAfterAFailure(t *testing.T) {
	ctx := context.Background()
	database := dbtest.WithDB(t)
	live := dbtest.NewTenant(t, database)
	gone := dbtest.NewTenant(t, database)
	dbtest.ArchiveTenant(t, database, gone)

	visit := func(roster tenantstate.Roster, failOn uuid.UUID) ([]uuid.UUID, error) {
		var seen []uuid.UUID
		err := tenantstate.Each(ctx, roster, func(ctx context.Context, tenant uuid.UUID) error {
			scoped, err := db.TenantFrom(ctx)
			require.NoError(t, err)
			require.Equal(t, tenant, scoped)
			seen = append(seen, tenant)
			if tenant == failOn {
				return errors.New("boom")
			}
			return nil
		})
		return seen, err
	}

	seen, err := visit(tenantstate.Active(database.Global()), uuid.Nil)
	require.NoError(t, err)
	require.Contains(t, seen, live)
	require.NotContains(t, seen, gone)

	seen, err = visit(tenantstate.All(database.Global()), live)
	require.ErrorContains(t, err, "boom")
	require.Contains(t, seen, live)
	require.Contains(t, seen, gone, "a failed tenant does not stop the rest, and All reaches archived ones")
}

func TestEach_StopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := tenantstate.Each(ctx, fixedRoster{uuid.New(), uuid.New()}, func(context.Context, uuid.UUID) error {
		calls++
		cancel()
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, calls)
}

type fixedRoster []uuid.UUID

func (r fixedRoster) Tenants(context.Context) ([]uuid.UUID, error) { return r, nil }
