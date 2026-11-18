package user_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/auth"
	"github.com/lowaa/slop-factory/go-auth/clientip"
	gologin "github.com/lowaa/slop-factory/go-auth/login"
	"github.com/lowaa/slop-factory/go-auth/ratelimit"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

// passwordOff is a second deployment over env's database, with password
// sign-in turned off: accounts are set up through env's own service,
// which still has passwords, and then exercised through this one.
func passwordOff(t *testing.T, env *providerEnv, providers ...gologin.AuthProvider) *user.Service {
	t.Helper()
	csrf, err := auth.NewCSRFTokens([]byte(testJWTSecret))
	require.NoError(t, err)
	return user.NewService(&user.Deps{Store: env.f.store, Signer: env.f.signer, Clock: env.f.clock, Mail: env.f.mail, CSRF: csrf,
		Providers: gologin.NewProviders(providers...), LoginStates: testLoginStates(t)}, &user.Config{
		PublicBaseURL: baseURL, ActivationTTL: time.Hour, ResetTTL: time.Hour, InviteTTL: time.Hour,
		RegistrationOpen: true, PasswordSignInDisabled: true,
		LoginLimit: generous(), MailLimit: generous(), InviteLimit: generous(), TenantCreateLimit: generous(),
		LoginClientLimit: generous(), MailClientLimit: generous(),
	}, slog.DiscardHandler)
}

func TestPasswordSignInOffRefusesEveryPasswordRoute(t *testing.T) {
	env := newProviderEnv(t)
	a := env.member(t, "a@example.com")
	off := passwordOff(t, env)

	// Tokens a password deployment issued, so each refusal below is the
	// switch and not a bad token.
	require.NoError(t, env.f.svc.RequestPasswordReset(env.f.ctx, "a@example.com"))
	resetToken := tokenFrom(t, env.f.mail.last(t, "a@example.com"))
	inviteToken := env.inviteToken(t, a, "new@example.com")
	mailBefore := len(env.f.mail.to("a@example.com"))

	_, err := off.Login(env.f.ctx, "a@example.com", password)
	assert.ErrorIs(t, err, user.ErrPasswordSignInOff, "sign-in")
	assert.ErrorIs(t, off.Register(env.f.ctx, &user.RegisterInput{Email: "b@example.com", Password: password, DisplayName: "B"}),
		user.ErrPasswordSignInOff, "sign-up")
	assert.ErrorIs(t, off.RequestPasswordReset(env.f.ctx, "a@example.com"), user.ErrPasswordSignInOff, "reset request")
	assert.Len(t, env.f.mail.to("a@example.com"), mailBefore, "no reset link mailed")
	assert.ErrorIs(t, off.ResetPassword(env.f.ctx, resetToken, "another good password"), user.ErrPasswordSignInOff, "reset")
	_, err = off.AcceptInviteAsNewAccount(env.f.ctx, inviteToken, "New", password, "")
	assert.ErrorIs(t, err, user.ErrPasswordSignInOff, "invite accepted with a password")
	_, err = off.ReauthenticateWithPassword(env.f.ctx, a, password)
	assert.ErrorIs(t, err, user.ErrPasswordSignInOff, "step-up by password")
	_, err = off.SetPassword(env.f.ctx, a, "another good password")
	assert.ErrorIs(t, err, user.ErrPasswordSignInOff, "setting a password")

	// Nothing was spent by the refusals: the reset link still works where
	// passwords are on.
	require.NoError(t, env.f.svc.ResetPassword(env.f.ctx, resetToken, "another good password"))
}

// With passwords off, a password is no way in, so it can't be what keeps an
// account's last provider identity removable.
func TestWithPasswordSignInOffAPasswordIsNoWayIn(t *testing.T) {
	env := newProviderEnv(t)
	_, oidcOnly := newFakeOIDC(t)
	off := passwordOff(t, env, oidcOnly.provider)

	a := env.member(t, "a@example.com")
	env.link(t, a, oidcName, "sub-a")
	assert.ErrorIs(t, off.RemoveIdentity(env.f.ctx, a, oidcName, "sub-a"), user.ErrLastMethod)

	m, err := off.SignInMethods(env.f.ctx, a)
	require.NoError(t, err)
	assert.Len(t, m.Identities, 1, "the refused removal left the identity linked")

	// The same account shape where passwords count: the password is the
	// way in that remains.
	b := env.member(t, "b@example.com")
	env.link(t, b, oidcName, "sub-b")
	assert.NoError(t, env.f.svc.RemoveIdentity(env.f.ctx, b, oidcName, "sub-b"))
}

