package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const recorded = "../../internal/pricegen/testdata/models.json"

func TestRun_FromSavedResponse(t *testing.T) {
	out := filepath.Join(t.TempDir(), "prices_gen.go")
	require.NoError(t, run(&source{file: recorded}, out))
	src, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Contains(t, string(src), `"anthropic/claude-opus-5.5"`)
}

func TestRun_Fetches(t *testing.T) {
	body, err := os.ReadFile(recorded)
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	out := filepath.Join(t.TempDir(), "prices_gen.go")
	require.NoError(t, run(&source{url: srv.URL}, out))
	src, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Contains(t, string(src), `"anthropic/claude-opus-5.5"`)
}

func TestRun_FetchErrorWritesNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	out := filepath.Join(t.TempDir(), "prices_gen.go")
	assert.ErrorContains(t, run(&source{url: srv.URL}, out), "502")
	assert.NoFileExists(t, out)
}
