package agent_test

import (
	"context"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	goauthn "github.com/lowaa/slop-factory/go-auth/authn"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

// tenantRoles is the user store's own membership rules, as the service takes them.
type tenantRoles struct{ store *user.PostgresStore }

func (o tenantRoles) IsTenantOwner(ctx context.Context, userID, tenantID uuid.UUID) (bool, error) {
	return o.store.IsOwner(ctx, userID, tenantID)
}

func (o tenantRoles) IsTenantMember(ctx context.Context, userID, tenantID uuid.UUID) (bool, error) {
	return o.store.IsMember(ctx, userID, tenantID)
}

// recordingGrants is agent.Grants that records which agents had their grants
// revoked and decides nothing else.
type recordingGrants struct {
	mu      sync.Mutex
	revoked []uuid.UUID
}

func (g *recordingGrants) Authenticate(context.Context, string) (*oauth.Access, error) {
	return nil, agent.ErrBadToken
}

func (g *recordingGrants) RevokeAgentGrants(_ context.Context, _, agentID uuid.UUID) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.revoked = append(g.revoked, agentID)
	return nil
}

func (g *recordingGrants) revokedAgents() []uuid.UUID {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]uuid.UUID(nil), g.revoked...)
}

// conductorFixture is conductors against the real stores and database: the
// ownership rules are constraints and queries, so a fake could only agree
// with itself.
type conductorFixture struct {
	database   *db.DB
	tenant     uuid.UUID
	ctx        context.Context
	agents     *agent.Service
	conductors *agent.ConductorService
	grants     *recordingGrants
	pending    *pendingaction.Service
	tickets    *ticket.Service
	owners     *lateOwners
	// auditLog is the audit events the ticket service recorded, as JSON lines.
	auditLog *syncBuffer
	// admin owns the tenant; owner and other are plain members.
	admin, owner, other *authn.User
}

func (f *conductorFixture) member(t *testing.T, tenant uuid.UUID, role string) *authn.User {
	t.Helper()
	id := dbtest.NewUser(t, f.database)
	_, err := f.database.Global().Exec(context.Background(),
		`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES ($1, $2, $3, $4)`,
		uuid.New(), tenant, id, role)
	require.NoError(t, err)
	return &authn.User{ID: id, TenantID: tenant, AuthTime: time.Now()}
}

func newConductorFixture(t *testing.T) *conductorFixture {
	t.Helper()
	database := dbtest.WithDB(t)
	tenant := dbtest.NewTenant(t, database)
	f := &conductorFixture{database: database, tenant: tenant, ctx: db.WithTenant(context.Background(), tenant)}
	f.grants = &recordingGrants{}
	f.pending = pendingaction.NewService(pendingaction.NewPostgresStore(database), pendingaction.Config{}, slog.DiscardHandler)
	f.auditLog = &syncBuffer{}
	f.owners = &lateOwners{}
	f.tickets = ticket.NewService(ticket.NewPostgresStore(database), slog.DiscardHandler,
		ticket.WithOwnerResolver(f.owners),
		ticket.WithEvents(audit.New(slog.NewJSONHandler(f.auditLog, nil), nil)))
	f.agents = agent.NewService(agent.NewPostgresStore(database), f.tickets, f.grants, slog.DiscardHandler,
		agent.WithProposalVoider(f.pending), agent.WithMembers(tenantRoles{user.NewPostgresStore(database)}))
	f.conductors = agent.NewConductorService(f.agents, tenantRoles{user.NewPostgresStore(database)}, slog.DiscardHandler)
	f.admin, f.owner, f.other = f.member(t, tenant, "owner"), f.member(t, tenant, "member"), f.member(t, tenant, "member")
	return f
}

func (f *conductorFixture) register(t *testing.T, who *authn.User, name string) *agent.Agent {
	t.Helper()
	created, err := f.conductors.Register(f.ctx, who, name)
	require.NoError(t, err)
	return created
}

