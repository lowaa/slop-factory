package agent_test

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
)

const stewardPath = "/api/supervisor/steward"

func TestASupervisorMintsTheTenantsSingleSteward(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	first := a.supervisor(t, "box-1", 1)
	second := a.supervisor(t, "box-2", 1)

	rec := a.do(http.MethodPut, stewardPath, a.token(t, first, "agent"), ``)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	made := decode[slotJSON](t, rec)
	steward, err := a.svc.Get(a.ctx, uuid.MustParse(made.ID))
	require.NoError(t, err)
	assert.Equal(t, agent.KindSteward, steward.Kind)

	rec = a.do(http.MethodPut, stewardPath, a.token(t, second, "agent"), ``)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, made.ID, decode[slotJSON](t, rec).ID, "a second supervisor gets the same steward, not another")
}

func TestOnlyASupervisorMintsASteward(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	sup := a.supervisor(t, "box", 1)
	worker, err := a.svc.Create(a.ctx, "plain-worker")
	require.NoError(t, err)
	steward, err := a.svc.Steward(a.ctx, sup.ID)
	require.NoError(t, err)
	callers := map[string]string{
		"a worker":               a.token(t, worker, "agent"),
		"a console":              a.token(t, a.console(t, "my-console"), "agent"),
		"a conductor":            a.token(t, a.conductor(t, "my-conductor"), "agent"),
		"the steward itself":     a.token(t, steward, "agent"),
		"a read-only supervisor": a.token(t, sup, "read"),
		"a user's session token": "user",
		"no credential":          "",
	}

	for who, bearer := range callers {
		for _, r := range []struct{ method, path string }{{http.MethodPut, stewardPath}, {http.MethodPost, stewardPath + "/credential"}} {
			t.Run(who+" "+r.method+" "+r.path, func(t *testing.T) {
				rec := a.do(r.method, r.path, bearer, ``)
				assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, rec.Code, rec.Body.String())
			})
		}
	}
	assert.Empty(t, a.logs.audit(t, "supervisor_steward_credential_issued"))
}

func TestTheDatabaseRefusesAStewardNoSupervisorMinted(t *testing.T) {
	f := newSupervisorFixture(t)
	sup := f.supervisor(t, "box", 1)
	insert := func(minter any) error {
		_, err := f.database.Exec(f.ctx, `INSERT INTO agents (id, name, kind, steward_minted_by) VALUES ($1, $2, 'steward', $3)`,
			uuid.New(), "s-"+uuid.NewString(), minter)
		return err
	}

	assert.Error(t, insert(nil), "a steward names its minter")
	assert.Error(t, insert(f.console(t, "a-console").ID), "the minter is a supervisor")
	_, err := f.database.Exec(f.ctx, `INSERT INTO agents (id, name, kind, steward_minted_by) VALUES ($1, 'w', 'worker', $2)`, uuid.New(), sup.ID)
	assert.Error(t, err, "only a steward carries a minter")

	_, err = f.agents.Steward(f.ctx, sup.ID)
	require.NoError(t, err)
	assert.Error(t, insert(sup.ID), "one live steward per tenant")
}

func TestTheStewardIsOnePerTenant(t *testing.T) {
	f := newSupervisorFixture(t)
	sup := f.supervisor(t, "box", 1)
	other := f.supervisorIn(t, f.ctx, "box-2", 1)

	a, err := f.agents.Steward(f.ctx, sup.ID)
	require.NoError(t, err)
	b, err := f.agents.Steward(f.ctx, other.ID)
	require.NoError(t, err)
	assert.Equal(t, a.ID, b.ID)
}

func TestConcurrentFirstMintsReturnTheSameSteward(t *testing.T) {
	f := newSupervisorFixture(t)
	const n = 6
	start := make(chan struct{})
	ids := make([]uuid.UUID, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		sup := f.supervisor(t, fmt.Sprintf("box-%d", i), 1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s, err := f.agents.Steward(f.ctx, sup.ID)
			errs[i] = err
			if err == nil {
				ids[i] = s.ID
			}
		}()
	}
	close(start)
	wg.Wait()

	for i := range n {
		require.NoError(t, errs[i])
		assert.Equal(t, ids[0], ids[i])
	}
}

func TestEachTenantHasItsOwnSteward(t *testing.T) {
	f := newSupervisorFixture(t)
	otherCtx := db.WithTenant(context.Background(), dbtest.NewTenant(t, f.database))

	mine, err := f.agents.Steward(f.ctx, f.supervisor(t, "box", 1).ID)
	require.NoError(t, err)
	theirs, err := f.agents.Steward(otherCtx, f.supervisorIn(t, otherCtx, "box", 1).ID)
	require.NoError(t, err)

	assert.NotEqual(t, mine.ID, theirs.ID)
	assert.Equal(t, f.tenant, mine.Tenant)
	assert.NotEqual(t, f.tenant, theirs.Tenant)
}

