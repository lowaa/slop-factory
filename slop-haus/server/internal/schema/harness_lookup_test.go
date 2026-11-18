package schema_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/harness"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
)

func registeredHarnesses(t *testing.T, ctx context.Context, rig *dbtest.MigrationRig) []string {
	t.Helper()
	rows, err := rig.Admin.Global().Query(ctx, `SELECT name FROM global_harnesses ORDER BY name`)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		out = append(out, n)
	}
	require.NoError(t, rows.Err())
	return out
}

// The migration seeds what the dropped CHECKs allowed, and Sync is what adds
// the rest of the registry, so a new harness needs no migration. Both are
// needed: the release before this one runs against the seed alone.
func TestHarnessLookupHoldsTheRegistryAfterSync(t *testing.T) {
	ctx := context.Background()
	rig := dbtest.NewMigrationRig(t, schema.Migrations)
	require.NoError(t, rig.MigrateTo(ctx, baselineVersion))
	assert.Equal(t, []string{"claude", "pi"}, registeredHarnesses(t, ctx, rig), "the migration's own seed")

	_, err := rig.Admin.Global().Exec(ctx, `DELETE FROM global_harnesses WHERE name = 'pi'`)
	require.NoError(t, err)
	require.NoError(t, harness.Sync(ctx, rig.Admin.Global()))
	require.NoError(t, harness.Sync(ctx, rig.Admin.Global()), "idempotent")

	registered := registeredHarnesses(t, ctx, rig)
	for _, n := range harness.Names() {
		assert.Contains(t, registered, n)
	}
}

// The columns are held to the table by foreign keys, not CHECK lists.
func TestHarnessColumnsReferenceTheLookup(t *testing.T) {
	ctx := context.Background()
	rig := dbtest.NewMigrationRig(t, schema.Migrations)
	require.NoError(t, rig.MigrateTo(ctx, baselineVersion))
	for _, table := range []string{"variant_versions", "attempts"} {
		var n int
		require.NoError(t, rig.Admin.Global().QueryRow(ctx, `
			SELECT count(*) FROM pg_constraint
			 WHERE conrelid = $1::regclass AND contype = 'f' AND confrelid = 'global_harnesses'::regclass`, table).Scan(&n))
		assert.Equal(t, 1, n, table)
		require.NoError(t, rig.Admin.Global().QueryRow(ctx, `
			SELECT count(*) FROM pg_constraint
			 WHERE conrelid = $1::regclass AND contype = 'c' AND pg_get_constraintdef(oid) LIKE '%harness IN%'`, table).Scan(&n))
		assert.Zero(t, n, "%s: the CHECK list is gone", table)
	}
}