func TestRegisterRecordsTheRegisteringUserAsOwner(t *testing.T) {
	f := newConductorFixture(t)

	c := f.register(t, f.owner, "my-conductor")

	assert.Equal(t, agent.KindConductor, c.Kind)
	require.NotNil(t, c.OwnerUserID)
	assert.Equal(t, f.owner.ID, *c.OwnerUserID)
	stored, err := f.agents.Get(f.ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, f.owner.ID, *stored.OwnerUserID)
}

func TestRegisterNeedsARecentSignIn(t *testing.T) {
	f := newConductorFixture(t)
	stale := *f.owner
	stale.AuthTime = time.Now().Add(-24 * time.Hour)

	_, err := f.conductors.Register(f.ctx, &stale, "stale")

	require.Error(t, err)
	assert.ErrorIs(t, err, goauthn.ErrReauthRequired)
	list, err := f.conductors.ListOwned(f.ctx, f.owner, false)
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestRegisterRefusesSomeoneWhoIsNotAMemberOfTheTenant(t *testing.T) {
	f := newConductorFixture(t)
	elsewhere := f.member(t, dbtest.NewTenant(t, f.database), "owner")
	elsewhere.TenantID = f.tenant

	_, err := f.conductors.Register(f.ctx, elsewhere, "intruder")

	assert.ErrorIs(t, err, agent.ErrNotOwner)
}

func TestRegisterRefusesAnEmptyOrTakenName(t *testing.T) {
	f := newConductorFixture(t)
	_, err := f.conductors.Register(f.ctx, f.owner, "  ")
	assert.ErrorIs(t, err, agent.ErrEmptyName)

	f.register(t, f.owner, "taken")
	_, err = f.conductors.Register(f.ctx, f.other, "taken")
	assert.ErrorIs(t, err, agent.ErrNameTaken)
}

// Immutable in the database, so no writer -- this service, a later one, a
// hand-written UPDATE -- can hand a conductor to someone else.
func TestAConductorsKindAndOwnerCannotChange(t *testing.T) {
	f := newConductorFixture(t)
	c := f.register(t, f.owner, "mine")
	worker, err := f.agents.Create(f.ctx, "worker")
	require.NoError(t, err)

	for name, stmt := range map[string]struct {
		sql  string
		args []any
	}{
		"re-own":             {`UPDATE agents SET owner_user_id = $2 WHERE id = $1`, []any{c.ID, f.other.ID}},
		"disown":             {`UPDATE agents SET owner_user_id = NULL, kind = 'worker' WHERE id = $1`, []any{c.ID}},
		"demote":             {`UPDATE agents SET kind = 'worker' WHERE id = $1`, []any{c.ID}},
		"promote a worker":   {`UPDATE agents SET kind = 'conductor', owner_user_id = $2 WHERE id = $1`, []any{worker.ID, f.other.ID}},
		"own a worker":       {`UPDATE agents SET owner_user_id = $2 WHERE id = $1`, []any{worker.ID, f.other.ID}},
		"re-own to self too": {`UPDATE agents SET owner_user_id = $2 WHERE id = $1`, []any{c.ID, f.admin.ID}},
	} {
		_, err := f.database.Exec(f.ctx, stmt.sql, stmt.args...)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "cannot change once registered", name)
	}

	got, err := f.agents.Get(f.ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, f.owner.ID, *got.OwnerUserID)
	assert.Equal(t, agent.KindConductor, got.Kind)

	// Rewriting the same values is no change, so the ordinary updates that
	// touch the row (disabling it) are not tripped by the guard.
	_, err = f.database.Exec(f.ctx, `UPDATE agents SET owner_user_id = owner_user_id, kind = kind WHERE id = $1`, c.ID)
	assert.NoError(t, err)
}

func TestAConductorHasAnOwnerAndAWorkerHasNone(t *testing.T) {
	f := newConductorFixture(t)
	for name, sql := range map[string]string{
		"conductor without an owner": `INSERT INTO agents (id, name, kind) VALUES (gen_random_uuid(), 'x', 'conductor')`,
		"worker with an owner":       `INSERT INTO agents (id, name, owner_user_id) VALUES (gen_random_uuid(), 'y', '` + f.owner.ID.String() + `')`,
	} {
		_, err := f.database.Exec(f.ctx, sql)
		assert.Error(t, err, name)
	}
}

