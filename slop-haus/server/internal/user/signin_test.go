package user_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/auth"
	gologin "github.com/lowaa/slop-factory/go-auth/login"
	"github.com/lowaa/slop-factory/go-auth/login/logintest"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

// providerEnv is the board with both kinds of login provider configured:
// GitHub (a fake of its API) and an OIDC issuer (go-auth's fake IdP).
type providerEnv struct {
	mux *http.ServeMux
	f   *fixture
	idp *logintest.Server
	gh  *fakeGitHub
}

func newProviderEnv(t *testing.T, opts ...func(*fixtureOpts)) *providerEnv {
	t.Helper()
	idp, oidcProvider := newFakeOIDC(t)
	gh, githubProvider := newFakeGitHub(t)
	o := &fixtureOpts{providers: []testProvider{githubProvider, oidcProvider}}
	for _, fn := range opts {
		fn(o)
	}
	mux, f := newTestMuxWith(t, o)
	return &providerEnv{mux: mux, f: f, idp: idp, gh: gh}
}

// member signs address up and returns their session, as the service sees
// it.
func (e *providerEnv) member(t *testing.T, address string) *authn.User {
	t.Helper()
	e.f.activeUser(t, address)
	return e.f.signIn(t, address)
}

func (e *providerEnv) link(t *testing.T, who *authn.User, provider, subject string) {
	t.Helper()
	require.NoError(t, e.f.store.LinkIdentity(e.f.ctx, who.ID, &user.Identity{Provider: provider, Subject: subject, Login: subject}))
}

func (e *providerEnv) inviteToken(t *testing.T, inviter *authn.User, address string) string {
	t.Helper()
	_, err := e.f.svc.InviteToTenant(e.f.ctx, inviter, address)
	require.NoError(t, err)
	return tokenFrom(t, e.f.mail.last(t, address))
}

// providerOnlyAccount makes address an account through inviter's invite,
// accepted with the OIDC identity sub: an account with no password.
func (e *providerEnv) providerOnlyAccount(t *testing.T, inviter *authn.User, address, sub string) *browser {
	t.Helper()
	token := e.inviteToken(t, inviter, address)
	b := newBrowser(t, e.mux)
	rec := b.callback(oidcName, b.startInvite(token, ""), asOIDC(e.idp, sub, sub))
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	return b
}

func (e *providerEnv) owner(t *testing.T, provider, subject string) *authn.User {
	t.Helper()
	id, err := e.f.store.IdentityOwner(e.f.ctx, provider, subject)
	if err != nil {
		require.ErrorIs(t, err, user.ErrNotFound)
		return nil
	}
	return &authn.User{ID: id}
}

func TestProviderSignInForALinkedAccount(t *testing.T) {
	env := newProviderEnv(t)
	a := env.member(t, "a@example.com")
	env.link(t, a, oidcName, "sub-a")

	b := newBrowser(t, env.mux)
	authURL := b.start("/api/auth/login/dex?next=/tickets/7")
	assert.NotEmpty(t, b.cookie(user.LoginStateCookie, user.CallbackPath(oidcName)))
	assert.Empty(t, b.cookie(user.LoginStateCookie, "/api/auth/me"), "the state cookie goes to the callback and nowhere else")
	assert.Empty(t, b.cookie(user.LoginStateCookie, user.CallbackPath("github")), "...and only this provider's")

	before := time.Now().Add(-time.Second)
	rec := b.callback(oidcName, authURL, asOIDC(env.idp, "sub-a", "a"))
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	assert.Equal(t, "/tickets/7", rec.Header().Get("Location"))
	assert.Empty(t, b.cookie(user.LoginStateCookie, user.CallbackPath(oidcName)), "a round trip is good for one callback")

	who := b.session(env.f)
	assert.Equal(t, a.ID, who.ID)
	assert.Equal(t, a.TenantID, who.TenantID, "lands in the same tenant a password sign-in does")
	assert.True(t, who.AuthTime.After(before), "a sign-in is an interactive authentication")
	assert.NotEmpty(t, b.cookie(user.RefreshCookie, user.RefreshPath), "the same session cookies the password sign-in sets")
	assert.Equal(t, http.StatusOK, b.do(http.MethodGet, "/api/auth/me", "").Code)
}

func TestGitHubSignInForALinkedAccount(t *testing.T) {
	env := newProviderEnv(t)
	a := env.member(t, "a@example.com")
	env.link(t, a, "github", "4242")

	b := newBrowser(t, env.mux)
	authURL := b.start("/api/auth/login/github")
	u, err := url.Parse(authURL)
	require.NoError(t, err)
	assert.False(t, u.Query().Has("scope"), "GitHub is asked for no scopes: the public profile is all a sign-in needs")

	rec := b.callback("github", authURL, asGitHub(env.gh, githubUser{ID: 4242, Login: "octo"}))
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	assert.Equal(t, "/", rec.Header().Get("Location"))
	assert.Equal(t, a.ID, b.session(env.f).ID)
	assert.Equal(t, 1, env.f.events.CountMethod(audit.SignIn, "github"), "counted by the method it was made with")

	linked, err := env.f.store.ListIdentities(env.f.ctx, a.ID)
	require.NoError(t, err)
	require.Len(t, linked, 1)
	assert.Equal(t, "octo", linked[0].Login, "what the provider last said is kept, to recognise the identity by")
	assert.NotNil(t, linked[0].LastLoginAt)
}

