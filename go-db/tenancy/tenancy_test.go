package tenancy_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/go-db/engine"
	"github.com/lowaa/slop-factory/go-db/internal/dbtestutil"
	"github.com/lowaa/slop-factory/go-db/internal/demo"
	"github.com/lowaa/slop-factory/go-db/migrations"
	"github.com/lowaa/slop-factory/go-db/tenancy"
)

// testAppConnString authenticates as dbtestutil.AppRole: a NOSUPERUSER,
// NOBYPASSRLS role. This is deliberate -- the container's default user is a
// superuser, and Postgres row security is always bypassed by superusers
// (even with FORCE ROW LEVEL SECURITY), so RLS-dependent tests would pass
// vacuously if they queried through that role instead.
var testAppConnString string

// TestMain migrates the schema once for the whole package, matching Mode
// A's "one container per test binary run, migrated up once" shape -- these
// tests can't use the dbtest package's helper for this because dbtest
// depends on tenancy, and that dependency can't run in reverse.
func TestMain(m *testing.M) {
	ctx := context.Background()

	ctr, err := dbtestutil.StartPostgres(ctx)
	if err != nil {
		panic(err)
	}

	adminEng := engine.NewPostgres(ctr.ConnString)
	if err := adminEng.Connect(ctx); err != nil {
		panic(err)
	}
	if err := migrations.Up(ctx, adminEng, demo.Migrations); err != nil {
		panic(err)
	}
	appConnString, err := ctr.EnsureAppRole(ctx, adminEng)
	if err != nil {
		panic(err)
	}
	testAppConnString = appConnString
	_ = adminEng.Close()

	code := m.Run()
	_ = ctr.Terminate(ctx)
	os.Exit(code)
}

func newEngine(t *testing.T) engine.Engine {
	t.Helper()
	ctx := context.Background()

	eng := engine.NewPostgres(testAppConnString)
	if err := eng.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

// seedAccount inserts one account row for tenantID via a TenantTx, and
// commits it, returning the new account's ID.
func seedAccount(t *testing.T, eng engine.Engine, tenantID uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	tx, err := tenancy.NewTenantTx(ctx, eng, tenantID)
	if err != nil {
		t.Fatalf("new tenant tx: %v", err)
	}

	accountID := uuid.New()
	_, err = tx.ExecContext(ctx,
		`INSERT INTO accounts (id, tenant_id, name, currency_code) VALUES ($1, $2, 'test account', 'USD')`,
		accountID, tenantID,
	)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert account: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return accountID
}

func countAccounts(t *testing.T, q engine.Querier) int {
	t.Helper()
	var count int
	if err := q.QueryRowContext(context.Background(), `SELECT count(*) FROM accounts`).Scan(&count); err != nil {
		t.Fatalf("count accounts: %v", err)
	}
	return count
}

func TestTenantTxIsolatesRowsAcrossTenants(t *testing.T) {
	eng := newEngine(t)
	tenantA := uuid.New()
	tenantB := uuid.New()

	seedAccount(t, eng, tenantA)

	ctx := context.Background()

	txA, err := tenancy.NewTenantTx(ctx, eng, tenantA)
	if err != nil {
		t.Fatalf("new tenant tx (A): %v", err)
	}
	defer func() { _ = txA.Rollback(ctx) }()
	if got := countAccounts(t, txA); got != 1 {
		t.Errorf("tenant A sees %d accounts, want 1", got)
	}

	txB, err := tenancy.NewTenantTx(ctx, eng, tenantB)
	if err != nil {
		t.Fatalf("new tenant tx (B): %v", err)
	}
	defer func() { _ = txB.Rollback(ctx) }()
	if got := countAccounts(t, txB); got != 0 {
		t.Errorf("tenant B sees %d accounts, want 0", got)
	}
}

// TestFailClosedWithoutTenantContext proves the RLS policy's fail-closed
// default: a transaction that never went through NewTenantTx (so
// app.current_tenant was never set) must see zero rows, not all rows.
func TestFailClosedWithoutTenantContext(t *testing.T) {
	eng := newEngine(t)
	tenant := uuid.New()
	seedAccount(t, eng, tenant)

	ctx := context.Background()
	rawTx, err := eng.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin raw tx: %v", err)
	}
	defer func() { _ = rawTx.Rollback(ctx) }()

	if got := countAccounts(t, rawTx); got != 0 {
		t.Errorf("no-tenant-context tx sees %d accounts, want 0 (fail-closed default)", got)
	}
}

// TestGlobalTableReachableWithoutTenantContext proves global_* tables are
// queryable through a plain engine.Tx/Querier with no tenant context set at
// all -- the explicit exception to tenant scoping.
func TestGlobalTableReachableWithoutTenantContext(t *testing.T) {
	eng := newEngine(t)
	ctx := context.Background()

	rawTx, err := eng.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin raw tx: %v", err)
	}
	defer func() { _ = rawTx.Rollback(ctx) }()

	var count int
	if err := rawTx.QueryRowContext(ctx, `SELECT count(*) FROM global_currencies`).Scan(&count); err != nil {
		t.Fatalf("count global_currencies: %v", err)
	}
	if count == 0 {
		t.Error("global_currencies returned zero rows via a tenant-less Querier, want the seeded reference rows")
	}
}

func TestTenantTxRollbackDiscardsWrites(t *testing.T) {
	eng := newEngine(t)
	tenant := uuid.New()
	ctx := context.Background()

	tx, err := tenancy.NewTenantTx(ctx, eng, tenant)
	if err != nil {
		t.Fatalf("new tenant tx: %v", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO accounts (id, tenant_id, name, currency_code) VALUES ($1, $2, 'rolled back', 'USD')`,
		uuid.New(), tenant,
	); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	verifyTx, err := tenancy.NewTenantTx(ctx, eng, tenant)
	if err != nil {
		t.Fatalf("new tenant tx (verify): %v", err)
	}
	defer func() { _ = verifyTx.Rollback(ctx) }()
	if got := countAccounts(t, verifyTx); got != 0 {
		t.Errorf("tenant sees %d accounts after rollback, want 0", got)
	}
}

// Options must not cost the scoping: a read-only snapshot transaction is
// still one tenant's view, and still refuses writes.
func TestTenantTxWithOptionsStaysScoped(t *testing.T) {
	eng := newEngine(t)
	tenantA, tenantB := uuid.New(), uuid.New()
	seedAccount(t, eng, tenantA)
	seedAccount(t, eng, tenantB)
	ctx := context.Background()

	tx, err := tenancy.NewTenantTxWithOptions(ctx, eng, tenantA,
		engine.TxOptions{Isolation: engine.IsolationRepeatableRead, ReadOnly: true})
	if err != nil {
		t.Fatalf("new tenant tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if got := countAccounts(t, tx); got != 1 {
		t.Errorf("tenant A sees %d accounts, want 1", got)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO accounts (id, tenant_id, name, currency_code) VALUES ($1, $2, 'x', 'USD')`,
		uuid.New(), tenantA); err == nil {
		t.Error("insert through a read-only tenant tx succeeded; want it refused")
	}
}
