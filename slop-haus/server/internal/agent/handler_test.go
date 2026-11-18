package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/harnessconfig"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/session"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/workspace"
)

// fakeAgentStore is an in-memory agent.Store, standing in for
// agent.PostgresStore so these tests exercise real wire behavior (routing,
// huma param binding, status codes, JSON shape) without a database.
type fakeAgentStore struct {
	mu     sync.Mutex
	byID   map[uuid.UUID]*agent.Agent
	byName map[string]uuid.UUID
	// tenantID stands in for the tenant every agent this fake creates
	// belongs to. A single fixed value is enough: nothing here tests
	// cross-tenant isolation, which is covered against the real store in
	// internal/ticket.
	tenantID uuid.UUID
	// createErr is what Create returns, when set. Name uniqueness is the
	// database's rule, tested against the real store in service_test.go, so
	// a test of the handler's error mapping injects ErrNameTaken here
	// rather than have this fake decide when a name is taken.
	createErr error
}

func newFakeAgentStore() *fakeAgentStore {
	return &fakeAgentStore{
		byID:     map[uuid.UUID]*agent.Agent{},
		byName:   map[string]uuid.UUID{},
		tenantID: uuid.New(),
	}
}

func (s *fakeAgentStore) Create(ctx context.Context, id uuid.UUID, name string, kind agent.Kind) (*agent.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.createErr != nil {
		return nil, s.createErr
	}
	a := &agent.Agent{ID: id, Tenant: s.tenantID, Name: name, CreatedAt: time.Now(), Kind: kind}
	s.byID[id] = a
	s.byName[name] = id
	cp := *a
	return &cp, nil
}

// runBy records that a supervisor runs the agent; the rule behind it lives in
// the database and is tested against the real store.
func (s *fakeAgentStore) runBy(id, supervisorID uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[id].SupervisorID = &supervisorID
}

func (s *fakeAgentStore) Get(ctx context.Context, id uuid.UUID) (*agent.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	a, ok := s.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", agent.ErrNotFound, id)
	}
	cp := *a
	return &cp, nil
}

func (s *fakeAgentStore) GetByName(ctx context.Context, name string) (*agent.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id, ok := s.byName[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", agent.ErrNotFound, name)
	}
	cp := *s.byID[id]
	return &cp, nil
}

func (s *fakeAgentStore) List(ctx context.Context, includeArchived bool) ([]agent.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := []agent.Agent{}
	for _, a := range s.byID {
		out = append(out, *a)
	}
	return out, nil
}

func (s *fakeAgentStore) SetDisabled(ctx context.Context, id uuid.UUID, disabled bool) (*agent.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	a, ok := s.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", agent.ErrNotFound, id)
	}
	if disabled {
		now := time.Now()
		a.DisabledAt = &now
	} else {
		a.DisabledAt = nil
	}
	cp := *a
	return &cp, nil
}

// Archiving's rules (disabled first, not enabled while archived) are the
// database's; they are tested against the real store in archive_test.go.
func (s *fakeAgentStore) SetArchived(context.Context, uuid.UUID, bool) (*agent.Agent, error) {
	return nil, errors.New("fakeAgentStore: archiving is tested against the real store")
}

// Replacing is one transaction in the database; tested against the real store
// in replace_test.go.
func (s *fakeAgentStore) Replace(context.Context, uuid.UUID, string, agent.Kind, uuid.UUID) (*agent.Agent, *agent.Agent, error) {
	return nil, nil, errors.New("fakeAgentStore: replacing is tested against the real store")
}

// Conductors are the real database's business (ownership and its
// immutability are constraints); a handler test reaching one is a mistake.
func (s *fakeAgentStore) CreateConductor(context.Context, uuid.UUID, string, uuid.UUID, *uuid.UUID) (*agent.Agent, error) {
	return nil, errors.New("fakeAgentStore: conductors are tested against the real store")
}

func (s *fakeAgentStore) ListConductors(context.Context, *uuid.UUID, bool) ([]agent.Agent, error) {
	return nil, errors.New("fakeAgentStore: conductors are tested against the real store")
}

