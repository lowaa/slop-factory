package schema_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-db/engine"
	"github.com/lowaa/slop-factory/go-db/migrations"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
)

func TestMain(m *testing.M) {
	dbtest.Main(m, schema.Migrations)
}

// Postgres has no default-deny: a tenant table that forgot its RLS
// boilerplate is silently readable across tenants through a correctly
// scoped transaction. The lint catches that in the migration files.
func TestMigrationsKeepTheGlobalTenantConvention(t *testing.T) {
	assert.Empty(t, migrations.LintGlobalTenantInvariants(schema.Migrations))
}

// ...and this catches it in the database the migrations actually produce,
// which the per-file lint cannot see across files.
func TestTheMigratedSchemaKeepsTheGlobalTenantConvention(t *testing.T) {
	database := dbtest.WithDB(t)

	assert.Empty(t, migrations.CheckLiveTenantInvariants(context.Background(), globalQuerier{database.Global()}))
}

// Each tenant numbers its own tickets from 1, whatever the others do.
func TestTicketsAreNumberedPerTenant(t *testing.T) {
	database := dbtest.WithDB(t)
	a := db.WithTenant(context.Background(), uuid.New())
	b := db.WithTenant(context.Background(), uuid.New())

	insert := func(ctx context.Context) int64 {
		var id int64
		require.NoError(t, database.QueryRow(ctx, `INSERT INTO tickets (title) VALUES ('t') RETURNING id`).Scan(&id))
		return id
	}
	assert.Equal(t, int64(1), insert(a))
	assert.Equal(t, int64(2), insert(a))
	assert.Equal(t, int64(1), insert(b), "another tenant's tickets do not move this one's numbers")
	assert.Equal(t, int64(3), insert(a))
}

// The composite (tenant_id, ...) keys refuse a reference to another
// tenant's row even when a query names its id outright.
//
// This used to insert a's ticket id from b's context and call that proof
// the FK refuses a cross-tenant reference -- but per-tenant numbering (see
// TestTicketsAreNumberedPerTenant) means a's first ticket and b's first
// ticket share the same id, so "ticket_id = aTicket" run as b was
// indistinguishable from "ticket_id = bTicket": it never actually reached
// across tenants, it just resolved to b's own row of the same number. Two
// tickets for a, none of them numbered like any of b's, is what makes the
// reference genuinely cross-tenant.
func TestARowCannotReferenceAnotherTenantsRow(t *testing.T) {
	database := dbtest.WithDB(t)
	a := db.WithTenant(context.Background(), uuid.New())
	b := db.WithTenant(context.Background(), uuid.New())

	var aTicket1, aTicket2 int64
	require.NoError(t, database.QueryRow(a, `INSERT INTO tickets (title) VALUES ('a1') RETURNING id`).Scan(&aTicket1))
	require.NoError(t, database.QueryRow(a, `INSERT INTO tickets (title) VALUES ('a2') RETURNING id`).Scan(&aTicket2))
	var bTicket int64
	require.NoError(t, database.QueryRow(b, `INSERT INTO tickets (title) VALUES ('b') RETURNING id`).Scan(&bTicket))
	require.Equal(t, aTicket1, bTicket, "both are #1 in their own tenant")
	require.NotEqual(t, aTicket2, bTicket, "a's second ticket has no counterpart in b")

	// From b, ticket_id = aTicket1 resolves to b's own #1 (tenant_id fills
	// in as b's by default) -- not a reference into a at all.
	_, err := database.Exec(b, `INSERT INTO ticket_paths (ticket_id, path) VALUES ($1, 'x/')`, aTicket1)
	require.NoError(t, err)

	// ticket_id = aTicket2 has no row in b's tenant: this is what an actual
	// cross-tenant reference looks like, and the composite FK refuses it.
	_, err = database.Exec(b, `INSERT INTO ticket_paths (ticket_id, path) VALUES ($1, 'z/')`, aTicket2)
	require.Error(t, err, "a's second ticket must not be reachable from b")

	// Naming a's tenant outright is refused by the RLS policy.
	aTenant, err := db.TenantFrom(a)
	require.NoError(t, err)
	_, err = database.Exec(b, `INSERT INTO ticket_paths (tenant_id, ticket_id, path) VALUES ($1, $2, 'y/')`, aTenant, aTicket1)
	require.Error(t, err, "writing a row into another tenant must be refused")

	var aPaths int
	require.NoError(t, database.QueryRow(a, `SELECT count(*) FROM ticket_paths`).Scan(&aPaths))
	assert.Zero(t, aPaths, "nothing landed in tenant a")
}

// UPDATE is as much a write as INSERT: moving an existing row into another
// tenant by overwriting its tenant_id must be refused the same way, not
// just blocked at creation time.
func TestAnUpdateCannotMoveARowToAnotherTenant(t *testing.T) {
	database := dbtest.WithDB(t)
	a := db.WithTenant(context.Background(), uuid.New())
	b := db.WithTenant(context.Background(), uuid.New())

	var aTicket int64
	require.NoError(t, database.QueryRow(a, `INSERT INTO tickets (title) VALUES ('a') RETURNING id`).Scan(&aTicket))
	bTenant, err := db.TenantFrom(b)
	require.NoError(t, err)

	_, err = database.Exec(a, `UPDATE tickets SET tenant_id = $1 WHERE id = $2`, bTenant, aTicket)
	require.Error(t, err, "a tenant-scoped update must not be able to move its own row into another tenant")

	var title string
	require.NoError(t, database.QueryRow(a, `SELECT title FROM tickets WHERE id = $1`, aTicket).Scan(&title))
	assert.Equal(t, "a", title, "the row is still a's, untouched")
	var inB int
	require.NoError(t, database.QueryRow(b, `SELECT count(*) FROM tickets WHERE id = $1`, aTicket).Scan(&inB))
	assert.Zero(t, inB, "and never became visible to b")
}

// A row written with no tenant is refused rather than left unowned.
func TestAWriteWithoutATenantIsRefused(t *testing.T) {
	database := dbtest.WithDB(t)

	_, err := database.Global().Exec(context.Background(), `INSERT INTO tickets (title) VALUES ('orphan')`)
	require.Error(t, err)
}

// globalQuerier adapts db.Querier to go-db's engine.Querier, which the
// live check takes.
type globalQuerier struct{ q db.Querier }

func (g globalQuerier) ExecContext(ctx context.Context, sql string, args ...any) (engine.Result, error) {
	return g.q.Exec(ctx, sql, args...)
}

func (g globalQuerier) QueryContext(ctx context.Context, sql string, args ...any) (engine.Rows, error) {
	return g.q.Query(ctx, sql, args...)
}

func (g globalQuerier) QueryRowContext(ctx context.Context, sql string, args ...any) engine.Row {
	return g.q.QueryRow(ctx, sql, args...)
}