func TestAConductorCannotAlsoBeAConsole(t *testing.T) {
	f := newConductorFixture(t)
	c := f.register(t, f.owner, "mine")

	_, err := f.database.Exec(f.ctx, `UPDATE agents SET kind = 'console' WHERE id = $1`, c.ID)
	assert.Error(t, err, "the database refuses to change a kind")
}

func TestOnlyTheOwnerMayUseAConductor(t *testing.T) {
	f := newConductorFixture(t)
	c := f.register(t, f.owner, "mine")

	owned, err := f.conductors.AuthorizeOwner(f.ctx, f.owner, c.ID)
	require.NoError(t, err)
	assert.Equal(t, c.ID, owned.Agent().ID)
	assert.Equal(t, f.owner.ID, owned.Owner())

	_, err = f.conductors.AuthorizeOwner(f.ctx, f.other, c.ID)
	assert.ErrorIs(t, err, agent.ErrNotOwner, "another member of the tenant")
}

func TestATenantOwnerCanDisableAConductorButNotUseIt(t *testing.T) {
	f := newConductorFixture(t)
	c := f.register(t, f.owner, "mine")

	_, err := f.conductors.AuthorizeOwner(f.ctx, f.admin, c.ID)
	assert.ErrorIs(t, err, agent.ErrNotOwner, "the tenant's owner may not use it")
	disabled, err := f.conductors.SetDisabled(f.ctx, f.admin, c.ID, true)
	require.NoError(t, err)
	assert.NotNil(t, disabled.DisabledAt)
	_, err = f.conductors.SetDisabled(f.ctx, f.admin, c.ID, false)
	assert.ErrorIs(t, err, agent.ErrNotOwner, "but not back on: that is its owner's alone")
	got, err := f.agents.Get(f.ctx, c.ID)
	require.NoError(t, err)
	assert.NotNil(t, got.DisabledAt)
}

func TestAPlainMemberCannotDisableAnotherMembersConductor(t *testing.T) {
	f := newConductorFixture(t)
	c := f.register(t, f.owner, "mine")

	_, err := f.conductors.SetDisabled(f.ctx, f.other, c.ID, true)

	assert.ErrorIs(t, err, agent.ErrNotOwner)
	got, err := f.agents.Get(f.ctx, c.ID)
	require.NoError(t, err)
	assert.Nil(t, got.DisabledAt)
}

func TestAnOwnerCanDisableAndReEnableTheirConductor(t *testing.T) {
	f := newConductorFixture(t)
	c := f.register(t, f.owner, "mine")

	_, err := f.conductors.SetDisabled(f.ctx, f.owner, c.ID, true)
	require.NoError(t, err)
	_, err = f.conductors.AuthorizeOwner(f.ctx, f.owner, c.ID)
	assert.ErrorIs(t, err, agent.ErrIsDisabled, "a disabled conductor is not usable, even by its owner")

	_, err = f.conductors.SetDisabled(f.ctx, f.owner, c.ID, false)
	require.NoError(t, err)
	_, err = f.conductors.AuthorizeOwner(f.ctx, f.owner, c.ID)
	assert.NoError(t, err)
}

func TestListingShowsAUserTheirOwnConductorsAndOnlyATenantOwnerTheRest(t *testing.T) {
	f := newConductorFixture(t)
	mine := f.register(t, f.owner, "mine")
	f.register(t, f.other, "theirs")

	own, err := f.conductors.ListOwned(f.ctx, f.owner, false)
	require.NoError(t, err)
	require.Len(t, own, 1)
	assert.Equal(t, mine.ID, own[0].ID)

	others, err := f.conductors.ListOthers(f.ctx, f.owner, false)
	require.NoError(t, err)
	assert.Empty(t, others, "a plain member sees only their own")
	others, err = f.conductors.ListOthers(f.ctx, f.admin, false)
	require.NoError(t, err)
	assert.Len(t, others, 2, "the tenant's owner sees everyone's, to disable")

	workers, err := f.agents.List(f.ctx, false)
	require.NoError(t, err)
	assert.Empty(t, workers, "conductors are not in the workers' listing")
}

