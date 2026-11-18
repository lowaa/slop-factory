package agent_test

import (
	"context"
	"encoding/json"
	"errors"
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

	"github.com/lowaa/slop-factory/go-auth/auth"
	"github.com/lowaa/slop-factory/go-auth/authn/authntest"
	"github.com/lowaa/slop-factory/go-auth/ratelimit"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit/audittest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/fleetinstructions"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth/oauthtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/agreement"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/supervisorwatch"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

// supervisorAPI is the supervisor's routes behind the real auth middleware,
// real services and a real database: the rules are about who is calling and
// what they own, and each is in a query or a marker.
var registerStewardSettings = sync.OnceValue(func() error { return settings.Register(steward.SettingSpecs()...) })

type supervisorAPI struct {
	*supervisorFixture
	// beforeIssue, when set, runs after the slot has been read as live and
	// before the credential is stored: the window a concurrent disable can
	// land in.
	beforeIssue func(ctx context.Context, slot uuid.UUID)
	svc         *agent.Service
	fleet       kindFleet
	settings    *settings.Service
	oauth       *oauth.Service
	mux         http.Handler
	counts      *audittest.Counter
	watch       *supervisorwatch.Service
	provision   *agent.ProvisionService
}

// hookedCredentials is the real credential issuer with a test's hook in front.
type hookedCredentials struct {
	agent.SlotCredentials
	api *supervisorAPI
}

func (h hookedCredentials) IssueSlotCredential(ctx context.Context, tenantID, supervisorID, slotID uuid.UUID) (*oauth.SlotCredential, error) {
	if h.api.beforeIssue != nil {
		h.api.beforeIssue(ctx, slotID)
	}
	return h.SlotCredentials.IssueSlotCredential(ctx, tenantID, supervisorID, slotID)
}

func newSupervisorAPI(t *testing.T, credentialLimit int) *supervisorAPI {
	t.Helper()
	base := newConsoleFixture(t)
	f := withLogs(t, base)
	grants := &oauthtest.DeferredGrants{}
	a := &supervisorAPI{supervisorFixture: f}
	svc := agent.NewService(agent.NewPostgresStore(f.database), f.tickets, grants, slog.NewJSONHandler(f.logs, nil),
		agent.WithProposalVoider(f.pending), agent.WithSlotCredentials(hookedCredentials{grants, a}))
	f.agents = svc
	generous := &ratelimit.Config{Limit: 1000, Window: time.Minute, MaxKeys: 1000}
	events, counts := audittest.NewRecorder()
	oauthSvc := oauth.NewService(oauth.NewPostgresStore(f.database), svc, &oauth.Config{
		Issuer: "http://x", AccessTTL: time.Hour, RefreshTTL: time.Hour, RegisterLimit: generous, DeviceLookupLimit: generous, DeviceStartLimit: generous,
		SlotCredentialLimit: &ratelimit.Config{Limit: credentialLimit, Window: time.Hour, MaxKeys: 100},
	}, events, slog.DiscardHandler)
	grants.OAuth = oauthSvc

	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	logger := slog.New(slog.DiscardHandler)
	api.UseMiddleware(authn.DefaultDeny(api, logger))
	api.UseMiddleware(authn.HumaMiddleware(api, func(ctx context.Context, token string) (authn.Identity, error) {
		return svc.Authenticate(ctx, token)
	}, events, logger))
	api.UseMiddleware(authn.UserMiddleware(api, func(_ context.Context, token string) (*authn.User, error) {
		if token == "user" {
			return f.user, nil
		}
		return nil, errors.New("unknown")
	}, authntest.Guard(), logger))
	roles := tenantRoles{user.NewPostgresStore(f.database)}
	a.settings = settings.NewService(settings.NewPostgresStore(f.database), events, slog.DiscardHandler, settings.WithOwners(roles))
	require.NoError(t, registerStewardSettings())
	a.watch = supervisorwatch.NewService(supervisorwatch.NewPostgresStore(f.database), nil, nil)
	provision := agent.NewProvisionService(agent.NewPostgresProvisionStore(f.database), svc, roles, a.settings, slog.NewJSONHandler(f.logs, nil), agent.WithSlotReports(a.watch))
	a.provision = provision
	agent.RegisterRoutes(api, agent.NewHandler(&agent.HandlerDeps{Agents: svc, Provision: provision,
		HostLeases: agent.NewHostLeaseService(agent.NewPostgresHostLeaseStore(f.database), svc, 0, slog.DiscardHandler),
		Fleet:      &a.fleet,
		Steward:    steward.NewSupervision(f.database, agreement.NewReporter(agreement.NewScorer(steward.NewStore(f.database), f.database)), steward.NewTenantSettings(a.settings))}, slog.DiscardHandler))
	a.svc, a.oauth, a.mux, a.counts = svc, oauthSvc, mux, counts
	return a
}

// token gives the agent a live access token under a grant approved by the
// fixture's user, and returns it.
func (a *supervisorAPI) token(t *testing.T, who *agent.Agent, scope string) string {
	t.Helper()
	raw, hash, err := auth.NewOpaqueToken()
	require.NoError(t, err)
	grant := uuid.New()
	_, err = a.database.Global().Exec(context.Background(), `
		INSERT INTO global_oauth_grants (id, tenant, agent_id, client_id, resource, approved_by, scope)
		VALUES ($1, $2, $3, 'slop-haus-workspace', 'r', $4, $5)`, grant, a.tenant, who.ID, a.user.ID, scope)
	require.NoError(t, err)
	_, err = a.database.Global().Exec(context.Background(), `
		INSERT INTO global_oauth_tokens (token_hash, grant_id, kind, expires_at) VALUES ($1, $2, 'access', now() + interval '1 hour')`, hash, grant)
	require.NoError(t, err)
	return raw
}

func (a *supervisorAPI) do(method, path, bearer, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	a.mux.ServeHTTP(rec, req)
	return rec
}

func (a *supervisorAPI) conductor(t *testing.T, name string) *agent.Agent {
	t.Helper()
	_, err := a.database.Global().Exec(context.Background(),
		`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES (gen_random_uuid(), $1, $2, 'member')`, a.tenant, a.user.ID)
	require.NoError(t, err)
	id := uuid.New()
	_, err = a.database.Exec(a.ctx, `INSERT INTO agents (id, name, kind, owner_user_id) VALUES ($1, $2, 'conductor', $3)`, id, name, a.user.ID)
	require.NoError(t, err)
	got, err := a.svc.Get(a.ctx, id)
	require.NoError(t, err)
	return got
}

func (a *supervisorAPI) grantCount(t *testing.T, who *agent.Agent) int {
	t.Helper()
	var n int
	require.NoError(t, a.database.Global().QueryRow(context.Background(),
		`SELECT count(*) FROM global_oauth_grants WHERE agent_id = $1`, who.ID).Scan(&n))
	return n
}

func (a *supervisorAPI) slotCount(t *testing.T, sup *agent.Agent) int {
	t.Helper()
	slots, err := a.svc.Slots(a.ctx, sup, false)
	require.NoError(t, err)
	return len(slots)
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return out
}

type slotJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func TestEverySupervisorRouteRefusesWhoIsNotAnEnabledSupervisorWithFullAccess(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	victim := a.supervisor(t, "victim-box", 3)
	victimSlot := a.slot(t, victim, "victim-slot")
	own := a.supervisor(t, "own-box", 3)
	ownSlot := a.slot(t, own, "own-slot")
	worker, err := a.svc.Create(a.ctx, "plain-worker")
	require.NoError(t, err)
	console := a.console(t, "my-console")
	conductor := a.conductor(t, "my-conductor")

	routes := []struct{ method, path, body string }{
		{http.MethodPost, "/api/supervisor/slots", `{"name":"x"}`},
		{http.MethodPost, "/api/supervisor/slots/adopt", `{"name":"plain-worker"}`},
		{http.MethodGet, "/api/supervisor/slots", ``},
		{http.MethodGet, "/api/supervisor/steward/mode", ``},
		{http.MethodDelete, "/api/supervisor/slots/" + ownSlot.ID.String(), ``},
		{http.MethodPost, "/api/supervisor/slots/" + ownSlot.ID.String() + "/credential", ``},
	}
	callers := map[string]string{
		"a worker":    a.token(t, worker, "agent"),
		"a slot":      a.token(t, ownSlot, "agent"),
		"a console":   a.token(t, console, "agent"),
		"a conductor": a.token(t, conductor, "agent"),
		"a supervisor with only a read-only grant": a.token(t, own, "read"),
		"a user's session token":                   "user",
		"no credential":                            "",
		"an unknown token":                         "nope",
	}
	for who, bearer := range callers {
		for _, r := range routes {
			t.Run(who+" "+r.method+" "+r.path, func(t *testing.T) {
				rec := a.do(r.method, r.path, bearer, r.body)
				assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, rec.Code, rec.Body.String())
			})
		}
	}

	assert.Equal(t, 1, a.slotCount(t, own), "nothing was created or retired")
	assert.Equal(t, 1, a.slotCount(t, victim))
	assert.Zero(t, a.grantCount(t, victimSlot)+a.grantCount(t, ownSlot)-1, "no credential was issued; the slot's own token is the one grant")
	assert.Empty(t, a.logs.audit(t, "supervisor_slot_credential_issued"))
	assert.Positive(t, a.counts.Count(audit.SupervisorRouteRefused), "the refusals of agents that are not supervisors are audited")
}

