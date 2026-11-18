package user_test

import (
	"context"
	"log/slog"
	"net/url"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/auth"
	gologin "github.com/lowaa/slop-factory/go-auth/login"
	"github.com/lowaa/slop-factory/go-auth/ratelimit"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit/audittest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

func TestMain(m *testing.M) {
	dbtest.Main(m, schema.Migrations)
}

const (
	baseURL  = "https://slop.example"
	password = "correct horse battery"
)

type sentMail struct {
	to, subject, body string
}

// fakeMailer records what would have been sent. The real Outbox only adds
// the background send, which outbox_test.go covers on its own.
type fakeMailer struct {
	mu   sync.Mutex
	sent []sentMail
}

func (f *fakeMailer) Enqueue(to, subject, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sentMail{to: to, subject: subject, body: body})
}

func (f *fakeMailer) to(address string) []sentMail {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []sentMail
	for _, m := range f.sent {
		if m.to == address {
			out = append(out, m)
		}
	}
	return out
}

func (f *fakeMailer) last(t *testing.T, address string) sentMail {
	t.Helper()
	all := f.to(address)
	require.NotEmpty(t, all, "no mail sent to %s", address)
	return all[len(all)-1]
}

var tokenInLink = regexp.MustCompile(`#token=(\S+)`)

// tokenFrom pulls the one-time token out of an emailed link, the way the
// frontend reads it from the page's fragment.
func tokenFrom(t *testing.T, m sentMail) string {
	t.Helper()
	match := tokenInLink.FindStringSubmatch(m.body)
	require.Len(t, match, 2, "no token link in %q", m.body)
	raw, err := url.QueryUnescape(match[1])
	require.NoError(t, err)
	return raw
}

type fixture struct {
	db     *db.DB
	store  *user.PostgresStore
	svc    *user.Service
	signer *auth.Signer
	// clock is the time the signer and the service share; advance it to
	// cross a second boundary instead of sleeping across one.
	clock *testClock
	mail  *fakeMailer
	ctx   context.Context
	// events counts what the service recorded (see audit).
	events *audittest.Counter
	// agents and tickets are the real services archiving switches agents
	// off through, over the same database.
	agents  *agent.Service
	tickets *ticket.Service
}

func generous() *ratelimit.Config {
	return &ratelimit.Config{Limit: 1000, Window: time.Minute, MaxKeys: 1000}
}

func newFixture(t *testing.T, tweak ...func(*user.Config)) *fixture {
	t.Helper()
	return newFixtureWith(t, &fixtureOpts{config: tweak})
}

// fixtureOpts are what the sign-in method tests add to a fixture.
type fixtureOpts struct {
	config []func(*user.Config)
	// providers are registered with their issuer (the table identities
	// reference) and handed to the service.
	providers []testProvider
	// noProviders hands the service a nil provider set, as a deployment
	// that configures none might.
	noProviders bool
	passwords   user.Passwords
	// agents replaces the fixture's real agent service as what archiving
	// calls; noAgents leaves it out.
	agents   user.TenantAgents
	noAgents bool
	// tenantCreated is told of each tenant made, as main wires it.
	tenantCreated user.TenantCreatedHook
}

// testLoginStates seals the fixtures' login states, keyed as the server
// keys them: from its JWT secret.
func testLoginStates(t *testing.T) *gologin.LoginStateCodec {
	t.Helper()
	c, err := gologin.NewLoginStateCodec([]byte(testJWTSecret))
	require.NoError(t, err)
	return c
}

const testJWTSecret = "test-secret-at-least-32-bytes-long!!"

