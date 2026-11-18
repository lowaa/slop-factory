package ticket_test

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// roleFixture is the ticket fixture plus the real role service: the claim
// predicate reads agent_role_members, which only internal/agent writes.
type roleFixture struct {
	*fixture
	roles *agent.RoleService
}

func newRoleFixture(t *testing.T) *roleFixture {
	t.Helper()
	f := newFixture(t)
	return &roleFixture{fixture: f, roles: agent.NewRoleService(agent.NewPostgresRoleStore(f.database), slog.DiscardHandler)}
}

func (f *roleFixture) role(t *testing.T, name string) uuid.UUID {
	t.Helper()
	created, err := f.roles.Create(f.ctx, testPerson(), name, "")
	require.NoError(t, err)
	return created.ID
}

func (f *roleFixture) give(t *testing.T, agentID uuid.UUID, roleIDs ...uuid.UUID) {
	t.Helper()
	_, err := f.roles.SetAgentRoles(f.ctx, testPerson(), agentID, roleIDs)
	require.NoError(t, err)
}

func (f *roleFixture) requiring(t *testing.T, title string, match model.RoleMatch, roleIDs ...uuid.UUID) *ticket.Ticket {
	t.Helper()
	created, err := f.svc.Create(f.ctx, &ticket.CreateParams{
		Title: title,
		Roles: ticket.RoleRequirement{IDs: roleIDs, Match: match},
	})
	require.NoError(t, err)
	return created
}

// claimed is the ticket the agent is granted by an undirected claim, or 0.
func (f *roleFixture) claimed(t *testing.T, agentID uuid.UUID) int64 {
	t.Helper()
	result, err := f.svc.Claim(f.ctx, agentID, nil)
	require.NoError(t, err)
	if result.Ticket == nil {
		return 0
	}
	return result.Ticket.ID
}

// peeked is what the Stop hook's probe would report for the agent, or 0.
func (f *roleFixture) peeked(t *testing.T, agentID uuid.UUID) int64 {
	t.Helper()
	result, err := f.svc.Peek(f.ctx, agentID, nil)
	require.NoError(t, err)
	if result.Ticket == nil {
		return 0
	}
	return result.Ticket.ID
}

func roleRefNames(refs []model.RoleRef) []string {
	names := make([]string, len(refs))
	for i, r := range refs {
		names[i] = r.Name
	}
	return names
}

func TestRequiredRoles_OnlyAnAgentWithTheRoleClaims(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	deploy := f.role(t, "deploy")
	holder, other := f.agent(t, "holder"), f.agent(t, "other")
	f.give(t, holder, deploy)
	scoped := f.requiring(t, "Ship it", model.RoleMatchAny, deploy)

	assert.Zero(t, f.peeked(t, other), "the probe offers the agent nothing it can't claim")
	assert.Zero(t, f.claimed(t, other), "an agent without the role is not granted it")
	assert.Equal(t, scoped.ID, f.peeked(t, holder))
	assert.Equal(t, scoped.ID, f.claimed(t, holder))
}

// A ticket requiring no roles is open to every agent, including ones that
// have roles.
func TestRequiredRoles_AnUnscopedTicketIsOpenToAgentsWithRoles(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	holder := f.agent(t, "holder")
	f.give(t, holder, f.role(t, "deploy"))
	open := f.ticket(t, "Anyone")

	assert.Equal(t, open.ID, f.claimed(t, holder))
}

func TestRequiredRoles_AnyNeedsOneAllNeedsEvery(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	deploy, review := f.role(t, "deploy"), f.role(t, "review")
	justDeploy, both, neither := f.agent(t, "just-deploy"), f.agent(t, "both"), f.agent(t, "neither")
	f.give(t, justDeploy, deploy)
	f.give(t, both, deploy, review)

	all := f.requiring(t, "All of them", model.RoleMatchAll, deploy, review)
	assert.Zero(t, f.peeked(t, justDeploy), "all: one of two roles is not enough")
	assert.Zero(t, f.claimed(t, justDeploy))
	assert.Zero(t, f.claimed(t, neither))
	assert.Equal(t, all.ID, f.claimed(t, both))

	anyOf := f.requiring(t, "Any of them", model.RoleMatchAny, deploy, review)
	assert.Zero(t, f.claimed(t, neither), "any: still needs one")
	assert.Equal(t, anyOf.ID, f.claimed(t, justDeploy), "any: one of two roles is enough")
}

