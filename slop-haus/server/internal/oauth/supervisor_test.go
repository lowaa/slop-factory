package oauth_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/ratelimit"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
)

func intp(n int) *int { return &n }

// supervisor is signed in as a new supervisor with the given cap, by the
// consent a person gives; it returns the agent, and its tokens.
func (f *fixture) supervisor(t *testing.T, name string, slotCap int) (*agent.Agent, *oauth.TokenResponse) {
	t.Helper()
	c := f.client(t)
	verifier, challenge := pkce(t)
	code := f.authorize(t, c, challenge, &oauth.Decision{Approve: true, AgentName: name, Kind: "supervisor", SlotCap: intp(slotCap)})
	tokens, err := f.exchange(c, code, verifier)
	require.NoError(t, err)
	access, err := f.svc.Authenticate(context.Background(), tokens.AccessToken)
	require.NoError(t, err)
	found, err := f.agents.Get(f.ctx, access.AgentID)
	require.NoError(t, err)
	return found, tokens
}

func TestConsentCreatesASupervisorWithItsSlotCap(t *testing.T) {
	f := newFixture(t)

	sup, _ := f.supervisor(t, "box-1", 3)

	assert.Equal(t, agent.KindSupervisor, sup.Kind)
	require.NotNil(t, sup.SlotCap)
	assert.Equal(t, 3, *sup.SlotCap)
}

func TestConsentRefusesASupervisorThatBreaksTheRules(t *testing.T) {
	existing := uuid.New()
	for name, tc := range map[string]struct {
		d    *oauth.Decision
		want error
	}{
		"no cap":                  {&oauth.Decision{Approve: true, AgentName: "s", Kind: "supervisor"}, oauth.ErrAgentKindInvalid},
		"a cap of none":           {&oauth.Decision{Approve: true, AgentName: "s", Kind: "supervisor", SlotCap: intp(0)}, oauth.ErrSlotCapInvalid},
		"a cap above the maximum": {&oauth.Decision{Approve: true, AgentName: "s", Kind: "supervisor", SlotCap: intp(agent.MaxSlotCap + 1)}, oauth.ErrSlotCapInvalid},
		"a cap on a worker":       {&oauth.Decision{Approve: true, AgentName: "s", Kind: "worker", SlotCap: intp(2)}, oauth.ErrAgentKindInvalid},
		"a cap on a default kind": {&oauth.Decision{Approve: true, AgentName: "s", SlotCap: intp(2)}, oauth.ErrAgentKindInvalid},
		"over an existing agent":  {&oauth.Decision{Approve: true, AgentID: &existing, Kind: "supervisor", SlotCap: intp(2)}, oauth.ErrAgentKindInvalid},
		"replacing an agent":      {&oauth.Decision{Approve: true, AgentName: "s", Kind: "supervisor", SlotCap: intp(2), ReplacesAgentID: &existing}, oauth.ErrAgentKindInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			c := f.client(t)
			_, challenge := pkce(t)
			consent, err := f.svc.Authorize(context.Background(), &oauth.AuthorizeParams{
				ResponseType: "code", ClientID: c.ID, RedirectURI: redirect,
				CodeChallenge: challenge, CodeChallengeMethod: "S256", State: "s",
			})
			require.NoError(t, err)
			u, _ := url.Parse(consent)

			_, err = f.svc.DecideAuthRequest(f.ctx, f.user, uuid.MustParse(u.Query().Get("request")), tc.d)

			assert.ErrorIs(t, err, tc.want)
			_, err = agent.NewPostgresStore(f.db).GetByName(f.ctx, "s")
			assert.ErrorIs(t, err, agent.ErrNotFound, "no agent was made")
		})
	}
}

func TestASupervisorIsNeverAnExistingAgentPick(t *testing.T) {
	f := newFixture(t)
	sup, _ := f.supervisor(t, "box-1", 2)
	c := f.client(t)
	_, challenge := pkce(t)
	consent, err := f.svc.Authorize(context.Background(), &oauth.AuthorizeParams{
		ResponseType: "code", ClientID: c.ID, RedirectURI: redirect,
		CodeChallenge: challenge, CodeChallengeMethod: "S256", State: "s",
	})
	require.NoError(t, err)
	u, _ := url.Parse(consent)

	_, err = f.svc.DecideAuthRequest(f.ctx, f.user, uuid.MustParse(u.Query().Get("request")), &oauth.Decision{Approve: true, AgentID: &sup.ID})

	assert.ErrorIs(t, err, oauth.ErrAgentUnavailable)
}

