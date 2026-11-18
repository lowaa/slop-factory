package manifest_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/workspace"
)

type tenantFixture struct {
	t       *testing.T
	db      *db.DB
	id      uuid.UUID
	ctx     context.Context
	owner   *authn.User
	member  *authn.User
	svc     *manifest.Service
	tickets *ticket.Service
	agents  *agent.Service
	layouts *agent.PostgresLayoutStore
}

type env struct {
	t        *testing.T
	database *db.DB
}

func newEnv(t *testing.T) *env { return &env{t: t, database: dbtest.WithDB(t)} }

func (e *env) join(tenant uuid.UUID, role string) *authn.User {
	e.t.Helper()
	id := dbtest.NewUser(e.t, e.database)
	_, err := e.database.Global().Exec(context.Background(),
		`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES ($1, $2, $3, $4)`,
		uuid.New(), tenant, id, role)
	require.NoError(e.t, err)
	return &authn.User{ID: id, TenantID: tenant}
}

func (e *env) tenant() *tenantFixture {
	e.t.Helper()
	id := dbtest.NewTenant(e.t, e.database)
	ctx := db.WithTenant(context.Background(), id)
	tickets := ticket.NewService(ticket.NewPostgresStore(e.database), slog.DiscardHandler)
	agents := agent.NewService(agent.NewPostgresStore(e.database), tickets, nil, slog.DiscardHandler)
	layouts := agent.NewPostgresLayoutStore(e.database)
	return &tenantFixture{
		t: e.t, db: e.database, id: id, ctx: ctx,
		owner: e.join(id, "owner"), member: e.join(id, "member"),
		svc: manifest.NewService(manifest.NewPostgresStore(e.database),
			user.NewPostgresStore(e.database), agents, layouts, slog.DiscardHandler),
		tickets: tickets, agents: agents, layouts: layouts,
	}
}

func (f *tenantFixture) newAgent(name string, l *agent.Layout) *agent.Agent {
	f.t.Helper()
	a, err := f.agents.Create(f.ctx, name)
	require.NoError(f.t, err)
	if l != nil {
		require.NoError(f.t, f.layouts.SaveLayout(f.ctx, a.ID, l))
	}
	return a
}

var twoRepos = []manifest.Repo{
	{Path: "app", Remote: "git@github.com:acme/app.git"},
	{Path: "deploy", Remote: "https://github.com/acme/deploy"},
}

func TestStore_ARoundTripIsPerTenant(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()

	got, err := a.svc.Get(a.ctx)
	require.NoError(t, err)
	assert.Nil(t, got, "a tenant that has declared nothing has no manifest, not an empty one")

	set, err := a.svc.Set(a.ctx, a.owner, twoRepos)
	require.NoError(t, err)
	got, err = a.svc.Get(a.ctx)
	require.NoError(t, err)
	assert.Equal(t, []manifest.Repo{
		{Path: "app", Remote: "github.com/acme/app"}, {Path: "deploy", Remote: "github.com/acme/deploy"},
	}, got.Repos, "stored normalised")
	assert.Equal(t, set.UpdatedAt, got.UpdatedAt)

	other, err := b.svc.Get(b.ctx)
	require.NoError(t, err)
	assert.Nil(t, other, "another tenant cannot see it")

	replaced, err := a.svc.Set(a.ctx, a.owner, []manifest.Repo{{Path: "app", Remote: "github.com/acme/mono"}})
	require.NoError(t, err)
	assert.Equal(t, []manifest.Repo{{Path: "app", Remote: "github.com/acme/mono"}}, replaced.Repos, "a second write replaces the first")

	require.NoError(t, a.svc.Clear(a.ctx, a.owner))
	got, err = a.svc.Get(a.ctx)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestStore_ATenantCannotWriteAnotherTenantsManifest(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	_, err := a.svc.Set(a.ctx, a.owner, twoRepos)
	require.NoError(t, err)

	// B's owner, acting in B's context, clears and rewrites: A's row is
	// out of reach of both.
	require.NoError(t, b.svc.Clear(b.ctx, b.owner))
	_, err = b.svc.Set(b.ctx, b.owner, []manifest.Repo{{Path: "app", Remote: "github.com/b/b"}})
	require.NoError(t, err)

	got, err := a.svc.Get(a.ctx)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "github.com/acme/app", got.Repos[0].Remote)
}

