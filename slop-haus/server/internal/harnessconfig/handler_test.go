package harnessconfig_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

	"github.com/lowaa/slop-factory/go-auth/authn/authntest"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit/audittest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/harnessconfig"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

// tenantRoles is the user store's own membership rules, as the conductor
// service takes them.
type tenantRoles struct{ store *user.PostgresStore }

func (r tenantRoles) IsTenantOwner(ctx context.Context, userID, tenantID uuid.UUID) (bool, error) {
	return r.store.IsOwner(ctx, userID, tenantID)
}

func (r tenantRoles) IsTenantMember(ctx context.Context, userID, tenantID uuid.UUID) (bool, error) {
	return r.store.IsMember(ctx, userID, tenantID)
}

// noReleaser is a tenant whose agents hold no leases.
type noReleaser struct{}

func (noReleaser) ReleaseLeasesFor(context.Context, uuid.UUID, string, time.Time) error { return nil }

// wire is the harness config routes over HTTP, behind the same auth
// middlewares cmd/api installs, in the same order, against the real store.
// Tenant A has a person and two agents (builder, reviewer) with bearer
// tokens; tenant B has a person and an agent of its own.
type wire struct {
	database *db.DB
	svc      *harnessconfig.Service
	mux      *http.ServeMux
	events   *audittest.Counter
	auditLog *bytes.Buffer
	builder  *agent.Agent
	reviewer *agent.Agent
	outsider *agent.Agent
	userA    *authn.User
	// conductor belongs to conductorOwner; otherMember is another member of
	// tenant A, and tenantOwner owns it.
	conductor                                *agent.Agent
	conductors                               *agent.ConductorService
	tenantCtx                                context.Context
	conductorOwner, otherMember, tenantOwner *authn.User
}

const (
	userAToken = "user-a"
	userBToken = "user-b"

	conductorOwnerToken = "conductor-owner"
	otherMemberToken    = "other-member"
	tenantOwnerToken    = "tenant-owner"
	builderToken        = "agent-builder"
	reviewerToken       = "agent-reviewer"
	outsiderToken       = "agent-outsider"
	conductorToken      = "agent-conductor"
)

func newWire(t *testing.T) *wire {
	t.Helper()
	database := dbtest.WithDB(t)
	tenantA, tenantB := dbtest.NewTenant(t, database), dbtest.NewTenant(t, database)
	agents := agent.NewService(agent.NewPostgresStore(database), noReleaser{}, nil, slog.DiscardHandler)
	create := func(tenant uuid.UUID, name string) *agent.Agent {
		created, err := agents.Create(db.WithTenant(context.Background(), tenant), name)
		require.NoError(t, err)
		return created
	}
	w := &wire{
		database: database,
		builder:  create(tenantA, "builder"),
		reviewer: create(tenantA, "reviewer"),
		outsider: create(tenantB, "outsider"),
		userA:    &authn.User{ID: uuid.New(), TenantID: tenantA},
	}
	userB := &authn.User{ID: uuid.New(), TenantID: tenantB}

	member := func(role string) *authn.User {
		id := dbtest.NewUser(t, database)
		_, err := database.Global().Exec(context.Background(),
			`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES ($1, $2, $3, $4)`,
			uuid.New(), tenantA, id, role)
		require.NoError(t, err)
		return &authn.User{ID: id, TenantID: tenantA, AuthTime: time.Now()}
	}
	w.conductorOwner, w.otherMember, w.tenantOwner = member("member"), member("member"), member("owner")
	conductors := agent.NewConductorService(agents, tenantRoles{user.NewPostgresStore(database)}, slog.DiscardHandler)
	w.conductors, w.tenantCtx = conductors, db.WithTenant(context.Background(), tenantA)
	var err error
	w.conductor, err = conductors.Register(w.tenantCtx, w.conductorOwner, "my-conductor")
	require.NoError(t, err)

	w.events = audittest.NewCounter()
	w.auditLog = &bytes.Buffer{}
	recorder := audit.New(slog.NewJSONHandler(w.auditLog, nil), w.events)
	svc := harnessconfig.NewService(harnessconfig.NewPostgresStore(database), recorder, slog.DiscardHandler,
		harnessconfig.WithOverrideGate(conductors))
	w.svc = svc
	w.mux = http.NewServeMux()
	api := humago.New(w.mux, huma.DefaultConfig("test", "0.0.0"))
	logger := slog.New(slog.DiscardHandler)
	api.UseMiddleware(authn.DefaultDeny(api, logger))
	api.UseMiddleware(authn.HumaMiddleware(api, func(_ context.Context, token string) (authn.Identity, error) {
		switch token {
		case builderToken:
			return w.builder, nil
		case reviewerToken:
			return w.reviewer, nil
		case outsiderToken:
			return w.outsider, nil
		case conductorToken:
			return w.conductor, nil
		}
		return nil, errors.New("unknown agent token")
	}, audit.New(slog.DiscardHandler, nil), logger))
	api.UseMiddleware(authn.UserMiddleware(api, func(_ context.Context, token string) (*authn.User, error) {
		switch token {
		case userAToken:
			return w.userA, nil
		case userBToken:
			return userB, nil
		case conductorOwnerToken:
			return w.conductorOwner, nil
		case otherMemberToken:
			return w.otherMember, nil
		case tenantOwnerToken:
			return w.tenantOwner, nil
		}
		return nil, errors.New("unknown user token")
	}, authntest.Guard(), logger))
	harnessconfig.RegisterRoutes(api, harnessconfig.NewHandler(svc, slog.DiscardHandler))
	return w
}