func TestRequiredRoles_MatchDefaultsToAny(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	deploy, review := f.role(t, "deploy"), f.role(t, "review")
	justDeploy := f.agent(t, "just-deploy")
	f.give(t, justDeploy, deploy)

	created := f.requiring(t, "Default", "", deploy, review)
	assert.Equal(t, model.RoleMatchAny, created.RoleMatch)
	assert.Equal(t, []string{"deploy", "review"}, roleRefNames(created.RequiredRoles))
	assert.Equal(t, created.ID, f.claimed(t, justDeploy))
}

func TestRequiredRoles_AnUnknownMatchIsRefused(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	deploy := f.role(t, "deploy")

	_, err := f.svc.Create(f.ctx, &ticket.CreateParams{
		Title: "Bad", Roles: ticket.RoleRequirement{IDs: []uuid.UUID{deploy}, Match: "some"},
	})
	assert.ErrorIs(t, err, ticket.ErrInvalidEnum)
}

// Taking an agent's role away stops it claiming the next ticket that needs
// it -- but not the one it already holds: roles affect future claims only.
func TestRequiredRoles_RemovingARoleStopsFutureClaimsButKeepsAHeldLease(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	deploy := f.role(t, "deploy")
	holder := f.agent(t, "holder")
	f.give(t, holder, deploy)
	first := f.requiring(t, "First", model.RoleMatchAny, deploy)
	second := f.requiring(t, "Second", model.RoleMatchAny, deploy)

	require.Equal(t, first.ID, f.claimed(t, holder))
	f.give(t, holder)

	require.NoError(t, f.svc.Heartbeat(f.ctx, holder), "the held lease survives losing the role")
	lease, err := f.svc.LeaseFor(f.ctx, holder)
	require.NoError(t, err)
	assert.Equal(t, first.ID, lease.TicketID)
	_, err = f.svc.Done(f.ctx, holder, &ticket.DoneParams{Summary: "done"})
	require.NoError(t, err, "and can still be finished")

	assert.Zero(t, f.peeked(t, holder))
	assert.Zero(t, f.claimed(t, holder), "but the next ticket needing the role is no longer its")
	_ = second
}

// Tightening a held ticket's requirement doesn't take it away either.
func TestRequiredRoles_ChangingAHeldTicketsRolesKeepsItsLease(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	holder := f.agent(t, "holder")
	held := f.ticket(t, "Held")
	require.Equal(t, held.ID, f.claimed(t, holder))

	_, err := f.svc.SetRequiredRoles(f.ctx, testPerson(), held.ID, &ticket.RoleRequirement{IDs: []uuid.UUID{f.role(t, "deploy")}})
	require.NoError(t, err)
	require.NoError(t, f.svc.Heartbeat(f.ctx, holder))
}

// A ticket handed back after a discussion is still held for the agent that
// asked -- but only if it may claim it: affinity is no way around a role.
func TestRequiredRoles_ResumingAfterADiscussionStillNeedsTheRole(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	deploy := f.role(t, "deploy")
	asker := f.agent(t, "asker")
	f.give(t, asker, deploy)
	scoped := f.requiring(t, "Ship it", model.RoleMatchAny, deploy)

	require.Equal(t, scoped.ID, f.claimed(t, asker))
	f.discuss(t, asker)
	f.humanReply(t, scoped.ID)
	f.give(t, asker)

	assert.Zero(t, f.peeked(t, asker))
	assert.Zero(t, f.claimed(t, asker), "first claim on the answer does not stand in for the role")
}

// A claim of one specific ticket the agent lacks the role for says so,
// rather than reporting an empty queue.
func TestRequiredRoles_ADirectedClaimNamesTheMissingRole(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	deploy, review := f.role(t, "deploy"), f.role(t, "review")
	other := f.agent(t, "other")
	f.give(t, other, review)

	single := f.requiring(t, "One role", model.RoleMatchAny, deploy)
	_, err := f.svc.Claim(f.ctx, other, &single.ID)
	var missing *ticket.MissingRoleError
	require.ErrorAs(t, err, &missing)
	assert.ErrorIs(t, err, ticket.ErrMissingRole)
	assert.Equal(t, single.ID, missing.TicketID)
	assert.Contains(t, err.Error(), fmt.Sprintf("#%d needs role deploy", single.ID))

	all := f.requiring(t, "Every role", model.RoleMatchAll, deploy, review)
	_, err = f.svc.Claim(f.ctx, other, &all.ID)
	assert.ErrorContains(t, err, fmt.Sprintf("#%d needs all of the roles deploy, review", all.ID),
		"all: having one of them is still refused, and says every one is needed")

	// And nothing was granted.
	_, err = f.svc.LeaseFor(f.ctx, other)
	assert.ErrorIs(t, err, ticket.ErrNoLease)
}