func TestConsentBodyForASupervisor(t *testing.T) {
	f := newFixture(t)
	mux := f.consentMux(t, map[string]*authn.User{"me": f.user})
	path := func() string {
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

	p := path()
	for name, body := range map[string]string{
		"a supervisor needs a cap":  `{"approve":true,"agent_name":"s","agent_kind":"supervisor"}`,
		"a cap needs a supervisor":  `{"approve":true,"agent_name":"s","agent_kind":"worker","slot_cap":2}`,
		"a cap of none":             `{"approve":true,"agent_name":"s","agent_kind":"supervisor","slot_cap":0}`,
		"a cap above the server's":  `{"approve":true,"agent_name":"s","agent_kind":"supervisor","slot_cap":17}`,
		"no replacing a supervisor": `{"approve":true,"agent_name":"s","agent_kind":"supervisor","slot_cap":2,"replaces_agent_id":"` + uuid.NewString() + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := call(mux, http.MethodPost, p, "me", body)
			assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
		})
	}

	rec := call(mux, http.MethodPost, p, "me", `{"approve":true,"agent_name":"my-box","agent_kind":"supervisor","slot_cap":4}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	made, err := agent.NewPostgresStore(f.db).GetByName(f.ctx, "my-box")
	require.NoError(t, err)
	assert.Equal(t, agent.KindSupervisor, made.Kind)
	assert.Equal(t, 4, *made.SlotCap)
}

// slotOf makes a slot for the supervisor.
func (f *fixture) slotOf(t *testing.T, sup *agent.Agent, name string) *agent.Agent {
	t.Helper()
	slot, err := f.agents.CreateSlot(f.ctx, sup, name)
	require.NoError(t, err)
	return slot
}

func TestASlotCredentialIsAGrantForTheSlotApprovedByTheSupervisorsApprover(t *testing.T) {
	f := newFixture(t)
	sup, _ := f.supervisor(t, "box-1", 2)
	slot := f.slotOf(t, sup, "slot-1")

	issued, err := f.svc.IssueSlotCredential(f.ctx, f.user.TenantID, sup.ID, slot.ID)
	require.NoError(t, err)

	assert.Equal(t, f.user.ID, issued.ApprovedBy)
	access, err := f.svc.Authenticate(context.Background(), issued.Tokens.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, slot.ID, access.AgentID, "it acts as the slot, not the supervisor")
	assert.Equal(t, f.user.TenantID, access.TenantID)
	assert.False(t, access.ReadOnly)
	assert.Equal(t, "Bearer", issued.Tokens.TokenType)
	assert.NotEmpty(t, issued.Tokens.RefreshToken, "the pair the device sign-in returns")

	var client, resource string
	var approvedBy uuid.UUID
	require.NoError(t, f.db.Global().QueryRow(context.Background(),
		`SELECT client_id, resource, approved_by FROM global_oauth_grants WHERE id = $1`, issued.GrantID).Scan(&client, &resource, &approvedBy))
	assert.Equal(t, oauth.WorkspaceClientID, client)
	assert.Equal(t, f.svc.Resource(), resource)
	assert.Equal(t, f.user.ID, approvedBy)

	refreshed, err := f.refresh(oauth.WorkspaceClientID, issued.Tokens.RefreshToken)
	require.NoError(t, err, "the slot signs itself in again with its refresh token")
	assert.NotEqual(t, issued.Tokens.RefreshToken, refreshed.RefreshToken)
}

func TestASlotCredentialNeedsTheSupervisorsLiveFullAccessGrant(t *testing.T) {
	setup := func(t *testing.T) (*fixture, *agent.Agent, *agent.Agent) {
		f := newFixture(t)
		sup, _ := f.supervisor(t, "box-1", 2)
		return f, sup, f.slotOf(t, sup, "slot-1")
	}
	t.Run("its grants have ended", func(t *testing.T) {
		f, sup, slot := setup(t)
		require.NoError(t, f.svc.RevokeAgentGrants(f.ctx, f.user.TenantID, sup.ID))

		_, err := f.svc.IssueSlotCredential(f.ctx, f.user.TenantID, sup.ID, slot.ID)

		assert.ErrorIs(t, err, oauth.ErrNotFound)
	})
	t.Run("its only grant is read-only", func(t *testing.T) {
		f, sup, slot := setup(t)
		require.NoError(t, f.svc.RevokeAgentGrants(f.ctx, f.user.TenantID, sup.ID))
		_, err := f.db.Global().Exec(context.Background(), `
			INSERT INTO global_oauth_grants (id, tenant, agent_id, client_id, approved_by, resource, scope)
			VALUES ($1, $2, $3, $4, $5, 'r', 'read')`, uuid.New(), f.user.TenantID, sup.ID, oauth.WorkspaceClientID, f.user.ID)
		require.NoError(t, err)

		_, err = f.svc.IssueSlotCredential(f.ctx, f.user.TenantID, sup.ID, slot.ID)

		assert.ErrorIs(t, err, oauth.ErrNotFound)
	})
	t.Run("nobody is recorded as having approved it", func(t *testing.T) {
		f, sup, slot := setup(t)
		_, err := f.db.Global().Exec(context.Background(),
			`UPDATE global_oauth_grants SET approved_by = NULL WHERE agent_id = $1`, sup.ID)
		require.NoError(t, err)

		_, err = f.svc.IssueSlotCredential(f.ctx, f.user.TenantID, sup.ID, slot.ID)

		assert.ErrorIs(t, err, oauth.ErrNoApprover)
	})
	t.Run("the grant is in another tenant", func(t *testing.T) {
		f, sup, slot := setup(t)

		_, err := f.svc.IssueSlotCredential(f.ctx, uuid.New(), sup.ID, slot.ID)

		assert.ErrorIs(t, err, oauth.ErrNotFound)
	})
	t.Run("a failed issue leaves nothing behind", func(t *testing.T) {
		f, sup, slot := setup(t)
		require.NoError(t, f.svc.RevokeAgentGrants(f.ctx, f.user.TenantID, sup.ID))
		_, err := f.svc.IssueSlotCredential(f.ctx, f.user.TenantID, sup.ID, slot.ID)
		require.Error(t, err)

		var n int
		require.NoError(t, f.db.Global().QueryRow(context.Background(),
			`SELECT count(*) FROM global_oauth_grants WHERE agent_id = $1`, slot.ID).Scan(&n))
		assert.Zero(t, n)
	})
}

func TestSlotCredentialsAreRateLimitedPerSupervisor(t *testing.T) {
	f := newFixture(t, func(c *oauth.Config) {
		c.SlotCredentialLimit = &ratelimit.Config{Limit: 2, Window: time.Hour, MaxKeys: 10}
	})
	sup, _ := f.supervisor(t, "box-1", 3)
	other, _ := f.supervisor(t, "box-2", 3)
	slot, second, otherSlot := f.slotOf(t, sup, "slot-1"), f.slotOf(t, sup, "slot-1b"), f.slotOf(t, other, "slot-2")

	for _, s := range []*agent.Agent{slot, second} {
		_, err := f.svc.IssueSlotCredential(f.ctx, f.user.TenantID, sup.ID, s.ID)
		require.NoError(t, err)
	}
	for _, s := range []*agent.Agent{slot, second} {
		_, err := f.svc.IssueSlotCredential(f.ctx, f.user.TenantID, sup.ID, s.ID)
		assert.ErrorIs(t, err, oauth.ErrTooManySlotCredentials, "the allowance is the supervisor's, not each slot's")
	}

	_, err := f.svc.IssueSlotCredential(f.ctx, f.user.TenantID, other.ID, otherSlot.ID)
	assert.NoError(t, err, "another supervisor has its own allowance")
}

func TestSlotCredentialsAreOffWithoutALimit(t *testing.T) {
	f := newFixture(t, func(c *oauth.Config) { c.SlotCredentialLimit = nil })
	sup, _ := f.supervisor(t, "box-1", 2)
	slot := f.slotOf(t, sup, "slot-1")

	_, err := f.svc.IssueSlotCredential(f.ctx, f.user.TenantID, sup.ID, slot.ID)

	assert.ErrorIs(t, err, oauth.ErrSlotCredentialsOff)
}

func TestASlotCredentialIsApprovedByTheNewestFullAccessGrantsApprover(t *testing.T) {
	f := newFixture(t)
	sup, _ := f.supervisor(t, "box-1", 2)
	slot := f.slotOf(t, sup, "slot-1")
	newer := f.newUser(t)
	_, err := f.db.Global().Exec(context.Background(), `
		INSERT INTO global_oauth_grants (id, tenant, agent_id, client_id, approved_by, resource, scope, created_at)
		VALUES ($1, $2, $3, $4, $5, 'r', 'agent', now() + interval '1 minute')`, uuid.New(), f.user.TenantID, sup.ID, oauth.WorkspaceClientID, newer.ID)
	require.NoError(t, err)
	_, err = f.db.Global().Exec(context.Background(), `
		INSERT INTO global_oauth_grants (id, tenant, agent_id, client_id, approved_by, resource, scope, created_at)
		VALUES ($1, $2, $3, $4, $5, 'r', 'read', now() + interval '2 minutes')`, uuid.New(), f.user.TenantID, sup.ID, oauth.WorkspaceClientID, f.newUser(t).ID)
	require.NoError(t, err)

	issued, err := f.svc.IssueSlotCredential(f.ctx, f.user.TenantID, sup.ID, slot.ID)
	require.NoError(t, err)

	assert.Equal(t, newer.ID, issued.ApprovedBy, "not the oldest grant's, and a newer read-only grant does not count")
	approver, err := f.svc.SlotApprover(context.Background(), f.user.TenantID, sup.ID)
	require.NoError(t, err)
	assert.Equal(t, newer.ID, *approver, "the audit lines name the same person")
}
