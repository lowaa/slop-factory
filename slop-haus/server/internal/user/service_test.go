package user_test

import (
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/clientip"
	"github.com/lowaa/slop-factory/go-auth/ratelimit"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

func TestSignUpActivateAndSignIn(t *testing.T) {
	f := newFixture(t)

	require.NoError(t, f.svc.Register(f.ctx, &user.RegisterInput{
		Email: "  Ada@Example.COM ", Password: password, DisplayName: " Ada ",
	}))
	mail := f.mail.last(t, "ada@example.com")
	assert.Contains(t, mail.body, baseURL+"/activate#token=", "the link is built from the configured base URL, token in the fragment")

	_, err := f.svc.Login(f.ctx, "ada@example.com", password)
	require.ErrorIs(t, err, user.ErrInvalidCredentials, "not until the link is followed")

	require.NoError(t, f.svc.Activate(f.ctx, tokenFrom(t, mail)))

	session, err := f.svc.Login(f.ctx, "ADA@example.com", password)
	require.NoError(t, err)
	assert.Equal(t, "ada@example.com", session.Profile.Email)
	assert.Equal(t, "Ada", session.Profile.DisplayName)
	assert.NotEqual(t, uuid.Nil, session.Profile.TenantID)
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM global_tenant_memberships WHERE user_id = $1 AND tenant = $2`,
		session.Profile.ID, session.Profile.TenantID), "activation made the user a member of their own tenant")

	who, err := f.svc.VerifyAccess(f.ctx, session.Access)
	require.NoError(t, err)
	assert.Equal(t, session.Profile.ID, who.ID)
	assert.Equal(t, session.Profile.TenantID, who.TenantID)
	assert.WithinDuration(t, time.Now(), who.IssuedAt, 5*time.Second, "the session's issue time rides along")
}

func TestEachAccountGetsItsOwnTenant(t *testing.T) {
	f := newFixture(t)
	f.activeUser(t, "a@example.com")
	f.activeUser(t, "b@example.com")

	a, err := f.svc.Login(f.ctx, "a@example.com", password)
	require.NoError(t, err)
	b, err := f.svc.Login(f.ctx, "b@example.com", password)
	require.NoError(t, err)
	assert.NotEqual(t, a.Profile.TenantID, b.Profile.TenantID)

	dir, err := f.svc.MemberDirectory(f.ctx, &authn.User{ID: a.Profile.ID, TenantID: a.Profile.TenantID})
	require.NoError(t, err)
	require.Len(t, dir.Members, 1)
	assert.Equal(t, a.Profile.ID, dir.Members[0].ID, "b is not in a's tenant")
}

func TestAnActivationLinkWorksOnce(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.svc.Register(f.ctx, &user.RegisterInput{Email: "a@example.com", Password: password, DisplayName: "A"}))
	token := tokenFrom(t, f.mail.last(t, "a@example.com"))

	require.NoError(t, f.svc.Activate(f.ctx, token))
	require.ErrorIs(t, f.svc.Activate(f.ctx, token), user.ErrInvalidToken)
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM global_tenants`))
}

// Two clicks on one link arriving together must not both get past the
// check -- that would be one account with two tenants.
func TestConcurrentActivationsMakeOneTenant(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.svc.Register(f.ctx, &user.RegisterInput{Email: "a@example.com", Password: password, DisplayName: "A"}))
	token := tokenFrom(t, f.mail.last(t, "a@example.com"))

	const racers = 8
	errs := make([]error, racers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range racers {
		wg.Go(func() {
			<-start
			errs[i] = f.svc.Activate(f.ctx, token)
		})
	}
	close(start)
	wg.Wait()

	succeeded := 0
	for _, err := range errs {
		if err == nil {
			succeeded++
		} else {
			assert.ErrorIs(t, err, user.ErrInvalidToken)
		}
	}
	assert.Equal(t, 1, succeeded)
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM global_tenants`))
}

func TestAnExpiredActivationLinkIsRefused(t *testing.T) {
	f := newFixture(t, func(c *user.Config) { c.ActivationTTL = -time.Second })
	require.NoError(t, f.svc.Register(f.ctx, &user.RegisterInput{Email: "a@example.com", Password: password, DisplayName: "A"}))

	require.ErrorIs(t, f.svc.Activate(f.ctx, tokenFrom(t, f.mail.last(t, "a@example.com"))), user.ErrInvalidToken)
}

// A reset token is not an activation token, and the other way round.
func TestTokensOnlySpendForTheirPurpose(t *testing.T) {
	f := newFixture(t)
	f.activeUser(t, "a@example.com")
	require.NoError(t, f.svc.RequestPasswordReset(f.ctx, "a@example.com"))
	reset := tokenFrom(t, f.mail.last(t, "a@example.com"))

	require.ErrorIs(t, f.svc.Activate(f.ctx, reset), user.ErrInvalidToken)

	require.NoError(t, f.svc.Register(f.ctx, &user.RegisterInput{Email: "b@example.com", Password: password, DisplayName: "B"}))
	activation := tokenFrom(t, f.mail.last(t, "b@example.com"))
	require.ErrorIs(t, f.svc.ResetPassword(f.ctx, activation, "a brand new password"), user.ErrInvalidToken)
}

// Signing up with a registered address answers exactly as a new one does;
// only the inbox learns the difference.
func TestSigningUpAgainRevealsNothingToTheCaller(t *testing.T) {
	f := newFixture(t)
	f.activeUser(t, "taken@example.com")
	before := len(f.mail.to("taken@example.com"))

	err := f.svc.Register(f.ctx, &user.RegisterInput{Email: "taken@example.com", Password: "someone elses password", DisplayName: "Mallory"})
	require.NoError(t, err)

	sent := f.mail.to("taken@example.com")
	require.Len(t, sent, before+1)
	assert.Equal(t, "You already have a slop-haus account", sent[len(sent)-1].subject)
	assert.NotContains(t, sent[len(sent)-1].body, "#token=", "no link that does anything")
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM global_users`))

	_, err = f.svc.Login(f.ctx, "taken@example.com", "someone elses password")
	require.ErrorIs(t, err, user.ErrInvalidCredentials, "the password is the original owner's")
}