func TestAWorkerIsNotAConductor(t *testing.T) {
	f := newConductorFixture(t)
	w, err := f.agents.Create(f.ctx, "worker")
	require.NoError(t, err)

	_, err = f.conductors.AuthorizeOwner(f.ctx, f.owner, w.ID)
	assert.ErrorIs(t, err, agent.ErrNotFound)
	_, err = f.conductors.SetDisabled(f.ctx, f.admin, w.ID, true)
	assert.ErrorIs(t, err, agent.ErrNotFound)
}

func TestWorkerRoutesDoNotReachAConductor(t *testing.T) {
	f := newConductorFixture(t)
	c := f.register(t, f.owner, "mine")

	_, err := f.agents.GetWorker(f.ctx, c.ID)
	assert.ErrorIs(t, err, agent.ErrNotFound)
	_, err = f.agents.SetDisabled(f.ctx, c.ID, true)
	assert.ErrorIs(t, err, agent.ErrNotFound, "anyone in the tenant can reach the worker route; it must not switch a conductor")
	_, err = f.agents.SignOutWorkspaces(f.ctx, c.ID)
	assert.ErrorIs(t, err, agent.ErrNotFound)
}

func TestAConductorIsInvisibleToAnotherTenant(t *testing.T) {
	f := newConductorFixture(t)
	c := f.register(t, f.owner, "mine")

	// The same person, a member of both tenants, using the other one.
	otherTenant := dbtest.NewTenant(t, f.database)
	_, err := f.database.Global().Exec(context.Background(),
		`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES ($1, $2, $3, 'owner')`, uuid.New(), otherTenant, f.owner.ID)
	require.NoError(t, err)
	there := &authn.User{ID: f.owner.ID, TenantID: otherTenant, AuthTime: time.Now()}
	thereCtx := db.WithTenant(context.Background(), otherTenant)

	_, err = f.conductors.AuthorizeOwner(thereCtx, there, c.ID)
	assert.ErrorIs(t, err, agent.ErrNotFound)
	_, err = f.conductors.SetDisabled(thereCtx, there, c.ID, true)
	assert.ErrorIs(t, err, agent.ErrNotFound)
	list, err := f.conductors.ListOwned(thereCtx, there, false)
	require.NoError(t, err)
	assert.Empty(t, list)
	all, err := f.conductors.ListOthers(thereCtx, there, false)
	require.NoError(t, err)
	assert.Empty(t, all)

	// The database refuses an owner who is not a member of the agent's own tenant.
	stranger := dbtest.NewUser(t, f.database)
	_, err = f.database.Exec(f.ctx, `INSERT INTO agents (id, name, kind, owner_user_id) VALUES (gen_random_uuid(), 'x', 'conductor', $1)`, stranger)
	assert.Error(t, err)
}

// Removing a member is a bare DELETE today (no application code does it), so
// the guarantee is the database's: it is exercised the same way.
func (f *conductorFixture) removeMember(t *testing.T, who *authn.User) {
	t.Helper()
	_, err := f.database.Global().Exec(context.Background(),
		`DELETE FROM global_tenant_memberships WHERE tenant = $1 AND user_id = $2`, f.tenant, who.ID)
	require.NoError(t, err)
}

func (f *conductorFixture) grantFor(t *testing.T, a *agent.Agent) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := f.database.Global().Exec(context.Background(),
		`INSERT INTO global_oauth_grants (id, tenant, agent_id, client_id, resource) VALUES ($1, $2, $3, 'slop-haus-workspace', 'r')`,
		id, f.tenant, a.ID)
	require.NoError(t, err)
	return id
}

