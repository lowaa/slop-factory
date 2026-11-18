package agent_test

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
)

// roleFixture is one database with two tenants, each with the agent and
// role services scoped to it, against the real stores: the rules that
// matter here -- uniqueness, cascades, tenancy -- live in Postgres.
type roleFixture struct {
	database *db.DB
	tenantA  uuid.UUID
	a, b     context.Context
	agents   *agent.Service
	roles    *agent.RoleService
}

func newRoleFixture(t *testing.T) *roleFixture {
	t.Helper()
	database := dbtest.WithDB(t)
	tenantA := dbtest.NewTenant(t, database)
	return &roleFixture{
		database: database,
		tenantA:  tenantA,
		a:        db.WithTenant(context.Background(), tenantA),
		b:        db.WithTenant(context.Background(), dbtest.NewTenant(t, database)),
		agents:   agent.NewService(agent.NewPostgresStore(database), &fakeReleaser{}, nil, slog.DiscardHandler),
		roles:    agent.NewRoleService(agent.NewPostgresRoleStore(database), slog.DiscardHandler),
	}
}

func (f *roleFixture) agent(t *testing.T, ctx context.Context, name string) *agent.Agent {
	t.Helper()
	created, err := f.agents.Create(ctx, name)
	require.NoError(t, err)
	return created
}

func (f *roleFixture) role(t *testing.T, ctx context.Context, name string) *agent.Role {
	t.Helper()
	created, err := f.roles.Create(ctx, testPerson(), name, "")
	require.NoError(t, err)
	return created
}

func roleNames(roles []agent.Role) []string {
	names := make([]string, len(roles))
	for i, r := range roles {
		names[i] = r.Name
	}
	return names
}

func ptr[T any](v T) *T { return &v }

func TestRoles_CreateListUpdateDelete(t *testing.T) {
	f := newRoleFixture(t)

	created, err := f.roles.Create(f.a, testPerson(), "  reviewer ", " reads PRs ")
	require.NoError(t, err)
	assert.Equal(t, "reviewer", created.Name, "trimmed")
	assert.Equal(t, "reads PRs", created.Description)
	f.role(t, f.a, "deployer")

	listed, err := f.roles.List(f.a)
	require.NoError(t, err)
	assert.Equal(t, []string{"deployer", "reviewer"}, roleNames(listed), "sorted by name")

	renamed, err := f.roles.Update(f.a, testPerson(), created.ID, &agent.RoleParams{Name: ptr("code-reviewer")})
	require.NoError(t, err)
	assert.Equal(t, "code-reviewer", renamed.Name)
	assert.Equal(t, "reads PRs", renamed.Description, "a field left nil is left alone")

	described, err := f.roles.Update(f.a, testPerson(), created.ID, &agent.RoleParams{Description: ptr("")})
	require.NoError(t, err)
	assert.Equal(t, "code-reviewer", described.Name)
	assert.Empty(t, described.Description, "a description can be cleared")

	require.NoError(t, f.roles.Delete(f.a, testPerson(), created.ID, false))
	listed, err = f.roles.List(f.a)
	require.NoError(t, err)
	assert.Equal(t, []string{"deployer"}, roleNames(listed))

	assert.ErrorIs(t, f.roles.Delete(f.a, testPerson(), created.ID, false), agent.ErrRoleNotFound)
	_, err = f.roles.Update(f.a, testPerson(), created.ID, &agent.RoleParams{Name: ptr("x")})
	assert.ErrorIs(t, err, agent.ErrRoleNotFound)
}

func TestRoles_NewTenantHasNone(t *testing.T) {
	f := newRoleFixture(t)
	listed, err := f.roles.List(f.a)
	require.NoError(t, err)
	assert.Empty(t, listed)
}

func TestRoles_RefuseABadName(t *testing.T) {
	f := newRoleFixture(t)
	for _, name := range []string{"", "   ", strings.Repeat("x", 65), "two\nlines", "tab\there"} {
		_, err := f.roles.Create(f.a, testPerson(), name, "")
		assert.ErrorIs(t, err, agent.ErrRoleInvalid, "%q", name)
	}
	_, err := f.roles.Create(f.a, testPerson(), "ok", strings.Repeat("d", 501))
	assert.ErrorIs(t, err, agent.ErrRoleInvalid, "description too long")

	r := f.role(t, f.a, "ok")
	_, err = f.roles.Update(f.a, testPerson(), r.ID, &agent.RoleParams{Name: ptr(" ")})
	assert.ErrorIs(t, err, agent.ErrRoleInvalid, "an edit is held to the same rules")
}

