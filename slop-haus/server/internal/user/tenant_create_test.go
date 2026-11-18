package user_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lowaa/slop-factory/go-auth/ratelimit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

func ownedCount(t *testing.T, f *fixture, id uuid.UUID) int {
	t.Helper()
	return f.count(t, `SELECT count(*) FROM global_tenant_memberships WHERE user_id = $1 AND role = 'owner'`, id)
}

func TestCreateTenantMakesAnOwnedNamedAuditedTenant(t *testing.T) {
	f := newFixture(t)
	f.activeUser(t, "a@example.com")
	a := f.signIn(t, "a@example.com")

	session, err := f.svc.CreateTenant(f.ctx, a, "  Side project ")
	require.NoError(t, err)
	require.NotNil(t, session.Profile)
	assert.NotEqual(t, a.TenantID, session.Profile.TenantID, "the session moves into the new tenant")

	got := tenantNamed(t, f, a, session.Profile.TenantID)
	assert.Equal(t, "Side project", got.Name)
	assert.True(t, got.IsOwner)
	assert.Equal(t, 2, ownedCount(t, f, a.ID))
	assert.Equal(t, 1, f.events.Count(audit.TenantCreated))
}

func TestCreateTenantValidatesAndRefusesAClashingName(t *testing.T) {
	f := newFixture(t)
	f.activeUser(t, "a@example.com")
	a := f.signIn(t, "a@example.com")
	own := tenantNamed(t, f, a, a.TenantID).Name

	_, err := f.svc.CreateTenant(f.ctx, a, "  ")
	require.ErrorIs(t, err, user.ErrTenantNameEmpty)
	_, err = f.svc.CreateTenant(f.ctx, a, "two\nlines")
	require.ErrorIs(t, err, user.ErrTenantNameInvalid)
	_, err = f.svc.CreateTenant(f.ctx, a, own)
	require.ErrorIs(t, err, user.ErrTenantNameTaken)
	assert.Equal(t, 1, ownedCount(t, f, a.ID))
	assert.Zero(t, f.events.Count(audit.TenantCreated))
}

func TestCreateTenantStopsAtTheOwnerLimitButNotForJoinedTenants(t *testing.T) {
	f := newFixture(t, func(c *user.Config) { c.TenantOwnerLimit = 3 })
	f.activeUser(t, "a@example.com")
	f.activeUser(t, "b@example.com")
	a := f.signIn(t, "a@example.com")
	b := f.signIn(t, "b@example.com")

	// b joins a's tenant without owning it.
	_, err := f.svc.InviteToTenant(f.ctx, a, "b@example.com")
	require.NoError(t, err)
	_, err = f.svc.AcceptInvite(f.ctx, b, tokenFrom(t, f.mail.last(t, "b@example.com")), "")
	require.NoError(t, err)

	for i := 0; i < 2; i++ {
		_, err := f.svc.CreateTenant(f.ctx, b, fmt.Sprintf("B%d", i))
		require.NoError(t, err, "b owns 1 + %d; the joined tenant doesn't count", i)
	}
	_, err = f.svc.CreateTenant(f.ctx, b, "B3")
	require.ErrorIs(t, err, user.ErrTenantLimit)
	assert.Equal(t, 3, ownedCount(t, f, b.ID))
	assert.Equal(t, 1, f.events.Count(audit.TenantCreateRefused))
}

func TestAnAdminInviteCreatesPastTheLimit(t *testing.T) {
	f := newFixture(t, func(c *user.Config) { c.TenantOwnerLimit = 1 })
	f.activeUser(t, "a@example.com")
	a := f.signIn(t, "a@example.com")
	admin := f.admin(t, "admin@example.com")

	_, err := f.svc.CreateTenant(f.ctx, a, "Second")
	require.ErrorIs(t, err, user.ErrTenantLimit)

	_, err = f.svc.InviteToNewTenant(f.ctx, admin, "a@example.com")
	require.NoError(t, err)
	_, err = f.svc.AcceptInvite(f.ctx, a, tokenFrom(t, f.mail.last(t, "a@example.com")), "Granted")
	require.NoError(t, err)
	assert.Equal(t, 2, ownedCount(t, f, a.ID))
}

func TestLimitZeroTurnsCreationOff(t *testing.T) {
	f := newFixture(t, func(c *user.Config) { c.TenantOwnerLimit = 0 })
	f.activeUser(t, "a@example.com")
	a := f.signIn(t, "a@example.com")
	_, err := f.svc.CreateTenant(f.ctx, a, "Nope")
	require.ErrorIs(t, err, user.ErrTenantCreationOff)
	assert.Equal(t, 1, ownedCount(t, f, a.ID))
}

func TestCreateTenantIsRateLimited(t *testing.T) {
	f := newFixture(t, func(c *user.Config) {
		c.TenantOwnerLimit = 10
		c.TenantCreateLimit = &ratelimit.Config{Limit: 1, Window: time.Hour, MaxKeys: 10}
	})
	f.activeUser(t, "a@example.com")
	a := f.signIn(t, "a@example.com")
	_, err := f.svc.CreateTenant(f.ctx, a, "One")
	require.NoError(t, err)
	_, err = f.svc.CreateTenant(f.ctx, a, "Two")
	require.ErrorIs(t, err, user.ErrRateLimited)
	assert.Equal(t, 2, ownedCount(t, f, a.ID), "a refused create makes nothing")
	assert.Equal(t, 1, f.events.Count(audit.TenantCreated))
}

// Two concurrent creates at one below the limit must yield exactly one
// success: the count and the insert are one step under the user's lock.
func TestConcurrentCreatesCannotBothPassTheLimit(t *testing.T) {
	f := newFixture(t, func(c *user.Config) { c.TenantOwnerLimit = 2 })
	f.activeUser(t, "a@example.com")
	a := f.signIn(t, "a@example.com")

	const n = 40
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = f.svc.CreateTenant(f.ctx, a, fmt.Sprintf("Race %d", i))
		}()
	}
	close(start)
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		} else {
			assert.ErrorIs(t, err, user.ErrTenantLimit)
		}
	}
	assert.Equal(t, 1, ok)
	assert.Equal(t, 2, ownedCount(t, f, a.ID))
}
