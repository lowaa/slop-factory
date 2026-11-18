package optionstest

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"

	"github.com/lowaa/slop-factory/go-db/dbtest"
	"github.com/lowaa/slop-factory/go-db/engine"
	"github.com/lowaa/slop-factory/go-db/internal/dbtestutil"
)

func TestMain(m *testing.M) {
	dbtest.MainWithOptions(m, nil, dbtest.Options{
		Container: dbtest.ContainerOptions{
			Customizers: []testcontainers.ContainerCustomizer{
				testcontainers.WithCmd("postgres", "-c", "max_connections=57"),
			},
		},
		MigrateTemplate: func(ctx context.Context, connStr string) error {
			eng := engine.NewPostgres(connStr)
			if err := eng.Connect(ctx); err != nil {
				return err
			}
			defer func() { _ = eng.Close() }()
			_, err := eng.ExecContext(ctx, `CREATE TABLE custom_marker (id int)`)
			return err
		},
	})
}

func connect(t *testing.T, dsn string) engine.Engine {
	t.Helper()
	eng := engine.NewPostgres(dsn)
	require.NoError(t, eng.Connect(context.Background()))
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

func TestCustomTemplateIsClonedPerTest(t *testing.T) {
	ctx := context.Background()
	a := connect(t, dbtest.WithCommittedAdminDSN(t))
	b := connect(t, dbtest.WithCommittedAdminDSN(t))

	_, err := a.ExecContext(ctx, `INSERT INTO custom_marker VALUES (1)`)
	require.NoError(t, err)

	var inA, inB int
	require.NoError(t, a.QueryRowContext(ctx, `SELECT count(*) FROM custom_marker`).Scan(&inA))
	require.NoError(t, b.QueryRowContext(ctx, `SELECT count(*) FROM custom_marker`).Scan(&inB))
	assert.Equal(t, 1, inA)
	assert.Equal(t, 0, inB, "clones share nothing; the template stays empty")
}

func TestContainerCustomizersApply(t *testing.T) {
	if os.Getenv(dbtestutil.SharedURLEnv) != "" {
		t.Skip("container options do not apply to a shared server")
	}
	eng := connect(t, dbtest.WithCommittedAdminDSN(t))
	var got string
	require.NoError(t, eng.QueryRowContext(context.Background(), `SHOW max_connections`).Scan(&got))
	assert.Equal(t, "57", got)
}

func TestAppDSNUnavailableWithoutDefaultRole(t *testing.T) {
	inner := &testing.T{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		dbtest.WithCommittedDSN(inner)
	}()
	<-done
	assert.True(t, inner.Failed())
}
