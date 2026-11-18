package dbtest

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/go-db/engine"
	"github.com/lowaa/slop-factory/go-db/internal/dbtestutil"
	"github.com/lowaa/slop-factory/go-db/migrations"
)

// setupTemplateDB creates and migrates a database dedicated to being a
// CREATE DATABASE ... TEMPLATE source. Postgres refuses to clone a database
// that has any other open connections, so this must be a database nothing
// else ever holds a persistent connection to -- unlike the main db, which
// adminEng/appEng stay connected to for the whole test binary run. The
// engine used to migrate/grant it is explicitly closed before returning.
func setupTemplateDB(ctx context.Context) error {
	maintConnStr, err := ctr.MaintenanceConnString()
	if err != nil {
		return err
	}
	maintEng := engine.NewPostgres(maintConnStr)
	if err := maintEng.Connect(ctx); err != nil {
		return fmt.Errorf("connect maintenance engine: %w", err)
	}
	defer func() { _ = maintEng.Close() }()

	if _, err := maintEng.ExecContext(ctx, fmt.Sprintf(`CREATE DATABASE %s`, ctr.TemplateDBName())); err != nil {
		return fmt.Errorf("create template db: %w", err)
	}

	templateConnStr, err := dbtestutil.WithDatabaseName(ctr.ConnString, ctr.TemplateDBName())
	if err != nil {
		return err
	}
	templateEng := engine.NewPostgres(templateConnStr)
	if err := templateEng.Connect(ctx); err != nil {
		return fmt.Errorf("connect template engine: %w", err)
	}
	// Closed (not deferred past this function) so no lingering pool
	// connection blocks later CREATE DATABASE ... TEMPLATE clones.
	defer func() { _ = templateEng.Close() }()

	if opts.MigrateTemplate != nil {
		if err := opts.MigrateTemplate(ctx, templateConnStr); err != nil {
			return fmt.Errorf("migrate template db: %w", err)
		}
		return nil
	}
	if err := migrations.Up(ctx, templateEng, packMigrationsFS); err != nil {
		return fmt.Errorf("migrate template db: %w", err)
	}
	if _, err := ctr.EnsureAppRole(ctx, templateEng); err != nil {
		return fmt.Errorf("grant app role on template db: %w", err)
	}

	return nil
}

// WithCommittedDB clones the migrated template database into a fresh,
// uniquely named database and connects to it as the app role, registering
// cleanup (closing the connection, then dropping the cloned database) via
// t.Cleanup. Unlike WithTenantTx, nothing here runs inside an outer
// transaction: this is Mode B, for tests that need to actually commit or
// roll back their own transactions and observe the result -- e.g.
// transactional behavior itself, or side effects visible to other
// connections.
//
// The app role is RLS-subject like production code, which is what most Mode
// B tests want. Migration up/down verification (Mode C) is the one
// exception -- it needs to create/drop tables and roles -- so it uses
// WithCommittedAdminDB instead.
func WithCommittedDB(t *testing.T) engine.Engine {
	t.Helper()
	return connectToClone(t, cloneTemplateDB(t), appConnStr)
}

// WithCommittedAdminDB is WithCommittedDB, but connects as the schema-owning
// superuser rather than the app role. Migration up/down verification (Mode
// C) needs this: golang-migrate must create/drop tables and manage its own
// schema_migrations table, none of which the app role has privileges for.
func WithCommittedAdminDB(t *testing.T) engine.Engine {
	t.Helper()
	return connectToClone(t, cloneTemplateDB(t), ctr.ConnString)
}

// WithCommittedDSN clones the template like WithCommittedDB but returns the
// clone's app-role connection string instead of a connection, for callers
// that wrap the database in their own client (e.g. pgx-native). Dropping the
// clone is registered via t.Cleanup; closing the caller's connections is the
// caller's, registered after this returns so it runs first.
func WithCommittedDSN(t *testing.T) string {
	t.Helper()
	if appConnStr == "" {
		t.Fatalf("dbtest: no app role: MigrateTemplate harnesses use WithCommittedAdminDSN and set up their own roles")
	}
	return cloneDSN(t, appConnStr)
}

// WithCommittedAdminDSN is WithCommittedDSN as the superuser.
func WithCommittedAdminDSN(t *testing.T) string {
	t.Helper()
	return cloneDSN(t, ctr.ConnString)
}

func cloneDSN(t *testing.T, baseConnStr string) string {
	t.Helper()
	dsn, err := dbtestutil.WithDatabaseName(baseConnStr, cloneTemplateDB(t))
	if err != nil {
		t.Fatalf("dbtest: build clone conn string: %v", err)
	}
	return dsn
}

// explain appends the Diagnose hook's report, if any, to a failure message.
func explain(msg string) string {
	if opts.Diagnose == nil {
		return msg
	}
	return msg + "\n" + opts.Diagnose(context.Background())
}

// AdminDSN is the superuser connection string to the package's main
// database, for work that has to happen outside any one clone, such as
// creating a cluster-wide role before a database is created with it as owner.
func AdminDSN() string { return ctr.ConnString }

