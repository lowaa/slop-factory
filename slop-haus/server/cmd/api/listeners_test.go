package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/clientip"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/config"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/githubhook"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/intake"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/metrics"
)

func gatedHandler(t *testing.T, trusted []string) http.Handler {
	t.Helper()
	resolver, err := clientip.NewResolver(trusted)
	require.NoError(t, err)
	limits := &limiters{telemetry: metrics.New(&metrics.Options{}), logHandler: slog.DiscardHandler}
	gate := clientGate(limits, resolver, config.RateLimit{Name: "test_client", Limit: 1, Window: time.Minute}, slog.DiscardHandler)
	return gate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
}

func hit(h http.Handler, remote, forwarded string) int {
	req := httptest.NewRequest(http.MethodPost, "/hooks/x", nil)
	req.RemoteAddr = remote
	if forwarded != "" {
		req.Header.Set("X-Forwarded-For", forwarded)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestClientGateCountsTheForwardedAddressBehindATrustedProxy(t *testing.T) {
	t.Parallel()
	h := gatedHandler(t, []string{"10.42.0.0/16"})
	require.Equal(t, http.StatusNoContent, hit(h, "10.42.0.9:1", "198.51.100.7"))
	assert.Equal(t, http.StatusTooManyRequests, hit(h, "10.42.0.9:2", "198.51.100.7"), "the same client behind the proxy")
	assert.Equal(t, http.StatusNoContent, hit(h, "10.42.0.9:3", "198.51.100.8"), "another client behind the same proxy")
}

func TestClientGateIgnoresForwardedForFromAnUntrustedPeer(t *testing.T) {
	t.Parallel()
	h := gatedHandler(t, []string{"10.42.0.0/16"})
	require.Equal(t, http.StatusNoContent, hit(h, "203.0.113.5:1", "198.51.100.1"))
	assert.Equal(t, http.StatusTooManyRequests, hit(h, "203.0.113.5:2", "198.51.100.2"), "rotating the claimed address buys no fresh allowance")
}

// The wiring itself: the doors the api serves are gated, whatever the
// receivers do when asked directly.
func TestHookDoorsAreGatedPerClient(t *testing.T) {
	t.Parallel()
	resolver, err := clientip.NewResolver(nil)
	require.NoError(t, err)
	limits := &limiters{telemetry: metrics.New(&metrics.Options{}), logHandler: slog.DiscardHandler}
	cfg := &config.Config{
		Webhook: &config.WebhookConfig{ClientLimitPerMinute: 1},
		Intake:  &config.IntakeConfig{ClientLimitPerMinute: 1},
	}
	hooks, err := githubhook.NewService(nil, nil, &githubhook.Config{Host: &codehosttest.Fake{Domain: "git.example.test"}}, slog.DiscardHandler)
	require.NoError(t, err)
	intakes := intake.NewService(nil, nil, &intake.Config{}, nil, slog.DiscardHandler)

	for name, h := range map[string]http.Handler{
		"github webhook": webhookHandler(cfg, hooks, limits, resolver, slog.DiscardHandler),
		"intake":         intakeHandler(cfg, intakes, limits, resolver, slog.DiscardHandler),
	} {
		t.Run(name, func(t *testing.T) {
			assert.NotEqual(t, http.StatusTooManyRequests, hit(h, "192.0.2.1:1", ""))
			assert.Equal(t, http.StatusTooManyRequests, hit(h, "192.0.2.1:2", ""))
		})
	}
}
