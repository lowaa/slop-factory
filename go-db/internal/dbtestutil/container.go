// Package dbtestutil holds Postgres testcontainer bootstrap logic shared by
// the tests of engine, tenancy, and migrations. Those three packages sit
// below dbtest in the module's dependency graph, so none of their tests can
// import the public dbtest package without creating a cycle; this internal
// package is the one place that boilerplate lives instead of being copied
// three times. dbtest itself also builds on this for its own container
// lifecycle.
package dbtestutil

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/lowaa/slop-factory/go-db/engine"
	"github.com/lowaa/slop-factory/go-db/migrations"
)

const Image = "postgres:16-alpine"

// SharedURLEnv, when set to a superuser connection string, makes
// StartPostgres use that already-running server instead of starting a
// container: a test run (test.sh) starts one Postgres for all its packages
// rather than every package paying a container start of its own. Each
// package still gets a database of its own on it, so packages never see
// each other's data.
//
// A shared server is shared at the cluster level too: roles, pg_stat_activity
// and pg_locks show every package's. Tests that create fixed-name roles or
// databases, or probe those views for their own sessions only, belong on
// StartDedicatedPostgres.
const SharedURLEnv = "DBTEST_POSTGRES_URL"

// AppRole/AppPassword name the least-privileged role RLS-dependent tests
// must connect as. This matters because the postgres Docker image's default
// user (the one ConnString authenticates as) is a superuser, and Postgres
// row security -- even with FORCE ROW LEVEL SECURITY -- is always bypassed
// by superusers and by any role with BYPASSRLS. A test that queries through
// ConnString's role would see every row regardless of tenant scoping and
// silently prove nothing.
const (
	AppRole     = "go_db_app"
	AppPassword = "go_db_app_password"
)

// Container wraps a running Postgres testcontainer.
type Container struct {
	ctr *tcpostgres.PostgresContainer
	// DBName is the database ConnString points at. Databases a package
	// derives from it are named with it as a prefix (see TemplateDBName), so
	// Terminate can drop them all on a shared server.
	DBName string
	// sharedMaintURL is set when the server is shared (SharedURLEnv); the
	// container is then not ours to stop, only this package's databases are.
	sharedMaintURL string
	// ConnString authenticates as a superuser. It's the right connection
	// for running migrations (which need to create tables/roles/policies),
	// but must never be used for tenancy-scoped queries -- see AppRole.
	ConnString string
}

// StartPostgres starts a fresh Postgres container and returns its connection
// string (sslmode disabled, suitable for local/test use only).
// With SharedURLEnv set it uses that server instead (see there).
func StartPostgres(ctx context.Context) (*Container, error) {
	if sharedURL := os.Getenv(SharedURLEnv); sharedURL != "" {
		return useSharedPostgres(ctx, sharedURL)
	}
	return StartDedicatedPostgres(ctx)
}

// StartDedicatedPostgres always starts a container of its own, ignoring
// SharedURLEnv.
func StartDedicatedPostgres(ctx context.Context) (*Container, error) {
	return StartDedicatedPostgresWith(ctx, nil)
}

// StartOptions tunes the container StartDedicatedPostgresWith starts.
type StartOptions struct {
	// Image overrides Image.
	Image string
	// Customizers are applied after the defaults, so they can add labels or
	// server flags and replace the wait strategy.
	Customizers []testcontainers.ContainerCustomizer
}

// StartDedicatedPostgresWith is StartDedicatedPostgres with the container
// tuned by opts; nil means the defaults.
func StartDedicatedPostgresWith(ctx context.Context, opts *StartOptions) (*Container, error) {
	if opts == nil {
		opts = &StartOptions{}
	}
	image := Image
	if opts.Image != "" {
		image = opts.Image
	}
	ctr, err := startWithRetry(ctx, startAttempts, startBackoff,
		func(ctx context.Context) (*tcpostgres.PostgresContainer, error) {
			customizers := append([]testcontainers.ContainerCustomizer{
				tcpostgres.WithDatabase("go_db_test"),
				tcpostgres.WithUsername("go_db_test"),
				tcpostgres.WithPassword("go_db_test"),
				tcpostgres.BasicWaitStrategies(),
			}, opts.Customizers...)
			return tcpostgres.Run(ctx, image, customizers...)
		},
		func(c *tcpostgres.PostgresContainer) { _ = testcontainers.TerminateContainer(c) })
	if err != nil {
		return nil, fmt.Errorf("dbtestutil: start postgres container: %w", err)
	}

	connString, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return nil, fmt.Errorf("dbtestutil: connection string: %w", err)
	}

	return &Container{ctr: ctr, ConnString: connString, DBName: "go_db_test"}, nil
}

