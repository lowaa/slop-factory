package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/login"
	"github.com/lowaa/slop-factory/go-auth/login/logintest"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/config"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/harness"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/metrics"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
)

func TestMain(m *testing.M) {
	dbtest.Main(m, schema.Migrations)
}

// Production starts with no provider configured, and signs in with
// passwords alone.
func TestNoLoginProvidersByDefault(t *testing.T) {
	t.Parallel()
	providers, closeFn, err := loginProviders(context.Background(), &config.Config{PublicBaseURL: "https://slop.example"},
		login.NewIssuerBindings(dbtest.WithDB(t).Global()))
	require.NoError(t, err)
	defer closeFn()
	assert.Empty(t, providers.Names())
}

// A provider name is bound to the issuer it first started with: a restart
// pointing it at another issuer refuses to start, rather than let that
// issuer's subjects sign in as the old one's.
func TestLoginProvidersRefuseANameThatMovesIssuer(t *testing.T) {
	t.Parallel()
	store := login.NewIssuerBindings(dbtest.WithDB(t).Global())
	first := logintest.New(t, "client", "secret")
	second := logintest.New(t, "client", "secret")
	cfg := &config.Config{
		PublicBaseURL: "https://slop.example",
		GitHub:        &config.GitHubConfig{ClientID: "id", ClientSecret: "secret"},
		OIDC: &config.OIDCConfig{IssuerURL: first.URL, ClientID: "client", ClientSecret: "secret",
			ProviderName: "sso", DisplayName: "SSO"},
	}
	start := func() error {
		_, closeFn, err := loginProviders(context.Background(), cfg, store)
		if err == nil {
			closeFn()
		}
		return err
	}

	providers, closeFn, err := loginProviders(context.Background(), cfg, store)
	require.NoError(t, err)
	closeFn()
	assert.Equal(t, []string{"github", "sso"}, providers.Names())

	cfg.OIDC.IssuerURL = first.URL + "/"
	assert.NoError(t, start(), "the same issuer, spelled with a trailing slash")

	cfg.OIDC.IssuerURL = second.URL
	err = start()
	require.Error(t, err)
	assert.Contains(t, err.Error(), first.URL)
}

// The issuer bindings' SQL against slop-haus's own global_auth_providers
// table; go-auth tests the rule itself.
func TestIssuerBindingsAgainstTheSchema(t *testing.T) {
	t.Parallel()
	bindings := login.NewIssuerBindings(dbtest.WithDB(t).Global())
	ctx := context.Background()

	require.NoError(t, bindings.Register(ctx, "dex", "https://dex.example"))
	require.NoError(t, bindings.Register(ctx, "dex", "https://dex.example"), "a restart with the same issuer")
	require.NoError(t, bindings.Register(ctx, "github", login.GitHubIssuer), "names are independent")

	err := bindings.Register(ctx, "dex", "https://other.example")
	require.Error(t, err, "the name may never move to another issuer")
	assert.Contains(t, err.Error(), "https://dex.example")
	assert.Contains(t, err.Error(), "https://other.example")
}

// The metrics listener serves /metrics and nothing else.
func TestTheMetricsListenerServesOnlyMetrics(t *testing.T) {
	t.Parallel()
	mux := metricsMux(metrics.New(&metrics.Options{}))
	for path, want := range map[string]int{"/metrics": http.StatusOK, "/api/auth/me": http.StatusNotFound, "/": http.StatusNotFound} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, want, rec.Code, path)
	}
}

func TestSignInMethodsAreTheConfiguredOnes(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []string{"password"}, signInMethods(&config.Config{}))
	assert.Equal(t, []string{"password", "github", "sso"}, signInMethods(&config.Config{
		GitHub: &config.GitHubConfig{ClientID: "id", ClientSecret: "secret"},
		OIDC:   &config.OIDCConfig{ProviderName: "sso"},
	}))
}

// A deploy polls this to see its build take over, so it must say which
// build is serving, and never from a cache.
func TestVersionSaysWhichBuildIsServing(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	versionHandler("20260928T151918Z-77bef973")(rec, httptest.NewRequest(http.MethodGet, "/api/version", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"version":"20260928T151918Z-77bef973"}`, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
}

// A harness added to the registry needs no migration: booting records it, so
// variants and attempts can reference it.
func TestOpenDatabaseRecordsRegisteredHarnesses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	role, password := dbtest.AppCredentials()
	dsn := dbtest.WithOwnerDSN(t)
	owner, err := db.New(ctx, dsn)
	require.NoError(t, err)
	defer owner.Close()
	_, err = owner.Global().Exec(ctx, `DELETE FROM global_harnesses WHERE name = $1`, harness.Names()[0])
	require.NoError(t, err)

	database, err := openDatabase(ctx, &config.Config{DatabaseURL: dsn, AppDBRole: role, AppDBPassword: password})
	require.NoError(t, err)
	defer database.Close()

	rows, err := database.Global().Query(ctx, `SELECT name FROM global_harnesses`)
	require.NoError(t, err)
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	assert.ElementsMatch(t, harness.Names(), names)
}