func (w *wire) do(t *testing.T, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, req)
	return rec
}

func (w *wire) saveDefaults(t *testing.T, settings string) harnessconfig.ScopeBody {
	t.Helper()
	rec := w.do(t, http.MethodPut, "/api/harness_config", userAToken, `{"settings":`+settings+`}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body harnessconfig.ScopeBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body
}

func (w *wire) saveOverride(t *testing.T, agentID uuid.UUID, settings string) harnessconfig.ScopeBody {
	t.Helper()
	rec := w.do(t, http.MethodPut, "/api/agents/"+agentID.String()+"/harness_config", userAToken, `{"settings":`+settings+`}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body harnessconfig.ScopeBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body
}

// own is what run_remote_control.sh would fetch with token: the status, and the body when
// there is one.
func (w *wire) own(t *testing.T, token string) (int, *harnessconfig.WorkspaceHarnessConfigBody) {
	t.Helper()
	rec := w.do(t, http.MethodGet, "/api/workspace/harness_config", token, "")
	if rec.Code != http.StatusOK {
		return rec.Code, nil
	}
	var body harnessconfig.WorkspaceHarnessConfigBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return rec.Code, &body
}

func TestWorkspaceConfig_NothingSavedIsNoContent(t *testing.T) {
	w := newWire(t)
	status, _ := w.own(t, builderToken)
	assert.Equal(t, http.StatusNoContent, status)
}

// The acceptance case: set the fleet on Sonnet once, and every agent's
// launcher fetches it; set it back, and the next fetch says so.
func TestWorkspaceConfig_DefaultsReachEveryAgent(t *testing.T) {
	w := newWire(t)
	saved := w.saveDefaults(t, `{"claude":{"model":"sonnet","subagent_model":"sonnet"},"pi":{"model":"claude-sonnet-4-5"}}`)
	assert.Nil(t, saved.AgentID)
	require.NotNil(t, saved.UpdatedBy)
	assert.Equal(t, w.userA.ID.String(), *saved.UpdatedBy)

	for _, token := range []string{builderToken, reviewerToken} {
		status, got := w.own(t, token)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, saved.Revision, got.Revision)
		assert.Equal(t, harnessconfig.Values{
			"claude": {"model": "sonnet", "subagent_model": "sonnet"},
			"codex":  {},
			"pi":     {"model": "claude-sonnet-4-5"},
		}, got.Settings)
	}

	back := w.saveDefaults(t, `{"claude":{"model":"opus"}}`)
	status, got := w.own(t, builderToken)
	require.Equal(t, http.StatusOK, status)
	assert.Greater(t, back.Revision, saved.Revision)
	assert.Equal(t, back.Revision, got.Revision)
	assert.Equal(t, harnessconfig.Values{"claude": {"model": "opus"}, "codex": {}, "pi": {}}, got.Settings,
		"a save replaces the whole scope: what it leaves out is unset")
}

