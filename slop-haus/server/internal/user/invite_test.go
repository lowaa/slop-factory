package user_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/ratelimit"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

func tenantIDs(t *testing.T, f *fixture, who *authn.User) []uuid.UUID {
	t.Helper()
	tenants, err := f.svc.Tenants(f.ctx, who)
	require.NoError(t, err)
	ids := make([]uuid.UUID, 0, len(tenants))
	for _, tn := range tenants {
		ids = append(ids, tn.ID)
	}
	return ids
}

func TestSignUpCanBeClosed(t *testing.T) {
	f := newFixture(t, func(c *user.Config) { c.RegistrationOpen = false })

	err := f.svc.Register(f.ctx, &user.RegisterInput{Email: "a@example.com", Password: password, DisplayName: "A"})
	require.ErrorIs(t, err, user.ErrRegistrationClosed)
	assert.Empty(t, f.mail.to("a@example.com"))
	assert.Zero(t, f.count(t, `SELECT count(*) FROM global_users`))
}

// An admin's invite is how someone gets a tenant of their own once sign-up
// is closed.
func TestAnAdminInviteStartsATenant(t *testing.T) {
	f := newFixture(t, func(c *user.Config) { c.RegistrationOpen = false })
	admin := f.seededAdmin(t, "admin@example.com")

	_, err := f.svc.InviteToNewTenant(f.ctx, admin, " New@Example.com ")
	require.NoError(t, err)
	mail := f.mail.last(t, "new@example.com")
	assert.Contains(t, mail.body, baseURL+"/invite#token=")

	preview, err := f.svc.PreviewInvite(f.ctx, tokenFrom(t, mail))
	require.NoError(t, err)
	assert.Equal(t, user.InviteNewTenant, preview.Kind)
	assert.False(t, preview.AccountExists)

	session, err := f.svc.AcceptInviteAsNewAccount(f.ctx, tokenFrom(t, mail), "Newcomer", password, "")
	require.NoError(t, err)
	assert.Equal(t, "new@example.com", session.Profile.Email, "the account is at the invited address")
	assert.NotEqual(t, admin.TenantID, session.Profile.TenantID, "a tenant of their own, not the admin's")

	newcomer := f.signIn(t, "new@example.com")
	assert.Equal(t, []uuid.UUID{session.Profile.TenantID}, tenantIDs(t, f, newcomer))
}

// A member's invite brings someone into that member's tenant -- and only
// that tenant.
func TestAMemberInviteJoinsTheirTenant(t *testing.T) {
	f := newFixture(t)
	f.activeUser(t, "a@example.com")
	a := f.signIn(t, "a@example.com")

	_, err := f.svc.InviteToTenant(f.ctx, a, "b@example.com")
	require.NoError(t, err)
	session, err := f.svc.AcceptInviteAsNewAccount(f.ctx, tokenFrom(t, f.mail.last(t, "b@example.com")), "Bee", password, "")
	require.NoError(t, err)

	assert.Equal(t, a.TenantID, session.Profile.TenantID)
	b := f.signIn(t, "b@example.com")
	assert.Equal(t, []uuid.UUID{a.TenantID}, tenantIDs(t, f, b), "a seat in a's tenant and nothing else")

	dir, err := f.svc.MemberDirectory(f.ctx, a)
	require.NoError(t, err)
	assert.Len(t, dir.Members, 2)
}

