package oauth_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/auth"
	"github.com/lowaa/slop-factory/go-auth/ratelimit"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit/audittest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth/oauthtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

func TestMain(m *testing.M) {
	dbtest.Main(m, schema.Migrations)
}

const issuer = "https://slop.example"

type noReleaser struct{}

func (noReleaser) ReleaseLeasesFor(context.Context, uuid.UUID, string, time.Time) error { return nil }

type grantsEndedRecorder struct {
	mu     sync.Mutex
	agents []uuid.UUID
}

func (r *grantsEndedRecorder) GrantsEnded(_ context.Context, _, agentID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents = append(r.agents, agentID)
	return nil
}

func (r *grantsEndedRecorder) calls() []uuid.UUID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uuid.UUID(nil), r.agents...)
}

// tenantMembers is the real membership store, as cmd/api wires it.
type tenantMembers struct{ store *user.PostgresStore }

func (m tenantMembers) IsTenantMember(ctx context.Context, userID, tenantID uuid.UUID) (bool, error) {
	return m.store.IsMember(ctx, userID, tenantID)
}

func (m tenantMembers) IsTenantOwner(ctx context.Context, userID, tenantID uuid.UUID) (bool, error) {
	return m.store.IsOwner(ctx, userID, tenantID)
}

type fixture struct {
	db  *db.DB
	svc *oauth.Service
	// admin is the operator's side of read-only clients, over the same
	// store and events.
	admin   *oauth.ClientAdmin
	agents  *agent.Service
	members tenantMembers
	user    *authn.User
	events  *audittest.Counter
	// recorder is what events counts.
	recorder *audit.Recorder
	ended    *grantsEndedRecorder
	// ctx is the signed-in user's, scoped to their tenant, as the consent
	// routes' context is.
	ctx context.Context
}

func generous() *ratelimit.Config {
	return &ratelimit.Config{Limit: 1000, Window: time.Minute, MaxKeys: 1000}
}

func testConfig() *oauth.Config {
	return &oauth.Config{
		Issuer:              issuer + "/",
		AccessTTL:           15 * time.Minute,
		RefreshTTL:          24 * time.Hour,
		CodeTTL:             time.Minute,
		AuthRequestTTL:      10 * time.Minute,
		DeviceTTL:           10 * time.Minute,
		PollInterval:        time.Nanosecond,
		RegisterLimit:       generous(),
		DeviceLookupLimit:   generous(),
		DeviceStartLimit:    generous(),
		SlotCredentialLimit: generous(),
		MaxUnclaimedClients: 1000,
	}
}

func newFixture(t *testing.T, tweak ...func(*oauth.Config)) *fixture {
	t.Helper()
	database := dbtest.WithDB(t)
	cfg := testConfig()
	for _, f := range tweak {
		f(cfg)
	}
	f := &fixture{db: database, ended: &grantsEndedRecorder{}}
	// Wired the way cmd/api wires it, so the agent side's sign-out reaches
	// these grants for real.
	grants := &oauthtest.DeferredGrants{}
	f.members = tenantMembers{user.NewPostgresStore(database)}
	f.agents = agent.NewService(agent.NewPostgresStore(database), noReleaser{}, grants, slog.DiscardHandler, agent.WithMembers(f.members), agent.WithSlotCredentials(grants))
	var events *audit.Recorder
	events, f.events = audittest.NewRecorder()
	f.recorder = events
	f.svc = oauth.NewService(oauth.NewPostgresStore(database), f.agents, cfg, events, slog.DiscardHandler, oauth.WithGrantsEnded(f.ended))
	f.admin = oauth.NewClientAdmin(oauth.NewPostgresStore(database), oauth.ResourceFor(cfg.Issuer), events, slog.DiscardHandler)
	grants.OAuth = f.svc
	f.user = f.newUser(t)
	f.ctx = authn.NewUserContext(context.Background(), f.user)
	return f
}

// newUser is a signed-in user in a tenant of their own.
func (f *fixture) newUser(t *testing.T) *authn.User {
	t.Helper()
	return &authn.User{ID: dbtest.NewUser(t, f.db), TenantID: dbtest.NewTenant(t, f.db)}
}

