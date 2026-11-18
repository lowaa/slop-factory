package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/privacy"
)

// Consent is for browsers. These boot the whole api and check it from the
// outside: a visitor can read and set their choices, and nothing else's
// requests -- an agent's, the MCP client's, a script's bearer token --
// ever reaches consent logic.

type consentState struct {
	Purposes []struct {
		ID              string `json:"id"`
		NeedsDecision   bool   `json:"needs_decision"`
		DocumentVersion string `json:"document_version"`
		BasedOn         int64  `json:"based_on"`
	} `json:"purposes"`
	NeedsDecision []string `json:"needs_decision"`
	GPC           bool     `json:"gpc"`
}

func (b *booted) consentState(t *testing.T, header map[string]string) (*consentState, *response) {
	t.Helper()
	resp := do(t, &request{method: http.MethodGet, url: b.base + "/api/privacy/consent", header: header})
	require.Equal(t, http.StatusOK, resp.code, resp.body)
	var st consentState
	require.NoError(t, json.Unmarshal([]byte(resp.body), &st))
	return &st, resp
}

func TestServe_ConsentVisitorSeesPurposesAndChooses(t *testing.T) {
	t.Parallel()
	b := boot(t, &bootOptions{})

	st, resp := b.consentState(t, nil)
	assert.Empty(t, resp.header.Values("Set-Cookie"), "nothing is stored before the visitor decides")
	assert.Equal(t, "no-store", resp.header.Get("Cache-Control"))
	assert.ElementsMatch(t, []string{privacy.PurposeAnalytics, privacy.PurposeMarketing}, st.NeedsDecision)
	var ids []string
	for _, p := range st.Purposes {
		ids = append(ids, p.ID)
	}
	assert.Equal(t, []string{privacy.PurposeNecessary, privacy.PurposeAnalytics, privacy.PurposeMarketing}, ids)

	version := st.Purposes[1].DocumentVersion
	require.NotEmpty(t, version)
	body, err := json.Marshal(map[string]any{"source": "banner", "decisions": []map[string]any{
		{"purpose": "analytics", "granted": false, "document_version": version, "based_on": 0},
		{"purpose": "marketing", "granted": false, "document_version": version, "based_on": 0},
	}})
	require.NoError(t, err)
	resp = do(t, &request{method: http.MethodPut, url: b.base + "/api/privacy/consent", body: string(body),
		header: map[string]string{"Origin": b.base}})
	require.Equal(t, http.StatusOK, resp.code, resp.body)
	cookie := resp.header.Get("Set-Cookie")
	assert.Contains(t, cookie, privacy.CookieName+"=")
	assert.Contains(t, cookie, "HttpOnly")
	assert.Contains(t, cookie, "SameSite=Lax")

	// The cookie is read back: both are decided now.
	st, _ = b.consentState(t, map[string]string{"Cookie": cookie})
	assert.Empty(t, st.NeedsDecision)
}

func TestServe_ConsentGPCDeniesMarketingForAVisitor(t *testing.T) {
	t.Parallel()
	b := boot(t, &bootOptions{})

	st, _ := b.consentState(t, map[string]string{"Sec-GPC": "1"})
	assert.True(t, st.GPC)

	version := st.Purposes[2].DocumentVersion
	body := `{"source":"banner","decisions":[{"purpose":"marketing","granted":true,"document_version":"` + version + `","based_on":0}]}`
	resp := do(t, &request{method: http.MethodPut, url: b.base + "/api/privacy/consent", body: body,
		header: map[string]string{"Origin": b.base, "Sec-GPC": "1"}})
	assert.Equal(t, http.StatusConflict, resp.code, "granting a sale/share purpose under GPC is refused")
	assert.Contains(t, resp.body, "gpc_active")

	// ...and it is the signal that refuses it, not marketing being
	// unavailable: the same grant without it is accepted.
	resp = do(t, &request{method: http.MethodPut, url: b.base + "/api/privacy/consent", body: body,
		header: map[string]string{"Origin": b.base}})
	assert.Equal(t, http.StatusOK, resp.code, resp.body)
}

func (b *booted) consentEventCount(t *testing.T, userID uuid.UUID) (total, merged int) {
	t.Helper()
	row := b.appDB.Global().QueryRow(context.Background(),
		`SELECT count(*), count(*) FILTER (WHERE merged_from_anonymous) FROM global_consent_events WHERE user_id = $1`, userID)
	require.NoError(t, row.Scan(&total, &merged))
	return total, merged
}

// A signed-in person's choices are filed against them, and the choice they
// made while signed out joins their record once, however often the cookie
// is presented again.
func TestServe_ConsentSignedInIsRecordedAndMergesTheCookieOnce(t *testing.T) {
	t.Parallel()
	b := boot(t, &bootOptions{})
	who, token := b.signedInUser(t)
	st, _ := b.consentState(t, nil)
	version := st.Purposes[1].DocumentVersion
	put := func(header map[string]string, purpose string, granted bool) *response {
		body := fmt.Sprintf(`{"source":"settings","decisions":[{"purpose":%q,"granted":%t,"document_version":%q,"based_on":0}]}`, purpose, granted, version)
		return do(t, &request{method: http.MethodPut, url: b.base + "/api/privacy/consent", body: body, header: header})
	}

	// Signed out: analytics is granted, into the cookie.
	anon := put(map[string]string{"Origin": b.base}, "analytics", true)
	require.Equal(t, http.StatusOK, anon.code, anon.body)
	cookie := strings.SplitN(anon.header.Get("Set-Cookie"), ";", 2)[0]
	total, _ := b.consentEventCount(t, who.ID)
	assert.Zero(t, total, "nothing is filed against anyone while signed out")

	// Signed in with a session, the first read merges it.
	session := "slop_haus_session=" + token + "; " + cookie
	st, _ = b.consentState(t, map[string]string{"Cookie": session})
	total, merged := b.consentEventCount(t, who.ID)
	assert.Equal(t, 1, total)
	assert.Equal(t, 1, merged)
	assert.NotContains(t, st.NeedsDecision, "analytics")

	b.consentState(t, map[string]string{"Cookie": session})
	total, _ = b.consentEventCount(t, who.ID)
	assert.Equal(t, 1, total, "the cookie merges once")

	// A decision as the signed-in user is theirs, with a bearer token or not.
	resp := put(map[string]string{"Authorization": "Bearer " + token}, "marketing", false)
	require.Equal(t, http.StatusOK, resp.code, resp.body)
	total, _ = b.consentEventCount(t, who.ID)
	assert.Equal(t, 2, total)
}

