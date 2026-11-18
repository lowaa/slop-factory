package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema/seeddata"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func TestMain(m *testing.M) {
	dbtest.Main(m, schema.Migrations)
}

// upgradeVersion is the synthetic migration the tests upgrade across, one
// step past the newest real one (latestVersion), so the tests hold however
// many real steps have landed.
var (
	latestVersion  = newestMigration()
	upgradeVersion = latestVersion + 1
)

func newestMigration() int64 {
	versions, err := dbtest.MigrationVersions(schema.Migrations)
	if err != nil || len(versions) == 0 {
		panic(fmt.Sprintf("read the migrations: %v", err))
	}
	return versions[len(versions)-1]
}

const addsAColumn = `-- +goose Up
ALTER TABLE tickets ADD COLUMN upgrade_test_note text;
-- +goose Down
ALTER TABLE tickets DROP COLUMN upgrade_test_note;
`

// withUpgrade is the real migrations plus one synthetic step after them.
func withUpgrade(t *testing.T, step string) fs.FS {
	t.Helper()
	fsys := fstest.MapFS{}
	names, err := fs.Glob(schema.Migrations, "*")
	require.NoError(t, err)
	for _, name := range names {
		data, err := fs.ReadFile(schema.Migrations, name)
		require.NoError(t, err)
		fsys[name] = &fstest.MapFile{Data: data}
	}
	fsys[fmt.Sprintf("%06d_upgrade_test.sql", upgradeVersion)] = &fstest.MapFile{Data: []byte(step)}
	return fsys
}

// upgradeRig is a database at the newest real migration, about to take step.
func upgradeRig(t *testing.T, step string) *dbtest.MigrationRig {
	t.Helper()
	rig := dbtest.NewMigrationRig(t, withUpgrade(t, step))
	require.NoError(t, rig.MigrateTo(context.Background(), latestVersion))
	role, password := dbtest.AppCredentials()
	t.Setenv("DATABASE_URL", rig.OwnerDSN)
	t.Setenv("APP_DB_ROLE", role)
	t.Setenv("APP_DB_PASSWORD", password)
	return rig
}

func runTool(ctx context.Context, stdin string, args ...string) (stdout, stderr string, err error) {
	var out, errOut bytes.Buffer
	err = run(ctx, args, strings.NewReader(stdin), &out, &errOut)
	return out.String(), errOut.String(), err
}

// Rows written at one schema version are found again at the next, which
// is what an upgrade test asks of a real upgrade.
func TestSeededRowsSurviveAnUpgrade(t *testing.T) {
	ctx := context.Background()
	rig := upgradeRig(t, addsAColumn)

	state, _, err := runTool(ctx, "", "seed")
	require.NoError(t, err)
	require.NoError(t, rig.MigrateTo(ctx, upgradeVersion))

	_, stderr, err := runTool(ctx, state, "check")
	require.NoError(t, err)
	assert.Contains(t, stderr, fmt.Sprintf("read back at %d", upgradeVersion))
}

// Rows the app wrote that the upgraded database no longer holds.
func TestACheckFailsWhenTheUpgradeLostARow(t *testing.T) {
	ctx := context.Background()
	rig := upgradeRig(t, addsAColumn)

	state, _, err := runTool(ctx, "", "seed")
	require.NoError(t, err)
	require.NoError(t, rig.MigrateTo(ctx, upgradeVersion))
	_, err = rig.Admin.Global().Exec(ctx, `DELETE FROM agent_messages`)
	require.NoError(t, err)

	_, _, err = runTool(ctx, state, "check")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent_messages rows of tenant")
}

// A migration that drops a table loses its rows on purpose, and says so in
// seeddata.Notes; without the note the check takes it for lost data.
func TestAMigrationThatDropsTablesNeedsItsNote(t *testing.T) {
	const dropsATable = "-- +goose Up\nDROP TABLE ignored_merges;\n-- +goose Down\nSELECT 1;\n"
	for name, noted := range map[string]bool{"noted": true, "not noted": false} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			rig := upgradeRig(t, dropsATable)
			if noted {
				seeddata.Notes[upgradeVersion] = seeddata.Note{DroppedTables: []string{"ignored_merges"}}
				t.Cleanup(func() { delete(seeddata.Notes, upgradeVersion) })
			}

			state, _, err := runTool(ctx, "", "seed")
			require.NoError(t, err)
			require.NoError(t, rig.MigrateTo(ctx, upgradeVersion))

			_, _, err = runTool(ctx, state, "check")
			if noted {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "did not survive the upgrade")
				assert.Contains(t, err.Error(), "ignored_merges")
			}
		})
	}
}

func TestTheToolNeedsItsDatabaseSettings(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	_, _, err := runTool(context.Background(), "", "seed")
	assert.ErrorContains(t, err, "usage")
	_, _, err = runTool(context.Background(), "", "wipe")
	assert.ErrorContains(t, err, "usage")
}

// The api's landing runner registers each tenant's server agent on its first
// tick after an upgrade; the seeded state already holds it, so the check does
// not see that registration as a changed row.
func TestTheServerAgentTheRunnerRegistersIsNotAChangedRow(t *testing.T) {
	ctx := context.Background()
	rig := upgradeRig(t, addsAColumn)

	state, _, err := runTool(ctx, "", "seed")
	require.NoError(t, err)
	var seeded struct{ Tenants []uuid.UUID }
	require.NoError(t, json.Unmarshal([]byte(state), &seeded))
	app, err := openApp()
	require.NoError(t, err)
	defer app.Close()
	store := ticket.NewPostgresStore(app)
	for _, tenant := range seeded.Tenants {
		_, err := store.EnsureServerAgent(db.WithTenant(ctx, tenant))
		require.NoError(t, err)
	}
	require.NoError(t, rig.MigrateTo(ctx, upgradeVersion))

	_, _, err = runTool(ctx, state, "check")
	require.NoError(t, err)
}