// A provider sign-in never makes an account, and never finds one by
// email -- not even an address the provider says it has verified.
func TestAnUnknownIdentityIsRefused(t *testing.T) {
	env := newProviderEnv(t)
	env.member(t, "victim@idp.example")
	users := env.f.count(t, `SELECT count(*) FROM global_users`)

	b := newBrowser(t, env.mux)
	rec := b.callback(oidcName, b.start("/api/auth/login/dex"), asOIDC(env.idp, "sub-stranger", "victim"))

	requireRefusal(t, rec, user.LoginPage, "no_account")
	assert.Equal(t, 1, env.f.events.CountMethod(audit.SignInRefused, oidcName))
	assert.Empty(t, b.cookie(authn.SessionCookie, "/"), "not signed in")
	assert.Empty(t, b.cookie(user.LoginStateCookie, user.CallbackPath(oidcName)), "the state cookie is cleared on a refusal too")
	assert.Equal(t, users, env.f.count(t, `SELECT count(*) FROM global_users`))
	assert.Zero(t, env.f.count(t, `SELECT count(*) FROM global_user_identities`))
}

func TestACallbackNeedsTheStateItsBrowserWasGiven(t *testing.T) {
	env := newProviderEnv(t)
	a := env.member(t, "a@example.com")
	env.link(t, a, oidcName, "sub-a")

	// Another browser's callback -- a login CSRF -- has no state cookie.
	victim := newBrowser(t, env.mux)
	attacker := newBrowser(t, env.mux)
	code, state := asOIDC(env.idp, "sub-a", "a")(t, attacker.start("/api/auth/login/dex"))
	rec := victim.navigate(user.CallbackPath(oidcName) + "?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(state))
	requireRefusal(t, rec, user.LoginPage, "login_expired")
	assert.Empty(t, victim.cookie(authn.SessionCookie, "/"))

	// The browser's own cookie, but another round trip's state.
	authURL := victim.start("/api/auth/login/dex")
	code, _ = asOIDC(env.idp, "sub-a", "a")(t, authURL)
	rec = victim.navigate(user.CallbackPath(oidcName) + "?code=" + url.QueryEscape(code) + "&state=forged")
	requireRefusal(t, rec, user.LoginPage, "login_expired")

	// Cancelled at the provider: the state, but no code.
	u, err := url.Parse(victim.start("/api/auth/login/dex"))
	require.NoError(t, err)
	rec = victim.navigate(user.CallbackPath(oidcName) + "?error=access_denied&state=" + url.QueryEscape(u.Query().Get("state")))
	requireRefusal(t, rec, user.LoginPage, "login_cancelled")
}

func TestAnUnconfiguredProviderIs404(t *testing.T) {
	env := newProviderEnv(t)
	b := newBrowser(t, env.mux)
	assert.Equal(t, http.StatusNotFound, b.navigate("/api/auth/login/gitlab").Code)
	// A callback is a navigation back from somewhere, so it goes back to the
	// board however wrong it is.
	requireRefusal(t, b.navigate(user.CallbackPath("gitlab")+"?code=x&state=y"), user.LoginPage, "unknown_provider")
}

func TestAcceptingAnInviteWithAProviderMakesTheAccountAtTheInvitesAddress(t *testing.T) {
	env := newProviderEnv(t)
	a := env.member(t, "a@example.com")
	token := env.inviteToken(t, a, "b@example.com")

	b := newBrowser(t, env.mux)
	rec := b.callback(oidcName, b.startInvite(token, "/board"), asOIDC(env.idp, "sub-b", "bee"))
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	assert.Equal(t, "/board", rec.Header().Get("Location"))

	who := b.session(env.f)
	assert.Equal(t, a.TenantID, who.TenantID, "signed in, in the tenant the invite grants")
	var me user.ProfileBody
	require.NoError(t, json.Unmarshal(b.do(http.MethodGet, "/api/auth/me", "").Body.Bytes(), &me))
	assert.Equal(t, "b@example.com", me.Email, "the invite's address -- the link proved it -- not the provider's bee@idp.example")
	assert.Equal(t, "Person bee", me.DisplayName)

	var methods struct {
		Password   *user.PasswordMethodBody  `json:"password"`
		Identities []user.IdentityMethodBody `json:"identities"`
	}
	rec = b.do(http.MethodGet, "/api/auth/sign-in-methods", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &methods))
	assert.Nil(t, methods.Password, "no password: the account signs in with the provider")
	assert.Equal(t, []user.IdentityMethodBody{{Provider: oidcName, Subject: "sub-b", Login: "bee", Usable: true}}, methods.Identities)

	_, err := env.f.svc.PreviewInvite(env.f.ctx, token)
	assert.ErrorIs(t, err, user.ErrInvalidToken, "the invite is spent")

	again := newBrowser(t, env.mux)
	rec = again.callback(oidcName, again.start("/api/auth/login/dex"), asOIDC(env.idp, "sub-b", "bee"))
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	assert.Equal(t, who.ID, again.session(env.f).ID, "and signs in with it from then on")
}

func TestAcceptingAnInviteWithAnIdentityLinkedElsewhereIsRefused(t *testing.T) {
	env := newProviderEnv(t)
	a := env.member(t, "a@example.com")
	c := env.member(t, "c@example.com")
	env.link(t, c, oidcName, "sub-c")
	token := env.inviteToken(t, a, "b@example.com")

	b := newBrowser(t, env.mux)
	rec := b.callback(oidcName, b.startInvite(token, ""), asOIDC(env.idp, "sub-c", "c"))

	requireRefusal(t, rec, user.InvitePage, "identity_in_use")
	assert.Empty(t, b.cookie(authn.SessionCookie, "/"))
	assert.Equal(t, c.ID, env.owner(t, oidcName, "sub-c").ID, "identities never move")
	assert.Zero(t, env.f.count(t, `SELECT count(*) FROM global_users WHERE email = 'b@example.com'`))
	_, err := env.f.svc.PreviewInvite(env.f.ctx, token)
	assert.NoError(t, err, "a refusal spends nothing")
}