func TestWorkspaceConfig_AnOverrideAppliesToThatAgentOnly(t *testing.T) {
	w := newWire(t)
	w.saveDefaults(t, `{"claude":{"model":"sonnet","effort":"high"}}`)
	override := w.saveOverride(t, w.builder.ID, `{"claude":{"model":"opus"}}`)
	require.NotNil(t, override.AgentID)
	assert.Equal(t, w.builder.ID.String(), *override.AgentID)

	_, builder := w.own(t, builderToken)
	assert.Equal(t, map[string]string{"model": "opus", "effort": "high"}, builder.Settings["claude"])
	assert.Equal(t, override.Revision, builder.Revision)

	_, reviewer := w.own(t, reviewerToken)
	assert.Equal(t, map[string]string{"model": "sonnet", "effort": "high"}, reviewer.Settings["claude"])
	assert.Less(t, reviewer.Revision, override.Revision,
		"another agent's override must not move this one's revision, or it would read as stale")
}

// Tenant B has saved nothing; tenant A's config is not the server's.
func TestWorkspaceConfig_IsPerTenant(t *testing.T) {
	w := newWire(t)
	w.saveDefaults(t, `{"claude":{"model":"sonnet"}}`)

	status, _ := w.own(t, outsiderToken)
	assert.Equal(t, http.StatusNoContent, status)

	rec := w.do(t, http.MethodGet, "/api/harness_config", userBToken, "")
	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Defaults  *harnessconfig.ScopeBody `json:"defaults"`
		Overrides []harnessconfig.ScopeBody
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Nil(t, body.Defaults)
	assert.Empty(t, body.Overrides)
}

// Only a person may read or set config. An agent's token opens nothing
// but its own effective config, and names no agent to read another's.
func TestAuthorisation_AnAgentTokenCanNeitherReadNorSetConfig(t *testing.T) {
	w := newWire(t)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/harness_config", ""},
		{http.MethodPut, "/api/harness_config", `{"settings":{"claude":{"model":"sonnet"}}}`},
		{http.MethodPut, "/api/agents/" + w.builder.ID.String() + "/harness_config", `{"settings":{"claude":{"model":"sonnet"}}}`},
		{http.MethodPut, "/api/agents/" + w.reviewer.ID.String() + "/harness_config", `{"settings":{"claude":{"model":"sonnet"}}}`},
	} {
		for _, token := range []string{builderToken, ""} {
			rec := w.do(t, tc.method, tc.path, token, tc.body)
			assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s with %q: %s", tc.method, tc.path, token, rec.Body.String())
		}
	}
	status, _ := w.own(t, builderToken)
	assert.Equal(t, http.StatusNoContent, status, "none of those may have saved anything")
	assert.Zero(t, w.events.Count(audit.HarnessConfigChanged))

	// A person's session is not an agent: the workspace route is refused
	// to it, so it cannot stand in for one.
	rec := w.do(t, http.MethodGet, "/api/workspace/harness_config", userAToken, "")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	// Asking for another agent's config by name has nowhere to go.
	rec = w.do(t, http.MethodGet, "/api/workspace/harness_config?agent_id="+w.reviewer.ID.String(), builderToken, "")
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestSave_RefusesUnknownSettingsAndBadValues(t *testing.T) {
	w := newWire(t)
	for _, settings := range []string{
		`{"claude":{"ANTHROPIC_MODEL":"sonnet"}}`,
		`{"claude":{"model":"--dangerously-skip-permissions"}}`,
		`{"claude":{"model":"sonnet; curl evil"}}`,
		`{"claude":{"effort":"ultra"}}`,
		`{"pi":{"provider":"x y"}}`,
		`{"SLOP_HAUS":{"URL":"http://evil"}}`,
	} {
		rec := w.do(t, http.MethodPut, "/api/harness_config", userAToken, `{"settings":`+settings+`}`)
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "%s: %s", settings, rec.Body.String())
	}
	status, _ := w.own(t, builderToken)
	assert.Equal(t, http.StatusNoContent, status, "a refused save stores nothing")
}

