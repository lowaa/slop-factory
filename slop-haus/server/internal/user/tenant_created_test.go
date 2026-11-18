package user_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

// switchableHook lets a test make tenants before the hook is wired, as
// tenants made before this behaviour shipped were.
type switchableHook struct {
	mu    sync.Mutex
	inner user.TenantCreatedHook
}

func (h *switchableHook) set(inner user.TenantCreatedHook) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.inner = inner
}

func (h *switchableHook) TenantCreated(ctx context.Context, tenantID, ownerID uuid.UUID) error {
	h.mu.Lock()
	inner := h.inner
	h.mu.Unlock()
	if inner == nil {
		return nil
	}
	return inner.TenantCreated(ctx, tenantID, ownerID)
}

type ownersOf struct{ f *fixture }

func (o ownersOf) IsTenantOwner(ctx context.Context, userID, tenantID uuid.UUID) (bool, error) {
	return o.f.svc.IsTenantOwner(ctx, userID, tenantID)
}

type countingImport struct {
	mu      sync.Mutex
	tenants []uuid.UUID
}

func (c *countingImport) ImportAll(ctx context.Context) (int, error) {
	tenant, err := db.TenantFrom(ctx)
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tenants = append(c.tenants, tenant)
	return 0, nil
}

type startingFixture struct {
	*fixture
	hook     *switchableHook
	settings *settings.Service
	imports  *countingImport
}

func newStartingFixture(t *testing.T) *startingFixture {
	t.Helper()
	hook := &switchableHook{}
	f := newFixtureWith(t, &fixtureOpts{tenantCreated: hook})
	imports := &countingImport{}
	svc := settings.NewService(settings.NewPostgresStore(f.db), audit.New(slog.DiscardHandler, nil), slog.DiscardHandler,
		settings.WithOwners(ownersOf{f}), settings.WithProvisionEnabledHook(imports))
	return &startingFixture{fixture: f, hook: hook, settings: svc, imports: imports}
}

func (f *startingFixture) provisions(t *testing.T, tenant uuid.UUID) bool {
	t.Helper()
	on, err := f.settings.ProvisionFromBoardEnabled(db.WithTenant(f.ctx, tenant))
	require.NoError(t, err)
	return on
}

func TestANewTenantStartsWithProvisionFromBoardOnAndTheHookRan(t *testing.T) {
	f := newStartingFixture(t)
	f.activeUser(t, "a@example.com")
	a := f.signIn(t, "a@example.com")
	f.hook.set(settings.NewTenantStartingSettings(f.settings))

	session, err := f.svc.CreateTenant(f.ctx, a, "Fresh")
	require.NoError(t, err)

	fresh := session.Profile.TenantID
	assert.True(t, f.provisions(t, fresh))
	assert.Equal(t, []uuid.UUID{fresh}, f.imports.tenants, "written through the service, so the import hook ran for it")
}

func TestATenantMadeBeforeThePolicyKeepsTheDefault(t *testing.T) {
	f := newStartingFixture(t)
	f.activeUser(t, "a@example.com")
	a := f.signIn(t, "a@example.com")
	f.hook.set(settings.NewTenantStartingSettings(f.settings))

	_, err := f.svc.CreateTenant(f.ctx, a, "Fresh")
	require.NoError(t, err)

	assert.False(t, f.provisions(t, a.TenantID), "the earlier tenant stored nothing and still reads the declared default")
}

func TestEveryPathThatMakesATenantStartsItWithTheSetting(t *testing.T) {
	f := newStartingFixture(t)
	f.hook.set(settings.NewTenantStartingSettings(f.settings))

	f.activeUser(t, "activated@example.com") // activation link
	activated := f.signIn(t, "activated@example.com")
	admin := f.admin(t, "admin@example.com")

	_, err := f.svc.InviteToNewTenant(f.ctx, admin, "invited@example.com")
	require.NoError(t, err)
	session, err := f.svc.AcceptInviteAsNewAccount(f.ctx, tokenFrom(t, f.mail.last(t, "invited@example.com")), "Invited", "a-long-enough-password", "")
	require.NoError(t, err)

	assert.True(t, f.provisions(t, activated.TenantID))
	assert.True(t, f.provisions(t, session.Profile.TenantID))
}

func TestAJoinedTenantIsNotRewritten(t *testing.T) {
	f := newStartingFixture(t)
	f.activeUser(t, "a@example.com")
	f.activeUser(t, "b@example.com")
	a := f.signIn(t, "a@example.com")
	b := f.signIn(t, "b@example.com")
	f.hook.set(settings.NewTenantStartingSettings(f.settings))

	_, err := f.svc.InviteToTenant(f.ctx, a, "b@example.com")
	require.NoError(t, err)
	_, err = f.svc.AcceptInvite(f.ctx, b, tokenFrom(t, f.mail.last(t, "b@example.com")), "")
	require.NoError(t, err)

	assert.False(t, f.provisions(t, a.TenantID))
	assert.Empty(t, f.imports.tenants)
}

type failingHook struct{ sawCommitted func(tenant uuid.UUID) bool }

func (h failingHook) TenantCreated(_ context.Context, tenantID, _ uuid.UUID) error {
	if !h.sawCommitted(tenantID) {
		return errors.New("tenant not visible: the hook ran before the commit")
	}
	return errors.New("boom")
}

func TestAFailingHookDoesNotUndoTheTenantAndRunsAfterTheCommit(t *testing.T) {
	f := newStartingFixture(t)
	f.activeUser(t, "a@example.com")
	a := f.signIn(t, "a@example.com")
	f.hook.set(failingHook{sawCommitted: func(tenant uuid.UUID) bool {
		return f.count(t, `SELECT count(*) FROM global_tenants WHERE id = $1`, tenant) == 1
	}})

	session, err := f.svc.CreateTenant(f.ctx, a, "Fresh")

	require.NoError(t, err)
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM global_tenants WHERE id = $1`, session.Profile.TenantID))
}
