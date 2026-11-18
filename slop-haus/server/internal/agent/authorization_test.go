package agent_test

import (
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

// Who may change roles, conductors and a supervisor's desired state is the
// services' to decide: a caller that is not a signed-in person is refused
// whatever transport it came by.

func TestRoleServiceRefusesACallerThatIsNotAPerson(t *testing.T) {
	f := newRoleFixture(t)
	existing := f.role(t, f.a, "reviewer")
	builder := f.agent(t, f.a, "builder")

	_, err := f.roles.Create(f.a, nil, "deployer", "")
	assert.ErrorIs(t, err, authn.ErrNotPerson)
	_, err = f.roles.Update(f.a, nil, existing.ID, &agent.RoleParams{Name: ptr("renamed")})
	assert.ErrorIs(t, err, authn.ErrNotPerson)
	_, err = f.roles.SetAgentRoles(f.a, nil, builder.ID, []uuid.UUID{existing.ID})
	assert.ErrorIs(t, err, authn.ErrNotPerson)
	assert.ErrorIs(t, f.roles.Delete(f.a, nil, existing.ID, false), authn.ErrNotPerson)

	roles, err := f.roles.List(f.a)
	require.NoError(t, err)
	require.Len(t, roles, 1, "nothing was created or deleted")
	assert.Equal(t, "reviewer", roles[0].Name, "nothing was renamed")
	held, err := f.roles.RolesFor(f.a, builder.ID)
	require.NoError(t, err)
	assert.Empty(t, held, "nothing was granted")
}

func TestConductorServiceRefusesACallerThatIsNotAPerson(t *testing.T) {
	f := newConductorFixture(t)
	c := f.register(t, f.owner, "mine")

	_, err := f.conductors.Register(f.ctx, nil, "theirs")
	assert.ErrorIs(t, err, authn.ErrNotPerson)
	_, err = f.conductors.ListOwned(f.ctx, nil, false)
	assert.ErrorIs(t, err, authn.ErrNotPerson)
	_, err = f.conductors.ListOthers(f.ctx, nil, false)
	assert.ErrorIs(t, err, authn.ErrNotPerson)
	_, err = f.conductors.AuthorizeOwner(f.ctx, nil, c.ID)
	assert.ErrorIs(t, err, authn.ErrNotPerson)
	_, err = f.conductors.MayChangeConfig(f.ctx, nil, c.ID)
	assert.ErrorIs(t, err, authn.ErrNotPerson)
	_, err = f.conductors.SetDisabled(f.ctx, nil, c.ID, true)
	assert.ErrorIs(t, err, authn.ErrNotPerson)
	_, err = f.conductors.SetArchived(f.ctx, nil, c.ID, true)
	assert.ErrorIs(t, err, authn.ErrNotPerson)
	_, err = f.agents.SetArchived(f.ctx, nil, c.ID, true)
	assert.ErrorIs(t, err, authn.ErrNotPerson)

	stored, err := f.agents.Get(f.ctx, c.ID)
	require.NoError(t, err)
	assert.Nil(t, stored.DisabledAt)
	assert.Nil(t, stored.ArchivedAt)
}

func TestProvisionServiceRefusesACallerThatIsNotAPerson(t *testing.T) {
	f := newConductorFixture(t)
	provision := agent.NewProvisionService(agent.NewPostgresProvisionStore(f.database), f.agents, tenantRoles{user.NewPostgresStore(f.database)}, nil, slog.DiscardHandler)

	_, err := provision.OwnerView(f.ctx, nil, uuid.New())
	assert.ErrorIs(t, err, authn.ErrNotPerson)
	_, err = provision.Edit(f.ctx, nil, uuid.New(), 0, nil)
	assert.ErrorIs(t, err, authn.ErrNotPerson)
}

func TestSupervisorCallerIsOnlyALiveSupervisorsOwnCredential(t *testing.T) {
	f := newSupervisorFixture(t)
	sup := f.supervisor(t, "sup", 2)
	worker, err := f.agents.Create(f.ctx, "worker")
	require.NoError(t, err)
	disabled := f.supervisor(t, "off", 2)
	_, err = f.database.Exec(f.ctx, `UPDATE agents SET disabled_at = now() WHERE id = $1`, disabled.ID)
	require.NoError(t, err)
	disabled, err = f.agents.Get(f.ctx, disabled.ID)
	require.NoError(t, err)

	got, err := f.agents.SupervisorCaller(f.ctx, sup)
	require.NoError(t, err)
	assert.Equal(t, sup.ID, got.ID)

	for name, caller := range map[string]authn.Identity{
		"read-only credential": readOnlyIdentity{sup},
		"no identity":          nil,
		"worker":               worker,
		"disabled supervisor":  disabled,
		"unknown supervisor":   &agent.Agent{ID: uuid.New(), Kind: agent.KindSupervisor},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.agents.SupervisorCaller(f.ctx, caller)
			assert.ErrorIs(t, err, agent.ErrNotSupervisorCaller)
		})
	}
}

func TestSetWorkerRolesIsAPersonsCallAndOnlyForWorkers(t *testing.T) {
	f := newConductorFixture(t)
	roles := agent.NewRoleService(agent.NewPostgresRoleStore(f.database), slog.DiscardHandler)
	role, err := roles.Create(f.ctx, f.owner, "reviewer", "")
	require.NoError(t, err)
	worker, err := f.agents.Create(f.ctx, "worker")
	require.NoError(t, err)
	conductor := f.register(t, f.owner, "conductor")

	_, err = f.agents.SetWorkerRoles(f.ctx, nil, roles, worker.ID, []uuid.UUID{role.ID})
	assert.ErrorIs(t, err, authn.ErrNotPerson)
	_, err = f.agents.SetWorkerRoles(f.ctx, f.owner, roles, conductor.ID, []uuid.UUID{role.ID})
	assert.ErrorIs(t, err, agent.ErrNotFound, "a conductor's roles are not for a person to set")

	_, err = f.agents.SetWorkerRoles(f.ctx, f.owner, roles, worker.ID, []uuid.UUID{role.ID})
	require.NoError(t, err)
	names, err := roles.RoleNames(f.ctx, worker.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"reviewer"}, names)
}

// readOnlyIdentity is a live supervisor's credential that may read and not write.
type readOnlyIdentity struct{ *agent.Agent }

func (readOnlyIdentity) ReadOnly() bool { return true }
