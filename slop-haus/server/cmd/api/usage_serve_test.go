package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/auth"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
)

const usagePath = "/api/usage/events"

// The browser's usage events are counted for a signed-in page and nobody else,
// and the count carries nothing that says who sent it.
func TestServe_UIUsageEventsAreCountedForSignedInPagesOnly(t *testing.T) {
	t.Parallel()
	b := boot(t, &bootOptions{metrics: true})
	userID, tenant := dbtest.NewUser(t, b.appDB), dbtest.NewTenant(t, b.appDB)
	signer := auth.NewSigner([]byte(bootJWTSecret), b.cfg.AccessTokenTTL, b.cfg.RefreshTokenTTL)
	csrfTokens, err := auth.NewCSRFTokens([]byte(bootJWTSecret))
	require.NoError(t, err)
	const sessionID = "one-browser-session"
	access, err := signer.IssueSessionAccessToken(userID, tenant, &auth.Session{ID: sessionID})
	require.NoError(t, err)
	signedIn := func(body string) *request {
		return &request{method: http.MethodPost, url: b.base + usagePath, body: body, header: map[string]string{
			"Cookie": authn.SessionCookie + "=" + access, "Origin": b.base, authn.CSRFHeader: csrfTokens.Token(sessionID),
		}}
	}
	metrics := func() string {
		resp := do(t, &request{method: http.MethodGet, url: "http://" + b.cfg.MetricsListenAddr + "/metrics"})
		require.Equal(t, http.StatusOK, resp.code)
		return resp.body
	}
	const good = `{"events":[` +
		`{"name":"steward_answer","attrs":{"screen":"ticket","action":"approve"}},` +
		`{"name":"made_up_event","attrs":{"screen":"ticket"}},` +
		`{"name":"new_ticket","attrs":{"screen":"board","action":"open_form","user":"` + "someone@example.com" + `"}},` +
		`{"name":"screen_time","attrs":{"screen":"board","visible_ms":15000}}]}`

	anonymous := do(t, &request{method: http.MethodPost, url: b.base + usagePath, body: good, header: map[string]string{"Origin": b.base}})
	assert.Equal(t, http.StatusUnauthorized, anonymous.code, anonymous.body)

	noCSRF := signedIn(good)
	delete(noCSRF.header, authn.CSRFHeader)
	assert.Equal(t, http.StatusForbidden, do(t, noCSRF).code, "a write needs the session's CSRF token like the board's others")

	resp := do(t, signedIn(good))
	require.Equal(t, http.StatusNoContent, resp.code, resp.body)

	body := metrics()
	assert.Contains(t, body, `slophaus_ui_events_total{action="approve",count_bucket="",event="steward_answer",filter="",has_text="",outcome="",screen="ticket"} 1`)
	assert.Contains(t, body, `slophaus_ui_events_total{action="open_form",count_bucket="",event="new_ticket",filter="",has_text="",outcome="",screen="board"} 1`, "an unknown attribute is ignored, the event still counted")
	assert.Contains(t, body, `slophaus_ui_screen_seconds_sum{screen="board"} 15`)
	assert.NotContains(t, body, "made_up_event")
	assert.NotContains(t, body, "example.com")
	assert.NotContains(t, body, userID.String())
	assert.NotContains(t, body, tenant.String())
	assert.NotContains(t, body, sessionID)

	assert.Equal(t, http.StatusUnprocessableEntity, do(t, signedIn(`{"events":[`+strings.Repeat(`{"name":"new_ticket"},`, 51)+`{"name":"new_ticket"}]}`)).code, "a batch past the cap is refused")
}

func TestServe_UIUsageEventsAreRateLimitedPerUser(t *testing.T) {
	t.Parallel()
	b := boot(t, &bootOptions{})
	userID, tenant := dbtest.NewUser(t, b.appDB), dbtest.NewTenant(t, b.appDB)
	signer := auth.NewSigner([]byte(bootJWTSecret), b.cfg.AccessTokenTTL, b.cfg.RefreshTokenTTL)
	csrfTokens, err := auth.NewCSRFTokens([]byte(bootJWTSecret))
	require.NoError(t, err)
	access, err := signer.IssueSessionAccessToken(userID, tenant, &auth.Session{ID: "s"})
	require.NoError(t, err)
	send := func() int {
		return do(t, &request{method: http.MethodPost, url: b.base + usagePath, body: `{"events":[]}`, header: map[string]string{
			"Cookie": authn.SessionCookie + "=" + access, "Origin": b.base, authn.CSRFHeader: csrfTokens.Token("s"),
		}}).code
	}
	var last int
	for range 61 {
		last = send()
	}
	assert.Equal(t, http.StatusTooManyRequests, last)
}