func (s *fakeAgentStore) ListConductorDirectory(context.Context, bool) ([]agent.DirectoryEntry, error) {
	return nil, errors.New("fakeAgentStore: conductors are tested against the real store")
}

// No handler reaches a supervisor's slots, so these fail loudly if one starts to.
func (s *fakeAgentStore) CreateSupervisor(context.Context, uuid.UUID, string, int) (*agent.Agent, error) {
	panic("fakeAgentStore: CreateSupervisor not expected")
}

func (s *fakeAgentStore) SetSlotCap(context.Context, uuid.UUID, int) (*agent.Agent, int, error) {
	panic("fakeAgentStore: SetSlotCap not expected")
}

func (s *fakeAgentStore) ListSupervisors(context.Context, bool) ([]agent.Agent, error) {
	panic("fakeAgentStore: ListSupervisors not expected")
}

func (s *fakeAgentStore) EnsureSteward(context.Context, uuid.UUID, uuid.UUID) (*agent.Agent, error) {
	panic("fakeAgentStore: EnsureSteward not expected")
}

func (s *fakeAgentStore) CreateSlot(context.Context, uuid.UUID, uuid.UUID, string) (*agent.Agent, error) {
	panic("fakeAgentStore: CreateSlot not expected")
}

func (s *fakeAgentStore) AdoptSlot(context.Context, uuid.UUID, string) (*agent.Agent, error) {
	panic("fakeAgentStore: AdoptSlot not expected")
}

func (s *fakeAgentStore) ListSlots(context.Context, uuid.UUID, bool) ([]agent.Agent, error) {
	panic("fakeAgentStore: ListSlots not expected")
}

func (s *fakeAgentStore) DisableSlots(context.Context, uuid.UUID, string) ([]agent.Agent, error) {
	panic("fakeAgentStore: DisableSlots not expected")
}

func (s *fakeAgentStore) SupervisorLastSeen(context.Context, uuid.UUID) (time.Time, error) {
	panic("fakeAgentStore: SupervisorLastSeen not expected")
}

func (s *fakeAgentStore) ListEnabled(context.Context) ([]agent.Agent, error) {
	panic("fakeAgentStore: ListEnabled not expected")
}

var _ agent.Store = (*fakeAgentStore)(nil)

// fakeAgentTicketStore is the leases the agent handler reads, which is all
// of the ticket service it calls.
type fakeAgentTicketStore struct {
	mu     sync.Mutex
	leases map[uuid.UUID]*ticket.Lease
}

func (s *fakeAgentTicketStore) LeaseFor(ctx context.Context, agentID uuid.UUID) (*ticket.Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	l, ok := s.leases[agentID]
	if !ok {
		return nil, ticket.ErrNoLease
	}
	cp := *l
	return &cp, nil
}

var _ agent.Leases = (*fakeAgentTicketStore)(nil)

// newTestMux wires /api/agents exactly as cmd/api/main.go does, so these
// tests exercise the real wire behavior (status + JSON shape) rather than
// just the handler's Go return values.
func newTestMux() (*http.ServeMux, *fakeAgentStore, *fakeAgentTicketStore) {
	mux, agentStore, ticketStore, _ := newTestMuxWithGrants()
	return mux, agentStore, ticketStore
}

// newTestMuxWithGrants is newTestMux plus the fake OAuth side, for the
// routes that sign an agent's workspaces out.
func newTestMuxWithGrants() (*http.ServeMux, *fakeAgentStore, *fakeAgentTicketStore, *fakeGrants) {
	mux, agentStore, ticketStore, grants, _ := newTestMuxWithEverything()
	return mux, agentStore, ticketStore, grants
}

// fakeSessions is each agent's current run, keyed by agent.
type fakeSessions struct {
	current map[uuid.UUID]*session.Session
}

func (f *fakeSessions) List(context.Context) ([]session.Session, error) {
	out := []session.Session{}
	for _, s := range f.current {
		out = append(out, *s)
	}
	return out, nil
}

func (f *fakeSessions) Current(_ context.Context, agentID uuid.UUID) (*session.Session, error) {
	s, ok := f.current[agentID]
	if !ok {
		return nil, session.ErrNotFound
	}
	cp := *s
	return &cp, nil
}

