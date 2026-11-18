package db_test

import (
	"context"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
)

func TestMigrate_UpCreatesTheSchema(t *testing.T) {
	database := dbtest.WithEmptyDB(t)
	ctx := context.Background()

	require.NoError(t, db.Migrate(ctx, database, schema.Migrations))

	version, err := db.Version(ctx, database, schema.Migrations)
	require.NoError(t, err)
	assert.Positive(t, version)

	for _, table := range []string{"global_users", "global_tenants", "global_tenant_memberships",
		"global_auth_tokens", "global_oauth_clients", "global_oauth_grants",
		"global_oauth_auth_requests", "global_oauth_codes", "global_oauth_device_codes",
		"global_oauth_tokens", "agents", "tickets", "ticket_paths",
		"ticket_deps", "ticket_comments", "agent_contexts", "leases", "locks",
		"sessions", "tenant_settings"} {
		var exists bool
		require.NoError(t, database.Global().QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables
			  WHERE table_schema = 'public' AND table_name = $1)`, table).Scan(&exists))
		assert.True(t, exists, "table %s should exist after migrating up", table)
	}

	var oldSettings bool
	require.NoError(t, database.Global().QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables
		  WHERE table_schema = 'public' AND table_name = 'settings')`).Scan(&oldSettings))
	assert.False(t, oldSettings, "the settings table was replaced by tenant_settings and dropped")
}

// A down migration that is written but never executed is a down migration
// that does not work. This is the only thing that proves otherwise.
//
// It walks down one migration at a time to the baseline rather than
// asserting a fixed number of steps -- MigrateDown is a single step, and an
// earlier version of this test had to be rewritten each time a migration
// was added, which is how a test stops being run rather than stops being
// true. The baseline itself refuses to go down (see
// TestMigrate_TheBaselineRefusesToRollBack).
func TestMigrate_DownRevertsEveryMigrationAfterTheBaseline(t *testing.T) {
	database := dbtest.WithEmptyDB(t)
	ctx := context.Background()

	require.NoError(t, db.Migrate(ctx, database, schema.Migrations))

	appTables := func() []string {
		rows, err := database.Global().Query(ctx, `
			SELECT table_name FROM information_schema.tables
			 WHERE table_schema = 'public'
			   AND table_name <> 'goose_db_version'
			 ORDER BY table_name`)
		require.NoError(t, err)
		defer rows.Close()

		names := []string{}
		for rows.Next() {
			var name string
			require.NoError(t, rows.Scan(&name))
			names = append(names, name)
		}
		require.NoError(t, rows.Err())
		return names
	}

	require.NotEmpty(t, appTables(), "migrating up should have created tables")
	// Walking down from the latest version ends on the baseline's own tables.
	baselineTables := tablesAtTheBaseline(t)

	for step := 0; ; step++ {
		version, err := db.Version(ctx, database, schema.Migrations)
		require.NoError(t, err)
		if version == baselineVersion {
			break
		}
		// Only stops a down that never reaches zero; well above the count of
		// migrations, which a fixed 50 once fell behind.
		require.Less(t, step, 500, "down migrations are not converging")
		require.NoError(t, db.MigrateDown(ctx, database, schema.Migrations),
			"down failed at version %d", version)
	}

	assert.Equal(t, baselineTables, appTables(), "every down should have dropped what its up created")

	// And the whole stack goes back up afterwards -- a down that leaves
	// something behind usually shows up as the next up failing.
	require.NoError(t, db.Migrate(ctx, database, schema.Migrations))
	assert.NotEmpty(t, appTables())
}

// Rolling the baseline back would drop every table, so a stray down on a
// live database fails instead, and leaves the schema as it was.
func TestMigrate_TheBaselineRefusesToRollBack(t *testing.T) {
	database := dbtest.WithEmptyDB(t)
	ctx := context.Background()
	baselineOnly := fstest.MapFS{}
	for _, name := range []string{"000172_baseline.sql"} {
		data, err := fs.ReadFile(schema.Migrations, name)
		require.NoError(t, err)
		baselineOnly[name] = &fstest.MapFile{Data: data}
	}
	require.NoError(t, db.Migrate(ctx, database, baselineOnly))

	err := db.MigrateDown(ctx, database, baselineOnly)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be rolled back")

	version, err := db.Version(ctx, database, baselineOnly)
	require.NoError(t, err)
	assert.EqualValues(t, baselineVersion, version)
	var tickets bool
	require.NoError(t, database.Global().QueryRow(ctx, `SELECT to_regclass('tickets') IS NOT NULL`).Scan(&tickets))
	assert.True(t, tickets, "the schema is still there")
}