// An address that already has an account gets no second one, and no
// identity attached to it by virtue of the address: its owner signs in
// and accepts, or links, from there.
func TestAcceptingAnInviteForAnExistingAccountWithANewIdentityIsRefused(t *testing.T) {
	env := newProviderEnv(t)
	a := env.member(t, "a@example.com")
	env.member(t, "b@example.com")
	token := env.inviteToken(t, a, "b@example.com")

	b := newBrowser(t, env.mux)
	rec := b.callback(oidcName, b.startInvite(token, ""), asOIDC(env.idp, "sub-new", "b"))
	requireRefusal(t, rec, user.InvitePage, "account_exists")
	assert.Nil(t, env.owner(t, oidcName, "sub-new"))
}

// The invitee's account already signs in with the identity: accepting with
// it is AcceptInvite, for that account.
func TestAcceptingAnInviteWithTheInviteesOwnIdentityJoinsTheTenant(t *testing.T) {
	env := newProviderEnv(t)
	a := env.member(t, "a@example.com")
	bee := env.member(t, "b@example.com")
	env.link(t, bee, oidcName, "sub-b")
	token := env.inviteToken(t, a, "b@example.com")

	b := newBrowser(t, env.mux)
	rec := b.callback(oidcName, b.startInvite(token, ""), asOIDC(env.idp, "sub-b", "b"))
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	who := b.session(env.f)
	assert.Equal(t, bee.ID, who.ID)
	assert.Equal(t, a.TenantID, who.TenantID)
}

// Only a plain sign-in starts from a navigation: any page can send a
// browser there, and a sign-in only ever signs in as whoever the browser
// is at the provider. Accepting an invite or re-authenticating acts on an
// account -- a hostile page could otherwise send a visitor through an
// invite of the attacker's, binding the visitor's identity to an account
// the attacker invited -- so a GET asking for either starts nothing, and
// an invite token in a URL is never acted on.
func TestAGetStartsOnlyAPlainSignIn(t *testing.T) {
	env := newProviderEnv(t)
	a := env.member(t, "a@example.com")
	token := env.inviteToken(t, a, "b@example.com")
	signedIn := newBrowser(t, env.mux)
	signedIn.signInWithPassword("a@example.com")
	signedIn.staleSession(env.f)
	stale := signedIn.session(env.f).AuthTime

	for _, query := range []string{
		"?invite=" + url.QueryEscape(token),
		"?invite=" + url.QueryEscape(token) + "&intent=accept_invite",
		"?intent=reauth",
		"?intent=accept_invite",
		"?intent=link",
		"?intent=sudo",
	} {
		for name, b := range map[string]*browser{"signed out": newBrowser(t, env.mux), "signed in": signedIn} {
			rec := b.navigate("/api/auth/login/dex" + query + "&next=/x")
			assert.Equal(t, http.StatusBadRequest, rec.Code, name+" "+query)
			assert.Equal(t, "post_required", problem(t, rec).Code, name+" "+query)
			assert.Empty(t, rec.Header().Get("Location"), name+" "+query)
			assert.Empty(t, b.cookie(user.LoginStateCookie, user.CallbackPath(oidcName)), "%s %s: no round trip started", name, query)
		}
	}
	_, err := env.f.svc.PreviewInvite(env.f.ctx, token)
	assert.NoError(t, err, "the invite is untouched")
	assert.Equal(t, stale, signedIn.session(env.f).AuthTime, "and the session too")

	for _, query := range []string{"", "?intent=login", "?next=/x"} {
		b := newBrowser(t, env.mux)
		assert.Equal(t, http.StatusFound, b.navigate("/api/auth/login/dex"+query).Code, query)
		assert.NotEmpty(t, b.cookie(user.LoginStateCookie, user.CallbackPath(oidcName)), query)
	}
}

// The round trips that act on an account start from POSTs, which another
// site's page can't make: they must come from the board's origin, and the
// signed-in ones must carry the session's CSRF token too.
func TestAccountActingStartsRefuseAnotherSite(t *testing.T) {
	env := newProviderEnv(t)
	a := env.member(t, "a@example.com")
	token := env.inviteToken(t, a, "b@example.com")
	b := newBrowser(t, env.mux)
	b.signInWithPassword("a@example.com")

	post := func(path, body, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, baseURL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		return b.send(req)
	}
	for path, body := range map[string]string{
		"/api/auth/invite/accept/dex": `{"token":"` + token + `"}`,
		"/api/auth/reauth/dex":        `{}`,
		"/api/auth/link/dex":          `{}`,
	} {
		rec := post(path, body, "https://evil.example")
		assert.Equal(t, http.StatusForbidden, rec.Code, path)
		assert.Equal(t, authn.CodeCrossOrigin, problem(t, rec).Code, path)
	}
	for _, path := range []string{"/api/auth/reauth/dex", "/api/auth/link/dex"} {
		rec := post(path, `{}`, baseURL)
		assert.Equal(t, http.StatusForbidden, rec.Code, path)
		assert.Equal(t, authn.CodeCSRF, problem(t, rec).Code, path)
	}
	assert.Empty(t, b.cookie(user.LoginStateCookie, user.CallbackPath(oidcName)))
}

