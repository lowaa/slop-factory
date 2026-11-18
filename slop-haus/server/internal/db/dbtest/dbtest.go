// Package dbtest runs tests against a real Postgres: one container per test
// binary, migrated once into a template database, then cloned per test.
//
// Cloning rather than wrapping each test in a rolled-back transaction is a
// deliberate choice. slop-haus's interesting behaviour is concurrent -- two
// agents claiming a ticket at once, a heartbeat racing an expiry sweep --
// and none of that can be exercised inside a single wrapping transaction,
// because the code under test needs to commit and to be observed from
// another connection. CREATE DATABASE ... TEMPLATE is a storage-layer copy,
// so a clone costs far less than re-running migrations per test.
//
// Tests connect as the same role the server serves requests as: the app
// role, which row level security binds, not the owner that ran the
// migrations. That matters more here than anywhere -- RLS does not apply
// to a table's owner or a superuser, so a test connected as either would
// pass every tenant-isolation check with isolation switched off entirely.
package dbtest

import (
	"context"
	"fmt"
	"io/fs"
	"testing"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	gdbtest "github.com/lowaa/slop-factory/go-db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/harness"
)

const (
	postgresImage = "postgres:17-alpine"
	appRole       = "slophaus_app"
	appPassword   = "slophaus_app_password"
)

// postgresArgs tune the throwaway server for many tests at once. Durability
// is worthless here (the container is discarded), and fsync on every clone
// and commit is what parallel tests would otherwise queue behind. The
// connection limit is well above the default 100 because each parallel
// test holds its own pools.
var postgresArgs = []string{
	"postgres",
	"-c", "fsync=off",
	"-c", "synchronous_commit=off",
	"-c", "full_page_writes=off",
	"-c", "max_connections=500",
}

var (
	// Captured at start: a removed container can no longer be inspected, and
	// its session label is what ties it to a reaper (see describeReaper).
	container       testcontainers.Container
	containerLabels map[string]string
)

// Main is the TestMain entry point: start the container, create and migrate
// the template database, run the package's tests, then tear it all down.
// The container, template and clones are go-db's dbtest; this adds slop-haus's
// schema setup, container tuning and failure diagnosis.
//
// The migration source is a parameter rather than baked in, so this harness
// has no dependency on any particular schema:
//
//	func TestMain(m *testing.M) { dbtest.Main(m, schema.Migrations) }
func Main(m *testing.M, migrationsFS fs.FS) {
	gdbtest.MainWithOptions(m, migrationsFS, gdbtest.Options{
		Container: gdbtest.ContainerOptions{
			Image: postgresImage,
			Customizers: []testcontainers.ContainerCustomizer{
				reapOption(),
				testcontainers.WithCmd(postgresArgs...),
				// Postgres in this image starts, runs its init scripts and restarts;
				// waiting for the log line twice avoids connecting during that
				// window and getting a connection that is about to be closed.
				testcontainers.WithWaitStrategy(
					wait.ForLog("database system is ready to accept connections").WithOccurrence(2)),
				testcontainers.WithAdditionalLifecycleHooks(testcontainers.ContainerLifecycleHooks{
					PostStarts: []testcontainers.ContainerHook{captureContainer},
				}),
			},
		},
		MigrateTemplate: func(ctx context.Context, templateDSN string) error {
			return migrateTemplate(ctx, templateDSN, migrationsFS)
		},
		Diagnose: func(context.Context) string {
			// No container of our own when DBTEST_POSTGRES_URL names a shared server.
			if container == nil {
				return "no container to diagnose: using a shared server"
			}
			return describeContainer(sharedContainerProbe())
		},
	})
}

func captureContainer(ctx context.Context, c testcontainers.Container) error {
	container = c
	if inspect, err := c.Inspect(ctx); err == nil {
		containerLabels = inspect.Config.Labels
	}
	return nil
}

// migrateTemplate migrates the template database once. Every per-test clone
// inherits that migrated schema, so migrations run once per binary rather
// than once per test.
func migrateTemplate(ctx context.Context, templateDSN string, migrations fs.FS) error {
	tmpl, err := db.New(ctx, templateDSN)
	if err != nil {
		return err
	}
	defer tmpl.Close()

	if err := db.Migrate(ctx, tmpl, migrations); err != nil {
		return fmt.Errorf("migrate template: %w", err)
	}
	if err := syncHarnesses(ctx, tmpl); err != nil {
		return err
	}
	// The role is cluster-wide; its grants live in the template and are
	// copied into every clone with the rest of the catalog.
	if err := db.EnsureAppRole(ctx, tmpl, appRole, appPassword); err != nil {
		return fmt.Errorf("app role: %w", err)
	}
	return nil
}

// syncHarnesses records the harness registry in the template, as the server
// does at startup: a migrated database never sees that, and without it a
// variant or attempt on any harness the migration did not seed fails its
// foreign key. Callers with their own schema (the migration rig's) have no
// such table, so it is skipped there rather than made a Main parameter that
// every TestMain would have to pass.
func syncHarnesses(ctx context.Context, tmpl *db.DB) error {
	var present bool
	if err := tmpl.Global().QueryRow(ctx,
		`SELECT to_regclass('global_harnesses') IS NOT NULL`).Scan(&present); err != nil {
		return fmt.Errorf("look for global_harnesses: %w", err)
	}
	if !present {
		return nil
	}
	if err := harness.Sync(ctx, tmpl.Global()); err != nil {
		return fmt.Errorf("sync harnesses: %w", err)
	}
	return nil
}

