package agent_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
)

const rcPath = "/api/supervisor/remote-control/"

func TestASupervisorEnrolsAFreshHostItIsToRun(t *testing.T) {
	p := newProvisioning(t)
	p.mustPut(t, 0, rcEntry("rc-1", "supervisor"))

	ensured := p.do(http.MethodPut, rcPath+"rc-1", p.bearer, ``)
	require.Equal(t, http.StatusOK, ensured.Code, ensured.Body.String())
	again := p.do(http.MethodPut, rcPath+"rc-1", p.bearer, ``)
	assert.Equal(t, decode[slotJSON](t, ensured).ID, decode[slotJSON](t, again).ID, "one agent however often it asks")

	rec := p.do(http.MethodPost, rcPath+"rc-1/credential", p.bearer, ``)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	tokens := decode[stewardTokens](t, rec)
	as, err := p.svc.Authenticate(p.ctx, tokens.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, agent.KindConductor, as.Kind)
	assert.Equal(t, "rc-1", as.Name)
	host, err := p.svc.Get(p.ctx, as.ID)
	require.NoError(t, err)
	assert.True(t, host.OwnedBy(p.user.ID), "owned by the person who approved the supervisor")
	require.NotNil(t, host.RunBySupervisorID)
	assert.Equal(t, p.sup.ID, *host.RunBySupervisorID)
	lines := p.logs.audit(t, "supervisor_remote_control_credential_issued")
	require.Len(t, lines, 1)
	assert.Equal(t, p.user.ID.String(), lines[0]["approved_by"])
	assert.Len(t, p.logs.audit(t, "supervisor_remote_control_host_created"), 1)
}

func TestACredentialRequestAloneEnrolsAFreshHost(t *testing.T) {
	p := newProvisioning(t)
	p.mustPut(t, 0, rcEntry("rc-1", "supervisor"))

	rec := p.do(http.MethodPost, rcPath+"rc-1/credential", p.bearer, ``)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	as, err := p.svc.Authenticate(p.ctx, decode[stewardTokens](t, rec).AccessToken)
	require.NoError(t, err)
	assert.Equal(t, agent.KindConductor, as.Kind)
}

// The two halves meet here: what enrolment makes must be what the host lease
// takes, or every supervisor-run host is refused at its first call.
func TestAnEnrolledHostTakesTheHostLease(t *testing.T) {
	p := newProvisioning(t)
	p.mustPut(t, 0, rcEntry("rc-1", "supervisor"))
	rec := p.do(http.MethodPost, rcPath+"rc-1/credential", p.bearer, ``)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	lease := p.do(http.MethodPost, "/api/agent/host_lease", decode[stewardTokens](t, rec).AccessToken, `{"holder":"host-a"}`)

	assert.Equal(t, http.StatusOK, lease.Code, lease.Body.String())
}

func TestEnrolmentStopsWhenTheTenantStopsProvisioningFromTheBoard(t *testing.T) {
	p := newProvisioning(t)
	p.mustPut(t, 0, rcEntry("rc-1", "supervisor"))
	_, err := p.settings.Set(p.ctx, settings.ProvisionFromBoard, settings.BoolValue(false), p.user.ID)
	require.NoError(t, err)

	assert.Equal(t, http.StatusNotFound, p.do(http.MethodPost, rcPath+"rc-1/credential", p.bearer, ``).Code)
}

func TestARemoteControlEntryNamingAnAgentThatIsNotTheApproversConductorReachesNothing(t *testing.T) {
	p := newProvisioning(t)
	worker, err := p.svc.Create(p.ctx, "plain-worker")
	require.NoError(t, err)
	console := p.console(t, "a-console")
	theirs := p.othersConductor(t, "their-session")
	p.mustPut(t, 0, rcEntry("plain-worker", "supervisor")+","+rcEntry("a-console", "supervisor")+","+rcEntry("their-session", "supervisor"))

	for _, held := range []*agent.Agent{worker, console, theirs} {
		assert.Equal(t, http.StatusNotFound, p.do(http.MethodPut, rcPath+held.Name, p.bearer, ``).Code, held.Name)
		assert.Equal(t, http.StatusNotFound, p.do(http.MethodPost, rcPath+held.Name+"/credential", p.bearer, ``).Code, held.Name)
		assert.Zero(t, p.grantCount(t, held), held.Name)
	}
}