// WithEmptyAdminDSN creates a fresh, unmigrated database and returns its
// superuser connection string. Migration tests need one: clones start from
// the already-migrated template. Dropping it is registered via t.Cleanup;
// closing the caller's connections is the caller's, registered after this
// returns so it runs first.
func WithEmptyAdminDSN(t *testing.T) string {
	t.Helper()
	return WithEmptyAdminDSNOwnedBy(t, "")
}

// WithEmptyAdminDSNOwnedBy is WithEmptyAdminDSN with the database owned by
// owner, a role that must already exist, for migrating as something less
// privileged than the superuser. An empty owner leaves it the superuser's.
func WithEmptyAdminDSNOwnedBy(t *testing.T, owner string) string {
	t.Helper()
	name := createDatabase(t, "", owner)
	dsn, err := dbtestutil.WithDatabaseName(ctr.ConnString, name)
	if err != nil {
		t.Fatalf("dbtest: build empty db conn string: %v", err)
	}
	return dsn
}

// cloneTemplateDB clones the template database into a fresh, uniquely named
// database and registers its cleanup (dropping it) via t.Cleanup. It
// returns the new database's name; callers connect to it themselves via
// connectToClone, potentially as different roles.
func cloneTemplateDB(t *testing.T) string {
	t.Helper()
	return createDatabase(t, ctr.TemplateDBName(), "")
}

// createDatabase creates a uniquely named database -- a clone of template
// when one is named, empty otherwise -- with the given owner if non-empty,
// and registers its drop via t.Cleanup.
func createDatabase(t *testing.T, template, owner string) string {
	t.Helper()
	ctx := context.Background()

	dbName := "go_db_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")

	maintConnStr, err := ctr.MaintenanceConnString()
	if err != nil {
		t.Fatalf("dbtest: maintenance conn string: %v", err)
	}
	maintEng := engine.NewPostgres(maintConnStr)
	if err := maintEng.Connect(ctx); err != nil {
		t.Fatalf("%s", explain(fmt.Sprintf("dbtest: connect maintenance engine: %v", err)))
	}
	defer func() { _ = maintEng.Close() }()

	create := "CREATE DATABASE " + dbName
	if template != "" {
		// CREATE DATABASE ... TEMPLATE requires zero other connections to the
		// source database. setupTemplateDB explicitly closes its connection to
		// the template db before returning, but Postgres can take a moment to
		// notice the client socket closed and drop the backend from
		// pg_stat_activity, so a clone immediately afterwards can otherwise lose
		// this race. Terminating any lingering backend explicitly makes this
		// deterministic instead of papering over it with a retry/sleep loop.
		if _, err := maintEng.ExecContext(ctx, fmt.Sprintf(
			`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s' AND pid <> pg_backend_pid()`,
			template,
		)); err != nil {
			t.Fatalf("%s", explain(fmt.Sprintf("dbtest: terminate lingering template db connections: %v", err)))
		}
		create += " TEMPLATE " + template
	}
	if owner != "" {
		create += " OWNER " + owner
	}
	if _, err := maintEng.ExecContext(ctx, create); err != nil {
		t.Fatalf("%s", explain(fmt.Sprintf("dbtest: create database: %v", err)))
	}

	t.Cleanup(func() {
		dropDatabase(t, dbName)
	})

	return dbName
}

// connectToClone rewrites baseConnStr to point at dbName, connects, and
// registers the connection's closure via t.Cleanup (which runs before the
// dropDatabase cleanup cloneTemplateDB already registered, since
// t.Cleanup runs last-registered-first).
func connectToClone(t *testing.T, dbName, baseConnStr string) engine.Engine {
	t.Helper()

	dbConnStr, err := dbtestutil.WithDatabaseName(baseConnStr, dbName)
	if err != nil {
		t.Fatalf("dbtest: build clone conn string: %v", err)
	}
	eng := engine.NewPostgres(dbConnStr)
	if err := eng.Connect(context.Background()); err != nil {
		t.Fatalf("%s", explain(fmt.Sprintf("dbtest: connect to cloned db: %v", err)))
	}
	t.Cleanup(func() { _ = eng.Close() })

	return eng
}

func dropDatabase(t *testing.T, dbName string) {
	t.Helper()
	ctx := context.Background()

	maintConnStr, err := ctr.MaintenanceConnString()
	if err != nil {
		t.Logf("dbtest: maintenance conn string during cleanup: %v", err)
		return
	}
	maintEng := engine.NewPostgres(maintConnStr)
	if err := maintEng.Connect(ctx); err != nil {
		t.Log(explain(fmt.Sprintf("dbtest: connect maintenance engine during cleanup: %v", err)))
		return
	}
	defer func() { _ = maintEng.Close() }()

	if _, err := maintEng.ExecContext(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, dbName)); err != nil {
		t.Log(explain(fmt.Sprintf("dbtest: drop database %s: %v", dbName, err)))
	}
}
