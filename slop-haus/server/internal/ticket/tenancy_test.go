package ticket_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// tenantFixture is one tenant's view of a database shared with another
// tenant: its own service, its own agent, its own context -- but the same
// underlying connection pool RLS has to keep it out of the other tenant's
// rows on. Two separate databases (two dbtest.WithDB calls) would prove
// nothing here: they share no connection and no policy that could fail.
type tenantFixture struct {
	svc      *ticket.Service
	agents   *agent.Service
	ctx      context.Context
	agentID  uuid.UUID
	database *db.DB
}

func newTwoTenants(t *testing.T) (a, b *tenantFixture) {
	t.Helper()
	database := dbtest.WithDB(t)
	build := func(name string) *tenantFixture {
		tenantID := dbtest.NewTenant(t, database)
		ctx := db.WithTenant(context.Background(), tenantID)
		svc := ticket.NewService(newStore(database), slog.DiscardHandler)
		agents := agent.NewService(agent.NewPostgresStore(database), svc, nil, slog.DiscardHandler)
		created, err := agents.Create(ctx, name)
		require.NoError(t, err)
		return &tenantFixture{svc: svc, agents: agents, ctx: ctx, agentID: created.ID, database: database}
	}
	return build("tenant-a-agent"), build("tenant-b-agent")
}

func (f *tenantFixture) ticket(t *testing.T, title string, paths ...string) *ticket.Ticket {
	t.Helper()
	scopes := make([]model.Path, 0, len(paths))
	for _, p := range paths {
		scopes = append(scopes, model.Path{Path: p})
	}
	created, err := f.svc.Create(f.ctx, &ticket.CreateParams{Title: title, Paths: scopes})
	require.NoError(t, err)
	return created
}

// Both tenants' first ticket is #1 -- the per-tenant sequence's whole
// point -- which is also what makes the tests below meaningful: "#1" names
// a different row depending on who is asking, so any query missing a
// tenant scope would blur the two into one.
func TestTenancy_BothTenantsFirstTicketIsNumberOne(t *testing.T) {
	t.Parallel()
	a, b := newTwoTenants(t)

	ta := a.ticket(t, "Tenant A's first ticket")
	tb := b.ticket(t, "Tenant B's first ticket")

	assert.Equal(t, int64(1), ta.ID)
	assert.Equal(t, int64(1), tb.ID)
}

// An agent in tenant A cannot see, claim or comment on tenant B's #1.
func TestTenancy_AgentCannotSeeClaimOrCommentOnAnotherTenantsTicket(t *testing.T) {
	t.Parallel()
	a, b := newTwoTenants(t)
	tb := b.ticket(t, "Tenant B's ticket", "server/")
	require.Equal(t, int64(1), tb.ID)

	_, err := a.svc.Get(a.ctx, tb.ID)
	assert.ErrorIs(t, err, ticket.ErrNotFound, "tenant A's #1 is a different ticket, not tenant B's")

	_, err = a.svc.AddComment(a.ctx, tb.ID, ticket.AgentAuthor(a.agentID), "should not land here")
	assert.ErrorIs(t, err, ticket.ErrNotFound)

	// A directed claim naming tenant B's id must not hand it over either --
	// tenant A simply has no #1 to grant.
	result, err := a.svc.Claim(a.ctx, a.agentID, &tb.ID)
	require.NoError(t, err)
	assert.Nil(t, result.Ticket, "there is no #1 in tenant A that could resolve to tenant B's row")
}

// Undirected claiming must never cross the boundary either: with work only
// in tenant B, tenant A's queue looks empty rather than borrowing it.
func TestTenancy_ClaimNeverReturnsAnotherTenantsTicket(t *testing.T) {
	t.Parallel()
	a, b := newTwoTenants(t)
	b.ticket(t, "Only in tenant B", "server/")

	result, err := a.svc.Claim(a.ctx, a.agentID, nil)
	require.NoError(t, err)
	assert.Nil(t, result.Ticket, "tenant A's queue has nothing of its own to grant")
	assert.Nil(t, result.Blocked)
}

// The integration lock is per tenant (locks' primary key is (tenant_id,
// name)): a hold in A must not stop B taking its own, or every tenant
// would share one merge queue.
func TestTenancy_IntegrationLockDoesNotCrossTenants(t *testing.T) {
	t.Parallel()
	a, b := newTwoTenants(t)
	a.ticket(t, "A's work", "a/")
	b.ticket(t, "B's work", "b/")

	_, err := a.svc.Claim(a.ctx, a.agentID, nil)
	require.NoError(t, err)
	_, err = b.svc.Claim(b.ctx, b.agentID, nil)
	require.NoError(t, err)

	require.NoError(t, a.svc.AcquireIntegrationLock(a.ctx, a.agentID))
	// If the lock were shared across tenants, this would fail with
	// ErrLockHeld -- see AcquireLock's ON CONFLICT (tenant_id, name).
	require.NoError(t, b.svc.AcquireIntegrationLock(b.ctx, b.agentID))
}
