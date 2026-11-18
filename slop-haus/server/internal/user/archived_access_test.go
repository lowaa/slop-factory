package user_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

func TestTenantArchivedFollowsTheArchive(t *testing.T) {
	f := newFixture(t)
	a, second := ownerWithTwoTenants(t, f)

	archived, err := f.svc.TenantArchived(f.ctx, second)
	require.NoError(t, err)
	assert.False(t, archived)

	require.NoError(t, f.svc.ArchiveTenant(f.ctx, a, second))
	archived, err = f.svc.TenantArchived(f.ctx, second)
	require.NoError(t, err)
	assert.True(t, archived, "at once: nothing is cached")

	require.NoError(t, f.svc.UnarchiveTenant(f.ctx, a, second))
	archived, err = f.svc.TenantArchived(f.ctx, second)
	require.NoError(t, err)
	assert.False(t, archived)
}

func TestAnArchivedTenantsSessionCannotBeRefreshedUntilUnarchived(t *testing.T) {
	f := newFixture(t)
	a, _ := ownerWithTwoTenants(t, f)
	second, err := f.svc.CreateTenant(f.ctx, a, "Third")
	require.NoError(t, err)
	_, err = f.svc.Refresh(f.ctx, second.Refresh)
	require.NoError(t, err, "it refreshes before the archive")

	require.NoError(t, f.svc.ArchiveTenant(f.ctx, a, second.Profile.TenantID))
	_, err = f.svc.Refresh(f.ctx, second.Refresh)
	assert.ErrorIs(t, err, user.ErrTenantArchived)

	require.NoError(t, f.svc.UnarchiveTenant(f.ctx, a, second.Profile.TenantID))
	_, err = f.svc.Refresh(f.ctx, second.Refresh)
	assert.NoError(t, err)
}

func TestSwitchingIntoAnArchivedTenantIsRefused(t *testing.T) {
	f := newFixture(t)
	a, second := ownerWithTwoTenants(t, f)
	require.NoError(t, f.svc.ArchiveTenant(f.ctx, a, second))

	_, err := f.svc.SwitchTenant(f.ctx, a, second)
	assert.ErrorIs(t, err, user.ErrTenantArchived)
	assert.Equal(t, authn.CodeTenantArchived, user.ProblemFor(err).Code)
}