func TestASupervisorWhoIsDisabledOrWhoseGrantsEndedIsRefused(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	disabled := a.supervisor(t, "disabled-box", 2)
	disabledToken := a.token(t, disabled, "agent")
	_, err := a.svc.SetSupervisorDisabled(a.ctx, disabled.ID, true)
	require.NoError(t, err)

	rec := a.do(http.MethodPost, "/api/supervisor/slots", disabledToken, `{"name":"x"}`)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "a switched-off supervisor does not even authenticate")

	revoked := a.supervisor(t, "revoked-box", 2)
	revokedToken := a.token(t, revoked, "agent")
	require.NoError(t, a.oauth.RevokeAgentGrants(a.ctx, a.tenant, revoked.ID))
	rec = a.do(http.MethodPost, "/api/supervisor/slots", revokedToken, `{"name":"x"}`)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestACreatedSlotIsAWorkerOfTheCaller(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	sup := a.supervisor(t, "box", 2)
	bearer := a.token(t, sup, "agent")

	rec := a.do(http.MethodPost, "/api/supervisor/slots", bearer, `{"name":"slot-1"}`)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	created := decode[slotJSON](t, rec)
	slot, err := a.svc.Get(a.ctx, uuid.MustParse(created.ID))
	require.NoError(t, err)
	assert.Equal(t, agent.KindWorker, slot.Kind)
	require.NotNil(t, slot.SupervisorID)
	assert.Equal(t, sup.ID, *slot.SupervisorID)

	lines := a.logs.audit(t, "supervisor_slot_created")
	require.Len(t, lines, 1)
	assert.Equal(t, sup.ID.String(), lines[0]["supervisor"])
	assert.Equal(t, slot.ID.String(), lines[0]["slot"])
}