var _ agent.Sessions = (*fakeSessions)(nil)

// noRoles is a tenant with no roles, for the agent listings these fakes
// serve. The rest of agent.RoleStore is embedded unimplemented, so reaching
// it panics; roles are tested against the real store in role_test.go.
type noRoles struct{ agent.RoleStore }

func (noRoles) RolesFor(context.Context, uuid.UUID) ([]agent.Role, error) { return nil, nil }

func (noRoles) RolesByAgent(context.Context) (map[uuid.UUID][]agent.Role, error) {
	return map[uuid.UUID][]agent.Role{}, nil
}

func newTestMuxWithEverything() (*http.ServeMux, *fakeAgentStore, *fakeAgentTicketStore, *fakeGrants, *fakeSessions) {
	agentStore := newFakeAgentStore()
	grants := &fakeGrants{}
	agentSvc := agent.NewService(agentStore, &fakeReleaser{}, grants, slog.NewTextHandler(io.Discard, nil))

	ticketStore := &fakeAgentTicketStore{leases: map[uuid.UUID]*ticket.Lease{}}

	sessions := &fakeSessions{current: map[uuid.UUID]*session.Session{}}
	handler := agent.NewHandler(&agent.HandlerDeps{
		Agents:   agentSvc,
		Roles:    agent.NewRoleService(noRoles{}, slog.DiscardHandler),
		Tickets:  ticketStore,
		Sessions: sessions,
	}, slog.DiscardHandler)

	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	agent.RegisterRoutes(api, handler)
	return mux, agentStore, ticketStore, grants, sessions
}

// fakeGrants stands in for *oauth.Service. These tests never authenticate
// by token (update_script_test.go does, against a fake of its own); they
// record which agents were signed out. The real revoke is covered against
// a real database in internal/oauth.
type fakeGrants struct {
	mu      sync.Mutex
	revoked []uuid.UUID
}

func (f *fakeGrants) Authenticate(context.Context, string) (*oauth.Access, error) {
	return nil, agent.ErrBadToken
}

func (f *fakeGrants) RevokeAgentGrants(_ context.Context, _, agentID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, agentID)
	return nil
}

var _ agent.Grants = (*fakeGrants)(nil)

