package schema_test

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema/seeddata"
)

// TakeSnapshot reads every table at once per tenant; what it files must be
// what reading each table on its own, as the app role, finds: a tenant's
// rows under that tenant, the global tables' under the zero uuid.
func TestTakeSnapshotHoldsWhatEachTableReadsAsTheAppRole(t *testing.T) {
	ctx := context.Background()
	rig := dbtest.NewMigrationRig(t, schema.Migrations)
	versions := rig.Versions()
	latest := versions[len(versions)-1]
	require.NoError(t, rig.MigrateTo(ctx, latest))
	tenants := []uuid.UUID{uuid.New(), uuid.New()}
	require.NoError(t, seeddata.NewSeeding(rig.App, seeddata.Seeders, tenants).Round(ctx, latest, len(versions)))

	snap, err := seeddata.TakeSnapshot(ctx, rig.App, tenants)
	require.NoError(t, err)

	tables, err := seeddata.Tables(ctx, rig.Admin.Global())
	require.NoError(t, err)
	require.Len(t, snap.Rows, len(tables))
	read := 0
	for _, table := range tables {
		assert.Equal(t, columnsOf(t, ctx, rig.Admin, table), snap.Columns[table], "%s: columns", table)
		if strings.HasPrefix(table, "global_") {
			assert.Equal(t, rowsOf(t, ctx, rig.App.Global(), table), snap.Rows[table][uuid.Nil], "%s: rows", table)
			read += len(snap.Rows[table][uuid.Nil])
			continue
		}
		for _, tenant := range tenants {
			got := snap.Rows[table][tenant]
			assert.Equal(t, rowsOf(t, db.WithTenant(ctx, tenant), rig.App, table), got, "%s: tenant %s's rows", table, tenant)
			for _, row := range got {
				var r struct {
					TenantID uuid.UUID `json:"tenant_id"`
				}
				require.NoError(t, json.Unmarshal([]byte(row), &r))
				assert.Equal(t, tenant, r.TenantID, "%s: a row filed under another tenant", table)
			}
			read += len(got)
		}
	}
	assert.Positive(t, read, "the seeded rows were read")
}

func columnsOf(t *testing.T, ctx context.Context, admin *db.DB, table string) []string {
	t.Helper()
	rows, err := admin.Global().Query(ctx,
		`SELECT column_name FROM information_schema.columns WHERE table_schema = 'public' AND table_name = $1 ORDER BY column_name`, table)
	require.NoError(t, err)
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		require.NoError(t, rows.Scan(&c))
		cols = append(cols, c)
	}
	require.NoError(t, rows.Err())
	return cols
}

func rowsOf(t *testing.T, ctx context.Context, q db.Querier, table string) []string {
	t.Helper()
	rows, err := q.Query(ctx, "SELECT to_jsonb(t) FROM "+table+" t")
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var raw []byte
		require.NoError(t, rows.Scan(&raw))
		out = append(out, string(raw))
	}
	require.NoError(t, rows.Err())
	sort.Strings(out)
	return out
}