func TestCreatingASlotCannotChooseAKindOrHitAnyRuleTwice(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	sup := a.supervisor(t, "box", 1)
	bearer := a.token(t, sup, "agent")

	rec := a.do(http.MethodPost, "/api/supervisor/slots", bearer, `{"name":"sneaky","kind":"console"}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "the kind is not a parameter")
	assert.Equal(t, http.StatusUnprocessableEntity, a.do(http.MethodPost, "/api/supervisor/slots", bearer, `{"name":""}`).Code)
	assert.Zero(t, a.slotCount(t, sup))

	require.Equal(t, http.StatusCreated, a.do(http.MethodPost, "/api/supervisor/slots", bearer, `{"name":"one"}`).Code)
	assert.Equal(t, http.StatusConflict, a.do(http.MethodPost, "/api/supervisor/slots", bearer, `{"name":"two"}`).Code, "at the cap")

	other := a.supervisor(t, "other-box", 2)
	rec = a.do(http.MethodPost, "/api/supervisor/slots", a.token(t, other, "agent"), `{"name":"one"}`)
	assert.Equal(t, http.StatusConflict, rec.Code, "the name is taken")
}

func TestAdoptingASlotOverHTTP(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	sup := a.supervisor(t, "box", 1)
	bearer := a.token(t, sup, "agent")
	reused, err := a.svc.Create(a.ctx, "pilot-1")
	require.NoError(t, err)
	_, err = a.svc.Create(a.ctx, "second")
	require.NoError(t, err)
	archived, err := a.svc.Create(a.ctx, "archived")
	require.NoError(t, err)
	_, err = a.database.Exec(a.ctx, `UPDATE agents SET disabled_at = now(), archived_at = now() WHERE id = $1`, archived.ID)
	require.NoError(t, err)
	_, err = a.svc.CreateKind(a.ctx, "a-console", agent.KindConsole)
	require.NoError(t, err)

	assert.Equal(t, http.StatusNotFound, a.do(http.MethodPost, "/api/supervisor/slots/adopt", bearer, `{"name":"nobody"}`).Code)
	assert.Equal(t, http.StatusUnprocessableEntity, a.do(http.MethodPost, "/api/supervisor/slots/adopt", bearer, `{"name":""}`).Code)
	assert.Equal(t, http.StatusConflict, a.do(http.MethodPost, "/api/supervisor/slots/adopt", bearer, `{"name":"archived"}`).Code)
	assert.Equal(t, http.StatusConflict, a.do(http.MethodPost, "/api/supervisor/slots/adopt", bearer, `{"name":"a-console"}`).Code)

	rec := a.do(http.MethodPost, "/api/supervisor/slots/adopt", bearer, `{"name":"pilot-1"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, reused.ID.String(), decode[slotJSON](t, rec).ID)
	assert.Equal(t, 1, a.slotCount(t, sup))
	assert.Equal(t, http.StatusConflict, a.do(http.MethodPost, "/api/supervisor/slots/adopt", bearer, `{"name":"second"}`).Code, "at the cap")
}

func TestListingSlotsIsTheCallersOwnLiveOnes(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	sup := a.supervisor(t, "box", 3)
	other := a.supervisor(t, "other-box", 3)
	kept, retired := a.slot(t, sup, "kept"), a.slot(t, sup, "retired")
	a.slot(t, other, "not-mine")
	_, err := a.svc.RetireSlot(a.ctx, sup, retired.ID)
	require.NoError(t, err)

	rec := a.do(http.MethodGet, "/api/supervisor/slots", a.token(t, sup, "agent"), ``)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decode[struct {
		Slots   []slotJSON `json:"slots"`
		SlotCap int        `json:"slot_cap"`
	}](t, rec)
	require.Len(t, body.Slots, 1)
	assert.Equal(t, kept.ID.String(), body.Slots[0].ID)
	assert.Equal(t, 3, body.SlotCap)
}

func TestCreatingASlotNameHeldByARetiredSlotWithHistoryIs409(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	sup := a.supervisor(t, "box", 3)
	bearer := a.token(t, sup, "agent")
	worked := a.slot(t, sup, "worked")
	a.claim(t, worked)
	_, err := a.svc.RetireSlot(a.ctx, sup, worked.ID)
	require.NoError(t, err)

	rec := a.do(http.MethodPost, "/api/supervisor/slots", bearer, `{"name":"worked"}`)

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "has history")
}