func newFixtureWith(t *testing.T, opts *fixtureOpts) *fixture {
	t.Helper()
	tweak := opts.config
	database := dbtest.WithDB(t)
	cfg := &user.Config{
		PublicBaseURL: baseURL + "/",
		ActivationTTL: time.Hour,
		ResetTTL:      time.Hour,
		InviteTTL:     time.Hour,
		// Open, so the tests of the sign-up flow itself can use it; the
		// invite tests close it where it matters.
		RegistrationOpen:  true,
		LoginLimit:        generous(),
		MailLimit:         generous(),
		LoginClientLimit:  generous(),
		MailClientLimit:   generous(),
		InviteLimit:       generous(),
		TenantCreateLimit: generous(),
		TenantOwnerLimit:  3,
	}
	for _, f := range tweak {
		f(cfg)
	}
	var storeOpts []user.StoreOption
	if opts.tenantCreated != nil {
		storeOpts = append(storeOpts, user.WithTenantCreatedHook(opts.tenantCreated, slog.DiscardHandler))
	}
	f := &fixture{
		db:    database,
		store: user.NewPostgresStore(database, storeOpts...),
		clock: &testClock{},
		mail:  &fakeMailer{},
		ctx:   context.Background(),
	}
	f.signer = auth.NewSigner([]byte(testJWTSecret), 15*time.Minute, 24*time.Hour).WithClock(f.clock.Now)
	var providers []gologin.AuthProvider
	for _, p := range opts.providers {
		require.NoError(t, gologin.NewIssuerBindings(database.Global()).Register(f.ctx, p.provider.Name(), p.issuer))
		providers = append(providers, p.provider)
	}
	csrf, err := auth.NewCSRFTokens([]byte(testJWTSecret))
	require.NoError(t, err)
	var events *audit.Recorder
	events, f.events = audittest.NewRecorder()
	f.tickets = ticket.NewService(ticket.NewPostgresStore(database), slog.DiscardHandler)
	f.agents = agent.NewService(agent.NewPostgresStore(database), f.tickets, nil, slog.DiscardHandler)
	var tenantAgents user.TenantAgents = f.agents
	if opts.agents != nil {
		tenantAgents = opts.agents
	}
	if opts.noAgents {
		tenantAgents = nil
	}
	deps := &user.Deps{
		Agents: tenantAgents,
		Store:  f.store, Signer: f.signer, Mail: f.mail,
		Passwords: opts.passwords, Providers: gologin.NewProviders(providers...), LoginStates: testLoginStates(t),
		CSRF: csrf, Audit: events, Clock: f.clock,
	}
	if opts.noProviders {
		deps.Providers = nil
	}
	f.svc = user.NewService(deps, cfg, slog.DiscardHandler)
	return f
}

// activeUser signs someone up and follows their activation link.
func (f *fixture) activeUser(t *testing.T, address string) {
	t.Helper()
	require.NoError(t, f.svc.Register(f.ctx, &user.RegisterInput{Email: address, Password: password, DisplayName: "Someone"}))
	require.NoError(t, f.svc.Activate(f.ctx, tokenFrom(t, f.mail.last(t, address))))
}

// signIn signs address in and returns who the middleware would say the
// session belongs to.
func (f *fixture) signIn(t *testing.T, address string) *authn.User {
	t.Helper()
	session, err := f.svc.Login(f.ctx, address, password)
	require.NoError(t, err)
	// Through VerifyAccess, as the middleware does, so the user carries the
	// session's issue time like a real request's.
	who, err := f.svc.VerifyAccess(f.ctx, session.Access)
	require.NoError(t, err)
	return who
}

// admin makes a signed-in admin.
func (f *fixture) admin(t *testing.T, address string) *authn.User {
	t.Helper()
	f.activeUser(t, address)
	require.NoError(t, user.NewSeeder(f.store).MakeAdmin(f.ctx, address))
	return f.signIn(t, address)
}

// seededAdmin makes a signed-in admin the way an environment's first
// account is made -- seeded, not signed up -- so it works with sign-up
// closed.
func (f *fixture) seededAdmin(t *testing.T, address string) *authn.User {
	t.Helper()
	seeder := user.NewSeeder(f.store)
	_, err := seeder.EnsureActive(f.ctx, &user.RegisterInput{Email: address, Password: password, DisplayName: "Admin"})
	require.NoError(t, err)
	require.NoError(t, seeder.MakeAdmin(f.ctx, address))
	return f.signIn(t, address)
}

func (f *fixture) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, f.db.Global().QueryRow(f.ctx, sql, args...).Scan(&n))
	return n
}

// testClock is the wall clock plus an offset the test moves forward.
type testClock struct {
	mu     sync.Mutex
	offset time.Duration
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.offset)
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.offset += d
}

func (c *testClock) NewTicker(d time.Duration) clock.Ticker { return clock.Real{}.NewTicker(d) }
func (c *testClock) After(d time.Duration) <-chan time.Time { return clock.Real{}.After(d) }
