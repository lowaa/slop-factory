package oauth_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
)

// liveWorker authorizes a new worker over the code flow and returns its id and
// tokens.
func (f *fixture) liveWorker(t *testing.T, name string) (uuid.UUID, *oauth.TokenResponse) {
	t.Helper()
	tokens := f.codeFlowTokens(t, f.client(t), name)
	a, err := agent.NewPostgresStore(f.db).GetByName(f.ctx, name)
	require.NoError(t, err)
	return a.ID, tokens
}

func (f *fixture) liveGrantsOf(t *testing.T, agentID uuid.UUID) int {
	t.Helper()
	grants, err := f.svc.Grants(context.Background(), f.user)
	require.NoError(t, err)
	n := 0
	for _, g := range grants {
		if g.AgentID == agentID {
			n++
		}
	}
	return n
}

func (f *fixture) disableAndArchive(t *testing.T, id uuid.UUID) {
	t.Helper()
	_, err := f.agents.SetDisabled(f.ctx, id, true)
	require.NoError(t, err)
	_, err = f.agents.SetArchived(f.ctx, f.user, id, true)
	require.NoError(t, err)
}

func TestArchivingAWorkerRevokesItsLiveGrants(t *testing.T) {
	f := newFixture(t)
	id, tokens := f.liveWorker(t, "old-worker")
	_, err := f.agents.SetDisabled(f.ctx, id, true)
	require.NoError(t, err)
	assert.Equal(t, 1, f.liveGrantsOf(t, id), "disabling a worker keeps its grants, so re-enabling carries on")

	_, err = f.agents.SetArchived(f.ctx, f.user, id, true)
	require.NoError(t, err)

	assert.Zero(t, f.liveGrantsOf(t, id))
	_, err = f.svc.Authenticate(context.Background(), tokens.AccessToken)
	assert.ErrorIs(t, err, oauth.ErrInvalidToken)
}

func TestArchivingOneWorkerLeavesOthersGrants(t *testing.T) {
	f := newFixture(t)
	gone, _ := f.liveWorker(t, "gone")
	kept, _ := f.liveWorker(t, "kept")
	f.disableAndArchive(t, gone)
	assert.Equal(t, 1, f.liveGrantsOf(t, kept))
}

func TestAGrantForADisabledOrArchivedAgentIsRefused(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	disabled, _ := f.liveWorker(t, "disabled")
	archived, _ := f.liveWorker(t, "archived")
	_, err := f.agents.SetDisabled(f.ctx, disabled, true)
	require.NoError(t, err)
	f.disableAndArchive(t, archived)

	for name, id := range map[string]uuid.UUID{"disabled": disabled, "archived": archived} {
		_, err := f.db.Global().Exec(context.Background(), `
			INSERT INTO global_oauth_grants (id, tenant, agent_id, client_id, resource)
			VALUES ($1, $2, $3, $4, 'r')`, uuid.New(), f.user.TenantID, id, c.ID)
		assert.Error(t, err, name)
	}
}

func TestTheGrantListingNamesTheAgentEachActsAs(t *testing.T) {
	f := newFixture(t)
	id, _ := f.liveWorker(t, "named")

	grants, err := f.svc.Grants(context.Background(), f.user)
	require.NoError(t, err)
	require.Len(t, grants, 1)
	assert.Equal(t, id, grants[0].AgentID)
	assert.Equal(t, "named", grants[0].AgentName)
	assert.Equal(t, "worker", grants[0].AgentKind)
}
