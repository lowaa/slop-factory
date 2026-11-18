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
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/authn/authntest"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/session"
)

// conductorAPI serves the agent routes as cmd/api does. A bearer token
// resolves to the user registered under it; any other token is an agent's,
// which resolves to no user.
type conductorAPI struct {
	mux http.Handler
}

func (f *conductorFixture) api(users map[string]*authn.User, tweaks ...func(*agent.HandlerDeps)) *conductorAPI {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	resolve := func(_ context.Context, token string) (*authn.User, error) {
		if u, ok := users[token]; ok {
			return u, nil
		}
		return nil, errors.New("not a user token")
	}
	api.UseMiddleware(authn.UserMiddleware(api, resolve, authntest.Guard(), slog.New(slog.DiscardHandler)))
	deps := &agent.HandlerDeps{
		Agents:     f.agents,
		Conductors: f.conductors,
		Roles:      agent.NewRoleService(agent.NewPostgresRoleStore(f.database), slog.DiscardHandler),
		Tickets:    f.tickets,
		Sessions:   session.NewService(session.NewPostgresStore(f.database), slog.DiscardHandler),
	}
	for _, tweak := range tweaks {
		tweak(deps)
	}
	agent.RegisterRoutes(api, agent.NewHandler(deps, slog.DiscardHandler))
	return &conductorAPI{mux: mux}
}

func (a *conductorAPI) do(token, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	a.mux.ServeHTTP(rec, req)
	return rec
}

func (f *conductorFixture) tokens() map[string]*authn.User {
	return map[string]*authn.User{"admin": f.admin, "owner": f.owner, "other": f.other}
}

func TestRegisteringAConductorOverRESTRecordsTheCallerAsOwnerAndReturnsTheScript(t *testing.T) {
	f := newConductorFixture(t)
	api := f.api(f.tokens())

	rec := api.do("owner", http.MethodPost, "/api/conductors", `{"name":"rest-conductor"}`)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/x-shellscript")
	assert.Contains(t, rec.Body.String(), "rest-conductor")
	own, err := f.conductors.ListOwned(f.ctx, f.owner, false)
	require.NoError(t, err)
	require.Len(t, own, 1)
	assert.Equal(t, f.owner.ID, *own[0].OwnerUserID)
}

func TestAnAgentTokenCannotRegisterOrManageAConductor(t *testing.T) {
	f := newConductorFixture(t)
	c := f.register(t, f.owner, "mine")
	api := f.api(f.tokens())
	id := c.ID.String()

	for name, req := range map[string][3]string{
		"register": {http.MethodPost, "/api/conductors", `{"name":"sneaky"}`},
		"list":     {http.MethodGet, "/api/conductors", ``},
		"disable":  {http.MethodPatch, "/api/conductors/" + id, `{"disabled":true}`},
		"rotate":   {http.MethodPost, "/api/conductors/" + id + "/setup_script", ``},
	} {
		rec := api.do("an-agents-token", req[0], req[1], req[2])
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s: %s", name, rec.Body.String())
	}
	own, err := f.conductors.ListOthers(f.ctx, f.admin, false)
	require.NoError(t, err)
	assert.Len(t, own, 1, "nothing was registered")
	got, err := f.agents.Get(f.ctx, c.ID)
	require.NoError(t, err)
	assert.Nil(t, got.DisabledAt)
	assert.Equal(t, f.owner.ID, *got.OwnerUserID, "and nothing was re-owned")
}

func TestRegisteringAConductorNeedsARecentSignIn(t *testing.T) {
	f := newConductorFixture(t)
	stale := *f.owner
	stale.AuthTime = time.Now().Add(-time.Hour)
	api := f.api(map[string]*authn.User{"stale": &stale})

	rec := api.do("stale", http.MethodPost, "/api/conductors", `{"name":"stale"}`)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), `"code":"reauth_required"`)
}

// Nobody but the owner gets the credential: a new setup script signs out the
// conductor's workspaces and lets a new device sign-in take over.
func TestOnlyTheOwnerCanRotateAConductorsSetupScript(t *testing.T) {
	f := newConductorFixture(t)
	c := f.register(t, f.owner, "mine")
	api := f.api(f.tokens())
	path := "/api/conductors/" + c.ID.String() + "/setup_script"

	for _, who := range []string{"other", "admin"} {
		rec := api.do(who, http.MethodPost, path, ``)
		assert.Equal(t, http.StatusForbidden, rec.Code, who)
	}
	assert.Empty(t, f.grants.revokedAgents(), "nobody else signed its workspaces out")
	assert.Equal(t, http.StatusOK, api.do("owner", http.MethodPost, path, ``).Code, "the owner's sign-out and script")
	assert.Equal(t, []uuid.UUID{c.ID}, f.grants.revokedAgents())
}

