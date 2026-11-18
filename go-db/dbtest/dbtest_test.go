package dbtest_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-db/dbtest"
	"github.com/lowaa/slop-factory/go-db/engine"
	"github.com/lowaa/slop-factory/go-db/internal/dbtestutil"
	"github.com/lowaa/slop-factory/go-db/internal/demo"
	"github.com/lowaa/slop-factory/go-db/migrations"
	"github.com/lowaa/slop-factory/go-db/tenancy"
)

func TestMain(m *testing.M) {
	dbtest.Main(m, demo.Migrations)
}

func insertAccount(t *testing.T, tx *tenancy.TenantTx, tenantID uuid.UUID) {
	t.Helper()
	_, err := tx.ExecContext(context.Background(),
		`INSERT INTO accounts (id, tenant_id, name, currency_code) VALUES ($1, $2, 'test', 'USD')`,
		uuid.New(), tenantID,
	)
	if err != nil {
		t.Fatalf("insert account: %v", err)
	}
}

func countAccounts(t *testing.T, q engine.Querier) int {
	t.Helper()
	var n int
	if err := q.QueryRowContext(context.Background(), `SELECT count(*) FROM accounts`).Scan(&n); err != nil {
		t.Fatalf("count accounts: %v", err)
	}
	return n
}

// TestModeA_CrossTenantIsolation is the "cross-tenant isolation tests as
// their own named category" from the design doc: seed data for tenant A,
// assert tenant B's TenantTx returns zero rows, against the real RLS
// policies (not a mock).
func TestModeA_CrossTenantIsolation(t *testing.T) {
	tenants := dbtest.SeedTenants(t, dbtest.AppEngine(), 2)
	tenantA, tenantB := tenants[0], tenants[1]

	txA := dbtest.WithTenantTx(t, tenantA)
	insertAccount(t, txA, tenantA)

	if got := countAccounts(t, txA); got != 1 {
		t.Errorf("tenant A sees %d accounts, want 1", got)
	}

	txB := dbtest.WithTenantTx(t, tenantB)
	if got := countAccounts(t, txB); got != 0 {
		t.Errorf("tenant B sees %d accounts, want 0", got)
	}
}

// TestModeA_FailClosedWithoutTenantContext is the "failure mode coverage"
// category: a query that never went through TenantTx must return zero rows,
// not every tenant's rows.
func TestModeA_FailClosedWithoutTenantContext(t *testing.T) {
	tenants := dbtest.SeedTenants(t, dbtest.AppEngine(), 1)
	tenantA := tenants[0]

	txA := dbtest.WithTenantTx(t, tenantA)
	insertAccount(t, txA, tenantA)

	ctx := context.Background()
	rawTx, err := dbtest.AppEngine().BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin raw tx: %v", err)
	}
	defer func() { _ = rawTx.Rollback(ctx) }()

	var count int
	if err := rawTx.QueryRowContext(ctx, `SELECT count(*) FROM accounts`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("no-tenant-context tx sees %d accounts, want 0", count)
	}
}

