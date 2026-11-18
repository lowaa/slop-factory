package user_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Whoever made a tenant owns it; people invited into it do not, whether
// they are new or already had a tenant of their own.
func TestTenantOwnership(t *testing.T) {
	f := newFixture(t)
	f.activeUser(t, "a@example.com")
	f.activeUser(t, "b@example.com")
	a := f.signIn(t, "a@example.com")
	b := f.signIn(t, "b@example.com")

	ok, err := f.svc.IsTenantOwner(f.ctx, a.ID, a.TenantID)
	require.NoError(t, err)
	assert.True(t, ok, "the person a tenant was made for owns it")

	_, err = f.svc.InviteToTenant(f.ctx, a, "b@example.com")
	require.NoError(t, err)
	_, err = f.svc.AcceptInvite(f.ctx, b, tokenFrom(t, f.mail.last(t, "b@example.com")), "")
	require.NoError(t, err)

	ok, err = f.svc.IsTenantOwner(f.ctx, b.ID, a.TenantID)
	require.NoError(t, err)
	assert.False(t, ok, "an existing account invited into a's tenant is a member")
	ok, err = f.svc.IsTenantOwner(f.ctx, b.ID, b.TenantID)
	require.NoError(t, err)
	assert.True(t, ok, "and still owns their own")

	_, err = f.svc.InviteToTenant(f.ctx, a, "c@example.com")
	require.NoError(t, err)
	session, err := f.svc.AcceptInviteAsNewAccount(f.ctx, tokenFrom(t, f.mail.last(t, "c@example.com")), "Cee", password, "")
	require.NoError(t, err)
	ok, err = f.svc.IsTenantOwner(f.ctx, session.Profile.ID, a.TenantID)
	require.NoError(t, err)
	assert.False(t, ok, "a new account invited in is a member")

	ok, err = f.svc.IsTenantOwner(f.ctx, a.ID, b.TenantID)
	require.NoError(t, err)
	assert.False(t, ok, "owning one tenant says nothing about another")
}
