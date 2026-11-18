package oauth_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
)

func TestConsentCreatesTheChosenKindOfAgent(t *testing.T) {
	for name, tc := range map[string]struct {
		kind string
		want agent.Kind
	}{
		"default is a worker": {kind: "", want: agent.KindWorker},
		"worker":              {kind: "worker", want: agent.KindWorker},
		"console":             {kind: "console", want: agent.KindConsole},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			c := f.client(t)
			verifier, challenge := pkce(t)

			code := f.authorize(t, c, challenge, &oauth.Decision{Approve: true, AgentName: "new-agent", Kind: tc.kind})
			tokens, err := f.exchange(c, code, verifier)
			require.NoError(t, err)
			access, err := f.svc.Authenticate(context.Background(), tokens.AccessToken)
			require.NoError(t, err)

			created, err := f.agents.Get(f.ctx, access.AgentID)
			require.NoError(t, err)
			assert.Equal(t, tc.want, created.Kind)
		})
	}
}

func TestConsentRefusesAKindItCannotCreate(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	_, challenge := pkce(t)
	consent, err := f.svc.Authorize(context.Background(), &oauth.AuthorizeParams{
		ResponseType: "code", ClientID: c.ID, RedirectURI: redirect,
		CodeChallenge: challenge, CodeChallengeMethod: "S256", State: "s",
	})
	require.NoError(t, err)
	u, _ := url.Parse(consent)
	_, err = f.svc.DecideAuthRequest(f.ctx, f.user, uuid.MustParse(u.Query().Get("request")),
		&oauth.Decision{Approve: true, AgentName: "sneaky", Kind: "conductor"})
	assert.ErrorIs(t, err, oauth.ErrAgentKindInvalid)
	_, err = agent.NewPostgresStore(f.db).GetByName(f.ctx, "sneaky")
	assert.ErrorIs(t, err, agent.ErrNotFound)
}

func TestChoosingAnExistingAgentKeepsItsKind(t *testing.T) {
	f := newFixture(t)
	existing, err := f.agents.Create(f.ctx, "builder-1")
	require.NoError(t, err)

	got, err := f.agents.Choose(f.ctx, f.user, &existing.ID, "", "console")
	require.NoError(t, err)
	assert.Equal(t, existing.ID, got)
	after, err := f.agents.Get(f.ctx, existing.ID)
	require.NoError(t, err)
	assert.Equal(t, agent.KindWorker, after.Kind)
}

func TestConsentBodyAgentKind(t *testing.T) {
	f := newFixture(t)
	mux := f.consentMux(t, map[string]*authn.User{"me": f.user})
	c := f.client(t)
	_, challenge := pkce(t)
	consent, err := f.svc.Authorize(context.Background(), &oauth.AuthorizeParams{
		ResponseType: "code", ClientID: c.ID, RedirectURI: redirect,
		CodeChallenge: challenge, CodeChallengeMethod: "S256", State: "s",
	})
	require.NoError(t, err)
	u, _ := url.Parse(consent)
	path := "/api/oauth/requests/" + u.Query().Get("request") + "/decision"

	rec := call(mux, http.MethodPost, path, "me", `{"approve":true,"agent_id":"`+uuid.NewString()+`","agent_kind":"console"}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "an existing agent has its kind already")
	rec = call(mux, http.MethodPost, path, "me", `{"approve":true,"agent_name":"x","agent_kind":"conductor"}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "only worker and console are choosable")

	rec = call(mux, http.MethodPost, path, "me", `{"approve":true,"agent_name":"my-console","agent_kind":"console"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	made, err := agent.NewPostgresStore(f.db).GetByName(f.ctx, "my-console")
	require.NoError(t, err)
	assert.Equal(t, agent.KindConsole, made.Kind)
}
