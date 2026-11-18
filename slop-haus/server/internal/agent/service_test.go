package agent_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
)

// stubTokenResolver is a plain map from token to the tenant and agent it
// authenticates -- the simplest thing that satisfies agent.Grants,
// standing in for internal/oauth so these tests can drive
// Service.Authenticate directly rather than through a whole OAuth grant,
// which is internal/oauth's own tests' job.
type stubTokenResolver map[string]struct {
	tenant, agent uuid.UUID
	readOnly      bool
}

func (m stubTokenResolver) Authenticate(_ context.Context, token string) (*oauth.Access, error) {
	cred, ok := m[token]
	if !ok {
		return nil, agent.ErrBadToken
	}
	return &oauth.Access{TenantID: cred.tenant, AgentID: cred.agent, ReadOnly: cred.readOnly}, nil
}

func (stubTokenResolver) RevokeAgentGrants(context.Context, uuid.UUID, uuid.UUID) error { return nil }

var _ agent.Grants = stubTokenResolver{}

func TestAuthenticate_ValidTokenResolvesToTheAgentScopedToItsTenant(t *testing.T) {
	database := dbtest.WithDB(t)
	tenantID := dbtest.NewTenant(t, database)
	store := agent.NewPostgresStore(database)

	svc := agent.NewService(store, &fakeReleaser{}, nil, slog.DiscardHandler)
	created, err := svc.Create(db.WithTenant(context.Background(), tenantID), "builder-1")
	require.NoError(t, err)

	tokens := stubTokenResolver{"good-token": {tenant: tenantID, agent: created.ID}}
	authSvc := agent.NewService(store, &fakeReleaser{}, tokens, slog.DiscardHandler)

	found, err := authSvc.Authenticate(context.Background(), "good-token")
	require.NoError(t, err)
	assert.Equal(t, created.ID, found.ID)
	assert.Equal(t, tenantID, found.Tenant)
	assert.False(t, found.ReadOnly())
}

// A read-only credential's agent carries that through to every transport,
// which is where it is enforced -- the agent itself is the same one.
func TestAuthenticate_ReadOnlyTokenYieldsAReadOnlyAgent(t *testing.T) {
	database := dbtest.WithDB(t)
	tenantID := dbtest.NewTenant(t, database)
	store := agent.NewPostgresStore(database)

	svc := agent.NewService(store, &fakeReleaser{}, nil, slog.DiscardHandler)
	created, err := svc.Create(db.WithTenant(context.Background(), tenantID), "smoke")
	require.NoError(t, err)

	tokens := stubTokenResolver{
		"ro": {tenant: tenantID, agent: created.ID, readOnly: true},
		"rw": {tenant: tenantID, agent: created.ID},
	}
	authSvc := agent.NewService(store, &fakeReleaser{}, tokens, slog.DiscardHandler)

	found, err := authSvc.Authenticate(context.Background(), "ro")
	require.NoError(t, err)
	assert.True(t, found.ReadOnly())

	found, err = authSvc.Authenticate(context.Background(), "rw")
	require.NoError(t, err)
	assert.False(t, found.ReadOnly())
}

func TestAuthenticate_UnknownToken_ReturnsErrBadToken(t *testing.T) {
	database := dbtest.WithDB(t)
	store := agent.NewPostgresStore(database)
	svc := agent.NewService(store, &fakeReleaser{}, stubTokenResolver{}, slog.DiscardHandler)

	_, err := svc.Authenticate(context.Background(), "nope")
	assert.ErrorIs(t, err, agent.ErrBadToken)
}

func TestAuthenticate_DisabledAgent_ReturnsErrIsDisabled(t *testing.T) {
	database := dbtest.WithDB(t)
	tenantID := dbtest.NewTenant(t, database)
	store := agent.NewPostgresStore(database)
	ctx := db.WithTenant(context.Background(), tenantID)

	svc := agent.NewService(store, &fakeReleaser{}, nil, slog.DiscardHandler)
	created, err := svc.Create(ctx, "flappable")
	require.NoError(t, err)
	_, err = svc.SetDisabled(ctx, created.ID, true)
	require.NoError(t, err)

	tokens := stubTokenResolver{"tok": {tenant: tenantID, agent: created.ID}}
	authSvc := agent.NewService(store, &fakeReleaser{}, tokens, slog.DiscardHandler)

	_, err = authSvc.Authenticate(context.Background(), "tok")
	assert.ErrorIs(t, err, agent.ErrIsDisabled)
}

// A token claiming the wrong tenant for a real agent id must not resolve.
// Row level security scopes Authenticate's lookup to the token's own
// tenant, so this is what stops a token from one tenant reaching another
// tenant's agent by guessing its id -- the property internal/oauth depends
// on to keep one tenant's grants from crossing into another's.
func TestAuthenticate_TokenTenantMustMatchTheAgentsOwnTenant(t *testing.T) {
	database := dbtest.WithDB(t)
	victimTenant := dbtest.NewTenant(t, database)
	attackerTenant := dbtest.NewTenant(t, database)
	store := agent.NewPostgresStore(database)

	svc := agent.NewService(store, &fakeReleaser{}, nil, slog.DiscardHandler)
	created, err := svc.Create(db.WithTenant(context.Background(), victimTenant), "victim")
	require.NoError(t, err)

	tokens := stubTokenResolver{"cross-tenant": {tenant: attackerTenant, agent: created.ID}}
	authSvc := agent.NewService(store, &fakeReleaser{}, tokens, slog.DiscardHandler)

	_, err = authSvc.Authenticate(context.Background(), "cross-tenant")
	assert.ErrorIs(t, err, agent.ErrBadToken)
}

