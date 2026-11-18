package login_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/login"
)

// fakeTable is global_auth_providers in memory: the insert keeps the first
// issuer per name, as ON CONFLICT DO NOTHING does, and the select reads it
// back. The SQL itself is exercised against a real schema by the services
// that create the table.
type fakeTable struct {
	issuers map[string]string
	failing error
}

func (f *fakeTable) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	if f.failing != nil {
		return pgconn.CommandTag{}, f.failing
	}
	name, issuer := args[0].(string), args[1].(string)
	if _, ok := f.issuers[name]; !ok {
		f.issuers[name] = issuer
	}
	return pgconn.CommandTag{}, nil
}

func (f *fakeTable) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	return row{issuer: f.issuers[args[0].(string)]}
}

type row struct{ issuer string }

func (r row) Scan(dest ...any) error {
	*dest[0].(*string) = r.issuer
	return nil
}

func TestRegisterBindsANameToItsFirstIssuer(t *testing.T) {
	bindings := login.NewIssuerBindings(&fakeTable{issuers: map[string]string{}})
	ctx := context.Background()

	require.NoError(t, bindings.Register(ctx, "dex", "https://dex.example"))
	require.NoError(t, bindings.Register(ctx, "dex", "https://dex.example"), "a restart with the same issuer")
	require.NoError(t, bindings.Register(ctx, "github", login.GitHubIssuer), "names are independent")
	require.NoError(t, bindings.Register(ctx, "dex", "https://dex.example/"), "the same issuer with a trailing slash")

	err := bindings.Register(ctx, "dex", "https://other.example")
	require.Error(t, err, "the name may never move to another issuer")
	assert.Contains(t, err.Error(), "https://dex.example")
	assert.Contains(t, err.Error(), "https://other.example")
}

func TestRegisterReportsAFailedWrite(t *testing.T) {
	down := errors.New("database down")
	err := login.NewIssuerBindings(&fakeTable{failing: down}).Register(context.Background(), "dex", "https://dex.example")
	assert.ErrorIs(t, err, down)
}

func TestNormalizeIssuerIgnoresATrailingSlash(t *testing.T) {
	assert.Equal(t, "https://dex.example/dex", login.NormalizeIssuer("https://dex.example/dex/"))
	assert.Equal(t, "https://dex.example/dex", login.NormalizeIssuer("https://dex.example/dex"))
	assert.Equal(t, "https://dex.example/dex", login.NormalizeIssuer(" https://dex.example/dex/ "))
}