// A directed claim refused for some other reason is not blamed on roles:
// not for an agent that meets them, nor for one that doesn't when the
// ticket isn't claimable by anyone.
func TestRequiredRoles_ADirectedClaimOfAnUnclaimableTicketIsNotARoleRefusal(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	deploy := f.role(t, "deploy")
	holder, other := f.agent(t, "holder"), f.agent(t, "other")
	f.give(t, holder, deploy)
	scoped := f.preparing(t, "Still shaping")
	_, err := f.svc.SetRequiredRoles(f.ctx, testPerson(), scoped.ID, &ticket.RoleRequirement{IDs: []uuid.UUID{deploy}})
	require.NoError(t, err)

	for _, agentID := range []uuid.UUID{holder, other} {
		result, err := f.svc.Claim(f.ctx, agentID, &scoped.ID)
		require.NoError(t, err)
		assert.Nil(t, result.Ticket)
	}
}

// The store files an ordinary ticket even for a caller that left the
// requirement's match empty without going through Service.
func TestRequiredRoles_TheStoreDefaultsAnEmptyMatch(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	created, err := newStore(f.database).CreateTicket(f.ctx, &ticket.CreateParams{
		Title: "Direct", Priority: model.PriorityNormal, Status: ticket.StatusOpen,
	})
	require.NoError(t, err)
	assert.Equal(t, model.RoleMatchAny, created.RoleMatch)
}

func TestRequiredRoles_SetReplacesAndClears(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	deploy, review := f.role(t, "deploy"), f.role(t, "review")
	other := f.agent(t, "other")
	scoped := f.requiring(t, "Scoped", model.RoleMatchAll, deploy)

	updated, err := f.svc.SetRequiredRoles(f.ctx, testPerson(), scoped.ID, &ticket.RoleRequirement{IDs: []uuid.UUID{review, review}})
	require.NoError(t, err)
	assert.Equal(t, []string{"review"}, roleRefNames(updated.RequiredRoles), "replaced, duplicates dropped")
	assert.Equal(t, model.RoleMatchAny, updated.RoleMatch, "an omitted match is any")

	assert.Zero(t, f.claimed(t, other))
	cleared, err := f.svc.SetRequiredRoles(f.ctx, testPerson(), scoped.ID, &ticket.RoleRequirement{})
	require.NoError(t, err)
	assert.Empty(t, cleared.RequiredRoles)
	assert.Equal(t, scoped.ID, f.claimed(t, other), "cleared, it is open to every agent")
}

func TestRequiredRoles_SetOnAMissingTicketIsNotFound(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	_, err := f.svc.SetRequiredRoles(f.ctx, testPerson(), 9999, &ticket.RoleRequirement{})
	assert.ErrorIs(t, err, ticket.ErrNotFound)
}

func TestRequiredRoles_ListAndGetCarryThem(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	deploy, review := f.role(t, "deploy"), f.role(t, "review")
	scoped := f.requiring(t, "Scoped", model.RoleMatchAll, review, deploy)
	plain := f.ticket(t, "Plain")

	got, err := f.svc.Get(f.ctx, scoped.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"deploy", "review"}, roleRefNames(got.RequiredRoles))
	assert.Equal(t, model.RoleMatchAll, got.RoleMatch)

	listed, err := f.svc.List(f.ctx, nil)
	require.NoError(t, err)
	byID := map[int64]ticket.Ticket{}
	for _, lt := range listed {
		byID[lt.ID] = lt
	}
	assert.Equal(t, []string{"deploy", "review"}, roleRefNames(byID[scoped.ID].RequiredRoles))
	assert.Equal(t, model.RoleMatchAll, byID[scoped.ID].RoleMatch)
	assert.Empty(t, byID[plain.ID].RequiredRoles)
	assert.NotNil(t, byID[plain.ID].RequiredRoles, "empty, not nil, so the wire says []")
}

