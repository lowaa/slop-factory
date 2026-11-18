package oauth_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
)

// consentRequest starts an authorization and returns its decision path.
func (f *fixture) consentRequest(t *testing.T) string {
	t.Helper()
	c := f.client(t)
	_, challenge := pkce(t)
	consent, err := f.svc.Authorize(context.Background(), &oauth.AuthorizeParams{
		ResponseType: "code", ClientID: c.ID, RedirectURI: redirect,
		CodeChallenge: challenge, CodeChallengeMethod: "S256", State: "s",
	})
	require.NoError(t, err)
	u, _ := url.Parse(consent)
	return "/api/oauth/requests/" + u.Query().Get("request") + "/decision"
}

func TestConsentCanReplaceAnExistingAgent(t *testing.T) {
	f := newFixture(t)
	mux := f.consentMux(t, map[string]*authn.User{"me": f.user})
	old, err := f.agents.Create(f.ctx, "old-worker")
	require.NoError(t, err)

	rec := call(mux, http.MethodPost, f.consentRequest(t), "me",
		`{"approve":true,"agent_name":"my-console","agent_kind":"console","replaces_agent_id":"`+old.ID.String()+`"}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	made, err := agent.NewPostgresStore(f.db).GetByName(f.ctx, "my-console")
	require.NoError(t, err)
	assert.Equal(t, agent.KindConsole, made.Kind)
	require.NotNil(t, made.Replaces)
	assert.Equal(t, old.ID, made.Replaces.ID)
	gone, err := f.agents.Get(f.ctx, old.ID)
	require.NoError(t, err)
	assert.NotNil(t, gone.DisabledAt)
	assert.NotNil(t, gone.ArchivedAt)
}

func TestConsentRefusesToReplaceWhatItShouldNot(t *testing.T) {
	f := newFixture(t)
	owner := &authn.User{ID: dbtest.NewUser(t, f.db), TenantID: f.user.TenantID, AuthTime: time.Now()}
	f.join(t, owner, "member")
	f.join(t, f.user, "member")
	conductors := agent.NewConductorService(f.agents, f.members, slog.DiscardHandler)
	theirs, err := conductors.Register(f.ctx, owner, "their-conductor")
	require.NoError(t, err)
	archived, err := f.agents.Create(f.ctx, "archived")
	require.NoError(t, err)
	_, err = f.agents.SetDisabled(f.ctx, archived.ID, true)
	require.NoError(t, err)
	_, err = f.agents.SetArchived(f.ctx, f.user, archived.ID, true)
	require.NoError(t, err)
	live, err := f.agents.Create(f.ctx, "live")
	require.NoError(t, err)
	mux := f.consentMux(t, map[string]*authn.User{"me": f.user})
	path := f.consentRequest(t)

	for name, tc := range map[string]struct {
		body string
		want int
	}{
		"someone else's conductor": {`{"approve":true,"agent_name":"x","replaces_agent_id":"` + theirs.ID.String() + `"}`, http.StatusUnprocessableEntity},
		"an archived agent":        {`{"approve":true,"agent_name":"x","replaces_agent_id":"` + archived.ID.String() + `"}`, http.StatusUnprocessableEntity},
		"an unknown agent":         {`{"approve":true,"agent_name":"x","replaces_agent_id":"` + uuid.NewString() + `"}`, http.StatusUnprocessableEntity},
		"with an existing agent":   {`{"approve":true,"agent_id":"` + live.ID.String() + `","replaces_agent_id":"` + archived.ID.String() + `"}`, http.StatusUnprocessableEntity},
		"without a name":           {`{"approve":true,"replaces_agent_id":"` + live.ID.String() + `"}`, http.StatusUnprocessableEntity},
		"a taken name":             {`{"approve":true,"agent_name":"live","replaces_agent_id":"` + live.ID.String() + `"}`, http.StatusConflict},
	} {
		rec := call(mux, http.MethodPost, path, "me", tc.body)
		assert.Equal(t, tc.want, rec.Code, name+": "+rec.Body.String())
	}

	for _, id := range []uuid.UUID{theirs.ID, live.ID} {
		after, err := f.agents.Get(f.ctx, id)
		require.NoError(t, err)
		assert.Nil(t, after.DisabledAt, "a refused replacement leaves the agent enabled")
	}
}
