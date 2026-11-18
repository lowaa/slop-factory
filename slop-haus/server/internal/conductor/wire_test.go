package conductor_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/authn/authntest"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/conductor"
)

// replica is one api process: its own Service and Handler over the shared
// database, served behind the auth middleware cmd/api installs. A bearer
// token is a signed-in user's or an agent's, by the maps it was registered
// in.
type replica struct {
	svc     *conductor.Service
	mux     *http.ServeMux
	server  *httptest.Server
	users   map[string]*authn.User
	agents  map[string]authn.Identity
	handler *conductor.Handler
}

func (f *fixture) replica(t *testing.T, svcOpts *conductor.Options, streamOpts *conductor.StreamOptions) *replica {
	t.Helper()
	r, api := f.bareReplica(t)
	r.svc = f.newService(svcOpts)
	t.Cleanup(r.svc.Close)
	r.handler = conductor.NewHandler(r.svc, f.conductors, streamOpts, slog.DiscardHandler)
	return r.serve(t, api, r.handler)
}

// bareReplicaWithOwners is a replica whose ownership checks go through owners.
func (f *fixture) bareReplicaWithOwners(t *testing.T, owners conductor.Owners, streamOpts *conductor.StreamOptions) *replica {
	t.Helper()
	r, api := f.bareReplica(t)
	r.svc = f.newService(nil)
	t.Cleanup(r.svc.Close)
	r.handler = conductor.NewHandler(r.svc, owners, streamOpts, slog.DiscardHandler)
	return r.serve(t, api, r.handler)
}

// bareReplica is the auth middleware and an empty API.
func (f *fixture) bareReplica(t *testing.T) (*replica, huma.API) {
	t.Helper()
	r := &replica{users: map[string]*authn.User{}, agents: map[string]authn.Identity{}}
	r.mux = http.NewServeMux()
	api := humago.New(r.mux, apiserver.NewConfig("test", "0.0.0"))
	logger := slog.New(slog.DiscardHandler)
	api.UseMiddleware(authn.DefaultDeny(api, logger))
	api.UseMiddleware(authn.HumaMiddleware(api, func(_ context.Context, token string) (authn.Identity, error) {
		if id, ok := r.agents[token]; ok {
			return id, nil
		}
		return nil, errors.New("unknown agent token")
	}, audit.New(slog.DiscardHandler, nil), logger))
	api.UseMiddleware(authn.UserMiddleware(api, func(_ context.Context, token string) (*authn.User, error) {
		if u, ok := r.users[token]; ok {
			return u, nil
		}
		return nil, errors.New("not a user token")
	}, authntest.Guard(), logger))
	return r, api
}

// replicaWith serves a handler of the test's own, for a fake service.
func (f *fixture) replicaWith(t *testing.T, h *conductor.Handler) *replica {
	t.Helper()
	r, api := f.bareReplica(t)
	return r.serve(t, api, h)
}

func (r *replica) serve(t *testing.T, api huma.API, h *conductor.Handler) *replica {
	t.Helper()
	conductor.RegisterRoutes(api, h)
	r.server = httptest.NewServer(r.mux)
	t.Cleanup(r.server.Close)
	return r
}

func (r *replica) do(token, method, path string, body any) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != nil {
		switch b := body.(type) {
		case string:
			reader = strings.NewReader(b)
		default:
			raw, _ := json.Marshal(b)
			reader = strings.NewReader(string(raw))
		}
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return out
}

// manualClock is driven by the test: a poll's wait ends when the test says
// the interval passed or the deadline came, never by the wall clock. Both
// channels are unbuffered, so sending on one returns only once the poll is
// waiting on it -- which is how a test knows a poll has looked and found
// nothing yet.
type manualClock struct {
	ticks    chan time.Time
	deadline chan time.Time
}

func newManualClock() *manualClock {
	return &manualClock{ticks: make(chan time.Time), deadline: make(chan time.Time)}
}

func (c *manualClock) Ticker(time.Duration) (<-chan time.Time, func()) { return c.ticks, func() {} }
func (c *manualClock) After(time.Duration) <-chan time.Time            { return c.deadline }

// tick makes one polling interval pass, returning once the poll has taken it.
func (c *manualClock) tick(t *testing.T) {
	t.Helper()
	select {
	case c.ticks <- time.Now():
	case <-time.After(10 * time.Second):
		t.Fatal("no poll was waiting")
	}
}

func newHostID() string { return uuid.NewString() }
