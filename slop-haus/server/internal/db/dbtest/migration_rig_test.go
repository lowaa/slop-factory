package dbtest_test

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
)

const createNotes = `-- +goose Up
CREATE FUNCTION current_tenant() RETURNS uuid LANGUAGE sql STABLE AS $$
    SELECT NULLIF(current_setting('app.current_tenant', true), '')::uuid
$$;
CREATE TABLE notes (
    tenant_id uuid NOT NULL DEFAULT current_tenant(),
    id int NOT NULL,
    body text NOT NULL,
    PRIMARY KEY (tenant_id, id)
);
ALTER TABLE notes ENABLE ROW LEVEL SECURITY;
ALTER TABLE notes FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON notes USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant());
-- +goose Down
DROP TABLE notes;
`

// The shape of a backfill that once broke a deploy: across every tenant, written as if it ran with
// no row level security.
const backfillNotes = `-- +goose Up
ALTER TABLE notes ADD COLUMN shout text;
UPDATE notes SET shout = upper(body);
-- +goose Down
ALTER TABLE notes DROP COLUMN shout;
`

var rigMigrations = fstest.MapFS{
	"000001_notes.sql":    {Data: []byte(createNotes)},
	"000002_backfill.sql": {Data: []byte(backfillNotes)},
}

func TestMain(m *testing.M) { dbtest.Main(m, rigMigrations) }

func TestTheRigMigratesOneVersionAtATime(t *testing.T) {
	ctx := context.Background()
	rig := dbtest.NewMigrationRig(t, rigMigrations)
	assert.Equal(t, []int64{1, 2}, rig.Versions())

	require.NoError(t, rig.MigrateTo(ctx, 1))
	version, err := db.Version(ctx, rig.Owner, rigMigrations)
	require.NoError(t, err)
	assert.Equal(t, int64(1), version)
	var hasColumn bool
	require.NoError(t, rig.Admin.Global().QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'notes' AND column_name = 'shout')`).Scan(&hasColumn))
	assert.False(t, hasColumn, "migration 2 has not run")

	require.NoError(t, rig.MigrateTo(ctx, 2))
	require.NoError(t, rig.Admin.Global().QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'notes' AND column_name = 'shout')`).Scan(&hasColumn))
	assert.True(t, hasColumn)
}

// What the rig is for: the migrating role is bound by FORCE ROW LEVEL
// SECURITY, as production's is, so a cross-tenant backfill written without
// regard to it fills nothing -- where the container's superuser, which the
// other tests migrate as, would fill every row and pass.
func TestTheMigratingRoleIsBoundByRowLevelSecurity(t *testing.T) {
	ctx := context.Background()
	rig := dbtest.NewMigrationRig(t, rigMigrations)
	require.NoError(t, rig.MigrateTo(ctx, 1))
	for i := 0; i < 2; i++ {
		_, err := rig.App.Exec(db.WithTenant(ctx, uuid.New()), `INSERT INTO notes (id, body) VALUES (1, 'hello')`)
		require.NoError(t, err)
	}

	require.NoError(t, rig.MigrateTo(ctx, 2))

	var rows, filled int
	require.NoError(t, rig.Admin.Global().QueryRow(ctx, `SELECT count(*), count(shout) FROM notes`).Scan(&rows, &filled))
	assert.Equal(t, 2, rows)
	assert.Zero(t, filled, "the owner's UPDATE saw no tenant's rows")

	var super, bypass bool
	require.NoError(t, rig.Admin.Global().QueryRow(ctx,
		`SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&super, &bypass))
	assert.True(t, super, "the admin handle is the superuser that sees everything")
	require.NoError(t, rig.Owner.Global().QueryRow(ctx,
		`SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&super, &bypass))
	assert.False(t, super)
	assert.False(t, bypass)
}
