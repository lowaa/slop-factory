package schema_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema/seeddata"
)

const codehostApps = 176

// Every credential row becomes one App, keeping its id (what its sealed key
// and webhook secret are bound to), and one credential bound to the host-wide
// 'github.com': an installed one with a webhook, a pending one, and no row at
// all for a tenant that never connected.
func TestCodehostAppsMigrationSplitsEachCredentialIntoAnAppAndAHostWideCredential(t *testing.T) {
	ctx := context.Background()
	rig := dbtest.NewMigrationRig(t, schema.Migrations)
	require.NoError(t, rig.MigrateTo(ctx, codehostApps-1))
	installed, pending, none := uuid.New(), uuid.New(), uuid.New()
	sd := seeddata.NewSeeding(rig.App, seeddata.Seeders, []uuid.UUID{installed, pending, none})
	require.NoError(t, sd.Round(ctx, codehostApps-1, 0))

	admin := rig.Admin.Global()
	for _, stmt := range []struct {
		sql string
		arg uuid.UUID
	}{
		{`UPDATE codehost_credentials SET webhook_secret_enc = '\x0102', webhook_secret_key_id = '0123456789abcdef',
		         webhook_url = 'https://hook.test', app_slug = 'the-app' WHERE tenant_id = $1`, installed},
		{`UPDATE codehost_credentials SET installation_id = NULL WHERE tenant_id = $1`, pending},
		{`DELETE FROM codehost_credentials WHERE tenant_id = $1`, none},
	} {
		_, err := admin.Exec(ctx, stmt.sql, stmt.arg)
		require.NoError(t, err)
	}

	require.NoError(t, rig.MigrateTo(ctx, codehostApps))

	type appRow struct {
		id          uuid.UUID
		appID       int64
		slug        *string
		secret      []byte
		url         *string
		credentials int
	}
	readApp := func(tenant uuid.UUID) *appRow {
		var r appRow
		err := admin.QueryRow(ctx, `
			SELECT a.id, a.app_id, a.app_slug, a.webhook_secret_enc, a.webhook_url,
			       (SELECT count(*) FROM codehost_credentials c WHERE c.app_ref = a.id)
			  FROM codehost_apps a WHERE a.tenant_id = $1`, tenant).Scan(&r.id, &r.appID, &r.slug, &r.secret, &r.url, &r.credentials)
		if err != nil {
			return nil
		}
		return &r
	}
	credential := func(tenant uuid.UUID) (binding string, installation *int64) {
		require.NoError(t, admin.QueryRow(ctx,
			`SELECT binding, installation_id FROM codehost_credentials WHERE tenant_id = $1`, tenant).Scan(&binding, &installation))
		return binding, installation
	}

	app := readApp(installed)
	require.NotNil(t, app)
	assert.Equal(t, installed, app.id, "the App keeps the old row's id, which its sealed fields are bound to")
	assert.Equal(t, int64(1), app.appID)
	assert.Equal(t, "the-app", *app.slug)
	assert.Equal(t, []byte{1, 2}, app.secret)
	assert.Equal(t, "https://hook.test", *app.url)
	assert.Equal(t, 1, app.credentials)
	binding, installation := credential(installed)
	assert.Equal(t, "github.com", binding)
	require.NotNil(t, installation)
	assert.Equal(t, int64(2), *installation)

	app = readApp(pending)
	require.NotNil(t, app)
	assert.Equal(t, 1, app.credentials, "a pending row is an App with one pending credential")
	binding, installation = credential(pending)
	assert.Equal(t, "github.com", binding)
	assert.Nil(t, installation)

	assert.Nil(t, readApp(none), "a tenant that never connected has nothing")
	var n int
	require.NoError(t, admin.QueryRow(ctx, `SELECT count(*) FROM codehost_credentials WHERE tenant_id = $1`, none).Scan(&n))
	assert.Zero(t, n)

	// The installation lookup is rebuilt: only the installed credential with
	// a webhook secret is in it, naming its App.
	var lookups int
	var appRef, credentialID uuid.UUID
	require.NoError(t, admin.QueryRow(ctx, `SELECT count(*) FROM global_github_app_installations WHERE tenant = ANY($1)`,
		[]uuid.UUID{installed, pending, none}).Scan(&lookups))
	assert.Equal(t, 1, lookups)
	require.NoError(t, admin.QueryRow(ctx, `SELECT app_ref, credential_id FROM global_github_app_installations WHERE tenant = $1`, installed).Scan(&appRef, &credentialID))
	assert.Equal(t, installed, appRef)
	assert.NotEqual(t, installed, credentialID)
}
