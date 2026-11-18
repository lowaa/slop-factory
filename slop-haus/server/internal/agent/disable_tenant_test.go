package agent_test

import (
	"context"
	"regexp"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
)

// agentsOfEveryKind registers one enabled agent of each kind in the
// fixture's tenant, keyed by kind, with a supervisor's slot under "slot".
func (f *supervisorFixture) agentsOfEveryKind(t *testing.T) map[string]*agent.Agent {
	t.Helper()
	owner := dbtest.NewUser(t, f.database)
	_, err := f.database.Global().Exec(context.Background(),
		`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES ($1, $2, $3, 'owner')`,
		uuid.New(), f.tenant, owner)
	require.NoError(t, err)
	conductorID := uuid.New()
	_, err = f.database.Exec(f.ctx, `INSERT INTO agents (id, name, kind, owner_user_id) VALUES ($1, 'conductor', 'conductor', $2)`, conductorID, owner)
	require.NoError(t, err)
	conductor, err := f.agents.Get(f.ctx, conductorID)
	require.NoError(t, err)
	sup := f.supervisor(t, "sup", 2)
	worker, err := f.agents.Create(f.ctx, "worker")
	require.NoError(t, err)
	steward, err := f.agents.Steward(f.ctx, sup.ID)
	require.NoError(t, err)
	return map[string]*agent.Agent{
		string(agent.KindSteward):    steward,
		string(agent.KindWorker):     worker,
		string(agent.KindConsole):    f.console(t, "console"),
		string(agent.KindConductor):  conductor,
		string(agent.KindSupervisor): sup,
		"slot":                       f.slot(t, sup, "slot"),
	}
}

func TestDisablingATenantsAgentsSwitchesOffEveryKindAndReleasesWhatTheyHold(t *testing.T) {
	f := newSupervisorFixture(t)
	all := f.agentsOfEveryKind(t)
	grants := map[string]uuid.UUID{}
	for kind, a := range all {
		grants[kind] = f.grant(t, a)
	}
	f.claim(t, all["worker"])
	f.claim(t, all["slot"])
	require.NoError(t, f.tickets.AcquireIntegrationLock(f.ctx, all["worker"].ID))

	// Another tenant's agent, which must not be touched.
	otherTenant := dbtest.NewTenant(t, f.database)
	otherCtx := db.WithTenant(context.Background(), otherTenant)
	bystander, err := f.agents.Create(otherCtx, "bystander")
	require.NoError(t, err)

	require.NoError(t, f.agents.DisableTenantAgents(context.Background(), f.tenant))

	for kind, a := range all {
		assert.NotNil(t, f.get(t, a).DisabledAt, "%s is disabled", kind)
	}
	for kind, id := range grants {
		if kind != string(agent.KindWorker) && kind != string(agent.KindConsole) {
			assert.True(t, f.grantRevoked(t, id), "%s grants revoked", kind)
		}
	}
	assert.False(t, f.holdsLease(t, all["worker"]))
	assert.False(t, f.holdsLease(t, all["slot"]))
	state, err := f.tickets.IntegrationLockState(f.ctx)
	require.NoError(t, err)
	assert.Nil(t, state.Holder, "the integration lock is freed")

	got, err := f.agents.Get(otherCtx, bystander.ID)
	require.NoError(t, err)
	assert.Nil(t, got.DisabledAt, "another tenant's agent")

	require.NoError(t, f.agents.DisableTenantAgents(context.Background(), f.tenant), "idempotent")
}

// A kind added to the schema must be switched off by archiving too. This
// fails when the database accepts a kind that agentsOfEveryKind does not
// make, so whoever adds one adds it there and so to the test above.
func TestEveryAgentKindTheDatabaseAcceptsIsCoveredByDisablingATenant(t *testing.T) {
	f := newSupervisorFixture(t)
	var def string
	require.NoError(t, f.database.Global().QueryRow(context.Background(),
		`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'agents_kind_check'`).Scan(&def))
	var accepted []string
	for _, m := range regexp.MustCompile(`'([a-z_]+)'`).FindAllStringSubmatch(def, -1) {
		accepted = append(accepted, m[1])
	}
	require.NotEmpty(t, accepted)

	made := f.agentsOfEveryKind(t)
	for _, kind := range accepted {
		assert.Contains(t, made, kind, "agentsOfEveryKind makes no %q agent", kind)
	}
}