// A session whose credential was refused is not the anonymous visitor: the
// write is refused, so the choice is not filed in the cookie and then again
// as the user when the client refreshes and retries.
func TestServe_ConsentRefusesAWriteOnARejectedCredential(t *testing.T) {
	t.Parallel()
	b := boot(t, &bootOptions{})
	st, _ := b.consentState(t, nil)

	body := `{"source":"banner","decisions":[{"purpose":"analytics","granted":false,"document_version":"` + st.Purposes[1].DocumentVersion + `","based_on":0}]}`
	resp := do(t, &request{method: http.MethodPut, url: b.base + "/api/privacy/consent", body: body,
		header: map[string]string{"Origin": b.base, "Cookie": "slop_haus_session=expired-or-forged"}})
	assert.Equal(t, http.StatusForbidden, resp.code, resp.body)
	assert.Contains(t, resp.body, "credential_rejected")
	assert.Empty(t, resp.header.Values("Set-Cookie"))
}

func TestServe_LegalDocumentsArePublicDrafts(t *testing.T) {
	t.Parallel()
	b := boot(t, &bootOptions{})

	for _, kind := range privacy.LegalKinds {
		resp := do(t, &request{method: http.MethodGet, url: b.base + "/api/privacy/documents/" + kind})
		require.Equal(t, http.StatusOK, resp.code, kind+": "+resp.body)
		var doc struct {
			Reviewed bool   `json:"reviewed"`
			Current  bool   `json:"current"`
			Body     string `json:"body"`
		}
		require.NoError(t, json.Unmarshal([]byte(resp.body), &doc))
		assert.False(t, doc.Reviewed, kind)
		assert.True(t, doc.Current, kind)
		assert.NotEmpty(t, doc.Body, kind)
	}
	assert.Equal(t, http.StatusNotFound,
		status(t, &request{method: http.MethodGet, url: b.base + "/api/privacy/documents/health_data_notice"}))
}

// The agents, the MCP client and a user's bearer token are not browsers. To
// show consent is not in their path at all, rather than merely tolerant of
// them, the consent tables are taken away: their requests are unaffected,
// while the one route that does use consent for a signed-in person fails.
func TestServe_BearerAndMCPClientsNeverReachConsent(t *testing.T) {
	t.Parallel()
	logs := &recordingHandler{}
	b := boot(t, &bootOptions{logs: logs})
	agentToken := b.agentToken(t)
	_, userToken := b.signedInUser(t)

	conn, err := pgx.Connect(context.Background(), b.ownerDSN)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	_, err = conn.Exec(context.Background(), `DROP TABLE global_consent_events, global_consent_merge_tokens`)
	require.NoError(t, err)

	gpc := map[string]string{"Sec-GPC": "1"}
	agentREST := do(t, &request{method: http.MethodPost, url: b.base + "/api/claims", body: `{}`, token: agentToken, header: gpc})
	assert.Equal(t, http.StatusOK, agentREST.code, agentREST.body)
	assert.Empty(t, agentREST.header.Values("Set-Cookie"))

	userAPI := do(t, &request{method: http.MethodGet, url: b.base + "/api/auth/me", token: userToken, header: gpc})
	assert.Equal(t, http.StatusOK, userAPI.code, userAPI.body)

	mcp := do(t, &request{method: http.MethodPost, url: b.base + "/mcp", token: agentToken, body: mcpInitialize,
		header: map[string]string{"Accept": "application/json, text/event-stream", "Sec-GPC": "1"}})
	assert.Equal(t, http.StatusOK, mcp.code, mcp.body)
	assert.Empty(t, mcp.header.Values("Set-Cookie"))

	// Consent's middleware fails open, so an answer alone doesn't show it was
	// skipped: it logs when the record can't be read, and does so only on the
	// consent route.
	assert.False(t, logs.has("consent unavailable"), "a bearer client's request reached consent")
	consent := do(t, &request{method: http.MethodGet, url: b.base + "/api/privacy/consent", token: userToken})
	assert.Equal(t, http.StatusInternalServerError, consent.code)
	assert.True(t, logs.has("consent unavailable"), "the consent route needs the tables the others were shown not to")
}

// recordingHandler keeps what is logged, for a test to look through.
type recordingHandler struct {
	mu   sync.Mutex
	msgs []string
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.msgs = append(h.msgs, r.Message)
	return nil
}
func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

func (h *recordingHandler) has(substr string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, m := range h.msgs {
		if strings.Contains(m, substr) {
			return true
		}
	}
	return false
}