// Agents file by role name. Names match exactly: "Deploy" is not "deploy".
func TestRequiredRoles_ByNameMatchesExactlyAndListsTheRealNames(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	deploy := f.role(t, "deploy")
	f.role(t, "review")
	filer := f.agent(t, "filer")

	created, err := f.svc.Create(f.ctx, &ticket.CreateParams{
		Title: "By name", CreatedByAgentID: &filer,
		Roles: ticket.RoleRequirement{Names: []string{"deploy"}},
	})
	require.NoError(t, err)
	require.Len(t, created.RequiredRoles, 1)
	assert.Equal(t, deploy, created.RequiredRoles[0].ID)

	_, err = f.svc.Create(f.ctx, &ticket.CreateParams{
		Title: "Wrong case", CreatedByAgentID: &filer,
		Roles: ticket.RoleRequirement{Names: []string{"deploy", "Deploy"}},
	})
	require.ErrorIs(t, err, ticket.ErrUnknownRole)
	assert.ErrorContains(t, err, `"Deploy"`)
	assert.ErrorContains(t, err, "deploy, review", "the refusal says what the roles are called")

	listed, err := f.svc.List(f.ctx, nil)
	require.NoError(t, err)
	assert.Len(t, listed, 1, "a refused requirement files nothing")
}

func TestRequiredRoles_AnotherTenantsRoleCannotBeRequired(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	otherTenant := db.WithTenant(context.Background(), dbtest.NewTenant(t, f.database))
	theirs, err := f.roles.Create(otherTenant, testPerson(), "deploy", "")
	require.NoError(t, err)

	_, err = f.svc.Create(f.ctx, &ticket.CreateParams{
		Title: "By id", Roles: ticket.RoleRequirement{IDs: []uuid.UUID{theirs.ID}},
	})
	assert.ErrorIs(t, err, ticket.ErrUnknownRole, "by id")
	_, err = f.svc.Create(f.ctx, &ticket.CreateParams{
		Title: "By name", Roles: ticket.RoleRequirement{Names: []string{"deploy"}},
	})
	assert.ErrorIs(t, err, ticket.ErrUnknownRole, "by name: their role of the same name is not ours")

	plain := f.ticket(t, "Plain")
	_, err = f.svc.SetRequiredRoles(f.ctx, testPerson(), plain.ID, &ticket.RoleRequirement{IDs: []uuid.UUID{theirs.ID}})
	assert.ErrorIs(t, err, ticket.ErrUnknownRole)

	// And the composite foreign key refuses it even from a raw insert.
	_, err = f.database.Exec(f.ctx,
		`INSERT INTO ticket_required_roles (ticket_id, role_id) VALUES ($1, $2)`, plain.ID, theirs.ID)
	assert.ErrorContains(t, err, "foreign key")
}

// Another tenant can neither give our agent a role nor have its same-named
// role stand in for ours: the membership can't be created, so the claim
// is refused.
func TestRequiredRoles_AnotherTenantsMembershipDoesNotCount(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	scoped := f.requiring(t, "Scoped", model.RoleMatchAny, f.role(t, "deploy"))
	holder := f.agent(t, "holder")

	otherTenant := db.WithTenant(context.Background(), dbtest.NewTenant(t, f.database))
	theirs, err := f.roles.Create(otherTenant, testPerson(), "deploy", "")
	require.NoError(t, err)
	_, err = f.roles.SetAgentRoles(otherTenant, testPerson(), holder, []uuid.UUID{theirs.ID})
	require.ErrorIs(t, err, agent.ErrNotFound, "another tenant can't give our agent roles")

	assert.Zero(t, f.claimed(t, holder))
	_, err = f.svc.Claim(f.ctx, holder, &scoped.ID)
	assert.ErrorIs(t, err, ticket.ErrMissingRole)
}

func TestRequiredRoles_NoEligibleAgentFollowsTheEnabledAgents(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	deploy := f.role(t, "deploy")
	scoped := f.requiring(t, "Scoped", model.RoleMatchAny, deploy)
	plain := f.ticket(t, "Plain")
	assert.True(t, scoped.NoEligibleAgent, "no agent has the role")
	assert.False(t, plain.NoEligibleAgent, "a ticket requiring nothing is never flagged")

	holder := f.agent(t, "holder")
	f.give(t, holder, deploy)
	assert.False(t, f.get(t, scoped.ID).NoEligibleAgent)

	_, err := f.agents.SetDisabled(f.ctx, holder, true)
	require.NoError(t, err)
	assert.True(t, f.get(t, scoped.ID).NoEligibleAgent, "a disabled agent claims nothing")

	listed, err := f.svc.List(f.ctx, nil)
	require.NoError(t, err)
	for _, lt := range listed {
		assert.Equal(t, lt.ID == scoped.ID, lt.NoEligibleAgent, "#%d on the board", lt.ID)
	}
}