func TestSave_AnOverrideForAnotherTenantsAgentIsNotFound(t *testing.T) {
	w := newWire(t)
	rec := w.do(t, http.MethodPut, "/api/agents/"+w.outsider.ID.String()+"/harness_config", userAToken,
		`{"settings":{"claude":{"model":"sonnet"}}}`)
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	status, _ := w.own(t, outsiderToken)
	assert.Equal(t, http.StatusNoContent, status)

	rec = w.do(t, http.MethodPut, "/api/agents/"+uuid.NewString()+"/harness_config", userAToken, `{"settings":{}}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	rec = w.do(t, http.MethodPut, "/api/agents/not-a-uuid/harness_config", userAToken, `{"settings":{}}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

// Every changed setting is on record with who changed it; clearing a
// scope still moves the revision, so sessions started on the old value
// show as behind.
func TestSave_RecordsEachChangeAndClearingStillMovesTheRevision(t *testing.T) {
	w := newWire(t)
	first := w.saveDefaults(t, `{"claude":{"model":"sonnet","effort":"high"}}`)
	assert.Equal(t, 2, w.events.Count(audit.HarnessConfigChanged))

	second := w.saveDefaults(t, `{"claude":{"model":"sonnet","effort":"low"}}`)
	assert.Equal(t, 3, w.events.Count(audit.HarnessConfigChanged), "only the setting that changed")

	cleared := w.saveDefaults(t, `{}`)
	assert.Equal(t, 5, w.events.Count(audit.HarnessConfigChanged))

	// Each event says who, where, which setting, and from what to what.
	type event struct {
		Action, UserID, Tenant, Scope, Harness, Setting, Old, New string
		Revision                                                  int64
	}
	var events []event
	for _, line := range strings.Split(strings.TrimSpace(w.auditLog.String()), "\n") {
		var raw map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &raw), line)
		str := func(k string) string { v, _ := raw[k].(string); return v }
		revision, _ := raw["revision"].(float64)
		events = append(events, event{
			Action: str("action"), UserID: str("user_id"), Tenant: str("tenant"), Scope: str("scope"),
			Harness: str("harness"), Setting: str("setting"), Old: str("old"), New: str("new"), Revision: int64(revision),
		})
	}
	tenant := w.userA.TenantID.String()
	user := w.userA.ID.String()
	assert.Equal(t, []event{
		{"harness_config_changed", user, tenant, "defaults", "claude", "model", "", "sonnet", first.Revision},
		{"harness_config_changed", user, tenant, "defaults", "claude", "effort", "", "high", first.Revision},
		{"harness_config_changed", user, tenant, "defaults", "claude", "effort", "high", "low", second.Revision},
		{"harness_config_changed", user, tenant, "defaults", "claude", "model", "sonnet", "", cleared.Revision},
		{"harness_config_changed", user, tenant, "defaults", "claude", "effort", "low", "", cleared.Revision},
	}, events)

	override := w.saveOverride(t, w.reviewer.ID, `{"pi":{"thinking":"high"}}`)
	assert.Contains(t, w.auditLog.String(), `"scope":"`+w.reviewer.ID.String()+`"`)
	assert.Contains(t, w.auditLog.String(), fmt.Sprintf(`"revision":%d`, override.Revision))
	assert.Less(t, first.Revision, second.Revision)
	assert.Less(t, second.Revision, cleared.Revision)

	status, got := w.own(t, builderToken)
	require.Equal(t, http.StatusOK, status, "cleared is not never-set: the revision still has to reach the launcher")
	assert.Equal(t, cleared.Revision, got.Revision)
	assert.Equal(t, harnessconfig.Values{"claude": {}, "codex": {}, "pi": {}}, got.Settings)
}