func pkce(t *testing.T) (verifier, challenge string) {
	t.Helper()
	b := make([]byte, 48)
	_, err := rand.Read(b)
	require.NoError(t, err)
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

const redirect = "http://localhost:33418/callback"

func (f *fixture) client(t *testing.T) *oauth.Client {
	t.Helper()
	c, err := f.svc.Register(context.Background(), netip.MustParseAddr("192.0.2.1"), &oauth.RegistrationRequest{
		ClientName: "Claude Code", RedirectURIs: []string{redirect},
	})
	require.NoError(t, err)
	return c
}

// authorize runs the browser half of the code flow: the request, then the
// user approving it as a new agent. It returns the code.
func (f *fixture) authorize(t *testing.T, c *oauth.Client, challenge string, d *oauth.Decision) string {
	t.Helper()
	consent, err := f.svc.Authorize(context.Background(), &oauth.AuthorizeParams{
		ResponseType: "code", ClientID: c.ID, RedirectURI: redirect,
		CodeChallenge: challenge, CodeChallengeMethod: "S256", State: "st-1", Resource: issuer + "/mcp",
	})
	require.NoError(t, err)
	u, err := url.Parse(consent)
	require.NoError(t, err)
	assert.Equal(t, issuer+"/authorize", u.Scheme+"://"+u.Host+u.Path)
	reqID := uuid.MustParse(u.Query().Get("request"))

	to, err := f.svc.DecideAuthRequest(f.ctx, f.user, reqID, d)
	require.NoError(t, err)
	back, err := url.Parse(to)
	require.NoError(t, err)
	assert.Equal(t, "st-1", back.Query().Get("state"), "the client's state comes back")
	assert.Equal(t, issuer, back.Query().Get("iss"))
	return back.Query().Get("code")
}

func newAgent(name string) *oauth.Decision {
	return &oauth.Decision{Approve: true, AgentName: name}
}

func (f *fixture) exchange(c *oauth.Client, code, verifier string) (*oauth.TokenResponse, error) {
	return f.svc.Token(context.Background(), &oauth.TokenRequest{
		GrantType: oauth.GrantAuthorizationCode, ClientID: c.ID, Code: code,
		RedirectURI: redirect, CodeVerifier: verifier, Resource: issuer + "/mcp",
	})
}

func (f *fixture) refresh(clientID, refreshToken string) (*oauth.TokenResponse, error) {
	return f.svc.Token(context.Background(), &oauth.TokenRequest{
		GrantType: oauth.GrantRefreshToken, ClientID: clientID, RefreshToken: refreshToken,
	})
}

func (f *fixture) works(t *testing.T, access string) bool {
	t.Helper()
	_, err := f.svc.Authenticate(context.Background(), access)
	if errors.Is(err, oauth.ErrInvalidToken) {
		return false
	}
	require.NoError(t, err)
	return true
}

func oauthCode(t *testing.T, err error) string {
	t.Helper()
	var oe *oauth.Error
	require.ErrorAs(t, err, &oe)
	return oe.Code
}

// The whole code flow, as Claude Code drives it: register, authorize,
// approve as a new agent, exchange with the verifier, use the token.
func TestTheCodeFlowCredentialsTheChosenAgent(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	verifier, challenge := pkce(t)

	code := f.authorize(t, c, challenge, newAgent("builder-1"))
	tokens, err := f.exchange(c, code, verifier)
	require.NoError(t, err)
	assert.Equal(t, "Bearer", tokens.TokenType)
	assert.NotEmpty(t, tokens.RefreshToken)

	access, err := f.svc.Authenticate(context.Background(), tokens.AccessToken)
	require.NoError(t, err)
	tenantID := access.TenantID
	agentID := access.AgentID
	assert.Equal(t, f.user.TenantID, tenantID)
	created, err := f.agents.Get(f.ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, "builder-1", created.Name, "the agent was made in the user's tenant")
}

func TestApprovingAsAnExistingAgent(t *testing.T) {
	f := newFixture(t)
	existing, err := f.agents.Create(f.ctx, "builder-1")
	require.NoError(t, err)
	c := f.client(t)
	verifier, challenge := pkce(t)

	code := f.authorize(t, c, challenge, &oauth.Decision{Approve: true, AgentID: &existing.ID})
	tokens, err := f.exchange(c, code, verifier)
	require.NoError(t, err)
	access, err := f.svc.Authenticate(context.Background(), tokens.AccessToken)
	require.NoError(t, err)
	agentID := access.AgentID
	assert.Equal(t, existing.ID, agentID)
}

// An agent id from another tenant is not the user's to hand out, however
// it was learned.
func TestApprovingAsAnotherTenantsAgentIsRefused(t *testing.T) {
	f := newFixture(t)
	other := f.newUser(t)
	theirs, err := f.agents.Create(authn.NewUserContext(context.Background(), other), "theirs")
	require.NoError(t, err)
	c := f.client(t)
	_, challenge := pkce(t)

	consent, err := f.svc.Authorize(context.Background(), &oauth.AuthorizeParams{
		ResponseType: "code", ClientID: c.ID, RedirectURI: redirect,
		CodeChallenge: challenge, CodeChallengeMethod: "S256",
	})
	require.NoError(t, err)
	u, _ := url.Parse(consent)
	_, err = f.svc.DecideAuthRequest(f.ctx, f.user, uuid.MustParse(u.Query().Get("request")),
		&oauth.Decision{Approve: true, AgentID: &theirs.ID})
	require.ErrorIs(t, err, oauth.ErrAgentUnavailable)
}

// PKCE is what makes a code useless to anyone who intercepts it: without
// the verifier the exchange fails, and the code is spent regardless.
func TestAWrongVerifierFailsAndSpendsTheCode(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	verifier, challenge := pkce(t)
	wrong, _ := pkce(t)
	code := f.authorize(t, c, challenge, newAgent("b"))

	_, err := f.exchange(c, code, wrong)
	assert.Equal(t, "invalid_grant", oauthCode(t, err))
	_, err = f.exchange(c, code, verifier)
	assert.Equal(t, "invalid_grant", oauthCode(t, err), "the code does not survive a failed attempt")
}

func TestTheCodeMustComeBackWithItsRedirect(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	verifier, challenge := pkce(t)
	code := f.authorize(t, c, challenge, newAgent("b"))

	_, err := f.svc.Token(context.Background(), &oauth.TokenRequest{
		GrantType: oauth.GrantAuthorizationCode, ClientID: c.ID, Code: code,
		RedirectURI: "http://localhost:9999/elsewhere", CodeVerifier: verifier,
	})
	assert.Equal(t, "invalid_grant", oauthCode(t, err))
}

// A code presented twice means someone else has it: the grant it made is
// revoked, taking the first exchange's tokens with it.
func TestAReplayedCodeRevokesItsGrant(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	verifier, challenge := pkce(t)
	code := f.authorize(t, c, challenge, newAgent("b"))
	require.Equal(t, 1, f.events.Count(audit.AuthorizationApproved))
	require.Zero(t, f.events.Count(audit.AuthorizationDenied))
	tokens, err := f.exchange(c, code, verifier)
	require.NoError(t, err)
	require.True(t, f.works(t, tokens.AccessToken))

	require.Zero(t, f.events.Count(audit.TokenReuse), "the first exchange is not a reuse")

	_, err = f.exchange(c, code, verifier)
	assert.Equal(t, "invalid_grant", oauthCode(t, err))
	assert.False(t, f.works(t, tokens.AccessToken), "the grant is gone")
	assert.Equal(t, 1, f.events.Count(audit.TokenReuse), "and the reuse is recorded")
}

func TestAnotherClientCannotExchangeTheCode(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	thief := f.client(t)
	verifier, challenge := pkce(t)
	code := f.authorize(t, c, challenge, newAgent("b"))

	_, err := f.exchange(thief, code, verifier)
	assert.Equal(t, "invalid_grant", oauthCode(t, err))
}

func TestAuthorizeRefusesUntrustedClientsWithoutRedirecting(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	_, challenge := pkce(t)
	for name, p := range map[string]*oauth.AuthorizeParams{
		"unknown client":         {ResponseType: "code", ClientID: "nope", RedirectURI: redirect, CodeChallenge: challenge, CodeChallengeMethod: "S256"},
		"unregistered redirect":  {ResponseType: "code", ClientID: c.ID, RedirectURI: "http://localhost:1/other", CodeChallenge: challenge, CodeChallengeMethod: "S256"},
		"redirect prefix trick":  {ResponseType: "code", ClientID: c.ID, RedirectURI: redirect + "/../x", CodeChallenge: challenge, CodeChallengeMethod: "S256"},
		"redirect with extra qs": {ResponseType: "code", ClientID: c.ID, RedirectURI: redirect + "?next=https://evil", CodeChallenge: challenge, CodeChallengeMethod: "S256"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.svc.Authorize(context.Background(), p)
			require.ErrorIs(t, err, oauth.ErrUntrustedRedirect)
		})
	}
}

func TestAuthorizeRequiresS256PKCEAndOurResource(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	verifier, challenge := pkce(t)
	base := func() *oauth.AuthorizeParams {
		return &oauth.AuthorizeParams{ResponseType: "code", ClientID: c.ID, RedirectURI: redirect,
			CodeChallenge: challenge, CodeChallengeMethod: "S256"}
	}
	cases := map[string]struct {
		mutate func(*oauth.AuthorizeParams)
		code   string
	}{
		"no challenge":     {func(p *oauth.AuthorizeParams) { p.CodeChallenge = "" }, "invalid_request"},
		"plain method":     {func(p *oauth.AuthorizeParams) { p.CodeChallengeMethod = "plain"; p.CodeChallenge = verifier }, "invalid_request"},
		"implicit flow":    {func(p *oauth.AuthorizeParams) { p.ResponseType = "token" }, "unsupported_response_type"},
		"another server's": {func(p *oauth.AuthorizeParams) { p.Resource = "https://evil.example/mcp" }, "invalid_target"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := base()
			tc.mutate(p)
			_, err := f.svc.Authorize(context.Background(), p)
			assert.Equal(t, tc.code, oauthCode(t, err))
		})
	}
}