// A dead invite fails before the round trip, not after it.
func TestADeadInviteFailsBeforeTheProvider(t *testing.T) {
	env := newProviderEnv(t)
	b := newBrowser(t, env.mux)
	rec := b.do(http.MethodPost, "/api/auth/invite/accept/dex", `{"token":"made-up"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, "invalid_token", problem(t, rec).Code)
	assert.Empty(t, b.cookie(user.LoginStateCookie, user.CallbackPath(oidcName)))
}

// Only a state this server sealed is acted on. Another device on the
// domain can plant a cookie in the browser (cookie tossing) -- the state
// cookie is path-scoped, so it can't be __Host- -- such as a link intent
// naming the victim; it is refused before the code is ever exchanged.
func TestAStateCookieThisServerDidNotSealIsRefused(t *testing.T) {
	env := newProviderEnv(t)
	victim := env.member(t, "victim@example.com")
	b := newBrowser(t, env.mux)
	b.signInWithPassword("victim@example.com")
	authURL := b.start("/api/auth/login/dex")
	u, err := url.Parse(authURL)
	require.NoError(t, err)
	attempt := &gologin.LoginAttempt{State: u.Query().Get("state"), Verifier: strings.Repeat("v", 43), Nonce: "n"}
	link := &gologin.LoginState{Attempt: attempt, Next: "/", Intent: gologin.IntentLink, UserID: victim.ID.String()}

	otherKey, err := gologin.NewLoginStateCodec([]byte(strings.Repeat("o", 32)))
	require.NoError(t, err)
	planted, err := otherKey.Encode(link)
	require.NoError(t, err)
	for name, value := range map[string]string{
		"sealed with another key": planted,
		"unsealed":                base64.RawURLEncoding.EncodeToString([]byte(`{"s":"` + attempt.State + `","v":"v","n":"n","r":"/","i":"link","u":"` + victim.ID.String() + `"}`)),
	} {
		b.jar.SetCookies(siteURL, []*http.Cookie{{Name: user.LoginStateCookie, Value: value, Path: user.CallbackPath(oidcName)}})
		rec := b.callback(oidcName, authURL, asOIDC(env.idp, "sub-attacker", "attacker"))
		requireRefusal(t, rec, user.LoginPage, "login_expired", name)
		assert.Nil(t, env.owner(t, oidcName, "sub-attacker"), "%s: nothing linked", name)
	}
}

func TestLinkingAProviderIdentity(t *testing.T) {
	env := newProviderEnv(t)
	a := env.member(t, "a@example.com")
	b := newBrowser(t, env.mux)
	b.signInWithPassword("a@example.com")

	rec := b.do(http.MethodPost, "/api/auth/link/dex", `{"next":"/account"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var started struct {
		RedirectURL string `json:"redirect_url"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &started))

	rec = b.callback(oidcName, started.RedirectURL, asOIDC(env.idp, "sub-a", "a"))
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	assert.Equal(t, "/account", rec.Header().Get("Location"))
	assert.Equal(t, a.ID, env.owner(t, oidcName, "sub-a").ID)
	assert.Equal(t, 1, env.f.events.CountMethod(audit.IdentityLinked, oidcName))

	other := newBrowser(t, env.mux)
	rec = other.callback(oidcName, other.start("/api/auth/login/dex"), asOIDC(env.idp, "sub-a", "a"))
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	assert.Equal(t, a.ID, other.session(env.f).ID, "the linked identity now signs in to the account")

	// Linking the same identity again is not an error.
	rec = b.do(http.MethodPost, "/api/auth/link/dex", `{}`)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &started))
	rec = b.callback(oidcName, started.RedirectURL, asOIDC(env.idp, "sub-a", "a"))
	assert.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
}

// The state cookie only says who started the link. If the browser has
// since signed in as someone else -- or out -- the identity goes to
// nobody.
func TestALinkCompletesOnlyForTheSessionThatStartedIt(t *testing.T) {
	env := newProviderEnv(t)
	env.member(t, "a@example.com")
	env.member(t, "c@example.com")

	for name, change := range map[string]func(b *browser){
		"signed in as someone else": func(b *browser) { b.signInWithPassword("c@example.com") },
		"signed out":                func(b *browser) { b.do(http.MethodPost, "/api/auth/logout", "") },
	} {
		t.Run(name, func(t *testing.T) {
			b := newBrowser(t, env.mux)
			b.signInWithPassword("a@example.com")
			rec := b.do(http.MethodPost, "/api/auth/link/dex", `{}`)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var started struct {
				RedirectURL string `json:"redirect_url"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &started))

			change(b)
			rec = b.callback(oidcName, started.RedirectURL, asOIDC(env.idp, "sub-a", "a"))
			requireRefusal(t, rec, user.AccountPage, "session_mismatch")
			assert.Nil(t, env.owner(t, oidcName, "sub-a"), "attached to nobody")
		})
	}
}

