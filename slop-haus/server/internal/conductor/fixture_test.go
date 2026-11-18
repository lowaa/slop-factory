package conductor_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/conductor"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

// tenantRoles is the user store's own membership rules.
type tenantRoles struct{ store *user.PostgresStore }

func (o tenantRoles) IsTenantOwner(ctx context.Context, userID, tenantID uuid.UUID) (bool, error) {
	return o.store.IsOwner(ctx, userID, tenantID)
}

func (o tenantRoles) IsTenantMember(ctx context.Context, userID, tenantID uuid.UUID) (bool, error) {
	return o.store.IsMember(ctx, userID, tenantID)
}

type noGrants struct{}

func (noGrants) Authenticate(context.Context, string) (*oauth.Access, error) {
	return nil, agent.ErrBadToken
}
func (noGrants) RevokeAgentGrants(context.Context, uuid.UUID, uuid.UUID) error { return nil }

// fixture is conductors and their conversations against the real database:
// the queue's order, the one live conversation and tenancy are constraints
// and queries, which a fake could only agree with itself about.
type fixture struct {
	database   *db.DB
	tenant     uuid.UUID
	ctx        context.Context
	agents     *agent.Service
	conductors *agent.ConductorService
	tickets    *ticket.Service
	svc        *conductor.Service
	// admin owns the tenant; owner and other are plain members.
	admin, owner, other *authn.User
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	database := dbtest.WithDB(t)
	tenant := dbtest.NewTenant(t, database)
	f := &fixture{database: database, tenant: tenant, ctx: db.WithTenant(context.Background(), tenant)}
	pending := pendingaction.NewService(pendingaction.NewPostgresStore(database), pendingaction.Config{}, slog.DiscardHandler)
	f.tickets = ticket.NewService(ticket.NewPostgresStore(database), slog.DiscardHandler)
	roles := tenantRoles{user.NewPostgresStore(database)}
	f.agents = agent.NewService(agent.NewPostgresStore(database), f.tickets, noGrants{}, slog.DiscardHandler,
		agent.WithProposalVoider(pending), agent.WithMembers(roles))
	f.conductors = agent.NewConductorService(f.agents, roles, slog.DiscardHandler)
	f.svc = f.newService(nil)
	f.admin, f.owner, f.other = f.member(t, tenant, "owner"), f.member(t, tenant, "member"), f.member(t, tenant, "member")
	t.Cleanup(f.svc.Close)
	return f
}

// newService is another service over the same database, as another api
// replica is. opts may be nil.
func (f *fixture) newService(opts *conductor.Options) *conductor.Service {
	o := conductor.Options{}
	if opts != nil {
		o = *opts
	}
	if o.Store == nil {
		o.Store = conductor.NewPostgresStore(f.database)
	}
	if o.Scopes == nil {
		o.Scopes = f.tickets
	}
	return conductor.NewService(&o, slog.DiscardHandler)
}

func (f *fixture) member(t *testing.T, tenant uuid.UUID, role string) *authn.User {
	t.Helper()
	id := dbtest.NewUser(t, f.database)
	_, err := f.database.Global().Exec(context.Background(),
		`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES ($1, $2, $3, $4)`,
		uuid.New(), tenant, id, role)
	require.NoError(t, err)
	return &authn.User{ID: id, TenantID: tenant, AuthTime: time.Now()}
}

// owned registers a conductor for who and proves it, as a handler does.
func (f *fixture) owned(t *testing.T, who *authn.User, name string) *agent.OwnedConductor {
	t.Helper()
	created, err := f.conductors.Register(f.ctx, who, name)
	require.NoError(t, err)
	owned, err := f.conductors.AuthorizeOwner(f.ctx, who, created.ID)
	require.NoError(t, err)
	return owned
}

func (f *fixture) start(t *testing.T, owned *agent.OwnedConductor) *conductor.Conversation {
	t.Helper()
	c, err := f.svc.Start(f.ctx, owned, &conductor.StartRequest{})
	require.NoError(t, err)
	return c
}

// host is a poll's identity: the conductor's own, with a host process id.
type host struct {
	proof *conductor.Host
	id    uuid.UUID
}

func (f *fixture) host(t *testing.T, owned *agent.OwnedConductor) *host {
	t.Helper()
	proof, err := f.svc.AuthenticateHost(authn.NewContext(f.ctx, owned.Agent()))
	require.NoError(t, err)
	return &host{proof: proof, id: uuid.New()}
}
