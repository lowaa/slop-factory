package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/smoke"
)

func TestMain(m *testing.M) {
	dbtest.Main(m, schema.Migrations)
}

// setUp points run() at a freshly migrated database, through the
// environment it reads, and returns a store to read back what it did.
func setUp(t *testing.T) oauth.Store {
	t.Helper()
	dsn := dbtest.WithDSN(t)
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("PUBLIC_BASE_URL", "https://slop.example")
	database, err := db.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(database.Close)
	return oauth.NewPostgresStore(database)
}

// create prints the whole credential as JSON -- stdout is the credential
// and nothing else, so it can be piped straight into a secret -- and the
// client it names holds a read-only grant in the smoke tenant.
func TestCreatePrintsTheCredentialAndRevokeEndsIt(t *testing.T) {
	store := setUp(t)

	var out bytes.Buffer
	require.NoError(t, run(context.Background(), []string{"create", "--name=ci"}, &out))
	var cred smoke.Credential
	dec := json.NewDecoder(strings.NewReader(out.String()))
	dec.DisallowUnknownFields()
	require.NoError(t, dec.Decode(&cred), out.String())
	assert.True(t, strings.HasPrefix(cred.ClientID, "ro-"), cred.ClientID)
	assert.NotEmpty(t, cred.ClientSecret)
	assert.Positive(t, cred.TicketID)
	assert.False(t, dec.More(), "nothing on stdout but the credential")

	g, err := store.ClientGrant(context.Background(), cred.ClientID)
	require.NoError(t, err)
	assert.Equal(t, oauth.ScopeRead, g.Scope)
	assert.Equal(t, smoke.TenantID, g.TenantID)
	assert.Equal(t, "https://slop.example/mcp", g.Resource)

	out.Reset()
	require.NoError(t, run(context.Background(), []string{"revoke", "--client-id=" + cred.ClientID}, &out))
	_, err = store.ClientGrant(context.Background(), cred.ClientID)
	assert.ErrorIs(t, err, oauth.ErrNotFound)

	assert.ErrorIs(t, run(context.Background(), []string{"revoke", "--client-id=ro-nope"}, &out), oauth.ErrNotFound)
}

// Every malformed invocation is refused before it touches a database.
func TestMalformedInvocationsAreRefused(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://unreachable.invalid/db")
	t.Setenv("PUBLIC_BASE_URL", "https://slop.example")
	for name, args := range map[string][]string{
		"nothing":            nil,
		"create, no name":    {"create"},
		"revoke, no id":      {"revoke"},
		"an unknown command": {"rotate", "--name=ci"},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			err := run(context.Background(), args, &out)
			assert.ErrorContains(t, err, "usage:")
			assert.Empty(t, out.String())
		})
	}
}
