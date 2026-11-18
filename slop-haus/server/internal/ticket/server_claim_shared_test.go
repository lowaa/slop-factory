package ticket_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// landingFixture is sharedFixture with mono's remote moved to the repo the
// review helpers' PR lives in.
func landingFixture(t *testing.T) *fixture {
	t.Helper()
	f := sharedFixture(t)
	_, err := manifest.NewPostgresStore(f.database).Put(f.ctx, []manifest.Repo{
		{Path: "mono", Remote: "github.example/acme/repo", SharedPaths: []manifest.SharedPathRule{migrations}},
	}, f.human)
	require.NoError(t, err)
	return f
}

func (f *fixture) claimRival(t *testing.T, paths ...string) {
	t.Helper()
	rival := f.ticket(t, "Rival", paths...)
	got, err := f.svc.Claim(f.ctx, f.agent(t, "rival"), &rival.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Ticket)
}

func (f *fixture) workspaceStore() *ticket.PostgresStore {
	return newStore(f.database, ticket.WithStoreWorkspace(manifest.NewPolicy(manifest.NewPostgresStore(f.database))))
}

// A worker's claim may overlap another ticket inside a shared path, so the
// server's must too; a store built without the workspace blocks on it.
func TestServerClaimLanding_SharedPathOverlap(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T, rivalPaths ...string) (*fixture, int64) {
		f := landingFixture(t)
		gated := f.approvedTicket(t, f.agent(t, "author"), "mono/db/migrations/")
		f.expireAffinity(t, gated.ID)
		f.claimRival(t, rivalPaths...)
		return f, gated.ID
	}
	t.Run("granted with the workspace", func(t *testing.T) {
		f, id := setup(t, "mono/db/")
		got, err := f.workspaceStore().ServerClaimLanding(f.ctx, f.serverAgent(t), id, 1, ticket.DefaultServerLeaseTTL)
		require.NoError(t, err)
		require.NotNil(t, got.Ticket)
	})
	t.Run("blocked without the workspace", func(t *testing.T) {
		f, id := setup(t, "mono/db/")
		got, err := f.serverClaimLanding(f.serverAgent(t), id)
		require.NoError(t, err)
		assert.Nil(t, got.Ticket)
	})
	t.Run("overlap outside the shared path still blocks", func(t *testing.T) {
		f := landingFixture(t)
		gated := f.approvedTicket(t, f.agent(t, "author"), "mono/db/migrations/", "mono/svc/")
		f.expireAffinity(t, gated.ID)
		f.claimRival(t, "mono/svc/")
		got, err := f.workspaceStore().ServerClaimLanding(f.ctx, f.serverAgent(t), gated.ID, 1, ticket.DefaultServerLeaseTTL)
		require.NoError(t, err)
		assert.Nil(t, got.Ticket)
		require.NotNil(t, got.Blocked)
	})
}
