package ticket_test

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/authn/authntest"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// spyStore is the real PostgresStore with two hooks for what a database
// can't show: the filter the handler passed down, and a read failing on cue.
// It decides nothing itself.
type spyStore struct {
	ticket.Store

	mu         sync.Mutex
	lastFilter *ticket.ListFilter
	listErr    error
}

func (s *spyStore) ListTickets(ctx context.Context, filter *ticket.ListFilter) ([]ticket.Ticket, error) {
	s.mu.Lock()
	s.lastFilter = filter
	err := s.listErr
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return s.Store.ListTickets(ctx, filter)
}

func (s *spyStore) failLists(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listErr = err
}

func (s *spyStore) listedWith() *ticket.ListFilter {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastFilter
}

// testStack is the ticket routes over a real database, store and service,
// signed in as a real user of a real tenant: what a handler test needs when
// it asserts on what storage returns.
type testStack struct {
	mux    http.Handler
	svc    *ticket.Service
	store  *spyStore
	ctx    context.Context
	user   uuid.UUID
	tenant uuid.UUID
}

func newTestStack(t *testing.T) *testStack {
	t.Helper()
	database := dbtest.WithDB(t)
	tenant := dbtest.NewTenant(t, database)
	user := dbtest.NewUser(t, database)
	store := &spyStore{Store: newStore(database)}
	svc := ticket.NewService(store, slog.DiscardHandler)

	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(authn.UserMiddleware(api, func(_ context.Context, _ string) (*authn.User, error) {
		return &authn.User{ID: user, TenantID: tenant}, nil
	}, authntest.Guard(), slog.New(slog.DiscardHandler)))
	ticket.RegisterRoutes(api, ticket.NewHandler(svc, slog.DiscardHandler))
	return &testStack{mux: mux, svc: svc, store: store, user: user, tenant: tenant,
		ctx: db.WithTenant(context.Background(), tenant)}
}

func (s *testStack) create(t *testing.T, params *ticket.CreateParams) *ticket.Ticket {
	t.Helper()
	created, err := s.svc.Create(s.ctx, params)
	require.NoError(t, err)
	return created
}

// newUnauthenticatedTestMux wires the same routes with a verifier that
// refuses every caller, so a route that lost its authn.RequireUser()
// metadata fails. No request reaches the service, hence no store.
func newUnauthenticatedTestMux() *http.ServeMux {
	svc := ticket.NewService(nil, slog.DiscardHandler)
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(authn.UserMiddleware(api, func(_ context.Context, _ string) (*authn.User, error) {
		return nil, errNoCredential
	}, authntest.Guard(), slog.New(slog.DiscardHandler)))
	ticket.RegisterRoutes(api, ticket.NewHandler(svc, slog.DiscardHandler))
	return mux
}