func (f *conductorFixture) grantRevoked(t *testing.T, id uuid.UUID) bool {
	t.Helper()
	var revoked bool
	require.NoError(t, f.database.Global().QueryRow(context.Background(),
		`SELECT revoked_at IS NOT NULL FROM global_oauth_grants WHERE id = $1`, id).Scan(&revoked))
	return revoked
}

func TestRemovingAnOwnerDisablesTheirConductorsAndKeepsTheirHistory(t *testing.T) {
	f := newConductorFixture(t)
	leaving, staying := f.register(t, f.owner, "going"), f.register(t, f.other, "staying")
	proposal := f.proposeAs(t, leaving)
	leavingGrant, stayingGrant := f.grantFor(t, leaving), f.grantFor(t, staying)

	// Rows the conductor authored: a ticket and a comment. Before, the
	// owner's foreign key made removing the member fail on these.
	tk, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: "by the conductor", Status: ticket.StatusOpen, CreatedByAgentID: &leaving.ID})
	require.NoError(t, err)
	require.NotNil(t, tk.CreatedByAgentID)
	_, err = f.database.Exec(f.ctx,
		`INSERT INTO ticket_comments (ticket_id, author_agent_id, body) VALUES ($1, $2, 'hello')`, tk.ID, leaving.ID)
	require.NoError(t, err)

	f.removeMember(t, f.owner)

	got, err := f.agents.Get(f.ctx, leaving.ID)
	require.NoError(t, err, "the conductor is kept")
	assert.True(t, got.Disabled())
	require.NotNil(t, got.OwnerUserID)
	assert.Equal(t, f.owner.ID, *got.OwnerUserID)
	assert.True(t, f.grantRevoked(t, leavingGrant), "its credentials are revoked")

	voided, err := f.pending.Get(f.ctx, proposal.ID)
	require.NoError(t, err, "its proposal history is kept")
	assert.Equal(t, pendingaction.StateVoided, voided.State)

	var authored int
	require.NoError(t, f.database.QueryRow(f.ctx,
		`SELECT (SELECT count(*) FROM tickets WHERE created_by_agent_id = $1) + (SELECT count(*) FROM ticket_comments WHERE author_agent_id = $1)`,
		leaving.ID).Scan(&authored))
	assert.Equal(t, 2, authored, "what it authored is still there")

	// Someone else's conductor in the same tenant is untouched.
	other, err := f.agents.Get(f.ctx, staying.ID)
	require.NoError(t, err)
	assert.False(t, other.Disabled())
	assert.False(t, f.grantRevoked(t, stayingGrant))
}

func TestAFormerMemberCannotUseOrReEnableTheirConductor(t *testing.T) {
	f := newConductorFixture(t)
	c := f.register(t, f.owner, "mine")
	f.removeMember(t, f.owner)

	_, err := f.conductors.AuthorizeOwner(f.ctx, f.owner, c.ID)
	assert.ErrorIs(t, err, agent.ErrNotOwner, "the former owner is refused")
	_, err = f.conductors.AuthorizeOwner(f.ctx, f.other, c.ID)
	assert.ErrorIs(t, err, agent.ErrNotOwner, "and nobody else ever could")

	_, err = f.conductors.SetDisabled(f.ctx, f.admin, c.ID, false)
	assert.ErrorIs(t, err, agent.ErrNotOwner, "a tenant owner cannot bring it back for someone who left")
	got, err := f.agents.Get(f.ctx, c.ID)
	require.NoError(t, err)
	assert.True(t, got.Disabled())

	_, err = f.conductors.Register(f.ctx, f.owner, "again")
	assert.ErrorIs(t, err, agent.ErrNotOwner, "a former member cannot register one")

	_, err = f.conductors.ListOwned(f.ctx, f.owner, false)
	assert.ErrorIs(t, err, agent.ErrNotOwner, "nor list theirs")
	_, err = f.conductors.SetDisabled(f.ctx, f.owner, c.ID, true)
	assert.ErrorIs(t, err, agent.ErrNotOwner, "nor switch it off as its owner")
}