func TestListingSlotsCanIncludeRetiredOnes(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	sup := a.supervisor(t, "box", 3)
	kept, retired := a.slot(t, sup, "kept"), a.slot(t, sup, "retired")
	_, err := a.svc.RetireSlot(a.ctx, sup, retired.ID)
	require.NoError(t, err)

	rec := a.do(http.MethodGet, "/api/supervisor/slots?include_archived=true", a.token(t, sup, "agent"), ``)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decode[struct {
		Slots []struct {
			ID         string     `json:"id"`
			ArchivedAt *time.Time `json:"archived_at"`
		} `json:"slots"`
	}](t, rec)
	byID := map[string]bool{}
	for _, s := range body.Slots {
		byID[s.ID] = s.ArchivedAt != nil
	}
	assert.Equal(t, map[string]bool{kept.ID.String(): false, retired.ID.String(): true}, byID)
}

func TestRetiringASlot(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	sup := a.supervisor(t, "box", 3)
	other := a.supervisor(t, "other-box", 3)
	bearer := a.token(t, sup, "agent")
	mine, theirs := a.slot(t, sup, "mine"), a.slot(t, other, "theirs")
	worker, err := a.svc.Create(a.ctx, "plain-worker")
	require.NoError(t, err)
	slotToken := a.token(t, mine, "agent")

	for name, id := range map[string]uuid.UUID{
		"another supervisor's slot": theirs.ID, "a plain worker": worker.ID, "the supervisor itself": sup.ID, "nothing": uuid.New(),
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, http.StatusNotFound, a.do(http.MethodDelete, "/api/supervisor/slots/"+id.String(), bearer, ``).Code)
		})
	}
	assert.Nil(t, a.get(t, theirs).DisabledAt, "the other supervisor's slot is untouched")
	assert.Nil(t, a.get(t, worker).DisabledAt)
	assert.Nil(t, a.get(t, sup).DisabledAt)
	assert.Equal(t, http.StatusUnprocessableEntity, a.do(http.MethodDelete, "/api/supervisor/slots/not-a-uuid", bearer, ``).Code)

	rec := a.do(http.MethodDelete, "/api/supervisor/slots/"+mine.ID.String(), bearer, ``)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	retired := a.get(t, mine)
	assert.NotNil(t, retired.DisabledAt)
	assert.NotNil(t, retired.ArchivedAt)
	assert.Equal(t, http.StatusUnauthorized, a.do(http.MethodGet, "/api/supervisor/slots", slotToken, ``).Code, "the slot's credential is dead, not merely refused")
	assert.Equal(t, http.StatusOK, a.do(http.MethodDelete, "/api/supervisor/slots/"+mine.ID.String(), bearer, ``).Code, "retiring again succeeds")

	lines := a.logs.audit(t, "supervisor_slot_retired")
	require.NotEmpty(t, lines)
	assert.Equal(t, a.user.ID.String(), lines[0]["approved_by"], "the audit line names the approver")
}