// Registration is open, so what it accepts is the whole of the defence
// against a client pointed at someone else's server (see
// validRedirectURI).
func TestRegistrationAcceptsOnlyLoopbackRedirects(t *testing.T) {
	f := newFixture(t)
	for _, uri := range []string{
		"http://localhost:33418/callback", "http://127.0.0.1:5000/cb", "http://[::1]:8080/cb",
	} {
		_, err := f.svc.Register(context.Background(), netip.MustParseAddr("192.0.2.1"), &oauth.RegistrationRequest{RedirectURIs: []string{uri}})
		assert.NoError(t, err, uri)
	}
	for _, uri := range []string{
		"https://evil.example/cb", "http://evil.example/cb", "http://localhost.evil.example/cb",
		"http://127.0.0.1.nip.io/cb", "http://user@localhost/cb", "javascript:alert(1)", "http://localhost/cb#frag",
	} {
		_, err := f.svc.Register(context.Background(), netip.MustParseAddr("192.0.2.1"), &oauth.RegistrationRequest{RedirectURIs: []string{uri}})
		assert.Equal(t, "invalid_redirect_uri", oauthCode(t, err), uri)
	}
}

func TestRegistrationIsPublicClientsAndCodeGrantsOnly(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Register(context.Background(), netip.MustParseAddr("192.0.2.1"), &oauth.RegistrationRequest{
		RedirectURIs: []string{redirect}, TokenEndpointAuthMethod: "client_secret_basic"})
	assert.Equal(t, "invalid_client_metadata", oauthCode(t, err))
	_, err = f.svc.Register(context.Background(), netip.MustParseAddr("192.0.2.1"), &oauth.RegistrationRequest{
		RedirectURIs: []string{redirect}, GrantTypes: []string{oauth.GrantDeviceCode}})
	assert.Equal(t, "invalid_client_metadata", oauthCode(t, err), "the device grant is the workspace client's")
}

func TestRegistrationIsRateLimitedPerAddress(t *testing.T) {
	f := newFixture(t, func(c *oauth.Config) {
		c.RegisterLimit = &ratelimit.Config{Limit: 1, Window: time.Hour, MaxKeys: 10}
	})
	req := &oauth.RegistrationRequest{RedirectURIs: []string{redirect}}
	_, err := f.svc.Register(context.Background(), netip.MustParseAddr("192.0.2.1"), req)
	require.NoError(t, err)
	_, err = f.svc.Register(context.Background(), netip.MustParseAddr("192.0.2.1"), req)
	assert.Equal(t, "slow_down", oauthCode(t, err))
	_, err = f.svc.Register(context.Background(), netip.MustParseAddr("192.0.2.2"), req)
	assert.NoError(t, err)
}

// Registration is open to any address, so the rows a flood from many
// addresses leaves behind are capped too. Only never-authorized clients
// count: a real client is authorized minutes after registering.
func TestRegistrationStopsAtTheUnclaimedClientCap(t *testing.T) {
	f := newFixture(t, func(c *oauth.Config) { c.MaxUnclaimedClients = 1 })
	register := func(addr string) (*oauth.Client, error) {
		return f.svc.Register(context.Background(), netip.MustParseAddr(addr), &oauth.RegistrationRequest{RedirectURIs: []string{redirect}})
	}
	first, err := register("192.0.2.1")
	require.NoError(t, err)
	_, err = register("192.0.2.2")
	assert.Equal(t, "temporarily_unavailable", oauthCode(t, err), "a second unclaimed client, from another address")
	assert.Equal(t, 1, f.events.Count(audit.ClientRegistered))
	assert.Equal(t, 1, f.events.Count(audit.ClientRegistrationRefused), "the cap is recorded, for an alert")

	f.codeFlowTokens(t, first, "builder")
	_, err = register("192.0.2.3")
	assert.NoError(t, err, "an authorized client no longer counts")
}