// Signing up again before activating resends the link, and does not let
// the second request choose the password.
func TestSigningUpAgainBeforeActivatingResendsTheLink(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.svc.Register(f.ctx, &user.RegisterInput{Email: "a@example.com", Password: password, DisplayName: "A"}))
	require.NoError(t, f.svc.Register(f.ctx, &user.RegisterInput{Email: "a@example.com", Password: "a different password", DisplayName: "A"}))

	sent := f.mail.to("a@example.com")
	require.Len(t, sent, 2)
	require.NoError(t, f.svc.Activate(f.ctx, tokenFrom(t, sent[1])))

	_, err := f.svc.Login(f.ctx, "a@example.com", "a different password")
	require.ErrorIs(t, err, user.ErrInvalidCredentials)
	_, err = f.svc.Login(f.ctx, "a@example.com", password)
	require.NoError(t, err)
}

func TestSignInFailuresAreIndistinguishable(t *testing.T) {
	f := newFixture(t)
	f.activeUser(t, "a@example.com")
	require.NoError(t, f.svc.Register(f.ctx, &user.RegisterInput{Email: "pending@example.com", Password: password, DisplayName: "P"}))

	_, unknown := f.svc.Login(f.ctx, "nobody@example.com", password)
	_, wrong := f.svc.Login(f.ctx, "a@example.com", "not the password")
	_, pending := f.svc.Login(f.ctx, "pending@example.com", password)
	for _, err := range []error{unknown, wrong, pending} {
		require.ErrorIs(t, err, user.ErrInvalidCredentials)
		assert.Equal(t, user.ErrInvalidCredentials.Error(), err.Error())
	}
	// Alike on the wire, but each is recorded, for spraying to be seen.
	assert.Equal(t, 3, f.events.CountMethod(audit.SignInRefused, "password"))
	assert.Zero(t, f.events.Count(audit.SignIn))

	_, err := f.svc.Login(f.ctx, "a@example.com", password)
	require.NoError(t, err)
	assert.Equal(t, 1, f.events.CountMethod(audit.SignIn, "password"))
	assert.Equal(t, 3, f.events.Count(audit.SignInRefused), "a success is not a refusal")
}

func TestPasswordReset(t *testing.T) {
	f := newFixture(t)
	f.activeUser(t, "a@example.com")

	require.NoError(t, f.svc.RequestPasswordReset(f.ctx, " A@example.com"))
	mail := f.mail.last(t, "a@example.com")
	assert.Contains(t, mail.body, baseURL+"/reset-password#token=")

	token := tokenFrom(t, mail)
	require.NoError(t, f.svc.ResetPassword(f.ctx, token, "a brand new password"))

	_, err := f.svc.Login(f.ctx, "a@example.com", password)
	require.ErrorIs(t, err, user.ErrInvalidCredentials)
	_, err = f.svc.Login(f.ctx, "a@example.com", "a brand new password")
	require.NoError(t, err)

	require.ErrorIs(t, f.svc.ResetPassword(f.ctx, token, "yet another password"), user.ErrInvalidToken, "the link works once")
}

