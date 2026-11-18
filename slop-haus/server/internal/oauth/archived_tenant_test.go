package oauth_test

import (
	"context"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
)

// setArchived flips the tenant's archive state directly, as the store does
// (user.Service.ArchiveTenant needs the user package's whole fixture).
func (f *fixture) setArchived(t *testing.T, tenantID uuid.UUID, archived bool) {
	t.Helper()
	q := `UPDATE global_tenants SET archived_at = NULL, archived_by = NULL WHERE id = $1`
	if archived {
		q = `UPDATE global_tenants SET archived_at = now(), archived_by = (SELECT id FROM global_users LIMIT 1) WHERE id = $1`
	}
	_, err := f.db.Global().Exec(context.Background(), q, tenantID)
	require.NoError(t, err)
}

func (f *fixture) archive(t *testing.T)   { t.Helper(); f.setArchived(t, f.user.TenantID, true) }
func (f *fixture) unarchive(t *testing.T) { t.Helper(); f.setArchived(t, f.user.TenantID, false) }

func TestAnArchivedTenantsAccessTokenIsRefusedUntilUnarchived(t *testing.T) {
	f := newFixture(t)
	tokens := f.codeFlowTokens(t, f.client(t), "b")
	require.True(t, f.works(t, tokens.AccessToken), "it works before the archive")

	f.archive(t)
	_, err := f.svc.Authenticate(context.Background(), tokens.AccessToken)
	assert.ErrorIs(t, err, oauth.ErrInvalidToken)

	f.unarchive(t)
	assert.True(t, f.works(t, tokens.AccessToken), "the grant survives the archive")
}

func TestAnArchivedTenantsRefreshTokenIssuesNothingAndStaysUnspent(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	tokens := f.codeFlowTokens(t, c, "b")

	f.archive(t)
	_, err := f.refresh(c.ID, tokens.RefreshToken)
	assert.Equal(t, "invalid_grant", oauthCode(t, err))

	f.unarchive(t)
	_, err = f.refresh(c.ID, tokens.RefreshToken)
	assert.NoError(t, err, "refused without being spent or the grant revoked")
}

func TestAnArchivedTenantsRotatedRefreshTokenIsNotReplayed(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	first := f.codeFlowTokens(t, c, "b")
	_, err := f.refresh(c.ID, first.RefreshToken)
	require.NoError(t, err)

	f.archive(t)
	_, err = f.refresh(c.ID, first.RefreshToken)
	assert.Equal(t, "invalid_grant", oauthCode(t, err))
}

func TestACodeForAnArchivedTenantIsRefused(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	verifier, challenge := pkce(t)
	code := f.authorize(t, c, challenge, newAgent("b"))

	f.archive(t)
	_, err := f.exchange(c, code, verifier)
	assert.Equal(t, "invalid_grant", oauthCode(t, err))
}

func TestADeviceCodeForAnArchivedTenantIsRefused(t *testing.T) {
	f := newFixture(t)
	d := f.startDevice(t)
	require.NoError(t, f.svc.DecideDevice(f.ctx, f.user, d.UserCode, newAgent("hooks-1")))

	f.archive(t)
	_, err := f.poll(d)
	assert.Equal(t, "invalid_grant", oauthCode(t, err))
}

func TestAReadOnlyClientOfAnArchivedTenantGetsNoToken(t *testing.T) {
	f := newFixture(t)
	id, secret, _ := f.readOnlyClient(t, "smoke-agent")
	_, err := f.clientCredentials(id, secret)
	require.NoError(t, err, "it works before the archive")

	f.archive(t)
	_, err = f.clientCredentials(id, secret)
	assert.Equal(t, "invalid_grant", oauthCode(t, err))
}

func TestASlotCredentialForAnArchivedTenantIsNotIssued(t *testing.T) {
	f := newFixture(t)
	sup, _ := f.supervisor(t, "box-1", 2)
	slot := f.slotOf(t, sup, "slot-1")

	f.archive(t)
	_, err := f.svc.IssueSlotCredential(f.ctx, f.user.TenantID, sup.ID, slot.ID)
	assert.Error(t, err)
}

// An owner approving in a session that outlived the archive must not put a
// new grant, and so a new agent, into the frozen tenant.
func TestConsentIntoAnArchivedTenantIsRefused(t *testing.T) {
	f := newFixture(t)
	c := f.client(t)
	_, challenge := pkce(t)
	consent, err := f.svc.Authorize(context.Background(), &oauth.AuthorizeParams{
		ResponseType: "code", ClientID: c.ID, RedirectURI: redirect,
		CodeChallenge: challenge, CodeChallengeMethod: "S256", State: "st-1", Resource: issuer + "/mcp",
	})
	require.NoError(t, err)
	u, err := url.Parse(consent)
	require.NoError(t, err)

	f.archive(t)
	_, err = f.svc.DecideAuthRequest(f.ctx, f.user, uuid.MustParse(u.Query().Get("request")), newAgent("late"))
	assert.Error(t, err)
	var n int
	require.NoError(t, f.db.Global().QueryRow(context.Background(),
		`SELECT count(*) FROM global_oauth_grants WHERE tenant = $1`, f.user.TenantID).Scan(&n))
	assert.Zero(t, n, "no grant was made")
}