func TestChoose_ExistingEnabledAgentReturnsItsID(t *testing.T) {
	database := dbtest.WithDB(t)
	tenantID := dbtest.NewTenant(t, database)
	ctx := db.WithTenant(context.Background(), tenantID)
	svc := agent.NewService(agent.NewPostgresStore(database), &fakeReleaser{}, nil, slog.DiscardHandler)

	created, err := svc.Create(ctx, "chosen-one")
	require.NoError(t, err)

	got, err := svc.Choose(ctx, &authn.User{ID: uuid.New(), TenantID: tenantID}, &created.ID, "", "")
	require.NoError(t, err)
	assert.Equal(t, created.ID, got)
}

func TestChoose_UnknownAgentID_ReturnsErrAgentUnavailable(t *testing.T) {
	database := dbtest.WithDB(t)
	tenantID := dbtest.NewTenant(t, database)
	ctx := db.WithTenant(context.Background(), tenantID)
	svc := agent.NewService(agent.NewPostgresStore(database), &fakeReleaser{}, nil, slog.DiscardHandler)

	unknown := uuid.New()
	_, err := svc.Choose(ctx, &authn.User{ID: uuid.New(), TenantID: tenantID}, &unknown, "", "")
	assert.ErrorIs(t, err, oauth.ErrAgentUnavailable)
}

func TestChoose_DisabledAgentID_ReturnsErrAgentUnavailable(t *testing.T) {
	database := dbtest.WithDB(t)
	tenantID := dbtest.NewTenant(t, database)
	ctx := db.WithTenant(context.Background(), tenantID)
	svc := agent.NewService(agent.NewPostgresStore(database), &fakeReleaser{}, nil, slog.DiscardHandler)

	created, err := svc.Create(ctx, "flappable")
	require.NoError(t, err)
	_, err = svc.SetDisabled(ctx, created.ID, true)
	require.NoError(t, err)

	_, err = svc.Choose(ctx, &authn.User{ID: uuid.New(), TenantID: tenantID}, &created.ID, "", "")
	assert.ErrorIs(t, err, oauth.ErrAgentUnavailable)
}

func TestChoose_NoAgentIDCreatesANewOneByName(t *testing.T) {
	database := dbtest.WithDB(t)
	tenantID := dbtest.NewTenant(t, database)
	ctx := db.WithTenant(context.Background(), tenantID)
	store := agent.NewPostgresStore(database)
	svc := agent.NewService(store, &fakeReleaser{}, nil, slog.DiscardHandler)

	got, err := svc.Choose(ctx, &authn.User{ID: uuid.New(), TenantID: tenantID}, nil, "brand-new", "")
	require.NoError(t, err)

	found, err := store.Get(ctx, got)
	require.NoError(t, err)
	assert.Equal(t, "brand-new", found.Name)
}

func TestChoose_NoAgentIDWithATakenName_ReturnsErrAgentNameTaken(t *testing.T) {
	database := dbtest.WithDB(t)
	tenantID := dbtest.NewTenant(t, database)
	ctx := db.WithTenant(context.Background(), tenantID)
	svc := agent.NewService(agent.NewPostgresStore(database), &fakeReleaser{}, nil, slog.DiscardHandler)

	_, err := svc.Create(ctx, "dup")
	require.NoError(t, err)

	_, err = svc.Choose(ctx, &authn.User{ID: uuid.New(), TenantID: tenantID}, nil, "dup", "")
	assert.ErrorIs(t, err, oauth.ErrAgentNameTaken)
}

func TestChoose_NoAgentIDWithAnEmptyName_ReturnsErrAgentUnavailable(t *testing.T) {
	database := dbtest.WithDB(t)
	tenantID := dbtest.NewTenant(t, database)
	ctx := db.WithTenant(context.Background(), tenantID)
	svc := agent.NewService(agent.NewPostgresStore(database), &fakeReleaser{}, nil, slog.DiscardHandler)

	_, err := svc.Choose(ctx, &authn.User{ID: uuid.New(), TenantID: tenantID}, nil, "  ", "")
	assert.ErrorIs(t, err, oauth.ErrAgentUnavailable)
}

func TestEnabled_EnabledAgentIsTrue(t *testing.T) {
	database := dbtest.WithDB(t)
	tenantID := dbtest.NewTenant(t, database)
	ctx := db.WithTenant(context.Background(), tenantID)
	svc := agent.NewService(agent.NewPostgresStore(database), &fakeReleaser{}, nil, slog.DiscardHandler)

	created, err := svc.Create(ctx, "enabled-one")
	require.NoError(t, err)

	ok, err := svc.Enabled(ctx, created.ID)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestEnabled_DisabledAgentIsFalse(t *testing.T) {
	database := dbtest.WithDB(t)
	tenantID := dbtest.NewTenant(t, database)
	ctx := db.WithTenant(context.Background(), tenantID)
	svc := agent.NewService(agent.NewPostgresStore(database), &fakeReleaser{}, nil, slog.DiscardHandler)

	created, err := svc.Create(ctx, "flappable")
	require.NoError(t, err)
	_, err = svc.SetDisabled(ctx, created.ID, true)
	require.NoError(t, err)

	ok, err := svc.Enabled(ctx, created.ID)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestEnabled_UnknownAgentIsFalseWithNoError(t *testing.T) {
	database := dbtest.WithDB(t)
	tenantID := dbtest.NewTenant(t, database)
	ctx := db.WithTenant(context.Background(), tenantID)
	svc := agent.NewService(agent.NewPostgresStore(database), &fakeReleaser{}, nil, slog.DiscardHandler)

	ok, err := svc.Enabled(ctx, uuid.New())
	require.NoError(t, err)
	assert.False(t, ok)
}
