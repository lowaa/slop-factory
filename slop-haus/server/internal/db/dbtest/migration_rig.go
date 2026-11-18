package dbtest

import (
	"context"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	gdbtest "github.com/lowaa/slop-factory/go-db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

const (
	migratorRole     = "slophaus_migrator_test"
	migratorPassword = "slophaus_migrator_password"
)

var migratorRoleOnce sync.Once

// MigrationRig is an empty database that migrations are applied to one
// version at a time, as production applies them: by a role that owns the
// tables but is neither a superuser nor BYPASSRLS, with the app role
// ensured after each step as the server does on boot.
//
// The container's default role is a superuser, which row level security
// never binds. Migrating as it hides every migration that touches rows
// without a tenant set -- a backfill once saw no rows under the FORCEd
// policies in production and failed the deploy, while each test that ran
// it as the superuser, on empty tables, passed.
type MigrationRig struct {
	// Owner is the migrating role. FORCE ROW LEVEL SECURITY binds it, and it
	// sees no tenant's rows without a tenant set.
	Owner *db.DB
	// App is the role the server serves as.
	App *db.DB
	// Admin is a superuser on the same database: the one handle that sees every
	// tenant's rows at once, so a test can tell "the app role cannot see it"
	// from "it is gone".
	Admin *db.DB

	// OwnerDSN is how the owner connects: what a server's DATABASE_URL is.
	OwnerDSN string

	fsys     fs.FS
	versions []int64
}

// NewMigrationRig creates the database, with nothing migrated yet.
func NewMigrationRig(t *testing.T, fsys fs.FS) *MigrationRig {
	t.Helper()
	ctx := context.Background()

	versions, err := MigrationVersions(fsys)
	require.NoError(t, err)

	ensureMigratorRole(t)
	adminDSN := gdbtest.WithEmptyAdminDSNOwnedBy(t, migratorRole)
	r := &MigrationRig{fsys: fsys, versions: versions}
	r.Admin = connect(t, adminDSN)
	r.OwnerDSN = withCredentials(adminDSN, migratorRole, migratorPassword)
	r.Owner = connect(t, r.OwnerDSN)
	r.App = connect(t, withCredentials(adminDSN, appRole, appPassword))
	require.NoError(t, db.EnsureAppRole(ctx, r.Owner, appRole, appPassword))
	return r
}

func connect(t *testing.T, dsn string) *db.DB {
	t.Helper()
	conn, err := db.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	return conn
}

func ensureMigratorRole(t *testing.T) {
	t.Helper()
	migratorRoleOnce.Do(func() {
		admin, err := db.New(context.Background(), gdbtest.AdminDSN())
		require.NoError(t, err)
		defer admin.Close()
		_, err = admin.Global().Exec(context.Background(), fmt.Sprintf(
			`DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '%[1]s') THEN
			   CREATE ROLE %[1]s LOGIN NOSUPERUSER NOBYPASSRLS PASSWORD '%[2]s'; END IF;
			 EXCEPTION WHEN duplicate_object OR unique_violation THEN NULL; END $$`,
			migratorRole, migratorPassword))
		require.NoError(t, err)
	})
}

// Versions are every migration's version, ascending.
func (r *MigrationRig) Versions() []int64 { return append([]int64(nil), r.versions...) }

// MigrateTo applies the pending migrations up to and including version,
// then re-ensures the app role.
func (r *MigrationRig) MigrateTo(ctx context.Context, version int64) error {
	if err := db.Migrate(ctx, r.Owner, upTo(r.fsys, version)); err != nil {
		return err
	}
	return db.EnsureAppRole(ctx, r.Owner, appRole, appPassword)
}

var migrationFile = regexp.MustCompile(`^(\d+)_.*\.sql$`)

// MigrationVersions lists the versions of the migration files at the root
// of fsys, ascending.
func MigrationVersions(fsys fs.FS) ([]int64, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var versions []int64
	for _, e := range entries {
		if m := migrationFile.FindStringSubmatch(e.Name()); m != nil {
			v, err := strconv.ParseInt(m[1], 10, 64)
			if err != nil {
				return nil, err
			}
			versions = append(versions, v)
		}
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
	return versions, nil
}

// upTo hides the migrations after max, which is how a step stops at one
// version without go-db's Up growing a target.
type upToFS struct {
	fs.FS
	max int64
}

func upTo(fsys fs.FS, max int64) fs.FS { return &upToFS{FS: fsys, max: max} }

func (u *upToFS) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := fs.ReadDir(u.FS, name)
	if err != nil {
		return nil, err
	}
	kept := entries[:0:0]
	for _, e := range entries {
		if m := migrationFile.FindStringSubmatch(e.Name()); m != nil {
			if v, _ := strconv.ParseInt(m[1], 10, 64); v > u.max {
				continue
			}
		}
		kept = append(kept, e)
	}
	return kept, nil
}