// Someone who already has an account accepts from their session, and ends
// up in both tenants, able to move between them.
func TestAnExistingAccountAcceptsAndSwitchesTenants(t *testing.T) {
	f := newFixture(t)
	f.activeUser(t, "a@example.com")
	f.activeUser(t, "b@example.com")
	a := f.signIn(t, "a@example.com")
	b := f.signIn(t, "b@example.com")
	bHome := b.TenantID

	_, err := f.svc.InviteToTenant(f.ctx, a, "b@example.com")
	require.NoError(t, err)
	mail := f.mail.last(t, "b@example.com")

	preview, err := f.svc.PreviewInvite(f.ctx, tokenFrom(t, mail))
	require.NoError(t, err)
	assert.True(t, preview.AccountExists, "the page asks b to sign in rather than make an account")

	_, err = f.svc.AcceptInviteAsNewAccount(f.ctx, tokenFrom(t, mail), "Impostor", password, "")
	require.ErrorIs(t, err, user.ErrAccountExists)

	session, err := f.svc.AcceptInvite(f.ctx, b, tokenFrom(t, mail), "")
	require.NoError(t, err, "the failed attempt above left the invite unspent")
	assert.Equal(t, a.TenantID, session.Profile.TenantID, "the session moves into the tenant joined")
	assert.ElementsMatch(t, []uuid.UUID{bHome, a.TenantID}, tenantIDs(t, f, b))

	back, err := f.svc.SwitchTenant(f.ctx, b, bHome)
	require.NoError(t, err)
	assert.Equal(t, bHome, back.Profile.TenantID)

	c := f.admin(t, "c@example.com")
	_, err = f.svc.SwitchTenant(f.ctx, b, c.TenantID)
	require.ErrorIs(t, err, user.ErrNotMember)
}

// An invite is for its address. Someone else holding the link -- signed in
// as themselves -- cannot spend it, and it stays good for its owner.
func TestAnInviteIsOnlyForItsAddress(t *testing.T) {
	f := newFixture(t)
	f.activeUser(t, "a@example.com")
	f.activeUser(t, "b@example.com")
	f.activeUser(t, "mallory@example.com")
	a := f.signIn(t, "a@example.com")

	_, err := f.svc.InviteToTenant(f.ctx, a, "b@example.com")
	require.NoError(t, err)
	token := tokenFrom(t, f.mail.last(t, "b@example.com"))

	_, err = f.svc.AcceptInvite(f.ctx, f.signIn(t, "mallory@example.com"), token, "")
	require.ErrorIs(t, err, user.ErrWrongAccount)

	_, err = f.svc.AcceptInvite(f.ctx, f.signIn(t, "b@example.com"), token, "")
	require.NoError(t, err)
}

