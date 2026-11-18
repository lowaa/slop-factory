package schema_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
)

// tenantSettingRow is one tenant_settings row as a reader sees it.
type tenantSettingRow struct {
	Name      string
	Type      string
	Str       *string
	Int       *int64
	Bool      *bool
	Float     *float64
	ChangedBy *uuid.UUID
	Deleted   bool
}

func readTenantSettings(t *testing.T, database *db.DB, ctx context.Context, name string) []tenantSettingRow {
	t.Helper()
	rows, err := database.Query(ctx, `
		SELECT setting_name, setting_type, str_value, int_value, bool_value, float_value, changed_by, changed_by_deleted
		  FROM tenant_settings WHERE ($1 = '' OR setting_name = $1) ORDER BY id`, name)
	require.NoError(t, err)
	defer rows.Close()
	var out []tenantSettingRow
	for rows.Next() {
		var r tenantSettingRow
		require.NoError(t, rows.Scan(&r.Name, &r.Type, &r.Str, &r.Int, &r.Bool, &r.Float, &r.ChangedBy, &r.Deleted))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

func ptr[T any](v T) *T { return &v }

type tenantSettingsFixture struct {
	t        *testing.T
	database *db.DB
	ctx      context.Context
	tenant   uuid.UUID
}

func newTenantSettingsFixture(t *testing.T) *tenantSettingsFixture {
	t.Helper()
	database := dbtest.WithDB(t)
	tenant := dbtest.NewTenant(t, database)
	return &tenantSettingsFixture{t: t, database: database, tenant: tenant, ctx: db.WithTenant(context.Background(), tenant)}
}

func TestATenantCannotReadAnotherTenantsSettings(t *testing.T) {
	database := dbtest.WithDB(t)
	tenantA, tenantB := uuid.New(), uuid.New()
	ctxA, ctxB := db.WithTenant(context.Background(), tenantA), db.WithTenant(context.Background(), tenantB)
	_, err := database.Exec(ctxA, `INSERT INTO tenant_settings (setting_name, setting_type, str_value) VALUES ('s', 'str', 'A')`)
	require.NoError(t, err)
	_, err = database.Exec(ctxB, `INSERT INTO tenant_settings (setting_name, setting_type, str_value) VALUES ('s', 'str', 'B')`)
	require.NoError(t, err)

	assert.Equal(t, []tenantSettingRow{{Name: "s", Type: "str", Str: ptr("A")}}, readTenantSettings(t, database, ctxA, ""))
	assert.Equal(t, []tenantSettingRow{{Name: "s", Type: "str", Str: ptr("B")}}, readTenantSettings(t, database, ctxB, ""))

	_, err = database.Exec(ctxA, `INSERT INTO tenant_settings (tenant_id, setting_name, setting_type, str_value) VALUES ($1, 's', 'str', 'x')`, tenantB)
	assert.Error(t, err, "a tenant cannot write another's rows")
}

func TestTenantSettingsChecksRejectBadShapes(t *testing.T) {
	f := newTenantSettingsFixture(t)
	person := dbtest.NewUser(t, f.database)
	cases := map[string]string{
		"no value":              `('n', 'str', NULL, NULL, NULL, NULL, NULL)`,
		"two values":            `('n', 'str', 'x', 1, NULL, NULL, NULL)`,
		"value of another type": `('n', 'str', NULL, 1, NULL, NULL, NULL)`,
		"bool in int column":    `('n', 'bool', NULL, 1, NULL, NULL, NULL)`,
		"float in jsonb column": `('n', 'float', NULL, NULL, NULL, NULL, '1')`,
		"unknown type":          `('n', 'string', 'x', NULL, NULL, NULL, NULL)`,
		"jsonb typed as str":    `('n', 'str', NULL, NULL, NULL, NULL, '{}')`,
		"int typed as float":    `('n', 'float', NULL, 1, NULL, NULL, NULL)`,
		"bool typed as int":     `('n', 'int', NULL, NULL, true, NULL, NULL)`,
		"str typed as jsonb":    `('n', 'jsonb', 'x', NULL, NULL, NULL, NULL)`,
		"float typed as bool":   `('n', 'bool', NULL, NULL, NULL, 1.5, NULL)`,
	}
	for name, values := range cases {
		t.Run(name, func(t *testing.T) {
			err := f.execErr(`INSERT INTO tenant_settings (setting_name, setting_type, str_value, int_value, bool_value, float_value, jsonb_value) VALUES ` + values)
			assert.Error(t, err)
		})
	}
	t.Run("flagged deleted yet naming a person", func(t *testing.T) {
		err := f.execErr(`INSERT INTO tenant_settings (setting_name, setting_type, str_value, changed_by, changed_by_deleted) VALUES ('n', 'str', 'x', $1, true)`, person)
		assert.Error(t, err)
	})
	t.Run("a valid row of each type is accepted", func(t *testing.T) {
		for _, values := range []string{
			`('s', 'str', 'x', NULL, NULL, NULL, NULL)`, `('i', 'int', NULL, 1, NULL, NULL, NULL)`,
			`('b', 'bool', NULL, NULL, false, NULL, NULL)`, `('f', 'float', NULL, NULL, NULL, 1.5, NULL)`,
			`('j', 'jsonb', NULL, NULL, NULL, NULL, '{"a":1}')`,
		} {
			require.NoError(t, f.execErr(`INSERT INTO tenant_settings (setting_name, setting_type, str_value, int_value, bool_value, float_value, jsonb_value) VALUES `+values), values)
		}
	})
}

func (f *tenantSettingsFixture) execErr(sql string, args ...any) error {
	f.t.Helper()
	_, err := f.database.Exec(f.ctx, sql, args...)
	return err
}

func TestRemovingAChangersAccountKeepsTheHistoryRow(t *testing.T) {
	f := newTenantSettingsFixture(t)
	person := dbtest.NewUser(t, f.database)
	require.NoError(t, f.execErr(`INSERT INTO tenant_settings (setting_name, setting_type, str_value, changed_by) VALUES ('n', 'str', 'x', $1)`, person))

	_, err := f.database.Global().Exec(context.Background(), `DELETE FROM global_users WHERE id = $1`, person)
	require.NoError(t, err)

	rows := readTenantSettings(t, f.database, f.ctx, "n")
	require.Len(t, rows, 1)
	assert.Nil(t, rows[0].ChangedBy, "the person's id is gone from the row")
}

// A column nulled by its person's deletion takes a new person without the
// flag refusing the write, and the flag goes with it.
func TestAColumnOfADeletedAccountTakesANewValueAndLosesItsFlag(t *testing.T) {
	f := newTenantSettingsFixture(t)
	gone, successor := dbtest.NewUser(t, f.database), dbtest.NewUser(t, f.database)
	require.NoError(t, f.execErr(`INSERT INTO tenant_settings (setting_name, setting_type, str_value, changed_by) VALUES ('n', 'str', 'x', $1)`, gone))
	_, err := f.database.Global().Exec(context.Background(), `DELETE FROM global_users WHERE id = $1`, gone)
	require.NoError(t, err)
	rows := readTenantSettings(t, f.database, f.ctx, "n")
	require.Len(t, rows, 1)
	require.True(t, rows[0].Deleted)

	require.NoError(t, f.execErr(`UPDATE tenant_settings SET changed_by = $1 WHERE setting_name = 'n'`, successor))

	rows = readTenantSettings(t, f.database, f.ctx, "n")
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].ChangedBy)
	assert.Equal(t, successor, *rows[0].ChangedBy)
	assert.False(t, rows[0].Deleted)
}
