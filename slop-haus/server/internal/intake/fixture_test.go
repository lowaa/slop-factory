package intake_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/intake"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// Deliveries end to end against the real store and the real ticket rules:
// a request in, a ticket filed or not, and a record of which.

type tenantFixture struct {
	id     uuid.UUID
	ctx    context.Context
	member uuid.UUID
}

func (t *tenantFixture) who() *authn.User {
	return &authn.User{ID: t.member, TenantID: t.id, AuthTime: time.Now()}
}

type fixture struct {
	owners   intake.Owners
	t        *testing.T
	database *db.DB
	tickets  *ticket.Service
	svc      *intake.Service
	handler  http.Handler
	logs     *syncBuffer
	a, b     *tenantFixture

	mu  sync.Mutex
	now time.Time
	// limiter, if set, replaces the default (generous) rate limit.
	limiter intake.Limiter
}

// syncBuffer collects log output from handlers running on other goroutines.
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

const testKey = "0123456789abcdef0123456789abcdef"

type option func(*fixture)

// owners decides who the tenant's owner is: the members it lists.
type owners map[uuid.UUID]bool

func (o owners) IsTenantOwner(_ context.Context, userID, _ uuid.UUID) (bool, error) {
	return o[userID], nil
}

func withOwners(o intake.Owners) option { return func(f *fixture) { f.owners = o } }

func withLimiter(l intake.Limiter) option { return func(f *fixture) { f.limiter = l } }

func newFixture(t *testing.T, opts ...option) *fixture {
	t.Helper()
	f := &fixture{t: t, database: dbtest.WithDB(t), logs: &syncBuffer{}, now: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
	for _, o := range opts {
		o(f)
	}
	workspace := manifest.NewPolicy(manifest.NewPostgresStore(f.database))
	f.tickets = ticket.NewService(ticket.NewPostgresStore(f.database, ticket.WithStoreWorkspace(workspace)), slog.DiscardHandler,
		ticket.WithWorkspace(workspace))
	secrets, err := intake.NewSecrets([]byte(testKey))
	require.NoError(t, err)
	logHandler := slog.NewTextHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})
	f.svc = intake.NewService(intake.NewPostgresStore(f.database), f.tickets, &intake.Config{
		Enabled: true, Secrets: secrets, PublicBaseURL: "http://intake.example", Limiter: f.limiter,
		Caps: intake.Caps{EventsPerTicket: 5, LiveTickets: 3},
		Now:  f.clock, Archive: tenantstate.NewChecker(f.database), Owners: f.owners,
	}, nil, logHandler)
	f.handler = intake.NewReceiver(f.svc, logHandler).Handler()
	f.a, f.b = f.newTenant(), f.newTenant()
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

func (f *fixture) newTenant() *tenantFixture {
	f.t.Helper()
	id := dbtest.NewTenant(f.t, f.database)
	member := dbtest.NewUser(f.t, f.database)
	return &tenantFixture{id: id, ctx: db.WithTenant(context.Background(), id), member: member}
}

// source is a registered source and the credential it was given.
type source struct {
	id     uuid.UUID
	token  string
	secret string
	tenant *tenantFixture
}

// spec is a native bearer source's spec: untrusted unless a test says so.
func spec(mods ...func(*intake.Spec)) *intake.Spec {
	s := &intake.Spec{
		Name: "desk", Adapter: "native", Trust: intake.TrustUntrusted, AuthKind: intake.AuthBearer,
		DefaultStatus: ticket.StatusPreparing, DefaultPriority: model.PriorityNormal, MaxPriority: model.PriorityHigh,
	}
	for _, m := range mods {
		m(s)
	}
	return s
}

func trusted(s *intake.Spec) { s.Trust = intake.TrustTrusted }

func (f *fixture) source(tenant *tenantFixture, s *intake.Spec) *source {
	f.t.Helper()
	created, err := f.svc.Create(tenant.ctx, tenant.who(), s)
	require.NoError(f.t, err)
	return &source{id: created.Source.ID, token: created.Token, secret: created.Secret, tenant: tenant}
}

// request is one POST to the receiver.
type request struct {
	path        string
	body        string
	contentType string
	headers     map[string]string
}

func (s *source) post(body any) *request {
	raw, ok := body.(string)
	if !ok {
		b, err := json.Marshal(body)
		if err != nil {
			panic(err)
		}
		raw = string(b)
	}
	return &request{
		path: "/hooks/intake/" + s.id.String(), body: raw, contentType: "application/json",
		headers: map[string]string{"Authorization": "Bearer " + s.token},
	}
}

func (r *request) header(k, v string) *request {
	r.headers[k] = v
	return r
}

func (r *request) without(k string) *request {
	delete(r.headers, k)
	return r
}

func (f *fixture) send(r *request) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(http.MethodPost, r.path, strings.NewReader(r.body))
	if r.contentType != "" {
		req.Header.Set("Content-Type", r.contentType)
	}
	for k, v := range r.headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

// event is a native payload.
func event(kind, key string, extra map[string]any) map[string]any {
	e := map[string]any{"key": key, "event": kind}
	if kind == "open" {
		e["title"] = "Printer on fire"
	}
	for k, v := range extra {
		e[k] = v
	}
	return e
}

// ticketFor is the newest ticket the source filed for key, or nil.
func (f *fixture) ticketFor(s *source, key string) *ticket.Ticket {
	f.t.Helper()
	all, err := f.tickets.List(s.tenant.ctx, &ticket.ListFilter{})
	require.NoError(f.t, err)
	var found *ticket.Ticket
	for i := range all {
		t := &all[i]
		if t.Intake != nil && t.Intake.SourceID == s.id && t.Intake.Key == key && (found == nil || t.ID > found.ID) {
			found = t
		}
	}
	return found
}

func (f *fixture) allTickets(tenant *tenantFixture) []ticket.Ticket {
	f.t.Helper()
	all, err := f.tickets.List(tenant.ctx, &ticket.ListFilter{})
	require.NoError(f.t, err)
	return all
}

func (f *fixture) deliveries(tenant *tenantFixture) []intake.Delivery {
	f.t.Helper()
	ds, err := f.svc.Deliveries(tenant.ctx)
	require.NoError(f.t, err)
	return ds
}

func (f *fixture) external(s *source, ticketID int64) []ticket.ExternalEvent {
	f.t.Helper()
	events, err := f.tickets.ListExternal(s.tenant.ctx, ticketID)
	require.NoError(f.t, err)
	return events
}

func (f *fixture) setStatus(tenant *tenantFixture, id int64, status ticket.Status) {
	f.t.Helper()
	_, err := f.tickets.Update(tenant.ctx, id, &ticket.UpdateParams{Status: &status})
	require.NoError(f.t, err)
}
