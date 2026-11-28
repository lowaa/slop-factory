package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/api"
	"github.com/lowaa/slop-factory/slop-bot/internal/config"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets/local"
)

// Tests of the ticket routes' own read-then-write rules, against the real
// local store, with hooks that land another request between their steps.

// hookedTickets runs a test's hook inside the next Create, so a second
// request can land in between.
type hookedTickets struct {
	tickets.TicketingSystem
	mu       sync.Mutex
	inCreate func()
}

func (h *hookedTickets) Create(ctx context.Context, by ident.Principal, nt *tickets.NewTicket) (*tickets.Ticket, error) {
	h.mu.Lock()
	hook := h.inCreate
	h.inCreate = nil
	h.mu.Unlock()
	if hook != nil {
		hook()
	}
	return h.TicketingSystem.Create(ctx, by, nt)
}

type routesFixture struct {
	t   *testing.T
	sys *hookedTickets
	tr  *ticketRoutes
}

func newRoutesFixture(t *testing.T, cfgYAML string) *routesFixture {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.Parse([]byte(cfgYAML), filepath.Join(dir, "config.yaml"), dir)
	require.NoError(t, err)
	ctx := context.Background()
	ts, err := local.Open(ctx, &local.Options{Path: filepath.Join(dir, "tickets.db"), Logger: slog.New(slog.DiscardHandler),
		Clock: clock.NewFake(time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC))})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ts.Close() })
	require.NoError(t, ts.Start(ctx, ""))
	sys := &hookedTickets{TicketingSystem: ts}
	tr := &ticketRoutes{sys: sys, cfg: cfg, user: "ada"}
	return &routesFixture{t: t, sys: sys, tr: tr}
}

// do serves one request to h as by, for ticket id when it isn't "", and
// returns its status, decoding a success's body into out.
func (f *routesFixture) do(by ident.Principal, h http.HandlerFunc, id string, in, out any) int {
	f.t.Helper()
	body, err := json.Marshal(in)
	require.NoError(f.t, err)
	req := httptest.NewRequestWithContext(control.WithPrincipal(context.Background(), by), http.MethodPost, "/", bytes.NewReader(body))
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	h(rec, req)
	if out != nil && rec.Code < 300 {
		require.NoError(f.t, json.Unmarshal(rec.Body.Bytes(), out))
	}
	return rec.Code
}

// Two agents filing at once with one place left in the budget: both count
// before either has created, so the count alone lets both through. The
// first is held inside its create while the second files.
func TestTwoFilingsRacingForTheLastBudgetPlaceGetOne(t *testing.T) {
	t.Parallel()
	f := newRoutesFixture(t, "concurrency: {max_active: 1, urgent: {max_active: 1}}\nautonomy: {tickets_per_agent: 1}\n")
	require.Equal(t, 2, f.tr.cfg.AutonomyBudget())
	var parent api.Ticket
	require.Equal(t, http.StatusCreated, f.do(ident.UserPrincipal(), f.tr.create, "", &api.NewTicket{Title: "parent"}, &parent))
	agent := ident.AgentPrincipal("a1", ident.TicketID(parent.ID))
	require.Equal(t, http.StatusCreated, f.do(agent, f.tr.create, "", &api.NewTicket{Title: "one"}, nil))

	entered, release := make(chan struct{}), make(chan struct{})
	f.sys.mu.Lock()
	f.sys.inCreate = func() { close(entered); <-release }
	f.sys.mu.Unlock()
	first := make(chan int, 1)
	go func() { first <- f.do(agent, f.tr.create, "", &api.NewTicket{Title: "two"}, nil) }()
	<-entered
	assert.Equal(t, http.StatusTooManyRequests, f.do(agent, f.tr.create, "", &api.NewTicket{Title: "three"}, nil),
		"the place the first is filing into is taken")
	close(release)
	assert.Equal(t, http.StatusCreated, <-first)
	n, err := f.sys.CountOutstandingAgentCreated(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, n)
}