func TestLinkingAnotherAccountsIdentityIsRefused(t *testing.T) {
	env := newProviderEnv(t)
	env.member(t, "a@example.com")
	c := env.member(t, "c@example.com")
	env.link(t, c, oidcName, "sub-c")

	b := newBrowser(t, env.mux)
	b.signInWithPassword("a@example.com")
	rec := b.do(http.MethodPost, "/api/auth/link/dex", `{}`)
	var started struct {
		RedirectURL string `json:"redirect_url"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &started))
	rec = b.callback(oidcName, started.RedirectURL, asOIDC(env.idp, "sub-c", "c"))

	requireRefusal(t, rec, user.AccountPage, "identity_in_use")
	assert.Equal(t, c.ID, env.owner(t, oidcName, "sub-c").ID, "identities never move")
}

func TestOneIdentityPerProviderPerAccount(t *testing.T) {
	env := newProviderEnv(t)
	a := env.member(t, "a@example.com")
	env.link(t, a, oidcName, "sub-a")
	err := env.f.store.LinkIdentity(env.f.ctx, a.ID, &user.Identity{Provider: oidcName, Subject: "sub-a2"})
	assert.ErrorIs(t, err, user.ErrProviderAlreadyLinked)
}

// Every change to how an account signs in -- and starting a link -- needs
// an interactive authentication in the last ten minutes.
func TestSignInMethodChangesNeedARecentSignIn(t *testing.T) {
	env := newProviderEnv(t)
	a := env.member(t, "a@example.com")
	env.link(t, a, oidcName, "sub-a")
	b := newBrowser(t, env.mux)
	b.signInWithPassword("a@example.com")
	b.staleSession(env.f)

	for _, req := range []struct{ method, path, body string }{
		{http.MethodPut, "/api/auth/sign-in-methods/password", `{"new_password":"another good password"}`},
		{http.MethodDelete, "/api/auth/sign-in-methods/password", ""},
		{http.MethodDelete, "/api/auth/sign-in-methods/identity?provider=dex&subject=sub-a", ""},
		{http.MethodPost, "/api/auth/link/dex", `{}`},
	} {
		rec := b.do(req.method, req.path, req.body)
		assert.Equal(t, http.StatusForbidden, rec.Code, req.method+" "+req.path)
		assert.Equal(t, "reauth_required", problem(t, rec).Code, req.method+" "+req.path)
	}
	assert.Empty(t, b.cookie(user.LoginStateCookie, user.CallbackPath(oidcName)), "no link was started")
	_, err := env.f.svc.Login(env.f.ctx, "a@example.com", password)
	assert.NoError(t, err, "the password is unchanged")
	assert.Equal(t, a.ID, env.owner(t, oidcName, "sub-a").ID, "the identity is still linked")

	var list struct {
		ReauthRequiredAfter time.Time `json:"reauth_required_after"`
	}
	require.NoError(t, json.Unmarshal(b.do(http.MethodGet, "/api/auth/sign-in-methods", "").Body.Bytes(), &list))
	assert.True(t, list.ReauthRequiredAfter.Before(time.Now()), "the list says the window has closed")

	rec := b.do(http.MethodPost, "/api/auth/reauth/password", `{"password":"wrong password"}`)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "invalid_credentials", problem(t, rec).Code)
	assert.Equal(t, http.StatusForbidden, b.do(http.MethodDelete, "/api/auth/sign-in-methods/password", "").Code, "still stale")

	sid, token := b.session(env.f).SessionID, b.csrfToken()
	rec = b.do(http.MethodPost, "/api/auth/reauth/password", `{"password":"`+password+`"}`)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.WithinDuration(t, time.Now(), b.session(env.f).AuthTime, 2*time.Second, "re-issued with a fresh auth time")
	assert.Equal(t, a.TenantID, b.session(env.f).TenantID)
	assert.Equal(t, sid, b.session(env.f).SessionID, "but the same session")
	assert.Equal(t, token, b.csrfToken(), "so the page's CSRF token still holds")
	rec = b.do(http.MethodPut, "/api/auth/sign-in-methods/password", `{"new_password":"another good password"}`)
	assert.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Equal(t, sid, b.session(env.f).SessionID, "a password change keeps this session")
}

func TestReauthWithTheProvider(t *testing.T) {
	env := newProviderEnv(t)
	a := env.member(t, "a@example.com")
	env.link(t, a, oidcName, "sub-a")
	b := newBrowser(t, env.mux)
	b.signInWithPassword("a@example.com")
	b.staleSession(env.f)
	stale := b.session(env.f).AuthTime
	sid, token := b.session(env.f).SessionID, b.csrfToken()

	authURL := b.post("/api/auth/reauth/dex", `{"next":"/account"}`)
	assert.Equal(t, stale, b.session(env.f).AuthTime, "starting a reauth re-issues the session, but proves nothing yet")
	rec := b.callback(oidcName, authURL, asOIDC(env.idp, "sub-a", "a"))
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	assert.Equal(t, "/account", rec.Header().Get("Location"))
	who := b.session(env.f)
	assert.Equal(t, a.ID, who.ID)
	assert.Equal(t, a.TenantID, who.TenantID)
	assert.WithinDuration(t, time.Now(), who.AuthTime, 2*time.Second)
	assert.Equal(t, sid, who.SessionID, "the same session, re-authenticated")
	assert.Equal(t, token, b.csrfToken())
	assert.Equal(t, 1, env.f.events.CountMethod(audit.Reauth, oidcName))

	rec = b.do(http.MethodDelete, "/api/auth/sign-in-methods/password", "")
	assert.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
}

// A reauth proves the signed-in user is present only with their own
// identity: anyone else's, or one nobody has, proves nothing.
func TestReauthWithSomeoneElsesIdentityIsRefused(t *testing.T) {
	env := newProviderEnv(t)
	a := env.member(t, "a@example.com")
	c := env.member(t, "c@example.com")
	env.link(t, a, oidcName, "sub-a")
	env.link(t, c, oidcName, "sub-c")

	for _, sub := range []string{"sub-c", "sub-nobody"} {
		b := newBrowser(t, env.mux)
		b.signInWithPassword("a@example.com")
		b.staleSession(env.f)
		stale := b.session(env.f).AuthTime

		rec := b.callback(oidcName, b.post("/api/auth/reauth/dex", `{}`), asOIDC(env.idp, sub, sub))
		requireRefusal(t, rec, user.AccountPage, "identity_mismatch", sub)
		assert.Equal(t, stale, b.session(env.f).AuthTime, "%s: still stale", sub)
		assert.Equal(t, a.ID, b.session(env.f).ID)
	}

	signedOut := newBrowser(t, env.mux)
	rec := signedOut.do(http.MethodPost, "/api/auth/reauth/dex", `{}`)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, signedOut.cookie(user.LoginStateCookie, user.CallbackPath(oidcName)))
}

// The auth time and session id are set by an interactive sign-in and
// nothing else: every way a session is re-issued from an existing one
// carries them unchanged -- and so the page's CSRF token, derived from the
// id, holds for the life of the session.
func TestTheSessionSurvivesRefreshInvitesAndTenantSwitches(t *testing.T) {
	env := newProviderEnv(t)
	a := env.member(t, "a@example.com")
	bee := env.member(t, "b@example.com")
	b := newBrowser(t, env.mux)
	signedInAt := time.Now().Add(-5 * time.Minute).Truncate(time.Second)
	const sid = "the-browser-session"
	b.setSession(env.f, bee.ID, bee.TenantID, &auth.Session{AuthTime: signedInAt, ID: sid})
	csrf := b.csrfToken()
	require.NotEmpty(t, csrf)
	same := func(step string) {
		t.Helper()
		who := b.session(env.f)
		assert.Equal(t, signedInAt, who.AuthTime, step)
		assert.Equal(t, sid, who.SessionID, step)
		assert.Equal(t, csrf, b.csrfToken(), step)
	}

	rec := b.do(http.MethodPost, user.RefreshPath, "")
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	refreshed := b.cookie(authn.SessionCookie, "/")
	assert.Equal(t, signedInAt, authTimeOf(t, env.f.signer, refreshed), "refresh")
	same("refresh")

	token := env.inviteToken(t, a, "b@example.com")
	rec = b.do(http.MethodPost, "/api/invites/accept", `{"token":"`+token+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, a.TenantID, b.session(env.f).TenantID)
	same("accepting an invite signed in")
	var profile user.ProfileBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &profile))
	assert.Equal(t, csrf, profile.CSRFToken, "the answer's token is the session's")

	rec = b.do(http.MethodPost, "/api/auth/switch-tenant", `{"tenant_id":"`+bee.TenantID.String()+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, bee.TenantID, b.session(env.f).TenantID)
	same("switching tenant")

	rec = b.do(http.MethodPost, user.RefreshPath, "")
	require.Equal(t, http.StatusNoContent, rec.Code)
	same("refreshing the switched session")

	b.signInWithPassword("b@example.com")
	assert.NotEqual(t, sid, b.session(env.f).SessionID, "signing in again starts a new session")
	assert.NotEqual(t, csrf, b.csrfToken(), "with a new token")
}