func TestDisablingAConductorOverREST(t *testing.T) {
	f := newConductorFixture(t)
	c := f.register(t, f.owner, "mine")
	api := f.api(f.tokens())
	path := "/api/conductors/" + c.ID.String()

	rec := api.do("other", http.MethodPatch, path, `{"disabled":true}`)
	assert.Equal(t, http.StatusForbidden, rec.Code, "another member")

	rec = api.do("admin", http.MethodPatch, path, `{"disabled":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body agent.ConductorBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.NotNil(t, body.DisabledAt)
	assert.False(t, body.OwnedByMe, "the admin sees it is not theirs")

	rec = api.do("owner", http.MethodPatch, path, `{"disabled":false}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestConductorListingOverREST(t *testing.T) {
	f := newConductorFixture(t)
	f.register(t, f.owner, "mine")
	f.register(t, f.other, "theirs")
	api := f.api(f.tokens())

	list := func(token string) (own, others []agent.ConductorBody) {
		rec := api.do(token, http.MethodGet, "/api/conductors", ``)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var body struct{ Conductors, Others []agent.ConductorBody }
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		return body.Conductors, body.Others
	}

	own, others := list("owner")
	require.Len(t, own, 1)
	assert.Equal(t, "mine", own[0].Name)
	assert.True(t, own[0].OwnedByMe)
	require.NotNil(t, own[0].OwnerID)
	assert.Equal(t, f.owner.ID.String(), *own[0].OwnerID)
	assert.False(t, own[0].OwnerDeleted)
	assert.Empty(t, others, "a member does not see everyone's")

	own, others = list("admin")
	assert.Empty(t, own, "an admin's own list is their own")
	require.Len(t, others, 2)
	for _, o := range others {
		assert.False(t, o.OwnedByMe)
	}
}

// The Agents screen's routes take any agent id: a conductor behind one would
// be reachable by every member of the tenant.
func TestWorkerRoutesAnswerNotFoundForAConductor(t *testing.T) {
	f := newConductorFixture(t)
	c := f.register(t, f.owner, "mine")
	api := f.api(f.tokens())
	id := c.ID.String()

	for name, req := range map[string][3]string{
		"get":     {http.MethodGet, "/api/agents/" + id, ``},
		"disable": {http.MethodPatch, "/api/agents/" + id, `{"disabled":true}`},
		"rotate":  {http.MethodPost, "/api/agents/" + id + "/setup_script", ``},
		"roles":   {http.MethodPut, "/api/agents/" + id + "/roles", `{"role_ids":[]}`},
	} {
		rec := api.do("admin", req[0], req[1], req[2])
		assert.Equal(t, http.StatusNotFound, rec.Code, "%s: %s", name, rec.Body.String())
	}
	got, err := f.agents.Get(f.ctx, c.ID)
	require.NoError(t, err)
	assert.Nil(t, got.DisabledAt)

	rec := api.do("admin", http.MethodGet, "/api/agents", ``)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), id, "and the workers' list does not show it")
}

