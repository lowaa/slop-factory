package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/authn/authntest"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/session"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

const (
	userToken  = "user-token"
	agentToken = "agent-token"
)

// roleWire is the role routes over HTTP, behind the same auth middlewares
// cmd/api installs, in the same order, against the real stores. One
// person and one agent of the fixture's tenant a each have a bearer token.
type roleWire struct {
	*roleFixture
	mux     *http.ServeMux
	builder *agent.Agent
}

func newRoleWire(t *testing.T, withUserMiddleware bool) *roleWire {
	t.Helper()
	f := newRoleFixture(t)
	builder := f.agent(t, f.a, "builder")
	user := &authn.User{ID: uuid.New(), TenantID: f.tenantA}

	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	logger := slog.New(slog.DiscardHandler)
	api.UseMiddleware(authn.DefaultDeny(api, logger))
	api.UseMiddleware(authn.HumaMiddleware(api, func(_ context.Context, token string) (authn.Identity, error) {
		if token == agentToken {
			return builder, nil
		}
		return nil, errors.New("unknown agent token")
	}, audit.New(slog.DiscardHandler, nil), logger))
	if withUserMiddleware {
		api.UseMiddleware(authn.UserMiddleware(api, func(_ context.Context, token string) (*authn.User, error) {
			if token == userToken {
				return user, nil
			}
			return nil, errors.New("unknown user token")
		}, authntest.Guard(), logger))
	}
	tickets := ticket.NewService(ticket.NewPostgresStore(f.database), slog.DiscardHandler)
	agent.RegisterRoutes(api, agent.NewHandler(&agent.HandlerDeps{
		Agents:   f.agents,
		Roles:    f.roles,
		Tickets:  tickets,
		Sessions: session.NewService(session.NewPostgresStore(f.database), slog.DiscardHandler),
	}, slog.DiscardHandler))
	return &roleWire{roleFixture: f, mux: mux, builder: builder}
}

func (w *roleWire) send(t *testing.T, method, path, bearer, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, req)
	return rec
}

type roleRoute struct{ method, path, body string }

// every role route, against a real role and agent so that a request which
// got past auth would succeed rather than fail for some other reason.
func (w *roleWire) routes(t *testing.T) []roleRoute {
	t.Helper()
	existing := w.role(t, w.a, "reviewer")
	return []roleRoute{
		{http.MethodGet, "/api/agent-roles", ""},
		{http.MethodPost, "/api/agent-roles", `{"name": "deployer"}`},
		{http.MethodPatch, "/api/agent-roles/" + existing.ID.String(), `{"name": "renamed"}`},
		{http.MethodPut, "/api/agents/" + w.builder.ID.String() + "/roles", `{"role_ids": ["` + existing.ID.String() + `"]}`},
		// Last, so the role is still there for the routes above.
		{http.MethodDelete, "/api/agent-roles/" + existing.ID.String(), ""},
	}
}