func TestService_OnlyOwnersChangeTheManifest(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	_, err := a.svc.Set(a.ctx, a.owner, twoRepos)
	require.NoError(t, err)

	for name, who := range map[string]*authn.User{
		"a member":                       a.member,
		"another tenant's owner":         {ID: b.owner.ID, TenantID: a.id},
		"a user with no membership here": {ID: dbtest.NewUser(t, e.database), TenantID: a.id},
	} {
		_, err := a.svc.Set(a.ctx, who, []manifest.Repo{{Path: "app", Remote: "github.com/evil/repo"}})
		assert.ErrorIs(t, err, manifest.ErrNotOwner, name)
		assert.ErrorIs(t, a.svc.Clear(a.ctx, who), manifest.ErrNotOwner, name)
		can, err := a.svc.CanEdit(a.ctx, who)
		require.NoError(t, err)
		assert.False(t, can, name)
	}
	got, err := a.svc.Get(a.ctx)
	require.NoError(t, err)
	require.NotNil(t, got, "nothing a non-owner did changed it")
	assert.Equal(t, "github.com/acme/app", got.Repos[0].Remote)

	can, err := a.svc.CanEdit(a.ctx, a.owner)
	require.NoError(t, err)
	assert.True(t, can)
}

func TestService_AnInvalidManifestIsNotStored(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	_, err := a.svc.Set(a.ctx, a.owner, []manifest.Repo{{Path: "a/b", Remote: "github.com/acme/app"}})
	require.ErrorIs(t, err, manifest.ErrInvalid)
	got, err := a.svc.Get(a.ctx)
	require.NoError(t, err)
	assert.Nil(t, got)
}

// Adopting is a statement about the layout, not a change to any ticket:
// every scope is already relative to the root, so open work carries on.
func TestService_AdoptingLeavesOpenTicketsUntouched(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	worker := a.newAgent("worker", nil)

	open, err := a.tickets.Create(a.ctx, &ticket.CreateParams{Title: "open", Paths: []model.Path{{Path: "elsewhere/"}}})
	require.NoError(t, err)
	held, err := a.tickets.Create(a.ctx, &ticket.CreateParams{Title: "held", Paths: []model.Path{{Path: "app/src/"}}})
	require.NoError(t, err)
	claimed, err := a.tickets.Claim(a.ctx, worker.ID, &held.ID)
	require.NoError(t, err)
	require.NotNil(t, claimed.Ticket)

	snapshot := func() []ticket.Ticket {
		list, err := a.tickets.List(a.ctx, &ticket.ListFilter{})
		require.NoError(t, err)
		return list
	}
	before := snapshot()
	require.Len(t, before, 2)

	_, err = a.svc.Set(a.ctx, a.owner, []manifest.Repo{{Path: "app", Remote: "github.com/acme/app"}})
	require.NoError(t, err)

	assert.Equal(t, before, snapshot(), "statuses, scopes, revisions and timestamps are all as they were")
	still, err := a.tickets.Get(a.ctx, open.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusOpen, still.Status, "a ticket whose scope is under no manifest repo is not re-validated")
}

