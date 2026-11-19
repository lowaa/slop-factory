package ticket_test

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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// fakeLeaseStore is a ticket.Store of canned answers, for the claim
// routes' refusals that are awkward to set up for real (processing switched
// off, a revocation notice) and for routes that never reach a store at all
// (no credential). It holds no leases and decides nothing: whether a claim
// is granted, a scope conflicts or a lock is held is PostgresStore's, and
// those routes are tested against it through agentMux, so this fake has no
// second copy of those rules to drift.
type fakeLeaseStore struct {
	ticket.TicketStore
	ticket.ServerStore

	// blocked, when set, is what Claim reports -- enough to exercise the
	// "blocked" wire shape without a real conflicting second claim.
	blocked *ticket.Blocked
	// claimErr, when set, is what Claim returns instead -- for the
	// refusals the store decides on, which the handler only has to map to
	// a status.
	claimErr error
	// heartbeatErr is what Heartbeat returns -- standing in for the
	// store's once-only revocation notice.
	heartbeatErr error
}

var errFakeLeaseStore = errors.New("fakeLeaseStore: tested against PostgresStore")

func (s *fakeLeaseStore) Claim(context.Context, uuid.UUID, *ticket.ClaimOptions, time.Duration, ticket.ConfigResolver) (*ticket.ClaimResult, error) {
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	return &ticket.ClaimResult{Blocked: s.blocked}, nil
}

func (s *fakeLeaseStore) RecentRevocation(context.Context, uuid.UUID, time.Duration) (*ticket.RevocationContext, error) {
	return nil, nil
}

func (s *fakeLeaseStore) LeaseEnd(context.Context, int64) (*ticket.LeaseEnd, error) {
	return nil, errFakeLeaseStore
}

func (s *fakeLeaseStore) WorkerSnapshot(context.Context, uuid.UUID) (*ticket.WorkerSnapshot, error) {
	return nil, errFakeLeaseStore
}

func (s *fakeLeaseStore) Heartbeat(context.Context, uuid.UUID, time.Duration) error {
	return s.heartbeatErr
}

func (s *fakeLeaseStore) Peek(context.Context, uuid.UUID, *int64) (*ticket.ClaimResult, error) {
	return nil, errFakeLeaseStore
}

func (s *fakeLeaseStore) QueueBlockage(context.Context) (*ticket.QueueBlockage, error) {
	return nil, errFakeLeaseStore
}

func (s *fakeLeaseStore) ScopeLocking(context.Context) (bool, error) {
	return true, nil
}

func (s *fakeLeaseStore) ExtendScope(context.Context, uuid.UUID, []model.Path, func([]string) error) (*ticket.ExtendResult, error) {
	return nil, errFakeLeaseStore
}

func (s *fakeLeaseStore) NarrowScope(context.Context, uuid.UUID, []string, []string) (*ticket.NarrowResult, error) {
	return nil, errFakeLeaseStore
}

func (s *fakeLeaseStore) AcquireLock(context.Context, uuid.UUID, string) error {
	return errFakeLeaseStore
}

func (s *fakeLeaseStore) AwaitLock(context.Context, uuid.UUID, string) error {
	return errFakeLeaseStore
}

func (s *fakeLeaseStore) LockState(context.Context, string) (*ticket.LockState, error) {
	return nil, errFakeLeaseStore
}

func (s *fakeLeaseStore) LockNames(context.Context) ([]string, error) {
	return nil, errFakeLeaseStore
}

func (s *fakeLeaseStore) ForceReleaseLock(context.Context, string) (*ticket.LockHolder, error) {
	return nil, errFakeLeaseStore
}

func (s *fakeLeaseStore) DropLockWaiter(context.Context, string, uuid.UUID) error {
	return errFakeLeaseStore
}

func (s *fakeLeaseStore) ReleaseLock(context.Context, uuid.UUID) error {
	return errFakeLeaseStore
}

func (s *fakeLeaseStore) LeaseFor(context.Context, uuid.UUID) (*ticket.Lease, error) {
	return nil, errFakeLeaseStore
}