func TestGetHarnessConfig_ListsDefinitionsDefaultsAndOverrides(t *testing.T) {
	w := newWire(t)
	w.saveDefaults(t, `{"claude":{"model":"sonnet"}}`)
	w.saveOverride(t, w.reviewer.ID, `{"pi":{"thinking":"high"}}`)

	rec := w.do(t, http.MethodGet, "/api/harness_config", userAToken, "")
	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Definitions []harnessconfig.DefinitionBody `json:"definitions"`
		Defaults    *harnessconfig.ScopeBody       `json:"defaults"`
		Overrides   []harnessconfig.ScopeBody      `json:"overrides"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Len(t, body.Definitions, len(harnessconfig.Definitions()))
	require.NotNil(t, body.Defaults)
	assert.Equal(t, "sonnet", body.Defaults.Settings["claude"]["model"])
	require.Len(t, body.Overrides, 1)
	assert.Equal(t, w.reviewer.ID.String(), *body.Overrides[0].AgentID)
	assert.Equal(t, "high", body.Overrides[0].Settings["pi"]["thinking"])
}

// A conductor runs on its owner's machine and spends on their subscription,
// so its config is its owner's alone: another member and the tenant's owner
// are refused, while the same people may set a worker's.
func TestSave_AConductorsOverrideIsItsOwnersAlone(t *testing.T) {
	w := newWire(t)
	path := "/api/agents/" + w.conductor.ID.String() + "/harness_config"
	body := `{"settings":{}}`

	for name, token := range map[string]string{"another member": otherMemberToken, "the tenant's owner": tenantOwnerToken} {
		rec := w.do(t, http.MethodPut, path, token, body)
		assert.Equal(t, http.StatusForbidden, rec.Code, "%s: %s", name, rec.Body.String())
	}
	assert.Equal(t, http.StatusOK, w.do(t, http.MethodPut, path, conductorOwnerToken, body).Code, "its owner")

	workerPath := "/api/agents/" + w.builder.ID.String() + "/harness_config"
	for _, token := range []string{otherMemberToken, tenantOwnerToken} {
		assert.Equal(t, http.StatusOK, w.do(t, http.MethodPut, workerPath, token, body).Code, "any member may set a worker's")
	}
}

func (w *wire) conductorPath(suffix string) string {
	return "/api/agents/" + w.conductor.ID.String() + "/harness_config" + suffix
}

// A conductor with no override of its own follows the tenant's defaults as
// they are at each fetch, the rule a worker already has.
func TestWorkspaceConfig_AConductorWithNoOverrideFollowsTheDefaultsLive(t *testing.T) {
	w := newWire(t)

	status, _ := w.own(t, conductorToken)
	assert.Equal(t, http.StatusNoContent, status, "no defaults and no override: nothing to apply, as for a worker")

	w.saveDefaults(t, `{"claude":{"model":"opus"}}`)
	status, body := w.own(t, conductorToken)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, map[string]string{"model": "opus"}, body.Settings["claude"])

	w.saveDefaults(t, `{"claude":{"model":"haiku","effort":"low"}}`)
	_, body = w.own(t, conductorToken)
	assert.Equal(t, map[string]string{"model": "haiku", "effort": "low"}, body.Settings["claude"], "a later change to the defaults reaches it")
}

func TestWorkspaceConfig_AConductorsOverrideWinsPerSettingOverTheDefaults(t *testing.T) {
	w := newWire(t)
	w.saveDefaults(t, `{"claude":{"model":"opus","effort":"high"}}`)
	rec := w.do(t, http.MethodPut, w.conductorPath(""), conductorOwnerToken, `{"settings":{"claude":{"model":"sonnet"}}}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	_, body := w.own(t, conductorToken)
	assert.Equal(t, map[string]string{"model": "sonnet", "effort": "high"}, body.Settings["claude"])

	w.saveDefaults(t, `{"claude":{"model":"haiku","effort":"low"}}`)
	_, body = w.own(t, conductorToken)
	assert.Equal(t, map[string]string{"model": "sonnet", "effort": "low"}, body.Settings["claude"], "the overridden setting stays, the rest follows")
}