// A reset link left in an inbox is a way back in for whoever reads the
// inbox next; using any one of them spends them all.
func TestAResetSpendsEveryOutstandingResetLink(t *testing.T) {
	f := newFixture(t)
	f.activeUser(t, "a@example.com")
	require.NoError(t, f.svc.RequestPasswordReset(f.ctx, "a@example.com"))
	first := tokenFrom(t, f.mail.last(t, "a@example.com"))
	require.NoError(t, f.svc.RequestPasswordReset(f.ctx, "a@example.com"))
	second := tokenFrom(t, f.mail.last(t, "a@example.com"))

	require.NoError(t, f.svc.ResetPassword(f.ctx, second, "a brand new password"))
	require.ErrorIs(t, f.svc.ResetPassword(f.ctx, first, "the attackers password"), user.ErrInvalidToken)
}

func TestAResetForAnAddressWithNoActiveAccountSendsNothing(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.svc.Register(f.ctx, &user.RegisterInput{Email: "pending@example.com", Password: password, DisplayName: "P"}))

	require.NoError(t, f.svc.RequestPasswordReset(f.ctx, "nobody@example.com"))
	require.NoError(t, f.svc.RequestPasswordReset(f.ctx, "pending@example.com"))

	assert.Empty(t, f.mail.to("nobody@example.com"))
	assert.Len(t, f.mail.to("pending@example.com"), 1, "only the activation link")
}

func TestAPasswordLongerThanBcryptCanHashIsRefused(t *testing.T) {
	f := newFixture(t)
	long := strings.Repeat("é", 40) // 80 bytes, 40 characters

	err := f.svc.Register(f.ctx, &user.RegisterInput{Email: "a@example.com", Password: long, DisplayName: "A"})
	require.ErrorIs(t, err, user.ErrPasswordTooLong)
}

func TestRefreshIssuesAnAccessTokenForTheSameTenant(t *testing.T) {
	f := newFixture(t)
	f.activeUser(t, "a@example.com")
	session, err := f.svc.Login(f.ctx, "a@example.com", password)
	require.NoError(t, err)

	access, err := f.svc.Refresh(f.ctx, session.Refresh)
	require.NoError(t, err)
	who, err := f.svc.VerifyAccess(f.ctx, access)
	require.NoError(t, err)
	assert.Equal(t, session.Profile.TenantID, who.TenantID)

	_, err = f.svc.Refresh(f.ctx, session.Access)
	require.ErrorIs(t, err, user.ErrInvalidSession, "an access token is not a refresh token")
	_, err = f.svc.VerifyAccess(f.ctx, session.Refresh)
	require.Error(t, err, "nor the other way round")
}

// Refresh is where a session meets the database; each of these must end
// it.
func TestRefreshIsRefusedOnceTheSessionIsRevoked(t *testing.T) {
	revocations := map[string]func(f *fixture, s *user.Session){
		"password reset": func(f *fixture, s *user.Session) {
			// A JWT's iat is whole seconds, and so is the cut-off a reset
			// sets; a reset in the same second as the sign-in would leave
			// that session standing.
			f.clock.Advance(2 * time.Second)
			require.NoError(t, f.svc.RequestPasswordReset(f.ctx, s.Profile.Email))
			require.NoError(t, f.svc.ResetPassword(f.ctx, tokenFrom(t, f.mail.last(t, s.Profile.Email)), "a brand new password"))
		},
		"deactivated": func(f *fixture, s *user.Session) {
			_, err := f.db.Global().Exec(f.ctx, `UPDATE global_users SET is_active = false WHERE id = $1`, s.Profile.ID)
			require.NoError(t, err)
		},
		"removed from the tenant": func(f *fixture, s *user.Session) {
			_, err := f.db.Global().Exec(f.ctx, `DELETE FROM global_tenant_memberships WHERE user_id = $1`, s.Profile.ID)
			require.NoError(t, err)
		},
		"account deleted": func(f *fixture, s *user.Session) {
			_, err := f.db.Global().Exec(f.ctx, `DELETE FROM global_users WHERE id = $1`, s.Profile.ID)
			require.NoError(t, err)
		},
	}
	for name, revoke := range revocations {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.activeUser(t, "a@example.com")
			session, err := f.svc.Login(f.ctx, "a@example.com", password)
			require.NoError(t, err)
			_, err = f.svc.Refresh(f.ctx, session.Refresh)
			require.NoError(t, err, "valid before")

			revoke(f, session)

			_, err = f.svc.Refresh(f.ctx, session.Refresh)
			require.ErrorIs(t, err, user.ErrInvalidSession)
		})
	}
}

// A new sign-in after a reset is a new session, and must survive it.
func TestASignInAfterAResetKeepsWorking(t *testing.T) {
	f := newFixture(t)
	f.activeUser(t, "a@example.com")
	require.NoError(t, f.svc.RequestPasswordReset(f.ctx, "a@example.com"))
	require.NoError(t, f.svc.ResetPassword(f.ctx, tokenFrom(t, f.mail.last(t, "a@example.com")), "a brand new password"))

	session, err := f.svc.Login(f.ctx, "a@example.com", "a brand new password")
	require.NoError(t, err)
	_, err = f.svc.Refresh(f.ctx, session.Refresh)
	require.NoError(t, err)
}

