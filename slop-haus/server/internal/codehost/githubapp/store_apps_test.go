package githubapp_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/githubapp"
)

func newApp(f *fixture, githubAppID int64) *githubapp.App {
	return &githubapp.App{ID: uuid.New(), AppID: githubAppID, AppSlug: "slug", PrivateKeyEnc: []byte{1},
		KeyID: f.keyring.CurrentID(), Fingerprint: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}
}

func (f *fixture) putApp(tn *tenant, githubAppID int64) *githubapp.App {
	f.t.Helper()
	a, err := f.store.PutApp(tn.ctx, newApp(f, githubAppID))
	require.NoError(f.t, err)
	return a
}

func (f *fixture) putCredential(tn *tenant, app *githubapp.App, installation int64, binding string) *githubapp.Credential {
	f.t.Helper()
	c, err := f.store.PutCredential(tn.ctx, &githubapp.Credential{ID: uuid.New(), AppRef: app.ID, InstallationID: installation, Binding: binding})
	require.NoError(f.t, err)
	return c
}

func TestStoreApps_AppIDIsUniquePerTenantAndReplacesInPlace(t *testing.T) {
	f := newFixture(t)
	a, b := f.tenant(), f.tenant()
	app := f.putApp(a, 11)

	_, err := f.store.PutApp(a.ctx, newApp(f, 11))
	assert.ErrorIs(t, err, githubapp.ErrAppAlreadyConnected, "a second App with a connected app_id")
	_, err = f.store.PutApp(b.ctx, newApp(f, 11))
	assert.NoError(t, err, "another tenant may connect the same App")

	replaced := *app
	replaced.PrivateKeyEnc, replaced.KeyID = []byte{9}, "0123456789abcdef"
	replaced.WebhookSecretEnc, replaced.WebhookSecretKeyID, replaced.WebhookURL = []byte{7}, "0123456789abcdef", "https://hook.test"
	got, err := f.store.PutApp(a.ctx, &replaced)
	require.NoError(t, err)
	assert.Equal(t, app.ID, got.ID)
	assert.Equal(t, []byte{9}, got.PrivateKeyEnc)
	assert.Equal(t, "https://hook.test", got.WebhookURL)
	apps, err := f.store.ListApps(a.ctx)
	require.NoError(t, err)
	assert.Len(t, apps, 1, "replaced in place, not added")
}

func TestStoreApps_CredentialBindingAndInstallationRules(t *testing.T) {
	f := newFixture(t)
	tn := f.tenant()
	one, two := f.putApp(tn, 11), f.putApp(tn, 12)
	first := f.putCredential(tn, one, 101, "github.com/acme")

	t.Run("an identical binding of the same App replaces in place", func(t *testing.T) {
		again, err := f.store.PutCredential(tn.ctx, &githubapp.Credential{ID: uuid.New(), AppRef: one.ID, InstallationID: 101, Binding: "github.com/acme"})
		require.NoError(t, err)
		assert.Equal(t, first.ID, again.ID)
		all, err := f.store.ListCredentials(tn.ctx)
		require.NoError(t, err)
		assert.Len(t, all, 1)
	})
	t.Run("a different credential with the same binding is refused", func(t *testing.T) {
		_, err := f.store.PutCredential(tn.ctx, &githubapp.Credential{ID: uuid.New(), AppRef: two.ID, InstallationID: 201, Binding: "github.com/acme"})
		assert.ErrorIs(t, err, githubapp.ErrBindingTaken, "another App")
		_, err = f.store.PutCredential(tn.ctx, &githubapp.Credential{ID: uuid.New(), AppRef: one.ID, InstallationID: 102, Binding: "github.com/acme"})
		assert.ErrorIs(t, err, githubapp.ErrBindingTaken, "another installation")
	})
	t.Run("an installation is at most one credential", func(t *testing.T) {
		_, err := f.store.PutCredential(tn.ctx, &githubapp.Credential{ID: uuid.New(), AppRef: two.ID, InstallationID: 101, Binding: "github.com/lowaa"})
		assert.ErrorIs(t, err, githubapp.ErrInstallationTaken)
		_, err = f.store.SetCredentialBinding(tn.ctx, first.ID, "github.com/lowaa")
		assert.NoError(t, err, "a re-bind keeps the installation")
	})
	t.Run("a pending credential is adopted in place", func(t *testing.T) {
		pending := f.putCredential(tn, two, 0, "github.com")
		assert.True(t, pending.Pending())
		adopted, err := f.store.PutCredential(tn.ctx, &githubapp.Credential{ID: uuid.New(), AppRef: two.ID, InstallationID: 202, Binding: "github.com"})
		require.NoError(t, err)
		assert.Equal(t, pending.ID, adopted.ID)
		assert.Equal(t, int64(202), adopted.InstallationID)
	})
	t.Run("a re-bind onto a taken binding is refused", func(t *testing.T) {
		_, err := f.store.SetCredentialBinding(tn.ctx, first.ID, "github.com")
		assert.ErrorIs(t, err, githubapp.ErrBindingTaken)
	})
	t.Run("a credential needs an App that exists", func(t *testing.T) {
		_, err := f.store.PutCredential(tn.ctx, &githubapp.Credential{ID: uuid.New(), AppRef: uuid.New(), InstallationID: 9, Binding: "github.com/x"})
		assert.ErrorIs(t, err, githubapp.ErrAppNotFound)
	})
	t.Run("a malformed binding is refused", func(t *testing.T) {
		for _, binding := range []string{"", "GitHub.com/Acme", "github.com/", "/acme", "github.com/a/b/c", "github.com/a b"} {
			_, err := f.store.PutCredential(tn.ctx, &githubapp.Credential{ID: uuid.New(), AppRef: one.ID, InstallationID: 5, Binding: binding})
			assert.ErrorIs(t, err, githubapp.ErrInvalidBinding, "%q", binding)
		}
	})
}