func (s *fakeLeaseStore) HeldScopes(context.Context) ([]ticket.HeldScope, error) {
	return nil, errFakeLeaseStore
}

func (s *fakeLeaseStore) ReleaseLeasesFor(context.Context, uuid.UUID, string, time.Time) error {
	return errFakeLeaseStore
}

func (s *fakeLeaseStore) InTransition(context.Context, func(ticket.TransitionTx) error) error {
	return errFakeLeaseStore
}

func (s *fakeLeaseStore) OnHeldLease(context.Context, uuid.UUID, func(ticket.TransitionTx, *ticket.HeldLease) error) error {
	return errFakeLeaseStore
}

var _ ticket.Store = (*fakeLeaseStore)(nil)

// newLeaseTestMux wires /api/claims and /api/integration_lock exactly as
// cmd/api/main.go does, so these tests exercise the real wire behavior
// (status + JSON shape) rather than just the handler's Go return values.
// testIdentity is the authenticated caller these tests present. The real
// one is *agent.Agent; the ticket package only ever needs the id, and
// depending on agent here would be the cycle authn.Identity exists to
// avoid.
type testIdentity struct{ id uuid.UUID }

func (t testIdentity) AgentID() uuid.UUID  { return t.id }
func (t testIdentity) AgentName() string   { return "test-agent" }
func (t testIdentity) TenantID() uuid.UUID { return testTenantID }
func (t testIdentity) ReadOnly() bool      { return false }

// tokenFor turns an agent id into the bearer token these tests send. The
// fake authenticator below reverses it, so a test can act as any agent
// without a database.
func tokenFor(agentID uuid.UUID) string { return "token-" + agentID.String() }

func authorize(req *http.Request, agentID uuid.UUID) {
	req.Header.Set("Authorization", "Bearer "+tokenFor(agentID))
}

// agentMux wires the ticket routes as cmd/api does, against the fixture's
// real store, for an agent of the fixture's tenant presenting tokenFor(id).
func (f *fixture) agentMux() http.Handler {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(authn.HumaMiddleware(api,
		func(_ context.Context, token string) (authn.Identity, error) {
			raw, ok := strings.CutPrefix(token, "token-")
			if !ok {
				return nil, errors.New("unknown token")
			}
			id, err := uuid.Parse(raw)
			if err != nil {
				return nil, errors.New("unknown token")
			}
			return tenantIdentity{testIdentity{id: id}, f.tenant}, nil
		}, audit.New(slog.DiscardHandler, nil), slog.New(slog.DiscardHandler)))
	ticket.RegisterRoutes(api, ticket.NewHandler(f.svc, slog.DiscardHandler))
	return mux
}

// tenantIdentity is testIdentity in a tenant of its own, for the routes
// that reach a real store, whose rows are scoped by it.
type tenantIdentity struct {
	testIdentity
	tenant uuid.UUID
}

func (t tenantIdentity) TenantID() uuid.UUID { return t.tenant }

func newLeaseTestMux() (*http.ServeMux, *fakeLeaseStore) {
	store := &fakeLeaseStore{}
	svc := ticket.NewService(store, slog.NewTextHandler(io.Discard, nil))
	handler := ticket.NewHandler(svc, slog.DiscardHandler)

	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	// The same middleware cmd/api installs, so these tests exercise the
	// real auth path rather than a route set that happens to be open.
	api.UseMiddleware(authn.HumaMiddleware(api,
		func(_ context.Context, token string) (authn.Identity, error) {
			raw, ok := strings.CutPrefix(token, "token-")
			if !ok {
				return nil, errors.New("unknown token")
			}
			id, err := uuid.Parse(raw)
			if err != nil {
				return nil, errors.New("unknown token")
			}
			return testIdentity{id: id}, nil
		}, audit.New(slog.DiscardHandler, nil), slog.New(slog.DiscardHandler)))
	ticket.RegisterRoutes(api, handler)
	return mux, store
}