// Every interactive sign-in starts a new session: a password, a provider,
// an invite accepted as a new account or through a provider.
func TestEverySignInStartsANewSession(t *testing.T) {
	env := newProviderEnv(t)
	a := env.member(t, "a@example.com")
	env.link(t, a, oidcName, "sub-a")
	seen := map[string]string{}
	fresh := func(how string, b *browser) {
		t.Helper()
		sid := b.session(env.f).SessionID
		require.NotEmpty(t, sid, how)
		assert.Empty(t, seen[sid], "%s reused %s's session id", how, seen[sid])
		seen[sid] = how
	}

	withPassword := newBrowser(t, env.mux)
	withPassword.signInWithPassword("a@example.com")
	fresh("a password", withPassword)

	provider := newBrowser(t, env.mux)
	require.Equal(t, http.StatusFound, provider.callback(oidcName, provider.start("/api/auth/login/dex"), asOIDC(env.idp, "sub-a", "a")).Code)
	fresh("a provider", provider)

	invited := newBrowser(t, env.mux)
	rec := invited.do(http.MethodPost, "/api/auth/invite/accept",
		`{"token":"`+env.inviteToken(t, a, "new@example.com")+`","display_name":"New","password":"`+password+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	fresh("an invite accepted with a password", invited)

	viaProvider := newBrowser(t, env.mux)
	authURL := viaProvider.startInvite(env.inviteToken(t, a, "newer@example.com"), "/")
	require.Equal(t, http.StatusFound, viaProvider.callback(oidcName, authURL, asOIDC(env.idp, "sub-newer", "newer")).Code)
	fresh("an invite accepted through a provider", viaProvider)
}

func TestAddingAPasswordToAProviderOnlyAccount(t *testing.T) {
	env := newProviderEnv(t)
	a := env.member(t, "a@example.com")
	b := env.providerOnlyAccount(t, a, "b@example.com", "sub-b")

	elsewhere := newBrowser(t, env.mux)
	rec := elsewhere.callback(oidcName, elsewhere.start("/api/auth/login/dex"), asOIDC(env.idp, "sub-b", "sub-b"))
	require.Equal(t, http.StatusFound, rec.Code)
	// Sessions are cut off by the second they were issued in; one issued in
	// the same second as the change would survive it.
	env.f.clock.Advance(2 * time.Second)

	rec = b.do(http.MethodPut, "/api/auth/sign-in-methods/password", `{"new_password":"a brand new password"}`)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	_, err := env.f.svc.Login(env.f.ctx, "b@example.com", "a brand new password")
	assert.NoError(t, err, "the account now signs in with a password too")

	assert.Equal(t, http.StatusNoContent, b.do(http.MethodPost, user.RefreshPath, "").Code, "this session was re-issued and survives")
	assert.Equal(t, http.StatusOK, b.do(http.MethodGet, "/api/auth/me", "").Code)
	assert.Equal(t, http.StatusUnauthorized, elsewhere.do(http.MethodPost, user.RefreshPath, "").Code, "every other session ends")
}

func TestChangingAndRemovingThePassword(t *testing.T) {
	env := newProviderEnv(t)
	a := env.member(t, "a@example.com")
	env.link(t, a, oidcName, "sub-a")
	b := newBrowser(t, env.mux)
	b.signInWithPassword("a@example.com")

	rec := b.do(http.MethodPut, "/api/auth/sign-in-methods/password", `{"new_password":"a changed password"}`)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Equal(t, 1, env.f.events.Count(audit.PasswordChanged))
	assert.Zero(t, env.f.events.Count(audit.PasswordAdded), "the account had one")
	_, err := env.f.svc.Login(env.f.ctx, "a@example.com", password)
	assert.ErrorIs(t, err, user.ErrInvalidCredentials, "the old password is gone")
	_, err = env.f.svc.Login(env.f.ctx, "a@example.com", "a changed password")
	assert.NoError(t, err)

	rec = b.do(http.MethodPut, "/api/auth/sign-in-methods/password", `{"new_password":"short"}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "the same length rules as everywhere")
	rec = b.do(http.MethodPut, "/api/auth/sign-in-methods/password", `{"new_password":"`+twoByteRunes(72)+`"}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	assert.Equal(t, "password_too_long", problem(t, rec).Code, "72 characters but more bytes: bcrypt's limit")

	rec = b.do(http.MethodDelete, "/api/auth/sign-in-methods/password", "")
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Equal(t, 1, env.f.events.CountMethod(audit.SignInMethodRemoved, "password"))
	_, err = env.f.svc.Login(env.f.ctx, "a@example.com", "a changed password")
	assert.ErrorIs(t, err, user.ErrInvalidCredentials)
	rec = b.do(http.MethodDelete, "/api/auth/sign-in-methods/password", "")
	assert.Equal(t, http.StatusNotFound, rec.Code, "there is no password to remove")

	other := newBrowser(t, env.mux)
	rec = other.callback(oidcName, other.start("/api/auth/login/dex"), asOIDC(env.idp, "sub-a", "a"))
	assert.Equal(t, http.StatusFound, rec.Code, "the provider still signs in")
}

// twoByteRunes is n characters of two bytes each.
func twoByteRunes(n int) string {
	return strings.Repeat("é", n)
}

func TestTheLastWayInCannotBeRemoved(t *testing.T) {
	env := newProviderEnv(t)
	a := env.member(t, "a@example.com")

	passwordOnly := newBrowser(t, env.mux)
	passwordOnly.signInWithPassword("a@example.com")
	rec := passwordOnly.do(http.MethodDelete, "/api/auth/sign-in-methods/password", "")
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, "last_method", problem(t, rec).Code)

	providerOnly := env.providerOnlyAccount(t, a, "b@example.com", "sub-b")
	rec = providerOnly.do(http.MethodDelete, "/api/auth/sign-in-methods/identity?provider=dex&subject=sub-b", "")
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, "last_method", problem(t, rec).Code)
	assert.NotNil(t, env.owner(t, oidcName, "sub-b"))

	rec = providerOnly.do(http.MethodDelete, "/api/auth/sign-in-methods/identity?provider=dex&subject=sub-a", "")
	assert.Equal(t, http.StatusNotFound, rec.Code, "not this account's identity")
}