func TestStoreApps_OneAppTwoInstallationsAndTwoApps(t *testing.T) {
	f := newFixture(t)
	tn := f.tenant()
	one, two := f.putApp(tn, 11), f.putApp(tn, 12)
	f.putCredential(tn, one, 101, "github.com/acme")
	f.putCredential(tn, one, 102, "github.com/lowaa")
	f.putCredential(tn, two, 201, "github.com")

	creds, err := f.store.ListCredentials(tn.ctx)
	require.NoError(t, err)
	assert.Len(t, creds, 3)
	byApp := map[uuid.UUID]int{}
	for _, c := range creds {
		byApp[c.AppRef]++
	}
	assert.Equal(t, 2, byApp[one.ID])
	assert.Equal(t, 1, byApp[two.ID])
	byBinding, err := f.store.GetCredentialByBinding(tn.ctx, "github.com/lowaa")
	require.NoError(t, err)
	assert.Equal(t, int64(102), byBinding.InstallationID)
	_, err = f.store.GetCredentialByBinding(tn.ctx, "github.com/nobody")
	assert.ErrorIs(t, err, githubapp.ErrCredentialNotFound)
}

func TestStoreApps_RemovingAnAppRemovesItsCredentialsButNotTheReverse(t *testing.T) {
	f := newFixture(t)
	tn := f.tenant()
	one, two := f.putApp(tn, 11), f.putApp(tn, 12)
	c1 := f.putCredential(tn, one, 101, "github.com/acme")
	f.putCredential(tn, one, 102, "github.com/lowaa")
	f.putCredential(tn, two, 201, "github.com")

	removed, err := f.store.DeleteCredential(tn.ctx, c1.ID)
	require.NoError(t, err)
	assert.True(t, removed)
	_, err = f.store.GetApp(tn.ctx, one.ID)
	assert.NoError(t, err, "the App stays")

	removed, err = f.store.DeleteApp(tn.ctx, one.ID)
	require.NoError(t, err)
	assert.True(t, removed)
	left, err := f.store.ListCredentials(tn.ctx)
	require.NoError(t, err)
	require.Len(t, left, 1, "its credentials went with it, the other App's did not")
	assert.Equal(t, two.ID, left[0].AppRef)
}

// The lookup the receiver reads is kept by triggers; it follows credentials
// and the App's secret, and never holds an installation with no secret.
func TestStoreApps_TheInstallationLookupFollowsCredentialsAndTheAppSecret(t *testing.T) {
	f := newFixture(t)
	tn := f.tenant()
	app := newApp(f, 11)
	_, err := f.store.PutApp(tn.ctx, app)
	require.NoError(t, err)
	f.putCredential(tn, app, 101, "github.com/acme")
	f.putCredential(tn, app, 102, "github.com/lowaa")
	f.putCredential(tn, app, 0, "github.com")

	rows := func() map[int64][]byte {
		r, err := f.owners.Global().Query(context.Background(),
			`SELECT installation_id, webhook_secret_enc FROM global_github_app_installations WHERE tenant = $1`, tn.id)
		require.NoError(t, err)
		defer r.Close()
		out := map[int64][]byte{}
		for r.Next() {
			var id int64
			var secret []byte
			require.NoError(t, r.Scan(&id, &secret))
			out[id] = secret
		}
		return out
	}
	assert.Empty(t, rows(), "no secret, nothing to verify against")

	ok, err := f.store.SetWebhook(tn.ctx, app.ID, []byte{1}, "0123456789abcdef", "https://hook.test")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, map[int64][]byte{101: {1}, 102: {1}}, rows(), "both installations, not the pending credential")

	ok, err = f.store.SetWebhook(tn.ctx, app.ID, []byte{2}, "0123456789abcdef", "https://hook.test")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, map[int64][]byte{101: {2}, 102: {2}}, rows(), "a rotation reaches every installation")

	cred, err := f.store.GetCredentialByBinding(tn.ctx, "github.com/acme")
	require.NoError(t, err)
	_, err = f.store.DeleteCredential(tn.ctx, cred.ID)
	require.NoError(t, err)
	assert.Equal(t, map[int64][]byte{102: {2}}, rows())

	_, err = f.store.DeleteApp(tn.ctx, app.ID)
	require.NoError(t, err)
	assert.Empty(t, rows())
}

func TestStoreApps_AreTenantScoped(t *testing.T) {
	f := newFixture(t)
	a, b := f.tenant(), f.tenant()
	app := f.putApp(a, 11)
	f.putCredential(a, app, 101, "github.com")

	apps, err := f.store.ListApps(b.ctx)
	require.NoError(t, err)
	assert.Empty(t, apps)
	_, err = f.store.GetApp(b.ctx, app.ID)
	assert.ErrorIs(t, err, githubapp.ErrAppNotFound)
	_, err = f.store.GetCredentialByBinding(b.ctx, "github.com")
	assert.ErrorIs(t, err, githubapp.ErrCredentialNotFound)
}
