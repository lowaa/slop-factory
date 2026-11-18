package smoke_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/ratelimit"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth/oauthtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/smoke"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func TestMain(m *testing.M) {
	dbtest.Main(m, schema.Migrations)
}

const issuer = "https://slop.example"

type noReleaser struct{}

func (noReleaser) ReleaseLeasesFor(context.Context, uuid.UUID, string, time.Time) error { return nil }

type rig struct {
	provisioner *smoke.Provisioner
	oauth       *oauth.Service
	tickets     *ticket.Service
}

func newRig(t *testing.T) *rig {
	t.Helper()
	database := dbtest.WithDB(t)
	grants := &oauthtest.DeferredGrants{}
	agents := agent.NewService(agent.NewPostgresStore(database), noReleaser{}, grants, slog.DiscardHandler)
	tickets := ticket.NewService(ticket.NewPostgresStore(database), slog.DiscardHandler)
	store := oauth.NewPostgresStore(database)
	generous := &ratelimit.Config{Limit: 1000, Window: time.Minute, MaxKeys: 1000}
	oauthSvc := oauth.NewService(store, agents, &oauth.Config{
		Issuer: issuer, AccessTTL: time.Minute, RegisterLimit: generous, DeviceLookupLimit: generous, DeviceStartLimit: generous,
	}, nil, slog.DiscardHandler)
	grants.OAuth = oauthSvc
	return &rig{
		provisioner: smoke.NewProvisioner(&smoke.Deps{
			DB: database, Agents: agents, Tickets: tickets,
			Clients: oauth.NewClientAdmin(store, oauth.ResourceFor(issuer), nil, slog.DiscardHandler),
		}, slog.DiscardHandler),
		oauth:   oauthSvc,
		tickets: tickets,
	}
}

func (r *rig) signIn(t *testing.T, c *smoke.Credential) (*oauth.Access, error) {
	t.Helper()
	tokens, err := r.oauth.Token(context.Background(), &oauth.TokenRequest{
		GrantType: oauth.GrantClientCredentials, ClientID: c.ClientID, ClientSecret: c.ClientSecret,
	})
	if err != nil {
		return nil, err
	}
	return r.oauth.Authenticate(context.Background(), tokens.AccessToken)
}

// What the command hands CI signs in, read-only, as an agent of the smoke
// tenant -- and the ticket it names is there to read.
func TestACredentialSignsInReadOnlyToTheSmokeTenant(t *testing.T) {
	r := newRig(t)
	c, err := r.provisioner.Create(context.Background(), "ci")
	require.NoError(t, err)

	access, err := r.signIn(t, c)
	require.NoError(t, err)
	assert.True(t, access.ReadOnly)
	assert.Equal(t, smoke.TenantID, access.TenantID)

	got, err := r.tickets.Get(db.WithTenant(context.Background(), smoke.TenantID), c.TicketID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusPreparing, got.Status, "a fixture, not work")
}

// Running it again -- to rotate the secret -- makes a second client for
// the same agent and ticket, and leaves the first working until revoked.
func TestCreatingAgainRotatesWithoutAGap(t *testing.T) {
	r := newRig(t)
	first, err := r.provisioner.Create(context.Background(), "ci")
	require.NoError(t, err)
	second, err := r.provisioner.Create(context.Background(), "ci")
	require.NoError(t, err)

	assert.NotEqual(t, first.ClientID, second.ClientID)
	assert.Equal(t, first.TicketID, second.TicketID, "one fixture ticket, found again")
	a1, err := r.signIn(t, first)
	require.NoError(t, err)
	a2, err := r.signIn(t, second)
	require.NoError(t, err)
	assert.Equal(t, a1.AgentID, a2.AgentID, "one smoke agent, found again")

	require.NoError(t, r.provisioner.Revoke(context.Background(), first.ClientID))
	_, err = r.signIn(t, first)
	assert.Error(t, err)
	_, err = r.signIn(t, second)
	assert.NoError(t, err)
}