// Only what can sign in here counts: an identity at a provider this
// deployment no longer offers is no way in.
func TestAnIdentityAtAnUnconfiguredProviderIsNoWayIn(t *testing.T) {
	gone := newProviderEnv(t)
	a := gone.member(t, "a@example.com")
	gone.link(t, a, "github", "4242")

	// The same database, but a deployment with only the OIDC provider.
	_, oidcOnly := newFakeOIDC(t)
	csrf, err := auth.NewCSRFTokens([]byte(testJWTSecret))
	require.NoError(t, err)
	svc := user.NewService(&user.Deps{Store: gone.f.store, Signer: gone.f.signer, Clock: gone.f.clock, Mail: gone.f.mail, CSRF: csrf,
		Providers: gologin.NewProviders(oidcOnly.provider), LoginStates: testLoginStates(t)}, &user.Config{
		PublicBaseURL: baseURL, ActivationTTL: time.Hour, ResetTTL: time.Hour, InviteTTL: time.Hour,
		LoginLimit: generous(), MailLimit: generous(), InviteLimit: generous(), TenantCreateLimit: generous(),
		LoginClientLimit: generous(), MailClientLimit: generous(),
	}, slog.DiscardHandler)

	m, err := svc.SignInMethods(gone.f.ctx, a)
	require.NoError(t, err)
	require.Len(t, m.Identities, 1)
	assert.False(t, m.Identities[0].Usable)
	assert.ErrorIs(t, svc.RemovePassword(gone.f.ctx, a), user.ErrLastMethod)
}

// Removals take the account's row lock, so two racing each other can't
// both see the other's method still there.
func TestRacingRemovalsCannotLeaveNoWayIn(t *testing.T) {
	env := newProviderEnv(t)
	for i := range 8 {
		who := env.member(t, fmt.Sprintf("racer%d@example.com", i))
		env.link(t, who, oidcName, fmt.Sprintf("sub-racer%d", i))

		var wg sync.WaitGroup
		start := make(chan struct{})
		errs := make([]error, 2)
		for j, remove := range []func() error{
			func() error { return env.f.svc.RemovePassword(env.f.ctx, who) },
			func() error { return env.f.svc.RemoveIdentity(env.f.ctx, who, oidcName, fmt.Sprintf("sub-racer%d", i)) },
		} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[j] = remove()
			}()
		}
		close(start)
		wg.Wait()

		failed := 0
		for _, err := range errs {
			if err != nil {
				assert.ErrorIs(t, err, user.ErrLastMethod)
				failed++
			}
		}
		assert.Equal(t, 1, failed, "exactly one removal wins")
		m, err := env.f.svc.SignInMethods(env.f.ctx, who)
		require.NoError(t, err)
		left := len(m.Identities)
		if m.HasPassword {
			left++
		}
		assert.Equal(t, 1, left, "one way in remains")
	}
}

// recordingPasswords is bcrypt that remembers which hashes it was asked to
// check against.
type recordingPasswords struct {
	mu      sync.Mutex
	checked []string
}

func (p *recordingPasswords) Hash(plain string) (string, error) { return auth.HashPassword(plain) }