func TestRoles_NamesAreUniquePerTenant(t *testing.T) {
	f := newRoleFixture(t)
	f.role(t, f.a, "reviewer")
	other := f.role(t, f.a, "deployer")

	_, err := f.roles.Create(f.a, testPerson(), "reviewer", "")
	assert.ErrorIs(t, err, agent.ErrRoleNameTaken)
	_, err = f.roles.Update(f.a, testPerson(), other.ID, &agent.RoleParams{Name: ptr("reviewer")})
	assert.ErrorIs(t, err, agent.ErrRoleNameTaken)

	_, err = f.roles.Create(f.b, testPerson(), "reviewer", "")
	assert.NoError(t, err, "another tenant's role of the same name is no conflict")
}

func TestRoles_SetAgentRolesReplacesTheWholeSet(t *testing.T) {
	f := newRoleFixture(t)
	builder := f.agent(t, f.a, "builder")
	reviewer, deployer := f.role(t, f.a, "reviewer"), f.role(t, f.a, "deployer")

	got, err := f.roles.SetAgentRoles(f.a, testPerson(), builder.ID, []uuid.UUID{reviewer.ID, deployer.ID, reviewer.ID})
	require.NoError(t, err)
	assert.Equal(t, []string{"deployer", "reviewer"}, roleNames(got), "duplicates ignored")

	got, err = f.roles.SetAgentRoles(f.a, testPerson(), builder.ID, []uuid.UUID{deployer.ID})
	require.NoError(t, err)
	assert.Equal(t, []string{"deployer"}, roleNames(got))

	names, err := f.roles.RoleNames(f.a, builder.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"deployer"}, names)

	got, err = f.roles.SetAgentRoles(f.a, testPerson(), builder.ID, nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// A set naming a role that doesn't exist is refused whole: the agent keeps
// what it had rather than getting the half that was valid.
func TestRoles_SetAgentRolesWithAnUnknownRoleChangesNothing(t *testing.T) {
	f := newRoleFixture(t)
	builder := f.agent(t, f.a, "builder")
	reviewer, deployer := f.role(t, f.a, "reviewer"), f.role(t, f.a, "deployer")
	_, err := f.roles.SetAgentRoles(f.a, testPerson(), builder.ID, []uuid.UUID{reviewer.ID})
	require.NoError(t, err)

	_, err = f.roles.SetAgentRoles(f.a, testPerson(), builder.ID, []uuid.UUID{deployer.ID, uuid.New()})
	assert.ErrorIs(t, err, agent.ErrRoleNotFound)

	names, err := f.roles.RoleNames(f.a, builder.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"reviewer"}, names)

	_, err = f.roles.SetAgentRoles(f.a, testPerson(), uuid.New(), []uuid.UUID{reviewer.ID})
	assert.ErrorIs(t, err, agent.ErrNotFound, "an unknown agent")
}

func TestRoles_RolesByAgentCoversTheWholeRoster(t *testing.T) {
	f := newRoleFixture(t)
	one, two, none := f.agent(t, f.a, "one"), f.agent(t, f.a, "two"), f.agent(t, f.a, "none")
	reviewer, deployer := f.role(t, f.a, "reviewer"), f.role(t, f.a, "deployer")
	_, err := f.roles.SetAgentRoles(f.a, testPerson(), one.ID, []uuid.UUID{reviewer.ID, deployer.ID})
	require.NoError(t, err)
	_, err = f.roles.SetAgentRoles(f.a, testPerson(), two.ID, []uuid.UUID{reviewer.ID})
	require.NoError(t, err)

	byAgent, err := f.roles.RolesByAgent(f.a)
	require.NoError(t, err)
	assert.Equal(t, []string{"deployer", "reviewer"}, roleNames(byAgent[one.ID]))
	assert.Equal(t, []string{"reviewer"}, roleNames(byAgent[two.ID]))
	assert.Empty(t, byAgent[none.ID])
}

func TestRoles_DeletingARoleTakesItFromEveryAgent(t *testing.T) {
	f := newRoleFixture(t)
	builder := f.agent(t, f.a, "builder")
	reviewer, deployer := f.role(t, f.a, "reviewer"), f.role(t, f.a, "deployer")
	_, err := f.roles.SetAgentRoles(f.a, testPerson(), builder.ID, []uuid.UUID{reviewer.ID, deployer.ID})
	require.NoError(t, err)

	require.NoError(t, f.roles.Delete(f.a, testPerson(), reviewer.ID, false))

	names, err := f.roles.RoleNames(f.a, builder.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"deployer"}, names)
}