func claimReq(agentID uuid.UUID, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/claims", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	authorize(req, agentID)
	return req
}

func TestClaimNext_ReturnsTicket(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	want := f.ticket(t, "fix it", "server/")

	rec := httptest.NewRecorder()
	f.agentMux().ServeHTTP(rec, claimReq(a, `{}`))

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var body ticket.ClaimResponseBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotNil(t, body.Ticket)
	assert.Equal(t, want.ID, body.Ticket.ID)
	assert.Nil(t, body.Blocked)
}

func TestClaimNext_ReturnsTheAttemptAndItsBranchGuidance(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	want := f.ticket(t, "fix it", "server/")

	rec := httptest.NewRecorder()
	f.agentMux().ServeHTTP(rec, claimReq(a, `{}`))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var body ticket.ClaimResponseBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotNil(t, body.Attempt)
	assert.Equal(t, 1, body.Attempt.Number)
	assert.Equal(t, ticket.BranchName(want.ID, 1), body.Attempt.Branch)
	assert.NotEmpty(t, body.Attempt.Guidance, "the claim says what to do with the branch")
	assert.False(t, body.Attempt.Unpromoted)
}

func TestClaimNext_ReturnsTheGuidanceThatAppliesToTheTicket(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	_, err := f.svc.Create(f.ctx, &ticket.CreateParams{
		Title: "Rough idea", NeedsDiscussion: true, Paths: []model.Path{{Path: "server/"}},
	})
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	f.agentMux().ServeHTTP(rec, claimReq(a, `{}`))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var body ticket.ClaimResponseBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotNil(t, body.Guidance)
	assert.Contains(t, body.Guidance.NeedsDiscussion, "NEEDS DISCUSSION")
}

func TestClaimNext_NothingGranted_HasNoAttempt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	rec := httptest.NewRecorder()
	f.agentMux().ServeHTTP(rec, claimReq(f.agent(t, "builder-1"), `{}`))
	require.Equal(t, http.StatusOK, rec.Code)

	var body ticket.ClaimResponseBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Nil(t, body.Attempt)
	assert.Nil(t, body.Guidance)
}

func TestClaimNext_EmptyQueue_ReturnsBothNull(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	rec := httptest.NewRecorder()
	f.agentMux().ServeHTTP(rec, claimReq(f.agent(t, "builder-1"), `{}`))

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var body ticket.ClaimResponseBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Nil(t, body.Ticket)
	assert.Nil(t, body.Blocked)
}

func TestClaimNext_Blocked_NamesTheConflictingTicket(t *testing.T) {
	t.Parallel()
	mux, store := newLeaseTestMux()
	holder := uuid.New()
	store.blocked = &ticket.Blocked{
		TicketID: 2, ConflictingTicketID: 1,
		ConflictingPath: "server/", HolderAgentID: holder,
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, claimReq(uuid.New(), `{}`))

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var body ticket.ClaimResponseBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotNil(t, body.Blocked)
	assert.Nil(t, body.Ticket)
	assert.Equal(t, int64(1), body.Blocked.ConflictingTicketID)
	assert.Equal(t, "server/", body.Blocked.ConflictingPath)
	assert.Equal(t, holder.String(), body.Blocked.HolderAgentID)
}

// No credential at all: 401, not 400. Identity is not a malformed
// field on an otherwise valid request.
func TestClaimNext_NoCredential_Returns401(t *testing.T) {
	t.Parallel()
	mux, _ := newLeaseTestMux()

	req := httptest.NewRequest(http.MethodPost, "/api/claims", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code, "body: %s", rec.Body.String())
}

// An unknown token is answered the same way as a missing one: telling a
// caller which tokens exist is telling it which to keep guessing at.
func TestClaimNext_UnknownToken_Returns401(t *testing.T) {
	t.Parallel()
	mux, _ := newLeaseTestMux()

	req := httptest.NewRequest(http.MethodPost, "/api/claims", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code, "body: %s", rec.Body.String())
}

