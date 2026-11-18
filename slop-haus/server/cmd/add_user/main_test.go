package main

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

func TestMain(m *testing.M) {
	dbtest.Main(m, schema.Migrations)
}

// setUp gives the test a freshly migrated database's DSN, and a way to read
// back what run() did. run() reads DATABASE_URL and ADD_USER_PASSWORD from
// the environment itself rather than taking them as parameters (see its own
// comment on why: a flag is visible in the process list), so every test
// sets them with t.Setenv rather than passing them in.
func setUp(t *testing.T) (dsn string, findUser func(email string) *user.User) {
	t.Helper()
	dsn = dbtest.WithDSN(t)
	database, err := db.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(database.Close)
	store := user.NewPostgresStore(database)
	return dsn, func(email string) *user.User {
		t.Helper()
		u, err := store.FindByEmail(context.Background(), email)
		require.NoError(t, err)
		return u
	}
}

func TestRun_CreatesAnActiveAccount(t *testing.T) {
	dsn, findUser := setUp(t)
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("ADD_USER_PASSWORD", "correct horse battery staple")

	err := run([]string{"--email=new@example.com", "--display-name=New Person"})
	require.NoError(t, err)

	u := findUser("new@example.com")
	assert.True(t, u.IsActive, "the account works without an emailed link")
	assert.Equal(t, "New Person", u.DisplayName)
	assert.False(t, u.IsAdmin)
	assert.NotEmpty(t, u.PasswordHash)
}

func TestRun_StartsTheSeededTenantWithProvisionFromBoardOn(t *testing.T) {
	dsn, findUser := setUp(t)
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("ADD_USER_PASSWORD", "correct horse battery staple")
	require.NoError(t, run([]string{"--email=seed@example.com", "--display-name=Seed"}))

	database, err := db.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(database.Close)
	var tenant uuid.UUID
	require.NoError(t, database.Global().QueryRow(context.Background(),
		`SELECT tenant FROM global_tenant_memberships WHERE user_id = $1`, findUser("seed@example.com").ID).Scan(&tenant))

	svc := settings.NewService(settings.NewPostgresStore(database), audit.New(slog.DiscardHandler, nil), slog.DiscardHandler)
	on, err := svc.ProvisionFromBoardEnabled(db.WithTenant(context.Background(), tenant))
	require.NoError(t, err)
	assert.True(t, on)
}

// EnsureActive's whole point is that running this twice -- once per
// environment start -- must not touch an account that already exists.
func TestRun_IsIdempotent_SecondRunLeavesThePasswordUnchanged(t *testing.T) {
	dsn, findUser := setUp(t)
	t.Setenv("DATABASE_URL", dsn)

	t.Setenv("ADD_USER_PASSWORD", "first-password")
	require.NoError(t, run([]string{"--email=repeat@example.com", "--display-name=First Name"}))
	first := findUser("repeat@example.com")

	// A different password and display name on the second run: if either
	// landed, this would not be catching idempotence at all.
	t.Setenv("ADD_USER_PASSWORD", "second-password")
	require.NoError(t, run([]string{"--email=repeat@example.com", "--display-name=Second Name"}))
	second := findUser("repeat@example.com")

	assert.Equal(t, first.PasswordHash, second.PasswordHash, "an existing account's password is left alone")
	assert.Equal(t, "First Name", second.DisplayName, "and so is its display name")
}

// --admin is the one thing applied to an existing account too, so an
// environment's first account (already created by an earlier run) can be
// made the admin who invites everyone else.
func TestRun_AdminPromotesAnExistingAccount(t *testing.T) {
	dsn, findUser := setUp(t)
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("ADD_USER_PASSWORD", "a-password")

	require.NoError(t, run([]string{"--email=admin@example.com", "--display-name=Future Admin"}))
	require.False(t, findUser("admin@example.com").IsAdmin, "not an admin yet")

	require.NoError(t, run([]string{"--email=admin@example.com", "--display-name=Future Admin", "--admin"}))
	assert.True(t, findUser("admin@example.com").IsAdmin)
}

// Every one of these is required for run() to do anything at all; missing
// any one of them must fail with the usage message rather than, say, a nil
// pointer further down or a silent no-op.
func TestRun_MissingRequiredInputIsAUsageError(t *testing.T) {
	dsn, _ := setUp(t)
	cases := map[string]struct {
		env  map[string]string
		args []string
	}{
		"no email":        {map[string]string{"DATABASE_URL": dsn, "ADD_USER_PASSWORD": "pw"}, []string{"--display-name=X"}},
		"no display name": {map[string]string{"DATABASE_URL": dsn, "ADD_USER_PASSWORD": "pw"}, []string{"--email=x@example.com"}},
		"no password":     {map[string]string{"DATABASE_URL": dsn}, []string{"--email=x@example.com", "--display-name=X"}},
		"no database url": {map[string]string{"ADD_USER_PASSWORD": "pw"}, []string{"--email=x@example.com", "--display-name=X"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", tc.env["DATABASE_URL"])
			t.Setenv("ADD_USER_PASSWORD", tc.env["ADD_USER_PASSWORD"])

			err := run(tc.args)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "usage:")
		})
	}
}
