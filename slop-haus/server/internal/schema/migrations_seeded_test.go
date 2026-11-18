package schema_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-db/migrations"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema/seeddata"
)

// step is what a migration's own checks get: the database just after it ran.
type step struct {
	ctx     context.Context
	rig     *dbtest.MigrationRig
	tenants []uuid.UUID
	version int64
}

// migrationChecks hold the assertions specific to one migration, run once
// it has been applied on top of the seeded rows.
var migrationChecks = map[int64]func(t *testing.T, st *step){}

// Every migration, applied to a database that holds rows -- several tenants'
// worth, in every table, as the app role wrote them -- and run as production
// runs them: by a role that owns the tables but is neither a superuser nor
// BYPASSRLS, so FORCE ROW LEVEL SECURITY binds it.
//
// Each migration's tests used to run on empty tables as a superuser, which
// is how a migration once broke production's deploy while passing everything: its
// backfill saw no rows under the policies, and SET NOT NULL then failed on
// the rows it could not see.
//
// The walk goes up one migration at a time, seeding every table that exists
// before each, so migration N meets the rows the schema of N-1 holds, plus
// everything seeded since 7 and carried through. Down migrations are not
// used to get to N-1: a down is lossy where it drops a column, absent where
// there is nothing to undo, and a test that rebuilds the state through them
// tests the downs.
func TestEveryMigrationRunsOverSeededMultiTenantData(t *testing.T) {
	ctx := context.Background()
	rig := dbtest.NewMigrationRig(t, schema.Migrations)
	tenants := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	sd := seeddata.NewSeeding(rig.App, seeddata.Seeders, tenants)

	var previous int64
	for round, version := range rig.Versions() {
		version, round := version, round
		passed := t.Run(fmt.Sprintf("%06d", version), func(t *testing.T) {
			if previous != 0 {
				require.NoError(t, sd.Round(ctx, previous, round), "seeding at version %d", previous)
			}
			before, err := seeddata.TakeSnapshot(ctx, rig.App, tenants)
			require.NoError(t, err)

			require.NoError(t, rig.MigrateTo(ctx, version), "migrating %d over the seeded rows", version)

			after, err := seeddata.TakeSnapshot(ctx, rig.App, tenants)
			require.NoError(t, err)
			assert.Empty(t, seeddata.Compare(before, after, seeddata.Notes[version]), "rows that existed before the migration")
			assertTenantsAreIsolated(t, ctx, rig, tenants)
			assert.Empty(t, migrations.CheckLiveTenantInvariants(ctx, globalQuerier{rig.Owner.Global()}))
			if check := migrationChecks[version]; check != nil {
				check(t, &step{ctx: ctx, rig: rig, tenants: tenants, version: version})
			}
		})
		// Later steps build on this one's database; after a failure they would
		// report a state nobody wrote.
		require.True(t, passed, "stopping at migration %d: the steps after it start from its result", version)
		previous = version
	}

	t.Run("the latest schema takes the seeders' rows", func(t *testing.T) {
		require.NoError(t, sd.Round(ctx, previous, len(rig.Versions())), "seeding at version %d", previous)
		assertTenantsAreIsolated(t, ctx, rig, tenants)
	})
	t.Run("no seeder is left over from a table that never existed", func(t *testing.T) {
		assert.Empty(t, sd.Unused())
	})
}

// userPrivateTables are tenant tables with a second, restrictive policy that
// admits only the transaction's user (app.current_user_id). A tenant-scoped
// count with no user set therefore sees nothing, which is what is asserted.
var userPrivateTables = map[string]bool{"user_tenant_settings": true}

// Row level security still scopes every tenant table after the migration: a
// tenant sees exactly its own rows -- as many as the superuser counts under
// its id -- a tenant with no data sees none, and no rows sit under an id
// nobody seeded.
func assertTenantsAreIsolated(t *testing.T, ctx context.Context, rig *dbtest.MigrationRig, tenants []uuid.UUID) {
	t.Helper()
	tables, err := seeddata.Tables(ctx, rig.Admin.Global())
	require.NoError(t, err)
	stranger := uuid.New()
	for _, table := range tables {
		if strings.HasPrefix(table, "global_") {
			continue
		}
		var total int
		require.NoError(t, rig.Admin.Global().QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&total))
		sum := 0
		for _, tenant := range tenants {
			var own, seen int
			require.NoError(t, rig.Admin.Global().QueryRow(ctx,
				"SELECT count(*) FROM "+table+" WHERE tenant_id = $1", tenant).Scan(&own))
			require.NoError(t, rig.App.QueryRow(db.WithTenant(ctx, tenant), "SELECT count(*) FROM "+table).Scan(&seen))
			if userPrivateTables[table] {
				assert.Zero(t, seen, "%s: tenant %s, with no user set, sees none of the users' rows", table, tenant)
			} else {
				assert.Equal(t, own, seen, "%s: tenant %s sees its rows and only its rows", table, tenant)
			}
			sum += own
		}
		assert.Equal(t, total, sum, "%s: every row belongs to a seeded tenant", table)
		var seen int
		require.NoError(t, rig.App.QueryRow(db.WithTenant(ctx, stranger), "SELECT count(*) FROM "+table).Scan(&seen))
		assert.Zero(t, seen, "%s: a tenant with no data sees none of the others'", table)
	}
}