// Authorization fails closed on its own, without relying on the removal
// having disabled the conductor.
func TestAuthorizeOwnerChecksMembershipEvenIfTheConductorIsStillEnabled(t *testing.T) {
	f := newConductorFixture(t)
	c := f.register(t, f.owner, "mine")
	f.removeMember(t, f.owner)
	_, err := f.database.Exec(f.ctx, `UPDATE agents SET disabled_at = NULL WHERE id = $1`, c.ID)
	require.NoError(t, err)

	_, err = f.conductors.AuthorizeOwner(f.ctx, f.owner, c.ID)
	assert.ErrorIs(t, err, agent.ErrNotOwner)
}

// A removal from inside another tenant's scope would have the trigger write
// the conductor's rows under a scope it was not given, so it is refused;
// a member with no conductor is removed from there as ever.
func TestRemovingAnOwnerFromInsideAnotherTenantsScopeIsRefused(t *testing.T) {
	f := newConductorFixture(t)
	f.register(t, f.owner, "mine")
	elsewhere := db.WithTenant(context.Background(), dbtest.NewTenant(t, f.database))
	remove := func(who *authn.User) error {
		return f.database.InTx(elsewhere, func(q db.Querier) error {
			_, err := q.Exec(elsewhere, `DELETE FROM global_tenant_memberships WHERE tenant = $1 AND user_id = $2`, f.tenant, who.ID)
			return err
		})
	}

	require.Error(t, remove(f.owner))
	var members int
	require.NoError(t, f.database.Global().QueryRow(context.Background(),
		`SELECT count(*) FROM global_tenant_memberships WHERE tenant = $1 AND user_id = $2`, f.tenant, f.owner.ID).Scan(&members))
	assert.Equal(t, 1, members, "the member is still there")

	require.NoError(t, remove(f.other), "a member with no conductor is unaffected")
}

func (f *conductorFixture) proposeAs(t *testing.T, a *agent.Agent) *pendingaction.Action {
	t.Helper()
	tk, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: "t", Status: ticket.StatusOpen})
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx, `UPDATE tickets SET status = 'preparing' WHERE id = $1`, tk.ID)
	require.NoError(t, err)
	got, err := f.pending.Propose(f.ctx, &pendingaction.ProposeParams{AgentID: a.ID, TicketID: tk.ID, Kind: pendingaction.KindReady})
	require.NoError(t, err)
	return got
}

func TestAConductorProposesWithoutTheConsoleFlagAndDisablingVoidsIt(t *testing.T) {
	f := newConductorFixture(t)
	c := f.register(t, f.owner, "mine")

	may, err := f.agents.MayPropose(f.ctx, c.ID)
	require.NoError(t, err)
	assert.True(t, may)
	p := f.proposeAs(t, c)
	assert.Equal(t, pendingaction.StatePending, p.State)

	_, err = f.conductors.SetDisabled(f.ctx, f.admin, c.ID, true)
	require.NoError(t, err)

	got, err := f.pending.Get(f.ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, pendingaction.StateVoided, got.State)
	may, err = f.agents.MayPropose(f.ctx, c.ID)
	require.NoError(t, err)
	assert.False(t, may, "a disabled conductor proposes nothing")
	_, err = f.pending.Propose(f.ctx, &pendingaction.ProposeParams{AgentID: c.ID, TicketID: *p.TicketID, Kind: pendingaction.KindReady})
	assert.ErrorIs(t, err, pendingaction.ErrNotConsole)
}

func TestAWorkerStillCannotPropose(t *testing.T) {
	f := newConductorFixture(t)
	w, err := f.agents.Create(f.ctx, "worker")
	require.NoError(t, err)

	may, err := f.agents.MayPropose(f.ctx, w.ID)
	require.NoError(t, err)
	assert.False(t, may)
}