// An agent has no credential of its own any more -- a client is authorized
// to act as it through OAuth (see internal/oauth) -- so the create
// response is a plain AgentBody with nothing token-shaped in it, on this
// response or any later fetch of the same agent.
func TestCreateAgent_HasNoCredentialInTheResponse(t *testing.T) {
	mux, _, _ := newTestMux()

	req := httptest.NewRequest(http.MethodPost, "/api/agents", strings.NewReader(`{"name": "watcher"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "token", "an agent is authorized through OAuth, not a bearer token in this response")

	var created agent.AgentBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	assert.Equal(t, "watcher", created.Name)
	assert.Nil(t, created.Lease)

	getReq := httptest.NewRequest(http.MethodGet, "/api/agents/"+created.ID, nil)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, getReq)
	require.Equal(t, http.StatusOK, getRec.Code, "body: %s", getRec.Body.String())
	assert.NotContains(t, getRec.Body.String(), "token")
}

func TestCreateAgent_EmptyName_Returns422(t *testing.T) {
	mux, _, _ := newTestMux()

	req := httptest.NewRequest(http.MethodPost, "/api/agents", strings.NewReader(`{"name": ""}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body: %s", rec.Body.String())
}

func TestCreateAgent_NameTaken_Returns422(t *testing.T) {
	mux, agentStore, _ := newTestMux()
	agentStore.createErr = fmt.Errorf("%w: %q", agent.ErrNameTaken, "dup")

	req := httptest.NewRequest(http.MethodPost, "/api/agents", strings.NewReader(`{"name": "dup"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body: %s", rec.Body.String())
}

func TestGetAgent_NotFound_Returns404(t *testing.T) {
	mux, _, _ := newTestMux()

	req := httptest.NewRequest(http.MethodGet, "/api/agents/"+uuid.New().String(), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code, "body: %s", rec.Body.String())
}

func TestGetAgent_BadUUID_Returns422(t *testing.T) {
	mux, _, _ := newTestMux()

	req := httptest.NewRequest(http.MethodGet, "/api/agents/not-a-uuid", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body: %s", rec.Body.String())
}

// The agents page splits workers from sessions by this field alone.
func TestGetAgent_ReportsTheKind(t *testing.T) {
	mux, agentStore, _ := newTestMux()
	for name, kind := range map[string]agent.Kind{"a-worker": agent.KindWorker, "a-console": agent.KindConsole} {
		created, err := agentStore.Create(context.Background(), uuid.New(), name, kind)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/api/agents/"+created.ID.String(), nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		var body agent.AgentBody
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Equal(t, kind, body.Kind, name)
	}
}

// The Agents screen is the one the human lives on, so a listing must show
// who holds what without a second request per agent.
func TestListAgents_ShowsHeldLease(t *testing.T) {
	mux, agentStore, ticketStore := newTestMux()
	ctx := context.Background()

	holder, err := agentStore.Create(ctx, uuid.New(), "holder", agent.KindWorker)
	require.NoError(t, err)
	_, err = agentStore.Create(ctx, uuid.New(), "idle", agent.KindWorker)
	require.NoError(t, err)

	now := time.Now()
	ticketStore.leases[holder.ID] = &ticket.Lease{
		TicketID:   42,
		AgentID:    holder.ID,
		AcquiredAt: now,
		ExpiresAt:  now.Add(ticket.DefaultLeaseTTL),
	}

	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var body struct {
		Agents []agent.AgentBody `json:"agents"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Agents, 2)

	byName := map[string]agent.AgentBody{}
	for _, a := range body.Agents {
		byName[a.Name] = a
	}
	require.NotNil(t, byName["holder"].Lease)
	assert.Equal(t, int64(42), byName["holder"].Lease.TicketID)
	assert.Nil(t, byName["idle"].Lease, "an agent holding nothing must show a null lease, not an error")
}

// What each agent is running as -- harness, model, labels -- is on the
// listing and on a single agent, and an agent that has never run shows a
// null session rather than an empty one.
// The loop breaker's notice lives on the run it stopped: a run that is stuck
// shows why and since when, and one that is not shows null.
func TestAgents_ShowAStuckRunWithItsReason(t *testing.T) {
	mux, agentStore, _, _, sessions := newTestMuxWithEverything()
	ctx := context.Background()
	stuck, err := agentStore.Create(ctx, uuid.New(), "stuck", agent.KindWorker)
	require.NoError(t, err)
	fine, err := agentStore.Create(ctx, uuid.New(), "fine", agent.KindWorker)
	require.NoError(t, err)
	at := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	sessions.current[stuck.ID] = &session.Session{AgentID: stuck.ID, Stuck: &session.Stuck{Reason: "Stuck on #42", At: at}}
	sessions.current[fine.ID] = &session.Session{AgentID: fine.ID}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var list struct {
		Agents []agent.AgentBody `json:"agents"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	byName := map[string]agent.AgentBody{}
	for _, a := range list.Agents {
		byName[a.Name] = a
	}
	assert.Equal(t, &agent.StuckBody{Reason: "Stuck on #42", At: at}, byName["stuck"].Session.Stuck)
	assert.Nil(t, byName["fine"].Session.Stuck)
}

func TestAgents_ShowTheCurrentRunsSelfDescription(t *testing.T) {
	mux, agentStore, _, _, sessions := newTestMuxWithEverything()
	ctx := context.Background()

	running, err := agentStore.Create(ctx, uuid.New(), "running", agent.KindWorker)
	require.NoError(t, err)
	_, err = agentStore.Create(ctx, uuid.New(), "never-ran", agent.KindWorker)
	require.NoError(t, err)
	started := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	sessions.current[running.ID] = &session.Session{
		AgentID:    running.ID,
		StartedAt:  started,
		LastSeenAt: started.Add(5 * time.Minute),
		Description: session.Description{
			Harness: "pi", HarnessVersion: "0.87.0", Provider: "anthropic", Model: "claude-sonnet-4-5",
			Labels: map[string]string{"gpu": "a100"},
		},
	}
	want := &agent.SessionBody{
		StartedAt: started, LastSeenAt: started.Add(5 * time.Minute),
		Harness: "pi", HarnessVersion: "0.87.0", Provider: "anthropic", Model: "claude-sonnet-4-5",
		Labels: map[string]string{"gpu": "a100"}, ConfigStatus: agent.ConfigNone, BundleStatus: workspace.BundleUnknown,
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var list struct {
		Agents []agent.AgentBody `json:"agents"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	byName := map[string]agent.AgentBody{}
	for _, a := range list.Agents {
		byName[a.Name] = a
	}
	assert.Equal(t, want, byName["running"].Session)
	assert.Nil(t, byName["never-ran"].Session)
	assert.Contains(t, rec.Body.String(), `"session":null`)

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents/"+running.ID.String(), nil))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var one agent.AgentBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &one))
	assert.Equal(t, want, one.Session)
}

// fakeHarness is a tenant's saved harness config, as the Agents screen
// reads it.
type fakeHarness struct{ tenant *harnessconfig.Tenant }

func (f *fakeHarness) Load(context.Context) (*harnessconfig.Tenant, error) { return f.tenant, nil }

func (f *fakeHarness) Membership(_ context.Context, id uuid.UUID) (*harnessconfig.VariantMembership, error) {
	return f.tenant.Variants[id], nil
}

// A session started on an older config revision than its agent's current
// one is marked stale -- the human's cue to restart it -- and only that
// agent's: another agent's override moves nobody else's revision.
func TestAgents_MarkASessionRunningAnOlderConfig(t *testing.T) {
	agentStore := newFakeAgentStore()
	ctx := context.Background()
	create := func(name string) *agent.Agent {
		created, err := agentStore.Create(ctx, uuid.New(), name, agent.KindWorker)
		require.NoError(t, err)
		return created
	}
	current, stale, overridden, unknown := create("current"), create("stale"), create("overridden"), create("unknown")
	reset := create("reset")
	create("idle")
	joined := create("joined")

	revision := func(n int64) *int64 { return &n }
	sessions := &fakeSessions{current: map[uuid.UUID]*session.Session{
		current.ID:    {AgentID: current.ID, Description: session.Description{ConfigRevision: revision(4)}},
		stale.ID:      {AgentID: stale.ID, Description: session.Description{ConfigRevision: revision(3)}},
		overridden.ID: {AgentID: overridden.ID, Description: session.Description{ConfigRevision: revision(4)}},
		unknown.ID:    {AgentID: unknown.ID},
		// Started on an override that has since been removed.
		reset.ID: {AgentID: reset.ID, Description: session.Description{ConfigRevision: revision(6)}},
		// Started before the agent was put on a variant, which moved its revision.
		joined.ID: {AgentID: joined.ID, Description: session.Description{ConfigRevision: revision(4)}},
	}}
	harness := &fakeHarness{tenant: &harnessconfig.Tenant{
		Defaults: &harnessconfig.Scope{Revision: 4},
		Overrides: map[uuid.UUID]*harnessconfig.Scope{
			overridden.ID: {AgentID: &overridden.ID, Revision: 6},
			joined.ID:     {AgentID: &joined.ID, Revision: 2},
		},
		Variants: map[uuid.UUID]*harnessconfig.VariantMembership{
			joined.ID: {VersionID: uuid.New(), Name: "fast", Version: 2, Harness: "pi", Revision: 9},
		},
	}}
	handler := agent.NewHandler(&agent.HandlerDeps{
		Agents:        agent.NewService(agentStore, &fakeReleaser{}, &fakeGrants{}, slog.DiscardHandler),
		Roles:         agent.NewRoleService(noRoles{}, slog.DiscardHandler),
		Tickets:       &fakeAgentTicketStore{leases: map[uuid.UUID]*ticket.Lease{}},
		Sessions:      sessions,
		HarnessConfig: harness,
	}, slog.DiscardHandler)
	mux := http.NewServeMux()
	agent.RegisterRoutes(humago.New(mux, huma.DefaultConfig("test", "0.0.0")), handler)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var list struct {
		Agents []agent.AgentBody `json:"agents"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	byName := map[string]agent.AgentBody{}
	for _, a := range list.Agents {
		byName[a.Name] = a
	}

	for name, want := range map[string]struct {
		revision int64
		status   string
	}{
		"current":    {4, agent.ConfigCurrent},
		"stale":      {4, agent.ConfigStale},
		"overridden": {6, agent.ConfigStale},
		"unknown":    {4, agent.ConfigUnknown},
		"reset":      {4, agent.ConfigStale},
		"joined":     {9, agent.ConfigStale},
	} {
		got := byName[name]
		assert.Equal(t, want.revision, got.ConfigRevision, name)
		if assert.NotNil(t, got.Session, name) {
			assert.Equal(t, want.status, got.Session.ConfigStatus, name)
		}
	}
	if assert.NotNil(t, byName["joined"].Variant) {
		assert.Equal(t, "fast", byName["joined"].Variant.Name)
		assert.Equal(t, "pi", byName["joined"].Variant.Harness)
		assert.Equal(t, 2, byName["joined"].Variant.Version)
		assert.NotEmpty(t, byName["joined"].Variant.VersionID)
	}
	assert.True(t, byName["joined"].HarnessOverrideInactive, "its override is saved but ignored")
	assert.Nil(t, byName["overridden"].Variant)
	assert.False(t, byName["overridden"].HarnessOverrideInactive)
	assert.Equal(t, int64(4), byName["idle"].ConfigRevision, "the defaults apply to an agent that has never run")
	assert.Nil(t, byName["idle"].Session)

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents/"+stale.ID.String(), nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var one agent.AgentBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &one))
	assert.Equal(t, agent.ConfigStale, one.Session.ConfigStatus, "a single agent is marked the same way")
}

func TestSetAgentDisabled_TogglesDisabledAt(t *testing.T) {
	mux, agentStore, _ := newTestMux()
	created, err := agentStore.Create(context.Background(), uuid.New(), "flappable", agent.KindWorker)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPatch, "/api/agents/"+created.ID.String(),
		strings.NewReader(`{"disabled": true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var body agent.AgentBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotNil(t, body.DisabledAt)
}

// fakeReleaser stands in for *ticket.Service. Disabling an agent releases
// its leases through that seam; these handler tests only care that the
// agents row flips, and the real release path is covered against a real
// database in internal/ticket.
type fakeReleaser struct {
	released []uuid.UUID
}

func (f *fakeReleaser) ReleaseLeasesFor(_ context.Context, agentID uuid.UUID, _ string, _ time.Time) error {
	f.released = append(f.released, agentID)
	return nil
}

// A session whose workspace installed another bundle than the one served now
// is marked stale, and one that never said is unknown, not stale.
func TestAgents_MarkASessionWhoseWorkspaceBundleIsOlder(t *testing.T) {
	agentStore := newFakeAgentStore()
	ctx := context.Background()
	create := func(name string) *agent.Agent {
		created, err := agentStore.Create(ctx, uuid.New(), name, agent.KindWorker)
		require.NoError(t, err)
		return created
	}
	current, stale, silent := create("current"), create("stale"), create("silent")
	sessions := &fakeSessions{current: map[uuid.UUID]*session.Session{
		current.ID: {AgentID: current.ID, Description: session.Description{BundleHash: workspace.BundleHash()}},
		stale.ID:   {AgentID: stale.ID, Description: session.Description{BundleHash: "0000000000000000"}},
		silent.ID:  {AgentID: silent.ID},
	}}
	handler := agent.NewHandler(&agent.HandlerDeps{
		Agents:        agent.NewService(agentStore, &fakeReleaser{}, &fakeGrants{}, slog.DiscardHandler),
		Roles:         agent.NewRoleService(noRoles{}, slog.DiscardHandler),
		Tickets:       &fakeAgentTicketStore{leases: map[uuid.UUID]*ticket.Lease{}},
		Sessions:      sessions,
		HarnessConfig: &fakeHarness{tenant: &harnessconfig.Tenant{}},
	}, slog.DiscardHandler)
	mux := http.NewServeMux()
	agent.RegisterRoutes(humago.New(mux, huma.DefaultConfig("test", "0.0.0")), handler)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var list struct {
		Agents []agent.AgentBody `json:"agents"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	got := map[string]*agent.SessionBody{}
	for _, a := range list.Agents {
		got[a.Name] = a.Session
	}
	require.NotNil(t, got["current"])
	assert.Equal(t, workspace.BundleCurrent, got["current"].BundleStatus)
	assert.Equal(t, workspace.BundleStale, got["stale"].BundleStatus)
	assert.Equal(t, "0000000000000000", got["stale"].BundleHash)
	assert.Equal(t, workspace.BundleUnknown, got["silent"].BundleStatus)
}

// stewardReportsOnly serves stored steward reports and nothing else; the
// store's behaviour is tested against the real database in steward_observed_test.go.
type stewardReportsOnly struct {
	agent.ProvisionStore
	reports map[uuid.UUID]agent.ObservedSteward
}

func (s stewardReportsOnly) StewardReports(context.Context) (map[uuid.UUID]agent.ObservedSteward, error) {
	return s.reports, nil
}

func TestListAgents_ShowsTheStewardsObservedReport(t *testing.T) {
	agentStore := newFakeAgentStore()
	agentSvc := agent.NewService(agentStore, &fakeReleaser{}, &fakeGrants{}, slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	stew, err := agentStore.Create(ctx, uuid.New(), "steward", agent.KindSteward)
	require.NoError(t, err)
	_, err = agentStore.Create(ctx, uuid.New(), "worker", agent.KindWorker)
	require.NoError(t, err)

	at := time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)
	provision := agent.NewProvisionService(stewardReportsOnly{reports: map[uuid.UUID]agent.ObservedSteward{
		stew.ID: {Phase: agent.PhaseFailed, Reason: "sandbox probe refused `network`", ReportedAt: at},
	}}, agentSvc, nil, nil, slog.DiscardHandler)
	handler := agent.NewHandler(&agent.HandlerDeps{
		Agents:    agentSvc,
		Roles:     agent.NewRoleService(noRoles{}, slog.DiscardHandler),
		Tickets:   &fakeAgentTicketStore{leases: map[uuid.UUID]*ticket.Lease{}},
		Sessions:  &fakeSessions{current: map[uuid.UUID]*session.Session{}},
		Provision: provision,
	}, slog.DiscardHandler)
	mux := http.NewServeMux()
	agent.RegisterRoutes(humago.New(mux, huma.DefaultConfig("test", "0.0.0")), handler)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Agents []agent.AgentBody `json:"agents"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	for _, a := range body.Agents {
		if a.Name == "steward" {
			require.NotNil(t, a.Observed)
			assert.Equal(t, "failed", a.Observed.Phase)
			assert.Equal(t, "sandbox probe refused `network`", a.Observed.Reason)
			assert.True(t, at.Equal(a.Observed.ReportedAt))
		} else {
			assert.Nil(t, a.Observed)
		}
	}
}

// The roster says which supervisor runs a worker, so the Workers page can
// group by machine without a request per supervisor.
func TestListAgents_NamesTheSupervisorThatRunsAWorker(t *testing.T) {
	mux, agentStore, _ := newTestMux()
	ctx := context.Background()
	sup, err := agentStore.Create(ctx, uuid.New(), "machine", agent.KindWorker)
	require.NoError(t, err)
	slot, err := agentStore.Create(ctx, uuid.New(), "slot", agent.KindWorker)
	require.NoError(t, err)
	agentStore.runBy(slot.ID, sup.ID)
	_, err = agentStore.Create(ctx, uuid.New(), "by-hand", agent.KindWorker)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var list struct {
		Agents []agent.AgentBody `json:"agents"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	byName := map[string]agent.AgentBody{}
	for _, a := range list.Agents {
		byName[a.Name] = a
	}
	require.NotNil(t, byName["slot"].SupervisorAgentID)
	assert.Equal(t, sup.ID.String(), *byName["slot"].SupervisorAgentID)
	assert.Nil(t, byName["by-hand"].SupervisorAgentID)
}
