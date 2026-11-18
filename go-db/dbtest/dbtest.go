// Package dbtest is the test harness: one Postgres testcontainer per test
// binary run, migrated up once, exposing two isolation strategies.
//
//   - Mode A (WithTenantTx): fast, per-test isolation via an
//     open-transaction-then-always-rollback TenantTx. Use this for the bulk
//     of repository/handler-level tests.
//   - Mode B (WithCommittedDB): a freshly cloned, fully committed database
//     per test, for anything that needs real commit/rollback semantics --
//     transactional behavior itself, side effects visible to other
//     connections, or migration up/down verification.
//
// Both modes go through the same TenantTx/engine.Querier surface production
// code uses; neither bypasses RLS.
package dbtest

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"

	"github.com/lowaa/slop-factory/go-db/engine"
	"github.com/lowaa/slop-factory/go-db/internal/dbtestutil"
	"github.com/lowaa/slop-factory/go-db/migrations"
	"github.com/lowaa/slop-factory/go-db/tenancy"
)

var (
	ctr              *dbtestutil.Container
	adminEng         engine.Engine // superuser, connected to the main (non-template) db
	appEng           engine.Engine // NOSUPERUSER/NOBYPASSRLS, connected to the main db -- this is what Mode A and fixtures use
	appConnStr       string        // app-role conn string for the main db, reused by Mode B to build per-clone connections
	packMigrationsFS fs.FS         // the migration source Main was given, reused by Mode B's template setup
	opts             Options       // hooks Main was given; zero value for plain Main
)

// ContainerOptions tunes the container; it is ignored when DBTEST_POSTGRES_URL
// names an already-running server.
type ContainerOptions struct {
	// Image overrides the default Postgres image.
	Image string
	// Customizers are applied after the defaults, so they can add labels or
	// server flags and replace the wait strategy.
	Customizers []testcontainers.ContainerCustomizer
}

// Options are the hooks for a harness whose schema, container or failure
// reporting differ from the defaults. The zero value is plain Main.
type Options struct {
	// Container tunes the Postgres container (image, labels, server flags).
	Container ContainerOptions
	// MigrateTemplate replaces the default template setup (go-db migrations
	// plus the go-db app role), for a schema applied by something other than
	// go-db's migrations. It is given a superuser connection string to the
	// empty template database and must leave it migrated and, if clones are
	// to be used as an app role, granted. It also replaces the main
	// database's setup, so Mode A (WithTenantTx, AppEngine) is unavailable.
	// Clones are reached through WithCommittedDSN/WithCommittedAdminDSN.
	MigrateTemplate func(ctx context.Context, templateConnStr string) error
	// Diagnose explains a failed connection to the server -- e.g. why the
	// container vanished -- and is appended to the failure. It is called only
	// on failure paths and must not fail itself.
	Diagnose func(ctx context.Context) string
}

// Main is the TestMain entry point: start one container for the whole test
// binary, migrate it with migrationsFS, provision the least-privileged app
// role Mode A/B need for RLS to actually be exercised (see
// dbtestutil.AppRole), set up the Mode B template database, run the
// package's tests, then tear everything down.
//
// dbtest is schema-agnostic -- it has no built-in notion of what tables
// exist, so callers supply their own migration source, e.g.:
//
//	func TestMain(m *testing.M) { dbtest.Main(m, demo.Migrations) }
func Main(m *testing.M, migrationsFS fs.FS) {
	MainWithOptions(m, migrationsFS, Options{})
}

// MainWithOptions is Main with the harness tuned by o.
func MainWithOptions(m *testing.M, migrationsFS fs.FS, o Options) {
	os.Exit(run(m, migrationsFS, o))
}

func run(m *testing.M, migrationsFS fs.FS, o Options) int {
	ctx := context.Background()
	packMigrationsFS = migrationsFS
	opts = o

	c, err := startContainer(ctx, &o.Container)
	if err != nil {
		panic(fmt.Errorf("dbtest: start postgres: %w", err))
	}
	ctr = c
	defer func() { _ = ctr.Terminate(context.Background()) }()

	if o.MigrateTemplate != nil {
		if err := setupTemplateDB(ctx); err != nil {
			panic(fmt.Errorf("dbtest: set up template db: %w", err))
		}
		return m.Run()
	}

	adminEng = engine.NewPostgres(ctr.ConnString)
	if err := adminEng.Connect(ctx); err != nil {
		panic(fmt.Errorf("dbtest: connect admin engine: %w", err))
	}
	defer func() { _ = adminEng.Close() }()

	if err := migrations.Up(ctx, adminEng, migrationsFS); err != nil {
		panic(fmt.Errorf("dbtest: migrate main db: %w", err))
	}

	connStr, err := ctr.EnsureAppRole(ctx, adminEng)
	if err != nil {
		panic(fmt.Errorf("dbtest: ensure app role: %w", err))
	}
	appConnStr = connStr

	appEng = engine.NewPostgres(appConnStr)
	if err := appEng.Connect(ctx); err != nil {
		panic(fmt.Errorf("dbtest: connect app engine: %w", err))
	}
	defer func() { _ = appEng.Close() }()

	if err := setupTemplateDB(ctx); err != nil {
		panic(fmt.Errorf("dbtest: set up template db: %w", err))
	}

	return m.Run()
}

// startContainer honours SharedURLEnv like StartPostgres; container options
// only apply to a container of our own.
func startContainer(ctx context.Context, co *ContainerOptions) (*dbtestutil.Container, error) {
	if os.Getenv(dbtestutil.SharedURLEnv) != "" {
		return dbtestutil.StartPostgres(ctx)
	}
	return dbtestutil.StartDedicatedPostgresWith(ctx, &dbtestutil.StartOptions{Image: co.Image, Customizers: co.Customizers})
}

// WithTenantTx opens a TenantTx scoped to tenantID against the package's
// shared app-role connection pool, and registers a rollback via t.Cleanup --
// pass or fail, the transaction is never committed. This is Mode A.
func WithTenantTx(t *testing.T, tenantID uuid.UUID) *tenancy.TenantTx {
	t.Helper()
	ctx := context.Background()
	if appEng == nil {
		t.Fatalf("dbtest: Mode A is unavailable when Options.MigrateTemplate is set")
	}

	tx, err := tenancy.NewTenantTx(ctx, appEng, tenantID)
	if err != nil {
		t.Fatalf("dbtest: new tenant tx: %v", err)
	}
	t.Cleanup(func() {
		_ = tx.Rollback(context.Background())
	})
	return tx
}

// AppEngine returns the package's shared app-role (NOSUPERUSER, NOBYPASSRLS)
// Engine connected to the main db, for tests that need a plain Querier --
// e.g. reading a global_* table, or proving the fail-closed default with a
// transaction that never went through NewTenantTx.
func AppEngine() engine.Engine {
	return appEng
}