// A flood arrives all at once: registrations racing each other must not
// all see room under the cap.
func TestConcurrentRegistrationsStopAtTheCap(t *testing.T) {
	f := newFixture(t, func(c *oauth.Config) { c.MaxUnclaimedClients = 3 })
	var wg sync.WaitGroup
	var ok atomic.Int32
	start := make(chan struct{})
	for i := range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := f.svc.Register(context.Background(), netip.AddrFrom4([4]byte{192, 0, byte(i / 250), byte(i%250 + 1)}),
				&oauth.RegistrationRequest{RedirectURIs: []string{redirect}})
			if err == nil {
				ok.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	assert.Equal(t, int32(3), ok.Load())
}

// The race above is too narrow to lose reliably without the lock, so this
// pins the mechanism itself: a registration waits while another holds the
// registration lock.
func TestRegistrationWaitsForTheRegistrationLock(t *testing.T) {
	f := newFixture(t)
	held, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unlock := func() { releaseOnce.Do(func() { close(release) }) }
	// Released on failure too, or the held connection would hang cleanup.
	t.Cleanup(unlock)
	go func() {
		_ = f.db.InGlobalTx(context.Background(), func(q db.Querier) error {
			_, err := q.Exec(context.Background(), `SELECT pg_advisory_xact_lock($1)`, oauth.UnclaimedClientsLock)
			close(held)
			<-release
			return err
		})
	}()
	<-held

	done := make(chan error, 1)
	go func() {
		_, err := f.svc.Register(context.Background(), netip.MustParseAddr("192.0.2.1"), &oauth.RegistrationRequest{RedirectURIs: []string{redirect}})
		done <- err
	}()
	select {
	case <-done:
		t.Fatal("registered while another registration held the lock")
	case <-time.After(300 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("registration never went ahead once the lock was released")
	}
}

func (f *fixture) codeFlowTokens(t *testing.T, c *oauth.Client, agentName string) *oauth.TokenResponse {
	t.Helper()
	verifier, challenge := pkce(t)
	tokens, err := f.exchange(c, f.authorize(t, c, challenge, newAgent(agentName)), verifier)
	require.NoError(t, err)
	return tokens
}

func TestRefreshTokensRotate(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	first := f.codeFlowTokens(t, c, "b")

	second, err := f.refresh(c.ID, first.RefreshToken)
	require.NoError(t, err)
	assert.NotEqual(t, first.RefreshToken, second.RefreshToken)
	assert.True(t, f.works(t, second.AccessToken))

	third, err := f.refresh(c.ID, second.RefreshToken)
	require.NoError(t, err, "the new one works in turn")
	assert.True(t, f.works(t, third.AccessToken))
}

// A rotated-out refresh token presented again is a copy in someone else's
// hands; the grant ends for both holders.
func TestAReusedRefreshTokenRevokesTheGrant(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	first := f.codeFlowTokens(t, c, "b")
	second, err := f.refresh(c.ID, first.RefreshToken)
	require.NoError(t, err)

	require.Zero(t, f.events.Count(audit.TokenReuse), "a rotation is not a reuse")

	f.ageRotation(t, first.RefreshToken, time.Hour)
	_, err = f.refresh(c.ID, first.RefreshToken)
	assert.Equal(t, "invalid_grant", oauthCode(t, err))
	assert.False(t, f.works(t, second.AccessToken))
	assert.Equal(t, 1, f.events.Count(audit.TokenReuse), "the reuse is recorded")
	_, err = f.refresh(c.ID, second.RefreshToken)
	assert.Equal(t, "invalid_grant", oauthCode(t, err), "the legitimate holder must authorize again")
	assert.Equal(t, 1, f.events.Count(audit.TokenReuse), "a token of a revoked grant is dead, not reused")
}

func TestARefreshTokenIsBoundToItsClient(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	other := f.client(t)
	tokens := f.codeFlowTokens(t, c, "b")

	_, err := f.refresh(other.ID, tokens.RefreshToken)
	assert.Equal(t, "invalid_grant", oauthCode(t, err))
	_, err = f.refresh(c.ID, tokens.RefreshToken)
	assert.NoError(t, err, "and the refusal did not spend it")
}

// The grace window answers the client the token was issued to, and no other.
func TestAReplayedRefreshTokenIsBoundToItsClient(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	other := f.client(t)
	first := f.codeFlowTokens(t, c, "b")
	second, err := f.refresh(c.ID, first.RefreshToken)
	require.NoError(t, err)

	_, err = f.refresh(other.ID, first.RefreshToken)
	assert.Equal(t, "invalid_grant", oauthCode(t, err))
	again, err := f.refresh(c.ID, first.RefreshToken)
	require.NoError(t, err, "the refusal neither revoked the grant nor spent the replay")
	assert.Equal(t, second, again)
}

// Disabling an agent ends its sessions at the next refresh, but not its
// grant: switched back on, the same workspace carries on.
func TestADisabledAgentCannotRefreshUntilReEnabled(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	tokens := f.codeFlowTokens(t, c, "b")
	access, err := f.svc.Authenticate(context.Background(), tokens.AccessToken)
	require.NoError(t, err)
	agentID := access.AgentID

	_, err = f.agents.SetDisabled(f.ctx, agentID, true)
	require.NoError(t, err)
	_, err = f.refresh(c.ID, tokens.RefreshToken)
	assert.Equal(t, "invalid_grant", oauthCode(t, err))

	_, err = f.agents.SetDisabled(f.ctx, agentID, false)
	require.NoError(t, err)
	_, err = f.refresh(c.ID, tokens.RefreshToken)
	assert.NoError(t, err)
}

func TestRevokingAGrantEndsItsTokens(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	tokens := f.codeFlowTokens(t, c, "b")

	grants, err := f.svc.Grants(f.ctx, f.user)
	require.NoError(t, err)
	require.Len(t, grants, 1)
	assert.Equal(t, "Claude Code", grants[0].ClientName)

	outsider := f.newUser(t)
	require.ErrorIs(t, f.svc.RevokeGrant(context.Background(), outsider, grants[0].ID), oauth.ErrNotFound,
		"another tenant cannot revoke it")
	require.True(t, f.works(t, tokens.AccessToken))
	assert.Zero(t, f.events.Count(audit.GrantRevoked), "a refused revoke is not recorded as one")

	require.NoError(t, f.svc.RevokeGrant(f.ctx, f.user, grants[0].ID))
	assert.False(t, f.works(t, tokens.AccessToken))
	assert.Equal(t, 1, f.events.Count(audit.GrantRevoked))
	_, err = f.refresh(c.ID, tokens.RefreshToken)
	assert.Equal(t, "invalid_grant", oauthCode(t, err))
	grants, err = f.svc.Grants(f.ctx, f.user)
	require.NoError(t, err)
	assert.Empty(t, grants)
}

// Handing out a new setup script for an agent signs out every workspace
// acting as it -- and nothing else: another agent's workspace, and the
// agent's grants as seen from another tenant, are untouched.
func TestSigningOutAnAgentsWorkspacesEndsEveryGrantOfThatAgentOnly(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	first := f.codeFlowTokens(t, c, "a")
	access, err := f.svc.Authenticate(context.Background(), first.AccessToken)
	require.NoError(t, err)
	agentA := access.AgentID
	verifier, challenge := pkce(t)
	second, err := f.exchange(c, f.authorize(t, c, challenge, &oauth.Decision{Approve: true, AgentID: &agentA}), verifier)
	require.NoError(t, err)
	bystander := f.codeFlowTokens(t, c, "b")

	outsider := f.newUser(t)
	_, err = f.agents.SignOutWorkspaces(authn.NewUserContext(context.Background(), outsider), agentA)
	require.ErrorIs(t, err, agent.ErrNotFound, "another tenant cannot see the agent to sign it out")
	require.NoError(t, f.svc.RevokeAgentGrants(context.Background(), outsider.TenantID, agentA))
	require.True(t, f.works(t, first.AccessToken), "a revoke naming the wrong tenant touches nothing")

	_, err = f.agents.SignOutWorkspaces(f.ctx, agentA)
	require.NoError(t, err)

	assert.False(t, f.works(t, first.AccessToken))
	assert.False(t, f.works(t, second.AccessToken))
	_, err = f.refresh(c.ID, second.RefreshToken)
	assert.Equal(t, "invalid_grant", oauthCode(t, err))
	assert.True(t, f.works(t, bystander.AccessToken))
	grants, err := f.svc.Grants(f.ctx, f.user)
	require.NoError(t, err)
	require.Len(t, grants, 1)
	assert.NotEqual(t, agentA, grants[0].AgentID)
}

// A disabled agent is refused rather than signed out: the new script's
// sign-in would be refused too, so going ahead would only look like it
// had worked.
func TestSigningOutADisabledAgentIsRefused(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	tokens := f.codeFlowTokens(t, c, "a")
	access, err := f.svc.Authenticate(context.Background(), tokens.AccessToken)
	require.NoError(t, err)
	agentID := access.AgentID
	_, err = f.agents.SetDisabled(f.ctx, agentID, true)
	require.NoError(t, err)

	_, err = f.agents.SignOutWorkspaces(f.ctx, agentID)
	require.ErrorIs(t, err, agent.ErrIsDisabled)

	grants, err := f.svc.Grants(f.ctx, f.user)
	require.NoError(t, err)
	assert.Len(t, grants, 1, "nothing was revoked")
}

func TestAnExpiredAccessTokenIsRefused(t *testing.T) {
	f := newFixture(t, func(c *oauth.Config) { c.AccessTTL = -time.Second })
	c := f.client(t)
	tokens := f.codeFlowTokens(t, c, "b")
	assert.False(t, f.works(t, tokens.AccessToken))
	_, err := f.refresh(c.ID, tokens.RefreshToken)
	assert.NoError(t, err, "the refresh token is what outlives it")
}

// A code presented after CodeTTL has passed is treated the same as a
// stolen one: refused, without revoking the grant it belongs to (nothing
// was ever handed out for it to steal).
func TestAnExpiredCodeIsRefused(t *testing.T) {
	f := newFixture(t, func(c *oauth.Config) { c.CodeTTL = -time.Second })
	c := f.client(t)
	verifier, challenge := pkce(t)
	code := f.authorize(t, c, challenge, newAgent("b"))

	_, err := f.exchange(c, code, verifier)
	assert.Equal(t, "invalid_grant", oauthCode(t, err))
}

// Expiries are stamped from the injected clock: a code issued while the
// clock is two hours behind is already expired by the database's own time,
// and one issued after the clock has caught up is not -- no sleeping.
func TestCodeExpiryFollowsTheInjectedClock(t *testing.T) {
	fake := clock.NewFake(time.Now().Add(-2 * time.Hour))
	f := newFixture(t)
	cfg := testConfig()
	// Outlives the two hours the clock starts behind, so only the code expires.
	cfg.AuthRequestTTL = 24 * time.Hour
	f.svc = oauth.NewService(oauth.NewPostgresStore(f.db), f.agents, cfg, nil, slog.DiscardHandler, oauth.WithClock(fake))
	c := f.client(t)

	verifier, challenge := pkce(t)
	stale := f.authorize(t, c, challenge, newAgent("b"))
	_, err := f.exchange(c, stale, verifier)
	assert.Equal(t, "invalid_grant", oauthCode(t, err))

	fake.Advance(2 * time.Hour)
	verifier, challenge = pkce(t)
	fresh := f.authorize(t, c, challenge, newAgent("c"))
	_, err = f.exchange(c, fresh, verifier)
	assert.NoError(t, err)
}

// A consent request past AuthRequestTTL is gone, whether the caller is
// showing the consent page or recording the decision on it.
func TestAnExpiredAuthRequestIsNotFound(t *testing.T) {
	f := newFixture(t, func(c *oauth.Config) { c.AuthRequestTTL = -time.Second })
	c := f.client(t)
	_, challenge := pkce(t)
	consent, err := f.svc.Authorize(context.Background(), &oauth.AuthorizeParams{
		ResponseType: "code", ClientID: c.ID, RedirectURI: redirect,
		CodeChallenge: challenge, CodeChallengeMethod: "S256",
	})
	require.NoError(t, err)
	u, err := url.Parse(consent)
	require.NoError(t, err)
	id := uuid.MustParse(u.Query().Get("request"))

	_, err = f.svc.PendingAuthRequest(context.Background(), id)
	assert.ErrorIs(t, err, oauth.ErrNotFound)

	_, err = f.svc.DecideAuthRequest(f.ctx, f.user, id, newAgent("b"))
	assert.ErrorIs(t, err, oauth.ErrNotFound)
}

// A refresh token past RefreshTTL is refused like any other invalid one --
// the client's only way back in is to authorize again.
func TestAnExpiredRefreshTokenIsRefused(t *testing.T) {
	f := newFixture(t, func(c *oauth.Config) { c.RefreshTTL = -time.Second })
	c := f.client(t)
	tokens := f.codeFlowTokens(t, c, "b")

	_, err := f.refresh(c.ID, tokens.RefreshToken)
	assert.Equal(t, "invalid_grant", oauthCode(t, err))
}

func TestARefusedConsentReturnsAccessDeniedOnce(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	_, challenge := pkce(t)
	consent, err := f.svc.Authorize(context.Background(), &oauth.AuthorizeParams{
		ResponseType: "code", ClientID: c.ID, RedirectURI: redirect, State: "s",
		CodeChallenge: challenge, CodeChallengeMethod: "S256",
	})
	require.NoError(t, err)
	u, _ := url.Parse(consent)
	id := uuid.MustParse(u.Query().Get("request"))

	to, err := f.svc.DecideAuthRequest(f.ctx, f.user, id, &oauth.Decision{Approve: false})
	require.NoError(t, err)
	assert.Equal(t, 1, f.events.Count(audit.AuthorizationDenied))
	assert.Zero(t, f.events.Count(audit.AuthorizationApproved))
	back, _ := url.Parse(to)
	assert.Equal(t, "access_denied", back.Query().Get("error"))
	assert.Equal(t, "s", back.Query().Get("state"))
	assert.Empty(t, back.Query().Get("code"))

	_, err = f.svc.DecideAuthRequest(f.ctx, f.user, id, newAgent("b"))
	require.ErrorIs(t, err, oauth.ErrNotFound, "a decided request cannot be decided again")
}

// -- Device flow --

func (f *fixture) startDevice(t *testing.T) *oauth.DeviceAuthorization {
	t.Helper()
	d, err := f.svc.StartDevice(context.Background(), netip.MustParseAddr("192.0.2.1"), oauth.WorkspaceClientID, "")
	require.NoError(t, err)
	assert.Equal(t, issuer+"/device", d.VerificationURI)
	return d
}

func (f *fixture) poll(d *oauth.DeviceAuthorization) (*oauth.TokenResponse, error) {
	return f.svc.Token(context.Background(), &oauth.TokenRequest{
		GrantType: oauth.GrantDeviceCode, ClientID: oauth.WorkspaceClientID, DeviceCode: d.DeviceCode,
	})
}

func TestTheDeviceFlowCredentialsTheChosenAgent(t *testing.T) {
	f := newFixture(t)
	d := f.startDevice(t)

	_, err := f.poll(d)
	assert.Equal(t, "authorization_pending", oauthCode(t, err))

	// As typed by a person: lower case, spaced, no dash.
	typed := strings.ToLower(strings.Replace(d.UserCode, "-", " ", 1))
	pending, err := f.svc.LookupDevice(f.ctx, f.user, typed)
	require.NoError(t, err)
	assert.Equal(t, "slop-haus workspace", pending.ClientName)
	require.NoError(t, f.svc.DecideDevice(f.ctx, f.user, typed, newAgent("hooks-1")))
	assert.Equal(t, 1, f.events.Count(audit.DeviceStarted))
	assert.Equal(t, 1, f.events.Count(audit.DeviceApproved))
	assert.Zero(t, f.events.Count(audit.DeviceDenied))

	tokens, err := f.poll(d)
	require.NoError(t, err)
	access, err := f.svc.Authenticate(context.Background(), tokens.AccessToken)
	require.NoError(t, err)
	tenantID := access.TenantID
	agentID := access.AgentID
	assert.Equal(t, f.user.TenantID, tenantID)
	created, err := f.agents.Get(f.ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, "hooks-1", created.Name, "the token authenticates as the agent chosen on the device page")

	_, err = f.poll(d)
	assert.Equal(t, "invalid_grant", oauthCode(t, err), "a device code yields tokens once")
}

func TestADeniedDeviceCodeSaysSo(t *testing.T) {
	f := newFixture(t)
	d := f.startDevice(t)
	require.NoError(t, f.svc.DecideDevice(f.ctx, f.user, d.UserCode, &oauth.Decision{Approve: false}))
	_, err := f.poll(d)
	assert.Equal(t, "access_denied", oauthCode(t, err))
	assert.Equal(t, 1, f.events.Count(audit.DeviceDenied))
	assert.Zero(t, f.events.Count(audit.DeviceApproved))
}

func TestAnExpiredDeviceCodeSaysSo(t *testing.T) {
	f := newFixture(t, func(c *oauth.Config) { c.DeviceTTL = -time.Second })
	d := f.startDevice(t)
	_, err := f.poll(d)
	assert.Equal(t, "expired_token", oauthCode(t, err))
	_, err = f.svc.LookupDevice(f.ctx, f.user, d.UserCode)
	require.ErrorIs(t, err, oauth.ErrNotFound)
}

func TestPollingTooFastIsToldToSlowDown(t *testing.T) {
	f := newFixture(t, func(c *oauth.Config) { c.PollInterval = time.Hour })
	d := f.startDevice(t)
	_, err := f.poll(d)
	assert.Equal(t, "authorization_pending", oauthCode(t, err))
	_, err = f.poll(d)
	assert.Equal(t, "slow_down", oauthCode(t, err))
}

// The device grant belongs to the workspace client; a client registered
// for the code flow cannot use it.
func TestOnlyTheWorkspaceClientUsesTheDeviceGrant(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	_, err := f.svc.StartDevice(context.Background(), netip.MustParseAddr("192.0.2.1"), c.ID, "")
	assert.Equal(t, "unauthorized_client", oauthCode(t, err))
	_, err = f.svc.StartDevice(context.Background(), netip.MustParseAddr("192.0.2.1"), "nope", "")
	assert.Equal(t, "invalid_client", oauthCode(t, err))
}

func TestUserCodeGuessingIsRateLimited(t *testing.T) {
	f := newFixture(t, func(c *oauth.Config) {
		c.DeviceLookupLimit = &ratelimit.Config{Limit: 2, Window: time.Hour, MaxKeys: 10}
	})
	for range 2 {
		_, err := f.svc.LookupDevice(f.ctx, f.user, "BCDF-GHJK")
		require.ErrorIs(t, err, oauth.ErrNotFound)
	}
	_, err := f.svc.LookupDevice(f.ctx, f.user, "BCDF-GHJK")
	require.ErrorIs(t, err, oauth.ErrTooManyLookups)
}

func TestNormalizeUserCode(t *testing.T) {
	for in, want := range map[string]string{
		"bcdf-ghjk": "BCDF-GHJK", "BCDFGHJK": "BCDF-GHJK", " bcdf ghjk ": "BCDF-GHJK", "short": "SHORT",
	} {
		assert.Equal(t, want, oauth.NormalizeUserCode(in), in)
	}
}

// race runs fn from n goroutines released together and counts successes.
func race(n int, fn func() error) int {
	var wg sync.WaitGroup
	var ok atomic.Int32
	start := make(chan struct{})
	for range n {
		wg.Go(func() {
			<-start
			if fn() == nil {
				ok.Add(1)
			}
		})
	}
	close(start)
	wg.Wait()
	return int(ok.Load())
}

// Polls arriving together after approval must not each get tokens: the
// device code is spent by exactly one of them.
func TestConcurrentPollsGetTokensOnce(t *testing.T) {
	f := newFixture(t)
	d := f.startDevice(t)
	require.NoError(t, f.svc.DecideDevice(f.ctx, f.user, d.UserCode, newAgent("hooks-1")))

	got := race(8, func() error { _, err := f.poll(d); return err })
	assert.Equal(t, 1, got)
}

// A consent form submitted twice at once -- a double click -- must not mint
// two grants.
func TestConcurrentApprovalsMakeOneGrant(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	_, challenge := pkce(t)
	consent, err := f.svc.Authorize(context.Background(), &oauth.AuthorizeParams{
		ResponseType: "code", ClientID: c.ID, RedirectURI: redirect,
		CodeChallenge: challenge, CodeChallengeMethod: "S256",
	})
	require.NoError(t, err)
	u, _ := url.Parse(consent)
	id := uuid.MustParse(u.Query().Get("request"))
	existing, err := f.agents.Create(f.ctx, "b")
	require.NoError(t, err)

	got := race(8, func() error {
		_, err := f.svc.DecideAuthRequest(f.ctx, f.user, id, &oauth.Decision{Approve: true, AgentID: &existing.ID})
		return err
	})
	assert.Equal(t, 1, got)
	grants, err := f.svc.Grants(f.ctx, f.user)
	require.NoError(t, err)
	assert.Len(t, grants, 1)
}

// Refreshes arriving together with one token rotate it once; the others
// arrive after the winner committed, inside the reuse grace window, and are
// answered with the winner's pair. Without the window they looked exactly like
// a stolen copy and revoked the grant.
func TestConcurrentRefreshesRotateOnce(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	tokens := f.codeFlowTokens(t, c, "b")
	var mu sync.Mutex
	var got []*oauth.TokenResponse
	ok := race(8, func() error {
		resp, err := f.refresh(c.ID, tokens.RefreshToken)
		if err == nil {
			mu.Lock()
			got = append(got, resp)
			mu.Unlock()
		}
		return err
	})
	assert.Equal(t, 8, ok)
	for _, r := range got {
		assert.Equal(t, got[0], r, "every racer got the one pair that was issued")
	}
	assert.True(t, f.works(t, got[0].AccessToken))
	assert.Zero(t, f.events.Count(audit.TokenReuse))
}

// meetingStore holds each GrantForRefresh until n of them have read the
// refresh token, so all n are past the live check before any spends it: the
// interleaving that makes the losers' spend fail, not a lookup that is
// already answered from the winner's commit.
type meetingStore struct {
	oauth.Store
	n       int
	mu      sync.Mutex
	arrived int
	all     chan struct{}
}

func (m *meetingStore) GrantForRefresh(ctx context.Context, refreshHash string) (*oauth.Grant, error) {
	g, err := m.Store.GrantForRefresh(ctx, refreshHash)
	m.mu.Lock()
	m.arrived++
	if m.arrived == m.n {
		close(m.all)
	}
	m.mu.Unlock()
	<-m.all
	return g, err
}

// The losers of a race all read the token live, then fail their spend once
// the winner has committed. That is the winner's pair still wanted, not a
// copy, so they must be replayed it rather than revoke the grant.
func TestConcurrentRefreshesThatAllReadTheTokenLiveRotateOnce(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	tokens := f.codeFlowTokens(t, c, "b")
	const n = 6
	store := &meetingStore{Store: oauth.NewPostgresStore(f.db), n: n, all: make(chan struct{})}
	f.svc = oauth.NewService(store, f.agents, testConfig(), nil, slog.DiscardHandler)
	var mu sync.Mutex
	var got []*oauth.TokenResponse
	ok := race(n, func() error {
		resp, err := f.refresh(c.ID, tokens.RefreshToken)
		if err == nil {
			mu.Lock()
			got = append(got, resp)
			mu.Unlock()
		}
		return err
	})
	assert.Equal(t, n, ok)
	for _, r := range got {
		assert.Equal(t, got[0], r)
	}
	assert.True(t, f.works(t, got[0].AccessToken), "the grant survived")
}

// With the grace turned off a rotated-out token is a copy at once.
func TestWithoutAGraceWindowAReusedRefreshTokenRevokesAtOnce(t *testing.T) {
	f := newFixture(t)
	f.svc = oauth.NewService(oauth.NewPostgresStore(f.db), f.agents, testConfig(), f.recorder, slog.DiscardHandler, oauth.WithRefreshReuseGrace(0))
	c := f.client(t)
	first := f.codeFlowTokens(t, c, "b")
	second, err := f.refresh(c.ID, first.RefreshToken)
	require.NoError(t, err)

	_, err = f.refresh(c.ID, first.RefreshToken)
	assert.Equal(t, "invalid_grant", oauthCode(t, err))
	assert.False(t, f.works(t, second.AccessToken))
	assert.Equal(t, 1, f.events.Count(audit.TokenReuse))
}

// ageRotation makes a refresh token's rotation d older, standing in for
// d having passed without a test that sleeps.
func (f *fixture) ageRotation(t *testing.T, refreshToken string, d time.Duration) {
	t.Helper()
	_, err := f.db.Global().Exec(context.Background(),
		`UPDATE global_oauth_tokens SET used_at = used_at - make_interval(secs => $2) WHERE token_hash = $1`,
		auth.HashOpaqueToken(refreshToken), d.Seconds())
	require.NoError(t, err)
}

// A client whose refresh response was lost retries with the token it sent.
// Inside the grace window it is handed the pair already issued, not a second
// one and not a revocation.
func TestARefreshTokenRepresentedInsideTheGraceWindowGetsTheSamePair(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	first := f.codeFlowTokens(t, c, "b")
	second, err := f.refresh(c.ID, first.RefreshToken)
	require.NoError(t, err)

	again, err := f.refresh(c.ID, first.RefreshToken)
	require.NoError(t, err)
	assert.Equal(t, second, again, "the same pair, byte for byte")
	var blob []byte
	require.NoError(t, f.db.Global().QueryRow(context.Background(),
		`SELECT replay_blob FROM global_oauth_tokens WHERE token_hash = $1`, auth.HashOpaqueToken(first.RefreshToken)).Scan(&blob))
	assert.NotContains(t, string(blob), second.AccessToken, "the database holds the pair sealed, not in the clear")
	assert.NotContains(t, string(blob), second.RefreshToken)
	assert.True(t, f.works(t, again.AccessToken))
	assert.Zero(t, f.events.Count(audit.TokenReuse))

	third, err := f.refresh(c.ID, again.RefreshToken)
	require.NoError(t, err, "the replayed refresh token is the live one")
	assert.True(t, f.works(t, third.AccessToken))
}

// The window is for the one retry of the last rotation. A token older than
// that, or one whose replacement has been spent, is a copy.
func TestARefreshTokenPastTheGraceWindowOrSupersededIsStillReuse(t *testing.T) {
	t.Run("past the window", func(t *testing.T) {
		f := newFixture(t)
		c := f.client(t)
		first := f.codeFlowTokens(t, c, "b")
		second, err := f.refresh(c.ID, first.RefreshToken)
		require.NoError(t, err)
		f.ageRotation(t, first.RefreshToken, oauth.DefaultRefreshReuseGrace+time.Second)

		_, err = f.refresh(c.ID, first.RefreshToken)
		assert.Equal(t, "invalid_grant", oauthCode(t, err))
		assert.False(t, f.works(t, second.AccessToken), "the grant is revoked")
		assert.Equal(t, 1, f.events.Count(audit.TokenReuse))
	})
	t.Run("an older token while its replacement was spent", func(t *testing.T) {
		f := newFixture(t)
		c := f.client(t)
		first := f.codeFlowTokens(t, c, "b")
		second, err := f.refresh(c.ID, first.RefreshToken)
		require.NoError(t, err)
		third, err := f.refresh(c.ID, second.RefreshToken)
		require.NoError(t, err)

		_, err = f.refresh(c.ID, first.RefreshToken)
		assert.Equal(t, "invalid_grant", oauthCode(t, err), "first's replacement is gone and second's was just spent")
		assert.False(t, f.works(t, third.AccessToken))
		assert.Equal(t, 1, f.events.Count(audit.TokenReuse))
	})
	t.Run("the token just spent whose pair has moved on", func(t *testing.T) {
		f := newFixture(t)
		c := f.client(t)
		first := f.codeFlowTokens(t, c, "b")
		second, err := f.refresh(c.ID, first.RefreshToken)
		require.NoError(t, err)
		_, err = f.refresh(c.ID, second.RefreshToken)
		require.NoError(t, err)

		_, err = f.refresh(c.ID, first.RefreshToken)
		assert.Equal(t, "invalid_grant", oauthCode(t, err), "replaying first would hand out second, already spent")
		assert.Equal(t, 1, f.events.Count(audit.TokenReuse))
	})
}

// What the window may replay is the issued pair, to whoever holds the old
// token, and nothing else: an unknown or mangled token is no better off.
func TestTheGraceWindowOnlyAnswersTheTokenItWasIssuedFor(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	first := f.codeFlowTokens(t, c, "b")
	second, err := f.refresh(c.ID, first.RefreshToken)
	require.NoError(t, err)

	_, err = f.refresh(c.ID, first.RefreshToken+"x")
	assert.Equal(t, "invalid_grant", oauthCode(t, err))
	assert.True(t, f.works(t, second.AccessToken), "a guess at the old token does not touch the grant")
}

// Tokens are only ever for this server's MCP endpoint (RFC 8707): a client
// asking for another resource gets nothing, at either grant.
func TestTokensAreOnlyForThisServer(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	verifier, challenge := pkce(t)
	code := f.authorize(t, c, challenge, newAgent("b"))

	_, err := f.svc.Token(context.Background(), &oauth.TokenRequest{
		GrantType: oauth.GrantAuthorizationCode, ClientID: c.ID, Code: code,
		RedirectURI: redirect, CodeVerifier: verifier, Resource: "https://evil.example/mcp",
	})
	assert.Equal(t, "invalid_target", oauthCode(t, err))
	_, err = f.exchange(c, code, verifier)
	assert.NoError(t, err, "refused before the code was spent")

	_, err = f.svc.StartDevice(context.Background(), netip.MustParseAddr("192.0.2.1"), oauth.WorkspaceClientID, "https://evil.example/mcp")
	assert.Equal(t, "invalid_target", oauthCode(t, err))
}

// Starting a device sign-in is open and stores a row, so one address can
// only start so many.
func TestDeviceStartsAreRateLimitedPerAddress(t *testing.T) {
	f := newFixture(t, func(c *oauth.Config) {
		c.DeviceStartLimit = &ratelimit.Config{Limit: 2, Window: time.Hour, MaxKeys: 10}
	})
	for range 2 {
		_, err := f.svc.StartDevice(context.Background(), netip.MustParseAddr("192.0.2.9"), oauth.WorkspaceClientID, "")
		require.NoError(t, err)
	}
	_, err := f.svc.StartDevice(context.Background(), netip.MustParseAddr("192.0.2.9"), oauth.WorkspaceClientID, "")
	assert.Equal(t, "slow_down", oauthCode(t, err))
	_, err = f.svc.StartDevice(context.Background(), netip.MustParseAddr("192.0.2.10"), oauth.WorkspaceClientID, "")
	assert.NoError(t, err, "another address has its own allowance")
}

// An IPv6 client picks its address from its /64, so the /64 is the client.
func TestOpenEndpointsCountAnIPv6ClientByItsSlash64(t *testing.T) {
	f := newFixture(t, func(c *oauth.Config) {
		c.DeviceStartLimit = &ratelimit.Config{Limit: 1, Window: time.Hour, MaxKeys: 10}
		c.RegisterLimit = &ratelimit.Config{Limit: 1, Window: time.Hour, MaxKeys: 10}
	})
	start := func(addr string) error {
		_, err := f.svc.StartDevice(context.Background(), netip.MustParseAddr(addr), oauth.WorkspaceClientID, "")
		return err
	}
	require.NoError(t, start("2001:db8:1:2::1"))
	assert.Equal(t, "slow_down", oauthCode(t, start("2001:db8:1:2::2")), "the same /64")
	assert.NoError(t, start("2001:db8:1:3::1"), "another /64")

	register := func(addr string) error {
		_, err := f.svc.Register(context.Background(), netip.MustParseAddr(addr), &oauth.RegistrationRequest{
			RedirectURIs: []string{"http://127.0.0.1:3000/callback"}})
		return err
	}
	require.NoError(t, register("2001:db8:1:2::1"))
	assert.Equal(t, "slow_down", oauthCode(t, register("2001:db8:1:2::2")), "the same /64")
}

// The refresh token presented and the tokens replacing it commit together.
// If issuing fails, the refresh token must still be unspent -- so the
// client's retry succeeds, rather than reading as a replay and revoking
// the grant. The failure is forced with a hash that is already taken.
func TestAFailedIssueLeavesTheRefreshTokenUnspent(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	tokens := f.codeFlowTokens(t, c, "b")
	store := oauth.NewPostgresStore(f.db)
	refreshHash := auth.HashOpaqueToken(tokens.RefreshToken)
	later := time.Now().Add(time.Hour)

	_, err := store.SpendRefresh(context.Background(), refreshHash, &oauth.TokenPair{
		Access:  &oauth.IssuedToken{Hash: auth.HashOpaqueToken(tokens.AccessToken), ExpiresAt: later},
		Refresh: &oauth.IssuedToken{Hash: "fresh-refresh", ExpiresAt: later},
	})
	require.Error(t, err, "the colliding hash makes issuing fail")

	_, err = f.refresh(c.ID, tokens.RefreshToken)
	require.NoError(t, err, "the refresh token survived the failed attempt")
	assert.True(t, f.works(t, tokens.AccessToken), "and the grant was not revoked")
	assert.Zero(t, f.events.Count(audit.TokenReuse))
}

// Denying uses a code as much as approving does, so it counts against the
// same per-user guessing limit.
func TestDenyingCodesIsRateLimited(t *testing.T) {
	f := newFixture(t, func(c *oauth.Config) {
		c.DeviceLookupLimit = &ratelimit.Config{Limit: 2, Window: time.Hour, MaxKeys: 10}
	})
	for range 2 {
		err := f.svc.DecideDevice(f.ctx, f.user, "BCDF-GHJK", &oauth.Decision{Approve: false})
		require.ErrorIs(t, err, oauth.ErrNotFound)
	}
	err := f.svc.DecideDevice(f.ctx, f.user, "BCDF-GHJK", &oauth.Decision{Approve: false})
	require.ErrorIs(t, err, oauth.ErrTooManyLookups)
}

// Whatever an agent had pending for a human is dropped when nothing can act
// as it any more: told once its last full-access grant is gone, not while
// another workspace still holds one.
func TestTheLastGrantOfAnAgentGoingTellsGrantsEnded(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	first := f.codeFlowTokens(t, c, "a")
	access, err := f.svc.Authenticate(context.Background(), first.AccessToken)
	require.NoError(t, err)
	agentA := access.AgentID
	verifier, challenge := pkce(t)
	_, err = f.exchange(c, f.authorize(t, c, challenge, &oauth.Decision{Approve: true, AgentID: &agentA}), verifier)
	require.NoError(t, err)
	f.codeFlowTokens(t, c, "b")

	grants, err := f.svc.Grants(f.ctx, f.user)
	require.NoError(t, err)
	var ofA []oauth.Grant
	for _, g := range grants {
		if g.AgentID == agentA {
			ofA = append(ofA, g)
		}
	}
	require.Len(t, ofA, 2)

	require.NoError(t, f.svc.RevokeGrant(f.ctx, f.user, ofA[0].ID))
	assert.Empty(t, f.ended.calls(), "another workspace still acts as the agent")

	require.NoError(t, f.svc.RevokeGrant(f.ctx, f.user, ofA[1].ID))
	assert.Equal(t, []uuid.UUID{agentA}, f.ended.calls())
}

func TestSigningOutAnAgentTellsGrantsEnded(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	tokens := f.codeFlowTokens(t, c, "a")
	access, err := f.svc.Authenticate(context.Background(), tokens.AccessToken)
	require.NoError(t, err)

	_, err = f.agents.SignOutWorkspaces(f.ctx, access.AgentID)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{access.AgentID}, f.ended.calls())
}

func TestHasFullAccessGrantFollowsTheAgentsLiveGrants(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	tokens := f.codeFlowTokens(t, c, "a")
	access, err := f.svc.Authenticate(context.Background(), tokens.AccessToken)
	require.NoError(t, err)

	has, err := f.svc.HasFullAccessGrant(f.ctx, f.user.TenantID, access.AgentID)
	require.NoError(t, err)
	assert.True(t, has)
	has, err = f.svc.HasFullAccessGrant(f.ctx, f.user.TenantID, uuid.New())
	require.NoError(t, err)
	assert.False(t, has, "another agent has none")
	has, err = f.svc.HasFullAccessGrant(f.ctx, uuid.New(), access.AgentID)
	require.NoError(t, err)
	assert.False(t, has, "another tenant does not see it")

	require.NoError(t, f.svc.RevokeAgentGrants(f.ctx, f.user.TenantID, access.AgentID))
	has, err = f.svc.HasFullAccessGrant(f.ctx, f.user.TenantID, access.AgentID)
	require.NoError(t, err)
	assert.False(t, has)
}
