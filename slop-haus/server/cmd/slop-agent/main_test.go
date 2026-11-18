package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/openai/openai-go/v2"
	"github.com/openai/openai-go/v2/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
)

// This is the only test that puts a real model in front of the MCP surface.
// Every other test calls the tools directly, so all of them keep passing if
// a tool is renamed or its description stops making sense.
//
// What it does catch: a model cannot find checkin or ticket_next from the
// descriptions alone; the MCP endpoint is unreachable with a valid agent
// token; the SessionStart briefing cannot be fetched or is empty; or the
// run leaves the board inconsistent -- a ticket stuck in_progress, a lease
// still held by an agent that has walked away.
//
// What it does not catch, and this was measured rather than assumed: a bad
// parameter on a tool the scenario never reaches. An optional `since`
// cursor on chat_read really did cost five of twelve turns to a model
// fishing for messages that were not there -- but re-introducing it with
// this scenario still passes in six turns, because the agent is given a
// clear reason to release and never goes near chat. The turn ceiling below
// catches gross flailing, not a subtle knob on an untouched tool.
//
// It is opt-in: it costs real API calls and needs a running server. A model
// will not repeat itself, so it asserts the shape of the outcome, never the
// transcript.
//
//	./dev_env.sh up -d
//	OPENROUTER_API_KEY=... SLOP_HAUS_URL=http://localhost:8190 \
//	  go test ./cmd/slop-agent -run TestLiveAgent -v
//
// SLOP_HAUS_BOARD_URL is where the board is served (the api's
// PUBLIC_BASE_URL; the dev stack's frontend by default): the signed-in
// user's writes are refused from any other origin.
func TestLiveAgent_CanDriveTheToolSurface(t *testing.T) {
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	baseURL := os.Getenv("SLOP_HAUS_URL")
	if apiKey == "" || baseURL == "" {
		t.Skip("set OPENROUTER_API_KEY and SLOP_HAUS_URL against a running server to run this")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	h := &harness{t: t, baseURL: baseURL, boardOrigin: envOr("SLOP_HAUS_BOARD_URL", "http://localhost:5273"), client: &http.Client{Jar: jar}}
	h.requireReachable(ctx)
	// The seeded dev account (docker-compose-dev.yaml): registering an
	// agent and filing a ticket are a signed-in user's to do.
	h.signIn(ctx, envOr("DEV_USER_EMAIL", "dev@slop-haus.local"), envOr("DEV_USER_PASSWORD", "slop-haus-dev-password"))

	agentName := fmt.Sprintf("live-test-%d", time.Now().UnixNano())
	agentID := h.createAgent(ctx, agentName)
	// Agents no longer carry a static token: the same OAuth device flow a
	// real workspace's token.sh walks is how this harness gets one to
	// drive the MCP endpoint with.
	agentToken := h.authorizeAgentByDeviceFlow(ctx, agentID, agentName)

	// Put work in the queue so there is something to find, but do not
	// assert on *this* ticket: the agent takes whatever the server offers
	// next, which on a shared dev stack is usually an older one. An
	// earlier version checked the ticket it had just filed and was
	// therefore asserting on a row the agent never touched.
	h.createTicket(ctx, "Live agent trial", "Pick this up and then hand it back.")

	cfg := config{
		server:   baseURL,
		token:    agentToken,
		apiKey:   apiKey,
		model:    envOr("OPENROUTER_MODEL", "openai/gpt-5.6-luna"),
		maxTurns: 12,
		task: "You have no shell or file-editing tools in this session. " +
			"If a ticket needs code changes you cannot make, say so and release it.",
	}

	session, err := connect(ctx, cfg)
	require.NoError(t, err, "the agent must be able to reach the MCP endpoint with its token")
	defer func() { _ = session.Close() }()

	summary, err := (&agent{cfg: cfg, mcp: session, llm: openai.NewClient(
		option.WithBaseURL(openRouterBaseURL), option.WithAPIKey(cfg.apiKey),
	)}).work(ctx)
	require.NoError(t, err, "tools called: %v", summary.ToolCalls)

	// What a model must be able to work out from the descriptions alone.
	assert.True(t, summary.Called("checkin"),
		"the briefing tells it to check in first; tools called: %v", summary.ToolCalls)
	assert.True(t, summary.Called("ticket_next"),
		"it must find the tool that takes work; tools called: %v", summary.ToolCalls)

	// A generous ceiling, not a benchmark. It is here to catch flailing --
	// the failure mode where a model can technically reach every tool but
	// the descriptions send it round in circles.
	assert.LessOrEqual(t, summary.Turns, 10,
		"took %d turns, which usually means it is going in circles: %v",
		summary.Turns, summary.ToolCalls)

	// Whatever it decided to do, it must not walk away still holding work.
	// Scoped to this run's own agent, because the dev stack is shared and
	// another agent legitimately holding a ticket is not this test's
	// business.
	assert.Nil(t, h.leaseFor(ctx, agentName),
		"the agent stopped while still holding a ticket")
}

// harness drives the REST API as a signed-in user, whose session lives in
// client's cookie jar. Its writes are made as the board's page makes them
// (see authn.BrowserGuard): from the board's origin, with the session's
// CSRF token.
type harness struct {
	t           *testing.T
	baseURL     string
	boardOrigin string
	csrfToken   string
	client      *http.Client
}

func (h *harness) signIn(ctx context.Context, email, password string) {
	h.t.Helper()
	encoded, err := json.Marshal(map[string]string{"email": email, "password": password})
	require.NoError(h.t, err)
	var profile struct {
		CSRFToken string `json:"csrf_token"`
	}
	h.do(ctx, http.MethodPost, "/api/auth/login", bytes.NewReader(encoded), "", &profile, http.StatusOK)
	require.NotEmpty(h.t, profile.CSRFToken)
	h.csrfToken = profile.CSRFToken
}

func (h *harness) requireReachable(ctx context.Context) {
	h.t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.baseURL+"/healthz", nil)
	require.NoError(h.t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(h.t, err, "no server at %s -- run ./dev_env.sh up -d", h.baseURL)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(h.t, http.StatusOK, resp.StatusCode)
}

// createAgent registers an agent and returns its id. There is no token to
// hand back any more: an agent is authorized through OAuth's device flow,
// which authorizeAgentByDeviceFlow below walks the same way a workspace's
// token.sh does.
func (h *harness) createAgent(ctx context.Context, name string) string {
	h.t.Helper()
	var out struct {
		ID string `json:"id"`
	}
	h.post(ctx, "/api/agents", map[string]string{"name": name}, "", &out)
	require.NotEmpty(h.t, out.ID)
	return out.ID
}

// authorizeAgentByDeviceFlow walks RFC 8628's device flow end to end: it
// starts the authorization as the workspace client would, approves it as
// the signed-in user would on the board's device page (naming the agent
// created above), and polls for the resulting access token the way
// token.sh does. This is the one piece of onboarding a real workspace
// needs a human's browser for; here the harness plays both parts because
// it already holds a session as the tenant's only user.
func (h *harness) authorizeAgentByDeviceFlow(ctx context.Context, agentID, agentName string) string {
	h.t.Helper()

	deviceCode, userCode := h.startDeviceAuthorization(ctx)
	h.approveDevice(ctx, userCode, agentID)
	return h.pollForAccessToken(ctx, deviceCode)
}

func (h *harness) startDeviceAuthorization(ctx context.Context) (deviceCode, userCode string) {
	h.t.Helper()
	form := url.Values{"client_id": {oauth.WorkspaceClientID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+"/oauth/device_authorization",
		bytes.NewReader([]byte(form.Encode())))
	require.NoError(h.t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(h.t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(h.t, err)
	require.Equal(h.t, http.StatusOK, resp.StatusCode, "device_authorization: %s", body)

	var out struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
	}
	require.NoError(h.t, json.Unmarshal(body, &out))
	require.NotEmpty(h.t, out.DeviceCode)
	require.NotEmpty(h.t, out.UserCode)
	return out.DeviceCode, out.UserCode
}

// approveDevice is the board's device page saying yes: a signed-in user,
// naming which of their tenant's agents this client may act as.
func (h *harness) approveDevice(ctx context.Context, userCode, agentID string) {
	h.t.Helper()
	h.do(ctx, http.MethodPost, "/api/oauth/device/lookup", jsonBody(h.t, map[string]string{"user_code": userCode}),
		"", nil, http.StatusOK)
	h.do(ctx, http.MethodPost, "/api/oauth/device/decision",
		jsonBody(h.t, map[string]any{"user_code": userCode, "approve": true, "agent_id": agentID}),
		"", nil, http.StatusNoContent)
}

// pollForAccessToken polls the token endpoint until the device code is
// approved, backing off on slow_down exactly as token.sh does, and returns
// the access token.
func (h *harness) pollForAccessToken(ctx context.Context, deviceCode string) string {
	h.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	interval := 200 * time.Millisecond
	for {
		require.True(h.t, time.Now().Before(deadline), "timed out waiting for device authorization")

		form := url.Values{
			"grant_type":  {oauth.GrantDeviceCode},
			"device_code": {deviceCode},
			"client_id":   {oauth.WorkspaceClientID},
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+"/oauth/token",
			bytes.NewReader([]byte(form.Encode())))
		require.NoError(h.t, err)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(h.t, err)
		body, err := io.ReadAll(resp.Body)
		require.NoError(h.t, err)
		_ = resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			var out struct {
				AccessToken string `json:"access_token"`
			}
			require.NoError(h.t, json.Unmarshal(body, &out))
			require.NotEmpty(h.t, out.AccessToken)
			return out.AccessToken
		}

		var oerr struct {
			Error string `json:"error"`
		}
		require.NoError(h.t, json.Unmarshal(body, &oerr))
		require.Contains(h.t, []string{"authorization_pending", "slow_down"}, oerr.Error,
			"device token poll: %s: %s", resp.Status, body)
		if oerr.Error == "slow_down" {
			interval += 5 * time.Second
		}
		time.Sleep(interval)
	}
}

func jsonBody(t *testing.T, v any) io.Reader {
	t.Helper()
	encoded, err := json.Marshal(v)
	require.NoError(t, err)
	return bytes.NewReader(encoded)
}

func (h *harness) createTicket(ctx context.Context, title, body string) int64 {
	h.t.Helper()
	var out struct {
		ID int64 `json:"id"`
	}
	h.post(ctx, "/api/tickets", map[string]any{"title": title, "body": body}, "", &out)
	require.NotZero(h.t, out.ID)
	return out.ID
}

// leaseFor reads the named agent's row from the fleet listing -- the same
// place the human would look to answer "is anything still held".
func (h *harness) leaseFor(ctx context.Context, name string) any {
	h.t.Helper()
	var out struct {
		Agents []struct {
			Name  string `json:"name"`
			Lease any    `json:"lease"`
		} `json:"agents"`
	}
	h.get(ctx, "/api/agents", "", &out)

	for _, a := range out.Agents {
		if a.Name == name {
			return a.Lease
		}
	}
	h.t.Fatalf("agent %q is not in the fleet listing", name)
	return nil
}

func (h *harness) post(ctx context.Context, path string, body any, token string, out any) {
	h.t.Helper()
	encoded, err := json.Marshal(body)
	require.NoError(h.t, err)
	h.do(ctx, http.MethodPost, path, bytes.NewReader(encoded), token, out, http.StatusCreated)
}

func (h *harness) get(ctx context.Context, path, token string, out any) {
	h.t.Helper()
	h.do(ctx, http.MethodGet, path, nil, token, out, http.StatusOK)
}

func (h *harness) do(ctx context.Context, method, path string, body io.Reader, token string, out any, want int) {
	h.t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, h.baseURL+path, body)
	require.NoError(h.t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", h.boardOrigin)
	if h.csrfToken != "" {
		req.Header.Set(authn.CSRFHeader, h.csrfToken)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := h.client.Do(req)
	require.NoError(h.t, err)
	defer func() { _ = resp.Body.Close() }()

	payload, _ := io.ReadAll(resp.Body)
	require.Equal(h.t, want, resp.StatusCode, "%s %s: %s", method, path, payload)
	if out != nil {
		require.NoError(h.t, json.Unmarshal(payload, out))
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
