package oauth_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
)

func (f *fixture) join(t *testing.T, who *authn.User, role string) {
	t.Helper()
	_, err := f.db.Global().Exec(context.Background(),
		`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES ($1, $2, $3, $4)`,
		uuid.New(), who.TenantID, who.ID, role)
	require.NoError(t, err)
}

// A token issued before a conductor is disabled must not come back when the
// conductor is switched on again: that is what lets its owner answer a leaked
// token by switching it off and on.
func TestAConductorsTokensStayDeadOnceItIsDisabled(t *testing.T) {
	f := newFixture(t)
	owner := *f.user
	owner.AuthTime = time.Now()
	f.join(t, &owner, "member")
	admin := &authn.User{ID: dbtest.NewUser(t, f.db), TenantID: owner.TenantID, AuthTime: time.Now()}
	f.join(t, admin, "owner")
	conductors := agent.NewConductorService(f.agents, f.members, slog.DiscardHandler)

	c, err := conductors.Register(f.ctx, &owner, "my-conductor")
	require.NoError(t, err)
	client := f.client(t)
	verifier, challenge := pkce(t)
	tokens, err := f.exchange(client, f.authorize(t, client, challenge, &oauth.Decision{Approve: true, AgentID: &c.ID}), verifier)
	require.NoError(t, err)
	require.True(t, f.works(t, tokens.AccessToken), "the conductor's token works before it is disabled")

	_, err = conductors.SetDisabled(f.ctx, admin, c.ID, true)
	require.NoError(t, err)
	assert.False(t, f.works(t, tokens.AccessToken))

	_, err = conductors.SetDisabled(f.ctx, admin, c.ID, false)
	assert.ErrorIs(t, err, agent.ErrNotOwner, "a tenant owner cannot switch it back on")
	_, err = conductors.SetDisabled(f.ctx, &owner, c.ID, false)
	require.NoError(t, err, "its owner can")

	assert.False(t, f.works(t, tokens.AccessToken), "but the token issued before the disable stays dead")
	_, err = f.refresh(client.ID, tokens.RefreshToken)
	assert.Equal(t, "invalid_grant", oauthCode(t, err))
}