func TestRequiredRoles_NoEligibleAgentUsesTheMatchMode(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	deploy, review := f.role(t, "deploy"), f.role(t, "review")
	f.give(t, f.agent(t, "deployer"), deploy)
	f.give(t, f.agent(t, "reviewer"), review)

	assert.False(t, f.requiring(t, "Any", model.RoleMatchAny, deploy, review).NoEligibleAgent)
	assert.True(t, f.requiring(t, "All", model.RoleMatchAll, deploy, review).NoEligibleAgent,
		"each role has an agent, but no one agent has both")
}

func TestRequiredRoles_DeletingARoleTicketsUseIsRefusedUnlessCleared(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	deploy, review := f.role(t, "deploy"), f.role(t, "review")
	onlyDeploy := f.requiring(t, "Only deploy", model.RoleMatchAny, deploy)
	both := f.requiring(t, "Both", model.RoleMatchAll, deploy, review)
	other := f.agent(t, "other")

	err := f.roles.Delete(f.ctx, testPerson(), deploy, false)
	var inUse *agent.RoleInUseError
	require.ErrorAs(t, err, &inUse)
	assert.ErrorIs(t, err, agent.ErrRoleInUse)
	ids := []int64{}
	for _, ref := range inUse.Tickets {
		ids = append(ids, ref.ID)
	}
	assert.Equal(t, []int64{onlyDeploy.ID, both.ID}, ids)
	listed, err := f.roles.List(f.ctx)
	require.NoError(t, err)
	assert.Len(t, listed, 2, "refused: the role is still there")
	assert.Equal(t, []string{"deploy"}, roleRefNames(f.get(t, onlyDeploy.ID).RequiredRoles), "and still required")

	require.NoError(t, f.roles.Delete(f.ctx, testPerson(), deploy, true))
	assert.Empty(t, f.get(t, onlyDeploy.ID).RequiredRoles)
	assert.Equal(t, []string{"review"}, roleRefNames(f.get(t, both.ID).RequiredRoles))
	assert.Equal(t, onlyDeploy.ID, f.claimed(t, other), "its only role cleared, the ticket is open to every agent")
}

// Even a delete that skipped the check can't loosen a ticket: the foreign
// key restricts rather than cascading.
func TestRequiredRoles_TheSchemaRefusesDeletingARequiredRole(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	deploy := f.role(t, "deploy")
	f.requiring(t, "Scoped", model.RoleMatchAny, deploy)

	_, err := f.database.Exec(f.ctx, `DELETE FROM agent_roles WHERE id = $1`, deploy)
	assert.ErrorContains(t, err, "foreign key")
}

// Replacing a ticket's roles while one of them is deleted with clearing
// must end with one of them winning, never a deadlock: without the roles
// locked first, the replacement holds the ticket's requirement rows and
// waits on the role, while the delete holds the role and waits on those
// rows.
func TestRequiredRoles_SetRacingAClearingDeleteDoesNotDeadlock(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	scoped := f.ticket(t, "Scoped")

	for i := 0; i < 30; i++ {
		doomed := f.role(t, fmt.Sprintf("doomed-%d", i))
		_, err := f.svc.SetRequiredRoles(f.ctx, testPerson(), scoped.ID, &ticket.RoleRequirement{IDs: []uuid.UUID{doomed}})
		require.NoError(t, err)

		var wg sync.WaitGroup
		var setErr, deleteErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, setErr = f.svc.SetRequiredRoles(f.ctx, testPerson(), scoped.ID, &ticket.RoleRequirement{IDs: []uuid.UUID{doomed}})
		}()
		go func() {
			defer wg.Done()
			deleteErr = f.roles.Delete(f.ctx, testPerson(), doomed, true)
		}()
		wg.Wait()

		require.NoError(t, deleteErr, "clearing, the delete goes ahead whichever ran first")
		if setErr != nil {
			// The delete ran first: the role is gone, so it can't be required.
			require.ErrorIs(t, setErr, ticket.ErrUnknownRole)
		}
		require.Empty(t, f.get(t, scoped.ID).RequiredRoles, "whichever ran first, the deleted role is gone from the ticket")
	}
}

// Loosening a restriction is a person's call, enforced by the service so no
// transport can skip it.
func TestRequiredRoles_SetRefusesACallerThatIsNotAPerson(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	deploy := f.role(t, "deploy")
	scoped := f.requiring(t, "Scoped", model.RoleMatchAll, deploy)

	_, err := f.svc.SetRequiredRoles(f.ctx, nil, scoped.ID, &ticket.RoleRequirement{})
	assert.ErrorIs(t, err, authn.ErrNotPerson)

	got, err := f.svc.Get(f.ctx, scoped.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"deploy"}, roleRefNames(got.RequiredRoles), "the restriction stands")
}