func TestStewardRefusesWhoIsNotALiveSupervisor(t *testing.T) {
	f := newSupervisorFixture(t)
	sup := f.supervisor(t, "box", 1)

	_, err := f.agents.Steward(f.ctx, f.console(t, "c").ID)
	assert.ErrorIs(t, err, agent.ErrNotSupervisor)
	_, err = f.agents.SetSupervisorDisabled(f.ctx, sup.ID, true)
	require.NoError(t, err)
	_, err = f.agents.Steward(f.ctx, sup.ID)
	assert.ErrorIs(t, err, agent.ErrIsDisabled)

	archived := f.supervisor(t, "old-box", 1)
	_, err = f.database.Exec(f.ctx, `UPDATE agents SET disabled_at = now(), archived_at = now() WHERE id = $1`, archived.ID)
	require.NoError(t, err)
	_, err = f.agents.Steward(f.ctx, archived.ID)
	assert.ErrorIs(t, err, agent.ErrArchived)
}

func TestAStewardCredentialActsAsTheStewardApprovedBySupervisorsApprover(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	sup := a.supervisor(t, "box", 1)

	rec := a.do(http.MethodPost, stewardPath+"/credential", a.token(t, sup, "agent"), ``)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	tokens := decode[struct {
		AccessToken string `json:"access_token"`
	}](t, rec)
	as, err := a.svc.Authenticate(a.ctx, tokens.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, agent.KindSteward, as.Kind)
	may, err := a.svc.MayPropose(a.ctx, as.ID)
	require.NoError(t, err)
	assert.True(t, may, "so it can use the propose tools")
	lines := a.logs.audit(t, "supervisor_steward_credential_issued")
	require.Len(t, lines, 1)
	assert.Equal(t, sup.ID.String(), lines[0]["supervisor"])
	assert.Equal(t, a.user.ID.String(), lines[0]["approved_by"])

	var approver, scope string
	require.NoError(t, a.database.Global().QueryRow(context.Background(),
		`SELECT approved_by::text, scope FROM global_oauth_grants WHERE agent_id = $1`, as.ID).Scan(&approver, &scope))
	assert.Equal(t, a.user.ID.String(), approver)
	assert.Equal(t, "agent", scope, "full access like a console's")
}

func TestADisabledStewardGetsNoCredentialAndAPersonSwitchesItOffFromTheAgentsScreen(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	sup := a.supervisor(t, "box", 1)
	supToken := a.token(t, sup, "agent")
	steward, err := a.svc.Steward(a.ctx, sup.ID)
	require.NoError(t, err)

	listed, err := a.svc.List(a.ctx, false)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, agent.KindSteward, listed[0].Kind, "the Agents screen shows it")

	_, err = a.svc.SetDisabled(a.ctx, steward.ID, true)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, a.do(http.MethodPost, stewardPath+"/credential", supToken, ``).Code)

	_, err = a.svc.SetArchived(a.ctx, a.user, steward.ID, true)
	require.NoError(t, err)
	_, err = a.svc.SignOutWorkspaces(a.ctx, steward.ID)
	assert.ErrorIs(t, err, agent.ErrNotFound, "the worker setup path does not reach the steward")
}

func TestDisablingTheStewardVoidsItsProposalsAndEndsItsGrants(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	sup := a.supervisor(t, "box", 1)
	steward, err := a.svc.Steward(a.ctx, sup.ID)
	require.NoError(t, err)
	grant := a.grant(t, steward)
	proposal := a.propose(t, steward)

	_, err = a.svc.SetDisabled(a.ctx, steward.ID, true)
	require.NoError(t, err)

	assert.Equal(t, pendingaction.StateVoided, a.state(t, proposal.ID))
	assert.True(t, a.grantRevoked(t, grant))
}

func TestAnArchivedStewardLeavesTheNameHeldSoMintingIs409(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	sup := a.supervisor(t, "box", 1)
	steward, err := a.svc.Steward(a.ctx, sup.ID)
	require.NoError(t, err)
	_, err = a.svc.SetDisabled(a.ctx, steward.ID, true)
	require.NoError(t, err)
	_, err = a.svc.SetArchived(a.ctx, a.user, steward.ID, true)
	require.NoError(t, err)

	rec := a.do(http.MethodPut, stewardPath, a.token(t, sup, "agent"), ``)

	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
}

type stewardTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

func (a *supervisorAPI) issueStewardCredential(t *testing.T, supToken string) stewardTokens {
	t.Helper()
	rec := a.do(http.MethodPost, stewardPath+"/credential", supToken, ``)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return decode[stewardTokens](t, rec)
}

