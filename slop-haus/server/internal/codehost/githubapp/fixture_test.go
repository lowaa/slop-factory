package githubapp_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/githubapp"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/secretbox"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

const (
	webURL         = "https://github.test"
	boardURL       = "https://board.test"
	appID          = int64(4242)
	installationID = int64(777)
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

type fakeManifest struct {
	remotes []string
	err     error
}

func (m *fakeManifest) Remotes(context.Context) ([]string, error) { return m.remotes, m.err }

type tenant struct {
	id     uuid.UUID
	ctx    context.Context
	owner  *authn.User
	member *authn.User
}

// fixture is the real store and rules against the fake GitHub: what is
// faked is the network, nothing the credential's own rules decide.
type fixture struct {
	t        *testing.T
	database *db.DB
	owners   *db.DB
	github   *fakeGitHub
	logs     *syncBuffer
	manifest *fakeManifest
	svc      *githubapp.Service
	keyring  *secretbox.Keyring
	keyBytes []byte
	store    *githubapp.PostgresStore

	mu  sync.Mutex
	now time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	app, owner := dbtest.WithOwnerDB(t)
	return newFixtureOver(t, app, owner)
}

func newFixtureOver(t *testing.T, app, owner *db.DB) *fixture {
	t.Helper()
	f := &fixture{t: t, database: app, owners: owner, github: newFakeGitHub(t), logs: &syncBuffer{},
		manifest: &fakeManifest{}, now: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC),
		store: githubapp.NewPostgresStore(app)}
	f.keyBytes = newKey(t)
	f.keyring = newRing(t, f.keyBytes, nil)
	f.svc = f.service(f.keyring)
	return f
}

func (f *fixture) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fixture) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// service builds a service over the shared database and fake GitHub: with
// a nil keyring it is the feature switched off.
func (f *fixture) service(ring *secretbox.Keyring) *githubapp.Service {
	return f.serviceOver(ring, f.store)
}

// serviceOver is service with another store, for a test that wraps the real one.
func (f *fixture) serviceOver(ring *secretbox.Keyring, store githubapp.Store) *githubapp.Service {
	return f.serviceWith(ring, store, nil)
}

// serviceWith is serviceOver with options the test adds to the usual ones.
func (f *fixture) serviceWith(ring *secretbox.Keyring, store githubapp.Store, tweak func(*githubapp.Options)) *githubapp.Service {
	client, err := githubapp.NewClient(&githubapp.ClientOptions{BaseURL: f.github.URL()})
	require.NoError(f.t, err)
	logHandler := slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})
	opts := &githubapp.Options{
		Store: store, Keyring: ring, Client: client, Owners: user.NewPostgresStore(f.database),
		Manifest: f.manifest, Events: audit.New(logHandler, nil), Logger: logHandler, Now: f.clock, WebBaseURL: webURL, BoardBaseURL: boardURL,
	}
	if tweak != nil {
		tweak(opts)
	}
	svc, err := githubapp.NewService(opts)
	require.NoError(f.t, err)
	return svc
}

func (f *fixture) join(tenantID uuid.UUID, role string) *authn.User {
	f.t.Helper()
	id := dbtest.NewUser(f.t, f.database)
	_, err := f.database.Global().Exec(context.Background(),
		`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES ($1, $2, $3, $4)`,
		uuid.New(), tenantID, id, role)
	require.NoError(f.t, err)
	return &authn.User{ID: id, TenantID: tenantID, AuthTime: time.Now()}
}

func (f *fixture) tenant() *tenant {
	f.t.Helper()
	id := dbtest.NewTenant(f.t, f.database)
	return &tenant{id: id, ctx: db.WithTenant(context.Background(), id),
		owner: f.join(id, "owner"), member: f.join(id, "member")}
}

// connect stores the n-th test key for the tenant.
func (f *fixture) connect(tn *tenant, keyN int) {
	f.t.Helper()
	_, err := f.svc.Connect(tn.ctx, tn.owner, &githubapp.Connection{
		AppID: appID, InstallationID: installationID, PrivateKey: []byte(pkcs1PEM(rsaKey(f.t, keyN))),
	})
	require.NoError(f.t, err)
}

// server mounts the real routes with the signed-in user the test picks, in
// place of the session middleware.
func (f *fixture) server(who *authn.User) *http.ServeMux {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(func(ctx huma.Context, next func(huma.Context)) {
		if who != nil {
			ctx = huma.WithContext(ctx, authn.NewUserContext(ctx.Context(), who))
		}
		next(ctx)
	})
	githubapp.RegisterRoutes(api, githubapp.NewHandler(f.svc, slog.New(slog.NewJSONHandler(f.logs, nil)).Handler()))
	return mux
}

func call(mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func jsonBody(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

func newKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, secretbox.KeySize)
	_, err := rand.Read(k)
	require.NoError(t, err)
	return k
}

func newRing(t *testing.T, current, previous []byte) *secretbox.Keyring {
	t.Helper()
	ring, err := secretbox.NewKeyring(current, previous)
	require.NoError(t, err)
	return ring
}

func (f *fixture) logHandler() slog.Handler {
	return slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})
}

func (f *fixture) client() *githubapp.Client {
	f.t.Helper()
	c, err := githubapp.NewClient(&githubapp.ClientOptions{BaseURL: f.github.URL()})
	require.NoError(f.t, err)
	return c
}