func TestAnInviteWorksOnce(t *testing.T) {
	f := newFixture(t)
	f.activeUser(t, "a@example.com")
	a := f.signIn(t, "a@example.com")
	_, err := f.svc.InviteToTenant(f.ctx, a, "b@example.com")
	require.NoError(t, err)
	token := tokenFrom(t, f.mail.last(t, "b@example.com"))

	const racers = 8
	errs := make([]error, racers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range racers {
		wg.Go(func() {
			<-start
			_, errs[i] = f.svc.AcceptInviteAsNewAccount(f.ctx, token, "Bee", password, "")
		})
	}
	close(start)
	wg.Wait()

	succeeded := 0
	for _, err := range errs {
		if err == nil {
			succeeded++
		}
	}
	assert.Equal(t, 1, succeeded)
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM global_users WHERE email = 'b@example.com'`))

	_, err = f.svc.PreviewInvite(f.ctx, token)
	require.ErrorIs(t, err, user.ErrInvalidToken)
}

func TestAnExpiredInviteIsRefused(t *testing.T) {
	f := newFixture(t, func(c *user.Config) { c.InviteTTL = -time.Second })
	f.activeUser(t, "a@example.com")
	a := f.signIn(t, "a@example.com")
	_, err := f.svc.InviteToTenant(f.ctx, a, "b@example.com")
	require.NoError(t, err)

	_, err = f.svc.AcceptInviteAsNewAccount(f.ctx, tokenFrom(t, f.mail.last(t, "b@example.com")), "Bee", password, "")
	require.ErrorIs(t, err, user.ErrInvalidToken)
}

// The invite's expiry is stamped from the service's clock: sent while the
// clock is two hours behind it is already expired by the database's time,
// and sent after the clock has caught up it is not -- no sleeping.
func TestInviteExpiryFollowsTheServiceClock(t *testing.T) {
	f := newFixture(t)
	f.activeUser(t, "a@example.com")
	a := f.signIn(t, "a@example.com")

	f.clock.Advance(-2 * time.Hour)
	_, err := f.svc.InviteToTenant(f.ctx, a, "b@example.com")
	require.NoError(t, err)
	_, err = f.svc.AcceptInviteAsNewAccount(f.ctx, tokenFrom(t, f.mail.last(t, "b@example.com")), "Bee", password, "")
	require.ErrorIs(t, err, user.ErrInvalidToken)

	f.clock.Advance(2 * time.Hour)
	_, err = f.svc.InviteToTenant(f.ctx, a, "b@example.com")
	require.NoError(t, err)
	_, err = f.svc.AcceptInviteAsNewAccount(f.ctx, tokenFrom(t, f.mail.last(t, "b@example.com")), "Bee", password, "")
	require.NoError(t, err)
}

// Revoking works within the scope listing does: a member reaches their
// tenant's invites, an admin the invites to start one, and neither reaches
// anything else.
func TestInvitesAreRevokedWithinTheirScope(t *testing.T) {
	f := newFixture(t)
	f.activeUser(t, "a@example.com")
	f.activeUser(t, "outsider@example.com")
	a := f.signIn(t, "a@example.com")
	outsider := f.signIn(t, "outsider@example.com")
	admin := f.admin(t, "admin@example.com")

	joinInv, err := f.svc.InviteToTenant(f.ctx, a, "b@example.com")
	require.NoError(t, err)
	newInv, err := f.svc.InviteToNewTenant(f.ctx, admin, "c@example.com")
	require.NoError(t, err)

	listed, err := f.svc.ListInvites(f.ctx, outsider)
	require.NoError(t, err)
	assert.Empty(t, listed, "another tenant's invites are not listed")
	require.ErrorIs(t, f.svc.RevokeInvite(f.ctx, outsider, joinInv.ID), user.ErrNotFound)
	require.ErrorIs(t, f.svc.RevokeAdminInvite(f.ctx, joinInv.ID), user.ErrNotFound, "an admin's scope is invites to start a tenant")
	require.ErrorIs(t, f.svc.RevokeInvite(f.ctx, a, newInv.ID), user.ErrNotFound)

	adminListed, err := f.svc.ListAdminInvites(f.ctx)
	require.NoError(t, err)
	require.Len(t, adminListed, 1)
	assert.Equal(t, newInv.ID, adminListed[0].ID)

	require.NoError(t, f.svc.RevokeInvite(f.ctx, a, joinInv.ID))
	_, err = f.svc.AcceptInviteAsNewAccount(f.ctx, tokenFrom(t, f.mail.last(t, "b@example.com")), "Bee", password, "")
	require.ErrorIs(t, err, user.ErrInvalidToken, "a revoked invite is dead")
}

func TestInvitingSomeoneAlreadyInTheTenantIsRefused(t *testing.T) {
	f := newFixture(t)
	f.activeUser(t, "a@example.com")
	a := f.signIn(t, "a@example.com")

	_, err := f.svc.InviteToTenant(f.ctx, a, "A@example.com")
	require.ErrorIs(t, err, user.ErrAlreadyMember)
}

// The session's tenant can be up to one access-token lifetime stale; a
// member removed in that window must not keep inviting people in.
func TestARemovedMemberLosesTheTenantsInvitations(t *testing.T) {
	f := newFixture(t)
	f.activeUser(t, "a@example.com")
	a := f.signIn(t, "a@example.com")
	pending, err := f.svc.InviteToTenant(f.ctx, a, "c@example.com")
	require.NoError(t, err)
	_, err = f.db.Global().Exec(f.ctx, `DELETE FROM global_tenant_memberships WHERE user_id = $1`, a.ID)
	require.NoError(t, err)

	_, err = f.svc.InviteToTenant(f.ctx, a, "b@example.com")
	require.ErrorIs(t, err, user.ErrNotMember)
	assert.Empty(t, f.mail.to("b@example.com"))
	_, err = f.svc.ListInvites(f.ctx, a)
	require.ErrorIs(t, err, user.ErrNotMember)
	require.ErrorIs(t, f.svc.RevokeInvite(f.ctx, a, pending.ID), user.ErrNotMember)
}

func TestInvitesAreRateLimitedPerInviter(t *testing.T) {
	f := newFixture(t, func(c *user.Config) {
		c.InviteLimit = &ratelimit.Config{Limit: 2, Window: time.Hour, MaxKeys: 10}
	})
	f.activeUser(t, "a@example.com")
	a := f.signIn(t, "a@example.com")

	for _, to := range []string{"b@example.com", "c@example.com"} {
		_, err := f.svc.InviteToTenant(f.ctx, a, to)
		require.NoError(t, err)
	}
	_, err := f.svc.InviteToTenant(f.ctx, a, "d@example.com")
	require.ErrorIs(t, err, user.ErrRateLimited)
}

// Accepting is public, so a made-up token must be refused before the
// password is hashed -- the over-long password is the observable.
func TestAcceptingABogusInviteIsRefusedBeforeHashing(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.AcceptInviteAsNewAccount(f.ctx, "not-a-real-token", "Bee", strings.Repeat("é", 40), "")
	require.ErrorIs(t, err, user.ErrInvalidToken)
}

func TestAdminIsCheckedAgainstTheDatabase(t *testing.T) {
	f := newFixture(t)
	admin := f.admin(t, "admin@example.com")
	f.activeUser(t, "a@example.com")
	a := f.signIn(t, "a@example.com")

	isAdmin, err := f.svc.IsAdmin(f.ctx, admin.ID)
	require.NoError(t, err)
	assert.True(t, isAdmin)
	isAdmin, err = f.svc.IsAdmin(f.ctx, a.ID)
	require.NoError(t, err)
	assert.False(t, isAdmin)

	me, err := f.svc.Me(f.ctx, admin)
	require.NoError(t, err)
	assert.True(t, me.IsAdmin)

	_, err = f.db.Global().Exec(f.ctx, `UPDATE global_users SET is_active = false WHERE id = $1`, admin.ID)
	require.NoError(t, err)
	isAdmin, err = f.svc.IsAdmin(f.ctx, admin.ID)
	require.NoError(t, err)
	assert.False(t, isAdmin, "a deactivated admin is no admin, whatever their token says")
}

// Minting a new session from an existing one -- switching tenant, or
// accepting an invite -- must honour the same revocation refresh does.
// Otherwise an access token from before a password reset (still valid for
// its short life) buys a fresh refresh token and outlives the reset.
func TestSessionMintingPathsHonourRevocation(t *testing.T) {
	revocations := map[string]func(f *fixture, who *authn.User){
		"password reset": func(f *fixture, who *authn.User) {
			// Whole seconds, as in TestRefreshIsRefusedOnceTheSessionIsRevoked.
			f.clock.Advance(2 * time.Second)
			_, err := f.db.Global().Exec(f.ctx, `UPDATE global_users SET sessions_valid_after = $2 WHERE id = $1`, who.ID, f.clock.Now())
			require.NoError(t, err)
		},
		"deactivated": func(f *fixture, who *authn.User) {
			_, err := f.db.Global().Exec(f.ctx, `UPDATE global_users SET is_active = false WHERE id = $1`, who.ID)
			require.NoError(t, err)
		},
	}
	for name, revoke := range revocations {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.activeUser(t, "a@example.com")
			f.activeUser(t, "b@example.com")
			a := f.signIn(t, "a@example.com")
			b := f.signIn(t, "b@example.com")
			_, err := f.svc.InviteToTenant(f.ctx, a, "b@example.com")
			require.NoError(t, err)
			token := tokenFrom(t, f.mail.last(t, "b@example.com"))

			revoke(f, b)

			_, err = f.svc.AcceptInvite(f.ctx, b, token, "")
			require.ErrorIs(t, err, user.ErrInvalidSession, "accepting an invite")
			_, err = f.svc.SwitchTenant(f.ctx, b, b.TenantID)
			require.ErrorIs(t, err, user.ErrInvalidSession, "switching tenant")
		})
	}
}
