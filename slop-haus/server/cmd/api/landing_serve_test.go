package main

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The landing read surface is mounted by the board, not by the routes alone:
// without the handler wired in, these answer 404.
func TestServe_LandingRoutesAreServedToASignedInUser(t *testing.T) {
	t.Parallel()
	b := boot(t, &bootOptions{})
	_, token := b.signedInUser(t)

	for _, path := range []string{"/api/landing/capabilities", "/api/landing/queue"} {
		resp := do(t, &request{method: http.MethodGet, url: b.base + path, token: token})
		assert.Equal(t, http.StatusOK, resp.code, "%s: %s", path, resp.body)
		assert.Equal(t, http.StatusUnauthorized,
			status(t, &request{method: http.MethodGet, url: b.base + path}), "%s needs a sign-in", path)
	}

	resp := do(t, &request{method: http.MethodGet, url: b.base + "/api/landing/capabilities", token: token})
	require.Equal(t, http.StatusOK, resp.code)
	assert.Contains(t, resp.body, "merge_method")
}