func TestRemovingASupervisorRunHostRetiresItsConductorAndRevokesItsGrants(t *testing.T) {
	p := newProvisioning(t)
	doc := p.mustPut(t, 0, rcEntry("rc-1", "supervisor"))
	host := p.enrolled(t, "rc-1")

	p.mustPut(t, doc.Revision, echoLaunch(doc, func(a *desiredAgentJSON) { a.State = "removed" }))

	got, err := p.svc.Get(p.ctx, host.ID)
	require.NoError(t, err)
	assert.True(t, got.Archived())
	assert.Zero(t, p.liveGrantCount(t, got))
}

func TestRemovingAnEntryLeavesAnotherMembersConductorOfThatNameAlone(t *testing.T) {
	p := newProvisioning(t)
	theirs := p.othersConductor(t, "their-session")
	doc := p.mustPut(t, 0, rcEntry("their-session", "supervisor"))

	p.mustPut(t, doc.Revision, echoLaunch(doc, func(a *desiredAgentJSON) { a.State = "removed" }))

	got, err := p.svc.Get(p.ctx, theirs.ID)
	require.NoError(t, err)
	assert.False(t, got.Archived())
	assert.False(t, got.Disabled())
}

func TestRemovingAHostRetiresItAfterItsSupervisorWasSignedOut(t *testing.T) {
	p := newProvisioning(t)
	doc := p.mustPut(t, 0, rcEntry("rc-1", "supervisor"))
	host := p.enrolled(t, "rc-1")
	_, err := p.database.Global().Exec(context.Background(),
		`UPDATE global_oauth_grants SET revoked_at = now() WHERE agent_id = $1 AND tenant = $2`, p.sup.ID, p.tenant)
	require.NoError(t, err)

	p.mustPut(t, doc.Revision, echoLaunch(doc, func(a *desiredAgentJSON) { a.State = "removed" }))

	got, err := p.svc.Get(p.ctx, host.ID)
	require.NoError(t, err)
	assert.True(t, got.Archived(), "the record of who runs it retires it, not who approves the supervisor now")
	assert.Zero(t, p.liveGrantCount(t, got))
}

func TestAHandRegisteredConductorOfTheApproverIsNeitherRunNorRetired(t *testing.T) {
	p := newProvisioning(t)
	mine := p.conductorOf(t, "my-session", p.user.ID)
	doc := p.mustPut(t, 0, rcEntry("my-session", "supervisor"))

	assert.Equal(t, http.StatusNotFound, p.do(http.MethodPost, rcPath+"my-session/credential", p.bearer, ``).Code)
	assert.Zero(t, p.grantCount(t, mine))
	p.mustPut(t, doc.Revision, echoLaunch(doc, func(a *desiredAgentJSON) { a.State = "removed" }))
	got, err := p.svc.Get(p.ctx, mine.ID)
	require.NoError(t, err)
	assert.False(t, got.Archived())
}