// WithDB gives the test its own freshly-cloned database, and registers
// cleanup (close the pool, drop the clone) via t.Cleanup. Nothing runs
// inside an outer transaction: the code under test commits for real, and
// another connection can observe it.
func WithDB(t *testing.T) *db.DB {
	t.Helper()
	dsn := WithDSN(t)
	conn, err := db.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("dbtest: connect clone: %v", err)
	}
	t.Cleanup(conn.Close)
	return conn
}

// WithDSN is WithDB without the connection: a freshly-cloned database's
// DSN, as the app role, for tests that need to configure their own pool
// (pgxpool reads pool_max_conns and friends from the connection string).
func WithDSN(t *testing.T) string {
	t.Helper()
	return withCredentials(gdbtest.WithCommittedAdminDSN(t), appRole, appPassword)
}

// WithOwnerDSN is a freshly-cloned database's DSN as its owner, for a
// test that boots the whole server: the server migrates and ensures the app
// role as the owner, then serves as the app role (see AppCredentials).
func WithOwnerDSN(t *testing.T) string {
	t.Helper()
	return gdbtest.WithCommittedAdminDSN(t)
}

// AppCredentials are the app role's name and password. A booted server
// must be given these: the role is cluster-wide, so ensuring it with any
// other password would change it under every other test's pool.
func AppCredentials() (role, password string) { return appRole, appPassword }

// WithOwnerDB is a freshly-cloned database connected as the owner, which
// RLS does not bind. For the rare test that has to see across tenants to
// prove a write landed where it should -- never for exercising the code
// under test, which runs as the app role.
func WithOwnerDB(t *testing.T) (app, owner *db.DB) {
	t.Helper()
	ownerDSN := gdbtest.WithCommittedAdminDSN(t)
	ctx := context.Background()
	o, err := db.New(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("dbtest: connect clone as owner: %v", err)
	}
	t.Cleanup(o.Close)
	a, err := db.New(ctx, withCredentials(ownerDSN, appRole, appPassword))
	if err != nil {
		t.Fatalf("dbtest: connect clone as app role: %v", err)
	}
	t.Cleanup(a.Close)
	return a, o
}

func withCredentials(dsn, user, password string) string {
	withUser, err := db.WithCredentials(dsn, user, password)
	if err != nil {
		panic(fmt.Errorf("dbtest: %w", err))
	}
	return withUser
}

// WithEmptyDB gives the test a fresh, unmigrated database. Migration tests
// need one: WithDB hands out clones of the already-migrated template, which
// is precisely what an up/down test must not start from.
func WithEmptyDB(t *testing.T) *db.DB {
	t.Helper()
	conn, err := db.New(context.Background(), gdbtest.WithEmptyAdminDSN(t))
	if err != nil {
		t.Fatalf("dbtest: connect empty database: %v", err)
	}
	t.Cleanup(conn.Close)
	return conn
}

// NewTenant inserts a row into global_tenants directly, through the global
// handle, and returns its id.
//
// db.WithTenant alone is enough for most tests: RLS scopes rows by
// whatever tenant a context names, whether or not that tenant is a real row
// anywhere. It stops being enough the moment something takes a real foreign
// key on global_tenants -- global_agent_credentials does, so any test that
// creates an agent (directly, or through agent.Service) needs a genuine
// row here first, or the insert fails a constraint that has nothing to do
// with what the test is checking.
func NewTenant(t *testing.T, database *db.DB) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := database.Global().Exec(context.Background(),
		`INSERT INTO global_tenants (id) VALUES ($1)`, id); err != nil {
		t.Fatalf("dbtest: create tenant: %v", err)
	}
	return id
}

// ArchiveTenant marks the tenant archived the way the archive service
// leaves it, without the service's checks (an owner, a second tenant).
func ArchiveTenant(t *testing.T, database *db.DB, id uuid.UUID) {
	t.Helper()
	if _, err := database.Global().Exec(context.Background(),
		`UPDATE global_tenants SET archived_at = now(), archived_by = $2 WHERE id = $1`, id, NewUser(t, database)); err != nil {
		t.Fatalf("dbtest: archive tenant: %v", err)
	}
}

// UnarchiveTenant undoes ArchiveTenant.
func UnarchiveTenant(t *testing.T, database *db.DB, id uuid.UUID) {
	t.Helper()
	if _, err := database.Global().Exec(context.Background(),
		`UPDATE global_tenants SET archived_at = NULL, archived_by = NULL WHERE id = $1`, id); err != nil {
		t.Fatalf("dbtest: unarchive tenant: %v", err)
	}
}

// NewUser inserts a row into global_users directly, through the global
// handle, and returns its id -- for tests exercising comments or tickets
// authored by a signed-in person, where author_user_id and
// created_by_user_id both carry a real foreign key to this table.
func NewUser(t *testing.T, database *db.DB) uuid.UUID {
	t.Helper()
	id := uuid.New()
	email := "test-" + id.String() + "@example.invalid"
	if _, err := database.Global().Exec(context.Background(), `
		INSERT INTO global_users (id, email, password_hash) VALUES ($1, $2, 'unused')`,
		id, email); err != nil {
		t.Fatalf("dbtest: create user: %v", err)
	}
	return id
}