func TestRemoveOverride_ReturnsAConductorToTheDefaults(t *testing.T) {
	w := newWire(t)
	w.saveDefaults(t, `{"claude":{"model":"opus"}}`)
	rec := w.do(t, http.MethodPut, w.conductorPath(""), conductorOwnerToken, `{"settings":{"claude":{"model":"sonnet"},"pi":{"model":"m"}}}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = w.do(t, http.MethodDelete, w.conductorPath(""), conductorOwnerToken, "")
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	_, body := w.own(t, conductorToken)
	assert.Equal(t, map[string]string{"model": "opus"}, body.Settings["claude"])
	assert.Empty(t, body.Settings["pi"])

	rec = w.do(t, http.MethodGet, "/api/harness_config", userAToken, "")
	var cfg struct {
		Overrides []harnessconfig.ScopeBody `json:"overrides"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &cfg))
	assert.Empty(t, cfg.Overrides, "no override is left, rather than an empty one")

	w.saveDefaults(t, `{"claude":{"model":"haiku"}}`)
	_, body = w.own(t, conductorToken)
	assert.Equal(t, map[string]string{"model": "haiku"}, body.Settings["claude"], "and it follows from then on")

	rec = w.do(t, http.MethodDelete, w.conductorPath(""), conductorOwnerToken, "")
	assert.Equal(t, http.StatusNoContent, rec.Code, "removing none is a no-op")
}