// The approver is read once to find the host and again by the grant insert:
// a sign-in by someone else landing in between must not leave them holding
// the owner's session.
func TestACredentialApprovedByAnyoneButTheHostsOwnerIsEndedAndRefused(t *testing.T) {
	p := newProvisioning(t)
	p.mustPut(t, 0, rcEntry("rc-1", "supervisor"))
	host := p.enrolled(t, "rc-1")
	other := p.otherMember(t)
	p.beforeIssue = func(ctx context.Context, _ uuid.UUID) {
		p.beforeIssue = nil
		_, err := p.database.Global().Exec(context.Background(), `
			INSERT INTO global_oauth_grants (id, tenant, agent_id, client_id, resource, approved_by, scope, created_at)
			VALUES (gen_random_uuid(), $1, $2, 'slop-haus-workspace', 'r', $3, 'agent', now() + interval '1 second')`, p.tenant, p.sup.ID, other)
		require.NoError(t, err)
	}
	before := p.liveGrantCount(t, host)

	rec := p.do(http.MethodPost, rcPath+"rc-1/credential", p.bearer, ``)

	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Equal(t, before, p.liveGrantCount(t, host), "the grant issued under the other approver is ended")
}

func TestASupervisorNobodyApprovedRunsNoHost(t *testing.T) {
	p := newProvisioning(t)
	unapproved := p.supervisor(t, "unapproved-box", 1)

	_, err := p.provision.RemoteControlHost(p.ctx, unapproved.ID, "rc-1")

	require.ErrorIs(t, err, agent.ErrGrantNoApprover)
	_, err = agent.NewPostgresStore(p.database).GetByName(p.ctx, "rc-1")
	assert.ErrorIs(t, err, agent.ErrNotFound, "no conductor is made without an owner")
}

// enrolled is the conductor the supervisor's first credential request makes for the entry name.
func (p *provisioning) enrolled(t *testing.T, name string) *agent.Agent {
	t.Helper()
	rec := p.do(http.MethodPost, rcPath+name+"/credential", p.bearer, ``)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	as, err := p.svc.Authenticate(p.ctx, decode[stewardTokens](t, rec).AccessToken)
	require.NoError(t, err)
	host, err := p.svc.Get(p.ctx, as.ID)
	require.NoError(t, err)
	return host
}

// otherMember is another member of the tenant.
func (p *provisioning) otherMember(t *testing.T) uuid.UUID {
	t.Helper()
	other := dbtest.NewUser(t, p.database)
	_, err := p.database.Global().Exec(context.Background(),
		`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES (gen_random_uuid(), $1, $2, 'member')`, p.tenant, other)
	require.NoError(t, err)
	return other
}

// othersConductor is a Remote Control another member of the tenant registered.
func (p *provisioning) othersConductor(t *testing.T, name string) *agent.Agent {
	t.Helper()
	return p.conductorOf(t, name, p.otherMember(t))
}

// conductorOf is a Remote Control owner registered by hand.
func (p *provisioning) conductorOf(t *testing.T, name string, owner uuid.UUID) *agent.Agent {
	t.Helper()
	id := uuid.New()
	_, err := p.database.Exec(p.ctx, `INSERT INTO agents (id, name, kind, owner_user_id) VALUES ($1, $2, 'conductor', $3)`, id, name, owner)
	require.NoError(t, err)
	got, err := p.svc.Get(p.ctx, id)
	require.NoError(t, err)
	return got
}

func TestARemoteControlHostIsRefusedUnlessTheSupervisorIsToRunIt(t *testing.T) {
	p := newProvisioning(t)
	p.mustPut(t, 0, rcEntry("rc-manual", "manual")+","+entry("a-slot", "running"))
	_, err := p.svc.Create(p.ctx, "plain-worker")
	require.NoError(t, err)

	for _, name := range []string{"rc-manual", "a-slot", "plain-worker", "not-listed"} {
		for _, path := range []string{"", "/credential"} {
			method := http.MethodPut
			if path != "" {
				method = http.MethodPost
			}
			assert.Equal(t, http.StatusNotFound, p.do(method, rcPath+name+path, p.bearer, ``).Code, name+path)
		}
	}
	assert.Equal(t, http.StatusNotFound, p.do(http.MethodPost, rcPath+"rc-manual/credential/revoke", p.bearer, refreshBody("x")).Code)
}

