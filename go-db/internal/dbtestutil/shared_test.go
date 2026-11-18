package dbtestutil

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-db/engine"
)

func connect(t *testing.T, connString string) engine.Engine {
	t.Helper()
	eng := engine.NewPostgres(connString)
	require.NoError(t, eng.Connect(context.Background()))
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

func databaseExists(t *testing.T, eng engine.Engine, name string) bool {
	t.Helper()
	var n int
	require.NoError(t, eng.QueryRowContext(context.Background(),
		`SELECT count(*) FROM pg_database WHERE datname = $1`, name).Scan(&n))
	return n == 1
}

// Two packages on one shared server must each get a database of their own,
// or their migrations and data would collide.
func TestSharedServerGivesEachPackageItsOwnDatabase(t *testing.T) {
	ctx := context.Background()
	server, err := StartPostgres(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Terminate(ctx) })

	t.Setenv(SharedURLEnv, server.ConnString)
	a, err := StartPostgres(ctx)
	require.NoError(t, err)
	b, err := StartPostgres(ctx)
	require.NoError(t, err)

	require.NotEqual(t, a.DBName, b.DBName)
	require.NotEqual(t, a.ConnString, b.ConnString)
	require.NotEqual(t, a.TemplateDBName(), b.TemplateDBName())

	engA, engB := connect(t, a.ConnString), connect(t, b.ConnString)
	_, err = engA.ExecContext(ctx, `CREATE TABLE only_in_a (id int)`)
	require.NoError(t, err)
	_, err = engB.ExecContext(ctx, `CREATE TABLE only_in_a (id int)`)
	require.NoError(t, err, "package B must not see package A's table")

	admin := connect(t, server.ConnString)
	require.True(t, databaseExists(t, admin, a.DBName))

	require.NoError(t, a.Terminate(ctx))
	require.False(t, databaseExists(t, admin, a.DBName), "Terminate drops the package's database")
	require.True(t, databaseExists(t, admin, b.DBName), "and leaves other packages' databases alone")
	require.NoError(t, b.Terminate(ctx))
}
