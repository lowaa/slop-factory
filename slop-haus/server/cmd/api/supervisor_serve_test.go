package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
)

// A supervisor's credential on the booted api: it creates a slot, is handed
// that slot's credential, hears the slot's work in one poll, and is refused
// everything else an agent does. The slot's credential then works as a worker.
func TestServe_ASupervisorRunsSlotsAndIsKeptToItsOwnRoutes(t *testing.T) {
	t.Parallel()
	b := boot(t, &bootOptions{})
	who, userToken := b.signedInUser(t)
	capTwo := 2
	token := b.agentTokenFor(t, who, &oauth.Decision{Approve: true, AgentName: "box-1", Kind: "supervisor", SlotCap: &capTwo})

	created := do(t, &request{method: http.MethodPost, url: b.base + "/api/supervisor/slots", token: token, body: `{"name":"slot-1"}`})
	require.Equal(t, http.StatusCreated, created.code, created.body)
	var slot struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(created.body), &slot))
	filed := do(t, &request{method: http.MethodPost, url: b.base + "/api/tickets", token: userToken, body: `{"title":"Headless","paths":[{"path":"server/"}]}`})
	require.Equal(t, http.StatusCreated, filed.code, filed.body)

	t.Run("one poll covers the slots", func(t *testing.T) {
		polled := do(t, &request{method: http.MethodPost, url: b.base + "/api/worker/poll", token: token, body: `{"wait_seconds":0}`})
		require.Equal(t, http.StatusOK, polled.code, polled.body)
		var body struct {
			Events []struct {
				Kind   string `json:"kind"`
				SlotID string `json:"slot_id"`
			} `json:"events"`
		}
		require.NoError(t, json.Unmarshal([]byte(polled.body), &body))
		require.Len(t, body.Events, 1)
		assert.Equal(t, "claimable", body.Events[0].Kind)
		assert.Equal(t, slot.ID, body.Events[0].SlotID)
	})

	t.Run("the supervisor itself can do nothing else an agent does", func(t *testing.T) {
		for _, r := range []request{
			{method: http.MethodPost, url: "/api/claims", body: `{}`},
			{method: http.MethodPost, url: "/api/claims/heartbeat", body: `{}`},
			{method: http.MethodPost, url: "/api/integration_lock"},
			{method: http.MethodGet, url: "/api/worker/state"},
			{method: http.MethodPost, url: "/api/worker/slot-state", body: `{"phase":"idle","phase_age_seconds":1}`},
			{method: http.MethodPost, url: "/hooks/stop", body: `{}`},
			{method: http.MethodPost, url: "/hooks/session-start", body: `{}`},
			{method: http.MethodPost, url: "/hooks/usage", body: `{}`},
			{method: http.MethodGet, url: "/api/workspace/harness_config"},
			{method: http.MethodPost, url: "/api/conductor/poll", body: `{}`},
		} {
			r.url, r.token = b.base+r.url, token
			resp := do(t, &r)
			assert.Equal(t, http.StatusForbidden, resp.code, "%s %s: %s", r.method, r.url, resp.body)
		}
		assert.Equal(t, http.StatusOK, do(t, &request{method: http.MethodGet, url: b.base + "/api/tickets", token: userToken}).code)
	})

	t.Run("it fetches the workspace update script that login installs token.sh from", func(t *testing.T) {
		resp := do(t, &request{method: http.MethodGet, url: b.base + "/api/workspace/update_script", token: token})
		assert.Equal(t, http.StatusOK, resp.code, resp.body)
	})

	t.Run("no MCP tool is its to call", func(t *testing.T) {
		_, err := mcp.NewClient(&mcp.Implementation{Name: "supervisor", Version: "0"}, nil).Connect(context.Background(),
			&mcp.StreamableClientTransport{Endpoint: b.base + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token: token}}}, nil)
		assert.Error(t, err)
	})

	t.Run("the credential it is handed works as the slot", func(t *testing.T) {
		issued := do(t, &request{method: http.MethodPost, url: b.base + "/api/supervisor/slots/" + slot.ID + "/credential", token: token})
		require.Equal(t, http.StatusOK, issued.code, issued.body)
		var tokens oauth.TokenResponse
		require.NoError(t, json.Unmarshal([]byte(issued.body), &tokens))

		claim := do(t, &request{method: http.MethodPost, url: b.base + "/api/claims", token: tokens.AccessToken, body: `{}`})
		assert.Equal(t, http.StatusOK, claim.code, claim.body)
		assert.Equal(t, http.StatusForbidden, do(t, &request{method: http.MethodPost, url: b.base + "/api/supervisor/slots", token: tokens.AccessToken,
			body: `{"name":"slot-2"}`}).code, "a slot is not a supervisor")
	})

	t.Run("disabling the supervisor ends the slot's credential", func(t *testing.T) {
		listed := do(t, &request{method: http.MethodGet, url: b.base + "/api/supervisors", token: userToken})
		require.Equal(t, http.StatusOK, listed.code, listed.body)
		var supervisors struct {
			Supervisors []struct {
				ID string `json:"id"`
			} `json:"supervisors"`
		}
		require.NoError(t, json.Unmarshal([]byte(listed.body), &supervisors))
		require.Len(t, supervisors.Supervisors, 1)
		off := do(t, &request{method: http.MethodPatch, url: b.base + "/api/supervisors/" + supervisors.Supervisors[0].ID, token: userToken, body: `{"disabled":true}`})
		require.Equal(t, http.StatusOK, off.code, off.body)
		assert.Equal(t, http.StatusUnauthorized, do(t, &request{method: http.MethodPost, url: b.base + "/api/supervisor/slots", token: token,
			body: `{"name":"slot-3"}`}).code)
	})
}

// The steward runner's routes are wired on the booted api: before they were,
// every call answered 503 and the steward never ran.
func TestServe_TheStewardRoutesAreWiredForASupervisor(t *testing.T) {
	t.Parallel()
	b := boot(t, &bootOptions{})
	who, _ := b.signedInUser(t)
	capTwo := 2
	token := b.agentTokenFor(t, who, &oauth.Decision{Approve: true, AgentName: "box-1", Kind: "supervisor", SlotCap: &capTwo})

	for _, path := range []string{"/api/supervisor/steward/events", "/api/supervisor/steward/agreement"} {
		resp := do(t, &request{method: http.MethodGet, url: b.base + path, token: token})
		assert.Equal(t, http.StatusOK, resp.code, "%s: %s", path, resp.body)
	}
}