// A conductor's workspace is told how changes land by the same rule as a
// worker's, on registration and on rotation; a credential that cannot be read
// fails the request before anything is created or signed out.
func TestConductorScriptsFollowTheTenantsCodeHostCredential(t *testing.T) {
	f := newConductorFixture(t)
	host := &fakeCodeHost{connected: true}
	api := f.api(f.tokens(), func(d *agent.HandlerDeps) { d.CodeHost = host })

	rec := api.do("owner", http.MethodPost, "/api/conductors", `{"name":"with-host"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), changeToolsLanding)
	own, err := f.conductors.ListOwned(f.ctx, f.owner, false)
	require.NoError(t, err)
	require.Len(t, own, 1)
	path := "/api/conductors/" + own[0].ID.String() + "/setup_script"

	rec = api.do("owner", http.MethodPost, path, ``)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), changeToolsLanding)

	host.connected = false
	rec = api.do("owner", http.MethodPost, path, ``)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), changeToolsLanding)
	assert.Contains(t, rec.Body.String(), ownToolsLanding)

	host.err = errors.New("db down")
	signedOut := len(f.grants.revokedAgents())
	assert.Equal(t, http.StatusInternalServerError, api.do("owner", http.MethodPost, path, ``).Code)
	assert.Len(t, f.grants.revokedAgents(), signedOut, "a script that cannot be built signs nobody out")
	assert.Equal(t, http.StatusInternalServerError,
		api.do("owner", http.MethodPost, "/api/conductors", `{"name":"ghost"}`).Code)
	own, err = f.conductors.ListOwned(f.ctx, f.owner, false)
	require.NoError(t, err)
	assert.Len(t, own, 1, "no conductor was created")
}

// A conductor whose owner's account is gone is still listed, for the tenant's
// owner to disable, and says so rather than carrying the owner's id.
func TestAConductorOfADeletedOwnerIsFlaggedAndNamesNoId(t *testing.T) {
	f := newConductorFixture(t)
	f.register(t, f.owner, "orphaned")
	api := f.api(f.tokens())
	_, err := f.database.Global().Exec(context.Background(), `DELETE FROM global_users WHERE id = $1`, f.owner.ID)
	require.NoError(t, err)

	rec := api.do("admin", http.MethodGet, "/api/conductors", ``)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), f.owner.ID.String())
	var body struct{ Others []agent.ConductorBody }
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Others, 1)
	assert.Nil(t, body.Others[0].OwnerID)
	assert.True(t, body.Others[0].OwnerDeleted)
	assert.False(t, body.Others[0].OwnedByMe)
}

func TestConductorDirectoryOverREST(t *testing.T) {
	f := newConductorFixture(t)
	mine := f.register(t, f.owner, "mine")
	theirs := f.register(t, f.other, "theirs")
	archived := f.register(t, f.other, "archived")
	_, err := f.conductors.SetDisabled(f.ctx, f.other, archived.ID, true)
	require.NoError(t, err)
	_, err = f.conductors.SetArchived(f.ctx, f.other, archived.ID, true)
	require.NoError(t, err)

	otherTenant := dbtest.NewTenant(t, f.database)
	foreign := f.member(t, otherTenant, "member")
	_, err = f.conductors.Register(db.WithTenant(context.Background(), otherTenant), foreign, "foreign")
	require.NoError(t, err)

	seen := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	_, err = f.database.Exec(f.ctx, `INSERT INTO conductor_queues (conductor_id, host_id, host_seen_at) VALUES ($1, $2, $3)`, mine.ID, uuid.New(), seen)
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx, `
		INSERT INTO conductor_conversations (id, conductor_id, owner_user_id, state)
		VALUES ($1, $2, $3, 'ready'), ($4, $5, $6, 'stopped'), ($7, $8, $9, 'starting')`,
		uuid.New(), mine.ID, f.owner.ID, uuid.New(), theirs.ID, f.other.ID, uuid.New(), archived.ID, f.other.ID)
	require.NoError(t, err)

	_, err = f.database.Global().Exec(context.Background(), `UPDATE global_users SET display_name = 'Olive' WHERE id = $1`, f.owner.ID)
	require.NoError(t, err)
	api := f.api(f.tokens())
	get := func(token, query string) (string, []agent.ConductorDirectoryEntryBody) {
		rec := api.do(token, http.MethodGet, "/api/conductors/directory"+query, ``)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var body struct {
			Conductors []agent.ConductorDirectoryEntryBody `json:"conductors"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		return rec.Body.String(), body.Conductors
	}

	// A plain member who owns none of them sees everyone's.
	raw, list := get("other", "")
	require.Len(t, list, 2, "archived and other tenants' are absent")
	assert.Equal(t, "mine", list[0].Name)
	require.NotNil(t, list[0].OwnerName)
	assert.Equal(t, "Olive", *list[0].OwnerName)
	assert.True(t, list[0].HasLiveSession)
	require.NotNil(t, list[0].HostSeenAt)
	assert.True(t, seen.Equal(*list[0].HostSeenAt))
	assert.Equal(t, "theirs", list[1].Name)
	require.NotNil(t, list[1].OwnerName)
	assert.Equal(t, "a member", *list[1].OwnerName, "no display name set")
	assert.False(t, list[1].HasLiveSession, "a stopped conversation is not live")
	assert.Nil(t, list[1].HostSeenAt)
	for _, banned := range []string{"title", "first_line", "conversation", "ref", "commit"} {
		assert.NotContains(t, raw, banned)
	}

	_, list = get("admin", "?include_archived=true")
	require.Len(t, list, 3)
	assert.NotNil(t, list[0].ArchivedAt)
	assert.Equal(t, "archived", list[0].Name)
	assert.True(t, list[0].HasLiveSession, "starting is live")

	outsider := &authn.User{ID: dbtest.NewUser(t, f.database), TenantID: f.tenant, AuthTime: time.Now()}
	api = f.api(map[string]*authn.User{"outsider": outsider})
	rec := api.do("outsider", http.MethodGet, "/api/conductors/directory", ``)
	assert.Equal(t, http.StatusForbidden, rec.Code, "a user who is not a member of the tenant")
}