// Nothing deletes an agent today, but its memberships must not be what
// stops one from ever being deleted.
func TestRoles_DeletingAnAgentDropsItsMemberships(t *testing.T) {
	f := newRoleFixture(t)
	builder := f.agent(t, f.a, "builder")
	reviewer := f.role(t, f.a, "reviewer")
	_, err := f.roles.SetAgentRoles(f.a, testPerson(), builder.ID, []uuid.UUID{reviewer.ID})
	require.NoError(t, err)

	_, err = f.database.Exec(f.a, `DELETE FROM agents WHERE id = $1`, builder.ID)
	require.NoError(t, err)

	var left int
	require.NoError(t, f.database.QueryRow(f.a, `SELECT count(*) FROM agent_role_members`).Scan(&left))
	assert.Zero(t, left)
	listed, err := f.roles.List(f.a)
	require.NoError(t, err)
	assert.Equal(t, []string{"reviewer"}, roleNames(listed), "the role itself stays")
}

// Row level security hides another tenant's roles entirely: they are not
// listed, can't be edited, deleted or assigned, and another tenant's
// agent can't be given roles.
func TestRoles_AreInvisibleToOtherTenants(t *testing.T) {
	f := newRoleFixture(t)
	aAgent := f.agent(t, f.a, "builder")
	aRole := f.role(t, f.a, "reviewer")
	_, err := f.roles.SetAgentRoles(f.a, testPerson(), aAgent.ID, []uuid.UUID{aRole.ID})
	require.NoError(t, err)
	bAgent := f.agent(t, f.b, "builder")

	listed, err := f.roles.List(f.b)
	require.NoError(t, err)
	assert.Empty(t, listed)
	byAgent, err := f.roles.RolesByAgent(f.b)
	require.NoError(t, err)
	assert.Empty(t, byAgent)
	names, err := f.roles.RoleNames(f.b, aAgent.ID)
	require.NoError(t, err)
	assert.Empty(t, names)

	_, err = f.roles.Update(f.b, testPerson(), aRole.ID, &agent.RoleParams{Name: ptr("mine")})
	assert.ErrorIs(t, err, agent.ErrRoleNotFound)
	assert.ErrorIs(t, f.roles.Delete(f.b, testPerson(), aRole.ID, false), agent.ErrRoleNotFound)

	_, err = f.roles.SetAgentRoles(f.b, testPerson(), bAgent.ID, []uuid.UUID{aRole.ID})
	assert.ErrorIs(t, err, agent.ErrRoleNotFound, "another tenant's role can't be assigned")
	_, err = f.roles.SetAgentRoles(f.b, testPerson(), aAgent.ID, nil)
	assert.ErrorIs(t, err, agent.ErrNotFound, "another tenant's agent can't be given roles, or have them taken")

	names, err = f.roles.RoleNames(f.a, aAgent.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"reviewer"}, names, "a's agent kept its role")
	listed, err = f.roles.List(f.a)
	require.NoError(t, err)
	assert.Equal(t, []string{"reviewer"}, roleNames(listed))
}

// The composite foreign key refuses a membership pointing at another
// tenant's role even from a raw insert that skips the service's check.
func TestRoles_AMembershipCannotReferenceAnotherTenantsRole(t *testing.T) {
	f := newRoleFixture(t)
	bRole := f.role(t, f.b, "reviewer")
	aAgent := f.agent(t, f.a, "builder")

	_, err := f.database.Exec(f.a, `INSERT INTO agent_role_members (agent_id, role_id) VALUES ($1, $2)`, aAgent.ID, bRole.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "foreign key")
}

// Replacing an agent's roles while one of them is deleted must end with
// one of them winning, never a deadlock: the two take the role and the
// agent's memberships in opposite orders unless SetAgentRoles locks the
// roles first.
func TestRoles_SetAgentRolesRacingADeleteDoesNotDeadlock(t *testing.T) {
	f := newRoleFixture(t)
	builder := f.agent(t, f.a, "builder")

	for i := 0; i < 30; i++ {
		doomed := f.role(t, f.a, fmt.Sprintf("doomed-%d", i))
		_, err := f.roles.SetAgentRoles(f.a, testPerson(), builder.ID, []uuid.UUID{doomed.ID})
		require.NoError(t, err)

		var wg sync.WaitGroup
		var setErr, deleteErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, setErr = f.roles.SetAgentRoles(f.a, testPerson(), builder.ID, []uuid.UUID{doomed.ID})
		}()
		go func() {
			defer wg.Done()
			deleteErr = f.roles.Delete(f.a, testPerson(), doomed.ID, false)
		}()
		wg.Wait()

		require.NoError(t, deleteErr)
		if setErr != nil {
			require.ErrorIs(t, setErr, agent.ErrRoleNotFound)
		}
		names, err := f.roles.RoleNames(f.a, builder.ID)
		require.NoError(t, err)
		require.Empty(t, names, "whichever ran first, the deleted role is gone from the agent")
	}
}