// useSharedPostgres creates a uniquely named database on the server at
// sharedURL and returns a Container pointing at it.
func useSharedPostgres(ctx context.Context, sharedURL string) (*Container, error) {
	maintURL, err := WithDatabaseName(sharedURL, "postgres")
	if err != nil {
		return nil, err
	}
	dbName := "go_db_pkg_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := execOnMaintenance(ctx, maintURL, "CREATE DATABASE "+dbName); err != nil {
		return nil, fmt.Errorf("dbtestutil: create package database on shared server: %w", err)
	}
	connString, err := WithDatabaseName(sharedURL, dbName)
	if err != nil {
		return nil, err
	}
	return &Container{ConnString: connString, DBName: dbName, sharedMaintURL: maintURL}, nil
}

// TemplateDBName names this package's Mode B template database. It carries
// DBName as a prefix so packages sharing a server don't collide on it.
func (c *Container) TemplateDBName() string {
	return c.DBName + "_template"
}

// Terminate stops and removes the container, or on a shared server drops
// this package's databases and leaves the server running.
func (c *Container) Terminate(ctx context.Context) error {
	if c.sharedMaintURL == "" {
		return c.ctr.Terminate(ctx)
	}
	for _, name := range []string{c.TemplateDBName(), c.DBName} {
		if err := execOnMaintenance(ctx, c.sharedMaintURL, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			return fmt.Errorf("dbtestutil: drop %s: %w", name, err)
		}
	}
	return nil
}

func execOnMaintenance(ctx context.Context, maintURL, stmt string) error {
	eng := engine.NewPostgres(maintURL)
	if err := eng.Connect(ctx); err != nil {
		return err
	}
	defer func() { _ = eng.Close() }()
	_, err := eng.ExecContext(ctx, stmt)
	return err
}

// EnsureAppRole creates (if missing) a NOSUPERUSER/NOBYPASSRLS role able to
// read/write every table in the public schema -- including tables created
// by later migrations, via ALTER DEFAULT PRIVILEGES -- and returns a
// connection string authenticating as that role. adminEng must already be
// connected via c.ConnString, and migrations should already have run,
// though running this before or after Up is fine either way since default
// privileges apply retroactively via the ALL TABLES grant and prospectively
// via ALTER DEFAULT PRIVILEGES.
func (c *Container) EnsureAppRole(ctx context.Context, adminEng engine.Engine) (string, error) {
	if err := migrations.EnsureRole(ctx, adminEng, AppRole, AppPassword); err != nil {
		return "", fmt.Errorf("dbtestutil: %w", err)
	}

	u, err := url.Parse(c.ConnString)
	if err != nil {
		return "", fmt.Errorf("dbtestutil: parse conn string: %w", err)
	}
	u.User = url.UserPassword(AppRole, AppPassword)

	return u.String(), nil
}

// MaintenanceConnString returns a connection string authenticating as the
// superuser but pointed at Postgres's always-present "postgres" database,
// rather than at c.ConnString's database. CREATE DATABASE/DROP DATABASE (and
// CREATE DATABASE ... TEMPLATE, used by Mode B's template-clone strategy)
// must run outside the database being created, dropped, or cloned, so admin
// connections doing that work need to target a different database entirely.
func (c *Container) MaintenanceConnString() (string, error) {
	return WithDatabaseName(c.ConnString, "postgres")
}

// WithDatabaseName returns connString with its database name replaced.
func WithDatabaseName(connString, dbName string) (string, error) {
	u, err := url.Parse(connString)
	if err != nil {
		return "", fmt.Errorf("dbtestutil: parse conn string: %w", err)
	}
	u.Path = "/" + dbName
	return u.String(), nil
}