func TestHeartbeat_NoLease_Returns409(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	rec := httptest.NewRecorder()
	f.agentMux().ServeHTTP(rec, claimRoute(f.agent(t, "builder-1"), "/api/claims/heartbeat", ""))

	assert.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
}

// Extending the lease is the heartbeat's whole job, so with the lease
// revoked the notice is its answer: a 409 carrying the reason verbatim.
func TestHeartbeat_Revoked_Returns409WithTheNotice(t *testing.T) {
	t.Parallel()
	mux, store := newLeaseTestMux()
	store.heartbeatErr = &ticket.LeaseRevokedError{TicketID: 12, Status: ticket.StatusPreparing}

	req := httptest.NewRequest(http.MethodPost, "/api/claims/heartbeat", nil)
	authorize(req, uuid.New())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "#12 was moved to Preparing by a human -- stop working on it")
}

func TestHeartbeat_HeldLease_Returns204(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	f.ticket(t, "fix it", "server/")
	f.claim(t, a)

	rec := httptest.NewRecorder()
	f.agentMux().ServeHTTP(rec, claimRoute(a, "/api/claims/heartbeat", ""))

	assert.Equal(t, http.StatusNoContent, rec.Code, "body: %s", rec.Body.String())
}

func lockRoute(agentID uuid.UUID, method string) *http.Request {
	req := httptest.NewRequest(method, "/api/integration_lock", nil)
	authorize(req, agentID)
	return req
}

func TestLockIntegration_ThenUnlock(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.agentMux()
	a := f.agent(t, "builder-1")
	f.ticket(t, "fix it", "server/")
	f.claim(t, a)

	lockRec := httptest.NewRecorder()
	mux.ServeHTTP(lockRec, lockRoute(a, http.MethodPost))
	require.Equal(t, http.StatusNoContent, lockRec.Code, "body: %s", lockRec.Body.String())

	unlockRec := httptest.NewRecorder()
	mux.ServeHTTP(unlockRec, lockRoute(a, http.MethodDelete))
	assert.Equal(t, http.StatusNoContent, unlockRec.Code, "body: %s", unlockRec.Body.String())
}

func TestLockIntegration_Refused_Returns409NamingTheHolder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.agentMux()
	holder, other := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	f.ticket(t, "holder work", "server/")
	f.ticket(t, "other work", "web/")
	f.claim(t, holder)
	f.claim(t, other)
	first := httptest.NewRecorder()
	mux.ServeHTTP(first, lockRoute(holder, http.MethodPost))
	require.Equal(t, http.StatusNoContent, first.Code)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, lockRoute(other, http.MethodPost))

	require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "builder-1")
	assert.Contains(t, rec.Body.String(), "holder work")
}

func TestUnlockIntegration_NotHeld_Returns404(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	rec := httptest.NewRecorder()
	f.agentMux().ServeHTTP(rec, lockRoute(f.agent(t, "builder-1"), http.MethodDelete))

	assert.Equal(t, http.StatusNotFound, rec.Code, "body: %s", rec.Body.String())
}

// The switch itself is enforced inside the claim transaction, and
// policy_test.go covers it against a real database. What this pins is the
// status it surfaces as: a valid request that conflicts with the server's
// current state, so 409 -- and specifically not a 500, which would read as
// a broken server and get retried as one.
func TestClaimNext_ProcessingDisabled_Returns409(t *testing.T) {
	t.Parallel()
	mux, store := newLeaseTestMux()
	store.claimErr = ticket.ErrProcessingDisabled

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, claimReq(uuid.New(), `{}`))

	require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "processing is disabled",
		"the refusal must name its reason, or an agent reads it as an empty queue")
}

func TestReleaseClaim_ReturnsTicket(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.agentMux()
	a := f.agent(t, "builder-1")
	f.ticket(t, "fix it", "server/")
	f.claim(t, a)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, claimRoute(a, "/api/claims/release", `{"reason": "out of time"}`))

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, ticket.StatusOpen, decodeTicket(t, rec.Body.Bytes()).Status)
}