func TestPasswordSignInOffOverHTTP(t *testing.T) {
	mux, _ := newTestMux(t, func(c *user.Config) { c.PasswordSignInDisabled = true })
	b := newBrowser(t, mux)

	rec := b.do(http.MethodGet, "/api/auth/config", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var cfg struct {
		PasswordLogin bool `json:"password_login"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &cfg))
	assert.False(t, cfg.PasswordLogin, "the sign-in page is told not to offer a password")

	rec = b.do(http.MethodPost, "/api/auth/login", `{"email":"a@example.com","password":"correct horse battery"}`)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Equal(t, "password_sign_in_off", problem(t, rec).Code)
}

// The point of the switch: the provider still signs people in, and the
// account page is told passwords are off even for an account that has one.
func TestPasswordSignInOffStillSignsInThroughAProvider(t *testing.T) {
	env := newProviderEnv(t, func(o *fixtureOpts) {
		o.config = append(o.config, func(c *user.Config) { c.PasswordSignInDisabled = true })
	})
	_, err := user.NewSeeder(env.f.store).EnsureActive(env.f.ctx, &user.RegisterInput{Email: "a@example.com", Password: password, DisplayName: "A"})
	require.NoError(t, err)
	a, err := env.f.store.FindByEmail(env.f.ctx, "a@example.com")
	require.NoError(t, err)
	env.link(t, &authn.User{ID: a.ID}, oidcName, "sub-a")

	b := newBrowser(t, env.mux)
	rec := b.callback(oidcName, b.start("/api/auth/login/dex"), asOIDC(env.idp, "sub-a", "a"))
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	assert.Equal(t, a.ID, b.session(env.f).ID)

	rec = b.do(http.MethodGet, "/api/auth/sign-in-methods", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var methods struct {
		Password             *struct{} `json:"password"`
		PasswordLoginEnabled bool      `json:"password_login_enabled"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &methods))
	assert.False(t, methods.PasswordLoginEnabled)
	assert.NotNil(t, methods.Password, "the stored password is still shown, so it can be removed")

	rec = newBrowser(t, env.mux).do(http.MethodPost, "/api/auth/login", `{"email":"a@example.com","password":"correct horse battery"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code, "...but it signs nobody in")

	// The provider sign-in was a fresh authentication, so the unusable
	// password can be removed straight away.
	rec = b.do(http.MethodDelete, "/api/auth/sign-in-methods/password", "")
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	gone, err := env.f.store.FindByEmail(env.f.ctx, "a@example.com")
	require.NoError(t, err)
	assert.Empty(t, gone.PasswordHash)
}

// Step-up by password counts against the account whatever client it comes
// from: a session thief moving between addresses gets no more guesses.
func TestPasswordStepUpIsLimitedPerAccountAcrossClients(t *testing.T) {
	f := newFixture(t, func(c *user.Config) {
		c.LoginLimit = &ratelimit.Config{Limit: 2, Window: time.Minute, MaxKeys: 10}
	})
	f.activeUser(t, "a@example.com")
	who := f.signIn(t, "a@example.com")
	from := func(addr string) context.Context { return clientip.NewContext(f.ctx, netip.MustParseAddr(addr)) }

	_, err := f.svc.ReauthenticateWithPassword(from("203.0.113.1"), who, "wrong")
	assert.ErrorIs(t, err, user.ErrInvalidCredentials)
	_, err = f.svc.ReauthenticateWithPassword(from("203.0.113.2"), who, "wrong")
	assert.ErrorIs(t, err, user.ErrInvalidCredentials)
	_, err = f.svc.ReauthenticateWithPassword(from("203.0.113.3"), who, password)
	assert.ErrorIs(t, err, user.ErrRateLimited, "a third client is the same account")
}