// An agent can never manage roles -- its own or anyone's -- because a role
// decides which tickets it may claim. Its token, which the agent routes
// accept, is refused on every role route, and nothing changed.
func TestRoleRoutes_RefuseAnAgentsToken(t *testing.T) {
	w := newRoleWire(t, true)

	for _, route := range w.routes(t) {
		rec := w.send(t, route.method, route.path, agentToken, route.body)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s: %s", route.method, route.path, rec.Body.String())
	}

	listed, err := w.roles.List(w.a)
	require.NoError(t, err)
	assert.Equal(t, []string{"reviewer"}, roleNames(listed), "no role created, renamed or deleted")
	names, err := w.roles.RoleNames(w.a, w.builder.ID)
	require.NoError(t, err)
	assert.Empty(t, names, "the agent gave itself nothing")

	// The token itself works: it is refused for what it is, not for being
	// unknown.
	rec := w.send(t, http.MethodGet, "/api/workspace/update_script", agentToken, "")
	assert.NotEqual(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
}

// And the same routes work for a signed-in person, so the refusal above is
// about who is asking.
func TestRoleRoutes_WorkForASignedInPerson(t *testing.T) {
	w := newRoleWire(t, true)

	for _, route := range w.routes(t) {
		rec := w.send(t, route.method, route.path, userToken, route.body)
		assert.Less(t, rec.Code, 300, "%s %s: %s", route.method, route.path, rec.Body.String())
	}
}

// The handlers refuse a change with no signed-in person behind it, even
// if a route's auth marker were ever wrong and let the request through.
func TestRoleRoutes_RefuseAChangeWithNoSignedInPerson(t *testing.T) {
	w := newRoleWire(t, false)

	for _, route := range w.routes(t) {
		if route.method == http.MethodGet {
			continue
		}
		rec := w.send(t, route.method, route.path, "", route.body)
		assert.Equal(t, http.StatusForbidden, rec.Code, "%s %s: %s", route.method, route.path, rec.Body.String())
	}
	listed, err := w.roles.List(w.a)
	require.NoError(t, err)
	assert.Equal(t, []string{"reviewer"}, roleNames(listed))
}

func TestRoleRoutes_OverTheWire(t *testing.T) {
	w := newRoleWire(t, true)
	idle := w.agent(t, w.a, "idle")

	rec := w.send(t, http.MethodPost, "/api/agent-roles", userToken, `{"name": "reviewer", "description": "reads PRs"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var reviewer agent.RoleBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &reviewer))
	assert.Equal(t, "reads PRs", reviewer.Description)

	rec = w.send(t, http.MethodPost, "/api/agent-roles", userToken, `{"name": "reviewer"}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "name taken: %s", rec.Body.String())
	rec = w.send(t, http.MethodPost, "/api/agent-roles", userToken, `{"name": " "}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "blank name: %s", rec.Body.String())
	rec = w.send(t, http.MethodPatch, "/api/agent-roles/"+uuid.NewString(), userToken, `{"name": "x"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	rec = w.send(t, http.MethodDelete, "/api/agent-roles/not-a-uuid", userToken, "")
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())

	rec = w.send(t, http.MethodPut, "/api/agents/"+w.builder.ID.String()+"/roles", userToken,
		`{"role_ids": ["`+reviewer.ID+`"]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var updated agent.AgentBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &updated))
	require.Len(t, updated.Roles, 1)
	assert.Equal(t, "reviewer", updated.Roles[0].Name)

	rec = w.send(t, http.MethodPut, "/api/agents/"+w.builder.ID.String()+"/roles", userToken,
		`{"role_ids": ["`+uuid.NewString()+`"]}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "unknown role: %s", rec.Body.String())
	rec = w.send(t, http.MethodPut, "/api/agents/"+uuid.NewString()+"/roles", userToken, `{"role_ids": []}`)
	assert.Equal(t, http.StatusNotFound, rec.Code, "unknown agent: %s", rec.Body.String())

	// The roster shows each agent's roles, and [] -- not null -- for none.
	rec = w.send(t, http.MethodGet, "/api/agents", userToken, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list struct {
		Agents []json.RawMessage `json:"agents"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	byName := map[string]agent.AgentBody{}
	raw := map[string]string{}
	for _, entry := range list.Agents {
		var a agent.AgentBody
		require.NoError(t, json.Unmarshal(entry, &a))
		byName[a.Name] = a
		raw[a.Name] = string(entry)
	}
	require.Len(t, byName["builder"].Roles, 1)
	assert.Equal(t, "reviewer", byName["builder"].Roles[0].Name)
	assert.Contains(t, raw[idle.Name], `"roles":[]`)

	rec = w.send(t, http.MethodDelete, "/api/agent-roles/"+reviewer.ID, userToken, "")
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	rec = w.send(t, http.MethodGet, "/api/agents/"+w.builder.ID.String(), userToken, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"roles":[]`, "deleting the role took it from the agent")
}

// Deleting a role tickets require is refused with the tickets named, so
// the board can ask whether to clear it from them; clear_tickets does both.
func TestRoleRoutes_DeletingARoleTicketsRequire(t *testing.T) {
	w := newRoleWire(t, true)
	deploy := w.role(t, w.a, "deploy")
	tickets := ticket.NewService(ticket.NewPostgresStore(w.database), slog.DiscardHandler)
	scoped, err := tickets.Create(w.a, &ticket.CreateParams{
		Title: "Ship it", Roles: ticket.RoleRequirement{IDs: []uuid.UUID{deploy.ID}},
	})
	require.NoError(t, err)

	path := "/api/agent-roles/" + deploy.ID.String()
	rec := w.send(t, http.MethodDelete, path, userToken, "")
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	var refused agent.RoleInUseBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &refused))
	assert.Equal(t, []agent.RoleInUseTicket{{ID: scoped.ID, Title: "Ship it", Status: "open"}}, refused.Tickets)
	assert.Contains(t, refused.Detail, "clear_tickets=true")
	assert.Equal(t, http.StatusConflict, refused.Status)
	assert.Equal(t, agent.RoleInUseCode, refused.Code)

	listed, err := w.roles.List(w.a)
	require.NoError(t, err)
	assert.Equal(t, []string{"deploy"}, roleNames(listed), "refused: still there")

	rec = w.send(t, http.MethodDelete, path+"?clear_tickets=true", userToken, "")
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	got, err := tickets.Get(w.a, scoped.ID)
	require.NoError(t, err)
	assert.Empty(t, got.RequiredRoles, "cleared from the ticket")
	listed, err = w.roles.List(w.a)
	require.NoError(t, err)
	assert.Empty(t, listed, "and the role deleted")

	// An agent's token is refused here as on every role route, clearing or not.
	other := w.role(t, w.a, "other")
	rec = w.send(t, http.MethodDelete, "/api/agent-roles/"+other.ID.String()+"?clear_tickets=true", agentToken, "")
	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
}
