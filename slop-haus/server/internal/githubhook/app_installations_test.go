package githubhook_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/githubapp"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/secretbox"
)

// putApp stores an App with a webhook secret through the real store, as the
// webhook configurer does, and returns its row id.
func (f *fixture) putApp(tenant *tenantFixture, githubAppID int64, secret string) uuid.UUID {
	f.t.Helper()
	id := uuid.New()
	app := &githubapp.App{ID: id, AppID: githubAppID, PrivateKeyEnc: []byte{0}, KeyID: f.keyring.CurrentID(), Fingerprint: strings.Repeat("f", 64)}
	f.setSecret(app, tenant, secret)
	_, err := githubapp.NewPostgresStore(f.database).PutApp(tenant.ctx, app)
	require.NoError(f.t, err)
	return id
}

func (f *fixture) setSecret(app *githubapp.App, tenant *tenantFixture, secret string) {
	f.t.Helper()
	sealed, err := f.keyring.Seal([]byte(secret), secretbox.Binding{Tenant: tenant.id, Row: app.ID, Purpose: webhookPurpose})
	require.NoError(f.t, err)
	app.WebhookSecretEnc, app.WebhookSecretKeyID = sealed, f.keyring.CurrentID()
}

func (f *fixture) addInstallation(tenant *tenantFixture, app uuid.UUID, installation int64, binding string) {
	f.t.Helper()
	_, err := githubapp.NewPostgresStore(f.database).PutCredential(tenant.ctx,
		&githubapp.Credential{ID: uuid.New(), AppRef: app, InstallationID: installation, Binding: binding})
	require.NoError(f.t, err)
}

func (f *fixture) ping(installation int64, signWith string) int {
	return f.deliverApp(appDelivery{event: "ping", signWith: signWith, body: withInstallation(`{}`, installation)}).Code
}

// One App installed twice has one webhook secret, which signs both
// installations' deliveries; changing it on the App changes it for both.
func TestReceiveApp_AnAppsInstallationsShareItsSecretThroughARotation(t *testing.T) {
	f := newFixture(t)
	app := f.putApp(f.a, 11, "secret-1")
	f.addInstallation(f.a, app, 101, "github.com/acme")
	f.addInstallation(f.a, app, 102, "github.com/other")

	assert.Equal(t, http.StatusOK, f.ping(101, "secret-1"))
	assert.Equal(t, http.StatusOK, f.ping(102, "secret-1"))

	store := githubapp.NewPostgresStore(f.database)
	rec, err := store.GetApp(f.a.ctx, app)
	require.NoError(t, err)
	f.setSecret(rec, f.a, "secret-2")
	_, err = store.PutApp(f.a.ctx, rec)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, f.ping(101, "secret-2"), "the first installation follows the rotation")
	assert.Equal(t, http.StatusOK, f.ping(102, "secret-2"), "and so does the second")
	assert.Equal(t, http.StatusUnauthorized, f.ping(101, "secret-1"), "the old secret is dead")
	assert.Equal(t, http.StatusUnauthorized, f.ping(102, "secret-1"))
}

// Two Apps in a tenant: an installation is verified against its own App's
// secret only, never the tenant's other App's.
func TestReceiveApp_ADeliverySignedWithAnotherAppsSecretIsRefused(t *testing.T) {
	f := newFixture(t)
	one := f.putApp(f.a, 11, "secret-one")
	two := f.putApp(f.a, 12, "secret-two")
	f.addInstallation(f.a, one, 101, "github.com/acme")
	f.addInstallation(f.a, two, 201, "github.com/lowaa")

	assert.Equal(t, http.StatusOK, f.ping(101, "secret-one"))
	assert.Equal(t, http.StatusOK, f.ping(201, "secret-two"))
	assert.Equal(t, http.StatusUnauthorized, f.ping(101, "secret-two"), "App two's secret does not sign App one's installation")
	assert.Equal(t, http.StatusUnauthorized, f.ping(201, "secret-one"))
}

// Removing one credential stops its installation's deliveries and leaves the
// App's other installation verifying.
func TestReceiveApp_ARemovedCredentialLeavesItsAppsOtherInstallations(t *testing.T) {
	f := newFixture(t)
	app := f.putApp(f.a, 11, "secret-1")
	f.addInstallation(f.a, app, 101, "github.com/acme")
	f.addInstallation(f.a, app, 102, "github.com/other")

	store := githubapp.NewPostgresStore(f.database)
	cred, err := store.GetCredentialByBinding(f.a.ctx, "github.com/acme")
	require.NoError(t, err)
	_, err = store.DeleteCredential(f.a.ctx, cred.ID)
	require.NoError(t, err)

	assert.Contains(t, f.deliverApp(appDelivery{event: "ping", signWith: "secret-1", body: withInstallation(`{}`, 101)}).Body.String(), "ignored")
	assert.Equal(t, http.StatusOK, f.ping(102, "secret-1"))
	assert.NotContains(t, f.deliverApp(appDelivery{event: "ping", signWith: "secret-1", body: withInstallation(`{}`, 102)}).Body.String(), "ignored")
}