// TestGlobalTableCoverage is the "global-table coverage as its own named
// category" from the design doc: a global_* table has no RLS policy at all
// (so it isn't accidentally scoped), and is reachable via a plain Querier
// with no tenant context set (so it's usable without a tenant).
func TestGlobalTableCoverage(t *testing.T) {
	ctx := context.Background()

	for _, table := range []string{"global_tenants", "global_currencies"} {
		var hasPolicy bool
		err := dbtest.AppEngine().QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM pg_policies WHERE tablename = $1)`, table,
		).Scan(&hasPolicy)
		if err != nil {
			t.Fatalf("check policy on %s: %v", table, err)
		}
		if hasPolicy {
			t.Errorf("%s has an RLS policy but should not (it's a global table)", table)
		}
	}

	rawTx, err := dbtest.AppEngine().BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin raw tx: %v", err)
	}
	defer func() { _ = rawTx.Rollback(ctx) }()

	var count int
	if err := rawTx.QueryRowContext(ctx, `SELECT count(*) FROM global_currencies`).Scan(&count); err != nil {
		t.Fatalf("count global_currencies: %v", err)
	}
	if count == 0 {
		t.Error("global_currencies returned zero rows through a tenant-less Querier, want the seeded reference rows")
	}
}

// TestModeB_CommitIsVisibleAcrossTransactions exercises transactional
// behavior itself -- something that can't be tested inside Mode A's
// always-rolled-back wrapper transaction, since the code under test needs
// to actually commit and have that be observable.
func TestModeB_CommitIsVisibleAcrossTransactions(t *testing.T) {
	eng := dbtest.WithCommittedDB(t)
	ctx := context.Background()
	tenantID := uuid.New()

	tx, err := tenancy.NewTenantTx(ctx, eng, tenantID)
	if err != nil {
		t.Fatalf("new tenant tx: %v", err)
	}
	insertAccount(t, tx, tenantID)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	verifyTx, err := tenancy.NewTenantTx(ctx, eng, tenantID)
	if err != nil {
		t.Fatalf("new tenant tx (verify): %v", err)
	}
	defer func() { _ = verifyTx.Rollback(ctx) }()

	var count int
	if err := verifyTx.QueryRowContext(ctx, `SELECT count(*) FROM accounts`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("committed row not visible to a new transaction: count = %d, want 1", count)
	}
}

// TestModeC_MigrationUpDownUp is the "implicit" Mode C from the design doc:
// built on Mode B's per-test committed database, run Down then Up again and
// assert the schema round-trips cleanly, never inside a wrapping
// transaction (golang-migrate manages its own).
func TestModeC_MigrationUpDownUp(t *testing.T) {
	eng := dbtest.WithCommittedAdminDB(t)
	ctx := context.Background()

	assertTableExists := func(want bool) {
		t.Helper()
		var exists bool
		err := eng.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'accounts')`,
		).Scan(&exists)
		if err != nil {
			t.Fatalf("check accounts exists: %v", err)
		}
		if exists != want {
			t.Errorf("accounts exists = %v, want %v", exists, want)
		}
	}

	assertTableExists(true)

	if err := migrations.Down(ctx, eng, demo.Migrations, 4); err != nil {
		t.Fatalf("down: %v", err)
	}
	assertTableExists(false)

	if err := migrations.Up(ctx, eng, demo.Migrations); err != nil {
		t.Fatalf("up: %v", err)
	}
	assertTableExists(true)
}

func TestWithCommittedDSN_ConnectsAsAppRole(t *testing.T) {
	eng := engine.NewPostgres(dbtest.WithCommittedDSN(t))
	if err := eng.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	var user string
	var super bool
	if err := eng.QueryRowContext(context.Background(),
		`SELECT current_user, (SELECT rolsuper FROM pg_roles WHERE rolname = current_user)`).Scan(&user, &super); err != nil {
		t.Fatalf("query: %v", err)
	}
	if user != dbtestutil.AppRole || super {
		t.Errorf("connected as %q (superuser=%v), want non-superuser %q", user, super, dbtestutil.AppRole)
	}
}

func TestWithEmptyAdminDSNOwnedBy_IsEmptyAndOwned(t *testing.T) {
	ctx := context.Background()
	admin := engine.NewPostgres(dbtest.AdminDSN())
	require.NoError(t, admin.Connect(ctx))
	t.Cleanup(func() { _ = admin.Close() })
	_, err := admin.ExecContext(ctx, `CREATE ROLE empty_owner_test NOLOGIN`)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.ExecContext(ctx, `DROP ROLE IF EXISTS empty_owner_test`) })

	eng := engine.NewPostgres(dbtest.WithEmptyAdminDSNOwnedBy(t, "empty_owner_test"))
	require.NoError(t, eng.Connect(ctx))
	t.Cleanup(func() { _ = eng.Close() })

	var tables int
	var owner string
	require.NoError(t, eng.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public'),
		(SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname = current_database())`).Scan(&tables, &owner))
	assert.Equal(t, 0, tables, "empty database must not carry the template's schema")
	assert.Equal(t, "empty_owner_test", owner)
}