func TestAnotherSupervisorsHostIsNotReached(t *testing.T) {
	p := newProvisioning(t)
	p.mustPut(t, 0, rcEntry("rc-1", "supervisor"))
	other := p.supervisor(t, "other-box", 1)
	require.Equal(t, http.StatusOK, p.do(http.MethodPut, rcPath+"rc-1", p.bearer, ``).Code, "the host exists")

	assert.Equal(t, http.StatusNotFound, p.do(http.MethodPost, rcPath+"rc-1/credential", p.token(t, other, "agent"), ``).Code)
}

func TestARemovedHostGetsNoCredential(t *testing.T) {
	p := newProvisioning(t)
	doc := p.mustPut(t, 0, rcEntry("rc-1", "supervisor"))
	require.Equal(t, http.StatusOK, p.do(http.MethodPut, rcPath+"rc-1", p.bearer, ``).Code)
	p.mustPut(t, doc.Revision, echoLaunch(doc, func(a *desiredAgentJSON) { a.State = "removed" }))

	assert.Equal(t, http.StatusNotFound, p.do(http.MethodPost, rcPath+"rc-1/credential", p.bearer, ``).Code)
}

func TestADisabledHostGetsNoCredentialAndAnArchivedOneIsRefused(t *testing.T) {
	p := newProvisioning(t)
	p.mustPut(t, 0, rcEntry("rc-1", "supervisor"))
	host := decode[slotJSON](t, p.do(http.MethodPut, rcPath+"rc-1", p.bearer, ``))
	id := uuid.MustParse(host.ID)
	// The service's own switches manage workers only.
	_, err := p.database.Exec(p.ctx, `UPDATE agents SET disabled_at = now() WHERE id = $1`, id)
	require.NoError(t, err)

	assert.Equal(t, http.StatusConflict, p.do(http.MethodPost, rcPath+"rc-1/credential", p.bearer, ``).Code)
	assert.Zero(t, p.grantCount(t, &agent.Agent{ID: id}))

	_, err = p.database.Exec(p.ctx, `UPDATE agents SET archived_at = now() WHERE id = $1`, id)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, p.do(http.MethodPut, rcPath+"rc-1", p.bearer, ``).Code)
}

func TestRevokingAHostCredentialEndsOnlyThatGrantAndOnlyTheHosts(t *testing.T) {
	p := newProvisioning(t)
	p.mustPut(t, 0, rcEntry("rc-1", "supervisor")+","+rcEntry("rc-2", "supervisor"))
	issue := func(name string) stewardTokens {
		rec := p.do(http.MethodPost, rcPath+name+"/credential", p.bearer, ``)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return decode[stewardTokens](t, rec)
	}
	old, kept, other := issue("rc-1"), issue("rc-1"), issue("rc-2")

	assert.Equal(t, http.StatusForbidden, p.do(http.MethodPost, rcPath+"rc-1/credential/revoke", p.bearer, refreshBody(other.RefreshToken)).Code,
		"another host's token")
	_, err := p.svc.Authenticate(p.ctx, other.AccessToken)
	require.NoError(t, err)

	rec := p.do(http.MethodPost, rcPath+"rc-1/credential/revoke", p.bearer, refreshBody(old.RefreshToken))

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Len(t, p.logs.audit(t, "supervisor_remote_control_credential_revoked"), 1)
	_, err = p.svc.Authenticate(p.ctx, old.AccessToken)
	assert.Error(t, err)
	_, err = p.svc.Authenticate(p.ctx, kept.AccessToken)
	assert.NoError(t, err)
}

func TestOnlyASupervisorEnrolsAHost(t *testing.T) {
	p := newProvisioning(t)
	p.mustPut(t, 0, rcEntry("rc-1", "supervisor"))
	console := p.console(t, "my-console")

	for _, bearer := range []string{"", "user", p.token(t, console, "agent")} {
		assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, p.do(http.MethodPost, rcPath+"rc-1/credential", bearer, ``).Code)
	}
}