func (a *supervisorAPI) liveGrants(t *testing.T, who *agent.Agent) int {
	t.Helper()
	var n int
	require.NoError(t, a.database.Global().QueryRow(context.Background(),
		`SELECT count(*) FROM global_oauth_grants WHERE agent_id = $1 AND revoked_at IS NULL`, who.ID).Scan(&n))
	return n
}

func refreshBody(token string) string { return fmt.Sprintf(`{"refresh_token":%q}`, token) }

func TestRevokingAStewardCredentialEndsOnlyThatGrant(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	sup := a.supervisor(t, "box", 1)
	supToken := a.token(t, sup, "agent")
	steward, err := a.svc.Steward(a.ctx, sup.ID)
	require.NoError(t, err)
	old, other := a.issueStewardCredential(t, supToken), a.issueStewardCredential(t, supToken)

	rec := a.do(http.MethodPost, stewardPath+"/credential/revoke", supToken, refreshBody(old.RefreshToken))

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	_, err = a.svc.Authenticate(a.ctx, old.AccessToken)
	assert.Error(t, err, "the revoked credential no longer signs in")
	_, err = a.svc.Authenticate(a.ctx, other.AccessToken)
	assert.NoError(t, err, "another host's credential for the same steward is untouched")
	assert.Equal(t, 1, a.liveGrants(t, steward))
	assert.Len(t, a.logs.audit(t, "supervisor_steward_credential_revoked"), 1)
}

func TestRevokingACredentialThatIsNotTheStewardsIsRefused(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	sup := a.supervisor(t, "box", 1)
	supToken := a.token(t, sup, "agent")
	slot, err := a.svc.CreateSlot(a.ctx, sup, "slot-1")
	require.NoError(t, err)
	slotRec := a.do(http.MethodPost, "/api/supervisor/slots/"+slot.ID.String()+"/credential", supToken, ``)
	require.Equal(t, http.StatusOK, slotRec.Code, slotRec.Body.String())
	slotTokens := decode[stewardTokens](t, slotRec)

	for name, body := range map[string]string{"a slot's token": refreshBody(slotTokens.RefreshToken), "an unknown token": refreshBody("nope")} {
		for _, path := range []string{"/credential/revoke"} {
			assert.Equal(t, http.StatusForbidden, a.do(http.MethodPost, stewardPath+path, supToken, body).Code, name+" "+path)
		}
	}
	_, err = a.svc.Authenticate(a.ctx, slotTokens.AccessToken)
	assert.NoError(t, err, "the slot's grant survives")
}

func TestOnlyASupervisorRevokesStewardCredentials(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	sup := a.supervisor(t, "box", 1)
	steward, err := a.svc.Steward(a.ctx, sup.ID)
	require.NoError(t, err)
	issued := a.issueStewardCredential(t, a.token(t, sup, "agent"))
	callers := map[string]string{
		"the steward itself":     issued.AccessToken,
		"a worker":               a.token(t, mustCreate(t, a, "plain-worker"), "agent"),
		"a read-only supervisor": a.token(t, sup, "read"),
		"no credential":          "",
	}

	for who, bearer := range callers {
		for _, path := range []string{"/credential/revoke"} {
			rec := a.do(http.MethodPost, stewardPath+path, bearer, refreshBody(issued.RefreshToken))
			assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, rec.Code, who+" "+path+": "+rec.Body.String())
		}
	}
	assert.Equal(t, 1, a.liveGrants(t, steward))
}

func mustCreate(t *testing.T, a *supervisorAPI, name string) *agent.Agent {
	t.Helper()
	got, err := a.svc.Create(a.ctx, name)
	require.NoError(t, err)
	return got
}

func TestTheStewardModeRouteReadsTheTenantSettingForASupervisorOnly(t *testing.T) {
	a := newSupervisorAPI(t, 100)
	sup := a.supervisor(t, "box", 1)
	bearer := a.token(t, sup, "agent")
	const path = stewardPath + "/mode"
	type modeJSON struct {
		Mode string `json:"mode"`
	}

	rec := a.do(http.MethodGet, path, bearer, ``)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "off", decode[modeJSON](t, rec).Mode, "off until the owner turns the steward on")

	_, err := a.database.Global().Exec(a.ctx,
		`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES (gen_random_uuid(), $1, $2, 'owner')`, a.tenant, a.user.ID)
	require.NoError(t, err)
	_, err = a.settings.Set(a.ctx, "steward_mode", settings.StrValue("shadow"), a.user.ID)
	require.NoError(t, err)
	assert.Equal(t, "shadow", decode[modeJSON](t, a.do(http.MethodGet, path, bearer, ``)).Mode)

	assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, a.do(http.MethodGet, path, a.token(t, a.slot(t, sup, "s"), "agent"), ``).Code)
}