func (p *recordingPasswords) Verify(hash, plain string) error {
	p.mu.Lock()
	p.checked = append(p.checked, hash)
	p.mu.Unlock()
	return auth.VerifyPassword(hash, plain)
}

func (p *recordingPasswords) take() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.checked
	p.checked = nil
	return out
}

// An account with no password is refused a password sign-in exactly as an
// unknown address is: the same answer, after the same bcrypt comparison.
func TestPasswordSignInToAnAccountWithoutOneIsRefusedLikeAnUnknownAddress(t *testing.T) {
	passwords := &recordingPasswords{}
	env := newProviderEnv(t, func(o *fixtureOpts) { o.passwords = passwords })
	a := env.member(t, "a@example.com")
	env.providerOnlyAccount(t, a, "b@example.com", "sub-b")
	passwords.take()

	_, err := env.f.svc.Login(env.f.ctx, "b@example.com", "any password at all")
	assert.ErrorIs(t, err, user.ErrInvalidCredentials)
	assert.Equal(t, []string{user.DummyHash}, passwords.take(), "one comparison, against the dummy hash")

	// An empty password can't match an empty hash either.
	_, err = env.f.svc.Login(env.f.ctx, "b@example.com", "")
	assert.ErrorIs(t, err, user.ErrInvalidCredentials)
	assert.Equal(t, []string{user.DummyHash}, passwords.take())

	b := newBrowser(t, env.mux)
	noPassword := b.do(http.MethodPost, "/api/auth/login", `{"email":"b@example.com","password":"x"}`)
	unknown := b.do(http.MethodPost, "/api/auth/login", `{"email":"nobody@example.com","password":"x"}`)
	assert.Equal(t, http.StatusUnauthorized, noPassword.Code)
	assert.Equal(t, unknown.Body.String(), noPassword.Body.String(), "indistinguishable on the wire")

	who := env.f.signIn(t, "a@example.com")
	passwords.take()
	_, err = env.f.svc.ReauthenticateWithPassword(env.f.ctx, who, "wrong")
	assert.ErrorIs(t, err, user.ErrInvalidCredentials)
}

func TestAPasswordResetForAnAccountWithoutAPasswordSendsANoteNotALink(t *testing.T) {
	env := newProviderEnv(t)
	a := env.member(t, "a@example.com")
	env.providerOnlyAccount(t, a, "b@example.com", "sub-b")
	sent := len(env.f.mail.to("b@example.com"))

	require.NoError(t, env.f.svc.RequestPasswordReset(env.f.ctx, "b@example.com"))
	mails := env.f.mail.to("b@example.com")
	require.Len(t, mails, sent+1)
	assert.NotContains(t, mails[len(mails)-1].body, "#token=", "no link that could set a password from an inbox")
	assert.Contains(t, mails[len(mails)-1].body, "no password")
}

// A deployment with no login providers -- production today -- whether
// handed an empty set or none at all: every route that deals in providers
// answers cleanly, and a provider named in a path is simply not there.
func TestNoProvidersConfigured(t *testing.T) {
	for name, opts := range map[string]*fixtureOpts{
		"nil":   {noProviders: true},
		"empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			mux, f := newTestMuxWith(t, opts)
			f.activeUser(t, "a@example.com")
			who := f.signIn(t, "a@example.com")
			b := newBrowser(t, mux)
			b.signInWithPassword("a@example.com")
			token := func() string {
				_, err := f.svc.InviteToTenant(f.ctx, who, "b@example.com")
				require.NoError(t, err)
				return tokenFrom(t, f.mail.last(t, "b@example.com"))
			}()

			rec := b.navigate("/api/auth/config")
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), `"providers":[]`)

			rec = b.do(http.MethodPost, "/api/auth/invite/preview", `{"token":"`+token+`"}`)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			rec = b.do(http.MethodGet, "/api/auth/sign-in-methods", "")
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), `"identities":[]`)

			rec = b.do(http.MethodDelete, "/api/auth/sign-in-methods/password", "")
			assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
			assert.Equal(t, "last_method", problem(t, rec).Code, "the password is the only way in")
			rec = b.do(http.MethodDelete, "/api/auth/sign-in-methods/identity?provider=dex&subject=x", "")
			assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

			requireRefusal(t, b.navigate(user.CallbackPath("dex")+"?code=x&state=y"), user.LoginPage, "unknown_provider")
			for _, rec := range []*httptest.ResponseRecorder{
				b.navigate("/api/auth/login/dex"),
				b.do(http.MethodPost, "/api/auth/link/dex", `{}`),
				b.do(http.MethodPost, "/api/auth/reauth/dex", `{}`),
				b.do(http.MethodPost, "/api/auth/invite/accept/dex", `{"token":"`+token+`"}`),
			} {
				assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
				assert.Equal(t, "unknown_provider", problem(t, rec).Code)
			}
			assert.Empty(t, b.cookie(user.LoginStateCookie, user.CallbackPath("dex")))
		})
	}
}

func TestAuthConfigNamesTheProviders(t *testing.T) {
	env := newProviderEnv(t)
	rec := newBrowser(t, env.mux).navigate("/api/auth/config")
	require.Equal(t, http.StatusOK, rec.Code)
	var cfg user.AuthConfigResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &cfg.Body))
	assert.True(t, cfg.Body.PasswordLogin)
	assert.Equal(t, []user.ProviderBody{{Name: "github", DisplayName: "GitHub"}, {Name: oidcName, DisplayName: "Dex"}}, cfg.Body.Providers)

	// Production today: no providers, and a list, not a null, saying so.
	mux, _ := newTestMux(t)
	rec = do(t, mux, request{method: http.MethodGet, path: "/api/auth/config"})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"providers":[]`)
	assert.Contains(t, rec.Body.String(), `"password_login":true`)
}
