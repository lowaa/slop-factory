package reporter

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/report/providerconfig"
)

func configServer(t *testing.T, status int, body string) (*Reporter, *http.Request) {
	t.Helper()
	var seen http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = *r
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	rep, err := New(srv.URL, "secret-token", t.TempDir(), slog.DiscardHandler)
	require.NoError(t, err)
	return rep, &seen
}

func TestFetchProviderConfig_ReturnsTheValidatedConfig(t *testing.T) {
	rep, seen := configServer(t, http.StatusOK,
		`{"revision":3,"primary":{"provider":"openrouter","model":"openai/gpt-5.6-luna","reasoning_effort":""}}`)
	cfg, err := rep.FetchProviderConfig(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(3), cfg.Revision)
	assert.Equal(t, "openrouter", cfg.Primary.Provider)
	assert.Equal(t, providerconfig.Path, seen.URL.Path)
	assert.Equal(t, http.MethodGet, seen.Method)
	assert.Equal(t, "Bearer secret-token", seen.Header.Get("Authorization"))
}

func TestFetchProviderConfig_NotFoundMeansNone(t *testing.T) {
	rep, _ := configServer(t, http.StatusNotFound, `{"title":"Not Found"}`)
	_, err := rep.FetchProviderConfig(context.Background())
	assert.ErrorIs(t, err, ErrNoProviderConfig)
}

func TestFetchProviderConfig_RefusesAnInvalidConfig(t *testing.T) {
	for name, body := range map[string]string{
		"unknown provider": `{"revision":1,"primary":{"provider":"evil-proxy","model":"m"}}`,
		"malformed model":  `{"revision":1,"primary":{"provider":"openrouter","model":"a b"}}`,
		"not json":         `<html>`,
	} {
		t.Run(name, func(t *testing.T) {
			rep, _ := configServer(t, http.StatusOK, body)
			_, err := rep.FetchProviderConfig(context.Background())
			require.Error(t, err)
			assert.NotErrorIs(t, err, ErrNoProviderConfig)
		})
	}
}

// A server echoing the Authorization header back in an error must not get
// the token into a message slop-review prints.
func TestFetchProviderConfig_ErrorsNeverCarryTheToken(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusInternalServerError} {
		rep, _ := configServer(t, status, "bad token: secret-token")
		_, err := rep.FetchProviderConfig(context.Background())
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "secret-token")
	}
}
