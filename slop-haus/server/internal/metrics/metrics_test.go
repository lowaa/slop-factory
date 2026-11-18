package metrics_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/metrics"
)

func scrape(t *testing.T, m *metrics.Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body, err := io.ReadAll(rec.Body)
	require.NoError(t, err)
	return string(body)
}

// Every known series is there at zero before anything happens, so an
// alert on increase() fires on the first event, not the second.
func TestKnownSeriesStartAtZero(t *testing.T) {
	m := metrics.New(&metrics.Options{Actions: []string{"sign_in", "oauth_token_reuse"}, Methods: []string{"password", "github"}})
	m.TrackLimiter("login")

	out := scrape(t, m)
	for _, series := range []string{
		`slophaus_audit_events_total{action="oauth_token_reuse",method=""} 0`,
		`slophaus_audit_events_total{action="sign_in",method="github"} 0`,
		`slophaus_audit_events_total{action="sign_in",method="password"} 0`,
		`slophaus_audit_events_total{action="sign_in",method="other"} 0`,
		`slophaus_rate_limit_refusals_total{limiter="login"} 0`,
		`slophaus_rate_limit_overflow_total{limiter="login"} 0`,
	} {
		assert.Contains(t, out, series)
	}
	assert.Contains(t, out, "go_goroutines", "the runtime's own metrics too")
}

func TestCounting(t *testing.T) {
	m := metrics.New(&metrics.Options{Actions: []string{"sign_in"}, Methods: []string{"password"}})
	m.AuditEvent("sign_in", "password")
	m.AuditEvent("sign_in", "password")
	m.RateLimitRefused("mail")
	m.RateLimitOverflowed("mail")

	out := scrape(t, m)
	assert.Contains(t, out, `slophaus_audit_events_total{action="sign_in",method="password"} 2`)
	assert.Contains(t, out, `slophaus_rate_limit_refusals_total{limiter="mail"} 1`)
	assert.Contains(t, out, `slophaus_rate_limit_overflow_total{limiter="mail"} 1`)
}

// The method label only ever takes the configured methods' names: anything
// else a caller passes is "other", so a label value can never be chosen
// by whoever sent the request.
func TestAnUnknownMethodIsOther(t *testing.T) {
	m := metrics.New(&metrics.Options{Actions: []string{"sign_in_method_removed"}, Methods: []string{"password"}})
	m.AuditEvent("sign_in_method_removed", "retired-idp")

	out := scrape(t, m)
	assert.Contains(t, out, `slophaus_audit_events_total{action="sign_in_method_removed",method="other"} 1`)
	assert.NotContains(t, out, "retired-idp")
}

func TestSecretWriteRefusals(t *testing.T) {
	m := metrics.New(&metrics.Options{SecretKinds: []string{"github_token"}})
	out := scrape(t, m)
	assert.Contains(t, out, `slophaus_secret_write_refused_total{kind="github_token"} 0`, "starts at zero so an alert fires on the first")

	m.SecretWriteRefused("github_token")
	assert.Contains(t, scrape(t, m), `slophaus_secret_write_refused_total{kind="github_token"} 1`)
}

func TestLandingSeries(t *testing.T) {
	m := metrics.New(&metrics.Options{})
	m.TrackLanding([]string{"merged", "checks_red", "batch_interaction"})
	out := scrape(t, m)
	for _, series := range []string{
		`slophaus_landing_outcomes_total{outcome="checks_red"} 0`,
		`slophaus_landing_outcomes_total{outcome="other"} 0`,
		`slophaus_landing_batch_members_total{result="red"} 0`,
	} {
		assert.Contains(t, out, series)
	}

	m.LandingOutcome("merged", 2, 10*time.Minute, 1)
	m.LandingOutcome("merged", -1, 0, 2)
	m.LandingOutcome("batch_interaction", 0, time.Minute, 2)
	m.LandingOutcome("checks_red", 1, time.Minute, 1)
	m.LandingOutcome("never_heard_of_it", -1, 0, 3)

	out = scrape(t, m)
	assert.Contains(t, out, `slophaus_landing_outcomes_total{outcome="merged"} 2`)
	assert.Contains(t, out, `slophaus_landing_outcomes_total{outcome="other"} 1`, "an unknown outcome cannot mint a label")
	assert.Contains(t, out, "slophaus_landing_queue_depth_count 3", "an unknown depth is not observed")
	assert.Contains(t, out, "slophaus_landing_time_to_land_seconds_count 1", "only a merge with a known wait")
	assert.Contains(t, out, "slophaus_landing_queue_depth_sum 3", "depths 2, 0 and 1")
	assert.Contains(t, out, "slophaus_landing_time_to_land_seconds_sum 600", "seconds, not another unit")
	assert.Contains(t, out, `slophaus_landing_batch_members_total{result="merged"} 1`)
	assert.Contains(t, out, `slophaus_landing_batch_members_total{result="red"} 1`)
	assert.Contains(t, out, `slophaus_landing_batch_members_total{result="other"} 1`, "batch of 1 is not counted; 3 is")

	assert.Contains(t, out, `slophaus_landing_batches_total{result="evidence_refused"} 0`, "pre-created")
	m.LandingBatch("red")
	m.LandingBatch("red")
	m.LandingBatch("made_up")
	out = scrape(t, m)
	assert.Contains(t, out, `slophaus_landing_batches_total{result="red"} 2`)
	assert.Contains(t, out, `slophaus_landing_batches_total{result="failed"} 1`, "an unknown result cannot mint a label")
}