func TestAnIssuedSlotCredentialActsAsTheSlot(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	sup := a.supervisor(t, "box", 2)
	slot := a.slot(t, sup, "slot-1")
	bearer := a.token(t, sup, "agent")

	rec := a.do(http.MethodPost, "/api/supervisor/slots/"+slot.ID.String()+"/credential", bearer, ``)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	tokens := decode[oauth.TokenResponse](t, rec)
	assert.Equal(t, "Bearer", tokens.TokenType)
	assert.NotEmpty(t, tokens.RefreshToken)
	as, err := a.svc.Authenticate(a.ctx, tokens.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, slot.ID, as.ID, "it is the slot's credential")

	lines := a.logs.audit(t, "supervisor_slot_credential_issued")
	require.Len(t, lines, 1)
	assert.Equal(t, sup.ID.String(), lines[0]["supervisor"])
	assert.Equal(t, slot.ID.String(), lines[0]["slot"])
	assert.Equal(t, a.user.ID.String(), lines[0]["approved_by"])
}

func TestASlotCredentialIsOnlyForTheCallersOwnLiveSlots(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	sup := a.supervisor(t, "box", 3)
	other := a.supervisor(t, "other-box", 3)
	bearer := a.token(t, sup, "agent")
	theirs := a.slot(t, other, "theirs")
	retired := a.slot(t, sup, "retired")
	_, err := a.svc.RetireSlot(a.ctx, sup, retired.ID)
	require.NoError(t, err)
	disabled := a.slot(t, sup, "disabled")
	_, err = a.svc.SetDisabled(a.ctx, disabled.ID, true)
	require.NoError(t, err)
	worker, err := a.svc.Create(a.ctx, "plain-worker")
	require.NoError(t, err)
	console := a.console(t, "a-console")

	for name, tc := range map[string]struct {
		target *agent.Agent
		status int
	}{
		"another supervisor's slot": {theirs, http.StatusNotFound},
		"a plain worker":            {worker, http.StatusNotFound},
		"a console":                 {console, http.StatusNotFound},
		"another supervisor":        {other, http.StatusNotFound},
		"the supervisor itself":     {sup, http.StatusNotFound},
		"a retired slot":            {retired, http.StatusNotFound},
		"a disabled slot":           {disabled, http.StatusConflict},
	} {
		t.Run(name, func(t *testing.T) {
			before := a.grantCount(t, tc.target)

			rec := a.do(http.MethodPost, "/api/supervisor/slots/"+tc.target.ID.String()+"/credential", bearer, ``)

			assert.Equal(t, tc.status, rec.Code, rec.Body.String())
			assert.Equal(t, before, a.grantCount(t, tc.target), "nothing was issued")
		})
	}
	assert.Equal(t, http.StatusNotFound, a.do(http.MethodPost, "/api/supervisor/slots/"+uuid.NewString()+"/credential", bearer, ``).Code)
	assert.Empty(t, a.logs.audit(t, "supervisor_slot_credential_issued"))
}