func TestAConductorIsNeverHandedATicket(t *testing.T) {
	f := newConductorFixture(t)
	c := f.register(t, f.owner, "mine")
	_, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: "work", Status: ticket.StatusOpen})
	require.NoError(t, err)

	got, err := f.tickets.Claim(f.ctx, c.ID, nil)

	require.NoError(t, err)
	assert.Nil(t, got.Ticket, "work is for workers")
}

func TestOnlyTheOwnerMayPickTheirConductorOnTheSignInPage(t *testing.T) {
	f := newConductorFixture(t)
	c := f.register(t, f.owner, "mine")

	got, err := f.agents.Choose(f.ctx, f.owner, &c.ID, "", "")
	require.NoError(t, err)
	assert.Equal(t, c.ID, got)

	for name, who := range map[string]*authn.User{"another member": f.other, "the tenant's owner": f.admin} {
		_, err = f.agents.Choose(f.ctx, who, &c.ID, "", "")
		assert.ErrorIs(t, err, oauth.ErrAgentUnavailable, name)
	}
}

// Every service method that acts on a conductor for a user takes the proof
// AuthorizeOwner issues, so a handler cannot reach one without the check.
// A method that does not must be listed here, with why it is safe: the list
// is where a new one is argued rather than forgotten.
func TestEveryConductorMethodIsOwnerChecked(t *testing.T) {
	exempt := map[string]string{
		"Register":         "makes the caller the owner; there is no conductor yet",
		"ListOwned":        "lists the caller's own",
		"ListOthers":       "tenant owners only, to disable; checked inside",
		"ListDirectory":    "any member, read-only, no conversation detail",
		"AuthorizeOwner":   "is the check",
		"AuthorizeSession": "is the check, for the conductor's own authenticated session",
		"MayChangeConfig":  "is the check, for a route that also takes workers' ids",
		"SetDisabled":      "its owner or the tenant's owner, checked inside; switching off is not using",
		"SetArchived":      "its owner or the tenant's owner, checked inside; putting away is not using",
	}
	ownedType := reflect.TypeOf(&agent.OwnedConductor{})
	svc := reflect.TypeOf(&agent.ConductorService{})
	for i := 0; i < svc.NumMethod(); i++ {
		m := svc.Method(i)
		takesProof := false
		for j := 1; j < m.Type.NumIn(); j++ {
			takesProof = takesProof || m.Type.In(j) == ownedType
		}
		if _, ok := exempt[m.Name]; ok {
			assert.False(t, takesProof, "%s is exempt, so it should not also take the proof: drop it from the list", m.Name)
			continue
		}
		assert.True(t, takesProof, "ConductorService.%s acts on a conductor but takes no *OwnedConductor: pass the proof from AuthorizeOwner, or list it as exempt with the reason", m.Name)
	}
	for name := range exempt {
		_, ok := svc.MethodByName(name)
		assert.True(t, ok, "exempt method %s no longer exists", name)
	}
}

func TestSigningOutAConductorsWorkspacesRevokesItsGrantsForItsOwnerOnly(t *testing.T) {
	f := newConductorFixture(t)
	c := f.register(t, f.owner, "mine")

	owned, err := f.conductors.AuthorizeOwner(f.ctx, f.owner, c.ID)
	require.NoError(t, err)
	_, err = f.conductors.SignOutWorkspaces(f.ctx, owned)
	require.NoError(t, err)

	assert.Equal(t, []uuid.UUID{c.ID}, f.grants.revokedAgents())
}

// The proof is older than the disable: signing out must look at the
// conductor as it is now.
func TestSigningOutAConductorDisabledSinceItWasAuthorizedIsRefused(t *testing.T) {
	f := newConductorFixture(t)
	c := f.register(t, f.owner, "mine")
	owned, err := f.conductors.AuthorizeOwner(f.ctx, f.owner, c.ID)
	require.NoError(t, err)
	_, err = f.conductors.SetDisabled(f.ctx, f.admin, c.ID, true)
	require.NoError(t, err)

	_, err = f.conductors.SignOutWorkspaces(f.ctx, owned)

	assert.ErrorIs(t, err, agent.ErrIsDisabled)
	assert.Empty(t, f.grants.revokedAgents())
}