func TestService_States(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	good := layout(false, "", repoEntry("app", "github.com/acme/app"), repoEntry("deploy", "github.com/acme/deploy"))
	bad := layout(false, "", repoEntry("app", "github.com/acme/fork"))
	a.newAgent("good", good)
	a.newAgent("bad", bad)
	a.newAgent("silent", nil)
	off := a.newAgent("off", bad)
	_, err := a.agents.SetDisabled(a.ctx, off.ID, true)
	require.NoError(t, err)

	byName := func(states []manifest.AgentState) map[string]manifest.AgentState {
		out := map[string]manifest.AgentState{}
		for _, s := range states {
			out[s.Agent.Name] = s
		}
		return out
	}

	t.Run("with no manifest there is nothing to match", func(t *testing.T) {
		states, err := a.svc.States(a.ctx, nil)
		require.NoError(t, err)
		got := byName(states)
		require.Len(t, got, 3, "a disabled agent is not part of the fleet")
		assert.Equal(t, manifest.StatusReported, got["good"].Status)
		assert.Equal(t, manifest.StatusReported, got["bad"].Status)
		assert.Equal(t, manifest.StatusUnknown, got["silent"].Status)
		assert.Empty(t, got["bad"].Mismatches)
	})

	t.Run("with a manifest, statuses are decided by the server", func(t *testing.T) {
		m, err := a.svc.Set(a.ctx, a.owner, twoRepos)
		require.NoError(t, err)
		states, err := a.svc.States(a.ctx, m)
		require.NoError(t, err)
		got := byName(states)
		assert.Equal(t, manifest.StatusMatches, got["good"].Status)
		assert.Equal(t, manifest.StatusMismatch, got["bad"].Status)
		assert.Equal(t, manifest.StatusUnknown, got["silent"].Status, "never reported is unknown, not a mismatch")
		require.Len(t, got["bad"].Mismatches, 2)
		assert.Equal(t, "app", got["bad"].Mismatches[0].Path)
		assert.Equal(t, "github.com/acme/fork", got["bad"].Mismatches[0].Found)
		assert.Equal(t, "deploy", got["bad"].Mismatches[1].Path)
	})
}

func TestService_ClaimNotice(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	// app is wrong; deploy is right.
	wrongApp := a.newAgent("wrong-app", layout(false, "", repoEntry("app", "github.com/acme/fork"), repoEntry("deploy", "github.com/acme/deploy")))
	wrongDeploy := a.newAgent("wrong-deploy", layout(false, "", repoEntry("app", "github.com/acme/app")))
	silent := a.newAgent("silent", nil)
	notice := func(who *agent.Agent, scopes ...string) string { return a.svc.ClaimNotice(a.ctx, who.ID, scopes) }

	assert.Empty(t, notice(wrongApp, "app/src/"), "with no manifest, nothing is enforced")

	_, err := a.svc.Set(a.ctx, a.owner, twoRepos)
	require.NoError(t, err)

	got := notice(wrongApp, "app/src/")
	assert.Contains(t, got, "WORKSPACE MISMATCH")
	assert.Contains(t, got, "github.com/acme/fork", "it says what was found")
	assert.Contains(t, got, "ticket_release")

	assert.Empty(t, notice(wrongApp, "deploy/main.tf"), "a mismatch in a repo the ticket does not need is only a board warning")
	assert.NotEmpty(t, notice(wrongDeploy, "deploy/main.tf"), "a repo that is missing counts")
	assert.Empty(t, notice(wrongDeploy, "app/x.go"))
	assert.Empty(t, notice(wrongApp, "docs/"), "a scope in no manifest repo needs none of them")
	assert.NotEmpty(t, notice(wrongApp, "deploy/x", "app/y"), "any needed repo is enough")
	assert.Empty(t, notice(silent, "app/src/"), "unknown never blocks")
}

func TestService_ClaimNoticeIsSilentWhenTheCheckFails(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	// No tenant in the context: the store reads nothing and errors or
	// returns no manifest; either way the agent is told nothing.
	assert.Empty(t, a.svc.ClaimNotice(context.Background(), uuid.New(), []string{"app/"}))
}

func TestService_ScriptReposAreTheStoredManifestAndEmptyWithoutOne(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()

	none, err := a.svc.ScriptRepos(a.ctx)
	require.NoError(t, err)
	assert.Empty(t, none, "no manifest: nothing for the scripts to clone")

	_, err = a.svc.Set(a.ctx, a.owner, twoRepos)
	require.NoError(t, err)
	got, err := a.svc.ScriptRepos(a.ctx)
	require.NoError(t, err)
	assert.Equal(t, []workspace.Repo{
		{Path: "app", Remote: "github.com/acme/app"}, {Path: "deploy", Remote: "github.com/acme/deploy"},
	}, got)

	other, err := b.svc.ScriptRepos(b.ctx)
	require.NoError(t, err)
	assert.Empty(t, other, "another tenant's manifest is not cloned here")
}