func TestRemoveOverride_IsTheOwnersAloneForAConductor(t *testing.T) {
	w := newWire(t)
	rec := w.do(t, http.MethodPut, w.conductorPath(""), conductorOwnerToken, `{"settings":{"claude":{"model":"sonnet"}}}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	for name, token := range map[string]string{"another member": otherMemberToken, "the tenant's owner": tenantOwnerToken} {
		rec := w.do(t, http.MethodDelete, w.conductorPath(""), token, "")
		assert.Equal(t, http.StatusForbidden, rec.Code, "%s: %s", name, rec.Body.String())
	}
	_, body := w.own(t, conductorToken)
	assert.Equal(t, map[string]string{"model": "sonnet"}, body.Settings["claude"], "the override survived")
}

// The conductor's own token must not be able to change what it runs.
func TestSaveAndRemove_RefuseAnAgentToken(t *testing.T) {
	w := newWire(t)
	for _, token := range []string{conductorToken, builderToken} {
		for _, method := range []string{http.MethodPut, http.MethodDelete} {
			rec := w.do(t, method, w.conductorPath(""), token, `{"settings":{}}`)
			assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, rec.Code, "%s by %s: %s", method, token, rec.Body.String())
		}
	}
	rec := w.do(t, http.MethodPut, "/api/harness_config", conductorToken, `{"settings":{}}`)
	assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, rec.Code)
}

func TestRemoveOverride_WorksForAWorkerAndForAnUnknownAgent(t *testing.T) {
	w := newWire(t)
	w.saveDefaults(t, `{"claude":{"model":"opus"}}`)
	w.saveOverride(t, w.builder.ID, `{"claude":{"effort":"high"}}`)

	rec := w.do(t, http.MethodDelete, "/api/agents/"+w.builder.ID.String()+"/harness_config", otherMemberToken, "")
	require.Equal(t, http.StatusNoContent, rec.Code, "any member may configure a worker: "+rec.Body.String())
	_, body := w.own(t, builderToken)
	assert.Equal(t, map[string]string{"model": "opus"}, body.Settings["claude"])

	rec = w.do(t, http.MethodDelete, "/api/agents/not-a-uuid/harness_config", userAToken, "")
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	rec = w.do(t, http.MethodDelete, "/api/agents/"+uuid.NewString()+"/harness_config", userAToken, "")
	assert.Equal(t, http.StatusNotFound, rec.Code, "a well-formed id that is no agent of the tenant: "+rec.Body.String())
}

// A removal is a change to what runs: each setting the override held is
// audited as unset, at the revision the removal took, and a repeat is not.
func TestRemoveOverride_AuditsEachSettingAtTheRevisionItTook(t *testing.T) {
	w := newWire(t)
	w.saveOverride(t, w.reviewer.ID, `{"claude":{"model":"sonnet","effort":"high"}}`)
	before := w.events.Count(audit.HarnessConfigChanged)
	path := "/api/agents/" + w.reviewer.ID.String() + "/harness_config"

	rec := w.do(t, http.MethodDelete, path, userAToken, "")
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	assert.Equal(t, before+2, w.events.Count(audit.HarnessConfigChanged))
	log := w.auditLog.String()
	assert.Contains(t, log, `"scope":"`+w.reviewer.ID.String()+`","harness":"claude","setting":"model","old":"sonnet","new":""`)
	assert.Contains(t, log, `"setting":"effort","old":"high","new":""`)

	saved := w.saveDefaults(t, `{"claude":{"model":"opus"}}`)
	assert.Greater(t, saved.Revision, int64(2), "the removal took a revision of its own")

	rec = w.do(t, http.MethodDelete, path, userAToken, "")
	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, before+3, w.events.Count(audit.HarnessConfigChanged), "only the defaults save added one: a repeat removes nothing")
}

func TestSave_TheOwnerConfiguresAndResetsAConductorWhileItIsDisabled(t *testing.T) {
	w := newWire(t)
	_, err := w.conductors.SetDisabled(w.tenantCtx, w.conductorOwner, w.conductor.ID, true)
	require.NoError(t, err)

	rec := w.do(t, http.MethodPut, w.conductorPath(""), conductorOwnerToken, `{"settings":{"claude":{"model":"sonnet"}}}`)
	assert.Equal(t, http.StatusOK, rec.Code, "set before switching it on: "+rec.Body.String())
	rec = w.do(t, http.MethodDelete, w.conductorPath(""), conductorOwnerToken, "")
	assert.Equal(t, http.StatusNoContent, rec.Code, "reset while disabled: "+rec.Body.String())

	for _, token := range []string{otherMemberToken, tenantOwnerToken} {
		rec := w.do(t, http.MethodPut, w.conductorPath(""), token, `{"settings":{}}`)
		assert.Equal(t, http.StatusForbidden, rec.Code, "disabled does not open it to anyone else")
	}
}

// harness_config.updated_by has no foreign key, so deleting its person is the
// delete routine's to clear; what the board then reads says so, without id.
func TestSavedConfigOfADeletedPersonIsFlaggedAndNamesNoId(t *testing.T) {
	w := newWire(t)
	w.saveDefaults(t, `{"claude":{"model":"sonnet"}}`)
	_, err := w.database.Exec(w.tenantCtx, `UPDATE harness_config SET updated_by = $1`, w.otherMember.ID)
	require.NoError(t, err)
	_, err = w.database.Global().Exec(context.Background(), `DELETE FROM global_users WHERE id = $1`, w.otherMember.ID)
	require.NoError(t, err)

	rec := w.do(t, http.MethodGet, "/api/harness_config", userAToken, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), w.otherMember.ID.String())
	var body struct {
		Defaults *harnessconfig.ScopeBody `json:"defaults"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotNil(t, body.Defaults)
	assert.Nil(t, body.Defaults.UpdatedBy)
	assert.True(t, body.Defaults.UpdatedByDeleted)

	// The next save is somebody's again.
	saved := w.saveDefaults(t, `{"claude":{"model":"opus"}}`)
	require.NotNil(t, saved.UpdatedBy)
	assert.Equal(t, w.userA.ID.String(), *saved.UpdatedBy)
	assert.False(t, saved.UpdatedByDeleted)
}

func TestSavedConfigOfALivePersonIsNotFlaggedDeleted(t *testing.T) {
	w := newWire(t)
	saved := w.saveDefaults(t, `{"claude":{"model":"sonnet"}}`)

	require.NotNil(t, saved.UpdatedBy)
	assert.False(t, saved.UpdatedByDeleted)
}