// The limit counts attempts per address whether or not the address has an
// account, so hitting it says nothing about which addresses do.
func TestSignInIsRateLimitedPerAddress(t *testing.T) {
	f := newFixture(t, func(c *user.Config) {
		c.LoginLimit = &ratelimit.Config{Limit: 3, Window: time.Hour, MaxKeys: 100}
	})
	f.activeUser(t, "a@example.com")

	for _, address := range []string{"a@example.com", "nobody@example.com"} {
		for range 3 {
			_, err := f.svc.Login(f.ctx, address, "wrong")
			require.ErrorIs(t, err, user.ErrInvalidCredentials)
		}
		_, err := f.svc.Login(f.ctx, address, password)
		require.ErrorIs(t, err, user.ErrRateLimited, address)
	}

	f.activeUser(t, "b@example.com")
	_, err := f.svc.Login(f.ctx, "b@example.com", password)
	require.NoError(t, err, "another address is unaffected")
}

// Counting per address and client is what stops a lockout: whoever is
// guessing at an address only ever exhausts their own allowance, and the
// owner, signing in from somewhere else, is not held up by it.
func TestAGuesserDoesNotLockTheOwnerOut(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	f := newFixture(t, func(c *user.Config) {
		c.LoginLimit = &ratelimit.Config{Limit: 3, Window: time.Minute, MaxKeys: 100, Now: func() time.Time { return now }}
	})
	f.activeUser(t, "a@example.com")
	guesser := clientip.NewContext(f.ctx, netip.MustParseAddr("203.0.113.66"))
	owner := clientip.NewContext(f.ctx, netip.MustParseAddr("198.51.100.7"))

	for range 3 {
		_, err := f.svc.Login(guesser, "a@example.com", "wrong")
		require.ErrorIs(t, err, user.ErrInvalidCredentials)
	}
	_, err := f.svc.Login(guesser, "a@example.com", password)
	require.ErrorIs(t, err, user.ErrRateLimited, "the guesser is stopped")

	_, err = f.svc.Login(owner, "a@example.com", password)
	require.NoError(t, err, "the owner, elsewhere, is not")

	now = now.Add(61 * time.Second)
	_, err = f.svc.Login(guesser, "a@example.com", password)
	require.NoError(t, err, "and the lock lasts one window")
}

func TestMailIsRateLimitedPerAddress(t *testing.T) {
	f := newFixture(t, func(c *user.Config) {
		c.MailLimit = &ratelimit.Config{Limit: 2, Window: time.Hour, MaxKeys: 100}
	})
	require.NoError(t, f.svc.Register(f.ctx, &user.RegisterInput{Email: "a@example.com", Password: password, DisplayName: "A"}))
	require.NoError(t, f.svc.Activate(f.ctx, tokenFrom(t, f.mail.last(t, "a@example.com"))))
	require.NoError(t, f.svc.RequestPasswordReset(f.ctx, "a@example.com"))

	require.ErrorIs(t, f.svc.RequestPasswordReset(f.ctx, "A@example.com"), user.ErrRateLimited, "normalized before counting")
	assert.Len(t, f.mail.to("a@example.com"), 2)
}

func TestSeedingIsIdempotent(t *testing.T) {
	f := newFixture(t)
	seeder := user.NewSeeder(f.store)

	created, err := seeder.EnsureActive(f.ctx, &user.RegisterInput{Email: "Dev@example.com", Password: password, DisplayName: "Dev"})
	require.NoError(t, err)
	assert.True(t, created)
	created, err = seeder.EnsureActive(f.ctx, &user.RegisterInput{Email: "dev@example.com", Password: "a different password", DisplayName: "Dev"})
	require.NoError(t, err)
	assert.False(t, created)

	_, err = f.svc.Login(f.ctx, "dev@example.com", password)
	require.NoError(t, err, "active from the start, and the second run changed nothing")
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM global_tenants`))
	assert.Empty(t, f.mail.to("dev@example.com"), "no link to follow")
}

// A made-up token must be turned away before the password is hashed: the
// route is public, and bcrypt is the expensive part. An over-long password
// is the observable -- hashing it would fail differently.
func TestAResetWithABogusTokenIsRefusedBeforeHashing(t *testing.T) {
	f := newFixture(t)
	err := f.svc.ResetPassword(f.ctx, "not-a-real-token", strings.Repeat("é", 40))
	require.ErrorIs(t, err, user.ErrInvalidToken)
}