func TestMayChangeConfigIsTheOwnersAloneForAConductorAndOpenForAnyOtherAgent(t *testing.T) {
	f := newConductorFixture(t)
	c := f.register(t, f.owner, "mine")
	worker, err := f.agents.Create(f.ctx, "worker")
	require.NoError(t, err)
	disabled := f.register(t, f.owner, "off")
	_, err = f.conductors.SetDisabled(f.ctx, f.owner, disabled.ID, true)
	require.NoError(t, err)

	for name, tc := range map[string]struct {
		who  *authn.User
		id   uuid.UUID
		want bool
	}{
		"its owner":                      {f.owner, c.ID, true},
		"another member":                 {f.other, c.ID, false},
		"the tenant's owner":             {f.admin, c.ID, false},
		"its owner, while disabled":      {f.owner, disabled.ID, true},
		"another member, while disabled": {f.other, disabled.ID, false},
		"anyone, for a worker":           {f.other, worker.ID, true},
		"anyone, for no such agent":      {f.other, uuid.New(), true},
	} {
		got, err := f.conductors.MayChangeConfig(f.ctx, tc.who, tc.id)
		require.NoError(t, err, name)
		assert.Equal(t, tc.want, got, name)
	}
}

// Disabling is one function in the database, whichever way it is reached: the
// service's disable and the removal of the owner (see
// TestRemovingAnOwnerDisablesTheirConductorsAndKeepsTheirHistory) both go
// through it, so both void the proposals and revoke the grants.
func TestDisablingAConductorVoidsItsProposalsAndRevokesItsGrants(t *testing.T) {
	f := newConductorFixture(t)
	c, bystander := f.register(t, f.owner, "mine"), f.register(t, f.other, "theirs")
	p := f.proposeAs(t, c)
	grant, bystanderGrant := f.grantFor(t, c), f.grantFor(t, bystander)
	worker, err := f.agents.Create(f.ctx, "worker")
	require.NoError(t, err)
	workerGrant := f.grantFor(t, worker)

	_, err = f.conductors.SetDisabled(f.ctx, f.owner, c.ID, true)
	require.NoError(t, err)
	_, err = f.agents.SetDisabled(f.ctx, worker.ID, true)
	require.NoError(t, err)

	voided, err := f.pending.Get(f.ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, pendingaction.StateVoided, voided.State)
	assert.True(t, f.grantRevoked(t, grant))
	assert.False(t, f.grantRevoked(t, bystanderGrant), "another conductor's grants are untouched")
	assert.False(t, f.grantRevoked(t, workerGrant), "a worker keeps its grants, so the same workspace carries on once it is switched back on")

	_, err = f.conductors.SetDisabled(f.ctx, f.owner, c.ID, false)
	require.NoError(t, err)
	assert.True(t, f.grantRevoked(t, grant), "switching it back on does not bring the grant back")
}

func TestChoosingAConductorChecksTheOwnerIsStillAMember(t *testing.T) {
	f := newConductorFixture(t)
	c := f.register(t, f.owner, "mine")
	f.removeMember(t, f.owner)
	// Enabled again behind the removal's back: Choose must not rely on it.
	_, err := f.database.Exec(f.ctx, `UPDATE agents SET disabled_at = NULL WHERE id = $1`, c.ID)
	require.NoError(t, err)

	_, err = f.agents.Choose(f.ctx, f.owner, &c.ID, "", "")

	assert.ErrorIs(t, err, oauth.ErrAgentUnavailable)
}

// lateOwners is the owner resolver for one that needs the agent service,
// which is built from the ticket service; it resolves nobody until set.
type lateOwners struct{ resolver ticket.OwnerResolver }

func (l *lateOwners) ResolveOwner(ctx context.Context, agentID uuid.UUID) (*ticket.OwnerBound, error) {
	if l.resolver == nil {
		return nil, nil
	}
	return l.resolver.ResolveOwner(ctx, agentID)
}