// A database built by the step-by-step migrations the baseline replaced
// records version 172 already: migrating it is a no-op, so a deployment
// carries on without rebuilding. Stood in for by one migration at that
// version that creates nothing, so any table found afterwards came from the
// baseline running when it must not.
func TestMigrate_LeavesADatabaseAtTheBaselineAlone(t *testing.T) {
	database := dbtest.WithEmptyDB(t)
	ctx := context.Background()
	history := fstest.MapFS{
		"000171_replaced.sql": {Data: []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n")},
		"000172_replaced.sql": {Data: []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n")},
	}
	require.NoError(t, db.Migrate(ctx, database, history))

	// The baseline alone: the steps after it would run over the stand-in,
	// which has none of the schema they build on.
	baselineOnly := fstest.MapFS{}
	data, err := fs.ReadFile(schema.Migrations, "000172_baseline.sql")
	require.NoError(t, err)
	baselineOnly["000172_baseline.sql"] = &fstest.MapFile{Data: data}
	require.NoError(t, db.Migrate(ctx, database, baselineOnly))

	version, err := db.Version(ctx, database, baselineOnly)
	require.NoError(t, err)
	assert.EqualValues(t, baselineVersion, version)
	var tickets bool
	require.NoError(t, database.Global().QueryRow(ctx, `SELECT to_regclass('tickets') IS NOT NULL`).Scan(&tickets))
	assert.False(t, tickets, "the baseline did not run over a database that had already reached it")
}

// A session's stuck reason and time move together: a reason with no time, or
// the reverse, would leave the Agents screen with half a notice.
func TestMigrate_StuckColumnsMoveTogether(t *testing.T) {
	database := dbtest.WithEmptyDB(t)
	ctx := context.Background()
	require.NoError(t, db.Migrate(ctx, database, schema.Migrations))

	tenant := db.WithTenant(ctx, uuid.New())
	agentID := uuid.New()
	_, err := database.Exec(tenant, `INSERT INTO agents (id, name) VALUES ($1, 'builder-1')`, agentID)
	require.NoError(t, err)
	_, err = database.Exec(tenant, `INSERT INTO sessions (agent_id, claude_session_id) VALUES ($1, 'run')`, agentID)
	require.NoError(t, err)

	for name, set := range map[string]string{
		"a reason alone": "stuck_reason = 'x'",
		"a time alone":   "stuck_at = now()",
	} {
		_, err := database.Exec(tenant, `UPDATE sessions SET `+set+` WHERE agent_id = $1`, agentID)
		assert.Error(t, err, name)
	}
	_, err = database.Exec(tenant, `UPDATE sessions SET stuck_reason = 'x', stuck_at = now() WHERE agent_id = $1`, agentID)
	assert.NoError(t, err, "both together")
}

func TestMigrate_IsIdempotent(t *testing.T) {
	database := dbtest.WithEmptyDB(t)
	ctx := context.Background()

	require.NoError(t, db.Migrate(ctx, database, schema.Migrations))
	before, err := db.Version(ctx, database, schema.Migrations)
	require.NoError(t, err)

	// cmd/api migrates on every boot, so a re-run against an
	// already-migrated database has to be a no-op rather than an error.
	require.NoError(t, db.Migrate(ctx, database, schema.Migrations))

	after, err := db.Version(ctx, database, schema.Migrations)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

// A database whose history stops short of the baseline must fail to
// migrate, not start healthy on half a schema. Its history is stood in for
// by versions 1 and 6, one of them making one of the baseline's tables: the
// baseline is attempted and collides with it.
func TestMigrate_RefusesADatabaseFromTheRetiredHistory(t *testing.T) {
	database := dbtest.WithEmptyDB(t)
	ctx := context.Background()
	retired := fstest.MapFS{
		"000001_retired.sql": {Data: []byte("-- +goose Up\nCREATE TABLE agents (id uuid PRIMARY KEY);\n-- +goose Down\nDROP TABLE agents;\n")},
		"000006_retired.sql": {Data: []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n")},
	}
	require.NoError(t, db.Migrate(ctx, database, retired))

	err := db.Migrate(ctx, database, schema.Migrations)
	require.Error(t, err)
	// Refused for the reason that protects it: the baseline is attempted and
	// collides with the tables already there -- not some unrelated failure
	// that would pass this test just as well.
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	assert.Equal(t, "42P07", pgErr.Code, "duplicate_table: %s", pgErr.Message)
}

// baselineVersion is 000172_baseline's: the oldest version a database can be
// migrated down to.
const baselineVersion = 172

// tablesAtTheBaseline lists the tables a database built from the baseline
// alone holds, for comparing a walk down to it.
func tablesAtTheBaseline(t *testing.T) []string {
	t.Helper()
	ctx := context.Background()
	database := dbtest.WithEmptyDB(t)
	data, err := fs.ReadFile(schema.Migrations, "000172_baseline.sql")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(ctx, database, fstest.MapFS{"000172_baseline.sql": {Data: data}}))
	rows, err := database.Global().Query(ctx, `
		SELECT table_name FROM information_schema.tables
		 WHERE table_schema = 'public' AND table_name <> 'goose_db_version'
		 ORDER BY table_name`)
	require.NoError(t, err)
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		names = append(names, name)
	}
	require.NoError(t, rows.Err())
	return names
}