func TestSlotCredentialsAreRateLimited(t *testing.T) {
	a := newSupervisorAPI(t, 2)
	sup := a.supervisor(t, "box", 2)
	slot := a.slot(t, sup, "slot-1")
	bearer := a.token(t, sup, "agent")
	path := "/api/supervisor/slots/" + slot.ID.String() + "/credential"

	assert.Equal(t, http.StatusOK, a.do(http.MethodPost, path, bearer, ``).Code)
	assert.Equal(t, http.StatusOK, a.do(http.MethodPost, path, bearer, ``).Code)
	rec := a.do(http.MethodPost, path, bearer, ``)

	assert.Equal(t, http.StatusTooManyRequests, rec.Code, rec.Body.String())
	assert.Len(t, a.logs.audit(t, "supervisor_slot_credential_issued"), 2, "a refused request is not an issue")
}

func TestAPeopleManageSupervisorsThroughTheirOwnRoutes(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	sup := a.supervisor(t, "box", 3)
	slot := a.slot(t, sup, "slot-1")
	worker, err := a.svc.Create(a.ctx, "plain-worker")
	require.NoError(t, err)

	assert.Equal(t, http.StatusUnauthorized, a.do(http.MethodGet, "/api/supervisors", "", ``).Code)
	assert.Equal(t, http.StatusUnauthorized, a.do(http.MethodGet, "/api/supervisors", a.token(t, worker, "agent"), ``).Code,
		"an agent's credential is not a person's")
	assert.Equal(t, http.StatusUnauthorized, a.do(http.MethodPatch, "/api/supervisors/"+sup.ID.String(), a.token(t, sup, "agent"), `{"disabled":true}`).Code,
		"a supervisor cannot switch itself, or anyone, off through the board's routes")

	rec := a.do(http.MethodGet, "/api/supervisors", "user", ``)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	listed := decode[struct {
		Supervisors []struct {
			ID        string `json:"id"`
			SlotCap   int    `json:"slot_cap"`
			SlotCount int    `json:"slot_count"`
		} `json:"supervisors"`
	}](t, rec)
	require.Len(t, listed.Supervisors, 1)
	assert.Equal(t, sup.ID.String(), listed.Supervisors[0].ID)
	assert.Equal(t, 3, listed.Supervisors[0].SlotCap)
	assert.Equal(t, 1, listed.Supervisors[0].SlotCount)

	assert.Equal(t, http.StatusNotFound, a.do(http.MethodPatch, "/api/supervisors/"+worker.ID.String(), "user", `{"disabled":true}`).Code,
		"this route is for supervisors only")
	assert.Nil(t, a.get(t, worker).DisabledAt)

	rec = a.do(http.MethodPatch, "/api/supervisors/"+sup.ID.String(), "user", `{"disabled":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotNil(t, a.get(t, sup).DisabledAt)
	assert.NotNil(t, a.get(t, slot).DisabledAt, "its slots went with it")
}

// Proposing puts a request in a person's queue; a supervisor, which runs no
// model, has no standing to.
func TestASupervisorCannotPropose(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	sup := a.supervisor(t, "box", 2)
	tk, err := a.tickets.Create(a.ctx, &ticket.CreateParams{Title: "t", Status: ticket.StatusOpen})
	require.NoError(t, err)
	_, err = a.database.Exec(a.ctx, `UPDATE tickets SET status = 'preparing' WHERE id = $1`, tk.ID)
	require.NoError(t, err)

	_, err = a.pending.Propose(a.ctx, &pendingaction.ProposeParams{AgentID: sup.ID, TicketID: tk.ID, Kind: pendingaction.KindReady})

	assert.ErrorIs(t, err, pendingaction.ErrNotConsole)
	may, err := a.svc.MayPropose(a.ctx, sup.ID)
	require.NoError(t, err)
	assert.False(t, may)
}

// A slot disabled between the check and the store would otherwise be left
// with a live grant that comes back to life if it is enabled again.
func TestACredentialIssuedToASlotThatWasJustDisabledIsRevoked(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	sup := a.supervisor(t, "box", 2)
	slot := a.slot(t, sup, "slot-1")
	a.beforeIssue = func(ctx context.Context, id uuid.UUID) {
		_, err := a.svc.SetDisabled(ctx, id, true)
		require.NoError(t, err)
	}

	rec := a.do(http.MethodPost, "/api/supervisor/slots/"+slot.ID.String()+"/credential", a.token(t, sup, "agent"), ``)

	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	var live int
	require.NoError(t, a.database.Global().QueryRow(context.Background(),
		`SELECT count(*) FROM global_oauth_grants WHERE agent_id = $1 AND revoked_at IS NULL`, slot.ID).Scan(&live))
	assert.Zero(t, live)
}

func TestSlotIDsAreTheSlotsThatCanAct(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	sup := a.supervisor(t, "box", 4)
	live, off, retired := a.slot(t, sup, "live"), a.slot(t, sup, "off"), a.slot(t, sup, "retired")
	_, err := a.svc.SetDisabled(a.ctx, off.ID, true)
	require.NoError(t, err)
	_, err = a.svc.RetireSlot(a.ctx, sup, retired.ID)
	require.NoError(t, err)

	ids, err := a.svc.SlotIDs(a.ctx, sup.ID)

	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{live.ID}, ids)
}

func TestListingSupervisorsHidesArchivedOnesUnlessAsked(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	kept, gone := a.supervisor(t, "kept", 1), a.supervisor(t, "gone", 1)
	_, err := a.svc.SetSupervisorDisabled(a.ctx, gone.ID, true)
	require.NoError(t, err)
	_, err = a.database.Exec(a.ctx, `UPDATE agents SET archived_at = now() WHERE id = $1`, gone.ID)
	require.NoError(t, err)
	type listed struct {
		Supervisors []struct {
			ID         string     `json:"id"`
			ArchivedAt *time.Time `json:"archived_at"`
		} `json:"supervisors"`
	}

	hidden := decode[listed](t, a.do(http.MethodGet, "/api/supervisors", "user", ``))
	require.Len(t, hidden.Supervisors, 1)
	assert.Equal(t, kept.ID.String(), hidden.Supervisors[0].ID)

	all := decode[listed](t, a.do(http.MethodGet, "/api/supervisors?include_archived=true", "user", ``))
	require.Len(t, all.Supervisors, 2)
	for _, s := range all.Supervisors {
		assert.Equal(t, s.ID == gone.ID.String(), s.ArchivedAt != nil)
	}
}

// slop-haus-supervisor login installs token.sh into the supervisor's
// directory from this script, so the supervisor's own credential must fetch
// it -- and it is the only worker route that credential is let into besides
// polling.
func TestASupervisorFetchesTheWorkspaceUpdateScriptAsItsOwn(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	sup := a.supervisor(t, "update-box", 2)
	worker, err := a.svc.Create(a.ctx, "update-worker")
	require.NoError(t, err)

	rec := a.do(http.MethodGet, "/api/workspace/update_script", a.token(t, sup, "agent"), ``)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "slop-haus agent: update-box", "rendered for the caller, not another agent")
	assert.Contains(t, rec.Header().Get("Content-Disposition"), "update-box")

	rec = a.do(http.MethodGet, "/api/workspace/update_script", a.token(t, worker, "agent"), ``)
	assert.Equal(t, http.StatusOK, rec.Code, "a worker is served as before")
	assert.Contains(t, rec.Body.String(), "slop-haus agent: update-worker")

	for who, bearer := range map[string]string{"no credential": "", "an unknown token": "nope", "a user's session token": "user"} {
		rec = a.do(http.MethodGet, "/api/workspace/update_script", bearer, ``)
		assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, rec.Code, who)
	}
}

// kindFleet hands back the pinned version the test gives for each kind.
type kindFleet struct {
	pinned map[fleetinstructions.Kind]*fleetinstructions.Version
}

func (k *kindFleet) Default(ctx context.Context) (*fleetinstructions.Version, error) {
	return k.DefaultOf(ctx, fleetinstructions.KindFleet)
}

func (k *kindFleet) DefaultOf(_ context.Context, kind fleetinstructions.Kind) (*fleetinstructions.Version, error) {
	return k.pinned[kind], nil
}

func (k *kindFleet) Get(context.Context, uuid.UUID) (*fleetinstructions.Version, error) {
	return nil, errors.New("kindFleet: Get is not used by the steward briefing")
}

func TestListedSupervisorsSayWhenTheyWereLastSeenAndWhetherThatIsStale(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	fresh, old, ended := a.supervisor(t, "fresh", 1), a.supervisor(t, "old", 1), a.supervisor(t, "ended", 1)
	for _, id := range []uuid.UUID{old.ID, ended.ID} {
		_, err := a.database.Exec(a.ctx, `UPDATE agents SET created_at = now() - interval '25 hours' WHERE id = $1`, id)
		require.NoError(t, err)
	}
	_, err := a.svc.SetSupervisorDisabled(a.ctx, ended.ID, true)
	require.NoError(t, err)
	type listed struct {
		Supervisors []struct {
			Name       string    `json:"name"`
			LastSeenAt time.Time `json:"last_seen_at"`
			Stale      bool      `json:"stale"`
		} `json:"supervisors"`
	}

	got := decode[listed](t, a.do(http.MethodGet, "/api/supervisors", "user", ``))

	stale := map[string]bool{}
	for _, s := range got.Supervisors {
		stale[s.Name] = s.Stale
		assert.False(t, s.LastSeenAt.IsZero())
	}
	assert.Equal(t, map[string]bool{"fresh": false, "old": true, "ended": false}, stale)
	_ = fresh
}
